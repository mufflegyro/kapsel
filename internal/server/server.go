package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"kapsel/internal/archive"
	"kapsel/internal/assetpath"
	kapselauth "kapsel/internal/auth"
	"kapsel/internal/denorm"
	"kapsel/internal/diskspace"
	"kapsel/internal/download"
	"kapsel/internal/jobs"
	"kapsel/internal/media"
	"kapsel/internal/playlistimport"
	"kapsel/internal/previews"
	"kapsel/internal/search"
	"kapsel/internal/sponsorblock"
	"kapsel/internal/storage"
	"kapsel/internal/taimport"
	"kapsel/internal/updater"
	"kapsel/internal/web"
)

const maxSearchQueryLength = 512
const maxLoginPayloadBytes = 4 * 1024
const maxDownloadPayloadBytes = 4 * 1024
const maxChannelPayloadBytes = 4 * 1024
const maxTubeArchivistImportPayloadBytes = 4 * 1024

// maxPlaylistCSVUploadBytes bounds a playlist CSV upload (multipart). Playlist
// exports are small, but leave headroom for large playlists.
const maxPlaylistCSVUploadBytes = 8 * 1024 * 1024

// maxPlaylistImportPayloadBytes bounds the JSON body of a playlist URL import.
const maxPlaylistImportPayloadBytes = 4 * 1024
const maxPlaybackProgressSeconds = 7 * 24 * 60 * 60
const maxPlaybackProgressPayloadBytes = 1024
const maxKeepForeverPayloadBytes = 1024
const maxBodylessPayloadBytes = 1
const maxDescriptionChapters = 200
const maxChapterDescriptionBytes = 256 * 1024
const maxChapterDescriptionLines = 4000
const sponsorBlockFailureBackoff = 5 * time.Minute
const securityHeadersCSP = "default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; media-src 'self' blob:; connect-src 'self' ws: wss:; form-action 'self'"

type Option func(*config)

type config struct {
	db                     *sql.DB
	jobs                   *jobs.Store
	media                  http.Handler
	mediaSigner            *media.Signer
	mediaURLTTL            time.Duration
	importRoot             string
	dataRoot               string
	mediaRoot              string
	minFreeSpaceBytes      uint64
	stat                   diskspace.StatFunc
	storageStatus          bool
	ytdlpPath              string
	ytdlpRunner            download.Runner
	ytdlpStatus            bool
	auth                   *kapselauth.Manager
	loginLimiter           *loginLimiter
	settingsDiagnostics    SettingsDiagnostics
	settingsStatus         bool
	supportedSchemaVersion int
	sponsorBlockClient     sponsorBlockClient
	updater                updateService
}

// updateService is the subset of the updater the web API exposes. The
// updater package implements it; tests substitute a stub.
type updateService interface {
	Status(ctx context.Context) (updater.StatusSummary, error)
	CheckNow(ctx context.Context) (jobs.Job, bool, error)
	Approve(ctx context.Context, id int64, approvedBy string) (updater.Offer, jobs.Job, bool, error)
	Dismiss(ctx context.Context, id int64) (updater.Offer, error)
	Enabled() bool
}

type sponsorBlockClient interface {
	SponsorSegments(context.Context, string) ([]sponsorblock.Segment, error)
}

type sponsorBlockFailureCache struct {
	mu       sync.Mutex
	backoff  time.Duration
	blocked  map[string]time.Time
	inflight map[string]chan struct{}
}

func newSponsorBlockFailureCache(backoff time.Duration) *sponsorBlockFailureCache {
	return &sponsorBlockFailureCache{backoff: backoff, blocked: make(map[string]time.Time), inflight: make(map[string]chan struct{})}
}

func (c *sponsorBlockFailureCache) begin(key string) (<-chan struct{}, bool) {
	if c == nil || key == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.pruneLocked(now)
	until, ok := c.blocked[key]
	if ok && now.Before(until) {
		return nil, true
	}
	if done, ok := c.inflight[key]; ok {
		return done, false
	}
	done := make(chan struct{})
	c.inflight[key] = done

	return nil, false
}

func (c *sponsorBlockFailureCache) finish(key string, failed bool) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if failed {
		c.blocked[key] = now.Add(c.backoff)
	} else {
		delete(c.blocked, key)
	}
	if done, ok := c.inflight[key]; ok {
		delete(c.inflight, key)
		close(done)
	}
	c.pruneLocked(now)
}

func (c *sponsorBlockFailureCache) pruneLocked(now time.Time) {
	for key, until := range c.blocked {
		if !now.Before(until) {
			delete(c.blocked, key)
		}
	}
}

type SettingsDiagnostics struct {
	Addr                         string
	AuthMode                     string
	DataDir                      string
	DBPath                       string
	ImportRoot                   string
	MediaRoot                    string
	MediaSigningSecretConfigured bool
	AuthenticationConfigured     bool
	SessionSecretConfigured      bool
	MediaURLTTL                  time.Duration
	MinFreeSpaceBytes            uint64
	PreviewsEnabled              bool
	SponsorBlockEnabled          bool
	FFMPEGPath                   string
	YTDLPPath                    string
}

func WithDatabase(db *sql.DB) Option {
	return func(config *config) {
		config.db = db
	}
}

func WithJobs(store *jobs.Store) Option {
	return func(config *config) {
		config.jobs = store
	}
}

func WithMedia(root string, signer media.Signer) Option {
	return func(config *config) {
		config.media = media.NewHandler(root, signer)
		config.mediaSigner = &signer
		config.mediaRoot = root
	}
}

func WithMediaURLTTL(ttl time.Duration) Option {
	return func(config *config) {
		config.mediaURLTTL = ttl
	}
}

func WithImportRoot(root string) Option {
	return func(config *config) {
		config.importRoot = root
	}
}

func WithYTDLPDiagnostics(path string, runner download.Runner) Option {
	return func(config *config) {
		config.ytdlpPath = path
		config.ytdlpRunner = runner
		config.ytdlpStatus = true
	}
}

func WithStorageDiagnostics(dataRoot string, mediaRoot string, minFreeSpaceBytes uint64, stat diskspace.StatFunc) Option {
	return func(config *config) {
		config.dataRoot = dataRoot
		config.mediaRoot = mediaRoot
		config.minFreeSpaceBytes = minFreeSpaceBytes
		config.stat = stat
		config.storageStatus = true
	}
}

func WithSettingsDiagnostics(settings SettingsDiagnostics) Option {
	return func(config *config) {
		config.settingsDiagnostics = settings
		config.settingsStatus = true
	}
}

func WithSupportedSchemaVersion(version int) Option {
	return func(config *config) {
		config.supportedSchemaVersion = version
	}
}

func WithAuth(manager *kapselauth.Manager) Option {
	return func(config *config) {
		config.auth = manager
	}
}

func WithSponsorBlockClient(client sponsorBlockClient) Option {
	return func(config *config) {
		config.sponsorBlockClient = client
	}
}

func WithUpdater(service updateService) Option {
	return func(config *config) {
		config.updater = service
	}
}

func NewHandler(options ...Option) http.Handler {
	var config config
	for _, option := range options {
		option(&config)
	}
	if config.mediaURLTTL == 0 {
		config.mediaURLTTL = 24 * time.Hour
	}
	if config.auth != nil && config.auth.Enabled() && config.loginLimiter == nil {
		config.loginLimiter = newLoginLimiter()
	}
	mediaURLs := mediaURLBuilder{root: config.mediaRoot, signer: config.mediaSigner, ttl: config.mediaURLTTL}
	sponsorFailures := newSponsorBlockFailureCache(sponsorBlockFailureBackoff)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", health)
	if config.auth != nil {
		mux.HandleFunc("GET /api/session", getSession(config.auth))
		mux.HandleFunc("POST /api/login", login(config.auth, config.loginLimiter))
		mux.HandleFunc("POST /api/logout", logout(config.auth))
	}
	if config.db != nil || config.mediaRoot != "" || config.ytdlpStatus || config.storageStatus {
		mux.HandleFunc("GET /api/diagnostics/readiness", requireAuth(config, diagnosticsReadiness(config)))
	}
	if config.settingsStatus {
		mux.HandleFunc("GET /api/settings", requireAuth(config, getSettings(config)))
	}
	if config.db != nil {
		mux.HandleFunc("GET /api/channels", requireAuth(config, listChannels(config.db, mediaURLs)))
		mux.HandleFunc("GET /api/channels/{id}/videos", requireAuth(config, listChannelVideos(config.db, mediaURLs)))
		mux.HandleFunc("GET /api/channels/{id}", requireAuth(config, getChannel(config.db, mediaURLs)))
		mux.HandleFunc("PUT /api/channels/{id}/subscription", requireAuth(config, updateChannelSubscription(config.db)))
		mux.HandleFunc("DELETE /api/channels/{id}", requireAuth(config, deleteChannel(config.db)))
		mux.HandleFunc("GET /api/playlists", requireAuth(config, listPlaylists(config.db)))
		mux.HandleFunc("GET /api/playlists/{id}", requireAuth(config, getPlaylist(config.db)))
		mux.HandleFunc("DELETE /api/playlists/{id}", requireAuth(config, deletePlaylist(config.db)))
		mux.HandleFunc("GET /api/playlists/{id}/videos", requireAuth(config, listPlaylistVideos(config.db, mediaURLs)))
		mux.HandleFunc("GET /api/search", requireAuth(config, searchDocuments(config.db, mediaURLs)))
		mux.HandleFunc("GET /api/home/videos", requireAuth(config, listHomeVideos(config.db, mediaURLs)))
		mux.HandleFunc("GET /api/videos", requireAuth(config, listVideos(config.db, mediaURLs)))
		mux.HandleFunc("GET /api/videos/{id}/progress", requireAuth(config, getVideoProgress(config.db)))
		mux.HandleFunc("PUT /api/videos/{id}/progress", requireAuth(config, updateVideoProgress(config.db)))
		mux.HandleFunc("PUT /api/videos/{id}/keep-forever", requireAuth(config, updateVideoKeepForever(config.db)))
		mux.HandleFunc("DELETE /api/videos/{id}/media", requireAuth(config, deleteVideoMedia(config.db, config.mediaRoot)))
		mux.HandleFunc("GET /api/videos/{id}/up-next", requireAuth(config, listUpNextVideos(config.db, mediaURLs)))
		mux.HandleFunc("GET /api/videos/{id}/comments", requireAuth(config, listVideoComments(config.db)))
		mux.HandleFunc("GET /api/videos/{id}/timeline-preview.vtt", requireAuth(config, getTimelinePreviewVTT(config.db, mediaURLs)))
		mux.HandleFunc("GET /api/videos/{id}/chapters.vtt", requireAuth(config, getVideoChaptersVTT(config.db)))
		mux.HandleFunc("GET /api/videos/{id}/sponsor-segments", requireAuth(config, getVideoSponsorSegments(config.db, config.sponsorBlockClient, sponsorFailures)))
		mux.HandleFunc("GET /api/videos/{id}", requireAuth(config, getVideo(config.db, mediaURLs, config.jobs, config.sponsorBlockClient, sponsorFailures)))
	}
	if config.jobs != nil {
		mux.HandleFunc("GET /api/live", requireAuth(config, liveJobs(config.jobs)))
		mux.HandleFunc("GET /api/jobs", requireAuth(config, listJobs(config.jobs)))
		mux.HandleFunc("GET /api/jobs/{id}", requireAuth(config, getJob(config.jobs)))
		mux.HandleFunc("GET /api/diagnostics/errors", requireAuth(config, diagnosticErrors(config.jobs)))
		mux.HandleFunc("POST /api/jobs/{id}/cancel", requireAuth(config, cancelJob(config.jobs)))
		mux.HandleFunc("POST /api/jobs/{id}/retry", requireAuth(config, retryJob(config.jobs)))
		mux.HandleFunc("POST /api/channels", requireAuth(config, createChannel(config.jobs)))
		mux.HandleFunc("POST /api/downloads", requireAuth(config, createDownload(config.jobs)))
		if config.db != nil {
			mux.HandleFunc("POST /api/channels/{id}/scan", requireAuth(config, createChannelScan(config.db, config.jobs)))
			mux.HandleFunc("POST /api/videos/{id}/download", requireAuth(config, createCatalogVideoDownload(config.db, config.jobs, mediaURLs)))
			mux.HandleFunc("POST /api/playlists/import", requireAuth(config, createPlaylistCSVImport(config.db, config.jobs)))
			mux.HandleFunc("POST /api/playlists/import-url", requireAuth(config, createPlaylistURLImport(config.db, config.jobs)))
		}
		if config.importRoot != "" {
			mux.HandleFunc("POST /api/imports/tubearchivist", requireAuth(config, createTubeArchivistImport(config.jobs, config.importRoot)))
		}
	}
	if config.updater != nil {
		mux.HandleFunc("GET /api/updates", requireAuth(config, getUpdates(config)))
		mux.HandleFunc("POST /api/updates/check", requireAuth(config, createUpdateCheck(config)))
		mux.HandleFunc("POST /api/updates/{id}/approve", requireAuth(config, approveUpdate(config)))
		mux.HandleFunc("POST /api/updates/{id}/dismiss", requireAuth(config, dismissUpdate(config)))
	}
	mux.HandleFunc("GET /api/", http.NotFound)
	if config.media != nil {
		mux.Handle("GET /media/{path...}", config.media)
	}
	mux.Handle("GET /", frontend(web.Static()))

	return cors(securityHeaders(mux))
}

// corsAllowedOrigins are the browser origins allowed to read /api responses
// cross-origin — the pages where the Yummle save userscript runs (YouTube).
// The Origin header is echoed only for these, so arbitrary websites cannot
// read archive data from a browser pointed at this server.
func corsAllowedOrigin(origin string) string {
	switch strings.ToLower(strings.TrimSuffix(origin, "/")) {
	case "https://www.youtube.com", "https://m.youtube.com", "https://www.youtube-nocookie.com":
		return origin
	}
	return ""
}

