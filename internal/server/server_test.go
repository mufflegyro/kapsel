package server

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kapsel/internal/auth"
	"kapsel/internal/database"
	"kapsel/internal/diskspace"
	"kapsel/internal/download"
	"kapsel/internal/jobs"
	"kapsel/internal/media"
	"kapsel/internal/previews"
	"kapsel/internal/sponsorblock"
	"kapsel/internal/storage"
	"kapsel/internal/taimport"
)

func TestHealthEndpoint(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if body := rec.Body.String(); body != "OK\n" {
		t.Fatalf("expected body %q, got %q", "OK\n", body)
	}
}

func TestReadinessEndpointReportsYTDLPStatus(t *testing.T) {
	t.Parallel()

	runner := &serverYTDLPRunner{stdout: []byte("2026.03.17\n")}
	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/readiness", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithYTDLPDiagnostics("/opt/bin/yt-dlp", runner)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response struct {
		YTDLP download.YTDLPStatus `json:"yt_dlp"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !response.YTDLP.Available || response.YTDLP.Path != "/opt/bin/yt-dlp" || response.YTDLP.Version != "2026.03.17" {
		t.Fatalf("unexpected readiness response: %#v", response)
	}
	if len(runner.commands) != 1 || runner.commands[0].Name != "/opt/bin/yt-dlp" {
		t.Fatalf("expected readiness endpoint to check configured path, got %#v", runner.commands)
	}
}

func TestReadinessEndpointReportsDatabaseAndMediaRootStatus(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	mediaRoot := t.TempDir()
	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/readiness", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db), WithSupportedSchemaVersion(supportedSchemaVersion(t)), WithMedia(mediaRoot, media.NewSigner("test-secret"))).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response struct {
		Status   string `json:"status"`
		Database struct {
			OK                     bool   `json:"ok"`
			Connected              bool   `json:"connected"`
			SchemaVersion          int    `json:"schema_version"`
			SupportedSchemaVersion int    `json:"supported_schema_version"`
			Error                  string `json:"error"`
		} `json:"database"`
		MediaRoot struct {
			Path string `json:"path"`
			OK   bool   `json:"ok"`
		} `json:"media_root"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "pass" || !response.Database.OK || !response.Database.Connected || response.Database.SchemaVersion == 0 || response.Database.SchemaVersion != response.Database.SupportedSchemaVersion || response.Database.Error != "" {
		t.Fatalf("unexpected database readiness: %#v", response)
	}
	if !response.MediaRoot.OK || response.MediaRoot.Path != mediaRoot {
		t.Fatalf("unexpected media root readiness: %#v", response.MediaRoot)
	}
}

func TestReadinessEndpointReportsDatabaseFailure(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/readiness", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db), WithSupportedSchemaVersion(supportedSchemaVersion(t))).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response struct {
		Status   string `json:"status"`
		Database struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"database"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "error" || response.Database.OK || !strings.Contains(response.Database.Error, "database") {
		t.Fatalf("expected database readiness failure, got %#v", response)
	}
}

func TestReadinessEndpointRequiresSupportedSchemaVersion(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/readiness", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response struct {
		Status   string `json:"status"`
		Database struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"database"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "error" || response.Database.OK || !strings.Contains(response.Database.Error, "supported database schema version") {
		t.Fatalf("expected supported schema readiness failure, got %#v", response)
	}
}

func TestReadinessEndpointRejectsNonDirectoryMediaRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mediaPath := filepath.Join(root, "media-file")
	if err := os.WriteFile(mediaPath, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/readiness", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithMedia(mediaPath, media.NewSigner("test-secret"))).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response struct {
		Status    string `json:"status"`
		MediaRoot struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"media_root"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "error" || response.MediaRoot.OK || !strings.Contains(response.MediaRoot.Error, "not a directory") {
		t.Fatalf("expected media root readiness failure, got %#v", response)
	}
}

func TestReadinessEndpointRejectsSymlinkMediaRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	realRoot := filepath.Join(root, "real-media")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkRoot := filepath.Join(root, "media-link")
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/readiness", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithMedia(symlinkRoot, media.NewSigner("test-secret"))).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response struct {
		Status    string `json:"status"`
		MediaRoot struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"media_root"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "error" || response.MediaRoot.OK || !strings.Contains(response.MediaRoot.Error, "symlink") {
		t.Fatalf("expected symlink media root readiness failure, got %#v", response)
	}
}

func TestReadinessEndpointReportsMissingYTDLP(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/readiness", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithYTDLPDiagnostics("/missing/yt-dlp", &serverYTDLPRunner{err: exec.ErrNotFound})).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response struct {
		YTDLP download.YTDLPStatus `json:"yt_dlp"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.YTDLP.Available || !strings.Contains(response.YTDLP.Error, "yt-dlp unavailable") || !strings.Contains(response.YTDLP.Error, "/missing/yt-dlp") {
		t.Fatalf("unexpected readiness response: %#v", response)
	}
}

func TestReadinessEndpointReportsLowStorageSpace(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/readiness", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithStorageDiagnostics("/data", "/media", 1<<30, func(path string) (diskspace.Stats, error) {
		available := uint64(2 << 30)
		if path == "/media" {
			available = 512 << 20
		}
		return diskspace.Stats{Path: path, AvailableBytes: available}, nil
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response struct {
		Storage diskspace.Report `json:"storage"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Storage.OK || len(response.Storage.Paths) != 2 {
		t.Fatalf("expected low-space storage readiness, got %#v", response)
	}
	if response.Storage.Paths[1].Path != "/media" || !strings.Contains(response.Storage.Paths[1].Warning, "low disk space") {
		t.Fatalf("expected media low-space warning, got %#v", response.Storage.Paths[1])
	}
}