// cors answers the CORS preflight and adds Access-Control-Allow-Origin for
// /api/* requests from allowlisted origins (the userscript's enqueue call).
// OPTIONS preflights are answered before the mux so unknown routes still get
// a valid preflight response; non-allowlisted origins and non-API paths get
// no CORS headers at all.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		allowed := corsAllowedOrigin(origin)
		if allowed == "" {
			next.ServeHTTP(w, r)
			return
		}
		header := w.Header()
		header.Set("Access-Control-Allow-Origin", allowed)
		header.Add("Vary", "Origin")
		if r.Method == http.MethodOptions {
			header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			header.Set("Access-Control-Allow-Headers", "Content-Type, Accept")
			header.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "same-origin")
		header.Set("Content-Security-Policy", securityHeadersCSP)

		next.ServeHTTP(w, r)
	})
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "OK")
}

type sessionResponse struct {
	AuthEnabled   bool   `json:"auth_enabled"`
	Configured    bool   `json:"configured"`
	Authenticated bool   `json:"authenticated"`
	Username      string `json:"username,omitempty"`
	LoginRequired bool   `json:"login_required"`
}

func getSession(manager *kapselauth.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username, ok := manager.AuthenticatedUser(r)
		writeJSON(w, http.StatusOK, sessionResponse{
			AuthEnabled:   manager.Enabled(),
			Configured:    manager.Configured(),
			Authenticated: ok,
			Username:      username,
			LoginRequired: manager.Enabled() && !ok,
		})
	}
}

func login(manager *kapselauth.Manager, limiter *loginLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		client := clientKey(r)
		if limiter != nil && !limiter.Allow(client, time.Now()) {
			http.Error(w, "too many login attempts", http.StatusTooManyRequests)
			return
		}
		var payload struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := decodeJSONPayload(w, r, maxLoginPayloadBytes, &payload); err != nil {
			http.Error(w, "invalid login payload", http.StatusBadRequest)
			return
		}
		if !manager.VerifyLogin(payload.Username, payload.Password) {
			if limiter != nil {
				limiter.RecordFailure(client, time.Now())
			}
			http.Error(w, "invalid username or password", http.StatusUnauthorized)
			return
		}
		if limiter != nil {
			limiter.Reset(client)
		}
		http.SetCookie(w, manager.SessionCookie(strings.TrimSpace(payload.Username)))
		writeJSON(w, http.StatusOK, sessionResponse{AuthEnabled: manager.Enabled(), Configured: manager.Configured(), Authenticated: true, Username: strings.TrimSpace(payload.Username)})
	}
}

func logout(manager *kapselauth.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireNoBody(w, r) {
			return
		}
		http.SetCookie(w, manager.ClearSessionCookie())
		writeJSON(w, http.StatusOK, sessionResponse{AuthEnabled: manager.Enabled(), Configured: manager.Configured(), Authenticated: !manager.Enabled(), Username: ""})
	}
}

func requireAuth(config config, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if config.auth == nil || !config.auth.Enabled() {
			next(w, r)
			return
		}
		if _, ok := config.auth.AuthenticatedUser(r); !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// requestPathID parses the {id} path segment as a database row id.
func requestPathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
}

func decodeJSONPayload(w http.ResponseWriter, r *http.Request, maxBytes int64, payload any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(payload); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}

		return errors.New("invalid trailing JSON")
	}

	return nil
}

func requireNoBody(w http.ResponseWriter, r *http.Request) bool {
	if r.Body == nil || r.Body == http.NoBody {
		return true
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodylessPayloadBytes))
	if err != nil || len(body) != 0 {
		http.Error(w, "request body is not allowed", http.StatusBadRequest)
		return false
	}

	return true
}

const maxLoginFailures = 5

type loginLimiter struct {
	mu       sync.Mutex
	failures map[string]loginFailure
}

type loginFailure struct {
	Count        int
	BlockedUntil time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{failures: map[string]loginFailure{}}
}

func (l *loginLimiter) Allow(client string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	failure := l.failures[client]
	if failure.BlockedUntil.After(now) {
		return false
	}
	if !failure.BlockedUntil.IsZero() && !failure.BlockedUntil.After(now) {
		delete(l.failures, client)
	}

	return true
}

func (l *loginLimiter) RecordFailure(client string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	failure := l.failures[client]
	failure.Count++
	if failure.Count >= maxLoginFailures {
		failure.BlockedUntil = now.Add(time.Minute)
	}
	l.failures[client] = failure
}

func (l *loginLimiter) Reset(client string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, client)
}

func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}

	return "local"
}

type readinessResponse struct {
	Status    string                     `json:"status"`
	Database  *databaseReadinessStatus   `json:"database,omitempty"`
	MediaRoot *filesystemReadinessStatus `json:"media_root,omitempty"`
	YTDLP     *download.YTDLPStatus      `json:"yt_dlp,omitempty"`
	Storage   *diskspace.Report          `json:"storage,omitempty"`
}

type databaseReadinessStatus struct {
	OK                     bool   `json:"ok"`
	Connected              bool   `json:"connected"`
	SchemaVersion          int    `json:"schema_version"`
	SupportedSchemaVersion int    `json:"supported_schema_version"`
	Error                  string `json:"error,omitempty"`
}