func TestReadinessEndpointSanitizesYTDLPError(t *testing.T) {
	t.Parallel()

	runner := &serverYTDLPRunner{
		stdout: []byte("ERROR: https://user:pass@example.com/watch?v=abc&token=secret#frag Authorization: Bearer supersecret"),
		err:    errors.New("exit status 1"),
	}
	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/readiness", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithYTDLPDiagnostics("yt-dlp", runner)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response struct {
		YTDLP download.YTDLPStatus `json:"yt_dlp"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.YTDLP.Available || !strings.Contains(response.YTDLP.Error, "https://example.com/watch") {
		t.Fatalf("unexpected readiness response: %#v", response)
	}
	for _, secret := range []string{"user:pass", "token=secret", "frag", "supersecret"} {
		if strings.Contains(response.YTDLP.Error, secret) {
			t.Fatalf("expected readiness error to redact %q, got %#v", secret, response)
		}
	}
}

func TestSettingsEndpointReportsRedactedConfiguration(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec := httptest.NewRecorder()

	NewHandler(
		WithSettingsDiagnostics(SettingsDiagnostics{
			Addr:                         "127.0.0.1:8080",
			AuthMode:                     "disabled",
			DataDir:                      "/srv/kapsel",
			DBPath:                       "/srv/kapsel/kapsel.db",
			ImportRoot:                   "/srv/kapsel/imports",
			MediaRoot:                    "/srv/kapsel/media",
			MediaSigningSecretConfigured: true,
			AuthenticationConfigured:     false,
			MediaURLTTL:                  time.Hour,
			MinFreeSpaceBytes:            1 << 30,
			PreviewsEnabled:              true,
			FFMPEGPath:                   "/usr/local/bin/ffmpeg",
			YTDLPPath:                    "/usr/local/bin/yt-dlp",
		}),
		WithYTDLPDiagnostics("/usr/local/bin/yt-dlp", &serverYTDLPRunner{stdout: []byte("2026.03.17\n")}),
		WithStorageDiagnostics("/srv/kapsel", "/srv/kapsel/media", 1<<30, func(path string) (diskspace.Stats, error) {
			return diskspace.Stats{Path: path, AvailableBytes: 2 << 30}, nil
		}),
	).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	body := rec.Body.String()
	for _, secret := range []string{"supersecret", "media_signing_secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("expected settings response to redact secret %q, got %s", secret, body)
		}
	}
	response := struct {
		Configuration struct {
			Addr       string `json:"addr"`
			DataDir    string `json:"data_dir"`
			DBPath     string `json:"db_path"`
			ImportRoot string `json:"import_root"`
			MediaRoot  string `json:"media_root"`
			YTDLPPath  string `json:"yt_dlp_path"`
		} `json:"configuration"`
		Checks []settingsCheckFixture `json:"checks"`
	}{}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Configuration.DataDir != "/srv/kapsel" || response.Configuration.DBPath != "/srv/kapsel/kapsel.db" || response.Configuration.YTDLPPath != "/usr/local/bin/yt-dlp" {
		t.Fatalf("unexpected settings configuration: %#v", response.Configuration)
	}
	if state := settingsCheckState(response.Checks, "media_signing"); state != "pass" {
		t.Fatalf("expected configured media signing to pass, got %q in %#v", state, response.Checks)
	}
	if state := settingsCheckState(response.Checks, "authentication"); state != "warn" {
		t.Fatalf("expected missing authentication to warn, got %q in %#v", state, response.Checks)
	}
	if state := settingsCheckState(response.Checks, "import_root"); state != "pass" {
		t.Fatalf("expected import root to pass, got %q in %#v", state, response.Checks)
	}
}

func TestSettingsEndpointReportsEphemeralSecretAndMissingToolWarnings(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec := httptest.NewRecorder()

	NewHandler(
		WithSettingsDiagnostics(SettingsDiagnostics{
			DataDir:                      "/data",
			DBPath:                       "/data/kapsel.db",
			ImportRoot:                   "/data/imports",
			MediaRoot:                    "/data/media",
			MediaSigningSecretConfigured: false,
			YTDLPPath:                    "/missing/yt-dlp",
		}),
		WithYTDLPDiagnostics("/missing/yt-dlp", &serverYTDLPRunner{err: exec.ErrNotFound}),
	).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	response := struct {
		Checks []settingsCheckFixture `json:"checks"`
	}{}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if state := settingsCheckState(response.Checks, "media_signing"); state != "warn" {
		t.Fatalf("expected generated media signing secret to warn, got %q in %#v", state, response.Checks)
	}
	if state := settingsCheckState(response.Checks, "yt_dlp"); state != "error" {
		t.Fatalf("expected missing yt-dlp to error, got %q in %#v", state, response.Checks)
	}
	if detail := settingsCheckDetail(response.Checks, "yt_dlp"); !strings.Contains(detail, "unavailable") || strings.Contains(detail, "supersecret") {
		t.Fatalf("expected redacted missing-tool detail, got %q", detail)
	}
}

func TestSettingsEndpointReportsLowStorageAsWarning(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec := httptest.NewRecorder()

	NewHandler(
		WithSettingsDiagnostics(SettingsDiagnostics{DataDir: "/data", MediaRoot: "/media"}),
		WithStorageDiagnostics("/data", "/media", 1<<30, func(path string) (diskspace.Stats, error) {
			available := uint64(2 << 30)
			if path == "/media" {
				available = 512 << 20
			}

			return diskspace.Stats{Path: path, AvailableBytes: available}, nil
		}),
	).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	response := struct {
		Checks []settingsCheckFixture `json:"checks"`
	}{}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if state := settingsCheckState(response.Checks, "storage"); state != "warn" {
		t.Fatalf("expected low storage to warn, got %q in %#v", state, response.Checks)
	}
	if detail := settingsCheckDetail(response.Checks, "storage"); !strings.Contains(detail, "low disk space") {
		t.Fatalf("expected low-space detail, got %q", detail)
	}
}

func TestSettingsEndpointIncludesStorageMaintenanceSummary(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mediaRoot := filepath.Join(root, "media")
	db := openServerTestDBAt(t, filepath.Join(root, "kapsel.db"))
	seedVideoWithAssets(t, db)
	writeServerFile(t, filepath.Join(mediaRoot, "videos", "sample.mp4"), "media")
	writeServerFile(t, filepath.Join(mediaRoot, "thumbs", "sample.jpg"), "thumb")
	writeServerFile(t, filepath.Join(mediaRoot, "orphan.bin"), "orphan")
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec := httptest.NewRecorder()

	NewHandler(
		WithDatabase(db),
		WithSettingsDiagnostics(SettingsDiagnostics{DataDir: root, DBPath: filepath.Join(root, "kapsel.db"), MediaRoot: mediaRoot}),
	).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	response := struct {
		StorageMaintenance storage.Summary `json:"storage_maintenance"`
	}{}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.StorageMaintenance.OrphanFiles != 1 || response.StorageMaintenance.MissingReferences != 0 {
		t.Fatalf("unexpected storage maintenance summary: %#v", response.StorageMaintenance)
	}
	if got := serverStorageUsageBytes(response.StorageMaintenance, storage.CategoryMedia); got != 5 {
		t.Fatalf("expected media usage 5 bytes, got %d", got)
	}
}

func TestFrontendShell(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(body), "Kapsel") {
		t.Fatalf("expected frontend shell to contain %q", "Kapsel")
	}
}

func TestSecurityHeadersOnAPIFrontendAndMediaResponses(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.mp4"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	signer := media.NewSigner("secret")
	mediaPath := "/media/sample.mp4?" + signer.Query("sample.mp4", time.Now().Add(time.Hour)).Encode()

	for _, test := range []struct {
		name        string
		handler     http.Handler
		path        string
		rangeHeader string
	}{
		{name: "api", handler: NewHandler(), path: "/api/health"},
		{name: "frontend", handler: NewHandler(), path: "/"},
		{name: "media", handler: NewHandler(WithMedia(root, signer)), path: mediaPath, rangeHeader: "bytes=0-3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.rangeHeader != "" {
				req.Header.Set("Range", test.rangeHeader)
			}
			rec := httptest.NewRecorder()

			test.handler.ServeHTTP(rec, req)

			if rec.Code < 200 || rec.Code >= 300 {
				t.Fatalf("expected successful response, got %d body=%s", rec.Code, rec.Body.String())
			}
			assertSecurityHeaders(t, rec.Result().Header)
		})
	}
}

func assertSecurityHeaders(t *testing.T, header http.Header) {
	t.Helper()

	if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected X-Content-Type-Options nosniff, got %q", got)
	}
	if got := header.Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("expected X-Frame-Options DENY, got %q", got)
	}
	if got := header.Get("Referrer-Policy"); got != "same-origin" {
		t.Fatalf("expected Referrer-Policy same-origin, got %q", got)
	}
	if got := header.Get("Content-Security-Policy"); got != securityHeadersCSP {
		t.Fatalf("unexpected Content-Security-Policy %q", got)
	}
}

func TestLoginEndpointSetsSessionCookie(t *testing.T) {
	t.Parallel()

	manager := newServerAuthManager(t, time.Now())
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"admin","password":"open sesame"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	NewHandler(WithAuth(manager)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected login status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != auth.SessionCookieName || cookies[0].Value == "" || !cookies[0].HttpOnly {
		t.Fatalf("expected session cookie, got %#v", cookies)
	}

	sessionReq := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	sessionReq.AddCookie(cookies[0])
	sessionRec := httptest.NewRecorder()
	NewHandler(WithAuth(manager)).ServeHTTP(sessionRec, sessionReq)
	if sessionRec.Code != http.StatusOK {
		t.Fatalf("expected session status %d, got %d", http.StatusOK, sessionRec.Code)
	}
	var session struct {
		Authenticated bool   `json:"authenticated"`
		Username      string `json:"username"`
	}
	if err := json.NewDecoder(sessionRec.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	if !session.Authenticated || session.Username != "admin" {
		t.Fatalf("unexpected session response: %#v", session)
	}
}

func TestJSONMutationEndpointsRejectOversizedBodies(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name       string
		method     string
		path       string
		body       string
		handler    http.Handler
		wantStatus int
		verify     func(*testing.T)
	}

	verifyNoJobs := func(db *sql.DB) func(*testing.T) {
		return func(t *testing.T) {
			t.Helper()
			var count int
			if err := db.QueryRow("SELECT count(*) FROM jobs").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("expected rejected payload not to enqueue jobs, got %d", count)
			}
		}
	}

	for _, test := range []testCase{
		{
			name:       "login",
			method:     http.MethodPost,
			path:       "/api/login",
			body:       `{"username":"admin","password":"open sesame"}` + strings.Repeat(" ", maxLoginPayloadBytes),
			handler:    NewHandler(WithAuth(newServerAuthManager(t, time.Now()))),
			wantStatus: http.StatusBadRequest,
		},
		func() testCase {
			db := openServerTestDB(t)
			store := jobs.NewStore(db)
			return testCase{
				name:       "download",
				method:     http.MethodPost,
				path:       "/api/downloads",
				body:       `{"url":"https://www.youtube.com/watch?v=abc123DEF45"}` + strings.Repeat(" ", maxDownloadPayloadBytes),
				handler:    NewHandler(WithJobs(store)),
				wantStatus: http.StatusBadRequest,
				verify:     verifyNoJobs(db),
			}
		}(),
		func() testCase {
			db := openServerTestDB(t)
			store := jobs.NewStore(db)
			return testCase{
				name:       "channel",
				method:     http.MethodPost,
				path:       "/api/channels",
				body:       `{"url":"https://www.youtube.com/@archive"}` + strings.Repeat(" ", maxChannelPayloadBytes),
				handler:    NewHandler(WithJobs(store)),
				wantStatus: http.StatusBadRequest,
				verify:     verifyNoJobs(db),
			}
		}(),
		func() testCase {
			db := openServerTestDB(t)
			store := jobs.NewStore(db)
			allowedRoot := t.TempDir()
			importRoot := filepath.Join(allowedRoot, "tubearchivist")
			if err := os.MkdirAll(importRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			return testCase{
				name:       "import",
				method:     http.MethodPost,
				path:       "/api/imports/tubearchivist",
				body:       `{"root":"` + filepath.ToSlash(importRoot) + `"}` + strings.Repeat(" ", maxTubeArchivistImportPayloadBytes),
				handler:    NewHandler(WithJobs(store), WithImportRoot(allowedRoot)),
				wantStatus: http.StatusBadRequest,
				verify:     verifyNoJobs(db),
			}
		}(),
		func() testCase {
			db := openServerTestDB(t)
			seedVideoWithAssets(t, db)
			return testCase{
				name:       "progress",
				method:     http.MethodPut,
				path:       "/api/videos/vid-1/progress",
				body:       `{"position_seconds":42,"duration_seconds":120}` + strings.Repeat(" ", maxPlaybackProgressPayloadBytes),
				handler:    NewHandler(WithDatabase(db)),
				wantStatus: http.StatusBadRequest,
				verify: func(t *testing.T) {
					t.Helper()
					var count int
					if err := db.QueryRow("SELECT count(*) FROM user_progress").Scan(&count); err != nil {
						t.Fatal(err)
					}
					if count != 0 {
						t.Fatalf("expected rejected payload not to write progress, got %d rows", count)
					}
				},
			}
		}(),
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			test.handler.ServeHTTP(rec, req)

			if rec.Code != test.wantStatus {
				t.Fatalf("expected status %d, got %d body=%s", test.wantStatus, rec.Code, rec.Body.String())
			}
			if test.verify != nil {
				test.verify(t)
			}
		})
	}
}

func TestJSONMutationEndpointsRejectUnknownFieldsAndTrailingJSON(t *testing.T) {
	t.Parallel()

	type invalidBody struct {
		name string
		body string
	}
	type testCase struct {
		name    string
		method  string
		path    string
		bodies  []invalidBody
		handler http.Handler
		verify  func(*testing.T, *httptest.ResponseRecorder)
	}

	verifyNoCookies := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		if cookies := rec.Result().Cookies(); len(cookies) != 0 {
			t.Fatalf("expected rejected payload not to set cookies, got %#v", cookies)
		}
	}
	verifyNoJobs := func(db *sql.DB) func(*testing.T, *httptest.ResponseRecorder) {
		return func(t *testing.T, rec *httptest.ResponseRecorder) {
			t.Helper()
			var count int
			if err := db.QueryRow("SELECT count(*) FROM jobs").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("expected rejected payload not to enqueue jobs, got %d", count)
			}
		}
	}
	verifyNoProgress := func(db *sql.DB) func(*testing.T, *httptest.ResponseRecorder) {
		return func(t *testing.T, rec *httptest.ResponseRecorder) {
			t.Helper()
			var count int
			if err := db.QueryRow("SELECT count(*) FROM user_progress").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("expected rejected payload not to write progress, got %d rows", count)
			}
		}
	}

	for _, test := range []testCase{
		{
			name:   "login",
			method: http.MethodPost,
			path:   "/api/login",
			bodies: []invalidBody{
				{name: "unknown-field", body: `{"username":"admin","password":"open sesame","extra":true}`},
				{name: "trailing-json", body: `{"username":"admin","password":"open sesame"} {}`},
			},
			handler: NewHandler(WithAuth(newServerAuthManager(t, time.Now()))),
			verify:  verifyNoCookies,
		},
		func() testCase {
			db := openServerTestDB(t)
			store := jobs.NewStore(db)
			return testCase{
				name:   "download",
				method: http.MethodPost,
				path:   "/api/downloads",
				bodies: []invalidBody{
					{name: "unknown-field", body: `{"url":"https://www.youtube.com/watch?v=abc123DEF45","extra":true}`},
					{name: "trailing-json", body: `{"url":"https://www.youtube.com/watch?v=abc123DEF45"} {}`},
				},
				handler: NewHandler(WithJobs(store)),
				verify:  verifyNoJobs(db),
			}
		}(),
		func() testCase {
			db := openServerTestDB(t)
			store := jobs.NewStore(db)
			return testCase{
				name:   "channel",
				method: http.MethodPost,
				path:   "/api/channels",
				bodies: []invalidBody{
					{name: "unknown-field", body: `{"url":"https://www.youtube.com/@archive","extra":true}`},
					{name: "trailing-json", body: `{"url":"https://www.youtube.com/@archive"} {}`},
				},
				handler: NewHandler(WithJobs(store)),
				verify:  verifyNoJobs(db),
			}
		}(),
		func() testCase {
			db := openServerTestDB(t)
			store := jobs.NewStore(db)
			allowedRoot := t.TempDir()
			importRoot := filepath.Join(allowedRoot, "tubearchivist")
			if err := os.MkdirAll(importRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			return testCase{
				name:   "import",
				method: http.MethodPost,
				path:   "/api/imports/tubearchivist",
				bodies: []invalidBody{
					{name: "unknown-field", body: `{"root":"` + filepath.ToSlash(importRoot) + `","extra":true}`},
					{name: "trailing-json", body: `{"root":"` + filepath.ToSlash(importRoot) + `"} {}`},
				},
				handler: NewHandler(WithJobs(store), WithImportRoot(allowedRoot)),
				verify:  verifyNoJobs(db),
			}
		}(),
		func() testCase {
			db := openServerTestDB(t)
			seedVideoWithAssets(t, db)
			return testCase{
				name:   "progress",
				method: http.MethodPut,
				path:   "/api/videos/vid-1/progress",
				bodies: []invalidBody{
					{name: "unknown-field", body: `{"position_seconds":42,"duration_seconds":120,"extra":true}`},
					{name: "trailing-json", body: `{"position_seconds":42,"duration_seconds":120} {}`},
				},
				handler: NewHandler(WithDatabase(db)),
				verify:  verifyNoProgress(db),
			}
		}(),
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			for _, body := range test.bodies {
				body := body
				t.Run(body.name, func(t *testing.T) {
					req := httptest.NewRequest(test.method, test.path, strings.NewReader(body.body))
					req.Header.Set("Content-Type", "application/json")
					rec := httptest.NewRecorder()

					test.handler.ServeHTTP(rec, req)

					if rec.Code != http.StatusBadRequest {
						t.Fatalf("expected status %d for %s, got %d body=%s", http.StatusBadRequest, body.body, rec.Code, rec.Body.String())
					}
					if test.verify != nil {
						test.verify(t, rec)
					}
				})
			}
		})
	}
}

func TestBodylessMutationEndpointsRejectUnexpectedBodies(t *testing.T) {
	t.Parallel()

	type requestCase struct {
		method         string
		path           string
		handler        http.Handler
		successStatus  int
		verifyAccepted func(*testing.T, *httptest.ResponseRecorder)
		verifyRejected func(*testing.T, *httptest.ResponseRecorder)
	}
	type endpointCase struct {
		name  string
		setup func(*testing.T) requestCase
	}

	for _, endpoint := range []endpointCase{
		{
			name: "logout",
			setup: func(t *testing.T) requestCase {
				manager := newServerAuthManager(t, time.Now())
				return requestCase{
					method:        http.MethodPost,
					path:          "/api/logout",
					handler:       NewHandler(WithAuth(manager)),
					successStatus: http.StatusOK,
					verifyAccepted: func(t *testing.T, rec *httptest.ResponseRecorder) {
						t.Helper()
						if cookies := rec.Result().Cookies(); len(cookies) != 1 || cookies[0].Name != auth.SessionCookieName {
							t.Fatalf("expected logout to clear session cookie, got %#v", cookies)
						}
					},
					verifyRejected: func(t *testing.T, rec *httptest.ResponseRecorder) {
						t.Helper()
						if cookies := rec.Result().Cookies(); len(cookies) != 0 {
							t.Fatalf("expected rejected logout not to set cookies, got %#v", cookies)
						}
					},
				}
			},
		},
		{
			name: "job-cancel",
			setup: func(t *testing.T) requestCase {
				db := openServerTestDB(t)
				store := jobs.NewStore(db)
				job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{Type: "download"})
				if err != nil {
					t.Fatal(err)
				}
				return requestCase{
					method:        http.MethodPost,
					path:          "/api/jobs/" + job.ID + "/cancel",
					handler:       NewHandler(WithJobs(store)),
					successStatus: http.StatusOK,
					verifyAccepted: func(t *testing.T, _ *httptest.ResponseRecorder) {
						t.Helper()
						stored, err := store.Get(context.Background(), job.ID)
						if err != nil {
							t.Fatal(err)
						}
						if stored.Status != jobs.StatusCancelled || !stored.CancelRequested {
							t.Fatalf("expected no-body cancel to cancel job, got %#v", stored)
						}
					},
					verifyRejected: func(t *testing.T, _ *httptest.ResponseRecorder) {
						t.Helper()
						stored, err := store.Get(context.Background(), job.ID)
						if err != nil {
							t.Fatal(err)
						}
						if stored.Status != jobs.StatusQueued || stored.CancelRequested {
							t.Fatalf("expected rejected cancel not to mutate job, got %#v", stored)
						}
					},
				}
			},
		},
		{
			name: "job-retry",
			setup: func(t *testing.T) requestCase {
				db := openServerTestDB(t)
				store := jobs.NewStore(db)
				job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{Type: "download", MaxAttempts: 1})
				if err != nil {
					t.Fatal(err)
				}
				if _, ok, err := store.Claim(context.Background(), time.Now().Add(time.Second), time.Hour); err != nil || !ok {
					t.Fatalf("expected job claim, ok=%v err=%v", ok, err)
				}
				if err := store.Fail(context.Background(), job.ID, errors.New("yt-dlp unavailable"), time.Now()); err != nil {
					t.Fatal(err)
				}
				return requestCase{
					method:        http.MethodPost,
					path:          "/api/jobs/" + job.ID + "/retry",
					handler:       NewHandler(WithJobs(store)),
					successStatus: http.StatusOK,
					verifyAccepted: func(t *testing.T, _ *httptest.ResponseRecorder) {
						t.Helper()
						stored, err := store.Get(context.Background(), job.ID)
						if err != nil {
							t.Fatal(err)
						}
						if stored.Status != jobs.StatusQueued || stored.Attempts != 1 || stored.MaxAttempts != 2 {
							t.Fatalf("expected no-body retry to requeue job, got %#v", stored)
						}
					},
					verifyRejected: func(t *testing.T, _ *httptest.ResponseRecorder) {
						t.Helper()
						stored, err := store.Get(context.Background(), job.ID)
						if err != nil {
							t.Fatal(err)
						}
						if stored.Status != jobs.StatusFailed || stored.MaxAttempts != 1 {
							t.Fatalf("expected rejected retry not to mutate job, got %#v", stored)
						}
					},
				}
			},
		},
		{
			name: "channel-scan",
			setup: func(t *testing.T) requestCase {
				db := openServerTestDB(t)
				store := jobs.NewStore(db)
				if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Archive Workshop')"); err != nil {
					t.Fatal(err)
				}
				return requestCase{
					method:        http.MethodPost,
					path:          "/api/channels/chan-1/scan",
					handler:       NewHandler(WithDatabase(db), WithJobs(store)),
					successStatus: http.StatusAccepted,
					verifyAccepted: func(t *testing.T, _ *httptest.ResponseRecorder) {
						t.Helper()
						assertServerScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(1), download.ChannelScanJobType)
					},
					verifyRejected: func(t *testing.T, _ *httptest.ResponseRecorder) {
						t.Helper()
						assertServerScalar(t, db, "SELECT count(*) FROM jobs", int64(0))
					},
				}
			},
		},
		{
			name: "catalog-video-download",
			setup: func(t *testing.T) requestCase {
				db := openServerTestDB(t)
				store := jobs.NewStore(db)
				if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Archive Workshop')"); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description)
VALUES ('abc123DEF45', 'abc123DEF45', 'chan-1', 'Catalog Video', 'Catalog-only metadata')`); err != nil {
					t.Fatal(err)
				}
				return requestCase{
					method:        http.MethodPost,
					path:          "/api/videos/abc123DEF45/download",
					handler:       NewHandler(WithDatabase(db), WithJobs(store)),
					successStatus: http.StatusAccepted,
					verifyAccepted: func(t *testing.T, _ *httptest.ResponseRecorder) {
						t.Helper()
						assertServerScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(1), download.JobType)
					},
					verifyRejected: func(t *testing.T, _ *httptest.ResponseRecorder) {
						t.Helper()
						assertServerScalar(t, db, "SELECT count(*) FROM jobs", int64(0))
					},
				}
			},
		},
		{
			name: "video-media-delete",
			setup: func(t *testing.T) requestCase {
				db := openServerTestDB(t)
				mediaRoot := t.TempDir()
				seedVideoWithAssets(t, db)
				writeServerFile(t, filepath.Join(mediaRoot, "videos", "sample.mp4"), "video")
				if _, err := db.Exec("INSERT INTO media_assets (owner_type, owner_id, kind, path) VALUES ('video', 'vid-1', 'media', 'videos/sample.mp4')"); err != nil {
					t.Fatal(err)
				}
				return requestCase{
					method:        http.MethodDelete,
					path:          "/api/videos/vid-1/media",
					handler:       NewHandler(WithDatabase(db), WithMedia(mediaRoot, media.NewSigner("secret"))),
					successStatus: http.StatusNoContent,
					verifyAccepted: func(t *testing.T, _ *httptest.ResponseRecorder) {
						t.Helper()
						assertServerScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "", "vid-1")
						assertServerScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'media'", int64(0), "vid-1")
					},
					verifyRejected: func(t *testing.T, _ *httptest.ResponseRecorder) {
						t.Helper()
						assertServerScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "videos/sample.mp4", "vid-1")
						assertServerScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'media'", int64(1), "vid-1")
					},
				}
			},
		},
		{
			name: "channel-delete",
			setup: func(t *testing.T) requestCase {
				db := openServerTestDB(t)
				if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-empty', 'chan-empty', 'Empty Channel')"); err != nil {
					t.Fatal(err)
				}
				return requestCase{
					method:        http.MethodDelete,
					path:          "/api/channels/chan-empty",
					handler:       NewHandler(WithDatabase(db)),
					successStatus: http.StatusNoContent,
					verifyAccepted: func(t *testing.T, _ *httptest.ResponseRecorder) {
						t.Helper()
						assertServerScalar(t, db, "SELECT count(*) FROM channels WHERE id = ?", int64(0), "chan-empty")
					},
					verifyRejected: func(t *testing.T, _ *httptest.ResponseRecorder) {
						t.Helper()
						assertServerScalar(t, db, "SELECT count(*) FROM channels WHERE id = ?", int64(1), "chan-empty")
					},
				}
			},
		},
		{
			name: "playlist-delete",
			setup: func(t *testing.T) requestCase {
				db := openServerTestDB(t)
				seedVideoList(t, db, 2)
				return requestCase{
					method:        http.MethodDelete,
					path:          "/api/playlists/playlist-1",
					handler:       NewHandler(WithDatabase(db)),
					successStatus: http.StatusNoContent,
					verifyAccepted: func(t *testing.T, _ *httptest.ResponseRecorder) {
						t.Helper()
						assertServerScalar(t, db, "SELECT count(*) FROM playlists WHERE id = ?", int64(0), "playlist-1")
					},
					verifyRejected: func(t *testing.T, _ *httptest.ResponseRecorder) {
						t.Helper()
						assertServerScalar(t, db, "SELECT count(*) FROM playlists WHERE id = ?", int64(1), "playlist-1")
						assertServerScalar(t, db, "SELECT count(*) FROM playlist_entries WHERE playlist_id = ?", int64(2), "playlist-1")
					},
				}
			},
		},
	} {
		endpoint := endpoint
		t.Run(endpoint.name, func(t *testing.T) {
			for _, body := range []struct {
				name string
				body string
			}{
				{name: "non-empty", body: `{}`},
				{name: "oversized", body: strings.Repeat("x", 4096)},
			} {
				body := body
				t.Run(body.name, func(t *testing.T) {
					test := endpoint.setup(t)
					req := httptest.NewRequest(test.method, test.path, strings.NewReader(body.body))
					req.Header.Set("Content-Type", "application/json")
					rec := httptest.NewRecorder()

					test.handler.ServeHTTP(rec, req)

					if rec.Code != http.StatusBadRequest {
						t.Fatalf("expected status %d, got %d body=%s", http.StatusBadRequest, rec.Code, rec.Body.String())
					}
					if test.verifyRejected != nil {
						test.verifyRejected(t, rec)
					}
				})
			}

			t.Run("no-body", func(t *testing.T) {
				test := endpoint.setup(t)
				req := httptest.NewRequest(test.method, test.path, nil)
				rec := httptest.NewRecorder()

				test.handler.ServeHTTP(rec, req)

				if rec.Code != test.successStatus {
					t.Fatalf("expected status %d, got %d body=%s", test.successStatus, rec.Code, rec.Body.String())
				}
				if test.verifyAccepted != nil {
					test.verifyAccepted(t, rec)
				}
			})
		})
	}
}

func TestLoginEndpointRejectsInvalidPassword(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	NewHandler(WithAuth(newServerAuthManager(t, time.Now()))).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected login failure status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("expected failed login not to set cookies, got %#v", cookies)
	}
}

func TestLoginEndpointRateLimitsRepeatedFailures(t *testing.T) {
	t.Parallel()

	handler := NewHandler(WithAuth(newServerAuthManager(t, time.Now())))
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected failed login %d to return %d, got %d", i+1, http.StatusUnauthorized, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected repeated failures to return %d, got %d", http.StatusTooManyRequests, rec.Code)
	}
}