type filesystemReadinessStatus struct {
	Path  string `json:"path"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func diagnosticsReadiness(config config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := readinessResponse{Status: "pass"}
		if config.db != nil {
			status := checkDatabaseReadiness(r.Context(), config.db, config.supportedSchemaVersion)
			response.Database = &status
		}
		if config.mediaRoot != "" {
			status := checkFilesystemReadiness(config.mediaRoot)
			response.MediaRoot = &status
		}
		if config.ytdlpStatus {
			status := download.CheckYTDLP(r.Context(), config.ytdlpPath, config.ytdlpRunner)
			response.YTDLP = &status
		}
		if config.storageStatus {
			report := diskspace.NewChecker(config.minFreeSpaceBytes, config.stat).Check(config.dataRoot, config.mediaRoot)
			response.Storage = &report
		}
		response.Status = readinessStatus(response)
		_ = json.NewEncoder(w).Encode(response)
	}
}

func checkDatabaseReadiness(ctx context.Context, db *sql.DB, supportedVersion int) databaseReadinessStatus {
	status := databaseReadinessStatus{}
	status.SupportedSchemaVersion = supportedVersion
	if supportedVersion <= 0 {
		status.Error = "supported database schema version is not configured"
		return status
	}
	if err := db.PingContext(ctx); err != nil {
		status.Error = download.SanitizeDiagnosticText(err.Error())
		return status
	}
	status.Connected = true
	schemaVersion, err := schemaVersion(ctx, db)
	if err != nil {
		status.Error = download.SanitizeDiagnosticText(err.Error())
		return status
	}
	status.SchemaVersion = schemaVersion
	if schemaVersion != status.SupportedSchemaVersion {
		status.Error = fmt.Sprintf("database schema version %d does not match supported version %d", schemaVersion, status.SupportedSchemaVersion)
		return status
	}
	status.OK = true

	return status
}

func schemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
		return 0, err
	}

	return version, nil
}

func checkFilesystemReadiness(path string) filesystemReadinessStatus {
	status := filesystemReadinessStatus{Path: path}
	if strings.TrimSpace(path) == "" {
		status.Error = "path is not configured"
		return status
	}
	root, err := assetpath.ValidateRoot(path)
	if errors.Is(err, assetpath.ErrInvalid) {
		status.Error = "path is not a directory"
		return status
	}
	if errors.Is(err, assetpath.ErrSymlink) {
		status.Error = "path is a symlink"
		return status
	}
	if err != nil {
		status.Error = download.SanitizeDiagnosticText(err.Error())
		return status
	}
	directory, err := os.Open(root)
	if err != nil {
		status.Error = download.SanitizeDiagnosticText(err.Error())
		return status
	}
	_ = directory.Close()
	status.OK = true

	return status
}

func readinessStatus(response readinessResponse) string {
	status := "pass"
	if response.Storage != nil && !response.Storage.OK {
		status = "warn"
		for _, pathStatus := range response.Storage.Paths {
			if pathStatus.Error != "" {
				status = "error"
			}
		}
	}
	if response.Database != nil && !response.Database.OK {
		status = "error"
	}
	if response.MediaRoot != nil && !response.MediaRoot.OK {
		status = "error"
	}
	if response.YTDLP != nil && !response.YTDLP.Available {
		status = "error"
	}

	return status
}

type settingsResponse struct {
	Configuration      settingsConfiguration    `json:"configuration"`
	Checks             []settingsReadinessCheck `json:"checks"`
	YTDLP              *download.YTDLPStatus    `json:"yt_dlp,omitempty"`
	Storage            *diskspace.Report        `json:"storage,omitempty"`
	StorageMaintenance *storage.Summary         `json:"storage_maintenance,omitempty"`
	Updates            *updater.StatusSummary   `json:"updates,omitempty"`
}

type settingsConfiguration struct {
	Addr                         string `json:"addr"`
	AuthMode                     string `json:"auth_mode"`
	DataDir                      string `json:"data_dir"`
	DBPath                       string `json:"db_path"`
	ImportRoot                   string `json:"import_root"`
	MediaRoot                    string `json:"media_root"`
	MediaSigningSecretConfigured bool   `json:"media_signing_configured"`
	AuthenticationConfigured     bool   `json:"authentication_configured"`
	SessionSecretConfigured      bool   `json:"session_secret_configured"`
	MediaURLTTLSeconds           int64  `json:"media_url_ttl_seconds"`
	MinFreeSpaceBytes            uint64 `json:"min_free_space_bytes"`
	PreviewsEnabled              bool   `json:"previews_enabled"`
	SponsorBlockEnabled          bool   `json:"sponsorblock_enabled"`
	FFMPEGPath                   string `json:"ffmpeg_path"`
	YTDLPPath                    string `json:"yt_dlp_path"`
}

type settingsReadinessCheck struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	State   string `json:"state"`
	Summary string `json:"summary"`
	Detail  string `json:"detail,omitempty"`
}

func getSettings(config config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := settingsResponse{
			Configuration: settingsConfigurationFromDiagnostics(config.settingsDiagnostics),
		}
		if config.ytdlpStatus {
			status := download.CheckYTDLP(r.Context(), config.ytdlpPath, config.ytdlpRunner)
			response.YTDLP = &status
		}
		if config.storageStatus {
			report := diskspace.NewChecker(config.minFreeSpaceBytes, config.stat).Check(config.dataRoot, config.mediaRoot)
			response.Storage = &report
		}
		if config.db != nil && config.settingsDiagnostics.MediaRoot != "" {
			report, err := storage.Scan(r.Context(), config.db, storage.Config{DataRoot: config.settingsDiagnostics.DataDir, MediaRoot: config.settingsDiagnostics.MediaRoot, DBPath: config.settingsDiagnostics.DBPath})
			if err == nil {
				response.StorageMaintenance = &report.Summary
			}
		}
		if config.updater != nil {
			if summary, err := config.updater.Status(r.Context()); err == nil {
				response.Updates = &summary
			}
		}
		response.Checks = settingsChecks(config.settingsDiagnostics, response.YTDLP, response.Storage)
		if config.updater != nil && response.Updates != nil {
			// The update state is the most actionable check — a pending offer
			// or a failed release check needs a decision — so it leads the list.
			response.Checks = append([]settingsReadinessCheck{updateSettingsCheck(*response.Updates)}, response.Checks...)
		}

		_ = json.NewEncoder(w).Encode(response)
	}
}

func settingsConfigurationFromDiagnostics(settings SettingsDiagnostics) settingsConfiguration {
	return settingsConfiguration{
		Addr:                         settings.Addr,
		AuthMode:                     settings.AuthMode,
		DataDir:                      settings.DataDir,
		DBPath:                       settings.DBPath,
		ImportRoot:                   settings.ImportRoot,
		MediaRoot:                    settings.MediaRoot,
		MediaSigningSecretConfigured: settings.MediaSigningSecretConfigured,
		AuthenticationConfigured:     settings.AuthenticationConfigured,
		SessionSecretConfigured:      settings.SessionSecretConfigured,
		MediaURLTTLSeconds:           int64(settings.MediaURLTTL.Seconds()),
		MinFreeSpaceBytes:            settings.MinFreeSpaceBytes,
		PreviewsEnabled:              settings.PreviewsEnabled,
		SponsorBlockEnabled:          settings.SponsorBlockEnabled,
		FFMPEGPath:                   settings.FFMPEGPath,
		YTDLPPath:                    settings.YTDLPPath,
	}
}

func settingsChecks(settings SettingsDiagnostics, ytdlp *download.YTDLPStatus, storage *diskspace.Report) []settingsReadinessCheck {
	checks := []settingsReadinessCheck{
		mediaSigningCheck(settings.MediaSigningSecretConfigured),
		authenticationCheck(settings),
		importRootCheck(settings.ImportRoot),
	}
	if ytdlp != nil {
		checks = append(checks, ytdlpSettingsCheck(*ytdlp))
	}
	if storage != nil {
		checks = append(checks, storageSettingsCheck(*storage))
	}
	checks = append(checks, previewSettingsCheck(settings.PreviewsEnabled, settings.FFMPEGPath))

	return checks
}

func mediaSigningCheck(configured bool) settingsReadinessCheck {
	if configured {
		return settingsReadinessCheck{ID: "media_signing", Label: "Media signing", State: "pass", Summary: "Stable media URL signing is configured."}
	}

	return settingsReadinessCheck{ID: "media_signing", Label: "Media signing", State: "warn", Summary: "KAPSEL_MEDIA_SIGNING_SECRET is not set.", Detail: "Kapsel is using a generated per-process signing secret, so existing media URLs stop working after restart."}
}

func authenticationCheck(settings SettingsDiagnostics) settingsReadinessCheck {
	if settings.AuthMode == "disabled" {
		return settingsReadinessCheck{ID: "authentication", Label: "Authentication", State: "warn", Summary: "Authentication is disabled by explicit development mode.", Detail: "KAPSEL_AUTH_MODE=disabled leaves the archive open to anyone who can reach this server."}
	}
	if !settings.AuthenticationConfigured {
		return settingsReadinessCheck{ID: "authentication", Label: "Authentication", State: "error", Summary: "No local auth account is configured.", Detail: "Set KAPSEL_AUTH_USERNAME, KAPSEL_AUTH_PASSWORD_HASH, and KAPSEL_SESSION_SECRET before using the server outside explicit development mode."}
	}
	if !settings.SessionSecretConfigured {
		return settingsReadinessCheck{ID: "authentication", Label: "Authentication", State: "warn", Summary: "Local auth is configured with an ephemeral session secret.", Detail: "Set KAPSEL_SESSION_SECRET so browser sessions survive process restarts."}
	}

	return settingsReadinessCheck{ID: "authentication", Label: "Authentication", State: "pass", Summary: "Local access protection is configured."}
}

func importRootCheck(importRoot string) settingsReadinessCheck {
	importRoot = strings.TrimSpace(importRoot)
	if importRoot == "" {
		return settingsReadinessCheck{ID: "import_root", Label: "Import root", State: "error", Summary: "TubeArchivist API imports are disabled.", Detail: "Set KAPSEL_IMPORT_ROOT to an allowlisted directory before using API-triggered imports."}
	}
	absRoot, err := filepath.Abs(importRoot)
	if err != nil {
		return settingsReadinessCheck{ID: "import_root", Label: "Import root", State: "error", Summary: "Import root cannot be resolved.", Detail: err.Error()}
	}
	if isFilesystemRoot(absRoot) {
		return settingsReadinessCheck{ID: "import_root", Label: "Import root", State: "error", Summary: "Import root is too broad.", Detail: "Do not allow API-triggered imports from the filesystem root."}
	}

	return settingsReadinessCheck{ID: "import_root", Label: "Import root", State: "pass", Summary: "TubeArchivist API imports are confined to the configured root.", Detail: filepath.Clean(importRoot)}
}

func ytdlpSettingsCheck(status download.YTDLPStatus) settingsReadinessCheck {
	if status.Available {
		return settingsReadinessCheck{ID: "yt_dlp", Label: "yt-dlp", State: "pass", Summary: "yt-dlp is available for downloads and channel scans.", Detail: status.Version}
	}

	return settingsReadinessCheck{ID: "yt_dlp", Label: "yt-dlp", State: "error", Summary: "yt-dlp is unavailable.", Detail: status.Error}
}

func storageSettingsCheck(report diskspace.Report) settingsReadinessCheck {
	if report.OK {
		return settingsReadinessCheck{ID: "storage", Label: "Storage", State: "pass", Summary: "Data and media roots have configured free-space headroom."}
	}

	var details []string
	hasError := false
	for _, pathStatus := range report.Paths {
		if pathStatus.Warning != "" {
			details = append(details, pathStatus.Warning)
		}
		if pathStatus.Error != "" {
			hasError = true
			details = append(details, pathStatus.Error)
		}
	}
	state := "warn"
	if hasError {
		state = "error"
	}

	return settingsReadinessCheck{ID: "storage", Label: "Storage", State: state, Summary: "Storage readiness needs attention.", Detail: strings.Join(details, "\n")}
}

func previewSettingsCheck(enabled bool, ffmpegPath string) settingsReadinessCheck {
	if !enabled {
		return settingsReadinessCheck{ID: "timeline_previews", Label: "Timeline previews", State: "pass", Summary: "Timeline previews are disabled, so ffmpeg is optional."}
	}
	if strings.TrimSpace(ffmpegPath) == "" {
		return settingsReadinessCheck{ID: "timeline_previews", Label: "Timeline previews", State: "warn", Summary: "Timeline previews are enabled without an ffmpeg path.", Detail: "Set KAPSEL_FFMPEG_PATH before generating previews."}
	}

	return settingsReadinessCheck{ID: "timeline_previews", Label: "Timeline previews", State: "pass", Summary: "Timeline previews are enabled with an ffmpeg path configured.", Detail: ffmpegPath}
}

func updateSettingsCheck(summary updater.StatusSummary) settingsReadinessCheck {
	if !summary.UpdateEnabled {
		return settingsReadinessCheck{ID: "updates", Label: "Updates", State: "pass", Summary: "GitHub release checks are disabled.", Detail: "Set KAPSEL_UPDATE_CHECK_INTERVAL to a duration like 24h to enable update checks."}
	}
	if summary.Pending != nil {
		return settingsReadinessCheck{ID: "updates", Label: "Updates", State: "warn", Summary: "Release " + summary.Pending.Version + " is awaiting your approval.", Detail: "Approve or dismiss the update from the Updates panel."}
	}
	if summary.LastCheck != nil && summary.LastCheck.Status != string(jobs.StatusSucceeded) && summary.LastCheck.Status != "" {
		return settingsReadinessCheck{ID: "updates", Label: "Updates", State: "warn", Summary: "The latest release check did not succeed.", Detail: summary.LastCheck.Detail}
	}

	return settingsReadinessCheck{ID: "updates", Label: "Updates", State: "pass", Summary: "Release checks run against " + summary.Repo + ".", Detail: "Running version " + summary.CurrentVersion + "."}
}

func isFilesystemRoot(value string) bool {
	cleaned := filepath.Clean(value)
	root := filepath.VolumeName(cleaned) + string(os.PathSeparator)

	return cleaned == root
}

func getUpdates(config config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		summary, err := config.updater.Status(r.Context())
		if err != nil {
			http.Error(w, "could not read update state", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, summary)
	}
}

func createUpdateCheck(config config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		job, created, err := config.updater.CheckNow(r.Context())
		if err != nil {
			http.Error(w, "could not queue the release check", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusAccepted, updateCheckResponse{Job: publicJobResponse(job), Created: created})
	}
}

func approveUpdate(config config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := requestPathID(r)
		if err != nil {
			http.Error(w, "invalid update id", http.StatusBadRequest)
			return
		}
		approvedBy := "archive-admin"
		if config.auth != nil {
			if username, ok := config.auth.AuthenticatedUser(r); ok && username != "" {
				approvedBy = username
			}
		}
		offer, job, created, err := config.updater.Approve(r.Context(), id, approvedBy)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, updater.ErrOfferNotFound) {
				status = http.StatusNotFound
			} else if errors.Is(err, updater.ErrOfferNotPending) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		jobResponse := publicJobResponse(job)
		writeJSON(w, http.StatusOK, updateApprovalResponse{Offer: offer, Job: &jobResponse, JobCreated: created})
	}
}

func dismissUpdate(config config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := requestPathID(r)
		if err != nil {
			http.Error(w, "invalid update id", http.StatusBadRequest)
			return
		}
		offer, err := config.updater.Dismiss(r.Context(), id)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, updater.ErrOfferNotFound) {
				status = http.StatusNotFound
			} else if errors.Is(err, updater.ErrOfferNotPending) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, offer)
	}
}

type updateCheckResponse struct {
	Job     jobResponse `json:"job"`
	Created bool        `json:"created"`
}

type updateApprovalResponse struct {
	Offer      updater.Offer `json:"offer"`
	Job        *jobResponse  `json:"job,omitempty"`
	JobCreated bool          `json:"job_created"`
}

func createDownload(store *jobs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var payload download.Payload
		if err := decodeJSONPayload(w, r, maxDownloadPayloadBytes, &payload); err != nil {
			http.Error(w, "invalid download payload", http.StatusBadRequest)
			return
		}
		payload.Origin = ""
		var err error
		payload, err = download.NormalizeDownloadPayload(payload)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		job, err := download.EnqueueDownload(r.Context(), store, payload)
		if err != nil {
			http.Error(w, "failed to enqueue download", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(publicJobResponse(job))
	}
}

func createChannel(store *jobs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var payload download.Payload
		if err := decodeJSONPayload(w, r, maxChannelPayloadBytes, &payload); err != nil {
			http.Error(w, "invalid channel payload", http.StatusBadRequest)
			return
		}
		var err error
		payload.URL, err = download.NormalizeChannelURL(payload.URL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		job, err := download.EnqueueChannelFirst(r.Context(), store, payload)
		if err != nil {
			http.Error(w, "failed to enqueue channel", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(publicJobResponse(job))
	}
}

func createChannelScan(db *sql.DB, store *jobs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireNoBody(w, r) {
			return
		}
		channelID := r.PathValue("id")
		var externalID string
		if err := db.QueryRowContext(r.Context(), "SELECT external_id FROM channels WHERE id = ?", channelID).Scan(&externalID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "failed to load channel", http.StatusInternalServerError)
			return
		}

		payload, err := download.ChannelScanPayloadForExternalID(channelID, externalID)
		if err != nil {
			http.Error(w, "channel cannot be scanned", http.StatusBadRequest)
			return
		}
		job, err := download.EnqueueChannelScan(r.Context(), store, payload)
		if err != nil {
			http.Error(w, "failed to enqueue channel scan", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(publicJobResponse(job))
	}
}

func createCatalogVideoDownload(db *sql.DB, store *jobs.Store, mediaURLs mediaURLBuilder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireNoBody(w, r) {
			return
		}
		var source string
		var externalID string
		var mediaPath string
		if err := db.QueryRowContext(r.Context(), "SELECT source, external_id, media_path FROM videos WHERE id = ?", r.PathValue("id")).Scan(&source, &externalID, &mediaPath); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "failed to load video", http.StatusInternalServerError)
			return
		}
		if mediaURLs.Available(mediaPath) {
			writeJSONError(w, "video is already downloaded", http.StatusConflict)
			return
		}
		if source != "youtube" || externalID == "" {
			writeJSONError(w, "video cannot be downloaded from catalog metadata", http.StatusBadRequest)
			return
		}

		videoURL, err := download.NormalizeDirectVideoURL("https://www.youtube.com/watch?v=" + url.QueryEscape(externalID))
		if err != nil {
			writeJSONError(w, "video cannot be downloaded from catalog metadata", http.StatusBadRequest)
			return
		}
		job, err := download.EnqueueDownload(r.Context(), store, download.Payload{URL: videoURL})
		if err != nil {
			http.Error(w, "failed to enqueue download", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(publicJobResponse(job))
	}
}

func activeDownloadJobForVideo(ctx context.Context, store *jobs.Store, source string, externalID string) (*activeJob, error) {
	if store == nil || source != "youtube" || strings.TrimSpace(externalID) == "" {
		return nil, nil
	}
	videoURL, err := download.NormalizeDirectVideoURL("https://www.youtube.com/watch?v=" + url.QueryEscape(externalID))
	if err != nil {
		return nil, nil
	}
	payloadJSON, err := json.Marshal(download.Payload{URL: videoURL})
	if err != nil {
		return nil, err
	}
	job, ok, err := download.ActiveJobForPayload(ctx, store, string(payloadJSON))
	if err != nil || !ok {
		return nil, err
	}

	return activeJobFromJob(job), nil
}

func activePreviewJobForVideo(ctx context.Context, store *jobs.Store, videoID string) (*activeJob, error) {
	if store == nil || strings.TrimSpace(videoID) == "" {
		return nil, nil
	}
	job, ok, err := previews.ActiveJobForVideo(ctx, store, videoID)
	if err != nil || !ok {
		return nil, err
	}

	return activeJobFromJob(job), nil
}

func loadTimelinePreviewMetadata(ctx context.Context, db *sql.DB, videoID string) (timelinePreviewMetadataRow, error) {
	var row timelinePreviewMetadataRow
	err := db.QueryRowContext(ctx, `
SELECT sprite_path, interval_seconds, frame_width, frame_height, columns, preview_count
FROM video_previews
WHERE video_id = ?`, videoID).Scan(&row.SpritePath, &row.IntervalSeconds, &row.FrameWidth, &row.FrameHeight, &row.Columns, &row.Count)
	if errors.Is(err, sql.ErrNoRows) {
		return timelinePreviewMetadataRow{}, nil
	}

	return row, err
}

func activeJobFromJob(job jobs.Job) *activeJob {
	return &activeJob{
		ID:              job.ID,
		Type:            job.Type,
		Status:          job.Status,
		Progress:        job.Progress,
		Error:           job.Error,
		UpdatedAt:       job.UpdatedAt,
		CancelRequested: job.CancelRequested,
	}
}

func createTubeArchivistImport(store *jobs.Store, importRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if isFilesystemRoot(importRoot) {
			http.Error(w, "import root is too broad", http.StatusForbidden)
			return
		}
		var payload taimport.Payload
		if err := decodeJSONPayload(w, r, maxTubeArchivistImportPayloadBytes, &payload); err != nil {
			http.Error(w, "invalid import payload", http.StatusBadRequest)
			return
		}
		payload, err := taimport.NormalizePayloadForImportRoot(payload, importRoot)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		job, err := taimport.EnqueueJob(r.Context(), store, payload)
		if err != nil {
			http.Error(w, "failed to enqueue import", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(publicJobResponse(job))
	}
}

// playlistImportResponse is the JSON returned by a successful CSV upload.
// playlist_id and title mirror the CLI report so the UI can confirm/refresh.
type playlistImportResponse struct {
	Playlists  int      `json:"playlists"`
	Linked     int      `json:"linked"`
	Missing    int      `json:"missing"`
	Enqueued   int      `json:"enqueued"`
	Skipped    int      `json:"skipped"`
	Errors     []string `json:"errors,omitempty"`
	PlaylistID string   `json:"playlist_id"`
	Title      string   `json:"title"`
}

// createPlaylistCSVImport accepts a single playlist CSV uploaded as
// multipart/form-data and imports it via playlistimport. The playlist title
// and deterministic id are derived from the original uploaded file name so
// re-uploading the same name refreshes that playlist (idempotent, matching
// the CLI's kapsel import-playlists behavior). Missing videos get metadata
// scans so a later re-upload can link them.
func createPlaylistCSVImport(db *sql.DB, store *jobs.Store) http.HandlerFunc {
	if db == nil || store == nil {
		return func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "database or job store unavailable", http.StatusServiceUnavailable)
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxPlaylistCSVUploadBytes)
		if err := r.ParseMultipartForm(maxPlaylistCSVUploadBytes); err != nil {
			http.Error(w, "could not read playlist upload (expected multipart/form-data with a file field)", http.StatusBadRequest)
			return
		}
		defer func() {
			_ = r.MultipartForm.RemoveAll()
		}()

		filePart, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing playlist CSV file (field \"file\")", http.StatusBadRequest)
			return
		}
		defer filePart.Close()

		if header == nil || header.Size == 0 {
			http.Error(w, "playlist CSV is empty", http.StatusBadRequest)
			return
		}
		if header.Size > maxPlaylistCSVUploadBytes {
			http.Error(w, "playlist CSV is too large", http.StatusRequestEntityTooLarge)
			return
		}

		// Preserve the original file name so the playlist title matches what
		// the CLI derives from the file base name (DnB-videos.csv → "DnB-videos")
		// and re-uploading the same name refreshes the same playlist.
		name := filepath.Base(header.Filename)
		if strings.TrimSpace(name) == "" {
			name = "playlist.csv"
		}

		entries, err := playlistimport.Parse(filePart)
		if err != nil {
			http.Error(w, "invalid playlist CSV: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(entries) == 0 {
			http.Error(w, "playlist CSV contains no valid video IDs", http.StatusBadRequest)
			return
		}

		// Import using the original uploaded name so the deterministic playlist
		// id/title match the CLI (idempotent per file name).
		report, err := playlistimport.ImportEntries(r.Context(), db, download.NewPlaylistImportEnqueuer(store), name, entries, playlistimport.ModeMetadataScan)
		if err != nil {
			http.Error(w, "playlist import failed: "+err.Error(), http.StatusBadRequest)
			return
		}

		response := playlistImportResponse{
			Playlists: report.Playlists,
			Linked:    report.Linked,
			Missing:   report.Missing,
			Enqueued:  report.Enqueued,
			Skipped:   report.Skipped,
			Errors:    report.Errors,
		}
		// Recompute the deterministic playlist id/title the importer used so
		// the client can refresh/navigate without guessing.
		identity := playlistimport.PlaylistIdentityFromPath(name)
		response.PlaylistID, response.Title = identity.ID, identity.Title

		writeJSON(w, http.StatusOK, response)
	}
}

// playlistURLImportRequest is the JSON body accepted by POST
// /api/playlists/import-url.
type playlistURLImportRequest struct {
	URL string `json:"url"`
}

// createPlaylistURLImport accepts a YouTube playlist link and enqueues a
// playlist_import job that fetches the playlist and imports it asynchronously
// (the CSV upload is synchronous because parsing is local; fetching from
// YouTube needs the sandboxed yt-dlp runner). It returns 202 with the enqueued
// job so the UI can show progress.
func createPlaylistURLImport(db *sql.DB, store *jobs.Store) http.HandlerFunc {
	if db == nil || store == nil {
		return func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "database or job store unavailable", http.StatusServiceUnavailable)
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxPlaylistImportPayloadBytes)
		var request playlistURLImportRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "could not read playlist link (expected a JSON body with a url field)", http.StatusBadRequest)
			return
		}
		if _, _, err := download.NormalizePlaylistURL(request.URL); err != nil {
			http.Error(w, "invalid YouTube playlist link: "+err.Error(), http.StatusBadRequest)
			return
		}

		job, err := download.EnqueuePlaylistImport(r.Context(), store, download.PlaylistImportPayload{URL: request.URL})
		if err != nil {
			http.Error(w, "failed to enqueue playlist import: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(publicJobResponse(job))
	}
}

type videoResponse struct {
	ID                string                 `json:"id"`
	Title             string                 `json:"title"`
	Description       string                 `json:"description"`
	PublishedAt       string                 `json:"published_at"`
	ArchivedAt        string                 `json:"archived_at"`
	DurationSeconds   int                    `json:"duration_seconds"`
	ViewCount         int                    `json:"view_count"`
	MediaURL          string                 `json:"media_url,omitempty"`
	ThumbnailURL      string                 `json:"thumbnail_url,omitempty"`
	ThumbnailFallback string                 `json:"thumbnail_fallback"`
	ArchiveState      string                 `json:"archive_state"`
	CanDownload       bool                   `json:"can_download"`
	MembersOnly       bool                   `json:"members_only"`
	KeepForever       bool                   `json:"keep_forever"`
	TimelinePreview   *timelinePreview       `json:"timeline_preview,omitempty"`
	ChaptersVTTURL    string                 `json:"chapters_vtt_url,omitempty"`
	ActiveDownloadJob *activeJob             `json:"active_download_job,omitempty"`
	ActivePreviewJob  *activeJob             `json:"active_preview_job,omitempty"`
	Subtitles         []subtitleTrack        `json:"subtitles,omitempty"`
	SponsorSegments   []sponsorblock.Segment `json:"sponsor_segments,omitempty"`
	Channel           channelInfo            `json:"channel"`
	PositionSeconds   int                    `json:"position_seconds"`
	Watched           bool                   `json:"watched"`
}

type sponsorSegmentsResponse struct {
	Data []sponsorblock.Segment `json:"data"`
}

type activeJob struct {
	ID              string      `json:"id"`
	Type            string      `json:"type"`
	Status          jobs.Status `json:"status"`
	Progress        float64     `json:"progress"`
	Error           string      `json:"error"`
	UpdatedAt       string      `json:"updated_at"`
	CancelRequested bool        `json:"cancel_requested"`
}

type timelinePreviewMetadataRow struct {
	SpritePath      string
	IntervalSeconds int
	FrameWidth      int
	FrameHeight     int
	Columns         int
	Count           int
}

type subtitleTrack struct {
	Language string `json:"language"`
	Label    string `json:"label"`
	Format   string `json:"format"`
	URL      string `json:"url"`
}

type timelinePreview struct {
	SpriteURL       string               `json:"sprite_url"`
	VTTURL          string               `json:"vtt_url,omitempty"`
	FrameWidth      int                  `json:"frame_width"`
	FrameHeight     int                  `json:"frame_height"`
	IntervalSeconds int                  `json:"interval_seconds"`
	Cues            []timelinePreviewCue `json:"cues"`
}

type timelinePreviewCue struct {
	StartSeconds int `json:"start_seconds"`
	EndSeconds   int `json:"end_seconds"`
	X            int `json:"x"`
	Y            int `json:"y"`
	Width        int `json:"width"`
	Height       int `json:"height"`
}

type videoChapter struct {
	StartSeconds int
	EndSeconds   int
	Label        string
}

type commentListResponse struct {
	Data       []commentItem `json:"data"`
	Pagination pagination    `json:"pagination"`
}

type commentItem struct {
	ID          string `json:"id"`
	ParentID    string `json:"parent_id,omitempty"`
	Author      string `json:"author"`
	Text        string `json:"text"`
	PublishedAt string `json:"published_at"`
	LikeCount   int    `json:"like_count"`
	ReplyCount  int    `json:"reply_count"`
}

type videoListResponse struct {
	Data       []videoListItem `json:"data"`
	Pagination pagination      `json:"pagination"`
}

type videoListItem struct {
	ID                string       `json:"id"`
	Title             string       `json:"title"`
	Description       string       `json:"description"`
	PublishedAt       string       `json:"published_at"`
	ArchivedAt        string       `json:"archived_at"`
	DurationSeconds   int          `json:"duration_seconds"`
	ViewCount         int          `json:"view_count"`
	ThumbnailURL      string       `json:"thumbnail_url,omitempty"`
	ThumbnailFallback string       `json:"thumbnail_fallback"`
	ArchiveState      string       `json:"archive_state"`
	CanDownload       bool         `json:"can_download"`
	MembersOnly       bool         `json:"members_only"`
	KeepForever       bool         `json:"keep_forever"`
	Channel           channelInfo  `json:"channel"`
	Progress          progressInfo `json:"progress"`
}

const videoListProjection = `v.id,
  v.title,
  v.description,
  COALESCE(v.published_at, ''),
  COALESCE(v.archived_at, ''),
  v.duration_seconds,
  v.view_count,
  v.media_path,
  v.thumbnail_path,
  v.thumbnail_url,
  v.source,
  v.external_id,
  v.keep_forever,
  v.members_only,
  COALESCE(c.id, ''),
  COALESCE(c.name, ''),
  COALESCE(c.thumbnail_url, ''),
  COALESCE(cma.path, ''),
  COALESCE(p.position_seconds, 0),
  COALESCE(p.duration_seconds, v.duration_seconds),
  CASE WHEN COALESCE(p.watched, 0) = 1 OR v.watched = 1 THEN 1 ELSE 0 END`

const videoListCommonJoins = `LEFT JOIN channels c ON c.id = v.channel_id
LEFT JOIN media_assets cma ON cma.owner_type = 'channel' AND cma.owner_id = c.id AND cma.kind = 'thumbnail'
LEFT JOIN user_progress p ON p.video_id = v.id`

func videoListSelect(from string) string {
	return "SELECT\n  " + videoListProjection + "\n" + from + "\n" + videoListCommonJoins + "\n"
}

type channelInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
}

type channelResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Handle       string `json:"handle"`
	Description  string `json:"description"`
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
	Subscribed   bool   `json:"subscribed"`
	VideoCount   int    `json:"video_count"`
}

type channelListResponse struct {
	Data       []channelListItem `json:"data"`
	Pagination pagination        `json:"pagination"`
}

type channelListItem struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Handle       string `json:"handle"`
	Description  string `json:"description"`
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
	Subscribed   bool   `json:"subscribed"`
	VideoCount   int    `json:"video_count"`
}

type channelSubscriptionPayload struct {
	Subscribed *bool `json:"subscribed"`
}

type channelSubscriptionResponse struct {
	ID         string `json:"id"`
	Subscribed bool   `json:"subscribed"`
}

type playlistResponse struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Subscribed  bool        `json:"subscribed"`
	Channel     channelInfo `json:"channel"`
	VideoCount  int         `json:"video_count"`
}

type playlistListResponse struct {
	Data       []playlistListItem `json:"data"`
	Pagination pagination         `json:"pagination"`
}

type playlistListItem struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Subscribed  bool        `json:"subscribed"`
	Channel     channelInfo `json:"channel"`
	VideoCount  int         `json:"video_count"`
}

type progressInfo struct {
	PositionSeconds int  `json:"position_seconds"`
	DurationSeconds int  `json:"duration_seconds"`
	Watched         bool `json:"watched"`
}

type progressUpdatePayload struct {
	PositionSeconds int  `json:"position_seconds"`
	DurationSeconds int  `json:"duration_seconds"`
	Watched         bool `json:"watched"`
}

type keepForeverPayload struct {
	KeepForever *bool `json:"keep_forever"`
}

type keepForeverResponse struct {
	ID          string `json:"id"`
	KeepForever bool   `json:"keep_forever"`
}

type pagination struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Total    int `json:"total"`
}

type searchResponse struct {
	Data           []search.Result `json:"data"`
	Limit          int             `json:"limit"`
	Total          int             `json:"total"`
	DistinctOwners int             `json:"distinct_owners"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func searchDocuments(db *sql.DB, mediaURLs mediaURLBuilder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		term := strings.TrimSpace(r.URL.Query().Get("q"))
		if term == "" {
			writeJSONError(w, "search query is required", http.StatusBadRequest)
			return
		}
		if len(term) > maxSearchQueryLength {
			writeJSONError(w, "search query is too long", http.StatusBadRequest)
			return
		}
		limit := searchLimit(r.URL.Query().Get("limit"))
		results, err := search.Search(r.Context(), db, search.Query{Term: term, Limit: limit})
		if err != nil {
			writeJSONError(w, "failed to search", http.StatusInternalServerError)
			return
		}
		stats, err := search.Stats(r.Context(), db, term)
		if err != nil {
			writeJSONError(w, "failed to search", http.StatusInternalServerError)
			return
		}
		for index := range results {
			if signed := mediaURLs.SignedURL(results[index].Record.ThumbnailPath); signed != "" {
				results[index].Record.ThumbnailURL = signed
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(searchResponse{Data: results, Limit: limit, Total: stats.Total, DistinctOwners: stats.DistinctOwners})
	}
}

func writeJSONError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: message})
}

func searchLimit(raw string) int {
	value := strings.TrimSpace(raw)
	if value == "" {
		return search.DefaultLimit
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit <= 0 {
		return search.DefaultLimit
	}
	if limit > search.MaxLimit {
		return search.MaxLimit
	}

	return limit
}

func listVideos(db *sql.DB, mediaURLs mediaURLBuilder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := boundedInt(r.URL.Query().Get("page"), 1, 1, 1000000)
		pageSize := boundedInt(r.URL.Query().Get("page_size"), 20, 1, 50)
		channelScoped := r.URL.Query().Get("channel") != ""
		homeList := homeVideoList(r)
		sortBy := r.URL.Query().Get("sort")
		if homeList && strings.TrimSpace(sortBy) == "" {
			sortBy = "watching"
		}
		sortExpr := videoSort(sortBy, r.URL.Query().Get("order"), channelScoped)
		where, args := videoFilters(r)
		if (homeList && videoSortIsWatching(sortBy)) || hideWatchedParam(r) {
			where = appendVideoFilter(where, homeUnwatchedVideoFilter())
		}
		writeVideoListPage(w, r, db, mediaURLs, where, args, sortExpr, page, pageSize)
	}
}

func listHomeVideos(db *sql.DB, mediaURLs mediaURLBuilder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := boundedInt(r.URL.Query().Get("page"), 1, 1, 1000000)
		pageSize := boundedInt(r.URL.Query().Get("page_size"), 20, 1, 50)
		sortBy := r.URL.Query().Get("sort")
		if strings.TrimSpace(sortBy) == "" {
			sortBy = "watching"
		}
		sortExpr := videoSort(sortBy, r.URL.Query().Get("order"), false)
		where := ""
		if videoSortIsWatching(sortBy) || hideWatchedParam(r) {
			where = appendVideoFilter(where, homeUnwatchedVideoFilter())
		}
		writeVideoListPage(w, r, db, mediaURLs, where, nil, sortExpr, page, pageSize)
	}
}

func listChannelVideos(db *sql.DB, mediaURLs mediaURLBuilder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		channelID := r.PathValue("id")
		var exists int
		if err := db.QueryRowContext(r.Context(), "SELECT 1 FROM channels WHERE id = ?", channelID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			http.Error(w, "failed to load channel", http.StatusInternalServerError)
			return
		}
		page := boundedInt(r.URL.Query().Get("page"), 1, 1, 1000000)
		pageSize := boundedInt(r.URL.Query().Get("page_size"), 20, 1, 50)
		where := "WHERE v.channel_id = ?"
		args := []any{channelID}
		sortExpr := videoSort(r.URL.Query().Get("sort"), r.URL.Query().Get("order"), true)
		if hideWatchedParam(r) {
			where = appendVideoFilter(where, homeUnwatchedVideoFilter())
		}
		writeVideoListPage(w, r, db, mediaURLs, where, args, sortExpr, page, pageSize)
	}
}