func TestProtectedRouteRejectsUnauthenticatedRequest(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	req := httptest.NewRequest(http.MethodPost, "/api/downloads", strings.NewReader(`{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store), WithAuth(newServerAuthManager(t, time.Now()))).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM jobs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected unauthenticated request not to enqueue jobs, got %d", count)
	}
}

func TestProtectedRouteRejectsExpiredSession(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	manager := newServerAuthManager(t, now)
	cookie := manager.SessionCookie("admin")
	expired := newServerAuthManager(t, now.Add(2*time.Hour))
	req := httptest.NewRequest(http.MethodPost, "/api/downloads", strings.NewReader(`{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store), WithAuth(expired)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected expired session status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestProtectedRouteAllowsAuthenticatedRequest(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	manager := newServerAuthManager(t, time.Now())
	req := httptest.NewRequest(http.MethodPost, "/api/downloads", strings.NewReader(`{"url":"https://www.youtube.com/watch?v=abc123DEF45"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(manager.SessionCookie("admin"))
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store), WithAuth(manager)).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected authenticated status %d, got %d body=%s", http.StatusAccepted, rec.Code, rec.Body.String())
	}
}

func TestGetJobEndpoint(t *testing.T) {
	t.Parallel()

	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "kapsel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{Type: "download", PayloadJSON: `{"secret":"top-secret"}`})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/"+job.ID, nil)
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	response := decodePublicJobResponse(t, rec.Body.String())
	if response.ID != job.ID || response.Status != jobs.StatusQueued {
		t.Fatalf("unexpected job response: %#v", response)
	}
}

func TestGetJobEndpointIncludesResultSummary(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	seedServerJob(t, db, store, "summary-job", jobs.StatusSucceeded, "2026-05-03T12:00:00Z", `{"video_id":"vid-1"}`)
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/summary-job", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	response := decodePublicJobResponse(t, rec.Body.String())
	if response.ID != "summary-job" || response.ResultSummary != `{"video_id":"vid-1"}` {
		t.Fatalf("unexpected job summary response: %#v", response)
	}
}

func TestListJobsEndpointPaginatesFiltersAndOmitsPayload(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	seedServerJob(t, db, store, "queued-old", jobs.StatusQueued, "2026-05-03T10:00:00Z", "{}")
	seedServerJob(t, db, store, "failed-old", jobs.StatusFailed, "2026-05-03T11:00:00Z", `{"video_id":"old"}`)
	seedServerJob(t, db, store, "failed-new", jobs.StatusFailed, "2026-05-03T12:00:00Z", `{"video_id":"new"}`)
	req := httptest.NewRequest(http.MethodGet, "/api/jobs?status=failed&page=1&page_size=1", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "payload_json") || strings.Contains(body, "result_json") || strings.Contains(body, "top-secret") {
		t.Fatalf("expected job list to omit payload history, got %s", body)
	}
	response := struct {
		Data []struct {
			ID            string      `json:"id"`
			Type          string      `json:"type"`
			Status        jobs.Status `json:"status"`
			Progress      float64     `json:"progress"`
			Error         string      `json:"error"`
			ResultSummary string      `json:"result_summary"`
			UpdatedAt     string      `json:"updated_at"`
		} `json:"data"`
		Pagination pagination `json:"pagination"`
	}{}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Pagination.Total != 2 || response.Pagination.PageSize != 1 || len(response.Data) != 1 {
		t.Fatalf("unexpected job list response: %#v", response)
	}
	if response.Data[0].ID != "failed-new" || response.Data[0].Status != jobs.StatusFailed || response.Data[0].Error == "" || response.Data[0].ResultSummary == "" {
		t.Fatalf("unexpected listed job: %#v", response.Data[0])
	}
}

func TestDiagnosticsErrorsEndpointBoundsAndRedactsFailedJobs(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	longSecret := strings.Repeat("x", 2000)
	for i := 0; i < 60; i++ {
		id := fmt.Sprintf("failed-%02d", i)
		if _, err := store.Enqueue(context.Background(), jobs.EnqueueParams{ID: id, Type: "download", PayloadJSON: `{"token":"top-secret"}`}); err != nil {
			t.Fatal(err)
		}
		_, err := db.Exec(`
UPDATE jobs
SET status = ?, error = ?, updated_at = ?, completed_at = ?
WHERE id = ?`, jobs.StatusFailed, "download failed for https://user:pass@example.com/watch?v=abc&token=secret Authorization: Bearer supersecret password: "+longSecret, fmt.Sprintf("2026-05-03T12:%02d:00Z", i), fmt.Sprintf("2026-05-03T12:%02d:00Z", i), id)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/errors?limit=1000", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, secret := range []string{"payload_json", "top-secret", "user:pass", "token=secret", "supersecret", longSecret} {
		if strings.Contains(body, secret) {
			t.Fatalf("expected diagnostic errors response to redact %q, got %s", secret, body)
		}
	}
	var response struct {
		Limit int `json:"limit"`
		Data  []struct {
			JobID  string `json:"job_id"`
			Type   string `json:"type"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"data"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Limit != 50 || len(response.Data) != 50 {
		t.Fatalf("expected bounded diagnostics response, got %#v", response)
	}
	if response.Data[0].JobID != "failed-59" || response.Data[0].Type != "download" || response.Data[0].Status != string(jobs.StatusFailed) || len(response.Data[0].Error) > 1300 {
		t.Fatalf("unexpected diagnostic error entry: %#v", response.Data[0])
	}
}

func TestListJobsEndpointRejectsInvalidStatusFilter(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/jobs?status=failed,unknown", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(jobs.NewStore(openServerTestDB(t)))).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid job status") {
		t.Fatalf("expected invalid status error, got %s", rec.Body.String())
	}
}

func TestCancelJobEndpointCancelsQueuedAndRunningJobs(t *testing.T) {
	t.Parallel()

	store := jobs.NewStore(openServerTestDB(t))
	queued, err := store.Enqueue(context.Background(), jobs.EnqueueParams{Type: "download"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/jobs/"+queued.ID+"/cancel", nil)
	rec := httptest.NewRecorder()
	handler := NewHandler(WithJobs(store))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	cancelled := decodePublicJobResponse(t, rec.Body.String())
	if cancelled.Status != jobs.StatusCancelled || !cancelled.CancelRequested {
		t.Fatalf("expected cancelled queued job response, got %#v", cancelled)
	}

	running, err := store.Enqueue(context.Background(), jobs.EnqueueParams{Type: "download"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != running.ID {
		t.Fatalf("expected running job claim, ok=%v job=%#v", ok, claimed)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/jobs/"+running.ID+"/cancel", nil)
	rec = httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	marked := decodePublicJobResponse(t, rec.Body.String())
	if marked.Status != jobs.StatusRunning || !marked.CancelRequested {
		t.Fatalf("expected running job marked for cancellation, got %#v", marked)
	}
}

func TestCancelJobEndpointRejectsInvalidTransition(t *testing.T) {
	t.Parallel()

	store := jobs.NewStore(openServerTestDB(t))
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{Type: "download"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected job claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.Complete(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/jobs/"+job.ID+"/cancel", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store)).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

func TestRetryJobEndpointRetriesFailedJobAndRejectsUnsafeRetry(t *testing.T) {
	t.Parallel()

	store := jobs.NewStore(openServerTestDB(t))
	failed, err := store.Enqueue(context.Background(), jobs.EnqueueParams{Type: "download", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != failed.ID {
		t.Fatalf("expected failed job claim, ok=%v job=%#v", ok, claimed)
	}
	if err := store.Fail(context.Background(), failed.ID, errors.New("yt-dlp unavailable"), time.Now()); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/jobs/"+failed.ID+"/retry", nil)
	rec := httptest.NewRecorder()
	handler := NewHandler(WithJobs(store))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	retried := decodePublicJobResponse(t, rec.Body.String())
	if retried.Status != jobs.StatusQueued || retried.Attempts != 1 || retried.MaxAttempts != 2 || retried.Error == "" {
		t.Fatalf("expected queued retry preserving history, got %#v", retried)
	}

	unsafeDB := openServerTestDB(t)
	unsafeStore := jobs.NewStore(unsafeDB)
	unsafe, err := unsafeStore.Enqueue(context.Background(), jobs.EnqueueParams{Type: "download", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err = unsafeStore.Claim(context.Background(), time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != unsafe.ID {
		t.Fatalf("expected unsafe job claim, ok=%v job=%#v", ok, claimed)
	}
	markServerJobRunningWithCommittedResult(t, unsafeDB, unsafe.ID, `{"video_id":"committed"}`)
	if err := unsafeStore.Fail(context.Background(), unsafe.ID, errors.New("after commit"), time.Now()); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/jobs/"+unsafe.ID+"/retry", nil)
	rec = httptest.NewRecorder()

	NewHandler(WithJobs(unsafeStore)).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

func TestCreateDownloadEndpoint(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	req := httptest.NewRequest(http.MethodPost, "/api/downloads", strings.NewReader(`{"url":" https://www.youtube.com/shorts/abc123DEF45?feature=share ","origin":"channel_auto"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler := NewHandler(WithDatabase(db), WithJobs(store))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, rec.Code)
	}
	response := decodePublicJobResponse(t, rec.Body.String())
	if response.Type != "download" || response.Status != jobs.StatusQueued {
		t.Fatalf("unexpected download job response: %#v", response)
	}
	var payload download.Payload
	if err := json.Unmarshal([]byte(serverJobPayloadJSON(t, db, response.ID)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.URL != "https://www.youtube.com/watch?v=abc123DEF45" {
		t.Fatalf("expected normalized direct video payload, got %#v", payload)
	}
	if payload.Origin != "" {
		t.Fatalf("expected public download endpoint to ignore client-supplied origin, got %#v", payload)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/downloads", strings.NewReader(`{"url":"https://youtu.be/abc123DEF45"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected duplicate status %d, got %d body=%s", http.StatusAccepted, rec.Code, rec.Body.String())
	}
	duplicate := decodePublicJobResponse(t, rec.Body.String())
	if duplicate.ID != response.ID || duplicate.Type != download.JobType || duplicate.Status != jobs.StatusQueued {
		t.Fatalf("unexpected duplicate download job response: %#v", duplicate)
	}
	assertServerScalar(t, db, "SELECT count(*) FROM jobs WHERE type = ?", int64(1), download.JobType)
}

func TestCreateDownloadEndpointRejectsUnsupportedURLScheme(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	req := httptest.NewRequest(http.MethodPost, "/api/downloads", strings.NewReader(`{"url":"file:///etc/passwd"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store)).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM jobs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected invalid download not to enqueue a job, got %d", count)
	}
}

func TestCreateDownloadEndpointRejectsNonYouTubeURL(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	req := httptest.NewRequest(http.MethodPost, "/api/downloads", strings.NewReader(`{"url":"https://example.com/watch?v=abc"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store)).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM jobs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected non-YouTube download not to enqueue a job, got %d", count)
	}
}

func TestCreateDownloadEndpointRejectsYouTubeNonVideoURL(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	req := httptest.NewRequest(http.MethodPost, "/api/downloads", strings.NewReader(`{"url":"https://www.youtube.com/@archive"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store)).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM jobs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected invalid direct download not to enqueue a job, got %d", count)
	}
}

func TestCreateChannelEndpoint(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	req := httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader(`{"url":"https://www.youtube.com/@archive"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store)).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, rec.Code)
	}
	response := decodePublicJobResponse(t, rec.Body.String())
	if response.Type != download.ChannelJobType || response.Status != jobs.StatusQueued {
		t.Fatalf("unexpected channel job response: %#v", response)
	}
}

func TestCreateChannelScanEndpointEnqueuesDurableScan(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Archive Workshop')"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/channels/chan-1/scan", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db), WithJobs(store)).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusAccepted, rec.Code, rec.Body.String())
	}
	response := decodePublicJobResponse(t, rec.Body.String())
	if response.Type != download.ChannelScanJobType || response.Status != jobs.StatusQueued {
		t.Fatalf("unexpected scan job response: %#v", response)
	}
	var payload struct {
		URL       string `json:"url"`
		ChannelID string `json:"channel_id"`
	}
	if err := json.Unmarshal([]byte(serverJobPayloadJSON(t, db, response.ID)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.URL != "https://www.youtube.com/channel/chan-1" || payload.ChannelID != "chan-1" {
		t.Fatalf("unexpected scan payload: %#v", payload)
	}
}

func TestUpdateChannelSubscriptionEndpoint(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 0)"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/channels/chan-1/subscription", strings.NewReader(`{"subscribed":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response struct {
		ID         string `json:"id"`
		Subscribed bool   `json:"subscribed"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.ID != "chan-1" || !response.Subscribed {
		t.Fatalf("unexpected subscription response: %#v", response)
	}
	assertServerScalar(t, db, "SELECT subscribed FROM channels WHERE id = ?", int64(1), "chan-1")
}

func TestUpdateChannelSubscriptionEndpointRejectsInvalidPayload(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, subscribed) VALUES ('chan-1', 'chan-1', 'Archive Workshop', 1)"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/channels/chan-1/subscription", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
	assertServerScalar(t, db, "SELECT subscribed FROM channels WHERE id = ?", int64(1), "chan-1")
}

func TestUpdateChannelSubscriptionEndpointMissingChannel(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	req := httptest.NewRequest(http.MethodPut, "/api/channels/missing/subscription", strings.NewReader(`{"subscribed":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestCreateCatalogVideoDownloadEndpointEnqueuesSelectedVideo(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Archive Workshop')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description)
VALUES ('abc123DEF45', 'abc123DEF45', 'chan-1', 'Catalog Video', 'Catalog-only metadata')`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/videos/abc123DEF45/download", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db), WithJobs(store)).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusAccepted, rec.Code, rec.Body.String())
	}
	response := decodePublicJobResponse(t, rec.Body.String())
	if response.Type != download.JobType || response.Status != jobs.StatusQueued {
		t.Fatalf("unexpected selected download job response: %#v", response)
	}
	var payload download.Payload
	if err := json.Unmarshal([]byte(serverJobPayloadJSON(t, db, response.ID)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.URL != "https://www.youtube.com/watch?v=abc123DEF45" {
		t.Fatalf("unexpected selected download payload: %#v", payload)
	}
}

func TestCreateCatalogVideoDownloadEndpointSuppressesActiveDuplicate(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Archive Workshop')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description)
VALUES ('abc123DEF45', 'abc123DEF45', 'chan-1', 'Catalog Video', 'Catalog-only metadata')`); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(WithDatabase(db), WithJobs(store))

	req := httptest.NewRequest(http.MethodPost, "/api/videos/abc123DEF45/download", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected initial status %d, got %d", http.StatusAccepted, rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/videos/abc123DEF45/download", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected duplicate status %d, got %d body=%s", http.StatusAccepted, rec.Code, rec.Body.String())
	}
	response := decodePublicJobResponse(t, rec.Body.String())
	if response.Type != download.JobType || response.Status != jobs.StatusQueued {
		t.Fatalf("unexpected duplicate selected download job response: %#v", response)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM jobs WHERE type = ?", download.JobType).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one active selected download job, got %d", count)
	}
}

func TestCreateChannelEndpointRejectsUnsupportedURLScheme(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	req := httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader(`{"url":"file:///etc/passwd"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store)).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM jobs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected invalid channel not to enqueue a job, got %d", count)
	}
}

func TestCreateChannelEndpointRejectsNonChannelURL(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	req := httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader(`{"url":"https://www.youtube.com/watch?v=abc"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store)).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestCreateTubeArchivistImportEndpoint(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	allowedRoot := t.TempDir()
	importRoot := filepath.Join(allowedRoot, "tubearchivist")
	if err := os.MkdirAll(importRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/imports/tubearchivist", strings.NewReader(`{"root":"`+filepath.ToSlash(importRoot)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store), WithImportRoot(allowedRoot)).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, rec.Code)
	}
	response := decodePublicJobResponse(t, rec.Body.String())
	if response.Type != taimport.JobType || response.Status != jobs.StatusQueued {
		t.Fatalf("unexpected import job response: %#v", response)
	}
	var payload taimport.Payload
	if err := json.Unmarshal([]byte(serverJobPayloadJSON(t, db, response.ID)), &payload); err != nil {
		t.Fatal(err)
	}
	resolvedImportRoot, err := filepath.EvalSymlinks(importRoot)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Root != resolvedImportRoot {
		t.Fatalf("unexpected import payload: %#v", payload)
	}
}

func TestCreateTubeArchivistImportEndpointRejectsMissingRoot(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	allowedRoot := t.TempDir()
	req := httptest.NewRequest(http.MethodPost, "/api/imports/tubearchivist", strings.NewReader(`{"root":" "}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store), WithImportRoot(allowedRoot)).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM jobs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected invalid import not to enqueue a job, got %d", count)
	}
}

func TestCreateTubeArchivistImportEndpointRejectsOutsideImportRoot(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	allowedRoot := t.TempDir()
	outsideRoot := t.TempDir()
	req := httptest.NewRequest(http.MethodPost, "/api/imports/tubearchivist", strings.NewReader(`{"root":"`+filepath.ToSlash(outsideRoot)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store), WithImportRoot(allowedRoot)).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM jobs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected outside import not to enqueue a job, got %d", count)
	}
}

func TestCreateTubeArchivistImportEndpointRejectsSymlinkOutsideImportRoot(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	allowedRoot := t.TempDir()
	outsideRoot := t.TempDir()
	symlinkRoot := filepath.Join(allowedRoot, "outside-link")
	if err := os.Symlink(outsideRoot, symlinkRoot); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/imports/tubearchivist", strings.NewReader(`{"root":"`+filepath.ToSlash(symlinkRoot)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	NewHandler(WithJobs(store), WithImportRoot(allowedRoot)).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestTubeArchivistImportJobResultIsQueryable(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	allowedRoot := t.TempDir()
	importRoot := filepath.Join(allowedRoot, "tubearchivist")
	writeServerBackupZip(t, importRoot)
	handler := NewHandler(WithJobs(store), WithImportRoot(allowedRoot))
	req := httptest.NewRequest(http.MethodPost, "/api/imports/tubearchivist", strings.NewReader(`{"root":"`+filepath.ToSlash(importRoot)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, rec.Code)
	}
	queued := decodePublicJobResponse(t, rec.Body.String())
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		taimport.JobType: taimport.NewJobHandler(db, store, allowedRoot).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/jobs/"+queued.ID, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	completed := decodePublicJobResponse(t, rec.Body.String())
	if completed.Status != jobs.StatusSucceeded || completed.Progress != 1 || completed.Error != "" {
		t.Fatalf("unexpected completed import job: %#v", completed)
	}
	var report taimport.Report
	if err := json.Unmarshal([]byte(completed.ResultSummary), &report); err != nil {
		t.Fatal(err)
	}
	if report.Videos != 1 || len(report.Skipped) != 1 {
		t.Fatalf("unexpected queried import report: %#v", report)
	}
}

func TestMediaEndpoint(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.mp4"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	signer := media.NewSigner("secret")
	req := httptest.NewRequest(http.MethodGet, "/media/sample.mp4?"+signer.Query("sample.mp4", time.Now().Add(time.Hour)).Encode(), nil)
	req.Header.Set("Range", "bytes=0-3")
	rec := httptest.NewRecorder()

	NewHandler(WithMedia(root, signer)).ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("expected status %d, got %d", http.StatusPartialContent, rec.Code)
	}
	if rec.Body.String() != "0123" {
		t.Fatalf("unexpected body %q", rec.Body.String())
	}
}

func TestVideoListEndpointOmitsPlayableMediaURLs(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	seedVideoWithAssets(t, db)
	if _, err := db.Exec("UPDATE videos SET archived_at = ?, view_count = ? WHERE id = ?", "2026-06-01T00:00:00Z", 1234, "vid-1"); err != nil {
		t.Fatal(err)
	}
	writeServerFile(t, filepath.Join(root, "videos", "sample.mp4"), "video")
	writeServerFile(t, filepath.Join(root, "thumbs", "sample.jpg"), "thumb")
	signer := media.NewSigner("secret")
	handler := NewHandler(WithDatabase(db), WithMedia(root, signer), WithMediaURLTTL(time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/api/videos?page_size=1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 {
		t.Fatalf("expected one video, got %d", len(response.Data))
	}
	item := response.Data[0]
	if _, ok := item["media_path"]; ok {
		t.Fatalf("expected raw media_path to be omitted: %#v", item)
	}
	if _, ok := item["thumbnail_path"]; ok {
		t.Fatalf("expected raw thumbnail_path to be omitted: %#v", item)
	}
	if _, ok := item["media_url"]; ok {
		t.Fatalf("expected list media_url to be omitted: %#v", item)
	}
	assertMediaURLServes(t, handler, stringField(t, item, "thumbnail_url"), "thumb")
	if stringField(t, item, "thumbnail_fallback") != "V" {
		t.Fatalf("expected deterministic fallback label for signed-thumbnail item, got %#v", item)
	}
	if stringField(t, item, "archive_state") != "downloaded" {
		t.Fatalf("expected downloaded archive state, got %#v", item)
	}
}

func TestVideoListEndpointIncludesChannelThumbnailURL(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	seedVideoWithAssets(t, db)
	if _, err := db.Exec("INSERT INTO media_assets (owner_type, owner_id, kind, path) VALUES ('channel', 'chan-1', 'thumbnail', 'channels/chan-1.jpg')"); err != nil {
		t.Fatal(err)
	}
	writeServerFile(t, filepath.Join(root, "channels", "chan-1.jpg"), "channel thumb")
	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")), WithMediaURLTTL(time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/api/videos?page_size=1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	response := decodeVideoListResponse(t, rec)
	if len(response.Data) != 1 {
		t.Fatalf("expected one video, got %d", len(response.Data))
	}
	assertMediaURLServes(t, handler, response.Data[0].Channel.ThumbnailURL, "channel thumb")
}

func TestVideoListEndpointsExposeCommonFields(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-shared', 'chan-shared', 'Shared Channel')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO playlists (id, external_id, title) VALUES ('playlist-shared', 'playlist-shared', 'Shared Playlist')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description, published_at, archived_at, duration_seconds, view_count, media_path, thumbnail_path, keep_forever)
VALUES
  ('current', 'current', 'chan-shared', 'Current Video', 'Current description', '2026-05-01', '2026-05-02', 60, 5, 'videos/current.mp4', 'thumbs/current.jpg', 0),
  ('shared-video', 'shared-video', 'chan-shared', 'Shared Video', 'Shared description', '2026-05-03', '2026-05-04', 120, 42, 'videos/shared.mp4', 'thumbs/shared.jpg', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO playlist_entries (playlist_id, video_id, position) VALUES ('playlist-shared', 'shared-video', 0)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO media_assets (owner_type, owner_id, kind, path) VALUES ('channel', 'chan-shared', 'thumbnail', 'channels/shared.jpg')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO user_progress (video_id, position_seconds, duration_seconds, watched) VALUES ('shared-video', 33, 120, 0)"); err != nil {
		t.Fatal(err)
	}
	writeServerFile(t, filepath.Join(root, "videos", "shared.mp4"), "shared media")
	writeServerFile(t, filepath.Join(root, "thumbs", "shared.jpg"), "shared thumb")
	writeServerFile(t, filepath.Join(root, "channels", "shared.jpg"), "shared channel thumb")
	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")), WithMediaURLTTL(time.Hour))

	for _, endpoint := range []string{
		"/api/videos?channel=chan-shared&sort=newest&page_size=1",
		"/api/home/videos?sort=newest&page_size=1",
		"/api/channels/chan-shared/videos?sort=newest&page_size=1",
		"/api/videos/current/up-next?page_size=1",
		"/api/playlists/playlist-shared/videos?page_size=1",
	} {
		req := httptest.NewRequest(http.MethodGet, endpoint, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		response := decodeVideoListResponse(t, rec)
		if len(response.Data) != 1 {
			t.Fatalf("expected one video from %s, got %#v", endpoint, response)
		}
		assertSharedVideoListItem(t, handler, response.Data[0])
	}
}

func TestVideoListEndpointReturnsThumbnailFallbackForMissingThumbnail(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	seedVideoWithoutThumbnail(t, db)
	writeServerFile(t, filepath.Join(root, "videos", "sample.mp4"), "video")
	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")))

	req := httptest.NewRequest(http.MethodGet, "/api/videos?page_size=1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 {
		t.Fatalf("expected one video, got %d", len(response.Data))
	}
	item := response.Data[0]
	if _, ok := item["thumbnail_url"]; ok {
		t.Fatalf("expected missing thumbnail_url to be omitted: %#v", item)
	}
	if stringField(t, item, "thumbnail_fallback") != "V" {
		t.Fatalf("expected deterministic fallback label, got %#v", item)
	}
	if stringField(t, item, "archive_state") != "downloaded" {
		t.Fatalf("expected downloaded archive state, got %#v", item)
	}
}

func TestVideoListEndpointMarksCatalogOnlyThumbnailState(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	seedCatalogOnlyVideoWithThumbnail(t, db)
	writeServerFile(t, filepath.Join(root, "thumbs", "catalog.jpg"), "thumb")
	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")))

	req := httptest.NewRequest(http.MethodGet, "/api/videos?page_size=1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	item := response.Data[0]
	if _, ok := item["media_url"]; ok {
		t.Fatalf("expected catalog-only media_url to be omitted: %#v", item)
	}
	assertMediaURLServes(t, handler, stringField(t, item, "thumbnail_url"), "thumb")
	if stringField(t, item, "archive_state") != "catalog-only" {
		t.Fatalf("expected catalog-only archive state, got %#v", item)
	}
}

func TestCatalogVideoCanDownloadEligibility(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Channel One')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO playlists (id, external_id, title) VALUES ('playlist-1', 'playlist-1', 'Catalog Playlist')"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO videos (id, external_id, source, channel_id, title, duration_seconds, media_path) VALUES ('vid-catalog', 'abc123DEF45', 'youtube', 'chan-1', 'Catalog Video', 120, '')`,
		`INSERT INTO videos (id, external_id, source, channel_id, title, duration_seconds, media_path) VALUES ('vid-downloaded', 'def123GHI45', 'youtube', 'chan-1', 'Downloaded Video', 120, 'videos/downloaded.mp4')`,
		`INSERT INTO videos (id, external_id, source, channel_id, title, duration_seconds, media_path) VALUES ('vid-missing-external', '', 'youtube', 'chan-1', 'Missing External', 120, '')`,
		`INSERT INTO videos (id, external_id, source, channel_id, title, duration_seconds, media_path) VALUES ('vid-non-youtube', 'abc123DEF45', 'vimeo', 'chan-1', 'Non YouTube', 120, '')`,
		`INSERT INTO playlist_entries (playlist_id, video_id, position) VALUES ('playlist-1', 'vid-catalog', 0)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	writeServerFile(t, filepath.Join(root, "videos", "downloaded.mp4"), "video")
	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/videos?channel=chan-1&page_size=10", nil)
	handler.ServeHTTP(rec, req)
	list := decodeVideoListResponse(t, rec)
	if !videoListItemByID(t, list.Data, "vid-catalog").CanDownload {
		t.Fatalf("expected catalog video to be downloadable: %#v", list.Data)
	}
	for _, id := range []string{"vid-downloaded", "vid-missing-external", "vid-non-youtube"} {
		if videoListItemByID(t, list.Data, id).CanDownload {
			t.Fatalf("expected %s to be ineligible for catalog download: %#v", id, list.Data)
		}
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/videos/vid-catalog", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected detail status %d, got %d", http.StatusOK, rec.Code)
	}
	var detail videoResponse
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if !detail.CanDownload {
		t.Fatalf("expected detail response to expose can_download: %#v", detail)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/playlists/playlist-1/videos", nil)
	handler.ServeHTTP(rec, req)
	playlistVideos := decodeVideoListResponse(t, rec)
	if len(playlistVideos.Data) != 1 || !playlistVideos.Data[0].CanDownload {
		t.Fatalf("expected playlist video response to expose can_download: %#v", playlistVideos)
	}
}

func TestGetVideoEndpointIncludesActiveCatalogDownloadJob(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Channel One')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO videos (id, external_id, source, channel_id, title, duration_seconds, media_path) VALUES ('vid-active', 'KjZ26DVMKJE', 'youtube', 'chan-1', 'Active Catalog Video', 120, '')"); err != nil {
		t.Fatal(err)
	}
	payloadJSON, err := json.Marshal(download.Payload{URL: "https://www.youtube.com/watch?v=KjZ26DVMKJE"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{Type: download.JobType, PayloadJSON: string(payloadJSON)})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected active download claim, ok=%v claimed=%#v", ok, claimed)
	}
	if err := store.Heartbeat(context.Background(), job.ID, 0.82); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-active", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db), WithJobs(store), WithMedia(t.TempDir(), media.NewSigner("secret"))).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var detail videoResponse
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.ActiveDownloadJob == nil || detail.ActiveDownloadJob.ID != job.ID || detail.ActiveDownloadJob.Status != jobs.StatusRunning || detail.ActiveDownloadJob.Progress != 0.82 {
		t.Fatalf("expected active download job on video response, got %#v", detail.ActiveDownloadJob)
	}
}

func TestGetVideoEndpointIncludesActivePreviewJob(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	store := jobs.NewStore(db)
	if _, err := db.Exec("INSERT INTO videos (id, external_id, source, title, duration_seconds, media_path) VALUES ('vid-active', 'KjZ26DVMKJE', 'youtube', 'Active Preview Video', 120, 'videos/vid-active.mp4')"); err != nil {
		t.Fatal(err)
	}
	payloadJSON, err := json.Marshal(previews.Payload{VideoID: "vid-active"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{Type: previews.JobType, PayloadJSON: string(payloadJSON)})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("expected active preview claim, ok=%v claimed=%#v", ok, claimed)
	}
	if err := store.Heartbeat(context.Background(), job.ID, 0.42); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-active", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db), WithJobs(store), WithMedia(t.TempDir(), media.NewSigner("secret"))).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var detail videoResponse
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.ActivePreviewJob == nil || detail.ActivePreviewJob.ID != job.ID || detail.ActivePreviewJob.Status != jobs.StatusRunning || detail.ActivePreviewJob.Progress != 0.42 {
		t.Fatalf("expected active preview job on video response, got %#v", detail.ActivePreviewJob)
	}
}

func TestVideoListEndpointReturnsBoundedCatalogRemoteThumbnails(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Archive Workshop')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, description, duration_seconds, thumbnail_url)
VALUES ('vid-remote', 'vid-remote', 'chan-1', 'Remote Catalog', 'Catalog-only metadata', 120, 'https://i.ytimg.com/vi/vid-remote/hqdefault.jpg')`); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(WithDatabase(db))

	req := httptest.NewRequest(http.MethodGet, "/api/videos?channel=chan-1&page_size=1000", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	response := decodeVideoListResponse(t, rec)
	if response.Pagination.PageSize != 50 {
		t.Fatalf("expected bounded page size 50, got %#v", response.Pagination)
	}
	if len(response.Data) != 1 {
		t.Fatalf("expected one catalog video, got %d", len(response.Data))
	}
	item := response.Data[0]
	if item.ThumbnailURL != "https://i.ytimg.com/vi/vid-remote/hqdefault.jpg" || item.ArchiveState != "catalog-only" {
		t.Fatalf("expected catalog-only remote thumbnail response, got %#v", item)
	}
}

func TestVideoListEndpointTreatsMissingMediaFileAsCatalogOnly(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	seedVideoWithAssets(t, db)
	writeServerFile(t, filepath.Join(root, "thumbs", "sample.jpg"), "thumb")
	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")))

	req := httptest.NewRequest(http.MethodGet, "/api/videos?page_size=1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	item := response.Data[0]
	if _, ok := item["media_url"]; ok {
		t.Fatalf("expected absent media file not to receive media_url: %#v", item)
	}
	assertMediaURLServes(t, handler, stringField(t, item, "thumbnail_url"), "thumb")
	if stringField(t, item, "archive_state") != "catalog-only" {
		t.Fatalf("expected missing media file to be catalog-only, got %#v", item)
	}
}

func TestMediaURLBuilderRejectsSymlinkPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.mp4")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "videos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "videos", "sample.mp4")); err != nil {
		t.Fatal(err)
	}
	signer := media.NewSigner("secret")
	builder := mediaURLBuilder{root: root, signer: &signer, ttl: time.Hour}

	if got := builder.SignedURL("videos/sample.mp4"); got != "" {
		t.Fatalf("expected symlink path to be unsigned, got %q", got)
	}
	if builder.Available("videos/sample.mp4") {
		t.Fatal("expected symlink path to be unavailable")
	}
}

func TestMediaURLBuilderRejectsSymlinkParents(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outsideRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideRoot, "sample.mp4"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideRoot, filepath.Join(root, "videos")); err != nil {
		t.Fatal(err)
	}
	signer := media.NewSigner("secret")
	builder := mediaURLBuilder{root: root, signer: &signer, ttl: time.Hour}

	if got := builder.SignedURL("videos/sample.mp4"); got != "" {
		t.Fatalf("expected path with symlink parent to be unsigned, got %q", got)
	}
}

func TestDeleteVideoMediaRemovesFileReferenceAndPreservesMetadata(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	seedVideoWithAssets(t, db)
	seedTimelinePreview(t, db)
	seedSubtitleTrack(t, db)
	if _, err := db.Exec("INSERT INTO comments (id, video_id, author, text) VALUES ('comment-1', 'vid-1', 'Viewer', 'Keep this comment')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO media_assets (owner_type, owner_id, kind, path) VALUES ('video', 'vid-1', 'media', 'videos/sample.mp4')"); err != nil {
		t.Fatal(err)
	}
	writeServerFile(t, filepath.Join(root, "videos", "sample.mp4"), "video")
	writeServerFile(t, filepath.Join(root, "thumbs", "sample.jpg"), "thumb")
	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")), WithMediaURLTTL(time.Hour))

	req := httptest.NewRequest(http.MethodDelete, "/api/videos/vid-1/media", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusNoContent, rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "videos", "sample.mp4")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected media file to be removed, got %v", err)
	}
	assertServerScalar(t, db, "SELECT count(*) FROM videos WHERE id = ?", int64(1), "vid-1")
	assertServerScalar(t, db, "SELECT title FROM videos WHERE id = ?", "Video One", "vid-1")
	assertServerScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "", "vid-1")
	assertServerScalar(t, db, "SELECT thumbnail_path FROM videos WHERE id = ?", "thumbs/sample.jpg", "vid-1")
	assertServerScalar(t, db, "SELECT count(*) FROM comments WHERE video_id = ?", int64(1), "vid-1")
	assertServerScalar(t, db, "SELECT count(*) FROM subtitles WHERE video_id = ?", int64(1), "vid-1")
	assertServerScalar(t, db, "SELECT count(*) FROM video_previews WHERE video_id = ?", int64(1), "vid-1")
	assertServerScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'media'", int64(0), "vid-1")

	req = httptest.NewRequest(http.MethodGet, "/api/videos/vid-1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected detail status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var detail map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if _, ok := detail["media_url"]; ok {
		t.Fatalf("expected deleted media not to receive media_url: %#v", detail)
	}
	if stringField(t, detail, "archive_state") != "catalog-only" {
		t.Fatalf("expected deleted media to become catalog-only, got %#v", detail)
	}
	if stringField(t, detail, "title") != "Video One" {
		t.Fatalf("expected metadata to remain, got %#v", detail)
	}
	assertMediaURLServes(t, handler, stringField(t, detail, "thumbnail_url"), "thumb")
}

func TestDeleteVideoMediaRejectsCatalogOnlyVideo(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	seedCatalogOnlyVideoWithThumbnail(t, db)
	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")))

	req := httptest.NewRequest(http.MethodDelete, "/api/videos/vid-1/media", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusConflict, rec.Code, rec.Body.String())
	}
	assertServerScalar(t, db, "SELECT count(*) FROM videos WHERE id = ?", int64(1), "vid-1")
	assertServerScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "", "vid-1")
}

func TestDeleteVideoMediaDoesNotClearReferenceWhenMediaRootIsMissing(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	missingRoot := filepath.Join(t.TempDir(), "missing-media-root")
	seedVideoWithAssets(t, db)
	if _, err := db.Exec("INSERT INTO media_assets (owner_type, owner_id, kind, path) VALUES ('video', 'vid-1', 'media', 'videos/sample.mp4')"); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(WithDatabase(db), WithMedia(missingRoot, media.NewSigner("secret")))

	req := httptest.NewRequest(http.MethodDelete, "/api/videos/vid-1/media", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusInternalServerError, rec.Code, rec.Body.String())
	}
	assertServerScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "videos/sample.mp4", "vid-1")
	assertServerScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'media'", int64(1), "vid-1")
}

func TestDeleteVideoMediaDoesNotClearReferenceWhenMediaFileIsMissing(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	seedVideoWithAssets(t, db)
	if _, err := db.Exec("INSERT INTO media_assets (owner_type, owner_id, kind, path) VALUES ('video', 'vid-1', 'media', 'videos/sample.mp4')"); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")))

	req := httptest.NewRequest(http.MethodDelete, "/api/videos/vid-1/media", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusConflict, rec.Code, rec.Body.String())
	}
	assertServerScalar(t, db, "SELECT media_path FROM videos WHERE id = ?", "videos/sample.mp4", "vid-1")
	assertServerScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'media'", int64(1), "vid-1")
}

func TestVideoDetailEndpointReturnsSignedMediaURLs(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	seedVideoWithAssets(t, db)
	if _, err := db.Exec("UPDATE videos SET archived_at = ?, view_count = ? WHERE id = ?", "2026-06-01T00:00:00Z", 1234, "vid-1"); err != nil {
		t.Fatal(err)
	}
	writeServerFile(t, filepath.Join(root, "videos", "sample.mp4"), "video")
	writeServerFile(t, filepath.Join(root, "thumbs", "sample.jpg"), "thumb")
	signer := media.NewSigner("secret")
	handler := NewHandler(WithDatabase(db), WithMedia(root, signer), WithMediaURLTTL(time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if _, ok := response["media_path"]; ok {
		t.Fatalf("expected raw media_path to be omitted: %#v", response)
	}
	if _, ok := response["thumbnail_path"]; ok {
		t.Fatalf("expected raw thumbnail_path to be omitted: %#v", response)
	}
	mediaURL := stringField(t, response, "media_url")
	assertMediaURLServes(t, handler, mediaURL, "video")
	assertMediaURLServes(t, handler, stringField(t, response, "thumbnail_url"), "thumb")
	if stringField(t, response, "thumbnail_fallback") != "V" {
		t.Fatalf("expected deterministic fallback label for detail, got %#v", response)
	}
	if stringField(t, response, "archive_state") != "downloaded" {
		t.Fatalf("expected downloaded archive state for detail, got %#v", response)
	}
	if stringField(t, response, "archived_at") != "2026-06-01T00:00:00Z" || response["view_count"] != float64(1234) {
		t.Fatalf("expected detail to expose archive date and view count, got %#v", response)
	}

	tamperedURL := strings.Replace(mediaURL, "videos/sample.mp4", "videos/other.mp4", 1)
	req = httptest.NewRequest(http.MethodGet, tamperedURL, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected tampered URL status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestVideoDetailEndpointDoesNotFetchUncachedSponsorBlockSegments(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoWithAssets(t, db)
	client := &fakeSponsorBlockClient{segments: []sponsorblock.Segment{{StartSeconds: 12.5, EndSeconds: 19.25}}}
	handler := NewHandler(WithDatabase(db), WithSponsorBlockClient(client))

	req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var detail videoResponse
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.SponsorSegments) != 0 {
		t.Fatalf("expected uncached SponsorBlock segments to stay out of detail response, got %#v", detail.SponsorSegments)
	}
	if got := strings.Join(client.Calls(), ","); got != "" {
		t.Fatalf("expected video detail not to call SponsorBlock, got %q", got)
	}
	assertServerScalar(t, db, "SELECT count(*) FROM sponsorblock_segments WHERE video_id = ?", int64(0), "vid-1")
}

func TestVideoDetailEndpointReturnsCachedSponsorBlockSegments(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoWithAssets(t, db)
	if err := saveVideoSponsorSegments(context.Background(), db, "vid-1", "youtube", "vid-1", []sponsorblock.Segment{{StartSeconds: 4, EndSeconds: 8}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeSponsorBlockClient{segments: []sponsorblock.Segment{{StartSeconds: 12, EndSeconds: 16}}}
	handler := NewHandler(WithDatabase(db), WithSponsorBlockClient(client))

	req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var detail videoResponse
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.SponsorSegments) != 1 || detail.SponsorSegments[0].StartSeconds != 4 || detail.SponsorSegments[0].EndSeconds != 8 {
		t.Fatalf("expected cached sponsor segment in detail response, got %#v", detail.SponsorSegments)
	}
	if got := strings.Join(client.Calls(), ","); got != "" {
		t.Fatalf("expected video detail to reuse cache without network fetch, got %q", got)
	}
}

func TestVideoSponsorSegmentsEndpointFetchesAndCachesSegments(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoWithAssets(t, db)
	client := &fakeSponsorBlockClient{segments: []sponsorblock.Segment{{StartSeconds: 12.5, EndSeconds: 19.25}}}
	handler := NewHandler(WithDatabase(db), WithSponsorBlockClient(client))

	req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-1/sponsor-segments", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response struct {
		Data []sponsorblock.Segment `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 || response.Data[0].StartSeconds != 12.5 || response.Data[0].EndSeconds != 19.25 {
		t.Fatalf("expected sponsor segment response, got %#v", response.Data)
	}
	if got := strings.Join(client.Calls(), ","); got != "vid-1" {
		t.Fatalf("expected SponsorBlock request for external ID, got %q", got)
	}
	assertServerScalar(t, db, "SELECT count(*) FROM sponsorblock_segments WHERE video_id = ?", int64(1), "vid-1")
}

func TestVideoDetailEndpointOmitsSponsorBlockSegmentsWithoutClient(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoWithAssets(t, db)
	if err := saveVideoSponsorSegments(context.Background(), db, "vid-1", "youtube", "vid-1", []sponsorblock.Segment{{StartSeconds: 4, EndSeconds: 8}}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(WithDatabase(db))

	req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var detail videoResponse
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.SponsorSegments) != 0 {
		t.Fatalf("expected SponsorBlock segments to be disabled without a client, got %#v", detail.SponsorSegments)
	}
}

func TestVideoSponsorSegmentsEndpointBacksOffSponsorBlockFailures(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoWithAssets(t, db)
	client := &fakeSponsorBlockClient{err: errors.New("sponsorblock unavailable")}
	handler := NewHandler(WithDatabase(db), WithSponsorBlockClient(client))

	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-1/sponsor-segments", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status %d while SponsorBlock is unavailable, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
		}
		var response struct {
			Data []sponsorblock.Segment `json:"data"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
			t.Fatal(err)
		}
		if len(response.Data) != 0 {
			t.Fatalf("expected unavailable SponsorBlock to omit segments, got %#v", response.Data)
		}
	}
	if got := strings.Join(client.Calls(), ","); got != "vid-1" {
		t.Fatalf("expected SponsorBlock failure backoff after one request, got %q", got)
	}
}

func TestVideoSponsorSegmentsEndpointCoalescesConcurrentSponsorBlockFailures(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoWithAssets(t, db)
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	client := &fakeSponsorBlockClient{err: errors.New("sponsorblock unavailable"), wait: release, started: started}
	handler := NewHandler(WithDatabase(db), WithSponsorBlockClient(client))
	responses := make(chan error, 2)
	requestSegments := func() {
		req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-1/sponsor-segments", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			responses <- fmt.Errorf("expected status %d while SponsorBlock is unavailable, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
			return
		}
		responses <- nil
	}

	go requestSegments()
	<-started
	go requestSegments()
	time.Sleep(25 * time.Millisecond)
	close(release)
	for range 2 {
		if err := <-responses; err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(client.Calls(), ","); got != "vid-1" {
		t.Fatalf("expected one coalesced SponsorBlock failure request, got %q", got)
	}
}

func TestVideoDetailEndpointIncludesChannelThumbnailURL(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoWithAssets(t, db)
	if _, err := db.Exec("UPDATE channels SET thumbnail_url = ? WHERE id = ?", "https://yt3.ggpht.com/channel-one.jpg", "chan-1"); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(WithDatabase(db))

	req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response struct {
		Channel struct {
			ThumbnailURL string `json:"thumbnail_url"`
		} `json:"channel"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Channel.ThumbnailURL != "https://yt3.ggpht.com/channel-one.jpg" {
		t.Fatalf("expected channel thumbnail URL in video detail, got %#v", response.Channel)
	}
}

func TestVideoDetailEndpointReturnsTimelinePreviewMetadata(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	seedVideoWithAssets(t, db)
	seedTimelinePreview(t, db)
	writeServerFile(t, filepath.Join(root, "videos", "sample.mp4"), "video")
	writeServerFile(t, filepath.Join(root, "thumbs", "sample.jpg"), "thumb")
	writeServerFile(t, filepath.Join(root, "derived", "previews", "vid-1", "sprite.jpg"), "sprite")
	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")), WithMediaURLTTL(time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	preview, ok := response["timeline_preview"].(map[string]any)
	if !ok {
		t.Fatalf("expected timeline preview metadata in %#v", response)
	}
	assertMediaURLServes(t, handler, stringField(t, preview, "sprite_url"), "sprite")
	vttURL := stringField(t, preview, "vtt_url")
	req = httptest.NewRequest(http.MethodGet, vttURL, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected VTT status %d, got %d", http.StatusOK, rec.Code)
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "text/vtt; charset=utf-8" {
		t.Fatalf("expected VTT content type, got %q", contentType)
	}
	vtt := rec.Body.String()
	for _, expected := range []string{
		"WEBVTT\n\n",
		"00:00:00.000 --> 00:00:10.000\n",
		"00:00:10.000 --> 00:00:20.000\n",
		"00:00:20.000 --> 00:00:30.000\n",
		"/media/derived/previews/vid-1/sprite.jpg?",
		"#xywh=0,0,160,90",
		"#xywh=160,0,160,90",
		"#xywh=320,0,160,90",
	} {
		if !strings.Contains(vtt, expected) {
			t.Fatalf("expected VTT to contain %q, got %q", expected, vtt)
		}
	}
	if _, ok := preview["sprite_path"]; ok {
		t.Fatalf("expected raw sprite path to be omitted: %#v", preview)
	}
	cues, ok := preview["cues"].([]any)
	if !ok || len(cues) != 3 {
		t.Fatalf("expected three preview cues, got %#v", preview)
	}
}

func TestTimelinePreviewVTTEndpointRequiresAuthAndServesSignedSprite(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	seedVideoWithAssets(t, db)
	seedTimelinePreview(t, db)
	writeServerFile(t, filepath.Join(root, "videos", "sample.mp4"), "video")
	writeServerFile(t, filepath.Join(root, "derived", "previews", "vid-1", "sprite.jpg"), "sprite")
	manager := newServerAuthManager(t, time.Now())
	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")), WithMediaURLTTL(time.Hour), WithAuth(manager))

	req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-1/timeline-preview.vtt", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated VTT status %d, got %d", http.StatusUnauthorized, rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos/vid-1/timeline-preview.vtt", nil)
	req.AddCookie(manager.SessionCookie("admin"))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected authenticated VTT status %d, got %d", http.StatusOK, rec.Code)
	}
	mediaURL := firstVTTMediaURL(t, rec.Body.String())
	assertMediaURLServes(t, handler, mediaURL, "sprite")

	tamperedURL := strings.Replace(mediaURL, "sprite.jpg", "other.jpg", 1)
	req = httptest.NewRequest(http.MethodGet, tamperedURL, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected tampered sprite URL status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestVideoChaptersVTTEndpointParsesDescriptionTimestamps(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoWithAssets(t, db)
	description := `00:00 - G16 review
30:26 - Culture battle around G16
35:22 - G16 bombs
40:20 - Ghostbusters Afterlife f**king sucks
52:30 - Ghostbusters Wreckoning`
	if _, err := db.Exec("UPDATE videos SET description = ?, duration_seconds = ? WHERE id = ?", description, 3600, "vid-1"); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(WithDatabase(db))

	req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var detail struct {
		ChaptersVTTURL string `json:"chapters_vtt_url"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.ChaptersVTTURL != "/api/videos/vid-1/chapters.vtt" {
		t.Fatalf("expected chapters VTT URL in video detail, got %#v", detail)
	}

	req = httptest.NewRequest(http.MethodGet, detail.ChaptersVTTURL, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected chapters VTT status %d, got %d", http.StatusOK, rec.Code)
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "text/vtt; charset=utf-8" {
		t.Fatalf("expected chapters VTT content type, got %q", contentType)
	}
	expected := "WEBVTT\n\n" +
		"00:00:00.000 --> 00:30:26.000\nG16 review\n\n" +
		"00:30:26.000 --> 00:35:22.000\nCulture battle around G16\n\n" +
		"00:35:22.000 --> 00:40:20.000\nG16 bombs\n\n" +
		"00:40:20.000 --> 00:52:30.000\nGhostbusters Afterlife f**king sucks\n\n" +
		"00:52:30.000 --> 01:00:00.000\nGhostbusters Wreckoning\n\n"
	if rec.Body.String() != expected {
		t.Fatalf("unexpected chapters VTT:\n%s", rec.Body.String())
	}
}

func TestVideoChaptersVTTEndpointReturnsNotFoundWithoutChapters(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoWithAssets(t, db)
	if _, err := db.Exec("UPDATE videos SET description = ? WHERE id = ?", "0:42 or 1:05 are seekable timestamps, but not chapter headings.", "vid-1"); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(WithDatabase(db))

	req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var detail map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if _, ok := detail["chapters_vtt_url"]; ok {
		t.Fatalf("expected non-heading timestamps to omit chapters URL: %#v", detail)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos/vid-1/chapters.vtt", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected no-chapters status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestVideoChaptersVTTEndpointRequiresAuth(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoWithAssets(t, db)
	if _, err := db.Exec("UPDATE videos SET description = ?, duration_seconds = ? WHERE id = ?", "00:00 - Intro\n01:00 - Next", 120, "vid-1"); err != nil {
		t.Fatal(err)
	}
	manager := newServerAuthManager(t, time.Now())
	handler := NewHandler(WithDatabase(db), WithAuth(manager))

	req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-1/chapters.vtt", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated chapters VTT status %d, got %d", http.StatusUnauthorized, rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos/vid-1/chapters.vtt", nil)
	req.AddCookie(manager.SessionCookie("admin"))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected authenticated chapters VTT status %d, got %d", http.StatusOK, rec.Code)
	}
}

func TestVideoDetailEndpointReturnsSubtitleTracks(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	seedVideoWithAssets(t, db)
	seedSubtitleTrack(t, db)
	writeServerFile(t, filepath.Join(root, "videos", "sample.mp4"), "video")
	writeServerFile(t, filepath.Join(root, "thumbs", "sample.jpg"), "thumb")
	writeServerFile(t, filepath.Join(root, "subtitles", "vid-1.en.vtt"), "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nCaption text\n")
	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")), WithMediaURLTTL(time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	tracks, ok := response["subtitles"].([]any)
	if !ok || len(tracks) != 1 {
		t.Fatalf("expected one subtitle track, got %#v", response)
	}
	track := tracks[0].(map[string]any)
	assertMediaURLServes(t, handler, stringField(t, track, "url"), "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nCaption text\n")
	if _, ok := track["text"]; ok {
		t.Fatalf("expected subtitle text to be omitted from detail API: %#v", track)
	}
	if stringField(t, track, "language") != "en" || stringField(t, track, "format") != "vtt" {
		t.Fatalf("unexpected subtitle track: %#v", track)
	}
}

func TestVideoDetailEndpointMediaURLTTLCanExpire(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	seedVideoWithAssets(t, db)
	writeServerFile(t, filepath.Join(root, "videos", "sample.mp4"), "video")
	signer := media.NewSigner("secret")
	handler := NewHandler(WithDatabase(db), WithMedia(root, signer), WithMediaURLTTL(-time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, stringField(t, response, "media_url"), nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected expired media URL status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestVideoProgressEndpointUpsertsAndMarksWatchedNearCompletion(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoWithAssets(t, db)
	handler := NewHandler(WithDatabase(db))
	req := httptest.NewRequest(http.MethodPut, "/api/videos/vid-1/progress", strings.NewReader(`{"position_seconds":42,"duration_seconds":120}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	progress := decodeProgressInfo(t, rec)
	if progress.PositionSeconds != 42 || progress.Watched {
		t.Fatalf("unexpected initial progress: %#v", progress)
	}

	req = httptest.NewRequest(http.MethodPut, "/api/videos/vid-1/progress", strings.NewReader(`{"position_seconds":112,"duration_seconds":120}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	progress = decodeProgressInfo(t, rec)
	if progress.PositionSeconds != 112 || !progress.Watched {
		t.Fatalf("expected near-complete progress to mark watched, got %#v", progress)
	}
	assertServerScalar(t, db, "SELECT watched FROM videos WHERE id = ?", int64(1), "vid-1")

	req = httptest.NewRequest(http.MethodGet, "/api/videos/vid-1/progress", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	progress = decodeProgressInfo(t, rec)
	if progress.PositionSeconds != 112 || !progress.Watched {
		t.Fatalf("expected persisted progress response, got %#v", progress)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos/vid-1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected video detail status %d, got %d", http.StatusOK, rec.Code)
	}
	var detail videoResponse
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.PositionSeconds != 112 || !detail.Watched {
		t.Fatalf("expected detail to include progress, got %#v", detail)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos?page_size=1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	list := decodeVideoListResponse(t, rec)
	if list.Data[0].Progress.PositionSeconds != 112 || !list.Data[0].Progress.Watched {
		t.Fatalf("expected library list to reflect progress, got %#v", list.Data[0].Progress)
	}
}

func TestVideoProgressEndpointAcceptsExplicitWatchedState(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoWithAssets(t, db)
	handler := NewHandler(WithDatabase(db))
	req := httptest.NewRequest(http.MethodPut, "/api/videos/vid-1/progress", strings.NewReader(`{"position_seconds":12,"duration_seconds":120,"watched":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	progress := decodeProgressInfo(t, rec)
	if progress.PositionSeconds != 12 || !progress.Watched {
		t.Fatalf("expected explicit watched progress, got %#v", progress)
	}
	assertServerScalar(t, db, "SELECT watched FROM videos WHERE id = ?", int64(1), "vid-1")

	req = httptest.NewRequest(http.MethodGet, "/api/videos/vid-1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected detail status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var detail videoResponse
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if !detail.Watched {
		t.Fatalf("expected detail to include explicit watched state, got %#v", detail)
	}
}

func TestVideoProgressEndpointPreservesImportedWatchedState(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoWithAssets(t, db)
	if _, err := db.Exec("UPDATE videos SET watched = 1 WHERE id = 'vid-1'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO user_progress (video_id, position_seconds, duration_seconds, watched) VALUES ('vid-1', 12, 120, 0)"); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(WithDatabase(db))
	req := httptest.NewRequest(http.MethodPut, "/api/videos/vid-1/progress", strings.NewReader(`{"position_seconds":12,"duration_seconds":120}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	progress := decodeProgressInfo(t, rec)
	if !progress.Watched {
		t.Fatalf("expected imported watched state to stay watched, got %#v", progress)
	}
	assertServerScalar(t, db, "SELECT watched FROM videos WHERE id = ?", int64(1), "vid-1")
	assertServerScalar(t, db, "SELECT watched FROM user_progress WHERE video_id = ?", int64(1), "vid-1")
}

func TestVideoProgressEndpointRepairsProgressWatchedState(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoWithAssets(t, db)
	if _, err := db.Exec("INSERT INTO user_progress (video_id, position_seconds, duration_seconds, watched) VALUES ('vid-1', 120, 120, 1)"); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(WithDatabase(db))
	req := httptest.NewRequest(http.MethodPut, "/api/videos/vid-1/progress", strings.NewReader(`{"position_seconds":12,"duration_seconds":120}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	progress := decodeProgressInfo(t, rec)
	if !progress.Watched {
		t.Fatalf("expected progress watched state to stay watched, got %#v", progress)
	}
	assertServerScalar(t, db, "SELECT watched FROM videos WHERE id = ?", int64(1), "vid-1")
	assertServerScalar(t, db, "SELECT watched FROM user_progress WHERE video_id = ?", int64(1), "vid-1")
}

func TestVideoKeepForeverEndpointTogglesAndExposesState(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoWithAssets(t, db)
	handler := NewHandler(WithDatabase(db))

	req := httptest.NewRequest(http.MethodPut, "/api/videos/vid-1/keep-forever", strings.NewReader(`{"keep_forever":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var updated videoResponse
	if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if !updated.KeepForever {
		t.Fatalf("expected keep forever response, got %#v", updated)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos/vid-1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected detail status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var detail videoResponse
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if !detail.KeepForever {
		t.Fatalf("expected detail to include keep forever, got %#v", detail)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos?page_size=1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	list := decodeVideoListResponse(t, rec)
	if len(list.Data) != 1 || !list.Data[0].KeepForever {
		t.Fatalf("expected list to include keep forever, got %#v", list.Data)
	}

	req = httptest.NewRequest(http.MethodPut, "/api/videos/vid-1/keep-forever", strings.NewReader(`{"keep_forever":false}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected clear status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.KeepForever {
		t.Fatalf("expected keep forever to clear, got %#v", updated)
	}
}

func TestVideoProgressEndpointRejectsInvalidPayloadAndBounds(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoWithAssets(t, db)
	handler := NewHandler(WithDatabase(db))
	for _, body := range []string{
		`{"position_seconds":-1,"duration_seconds":120}`,
		`{"position_seconds":42,"duration_seconds":-1}`,
		`{"position_seconds":604801,"duration_seconds":604801}`,
		`{"position_seconds":"bad","duration_seconds":120}`,
		`{"position_seconds":42,"duration_seconds":120} {}`,
	} {
		req := httptest.NewRequest(http.MethodPut, "/api/videos/vid-1/progress", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status %d for %s, got %d body=%s", http.StatusBadRequest, body, rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodPut, "/api/videos/missing/progress", strings.NewReader(`{"position_seconds":42,"duration_seconds":120}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected missing video status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestVideoProgressPersistsAcrossReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "kapsel.db")
	db := openServerTestDBAt(t, path)
	seedVideoWithAssets(t, db)
	req := httptest.NewRequest(http.MethodPut, "/api/videos/vid-1/progress", strings.NewReader(`{"position_seconds":64,"duration_seconds":120}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openServerTestDBAt(t, path)
	req = httptest.NewRequest(http.MethodGet, "/api/videos/vid-1/progress", nil)
	rec = httptest.NewRecorder()
	NewHandler(WithDatabase(reopened)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d after reopen, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	progress := decodeProgressInfo(t, rec)
	if progress.PositionSeconds != 64 || progress.Watched {
		t.Fatalf("expected durable progress after reopen, got %#v", progress)
	}
}

func TestVideoProgressWatchedStateIsMonotonic(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoWithAssets(t, db)
	handler := NewHandler(WithDatabase(db))

	req := httptest.NewRequest(http.MethodPut, "/api/videos/vid-1/progress", strings.NewReader(`{"position_seconds":110,"duration_seconds":120}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	progress := decodeProgressInfo(t, rec)
	if !progress.Watched {
		t.Fatalf("expected near-complete progress to mark watched, got %#v", progress)
	}

	req = httptest.NewRequest(http.MethodPut, "/api/videos/vid-1/progress", strings.NewReader(`{"position_seconds":120,"duration_seconds":120,"watched":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	progress = decodeProgressInfo(t, rec)
	if progress.PositionSeconds != 120 || !progress.Watched {
		t.Fatalf("expected explicit watched progress to advance position, got %#v", progress)
	}

	req = httptest.NewRequest(http.MethodPut, "/api/videos/vid-1/progress", strings.NewReader(`{"position_seconds":10,"duration_seconds":120}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	progress = decodeProgressInfo(t, rec)
	if progress.PositionSeconds != 120 || !progress.Watched {
		t.Fatalf("expected watched state and position to be preserved after stale progress write, got %#v", progress)
	}

	req = httptest.NewRequest(http.MethodPut, "/api/videos/vid-1/progress", strings.NewReader(`{"position_seconds":110,"duration_seconds":120,"watched":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	progress = decodeProgressInfo(t, rec)
	if progress.PositionSeconds != 120 || !progress.Watched {
		t.Fatalf("expected watched position to survive stale watched progress write, got %#v", progress)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos/vid-1/progress", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	progress = decodeProgressInfo(t, rec)
	if progress.PositionSeconds != 120 || !progress.Watched {
		t.Fatalf("expected persisted watched state and position to survive stale progress write, got %#v", progress)
	}
}

func TestSearchEndpointReturnsMatches(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedServerSearchDocuments(t, db)
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=Kapsel&limit=5", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response searchResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Limit != 5 {
		t.Fatalf("expected response limit 5, got %d", response.Limit)
	}
	if len(response.Data) == 0 {
		t.Fatal("expected search results")
	}
	result := response.Data[0]
	if result.OwnerType != "video" || result.OwnerID != "vid-1" || result.Field != "title" {
		t.Fatalf("unexpected search result: %#v", result)
	}
	if !strings.Contains(result.Snippet, "<mark>") {
		t.Fatalf("expected highlighted snippet, got %q", result.Snippet)
	}
}

func TestSearchEndpointSignsHydratedThumbnail(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Archive Workshop')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, thumbnail_path, thumbnail_url)
VALUES ('vid-search', 'vid-search', 'chan-1', 'Kapsel Search', 'thumbs/search.jpg', 'https://i.ytimg.com/vi/vid-search/hqdefault.jpg');
INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES ('video', 'vid-search', 'title', 'Kapsel Search')`); err != nil {
		t.Fatal(err)
	}
	writeServerFile(t, filepath.Join(root, "thumbs", "search.jpg"), "thumb")
	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")))
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=Kapsel&limit=1", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "thumbnail_path") {
		t.Fatalf("expected raw thumbnail path to stay hidden: %s", body)
	}
	if strings.Contains(body, "media_url") {
		t.Fatalf("expected search results to omit media_url: %s", body)
	}
	var response searchResponse
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 || !strings.HasPrefix(response.Data[0].Record.ThumbnailURL, "/media/thumbs/search.jpg?") {
		t.Fatalf("expected signed hydrated thumbnail, got %#v", response.Data)
	}
}

func TestSearchEndpointRejectsEmptyQuery(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=+", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(openServerTestDB(t))).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
	var response errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error == "" {
		t.Fatalf("expected frontend-ready error response, got %#v", response)
	}
}

func TestSearchEndpointRejectsOverlongQuery(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/search?q="+strings.Repeat("a", 513), nil)
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(openServerTestDB(t))).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
	var response errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error == "" {
		t.Fatalf("expected frontend-ready error response, got %#v", response)
	}
}

func TestSearchEndpointDefaultsInvalidLimit(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedServerSearchDocuments(t, db)
	for _, target := range []string{
		"/api/search?q=Kapsel",
		"/api/search?q=Kapsel&limit=invalid",
		"/api/search?q=Kapsel&limit=0",
		"/api/search?q=Kapsel&limit=-1",
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()

		NewHandler(WithDatabase(db)).ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status %d for %s, got %d", http.StatusOK, target, rec.Code)
		}
		var response searchResponse
		if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response.Limit != 20 {
			t.Fatalf("expected default limit 20 for %s, got %d", target, response.Limit)
		}
	}
}

func TestSearchEndpointEscapesSnippetHTML(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	_, err := db.Exec(
		"INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES ('video', 'vid-xss', 'title', ?)",
		`<img src=x onerror=alert(1)> kapsel`,
	)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=kapsel", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response searchResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 {
		t.Fatalf("expected one result, got %#v", response.Data)
	}
	snippet := response.Data[0].Snippet
	if strings.Contains(snippet, "<img") {
		t.Fatalf("expected snippet HTML to be escaped, got %q", snippet)
	}
	if !strings.Contains(snippet, "&lt;img") || !strings.Contains(snippet, "<mark>kapsel</mark>") {
		t.Fatalf("expected escaped snippet with highlight markup, got %q", snippet)
	}
}

func TestSearchEndpointReturnsJSONOnSearchFailure(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	handler := NewHandler(WithDatabase(db))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=kapsel", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}
	var response errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error == "" {
		t.Fatalf("expected JSON error response, got %#v", response)
	}
}

func TestSearchEndpointCapsLimit(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	for i := range 75 {
		_, err := db.Exec(
			"INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES (?, ?, ?, ?)",
			"video",
			fmt.Sprintf("vid-many-%03d", i),
			"title",
			"kapsel archive result",
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=kapsel&limit=500", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response searchResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Limit != 50 || len(response.Data) != 50 {
		t.Fatalf("expected capped limit/results 50, got limit=%d len=%d", response.Limit, len(response.Data))
	}
}

func TestVideoListEndpointPaginationAndCap(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoList(t, db, 55)

	req := httptest.NewRequest(http.MethodGet, "/api/videos", nil)
	rec := httptest.NewRecorder()
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)
	defaultResponse := decodeVideoListResponse(t, rec)
	if len(defaultResponse.Data) != 20 {
		t.Fatalf("expected default page size 20, got %d", len(defaultResponse.Data))
	}
	if defaultResponse.Pagination.PageSize != 20 || defaultResponse.Pagination.Total != 55 {
		t.Fatalf("unexpected default pagination: %#v", defaultResponse.Pagination)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos?page=2&page_size=5", nil)
	rec = httptest.NewRecorder()
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)
	customResponse := decodeVideoListResponse(t, rec)
	if len(customResponse.Data) != 5 || customResponse.Pagination.Page != 2 || customResponse.Pagination.PageSize != 5 {
		t.Fatalf("unexpected custom pagination: len=%d pagination=%#v", len(customResponse.Data), customResponse.Pagination)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos?page_size=500", nil)
	rec = httptest.NewRecorder()
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)
	cappedResponse := decodeVideoListResponse(t, rec)
	if len(cappedResponse.Data) != 50 || cappedResponse.Pagination.PageSize != 50 {
		t.Fatalf("expected max page size 50, len=%d pagination=%#v", len(cappedResponse.Data), cappedResponse.Pagination)
	}

	handler := NewHandler(WithDatabase(db))
	for _, endpoint := range []string{"/api/home/videos?page_size=500", "/api/channels/chan-a/videos?page_size=500"} {
		req = httptest.NewRequest(http.MethodGet, endpoint, nil)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		response := decodeVideoListResponse(t, rec)
		if response.Pagination.PageSize != 50 {
			t.Fatalf("expected %s to cap page size at 50, got %#v", endpoint, response.Pagination)
		}
	}
}

func TestVideoListEndpointSortAndFilters(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoList(t, db, 6)
	if _, err := db.Exec(`
UPDATE videos
SET duration_seconds = CASE id WHEN 'vid-005' THEN 600 WHEN 'vid-003' THEN 180 ELSE duration_seconds END,
    view_count = CASE id WHEN 'vid-003' THEN 900 WHEN 'vid-005' THEN 120 ELSE view_count END,
    published_at = CASE id WHEN 'vid-000' THEN NULL ELSE published_at END,
    archived_at = CASE id WHEN 'vid-000' THEN '2026-06-01T00:00:00Z' ELSE archived_at END`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/videos?sort=newest&page_size=1", nil)
	rec := httptest.NewRecorder()
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)
	dateSorted := decodeVideoListResponse(t, rec)
	if dateSorted.Data[0].ID != "vid-005" || dateSorted.Data[0].PublishedAt != "2026-05-06" {
		t.Fatalf("expected newest sort to prefer published dates before archive fallbacks, got %#v", dateSorted.Data)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos?sort=oldest&page_size=2", nil)
	rec = httptest.NewRecorder()
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)
	oldestSorted := decodeVideoListResponse(t, rec)
	if oldestSorted.Data[0].ID != "vid-001" || oldestSorted.Data[1].ID != "vid-002" {
		t.Fatalf("unexpected oldest sort order: %#v", oldestSorted.Data)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos?sort=length&page_size=1", nil)
	rec = httptest.NewRecorder()
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)
	lengthSorted := decodeVideoListResponse(t, rec)
	if lengthSorted.Data[0].ID != "vid-005" || lengthSorted.Data[0].DurationSeconds != 600 {
		t.Fatalf("expected length sort by duration, got %#v", lengthSorted.Data)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos?sort=popularity&page_size=1", nil)
	rec = httptest.NewRecorder()
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)
	popularSorted := decodeVideoListResponse(t, rec)
	if popularSorted.Data[0].ID != "vid-003" || popularSorted.Data[0].ViewCount != 900 {
		t.Fatalf("expected popularity sort by view count, got %#v", popularSorted.Data)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos?sort=published&order=asc&page_size=2", nil)
	rec = httptest.NewRecorder()
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)
	sorted := decodeVideoListResponse(t, rec)
	if sorted.Data[0].ID != "vid-001" || sorted.Data[1].ID != "vid-002" {
		t.Fatalf("unexpected published sort order: %#v", sorted.Data)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos?channel=chan-b&page_size=10", nil)
	rec = httptest.NewRecorder()
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)
	channelFiltered := decodeVideoListResponse(t, rec)
	if len(channelFiltered.Data) != 3 {
		t.Fatalf("expected 3 channel-filtered videos, got %d", len(channelFiltered.Data))
	}
	for _, video := range channelFiltered.Data {
		if video.Channel.ID != "chan-b" {
			t.Fatalf("unexpected channel-filtered video: %#v", video)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos?playlist=playlist-1&sort=published&order=asc&page_size=10", nil)
	rec = httptest.NewRecorder()
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)
	playlistFiltered := decodeVideoListResponse(t, rec)
	if len(playlistFiltered.Data) != 2 {
		t.Fatalf("expected 2 playlist-filtered videos, got %d", len(playlistFiltered.Data))
	}
	foundProgress := false
	for _, video := range playlistFiltered.Data {
		if video.ID == "vid-000" && video.Progress.PositionSeconds == 42 {
			foundProgress = true
		}
	}
	if !foundProgress {
		t.Fatalf("expected progress in list response, got %#v", playlistFiltered.Data)
	}
}

func TestVideoListEndpointNewestPrefersSourceDatesBeforeIndexFallbacks(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, updated_at) VALUES ('chan-indexed', 'chan-indexed', 'Indexed Channel', '2026-06-01T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, published_at, catalog_position, created_at)
VALUES
  ('dated-old', 'dated-old', 'chan-indexed', 'Dated old video', '2026-01-01', -1, '2026-01-01T00:00:00Z'),
  ('undated-first', 'undated-first', 'chan-indexed', 'Undated first catalog entry', NULL, 0, '2026-05-30T00:00:00Z'),
  ('undated-second', 'undated-second', 'chan-indexed', 'Undated second catalog entry', NULL, 1, '2026-05-31T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE videos SET archived_at = '2026-06-02T00:00:00Z' WHERE id = 'undated-first'"); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(WithDatabase(db))
	for _, endpoint := range []string{
		"/api/videos?sort=newest&page_size=3",
		"/api/videos?channel=chan-indexed&sort=newest&page_size=3",
		"/api/channels/chan-indexed/videos?sort=newest&page_size=3",
	} {
		req := httptest.NewRequest(http.MethodGet, endpoint, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		response := decodeVideoListResponse(t, rec)
		if got := videoListIDs(response.Data); got != "dated-old,undated-first,undated-second" {
			t.Fatalf("expected %s newest to prefer source dates before local fallbacks, got %s", endpoint, got)
		}
	}
}

func TestVideoListEndpointSortsRecentlyDownloadedVideos(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	if _, err := db.Exec(`
INSERT INTO channels (id, external_id, name)
VALUES ('chan-down', 'chan-down', 'Downloaded Channel'), ('chan-other', 'chan-other', 'Other Channel')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, published_at, media_path, media_downloaded_at)
VALUES
  ('download-newer', 'download-newer', 'chan-down', 'Downloaded newer', '2026-01-01', 'media/newer.mp4', '2026-05-12T00:00:00Z'),
  ('catalog-new', 'catalog-new', 'chan-down', 'Catalog newest source date', '2026-06-01', '', ''),
  ('download-older', 'download-older', 'chan-down', 'Downloaded older', '2026-04-01', 'media/older.mp4', '2026-05-10T00:00:00Z'),
  ('other-download', 'other-download', 'chan-other', 'Other downloaded newest', '2026-01-02', 'media/other.mp4', '2026-05-13T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"media/newer.mp4", "media/older.mp4", "media/other.mp4"} {
		writeServerFile(t, filepath.Join(root, path), "video")
	}

	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")))
	for _, test := range []struct {
		endpoint string
		want     string
	}{
		{endpoint: "/api/videos?sort=downloaded&page_size=4", want: "other-download,download-newer,download-older,catalog-new"},
		{endpoint: "/api/home/videos?sort=downloaded&page_size=4", want: "other-download,download-newer,download-older,catalog-new"},
		{endpoint: "/api/videos?channel=chan-down&sort=downloaded&page_size=3", want: "download-newer,download-older,catalog-new"},
		{endpoint: "/api/channels/chan-down/videos?sort=downloaded&page_size=3", want: "download-newer,download-older,catalog-new"},
	} {
		req := httptest.NewRequest(http.MethodGet, test.endpoint, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		response := decodeVideoListResponse(t, rec)
		if got := videoListIDs(response.Data); got != test.want {
			t.Fatalf("expected %s downloaded sort %s, got %s", test.endpoint, test.want, got)
		}
		if response.Data[0].ArchiveState != "downloaded" || response.Data[len(response.Data)-1].ArchiveState != "catalog-only" {
			t.Fatalf("expected %s to keep downloaded media ahead of catalog-only rows, got %#v", test.endpoint, response.Data)
		}
	}
}

func TestUpNextEndpointPrefersPlayableCandidates(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	if _, err := db.Exec(`
INSERT INTO channels (id, external_id, name)
VALUES ('chan-current', 'chan-current', 'Current Channel'), ('chan-other', 'chan-other', 'Other Channel')`); err != nil {
		t.Fatal(err)
	}
	videos := []struct {
		id        string
		channelID string
		date      string
		mediaPath string
	}{
		{id: "current", channelID: "chan-current", date: "2026-05-07", mediaPath: "videos/current.mp4"},
		{id: "same-catalog-newest", channelID: "chan-current", date: "2026-05-09"},
		{id: "same-started", channelID: "chan-current", date: "2026-05-03", mediaPath: "videos/same-started.mp4"},
		{id: "same-unstarted", channelID: "chan-current", date: "2026-05-04", mediaPath: "videos/same-unstarted.mp4"},
		{id: "other-available", channelID: "chan-other", date: "2026-05-08", mediaPath: "videos/other-available.mp4"},
		{id: "watched-video-flag", channelID: "chan-other", date: "2026-05-11", mediaPath: "videos/watched-video-flag.mp4"},
		{id: "watched-progress-flag", channelID: "chan-other", date: "2026-05-10", mediaPath: "videos/watched-progress-flag.mp4"},
		{id: "other-catalog", channelID: "chan-other", date: "2026-05-06"},
	}
	for _, video := range videos {
		_, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, published_at, duration_seconds, media_path)
VALUES (?, ?, ?, ?, ?, 120, ?)`, video.id, video.id, video.channelID, video.id, video.date, video.mediaPath)
		if err != nil {
			t.Fatal(err)
		}
		if video.mediaPath != "" {
			writeServerFile(t, filepath.Join(root, video.mediaPath), "video")
		}
	}
	if _, err := db.Exec("INSERT INTO user_progress (video_id, position_seconds, duration_seconds, watched) VALUES ('same-started', 42, 120, 0)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE videos SET watched = 1 WHERE id = 'watched-video-flag'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO user_progress (video_id, position_seconds, duration_seconds, watched) VALUES ('watched-progress-flag', 120, 120, 1)"); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")))

	req := httptest.NewRequest(http.MethodGet, "/api/videos/current/up-next?page_size=5", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	response := decodeVideoListResponse(t, rec)
	if got := videoListIDs(response.Data); got != "same-started,same-unstarted,other-available,same-catalog-newest,other-catalog" {
		t.Fatalf("unexpected up next order: %s", got)
	}
	if response.Data[2].ArchiveState != "downloaded" || response.Data[3].ArchiveState != "catalog-only" {
		t.Fatalf("expected available other-channel video before same-channel catalog video, got %#v", response.Data)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos/missing/up-next", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected missing video status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestVideoListEndpointChannelDateSortFallsBackToCatalogOrder(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, updated_at) VALUES ('chan-catalog', 'chan-catalog', 'Catalog Channel', '2026-05-05T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, catalog_position, created_at)
VALUES
  ('vid-b', 'vid-b', 'chan-catalog', 'First catalog entry', 0, '2026-05-04T00:00:01Z'),
  ('vid-c', 'vid-c', 'chan-catalog', 'Second catalog entry', 1, '2026-05-04T00:00:02Z'),
  ('vid-a', 'vid-a', 'chan-catalog', 'Third catalog entry', 2, '2026-05-04T00:00:03Z')`); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(WithDatabase(db))
	for _, endpoint := range []string{"/api/videos?channel=chan-catalog", "/api/channels/chan-catalog/videos"} {
		separator := "?"
		if strings.Contains(endpoint, "?") {
			separator = "&"
		}
		req := httptest.NewRequest(http.MethodGet, endpoint+separator+"sort=newest&page_size=3", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		response := decodeVideoListResponse(t, rec)
		if len(response.Data) != 3 || response.Data[0].ID != "vid-b" || response.Data[1].ID != "vid-c" || response.Data[2].ID != "vid-a" {
			t.Fatalf("expected %s newest to use catalog entry order for undated videos, got %#v", endpoint, response.Data)
		}

		req = httptest.NewRequest(http.MethodGet, endpoint+separator+"sort=oldest&page_size=3", nil)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		response = decodeVideoListResponse(t, rec)
		if len(response.Data) != 3 || response.Data[0].ID != "vid-a" || response.Data[1].ID != "vid-c" || response.Data[2].ID != "vid-b" {
			t.Fatalf("expected %s oldest to reverse catalog entry order for undated videos, got %#v", endpoint, response.Data)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/channels/missing/videos", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected missing channel videos status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestVideoListEndpointHomeDateSortFallsBackToCatalogOrder(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name, updated_at) VALUES ('chan-catalog', 'chan-catalog', 'Catalog Channel', '2026-05-05T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, catalog_position, created_at)
VALUES
  ('vid-b', 'vid-b', 'chan-catalog', 'First catalog entry', 0, '2026-05-04T00:00:01Z'),
  ('vid-c', 'vid-c', 'chan-catalog', 'Second catalog entry', 1, '2026-05-04T00:00:02Z'),
  ('vid-a', 'vid-a', 'chan-catalog', 'Third catalog entry', 2, '2026-05-04T00:00:03Z')`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/videos?sort=newest&page_size=3", nil)
	rec := httptest.NewRecorder()
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)

	response := decodeVideoListResponse(t, rec)
	if len(response.Data) != 3 || response.Data[0].ID != "vid-b" || response.Data[1].ID != "vid-c" || response.Data[2].ID != "vid-a" {
		t.Fatalf("expected home newest to use catalog entry order for undated videos, got %#v", response.Data)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos?sort=oldest&page_size=3", nil)
	rec = httptest.NewRecorder()
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)

	response = decodeVideoListResponse(t, rec)
	if len(response.Data) != 3 || response.Data[0].ID != "vid-a" || response.Data[1].ID != "vid-c" || response.Data[2].ID != "vid-b" {
		t.Fatalf("expected home oldest to reverse catalog entry order for undated videos, got %#v", response.Data)
	}
}

func TestVideoListEndpointHomeDefaultsToRecentlyWatchedUnfinished(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-home', 'chan-home', 'Home Channel')"); err != nil {
		t.Fatal(err)
	}
	videos := []struct {
		id        string
		date      string
		mediaPath string
		watched   int
	}{
		{id: "vid-catalog-new", date: "2026-05-08"},
		{id: "vid-finished-recent", date: "2026-05-07", mediaPath: "videos/finished.mp4", watched: 1},
		{id: "vid-progress-finished", date: "2026-05-04", mediaPath: "videos/progress-finished.mp4"},
		{id: "vid-unstarted-newer", date: "2026-05-06", mediaPath: "videos/unstarted.mp4"},
		{id: "vid-progress-older", date: "2026-05-03", mediaPath: "videos/progress-older.mp4"},
		{id: "vid-progress-newer", date: "2026-05-01", mediaPath: "videos/progress-newer.mp4"},
	}
	for _, video := range videos {
		_, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, published_at, duration_seconds, media_path, watched)
VALUES (?, ?, 'chan-home', ?, ?, 120, ?, ?)`, video.id, video.id, video.id, video.date, video.mediaPath, video.watched)
		if err != nil {
			t.Fatal(err)
		}
		if video.mediaPath != "" {
			writeServerFile(t, filepath.Join(root, video.mediaPath), "video")
		}
	}
	if _, err := db.Exec(`
INSERT INTO user_progress (video_id, position_seconds, duration_seconds, watched, updated_at)
VALUES
	  ('vid-progress-older', 42, 120, 0, '2026-05-07T10:00:00Z'),
	  ('vid-progress-newer', 12, 120, 0, '2026-05-08T10:00:00Z'),
	  ('vid-progress-finished', 120, 120, 1, '2026-05-08T11:00:00Z'),
	  ('vid-finished-recent', 119, 120, 1, '2026-05-09T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")))

	for _, endpoint := range []string{"/api/videos?home=1", "/api/home/videos"} {
		separator := "?"
		if strings.Contains(endpoint, "?") {
			separator = "&"
		}
		for _, query := range []string{"page_size=6", "sort=watching&page_size=6"} {
			req := httptest.NewRequest(http.MethodGet, endpoint+separator+query, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			response := decodeVideoListResponse(t, rec)
			if got := videoListIDs(response.Data); got != "vid-progress-newer,vid-progress-older,vid-unstarted-newer,vid-catalog-new" {
				t.Fatalf("unexpected default home watching order for %s %s: %s", endpoint, query, got)
			}
			if response.Pagination.Total != 4 {
				t.Fatalf("expected default home feed to count only unfinished videos for %s %s, got %d", endpoint, query, response.Pagination.Total)
			}
			if response.Data[0].Progress.Watched || response.Data[0].Progress.PositionSeconds != 12 || response.Data[1].Progress.PositionSeconds != 42 {
				t.Fatalf("expected unfinished progress videos first for %s %s, got %#v", endpoint, query, response.Data[:2])
			}
		}

		req := httptest.NewRequest(http.MethodGet, endpoint+separator+"sort=newest&page_size=6", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		response := decodeVideoListResponse(t, rec)
		if got := videoListIDs(response.Data); got != "vid-catalog-new,vid-finished-recent,vid-unstarted-newer,vid-progress-finished,vid-progress-older,vid-progress-newer" {
			t.Fatalf("expected explicit newest home sort for %s to remain available, got %s", endpoint, got)
		}

		req = httptest.NewRequest(http.MethodGet, endpoint+separator+"sort=oldest&page_size=6", nil)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		response = decodeVideoListResponse(t, rec)
		if got := videoListIDs(response.Data); got != "vid-progress-newer,vid-progress-older,vid-progress-finished,vid-unstarted-newer,vid-finished-recent,vid-catalog-new" {
			t.Fatalf("expected explicit oldest home sort for %s to keep watched videos available, got %s", endpoint, got)
		}
	}
}

func TestVideoListEndpointHomeFlagDoesNotAlterExplicitSort(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-home', 'chan-home', 'Home Channel')"); err != nil {
		t.Fatal(err)
	}
	videos := []struct {
		id        string
		date      string
		mediaPath string
		watched   int
	}{
		{id: "vid-catalog-new", date: "2026-05-06", watched: 0},
		{id: "vid-watched-downloaded", date: "2026-05-05", mediaPath: "videos/watched.mp4", watched: 1},
		{id: "vid-progress-watched", date: "2026-05-04", mediaPath: "videos/progress-watched.mp4", watched: 0},
		{id: "vid-unwatched-newer", date: "2026-05-03", mediaPath: "videos/unwatched-newer.mp4", watched: 0},
		{id: "vid-unwatched-older", date: "2026-05-01", mediaPath: "videos/unwatched-older.mp4", watched: 0},
	}
	for _, video := range videos {
		_, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, published_at, duration_seconds, media_path, watched)
VALUES (?, ?, 'chan-home', ?, ?, 120, ?, ?)`, video.id, video.id, video.id, video.date, video.mediaPath, video.watched)
		if err != nil {
			t.Fatal(err)
		}
		if video.mediaPath != "" {
			writeServerFile(t, filepath.Join(root, video.mediaPath), "video")
		}
	}
	if _, err := db.Exec("INSERT INTO user_progress (video_id, position_seconds, duration_seconds, watched) VALUES ('vid-progress-watched', 120, 120, 1)"); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")))
	ids := func(items []videoListItem) string {
		values := make([]string, len(items))
		for i, item := range items {
			values[i] = item.ID
		}

		return strings.Join(values, ",")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/videos?home=1&sort=newest&page_size=5", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	response := decodeVideoListResponse(t, rec)
	if got := ids(response.Data); got != "vid-catalog-new,vid-watched-downloaded,vid-progress-watched,vid-unwatched-newer,vid-unwatched-older" {
		t.Fatalf("unexpected home sort order: %s", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos?sort=newest&page_size=5", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	response = decodeVideoListResponse(t, rec)
	if got := ids(response.Data); got != "vid-catalog-new,vid-watched-downloaded,vid-progress-watched,vid-unwatched-newer,vid-unwatched-older" {
		t.Fatalf("expected plain video list to keep selected sort order, got %s", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos?home=1&channel=chan-home&sort=newest&page_size=5", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	response = decodeVideoListResponse(t, rec)
	if got := ids(response.Data); got != "vid-catalog-new,vid-watched-downloaded,vid-progress-watched,vid-unwatched-newer,vid-unwatched-older" {
		t.Fatalf("expected scoped video list to ignore home ordering, got %s", got)
	}
}

func TestGetChannelEndpoint(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoList(t, db, 6)
	if _, err := db.Exec("UPDATE channels SET handle = '@archive', description = 'Local archive channel', thumbnail_url = 'https://yt3.ggpht.com/archive.jpg' WHERE id = 'chan-b'"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/channels/chan-b", nil)
	rec := httptest.NewRecorder()
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response channelResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.ID != "chan-b" || response.Name != "Channel B" || response.Handle != "@archive" || response.ThumbnailURL != "https://yt3.ggpht.com/archive.jpg" || response.VideoCount != 3 {
		t.Fatalf("unexpected channel response: %#v", response)
	}
}

func TestGetChannelEndpointReturnsNotFound(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/channels/missing", nil)
	rec := httptest.NewRecorder()
	NewHandler(WithDatabase(openServerTestDB(t))).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestListChannelsEndpointPaginates(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoList(t, db, 6)
	if _, err := db.Exec("UPDATE channels SET subscribed = 1, thumbnail_url = 'https://yt3.ggpht.com/channel-b.jpg' WHERE id = 'chan-b'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-c', 'chan-c', 'Channel C')"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/channels?page=2&page_size=2", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response channelListResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Pagination.Total != 3 || response.Pagination.PageSize != 2 || len(response.Data) != 1 {
		t.Fatalf("unexpected channel pagination: %#v", response)
	}
	if response.Data[0].ID != "chan-c" || response.Data[0].VideoCount != 0 {
		t.Fatalf("unexpected channel page item: %#v", response.Data[0])
	}
	if response.Data[0].ThumbnailURL != "" {
		t.Fatalf("expected missing channel thumbnail to be omitted, got %#v", response.Data[0])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/channels?page=1&page_size=2", nil)
	rec = httptest.NewRecorder()
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 2 || response.Data[1].ID != "chan-b" || response.Data[1].ThumbnailURL != "https://yt3.ggpht.com/channel-b.jpg" {
		t.Fatalf("expected channel thumbnail in list response, got %#v", response.Data)
	}
}

func TestListPlaylistsEndpointPaginates(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoList(t, db, 3)
	if _, err := db.Exec("INSERT INTO playlists (id, external_id, title, subscribed) VALUES ('playlist-2', 'playlist-2', 'Playlist Two', 1), ('playlist-3', 'playlist-3', 'Playlist Three', 0)"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/playlists?page=2&page_size=2", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response playlistListResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Pagination.Total != 3 || response.Pagination.PageSize != 2 || len(response.Data) != 1 {
		t.Fatalf("unexpected playlist pagination: %#v", response)
	}
	if response.Data[0].ID != "playlist-2" || !response.Data[0].Subscribed {
		t.Fatalf("unexpected playlist page item: %#v", response.Data[0])
	}
}

func TestGetPlaylistEndpointAndVideosOrderByPosition(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoList(t, db, 4)
	if _, err := db.Exec("DELETE FROM playlist_entries WHERE playlist_id = 'playlist-1'"); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		videoID  string
		position int
	}{
		{videoID: "vid-002", position: 2},
		{videoID: "vid-000", position: 0},
		{videoID: "vid-001", position: 1},
	} {
		if _, err := db.Exec("INSERT INTO playlist_entries (playlist_id, video_id, position) VALUES ('playlist-1', ?, ?)", entry.videoID, entry.position); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/playlists/playlist-1", nil)
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected playlist detail status %d, got %d", http.StatusOK, rec.Code)
	}
	var detail playlistResponse
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != "playlist-1" || detail.VideoCount != 3 {
		t.Fatalf("unexpected playlist detail: %#v", detail)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/playlists/playlist-1/videos?page_size=2", nil)
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected playlist videos status %d, got %d", http.StatusOK, rec.Code)
	}
	var videos videoListResponse
	if err := json.NewDecoder(rec.Body).Decode(&videos); err != nil {
		t.Fatal(err)
	}
	if videos.Pagination.Total != 3 || videos.Pagination.PageSize != 2 || len(videos.Data) != 2 {
		t.Fatalf("unexpected playlist videos pagination: %#v", videos)
	}
	if videos.Data[0].ID != "vid-000" || videos.Data[1].ID != "vid-001" {
		t.Fatalf("expected playlist position order, got %#v", videos.Data)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/playlists/playlist-1/videos?page=0&page_size=999", nil)
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)
	videos = decodeVideoListResponse(t, rec)
	if videos.Pagination.Page != 1 || videos.Pagination.PageSize != 50 || videos.Pagination.Total != 3 {
		t.Fatalf("expected bounded playlist video pagination, got %#v", videos.Pagination)
	}
}

func TestListVideoCommentsPaginatesParentsAndReplies(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoList(t, db, 1)
	_, err := db.Exec(`
INSERT INTO comments (id, video_id, parent_id, author, text, published_at, like_count) VALUES
  ('comment-1', 'vid-000', NULL, 'Archivist', 'First parent comment', '2026-05-03T12:00:00Z', 7),
  ('comment-2', 'vid-000', NULL, 'Viewer', 'Second parent comment', '2026-05-03T12:01:00Z', 3),
  ('reply-1', 'vid-000', 'comment-1', 'Reply Author', 'A bounded reply', '2026-05-03T12:02:00Z', 1)`)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/videos/vid-000/comments?page=0&page_size=1", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected comments status %d, got %d", http.StatusOK, rec.Code)
	}
	var parents commentListResponse
	if err := json.NewDecoder(rec.Body).Decode(&parents); err != nil {
		t.Fatal(err)
	}
	if parents.Pagination.Page != 1 || parents.Pagination.PageSize != 1 || parents.Pagination.Total != 2 || len(parents.Data) != 1 {
		t.Fatalf("unexpected parent comment pagination: %#v", parents)
	}
	if parents.Data[0].ID != "comment-1" || parents.Data[0].ReplyCount != 1 {
		t.Fatalf("unexpected parent comment: %#v", parents.Data[0])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos/vid-000/comments?parent=comment-1&page_size=50", nil)
	rec = httptest.NewRecorder()
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)
	var replies commentListResponse
	if err := json.NewDecoder(rec.Body).Decode(&replies); err != nil {
		t.Fatal(err)
	}
	if replies.Pagination.Total != 1 || len(replies.Data) != 1 || replies.Data[0].ParentID != "comment-1" {
		t.Fatalf("unexpected reply comment page: %#v", replies)
	}
}

func TestDeleteChannelRequiresNoLocalReferences(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoList(t, db, 2)
	req := httptest.NewRequest(http.MethodDelete, "/api/channels/chan-a", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected referenced channel delete status %d, got %d", http.StatusConflict, rec.Code)
	}
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-empty', 'chan-empty', 'Empty Channel')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES ('channel', 'chan-empty', 'name', 'Empty Channel')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO media_assets (owner_type, owner_id, kind, path) VALUES ('channel', 'chan-empty', 'thumbnail', 'cache/channels/chan-empty.jpg')"); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodDelete, "/api/channels/chan-empty", nil)
	rec = httptest.NewRecorder()
	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected empty channel delete status %d, got %d", http.StatusNoContent, rec.Code)
	}
	assertServerScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'channel' AND owner_id = ?", int64(0), "chan-empty")
	assertServerScalar(t, db, "SELECT count(*) FROM media_assets WHERE owner_type = 'channel' AND owner_id = ?", int64(0), "chan-empty")
}

func TestDeletePlaylistRemovesMetadataOnly(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	seedVideoList(t, db, 2)
	if _, err := db.Exec("INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES ('playlist', 'playlist-1', 'title', 'Playlist One')"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/playlists/playlist-1", nil)
	rec := httptest.NewRecorder()

	NewHandler(WithDatabase(db)).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected playlist delete status %d, got %d", http.StatusNoContent, rec.Code)
	}
	assertServerScalar(t, db, "SELECT count(*) FROM playlists WHERE id = ?", int64(0), "playlist-1")
	assertServerScalar(t, db, "SELECT count(*) FROM playlist_entries WHERE playlist_id = ?", int64(0), "playlist-1")
	assertServerScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'playlist' AND owner_id = ?", int64(0), "playlist-1")
	assertServerScalar(t, db, "SELECT count(*) FROM videos", int64(2))
}

func decodeVideoListResponse(t *testing.T, rec *httptest.ResponseRecorder) videoListResponse {
	t.Helper()

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response videoListResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}

	return response
}

func videoListItemByID(t *testing.T, items []videoListItem, id string) videoListItem {
	t.Helper()

	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("expected video %q in %#v", id, items)

	return videoListItem{}
}

func assertSharedVideoListItem(t *testing.T, handler http.Handler, item videoListItem) {
	t.Helper()
	if item.ID != "shared-video" || item.Title != "Shared Video" || item.Description != "Shared description" {
		t.Fatalf("unexpected shared video identity fields: %#v", item)
	}
	if item.PublishedAt != "2026-05-03" || item.ArchivedAt != "2026-05-04" || item.DurationSeconds != 120 || item.ViewCount != 42 {
		t.Fatalf("unexpected shared video metadata fields: %#v", item)
	}
	if item.ThumbnailFallback != "S" || item.ArchiveState != "downloaded" || item.CanDownload || !item.KeepForever {
		t.Fatalf("unexpected shared video state fields: %#v", item)
	}
	if item.Channel.ID != "chan-shared" || item.Channel.Name != "Shared Channel" {
		t.Fatalf("unexpected shared video channel fields: %#v", item.Channel)
	}
	if item.Progress.PositionSeconds != 33 || item.Progress.DurationSeconds != 120 || item.Progress.Watched {
		t.Fatalf("unexpected shared video progress fields: %#v", item.Progress)
	}
	assertMediaURLServes(t, handler, item.ThumbnailURL, "shared thumb")
	assertMediaURLServes(t, handler, item.Channel.ThumbnailURL, "shared channel thumb")
}

func videoListIDs(items []videoListItem) string {
	values := make([]string, len(items))
	for i, item := range items {
		values[i] = item.ID
	}

	return strings.Join(values, ",")
}

func serverStorageUsageBytes(summary storage.Summary, category storage.Category) int64 {
	for _, usage := range summary.Usage {
		if usage.Category == category {
			return usage.Bytes
		}
	}

	return 0
}

func decodeProgressInfo(t *testing.T, rec *httptest.ResponseRecorder) progressInfo {
	t.Helper()

	var response progressInfo
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}

	return response
}

func openServerTestDB(t *testing.T) *sql.DB {
	t.Helper()

	return openServerTestDBAt(t, filepath.Join(t.TempDir(), "kapsel.db"))
}

func openServerTestDBAt(t *testing.T, path string) *sql.DB {
	t.Helper()

	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	return db
}

func supportedSchemaVersion(t *testing.T) int {
	t.Helper()

	version, err := database.SupportedSchemaVersion()
	if err != nil {
		t.Fatal(err)
	}

	return version
}

func seedVideoWithAssets(t *testing.T, db *sql.DB) {
	t.Helper()

	_, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Channel One')")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, published_at, duration_seconds, media_path, thumbnail_path)
VALUES ('vid-1', 'vid-1', 'chan-1', 'Video One', '2026-05-03', 120, 'videos/sample.mp4', 'thumbs/sample.jpg')`)
	if err != nil {
		t.Fatal(err)
	}
}

func seedVideoWithoutThumbnail(t *testing.T, db *sql.DB) {
	t.Helper()

	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Channel One')"); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, published_at, duration_seconds, media_path, thumbnail_path)
VALUES ('vid-1', 'vid-1', 'chan-1', 'Video One', '2026-05-03', 120, 'videos/sample.mp4', '')`)
	if err != nil {
		t.Fatal(err)
	}
}

func seedCatalogOnlyVideoWithThumbnail(t *testing.T, db *sql.DB) {
	t.Helper()

	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-1', 'chan-1', 'Channel One')"); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, published_at, duration_seconds, media_path, thumbnail_path)
VALUES ('vid-1', 'vid-1', 'chan-1', 'Video One', '2026-05-03', 120, '', 'thumbs/catalog.jpg')`)
	if err != nil {
		t.Fatal(err)
	}
}

func seedTimelinePreview(t *testing.T, db *sql.DB) {
	t.Helper()

	_, err := db.Exec(`
INSERT INTO video_previews (video_id, sprite_path, interval_seconds, frame_width, frame_height, columns, preview_count)
VALUES ('vid-1', 'derived/previews/vid-1/sprite.jpg', 10, 160, 90, 5, 3)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO media_assets (owner_type, owner_id, kind, path) VALUES ('video', 'vid-1', 'timeline_preview_sprite', 'derived/previews/vid-1/sprite.jpg')")
	if err != nil {
		t.Fatal(err)
	}
}

func seedSubtitleTrack(t *testing.T, db *sql.DB) {
	t.Helper()

	_, err := db.Exec("INSERT INTO subtitles (video_id, language, name, source, format, path, text) VALUES ('vid-1', 'en', 'English', 'downloaded', 'vtt', 'subtitles/vid-1.en.vtt', ?)", strings.Repeat("caption ", 1024))
	if err != nil {
		t.Fatal(err)
	}
}

func writeServerFile(t *testing.T, path string, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertServerScalar[T comparable](t *testing.T, db *sql.DB, query string, expected T, args ...any) {
	t.Helper()
	var got T
	if err := db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Fatalf("expected %v, got %v", expected, got)
	}
}

func serverJobPayloadJSON(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var payloadJSON string
	if err := db.QueryRow("SELECT payload_json FROM jobs WHERE id = ?", id).Scan(&payloadJSON); err != nil {
		t.Fatal(err)
	}
	return payloadJSON
}

type publicJobResponseFixture struct {
	ID              string      `json:"id"`
	Type            string      `json:"type"`
	Status          jobs.Status `json:"status"`
	Priority        int         `json:"priority"`
	Attempts        int         `json:"attempts"`
	MaxAttempts     int         `json:"max_attempts"`
	Progress        float64     `json:"progress"`
	Error           string      `json:"error"`
	RunAfter        string      `json:"run_after"`
	LockedAt        string      `json:"locked_at"`
	CancelRequested bool        `json:"cancel_requested"`
	CreatedAt       string      `json:"created_at"`
	UpdatedAt       string      `json:"updated_at"`
	CompletedAt     string      `json:"completed_at"`
	ResultSummary   string      `json:"result_summary"`
}

func decodePublicJobResponse(t *testing.T, body string) publicJobResponseFixture {
	t.Helper()
	assertPublicJobBody(t, body)
	var response publicJobResponseFixture
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func assertPublicJobBody(t *testing.T, body string) {
	t.Helper()
	for _, private := range []string{"payload_json", "result_json", "top-secret"} {
		if strings.Contains(body, private) {
			t.Fatalf("expected public job response to omit %q, got %s", private, body)
		}
	}
}

func assertMediaURLServes(t *testing.T, handler http.Handler, mediaURL string, expected string) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, mediaURL, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected media URL status %d, got %d", http.StatusOK, rec.Code)
	}
	if rec.Body.String() != expected {
		t.Fatalf("expected media body %q, got %q", expected, rec.Body.String())
	}
}

func firstVTTMediaURL(t *testing.T, body string) string {
	t.Helper()

	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "/media/") {
			continue
		}
		mediaURL, _, ok := strings.Cut(line, "#")
		if !ok || mediaURL == "" {
			break
		}

		return mediaURL
	}
	t.Fatalf("expected VTT media URL in %q", body)

	return ""
}

func stringField(t *testing.T, values map[string]any, key string) string {
	t.Helper()

	value, ok := values[key].(string)
	if !ok || value == "" {
		t.Fatalf("expected non-empty string field %q in %#v", key, values)
	}

	return value
}

func seedServerSearchDocuments(t *testing.T, db *sql.DB) {
	t.Helper()

	for _, row := range []struct {
		ownerType string
		ownerID   string
		field     string
		text      string
	}{
		{ownerType: "video", ownerID: "vid-1", field: "title", text: "Kapsel walkthrough"},
		{ownerType: "channel", ownerID: "chan-1", field: "name", text: "Archive Workshop"},
	} {
		_, err := db.Exec(
			"INSERT INTO search_documents (owner_type, owner_id, field, text) VALUES (?, ?, ?, ?)",
			row.ownerType,
			row.ownerID,
			row.field,
			row.text,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
}

type fakeSponsorBlockClient struct {
	mu       sync.Mutex
	segments []sponsorblock.Segment
	err      error
	calls    []string
	wait     <-chan struct{}
	started  chan<- struct{}
}

func (f *fakeSponsorBlockClient) SponsorSegments(_ context.Context, externalID string) ([]sponsorblock.Segment, error) {
	f.mu.Lock()
	f.calls = append(f.calls, externalID)
	started := f.started
	wait := f.wait
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if wait != nil {
		<-wait
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return append([]sponsorblock.Segment(nil), f.segments...), nil
}

func (f *fakeSponsorBlockClient) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func writeServerBackupZip(t *testing.T, root string) {
	t.Helper()

	backupPath := filepath.Join(root, "cache", "backup", "ta_backup-20260503-test.zip")
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	zipFile := zip.NewWriter(file)
	defer zipFile.Close()

	writeServerZipFile(t, zipFile, "es_video-20260503-0.json", serverBulkDocument("vid-1", map[string]any{
		"youtube_id":  "vid-1",
		"title":       "Kapsel Demo",
		"media_url":   "media/vid-1.mp4",
		"description": "A demo video",
	})+"\n"+`{"index":{"_index":"ta_video","_id":"bad"}}`+"\n"+`{"broken":`)
}

func writeServerZipFile(t *testing.T, zipFile *zip.Writer, name string, body string) {
	t.Helper()

	writer, err := zipFile.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
}

func serverBulkDocument(id string, source map[string]any) string {
	action, _ := json.Marshal(map[string]any{"index": map[string]any{"_index": "ta_backup", "_id": id}})
	body, _ := json.Marshal(source)

	return string(action) + "\n" + string(body)
}

func seedVideoList(t *testing.T, db *sql.DB, count int) {
	t.Helper()

	for _, channel := range []struct {
		id   string
		name string
	}{
		{id: "chan-a", name: "Channel A"},
		{id: "chan-b", name: "Channel B"},
	} {
		_, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES (?, ?, ?)", channel.id, channel.id, channel.name)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := db.Exec("INSERT INTO playlists (id, external_id, title) VALUES ('playlist-1', 'playlist-1', 'Playlist One')")
	if err != nil {
		t.Fatal(err)
	}

	for i := range count {
		id := fmt.Sprintf("vid-%03d", i)
		channelID := "chan-a"
		if i%2 == 1 {
			channelID = "chan-b"
		}
		_, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, published_at, duration_seconds, media_path, thumbnail_path)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id,
			id,
			channelID,
			fmt.Sprintf("Video %03d", i),
			fmt.Sprintf("2026-05-%02d", i+1),
			120,
			"media/"+id+".mp4",
			"thumbs/"+id+".jpg",
		)
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			_, err = db.Exec("INSERT INTO playlist_entries (playlist_id, video_id, position) VALUES ('playlist-1', ?, ?)", id, i)
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	_, err = db.Exec("INSERT INTO user_progress (video_id, position_seconds, duration_seconds, watched) VALUES ('vid-000', 42, 120, 0)")
	if err != nil {
		t.Fatal(err)
	}
}

func seedServerJob(t *testing.T, db *sql.DB, store *jobs.Store, id string, status jobs.Status, updatedAt string, resultJSON string) {
	t.Helper()

	if _, err := store.Enqueue(context.Background(), jobs.EnqueueParams{ID: id, Type: "download", PayloadJSON: `{"secret":"top-secret"}`}); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`
UPDATE jobs
SET status = ?, progress = ?, error = ?, result_json = ?, updated_at = ?, completed_at = CASE WHEN ? IN ('succeeded', 'failed', 'cancelled') THEN ? ELSE NULL END
WHERE id = ?`, status, 0.5, serverJobError(status), resultJSON, updatedAt, status, updatedAt, id)
	if err != nil {
		t.Fatal(err)
	}
}

func markServerJobRunningWithCommittedResult(t *testing.T, db *sql.DB, id string, resultJSON string) {
	t.Helper()

	// This legacy state models stale-recovery and retry-safety behavior from older
	// code paths; new final results should use jobs.Store.CompleteWithResult.
	result, err := db.Exec("UPDATE jobs SET result_json = ?, result_committed = 1 WHERE id = ? AND status = ?", resultJSON, id, jobs.StatusRunning)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := result.RowsAffected(); err != nil {
		t.Fatal(err)
	} else if changed != 1 {
		t.Fatalf("expected one running job to receive committed result, changed %d", changed)
	}
}

func serverJobError(status jobs.Status) string {
	if status == jobs.StatusFailed {
		return "download failed: install ffmpeg or disable previews"
	}

	return ""
}

type settingsCheckFixture struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Summary string `json:"summary"`
	Detail  string `json:"detail"`
}

func settingsCheckState(checks []settingsCheckFixture, id string) string {
	for _, check := range checks {
		if check.ID == id {
			return check.State
		}
	}

	return ""
}

func settingsCheckDetail(checks []settingsCheckFixture, id string) string {
	for _, check := range checks {
		if check.ID == id {
			return check.Detail
		}
	}

	return ""
}

var (
	serverAuthHashOnce sync.Once
	serverAuthHash     string
	serverAuthHashErr  error
)

func newServerAuthManager(t *testing.T, now time.Time) *auth.Manager {
	t.Helper()

	serverAuthHashOnce.Do(func() {
		serverAuthHash, serverAuthHashErr = auth.HashPassword("open sesame")
	})
	if serverAuthHashErr != nil {
		t.Fatal(serverAuthHashErr)
	}

	return auth.NewManager(auth.Config{
		Enabled:       true,
		Username:      "admin",
		PasswordHash:  serverAuthHash,
		SessionSecret: "session-secret",
		SessionTTL:    time.Hour,
		Now: func() time.Time {
			return now
		},
	})
}

type serverYTDLPRunner struct {
	stdout   []byte
	err      error
	commands []download.Command
}

func (r *serverYTDLPRunner) Run(_ context.Context, command download.Command) ([]byte, error) {
	r.commands = append(r.commands, command)

	return r.stdout, r.err
}

func TestDeleteChannelRemovesCatalogOnlyVideos(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	if _, err := db.Exec(`
INSERT INTO channels (id, external_id, name, subscribed, updated_at)
VALUES ('chan-purge', 'chan-purge', 'Purge Me', 1, strftime('%Y-%m-%dT%H:%M:%fZ','now'))`); err != nil {
		t.Fatal(err)
	}
	// Catalog-only videos: archived metadata with no downloaded media.
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, duration_seconds, published_at, archived_at)
VALUES ('vid-1', 'vid-1', 'chan-purge', 'Catalog One', 120, '2026-01-01', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
       ('vid-2', 'vid-2', 'chan-purge', 'Catalog Two', 90, '2026-01-02', strftime('%Y-%m-%dT%H:%M:%fZ','now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO search_documents (owner_type, owner_id, field, text)
VALUES ('video', 'vid-1', 'title', 'Catalog One'),
       ('video', 'vid-2', 'title', 'Catalog Two'),
       ('channel', 'chan-purge', 'name', 'Purge Me')`); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/channels/chan-purge", nil)
	NewHandler(WithDatabase(db), WithSupportedSchemaVersion(supportedSchemaVersion(t)), WithMedia(t.TempDir(), media.NewSigner("test-secret"))).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertServerScalar(t, db, "SELECT count(*) FROM channels WHERE id = 'chan-purge'", int64(0))
	assertServerScalar(t, db, "SELECT count(*) FROM videos WHERE channel_id = 'chan-purge'", int64(0))
	assertServerScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'video' AND owner_id IN ('vid-1','vid-2')", int64(0))
	assertServerScalar(t, db, "SELECT count(*) FROM search_documents WHERE owner_type = 'channel' AND owner_id = 'chan-purge'", int64(0))
}

func TestDeleteChannelRefusesWhenVideosHaveMedia(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	if _, err := db.Exec(`
INSERT INTO channels (id, external_id, name, subscribed, updated_at)
VALUES ('chan-media', 'chan-media', 'Keep Me', 1, strftime('%Y-%m-%dT%H:%M:%fZ','now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, duration_seconds, published_at, archived_at, media_path)
VALUES ('vid-m', 'vid-m', 'chan-media', 'Downloaded One', 120, '2026-01-01', strftime('%Y-%m-%dT%H:%M:%fZ','now'), 'videos/vid-m.mp4')`); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/channels/chan-media", nil)
	NewHandler(WithDatabase(db), WithSupportedSchemaVersion(supportedSchemaVersion(t)), WithMedia(t.TempDir(), media.NewSigner("test-secret"))).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertServerScalar(t, db, "SELECT count(*) FROM channels WHERE id = 'chan-media'", int64(1))
	assertServerScalar(t, db, "SELECT count(*) FROM videos WHERE id = 'vid-m'", int64(1))
}

func TestVideoListExcludesMembersOnlyVideos(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-mo', 'chan-mo', 'Channel')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, duration_seconds, published_at, archived_at)
VALUES
  ('vis-1', 'vis-1', 'chan-mo', 'Visible Video', 60, '2026-01-01', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('mem-1', 'mem-1', 'chan-mo', 'Members Only Video', 60, '2026-01-02', strftime('%Y-%m-%dT%H:%M:%fZ','now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE videos SET members_only = 1 WHERE id = 'mem-1'"); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")), WithMediaURLTTL(time.Hour))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/videos?page_size=10", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 || response.Data[0].ID != "vis-1" {
		t.Fatalf("expected only the non-members-only video in the list, got %#v", response.Data)
	}
}

func TestChannelVideoListExcludesMembersOnlyVideos(t *testing.T) {
	t.Parallel()

	db := openServerTestDB(t)
	root := t.TempDir()
	if _, err := db.Exec("INSERT INTO channels (id, external_id, name) VALUES ('chan-mo', 'chan-mo', 'Channel')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, external_id, channel_id, title, duration_seconds, published_at, archived_at)
VALUES
  ('vis-1', 'vis-1', 'chan-mo', 'Visible Video', 60, '2026-01-01', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('mem-1', 'mem-1', 'chan-mo', 'Members Only Video', 60, '2026-01-02', strftime('%Y-%m-%dT%H:%M:%fZ','now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE videos SET members_only = 1 WHERE id = 'mem-1'"); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(WithDatabase(db), WithMedia(root, media.NewSigner("secret")), WithMediaURLTTL(time.Hour))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/channels/chan-mo/videos?page_size=10", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 || response.Data[0].ID != "vis-1" {
		t.Fatalf("expected only the non-members-only video in the channel list, got %#v", response.Data)
	}
}