func writeVideoListPage(w http.ResponseWriter, r *http.Request, db *sql.DB, mediaURLs mediaURLBuilder, where string, args []any, sortExpr string, page int, pageSize int) {
	where = appendVideoFilter(where, "v.members_only = 0")
	var total int
	countQuery := "SELECT count(*) FROM videos v " + where
	if err := db.QueryRowContext(r.Context(), countQuery, args...).Scan(&total); err != nil {
		http.Error(w, "failed to count videos", http.StatusInternalServerError)
		return
	}

	query := videoListSelect("FROM videos v") + where + `
ORDER BY ` + sortExpr + `
LIMIT ? OFFSET ?`
	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := db.QueryContext(r.Context(), query, queryArgs...)
	if err != nil {
		http.Error(w, "failed to list videos", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items, err := scanVideoListItems(rows, mediaURLs)
	if err != nil {
		http.Error(w, "failed to read videos", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(videoListResponse{
		Data: items,
		Pagination: pagination{
			Page:     page,
			PageSize: pageSize,
			Total:    total,
		},
	})
}

func listUpNextVideos(db *sql.DB, mediaURLs mediaURLBuilder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		pageSize := boundedInt(r.URL.Query().Get("page_size"), 12, 1, 50)
		var currentChannelID string
		if err := db.QueryRowContext(r.Context(), "SELECT COALESCE(channel_id, '') FROM videos WHERE id = ?", id).Scan(&currentChannelID); errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			http.Error(w, "failed to load video", http.StatusInternalServerError)
			return
		}

		available := "NULLIF(v.media_path, '') IS NOT NULL"
		unwatched := "COALESCE(p.watched, 0) = 0 AND v.watched = 0"
		startedSameChannel := available + " AND v.channel_id = ? AND " + unwatched + " AND COALESCE(p.position_seconds, 0) > 0"
		unstartedSameChannel := available + " AND v.channel_id = ? AND " + unwatched
		rows, err := db.QueryContext(r.Context(), videoListSelect("FROM videos v")+`
WHERE v.id <> ?
  AND v.members_only = 0
  AND `+unwatched+`
ORDER BY CASE
  WHEN `+startedSameChannel+` THEN 0
  WHEN `+unstartedSameChannel+` THEN 1
  WHEN `+available+` THEN 2
  ELSE 3
END ASC, `+videoDateSort("DESC", false)+`
LIMIT ?`, id, currentChannelID, currentChannelID, pageSize)
		if err != nil {
			http.Error(w, "failed to list up next videos", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		items, err := scanVideoListItems(rows, mediaURLs)
		if err != nil {
			http.Error(w, "failed to read up next videos", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, videoListResponse{Data: items, Pagination: pagination{Page: 1, PageSize: pageSize, Total: len(items)}})
	}
}

func homeVideoList(r *http.Request) bool {
	if r.URL.Query().Get("home") != "1" {
		return false
	}

	return r.URL.Query().Get("channel") == "" && r.URL.Query().Get("playlist") == ""
}

func scanVideoListItems(rows *sql.Rows, mediaURLs mediaURLBuilder) ([]videoListItem, error) {
	items := []videoListItem{}
	for rows.Next() {
		var item videoListItem
		var mediaPath string
		var thumbnailPath string
		var remoteThumbnailURL string
		var source string
		var externalID string
		var remoteChannelThumbnailURL string
		var channelThumbnailPath string
		var watched int
		var keepForever int
		var membersOnly int
		if err := rows.Scan(
			&item.ID,
			&item.Title,
			&item.Description,
			&item.PublishedAt,
			&item.ArchivedAt,
			&item.DurationSeconds,
			&item.ViewCount,
			&mediaPath,
			&thumbnailPath,
			&remoteThumbnailURL,
			&source,
			&externalID,
			&keepForever,
			&membersOnly,
			&item.Channel.ID,
			&item.Channel.Name,
			&remoteChannelThumbnailURL,
			&channelThumbnailPath,
			&item.Progress.PositionSeconds,
			&item.Progress.DurationSeconds,
			&watched,
		); err != nil {
			return nil, err
		}
		mediaAvailable := mediaURLs.Available(mediaPath)
		item.ThumbnailURL = mediaURLs.SignedURL(thumbnailPath)
		if item.ThumbnailURL == "" {
			item.ThumbnailURL = remoteThumbnailURL
		}
		item.Channel.ThumbnailURL = mediaURLs.SignedURL(channelThumbnailPath)
		if item.Channel.ThumbnailURL == "" {
			item.Channel.ThumbnailURL = remoteChannelThumbnailURL
		}
		item.ThumbnailFallback = thumbnailFallback(item.ID, item.Title)
		item.ArchiveState = videoArchiveState(mediaAvailable)
		item.MembersOnly = membersOnly == 1
		item.CanDownload = canDownloadCatalogVideo(source, externalID, mediaAvailable, item.MembersOnly)
		item.KeepForever = keepForever == 1
		item.Progress.Watched = watched == 1
		items = append(items, item)
	}

	return items, rows.Err()
}

func videoFilters(r *http.Request) (string, []any) {
	var filters []string
	var args []any
	if channelID := r.URL.Query().Get("channel"); channelID != "" {
		filters = append(filters, "v.channel_id = ?")
		args = append(args, channelID)
	}
	if playlistID := r.URL.Query().Get("playlist"); playlistID != "" {
		filters = append(filters, "EXISTS (SELECT 1 FROM playlist_entries pe WHERE pe.video_id = v.id AND pe.playlist_id = ?)")
		args = append(args, playlistID)
	}
	if len(filters) == 0 {
		return "", args
	}

	return "WHERE " + strings.Join(filters, " AND "), args
}

func videoSort(sortBy string, orderBy string, channelScoped bool) string {
	sortBy = strings.ToLower(strings.TrimSpace(sortBy))
	direction := "DESC"
	if sortBy == "oldest" || strings.EqualFold(orderBy, "asc") {
		direction = "ASC"
	}

	switch sortBy {
	case "created":
		return "v.created_at " + direction + ", v.id " + direction
	case "length", "duration":
		dateExpr := "COALESCE(NULLIF(v.published_at, ''), NULLIF(v.archived_at, ''), v.created_at)"
		return "v.duration_seconds " + direction + ", " + dateExpr + " DESC, v.id " + direction
	case "popularity", "views":
		dateExpr := "COALESCE(NULLIF(v.published_at, ''), NULLIF(v.archived_at, ''), v.created_at)"
		return "v.view_count " + direction + ", " + dateExpr + " DESC, v.id " + direction
	case "downloaded", "recently-downloaded":
		return videoDownloadedSort(direction, channelScoped)
	case "watching", "continue", "in-progress":
		return videoWatchingSort(channelScoped)
	case "oldest", "newest", "date", "published":
		return videoDateSort(direction, channelScoped)
	default:
		return videoDateSort(direction, channelScoped)
	}
}

func videoSortIsWatching(sortBy string) bool {
	switch strings.ToLower(strings.TrimSpace(sortBy)) {
	case "watching", "continue", "in-progress":
		return true
	default:
		return false
	}
}

// hideWatchedParam reports whether the request asks to exclude finished
// videos from a video list, as offered by the sort toolbar's checkbox.
func hideWatchedParam(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hide_watched"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func appendVideoFilter(where string, condition string) string {
	if strings.TrimSpace(condition) == "" {
		return where
	}
	if strings.TrimSpace(where) == "" {
		return "WHERE " + condition
	}

	return where + " AND " + condition
}

func homeUnwatchedVideoFilter() string {
	return "v.watched = 0 AND NOT EXISTS (SELECT 1 FROM user_progress up WHERE up.video_id = v.id AND up.watched = 1)"
}

func videoWatchingSort(channelScoped bool) string {
	available := "NULLIF(v.media_path, '') IS NOT NULL"
	inProgress := "NULLIF(v.media_path, '') IS NOT NULL AND COALESCE(p.watched, 0) = 0 AND v.watched = 0 AND COALESCE(p.position_seconds, 0) > 0"
	return "CASE WHEN " + inProgress + " THEN 0 ELSE 1 END ASC, " +
		"CASE WHEN " + inProgress + " THEN COALESCE(NULLIF(p.updated_at, ''), '') ELSE '' END DESC, " +
		"CASE WHEN " + available + " THEN 0 ELSE 1 END ASC, " +
		videoDateSort("DESC", channelScoped)
}

func videoDownloadedSort(direction string, channelScoped bool) string {
	available := "NULLIF(v.media_path, '') IS NOT NULL"
	downloadedAt := "COALESCE(NULLIF(v.media_downloaded_at, ''), NULLIF(v.archived_at, ''), v.created_at)"
	return "CASE WHEN " + available + " THEN 0 ELSE 1 END ASC, " +
		"CASE WHEN " + available + " THEN " + downloadedAt + " ELSE '' END " + direction + ", " +
		videoDateSort("DESC", channelScoped)
}

func videoDateSort(direction string, channelScoped bool) string {
	sourceDateExpr := "NULLIF(v.published_at, '')"
	positionDirection := "ASC"
	if direction == "ASC" {
		positionDirection = "DESC"
	}
	if !channelScoped {
		fallbackDateExpr := "COALESCE(NULLIF(v.archived_at, ''), c.updated_at, v.created_at)"
		return "CASE WHEN " + sourceDateExpr + " IS NULL THEN 1 ELSE 0 END ASC, " +
			sourceDateExpr + " " + direction + ", " +
			"CASE WHEN " + sourceDateExpr + " IS NULL THEN " + fallbackDateExpr + " ELSE '' END " + direction + ", " +
			"CASE WHEN " + sourceDateExpr + " IS NULL AND v.catalog_position >= 0 THEN 0 ELSE 1 END ASC, " +
			"CASE WHEN " + sourceDateExpr + " IS NULL THEN v.catalog_position ELSE -1 END " + positionDirection + ", " +
			"v.id " + direction
	}

	createdDirection := "DESC"
	if direction == "ASC" {
		createdDirection = "ASC"
	}
	fallbackDateExpr := "COALESCE(NULLIF(v.archived_at, ''), v.created_at)"
	return "CASE WHEN " + sourceDateExpr + " IS NULL THEN 1 ELSE 0 END ASC, " +
		sourceDateExpr + " " + direction + ", " +
		"CASE WHEN v.catalog_position >= 0 THEN 0 ELSE 1 END ASC, " +
		"v.catalog_position " + positionDirection + ", " +
		"CASE WHEN " + sourceDateExpr + " IS NULL THEN " + fallbackDateExpr + " ELSE '' END " + direction + ", " +
		"v.created_at " + createdDirection + ", v.id " + direction
}

func boundedInt(raw string, fallback int, min int, max int) int {
	value, err := strconv.Atoi(raw)
	if err != nil {
		value = fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}

	return value
}

func getVideoProgress(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		progress, err := loadVideoProgress(r.Context(), db, r.PathValue("id"))
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "failed to load progress", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, progress)
	}
}

func updateVideoProgress(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var payload progressUpdatePayload
		if err := decodeJSONPayload(w, r, maxPlaybackProgressPayloadBytes, &payload); err != nil {
			writeJSONError(w, "invalid progress payload", http.StatusBadRequest)
			return
		}
		if !validProgressBounds(payload.PositionSeconds, payload.DurationSeconds) {
			writeJSONError(w, "progress payload out of bounds", http.StatusBadRequest)
			return
		}

		current, err := loadVideoProgress(r.Context(), db, r.PathValue("id"))
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "failed to load progress", http.StatusInternalServerError)
			return
		}

		progress := progressInfo{PositionSeconds: payload.PositionSeconds, DurationSeconds: payload.DurationSeconds}
		if progress.DurationSeconds == 0 {
			progress.DurationSeconds = current.DurationSeconds
		}
		if progress.DurationSeconds > 0 && progress.PositionSeconds > progress.DurationSeconds {
			progress.PositionSeconds = progress.DurationSeconds
		}
		progress.Watched = current.Watched || payload.Watched || nearCompletion(progress.PositionSeconds, progress.DurationSeconds)
		if current.Watched && !payload.Watched {
			progress.PositionSeconds = current.PositionSeconds
			progress.DurationSeconds = current.DurationSeconds
		}

		if err := saveVideoProgress(r.Context(), db, r.PathValue("id"), progress); err != nil {
			http.Error(w, "failed to save progress", http.StatusInternalServerError)
			return
		}

		saved, err := loadVideoProgress(r.Context(), db, r.PathValue("id"))
		if err != nil {
			http.Error(w, "failed to load progress", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, saved)
	}
}

func updateVideoKeepForever(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var payload keepForeverPayload
		if err := decodeJSONPayload(w, r, maxKeepForeverPayloadBytes, &payload); err != nil || payload.KeepForever == nil {
			writeJSONError(w, "invalid keep forever payload", http.StatusBadRequest)
			return
		}

		id := r.PathValue("id")
		result, err := db.ExecContext(r.Context(), `
UPDATE videos
SET keep_forever = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?`, boolInt(*payload.KeepForever), id)
		if err != nil {
			http.Error(w, "failed to update video", http.StatusInternalServerError)
			return
		}
		changed, err := result.RowsAffected()
		if err != nil {
			http.Error(w, "failed to update video", http.StatusInternalServerError)
			return
		}
		if changed == 0 {
			http.NotFound(w, r)
			return
		}

		writeJSON(w, http.StatusOK, keepForeverResponse{ID: id, KeepForever: *payload.KeepForever})
	}
}

func deleteVideoMedia(db *sql.DB, mediaRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireNoBody(w, r) {
			return
		}
		if strings.TrimSpace(mediaRoot) == "" {
			writeJSONError(w, "media root is not configured", http.StatusInternalServerError)
			return
		}
		if _, err := assetpath.ValidateRoot(mediaRoot); err != nil {
			http.Error(w, "failed to inspect media root", http.StatusInternalServerError)
			return
		}

		ctx := r.Context()
		id := r.PathValue("id")
		var mediaPath string
		var mediaOrigin string
		var mediaDownloadedAt string
		if err := db.QueryRowContext(ctx, "SELECT media_path, COALESCE(media_origin, ''), COALESCE(media_downloaded_at, '') FROM videos WHERE id = ?", id).Scan(&mediaPath, &mediaOrigin, &mediaDownloadedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "failed to load video", http.StatusInternalServerError)
			return
		}
		if strings.TrimSpace(mediaPath) == "" {
			writeJSONError(w, "video has no local media", http.StatusConflict)
			return
		}

		_, info, err := assetpath.Lstat(mediaRoot, mediaPath)
		if errors.Is(err, assetpath.ErrInvalid) || errors.Is(err, assetpath.ErrSymlink) {
			writeJSONError(w, "video media path is unsafe", http.StatusConflict)
			return
		}
		if errors.Is(err, os.ErrNotExist) {
			writeJSONError(w, "video media file is missing", http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, "failed to inspect media file", http.StatusInternalServerError)
			return
		}
		if !info.Mode().IsRegular() {
			writeJSONError(w, "video media is not a regular file", http.StatusConflict)
			return
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			http.Error(w, "failed to delete video media", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		result, err := tx.ExecContext(ctx, `
UPDATE videos
SET media_path = '', media_origin = 'imported', media_downloaded_at = '', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND media_path = ?`, id, mediaPath)
		if err != nil {
			http.Error(w, "failed to delete video media", http.StatusInternalServerError)
			return
		}
		changed, err := result.RowsAffected()
		if err != nil {
			http.Error(w, "failed to delete video media", http.StatusInternalServerError)
			return
		}
		if changed == 0 {
			writeJSONError(w, "video media changed before it could be deleted", http.StatusConflict)
			return
		}
		if err := tx.Commit(); err != nil {
			http.Error(w, "failed to delete video media", http.StatusInternalServerError)
			return
		}

		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancelCleanup()
		if _, err := assetpath.RemoveRegularMatching(mediaRoot, mediaPath, info); err != nil {
			if restoreErr := restoreVideoMediaReference(cleanupCtx, db, id, mediaPath, mediaOrigin, mediaDownloadedAt); restoreErr != nil {
				http.Error(w, "failed to delete media file and restore media reference", http.StatusInternalServerError)
				return
			}
			if errors.Is(err, os.ErrNotExist) {
				writeJSONError(w, "video media file is missing", http.StatusConflict)
				return
			}
			if errors.Is(err, assetpath.ErrChanged) {
				writeJSONError(w, "video media changed before it could be deleted", http.StatusConflict)
				return
			}
			http.Error(w, "failed to delete media file", http.StatusInternalServerError)
			return
		}
		if err := deleteVideoMediaAsset(cleanupCtx, db, id); err != nil {
			http.Error(w, "failed to delete video media asset", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func restoreVideoMediaReference(ctx context.Context, db *sql.DB, id string, mediaPath string, mediaOrigin string, mediaDownloadedAt string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
UPDATE videos
SET media_path = ?, media_origin = ?, media_downloaded_at = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND media_path = ''`, mediaPath, mediaOrigin, mediaDownloadedAt, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return errors.New("video media reference changed before restore")
	}
	return tx.Commit()
}

func deleteVideoMediaAsset(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, "DELETE FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'media'", id)

	return err
}

func loadVideoProgress(ctx context.Context, db *sql.DB, id string) (progressInfo, error) {
	var progress progressInfo
	var watched int
	err := db.QueryRowContext(ctx, `
SELECT
  COALESCE(p.position_seconds, 0),
  COALESCE(p.duration_seconds, v.duration_seconds),
  CASE WHEN COALESCE(p.watched, 0) = 1 OR v.watched = 1 THEN 1 ELSE 0 END
FROM videos v
LEFT JOIN user_progress p ON p.video_id = v.id
WHERE v.id = ?`, id).Scan(&progress.PositionSeconds, &progress.DurationSeconds, &watched)
	progress.Watched = watched == 1

	return progress, err
}

func saveVideoProgress(ctx context.Context, db *sql.DB, id string, progress progressInfo) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO user_progress (video_id, position_seconds, duration_seconds, watched)
VALUES (?, ?, ?, ?)
ON CONFLICT(video_id) DO UPDATE SET
  position_seconds = CASE
    WHEN user_progress.watched = 1 AND excluded.position_seconds < user_progress.position_seconds THEN user_progress.position_seconds
    ELSE excluded.position_seconds
  END,
  duration_seconds = CASE
    WHEN user_progress.watched = 1 AND excluded.position_seconds < user_progress.position_seconds THEN user_progress.duration_seconds
    ELSE excluded.duration_seconds
  END,
  watched = CASE WHEN user_progress.watched = 1 THEN 1 ELSE excluded.watched END,
  updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`, id, progress.PositionSeconds, progress.DurationSeconds, boolInt(progress.Watched)); err != nil {
		return err
	}
	if progress.Watched {
		if _, err := tx.ExecContext(ctx, `
UPDATE videos
SET watched = 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND watched = 0`, id); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func validProgressBounds(positionSeconds int, durationSeconds int) bool {
	return positionSeconds >= 0 && durationSeconds >= 0 && positionSeconds <= maxPlaybackProgressSeconds && durationSeconds <= maxPlaybackProgressSeconds
}

func nearCompletion(positionSeconds int, durationSeconds int) bool {
	if durationSeconds <= 0 || positionSeconds <= 0 {
		return false
	}
	threshold := durationSeconds * 9 / 10
	if durationSeconds > 30 && durationSeconds-30 > threshold {
		threshold = durationSeconds - 30
	}
	if threshold < 1 {
		threshold = 1
	}

	return positionSeconds >= threshold
}

func listVideoComments(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		videoID := r.PathValue("id")
		if exists, err := rowExists(r.Context(), db, "SELECT 1 FROM videos WHERE id = ?", videoID); err != nil {
			http.Error(w, "failed to load video", http.StatusInternalServerError)
			return
		} else if !exists {
			http.NotFound(w, r)
			return
		}
		page := boundedInt(r.URL.Query().Get("page"), 1, 1, 1000000)
		pageSize := boundedInt(r.URL.Query().Get("page_size"), 20, 1, 50)
		parentID := strings.TrimSpace(r.URL.Query().Get("parent"))
		where, args := commentFilter(videoID, parentID)

		var total int
		if err := db.QueryRowContext(r.Context(), "SELECT count(*) FROM comments c "+where, args...).Scan(&total); err != nil {
			http.Error(w, "failed to count comments", http.StatusInternalServerError)
			return
		}
		queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
		rows, err := db.QueryContext(r.Context(), `
SELECT
  c.id,
  COALESCE(c.parent_id, ''),
  c.author,
  c.text,
  COALESCE(c.published_at, ''),
  c.like_count,
  (SELECT count(*) FROM comments replies WHERE replies.video_id = c.video_id AND replies.parent_id = c.id)
FROM comments c
`+where+`
ORDER BY COALESCE(c.published_at, ''), c.id
LIMIT ? OFFSET ?`, queryArgs...)
		if err != nil {
			http.Error(w, "failed to list comments", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		items := []commentItem{}
		for rows.Next() {
			var item commentItem
			if err := rows.Scan(&item.ID, &item.ParentID, &item.Author, &item.Text, &item.PublishedAt, &item.LikeCount, &item.ReplyCount); err != nil {
				http.Error(w, "failed to read comments", http.StatusInternalServerError)
				return
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "failed to read comments", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, commentListResponse{Data: items, Pagination: pagination{Page: page, PageSize: pageSize, Total: total}})
	}
}

func commentFilter(videoID string, parentID string) (string, []any) {
	if parentID != "" {
		return "WHERE c.video_id = ? AND c.parent_id = ?", []any{videoID, parentID}
	}

	return "WHERE c.video_id = ? AND (c.parent_id IS NULL OR c.parent_id = '')", []any{videoID}
}

func boolInt(value bool) int {
	if value {
		return 1
	}

	return 0
}

func listChannels(db *sql.DB, mediaURLs mediaURLBuilder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := boundedInt(r.URL.Query().Get("page"), 1, 1, 1000000)
		pageSize := boundedInt(r.URL.Query().Get("page_size"), 20, 1, 50)
		var total int
		if err := db.QueryRowContext(r.Context(), "SELECT count(*) FROM channels").Scan(&total); err != nil {
			http.Error(w, "failed to count channels", http.StatusInternalServerError)
			return
		}
		rows, err := db.QueryContext(r.Context(), `
SELECT
  c.id,
  c.name,
  c.handle,
  c.description,
  c.thumbnail_url,
  COALESCE(ma.path, ''),
  c.subscribed,
  count(v.id)
FROM channels c
LEFT JOIN media_assets ma ON ma.owner_type = 'channel' AND ma.owner_id = c.id AND ma.kind = 'thumbnail'
LEFT JOIN videos v ON v.channel_id = c.id
GROUP BY c.id, c.name, c.handle, c.description, c.thumbnail_url, ma.path, c.subscribed
ORDER BY lower(c.name), c.id
LIMIT ? OFFSET ?`, pageSize, (page-1)*pageSize)
		if err != nil {
			http.Error(w, "failed to list channels", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		items := []channelListItem{}
		for rows.Next() {
			var item channelListItem
			var thumbnailPath string
			var remoteThumbnailURL string
			var subscribed int
			if err := rows.Scan(&item.ID, &item.Name, &item.Handle, &item.Description, &remoteThumbnailURL, &thumbnailPath, &subscribed, &item.VideoCount); err != nil {
				http.Error(w, "failed to read channels", http.StatusInternalServerError)
				return
			}
			item.ThumbnailURL = mediaURLs.SignedURL(thumbnailPath)
			if item.ThumbnailURL == "" {
				item.ThumbnailURL = remoteThumbnailURL
			}
			item.Subscribed = subscribed == 1
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "failed to read channels", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, channelListResponse{Data: items, Pagination: pagination{Page: page, PageSize: pageSize, Total: total}})
	}
}

func listPlaylists(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := boundedInt(r.URL.Query().Get("page"), 1, 1, 1000000)
		pageSize := boundedInt(r.URL.Query().Get("page_size"), 20, 1, 50)
		var total int
		if err := db.QueryRowContext(r.Context(), "SELECT count(*) FROM playlists").Scan(&total); err != nil {
			http.Error(w, "failed to count playlists", http.StatusInternalServerError)
			return
		}
		rows, err := db.QueryContext(r.Context(), `
SELECT
  p.id,
  p.title,
  p.description,
  p.subscribed,
  COALESCE(c.id, ''),
  COALESCE(c.name, ''),
  count(pe.video_id)
FROM playlists p
LEFT JOIN channels c ON c.id = p.channel_id
LEFT JOIN playlist_entries pe ON pe.playlist_id = p.id
GROUP BY p.id, p.title, p.description, p.subscribed, c.id, c.name
ORDER BY lower(p.title), p.id
LIMIT ? OFFSET ?`, pageSize, (page-1)*pageSize)
		if err != nil {
			http.Error(w, "failed to list playlists", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		items := []playlistListItem{}
		for rows.Next() {
			var item playlistListItem
			var subscribed int
			if err := rows.Scan(&item.ID, &item.Title, &item.Description, &subscribed, &item.Channel.ID, &item.Channel.Name, &item.VideoCount); err != nil {
				http.Error(w, "failed to read playlists", http.StatusInternalServerError)
				return
			}
			item.Subscribed = subscribed == 1
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "failed to read playlists", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, playlistListResponse{Data: items, Pagination: pagination{Page: page, PageSize: pageSize, Total: total}})
	}
}

func getPlaylist(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		response, err := loadPlaylist(r.Context(), db, r.PathValue("id"))
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "failed to load playlist", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, response)
	}
}

func loadPlaylist(ctx context.Context, db *sql.DB, id string) (playlistResponse, error) {
	var response playlistResponse
	var subscribed int
	err := db.QueryRowContext(ctx, `
SELECT
  p.id,
  p.title,
  p.description,
  p.subscribed,
  COALESCE(c.id, ''),
  COALESCE(c.name, ''),
  count(pe.video_id)
FROM playlists p
LEFT JOIN channels c ON c.id = p.channel_id
LEFT JOIN playlist_entries pe ON pe.playlist_id = p.id
WHERE p.id = ?
GROUP BY p.id, p.title, p.description, p.subscribed, c.id, c.name`, id).Scan(&response.ID, &response.Title, &response.Description, &subscribed, &response.Channel.ID, &response.Channel.Name, &response.VideoCount)
	response.Subscribed = subscribed == 1

	return response, err
}

func listPlaylistVideos(db *sql.DB, mediaURLs mediaURLBuilder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		playlistID := r.PathValue("id")
		if _, err := loadPlaylist(r.Context(), db, playlistID); errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			http.Error(w, "failed to load playlist", http.StatusInternalServerError)
			return
		}
		page := boundedInt(r.URL.Query().Get("page"), 1, 1, 1000000)
		pageSize := boundedInt(r.URL.Query().Get("page_size"), 20, 1, 50)
		var total int
		if err := db.QueryRowContext(r.Context(), "SELECT count(*) FROM playlist_entries WHERE playlist_id = ?", playlistID).Scan(&total); err != nil {
			http.Error(w, "failed to count playlist videos", http.StatusInternalServerError)
			return
		}
		rows, err := db.QueryContext(r.Context(), videoListSelect(`FROM playlist_entries pe
JOIN videos v ON v.id = pe.video_id`)+`
WHERE pe.playlist_id = ? AND v.members_only = 0
ORDER BY pe.position ASC, v.id ASC
LIMIT ? OFFSET ?`, playlistID, pageSize, (page-1)*pageSize)
		if err != nil {
			http.Error(w, "failed to list playlist videos", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		items, err := scanVideoListItems(rows, mediaURLs)
		if err != nil {
			http.Error(w, "failed to read playlist videos", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, videoListResponse{Data: items, Pagination: pagination{Page: page, PageSize: pageSize, Total: total}})
	}
}

func deleteChannel(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireNoBody(w, r) {
			return
		}
		ctx := r.Context()
		id := r.PathValue("id")
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			http.Error(w, "failed to delete channel", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		// Refuse when the channel has downloaded media or playlists (real local
		// content). Catalog-only video rows (empty media_path) are removable.
		var hasLocalContent int
		if err := tx.QueryRowContext(ctx, `
SELECT CASE WHEN EXISTS (
  SELECT 1 FROM videos WHERE channel_id = ? AND media_path <> ''
) OR EXISTS (
  SELECT 1 FROM playlists WHERE channel_id = ?
) THEN 1 ELSE 0 END`, id, id).Scan(&hasLocalContent); err != nil {
			http.Error(w, "failed to delete channel", http.StatusInternalServerError)
			return
		}
		if hasLocalContent == 1 {
			if err := tx.Rollback(); err != nil {
				http.Error(w, "failed to delete channel", http.StatusInternalServerError)
				return
			}
			if exists, err := rowExists(ctx, db, "SELECT 1 FROM channels WHERE id = ?", id); err != nil {
				http.Error(w, "failed to load channel", http.StatusInternalServerError)
			} else if !exists {
				http.NotFound(w, r)
			} else {
				writeJSONError(w, "channel still has downloaded media or playlists", http.StatusConflict)
			}
			return
		}

		// Remove the channel's catalog-only video rows and their denormalized
		// rows before deleting the channel itself.
		rows, err := tx.QueryContext(ctx, "SELECT id FROM videos WHERE channel_id = ?", id)
		if err != nil {
			http.Error(w, "failed to delete channel", http.StatusInternalServerError)
			return
		}
		videoIDs := []string{}
		for rows.Next() {
			var videoID string
			if err := rows.Scan(&videoID); err != nil {
				rows.Close()
				http.Error(w, "failed to delete channel", http.StatusInternalServerError)
				return
			}
			videoIDs = append(videoIDs, videoID)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			http.Error(w, "failed to delete channel", http.StatusInternalServerError)
			return
		}
		for _, videoID := range videoIDs {
			if err := denorm.DeleteSearchDocumentsForOwner(ctx, tx, "video", videoID); err != nil {
				http.Error(w, "failed to delete channel", http.StatusInternalServerError)
				return
			}
			if err := denorm.DeleteMediaAssetsForOwner(ctx, tx, "video", videoID); err != nil {
				http.Error(w, "failed to delete channel", http.StatusInternalServerError)
				return
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM videos WHERE channel_id = ?", id); err != nil {
			http.Error(w, "failed to delete channel", http.StatusInternalServerError)
			return
		}

		result, err := tx.ExecContext(ctx, "DELETE FROM channels WHERE id = ?", id)
		if err != nil {
			http.Error(w, "failed to delete channel", http.StatusInternalServerError)
			return
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			http.Error(w, "failed to delete channel", http.StatusInternalServerError)
			return
		}
		if deleted == 1 {
			if err := denorm.DeleteSearchDocumentsForOwner(ctx, tx, "channel", id); err != nil {
				http.Error(w, "failed to delete channel", http.StatusInternalServerError)
				return
			}
			if err := denorm.DeleteMediaAssetsForOwner(ctx, tx, "channel", id); err != nil {
				http.Error(w, "failed to delete channel", http.StatusInternalServerError)
				return
			}
			if err := tx.Commit(); err != nil {
				http.Error(w, "failed to delete channel", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := tx.Rollback(); err != nil {
			http.Error(w, "failed to delete channel", http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	}
}

func updateChannelSubscription(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		channelID := r.PathValue("id")
		var payload channelSubscriptionPayload
		if err := decodeJSONPayload(w, r, maxChannelPayloadBytes, &payload); err != nil || payload.Subscribed == nil {
			http.Error(w, "invalid channel subscription payload", http.StatusBadRequest)
			return
		}

		result, err := db.ExecContext(r.Context(), `
UPDATE channels
SET subscribed = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?`, boolInt(*payload.Subscribed), channelID)
		if err != nil {
			http.Error(w, "failed to update channel", http.StatusInternalServerError)
			return
		}
		changed, err := result.RowsAffected()
		if err != nil {
			http.Error(w, "failed to update channel", http.StatusInternalServerError)
			return
		}
		if changed == 0 {
			http.NotFound(w, r)
			return
		}

		writeJSON(w, http.StatusOK, channelSubscriptionResponse{ID: channelID, Subscribed: *payload.Subscribed})
	}
}

func deletePlaylist(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireNoBody(w, r) {
			return
		}
		ctx := r.Context()
		id := r.PathValue("id")
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			http.Error(w, "failed to delete playlist", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		result, err := tx.ExecContext(ctx, "DELETE FROM playlists WHERE id = ?", id)
		if err != nil {
			http.Error(w, "failed to delete playlist", http.StatusInternalServerError)
			return
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			http.Error(w, "failed to delete playlist", http.StatusInternalServerError)
			return
		}
		if deleted == 0 {
			if err := tx.Rollback(); err != nil {
				http.Error(w, "failed to delete playlist", http.StatusInternalServerError)
				return
			}
			http.NotFound(w, r)
			return
		}
		if err := denorm.DeleteSearchDocumentsForOwner(ctx, tx, "playlist", id); err != nil {
			http.Error(w, "failed to delete playlist", http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(); err != nil {
			http.Error(w, "failed to delete playlist", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func rowExists(ctx context.Context, db *sql.DB, query string, args ...any) (bool, error) {
	var value int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&value); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}

	return true, nil
}

func getChannel(db *sql.DB, mediaURLs mediaURLBuilder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var response channelResponse
		var thumbnailPath string
		var remoteThumbnailURL string
		var subscribed int
		if err := db.QueryRowContext(r.Context(), `
SELECT
  c.id,
  c.name,
  c.handle,
  c.description,
  c.thumbnail_url,
  COALESCE(ma.path, ''),
  c.subscribed,
  count(v.id)
FROM channels c
LEFT JOIN media_assets ma ON ma.owner_type = 'channel' AND ma.owner_id = c.id AND ma.kind = 'thumbnail'
LEFT JOIN videos v ON v.channel_id = c.id
WHERE c.id = ?
GROUP BY c.id, c.name, c.handle, c.description, c.thumbnail_url, ma.path, c.subscribed`, r.PathValue("id")).Scan(
			&response.ID,
			&response.Name,
			&response.Handle,
			&response.Description,
			&remoteThumbnailURL,
			&thumbnailPath,
			&subscribed,
			&response.VideoCount,
		); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "failed to load channel", http.StatusInternalServerError)
			return
		}
		response.ThumbnailURL = mediaURLs.SignedURL(thumbnailPath)
		if response.ThumbnailURL == "" {
			response.ThumbnailURL = remoteThumbnailURL
		}
		response.Subscribed = subscribed == 1

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}
}

func getVideo(db *sql.DB, mediaURLs mediaURLBuilder, store *jobs.Store, sponsorClient sponsorBlockClient, sponsorFailures *sponsorBlockFailureCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var response videoResponse
		var mediaPath string
		var thumbnailPath string
		var remoteThumbnailURL string
		var channelThumbnailPath string
		var remoteChannelThumbnailURL string
		var source string
		var externalID string
		var previewSpritePath string
		var previewInterval int
		var previewFrameWidth int
		var previewFrameHeight int
		var previewColumns int
		var previewCount int
		var watched int
		var keepForever int
		var membersOnly int
		if err := db.QueryRowContext(r.Context(), `
SELECT
  v.id,
	  v.title,
	  v.description,
	  COALESCE(v.published_at, ''),
	  COALESCE(v.archived_at, ''),
	  v.duration_seconds,
	  v.view_count,
		  v.media_path,
		  v.thumbnail_path,
		  v.thumbnail_url,
			  v.source,
			  v.external_id,
			  v.keep_forever,
			  v.members_only,
			  COALESCE(vp.sprite_path, ''),
	  COALESCE(vp.interval_seconds, 0),
	  COALESCE(vp.frame_width, 0),
	  COALESCE(vp.frame_height, 0),
	  COALESCE(vp.columns, 0),
			  COALESCE(vp.preview_count, 0),
			  COALESCE(c.id, ''),
			  COALESCE(c.name, ''),
			  COALESCE(c.thumbnail_url, ''),
			  COALESCE(cma.path, ''),
			  COALESCE(p.position_seconds, 0),
			  CASE WHEN COALESCE(p.watched, 0) = 1 OR v.watched = 1 THEN 1 ELSE 0 END
FROM videos v
LEFT JOIN channels c ON c.id = v.channel_id
LEFT JOIN media_assets cma ON cma.owner_type = 'channel' AND cma.owner_id = c.id AND cma.kind = 'thumbnail'
LEFT JOIN user_progress p ON p.video_id = v.id
LEFT JOIN video_previews vp ON vp.video_id = v.id
WHERE v.id = ?`, r.PathValue("id")).Scan(
			&response.ID,
			&response.Title,
			&response.Description,
			&response.PublishedAt,
			&response.ArchivedAt,
			&response.DurationSeconds,
			&response.ViewCount,
			&mediaPath,
			&thumbnailPath,
			&remoteThumbnailURL,
			&source,
			&externalID,
			&keepForever,
			&membersOnly,
			&previewSpritePath,
			&previewInterval,
			&previewFrameWidth,
			&previewFrameHeight,
			&previewColumns,
			&previewCount,
			&response.Channel.ID,
			&response.Channel.Name,
			&remoteChannelThumbnailURL,
			&channelThumbnailPath,
			&response.PositionSeconds,
			&watched,
		); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "failed to load video", http.StatusInternalServerError)
			return
		}

		mediaAvailable := mediaURLs.Available(mediaPath)
		response.MediaURL = mediaURLs.SignedURL(mediaPath)
		response.ThumbnailURL = mediaURLs.SignedURL(thumbnailPath)
		if response.ThumbnailURL == "" {
			response.ThumbnailURL = remoteThumbnailURL
		}
		response.Channel.ThumbnailURL = mediaURLs.SignedURL(channelThumbnailPath)
		if response.Channel.ThumbnailURL == "" {
			response.Channel.ThumbnailURL = remoteChannelThumbnailURL
		}
		if len(descriptionChapters(response.Description, response.DurationSeconds)) > 0 {
			response.ChaptersVTTURL = videoChaptersVTTURL(response.ID)
		}
		response.ThumbnailFallback = thumbnailFallback(response.ID, response.Title)
		response.ArchiveState = videoArchiveState(mediaAvailable)
		response.MembersOnly = membersOnly == 1
		response.CanDownload = canDownloadCatalogVideo(source, externalID, mediaAvailable, response.MembersOnly)
		response.KeepForever = keepForever == 1
		activeDownloadJob, err := activeDownloadJobForVideo(r.Context(), store, source, externalID)
		if err != nil {
			http.Error(w, "failed to load active download job", http.StatusInternalServerError)
			return
		}
		response.ActiveDownloadJob = activeDownloadJob
		activePreviewJob, err := activePreviewJobForVideo(r.Context(), store, response.ID)
		if err != nil {
			http.Error(w, "failed to load active preview job", http.StatusInternalServerError)
			return
		}
		response.ActivePreviewJob = activePreviewJob
		if activePreviewJob == nil && previewSpritePath == "" {
			previewMetadata, err := loadTimelinePreviewMetadata(r.Context(), db, response.ID)
			if err != nil {
				http.Error(w, "failed to load timeline preview", http.StatusInternalServerError)
				return
			}
			previewSpritePath = previewMetadata.SpritePath
			previewInterval = previewMetadata.IntervalSeconds
			previewFrameWidth = previewMetadata.FrameWidth
			previewFrameHeight = previewMetadata.FrameHeight
			previewColumns = previewMetadata.Columns
			previewCount = previewMetadata.Count
		}
		response.TimelinePreview = buildTimelinePreview(mediaURLs, response.ID, previewSpritePath, previewInterval, previewFrameWidth, previewFrameHeight, previewColumns, previewCount, response.DurationSeconds)
		tracks, err := loadSubtitleTracks(r.Context(), db, response.ID, mediaURLs)
		if err != nil {
			http.Error(w, "failed to load subtitles", http.StatusInternalServerError)
			return
		}
		response.Subtitles = tracks
		sponsorSegments, err := cachedVideoSponsorSegmentsIfEnabled(r.Context(), db, sponsorClient, response.ID, source, externalID)
		if err != nil {
			http.Error(w, "failed to load sponsor segments", http.StatusInternalServerError)
			return
		}
		response.SponsorSegments = sponsorSegments
		response.Watched = watched == 1
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}
}

func getVideoSponsorSegments(db *sql.DB, client sponsorBlockClient, failures *sponsorBlockFailureCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var source string
		var externalID string
		if err := db.QueryRowContext(r.Context(), `
SELECT COALESCE(source, ''), COALESCE(external_id, '')
FROM videos
WHERE id = ?`, r.PathValue("id")).Scan(&source, &externalID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "failed to load video", http.StatusInternalServerError)
			return
		}

		segments, err := loadVideoSponsorSegments(r.Context(), db, client, failures, r.PathValue("id"), source, externalID)
		if err != nil {
			http.Error(w, "failed to load sponsor segments", http.StatusInternalServerError)
			return
		}
		if segments == nil {
			segments = []sponsorblock.Segment{}
		}

		writeJSON(w, http.StatusOK, sponsorSegmentsResponse{Data: segments})
	}
}

func thumbnailFallback(id string, title string) string {
	for _, value := range []string{title, id, "K"} {
		for _, char := range strings.TrimSpace(value) {
			if unicode.IsLetter(char) || unicode.IsDigit(char) {
				return strings.ToUpper(string(char))
			}
		}
	}

	return "K"
}

func videoArchiveState(mediaAvailable bool) string {
	if !mediaAvailable {
		return string(archive.VideoStateCatalogOnly)
	}

	return string(archive.VideoStateDownloaded)
}

func loadVideoSponsorSegments(ctx context.Context, db *sql.DB, client sponsorBlockClient, failures *sponsorBlockFailureCache, videoID string, source string, externalID string) ([]sponsorblock.Segment, error) {
	if client == nil || source != "youtube" || strings.TrimSpace(externalID) == "" {
		return nil, nil
	}
	failureKey := sponsorBlockFailureKey(source, externalID)
	segments, cached, err := cachedVideoSponsorSegments(ctx, db, videoID, source, externalID)
	if err != nil {
		return nil, err
	}
	if cached {
		return segments, nil
	}
	wait, skip := failures.begin(failureKey)
	if skip {
		return nil, nil
	}
	if wait != nil {
		select {
		case <-wait:
			return loadVideoSponsorSegments(ctx, db, client, failures, videoID, source, externalID)
		case <-ctx.Done():
			return nil, nil
		}
	}

	segments, err = client.SponsorSegments(ctx, externalID)
	if err != nil {
		failures.finish(failureKey, ctx.Err() == nil)
		return nil, nil
	}
	segments = sponsorblock.Normalize(segments)
	if err := saveVideoSponsorSegments(ctx, db, videoID, source, externalID, segments); err != nil {
		failures.finish(failureKey, false)
		return nil, err
	}
	failures.finish(failureKey, false)

	return segments, nil
}

func cachedVideoSponsorSegmentsIfEnabled(ctx context.Context, db *sql.DB, client sponsorBlockClient, videoID string, source string, externalID string) ([]sponsorblock.Segment, error) {
	if client == nil || source != "youtube" || strings.TrimSpace(externalID) == "" {
		return nil, nil
	}
	segments, cached, err := cachedVideoSponsorSegments(ctx, db, videoID, source, externalID)
	if err != nil {
		return nil, err
	}
	if !cached {
		return nil, nil
	}

	return segments, nil
}

func sponsorBlockFailureKey(source string, externalID string) string {
	return source + "\x00" + externalID
}

func cachedVideoSponsorSegments(ctx context.Context, db *sql.DB, videoID string, source string, externalID string) ([]sponsorblock.Segment, bool, error) {
	var cached int
	if err := db.QueryRowContext(ctx, `
SELECT count(*)
FROM sponsorblock_cache
WHERE video_id = ? AND source = ? AND external_id = ?`, videoID, source, externalID).Scan(&cached); err != nil {
		return nil, false, err
	}
	if cached == 0 {
		return nil, false, nil
	}

	rows, err := db.QueryContext(ctx, `
SELECT start_seconds, end_seconds
FROM sponsorblock_segments
WHERE video_id = ?
ORDER BY start_seconds, end_seconds`, videoID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	var segments []sponsorblock.Segment
	for rows.Next() {
		var segment sponsorblock.Segment
		if err := rows.Scan(&segment.StartSeconds, &segment.EndSeconds); err != nil {
			return nil, false, err
		}
		segments = append(segments, segment)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	return segments, true, nil
}

func saveVideoSponsorSegments(ctx context.Context, db *sql.DB, videoID string, source string, externalID string, segments []sponsorblock.Segment) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO sponsorblock_cache (video_id, source, external_id, fetched_at)
VALUES (?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
ON CONFLICT(video_id) DO UPDATE SET
  source = excluded.source,
  external_id = excluded.external_id,
  fetched_at = excluded.fetched_at`, videoID, source, externalID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sponsorblock_segments WHERE video_id = ?", videoID); err != nil {
		return err
	}
	for _, segment := range segments {
		if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO sponsorblock_segments (video_id, start_seconds, end_seconds)
VALUES (?, ?, ?)`, videoID, segment.StartSeconds, segment.EndSeconds); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func canDownloadCatalogVideo(source string, externalID string, mediaAvailable bool, membersOnly bool) bool {
	if membersOnly || mediaAvailable || source != "youtube" || externalID == "" {
		return false
	}
	_, err := download.NormalizeDirectVideoURL("https://www.youtube.com/watch?v=" + url.QueryEscape(externalID))

	return err == nil
}

func getTimelinePreviewVTT(db *sql.DB, mediaURLs mediaURLBuilder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var spritePath string
		var durationSeconds int
		var intervalSeconds int
		var frameWidth int
		var frameHeight int
		var columns int
		var count int
		if err := db.QueryRowContext(r.Context(), `
SELECT
  COALESCE(vp.sprite_path, ''),
  v.duration_seconds,
  COALESCE(vp.interval_seconds, 0),
  COALESCE(vp.frame_width, 0),
  COALESCE(vp.frame_height, 0),
  COALESCE(vp.columns, 0),
  COALESCE(vp.preview_count, 0)
FROM videos v
JOIN video_previews vp ON vp.video_id = v.id
WHERE v.id = ?`, r.PathValue("id")).Scan(&spritePath, &durationSeconds, &intervalSeconds, &frameWidth, &frameHeight, &columns, &count); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "failed to load timeline preview", http.StatusInternalServerError)
			return
		}

		preview := buildTimelinePreview(mediaURLs, r.PathValue("id"), spritePath, intervalSeconds, frameWidth, frameHeight, columns, count, durationSeconds)
		if preview == nil {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		_, _ = io.WriteString(w, timelinePreviewVTT(preview))
	}
}

func getVideoChaptersVTT(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var description string
		var durationSeconds int
		if err := db.QueryRowContext(r.Context(), "SELECT description, duration_seconds FROM videos WHERE id = ?", r.PathValue("id")).Scan(&description, &durationSeconds); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "failed to load video chapters", http.StatusInternalServerError)
			return
		}

		chapters := descriptionChapters(description, durationSeconds)
		if len(chapters) == 0 {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		_, _ = io.WriteString(w, videoChaptersVTT(chapters))
	}
}

func buildTimelinePreview(mediaURLs mediaURLBuilder, videoID string, spritePath string, intervalSeconds int, frameWidth int, frameHeight int, columns int, count int, durationSeconds int) *timelinePreview {
	spriteURL := mediaURLs.SignedURL(spritePath)
	if spriteURL == "" || intervalSeconds <= 0 || frameWidth <= 0 || frameHeight <= 0 || columns <= 0 || count <= 0 {
		return nil
	}
	preview := timelinePreview{
		SpriteURL:       spriteURL,
		VTTURL:          timelinePreviewVTTURL(videoID),
		FrameWidth:      frameWidth,
		FrameHeight:     frameHeight,
		IntervalSeconds: intervalSeconds,
	}
	for index := range count {
		start := index * intervalSeconds
		end := start + intervalSeconds
		if durationSeconds > 0 && end > durationSeconds {
			end = durationSeconds
		}
		preview.Cues = append(preview.Cues, timelinePreviewCue{
			StartSeconds: start,
			EndSeconds:   end,
			X:            (index % columns) * frameWidth,
			Y:            (index / columns) * frameHeight,
			Width:        frameWidth,
			Height:       frameHeight,
		})
	}

	return &preview
}

func timelinePreviewVTTURL(videoID string) string {
	if videoID == "" {
		return ""
	}

	return "/api/videos/" + url.PathEscape(videoID) + "/timeline-preview.vtt"
}

func videoChaptersVTTURL(videoID string) string {
	if videoID == "" {
		return ""
	}

	return "/api/videos/" + url.PathEscape(videoID) + "/chapters.vtt"
}

func descriptionChapters(description string, durationSeconds int) []videoChapter {
	chapters := []videoChapter{}
	if len(description) > maxChapterDescriptionBytes {
		description = description[:maxChapterDescriptionBytes]
	}
	for linesScanned := 0; description != "" && linesScanned < maxChapterDescriptionLines; linesScanned++ {
		line := description
		if index := strings.IndexByte(description, '\n'); index >= 0 {
			line = description[:index]
			description = description[index+1:]
		} else {
			description = ""
		}
		line = strings.TrimSuffix(line, "\r")
		start, label, ok := parseDescriptionChapterLine(line)
		if !ok {
			continue
		}
		if len(chapters) > 0 && start <= chapters[len(chapters)-1].StartSeconds {
			continue
		}
		chapters = append(chapters, videoChapter{StartSeconds: start, Label: label})
		if len(chapters) > maxDescriptionChapters {
			break
		}
	}

	for index := range chapters {
		end := durationSeconds
		if index < len(chapters)-1 {
			end = chapters[index+1].StartSeconds
		} else if end <= 0 {
			continue
		}
		if durationSeconds > 0 && end > durationSeconds {
			end = durationSeconds
		}
		if end <= chapters[index].StartSeconds {
			continue
		}
		chapters[index].EndSeconds = end
	}

	valid := chapters[:0]
	for _, chapter := range chapters {
		if len(valid) >= maxDescriptionChapters {
			break
		}
		if chapter.EndSeconds > chapter.StartSeconds {
			valid = append(valid, chapter)
		}
	}

	return valid
}

func parseDescriptionChapterLine(line string) (int, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return 0, "", false
	}

	end := 0
	for end < len(line) && (line[end] == ':' || line[end] >= '0' && line[end] <= '9') {
		end++
	}
	if end == 0 || end == len(line) {
		return 0, "", false
	}
	seconds, ok := parseChapterTimestamp(line[:end])
	if !ok {
		return 0, "", false
	}
	label := strings.TrimSpace(line[end:])
	if !strings.HasPrefix(label, "-") {
		return 0, "", false
	}
	label = strings.TrimSpace(strings.TrimPrefix(label, "-"))
	if label == "" {
		return 0, "", false
	}

	return seconds, label, true
}

func parseChapterTimestamp(value string) (int, bool) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, false
	}
	values := make([]int, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return 0, false
		}
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 {
			return 0, false
		}
		values = append(values, parsed)
	}

	var seconds int
	if len(values) == 2 {
		if values[1] > 59 {
			return 0, false
		}
		seconds = values[0]*60 + values[1]
	} else {
		if values[1] > 59 || values[2] > 59 {
			return 0, false
		}
		seconds = values[0]*3600 + values[1]*60 + values[2]
	}
	if seconds > maxPlaybackProgressSeconds {
		return 0, false
	}

	return seconds, true
}

func videoChaptersVTT(chapters []videoChapter) string {
	var builder strings.Builder
	builder.WriteString("WEBVTT\n\n")
	for _, chapter := range chapters {
		if chapter.EndSeconds <= chapter.StartSeconds {
			continue
		}
		builder.WriteString(formatVTTTimestamp(chapter.StartSeconds))
		builder.WriteString(" --> ")
		builder.WriteString(formatVTTTimestamp(chapter.EndSeconds))
		builder.WriteByte('\n')
		builder.WriteString(escapeVTTText(chapter.Label))
		builder.WriteString("\n\n")
	}

	return builder.String()
}

func escapeVTTText(value string) string {
	value = strings.ReplaceAll(value, "-->", "->")
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

func timelinePreviewVTT(preview *timelinePreview) string {
	var builder strings.Builder
	builder.WriteString("WEBVTT\n\n")
	if preview == nil {
		return builder.String()
	}
	for _, cue := range preview.Cues {
		if cue.EndSeconds <= cue.StartSeconds {
			continue
		}
		builder.WriteString(formatVTTTimestamp(cue.StartSeconds))
		builder.WriteString(" --> ")
		builder.WriteString(formatVTTTimestamp(cue.EndSeconds))
		builder.WriteByte('\n')
		builder.WriteString(preview.SpriteURL)
		builder.WriteString("#xywh=")
		builder.WriteString(strconv.Itoa(cue.X))
		builder.WriteByte(',')
		builder.WriteString(strconv.Itoa(cue.Y))
		builder.WriteByte(',')
		builder.WriteString(strconv.Itoa(cue.Width))
		builder.WriteByte(',')
		builder.WriteString(strconv.Itoa(cue.Height))
		builder.WriteString("\n\n")
	}

	return builder.String()
}

func formatVTTTimestamp(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	hours := seconds / 3600
	minutes := seconds % 3600 / 60
	remainingSeconds := seconds % 60

	return fmt.Sprintf("%02d:%02d:%02d.000", hours, minutes, remainingSeconds)
}

func loadSubtitleTracks(ctx context.Context, db *sql.DB, videoID string, mediaURLs mediaURLBuilder) ([]subtitleTrack, error) {
	rows, err := db.QueryContext(ctx, `
SELECT language, name, format, path
FROM subtitles
WHERE video_id = ? AND path != ''
ORDER BY language, source`, videoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tracks []subtitleTrack
	for rows.Next() {
		var track subtitleTrack
		var path string
		if err := rows.Scan(&track.Language, &track.Label, &track.Format, &path); err != nil {
			return nil, err
		}
		if track.Format != "vtt" {
			continue
		}
		track.URL = mediaURLs.SignedURL(path)
		if track.URL == "" {
			continue
		}
		if track.Label == "" {
			track.Label = track.Language
		}
		tracks = append(tracks, track)
	}

	return tracks, rows.Err()
}

type mediaURLBuilder struct {
	root   string
	signer *media.Signer
	ttl    time.Duration
}

func (b mediaURLBuilder) SignedURL(rawPath string) string {
	if b.signer == nil {
		return ""
	}
	mediaPath, ok := b.availablePath(rawPath)
	if !ok {
		return ""
	}
	values := b.signer.Query(mediaPath, time.Now().Add(b.ttl))
	if len(values) == 0 {
		return ""
	}

	return (&url.URL{Path: "/media/" + mediaPath, RawQuery: values.Encode()}).String()
}

func (b mediaURLBuilder) Available(rawPath string) bool {
	_, ok := b.availablePath(rawPath)

	return ok
}

func (b mediaURLBuilder) availablePath(rawPath string) (string, bool) {
	if rawPath == "" || b.root == "" {
		return "", false
	}
	mediaPath, err := assetpath.Clean(rawPath)
	if err != nil {
		return "", false
	}
	_, info, err := assetpath.Lstat(b.root, mediaPath)
	if err != nil || info.IsDir() {
		return "", false
	}

	return mediaPath, true
}

type frontendHandler struct {
	static fs.FS
	files  http.Handler
}

func frontend(static fs.FS) http.Handler {
	return frontendHandler{
		static: static,
		files:  http.FileServerFS(static),
	}
}

func (h frontendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "." || name == "" {
		h.serveIndex(w)
		return
	}

	file, err := h.static.Open(name)
	if err == nil {
		_ = file.Close()
		h.files.ServeHTTP(w, r)
		return
	}

	h.serveIndex(w)
}

func (h frontendHandler) serveIndex(w http.ResponseWriter) {
	index, err := fs.ReadFile(h.static, "index.html")
	if err != nil {
		http.Error(w, "frontend assets are not built", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(index)
}
