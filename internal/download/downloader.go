// Package download owns media acquisition: direct downloads, channel
// catalog scans, auto-downloads, playlist imports, yt-dlp execution, and
// retention cleanup, all modeled as durable jobs.
//
// File boundaries within the package:
//
//   - downloader.go — Downloader core: Config, construction, shared plumbing.
//   - ytdlp.go — yt-dlp execution, command building, pacing/retry, failure
//     classification, and self-update.
//   - ingest.go — download ingestion: payload handling, metadata validation,
//     and persistence of videos/subtitles/thumbnails/search denormalization.
//   - catalog.go — channel jobs and catalog sync: channel first/scan,
//     catalog paging, auto-download sync, channel upserts.
//   - enqueue.go — the public enqueue API: payload normalization and
//     deduplicated enqueueing for every job type.
//   - urls.go — URL normalization and YouTube URL helpers.
//   - retention.go — retention cleanup: candidate selection and media removal.
//   - schedule.go — scheduling policy: the Ensure* functions and enqueue
//     helpers the tick loops call; see docs/scheduler.md for the ownership
//     model.
//   - handlers.go — the job-type dispatcher and shared job-result helpers.
//   - diagnostics.go — yt-dlp installation diagnostics.
package download

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"kapsel/internal/diskspace"
	"kapsel/internal/jobs"
	"kapsel/internal/playlistimport"
	"kapsel/internal/previews"
	"kapsel/internal/sandbox"
)

const (
	defaultYTDLPPath              = "yt-dlp"
	JobType                       = "download"
	ChannelJobType                = "channel_first_download"
	ChannelScanJobType            = "channel_scan"
	ChannelAutoDownloadJobType    = "channel_auto_download"
	RetentionJobType              = "retention_cleanup"
	YTDLPUpdateJobType            = "ytdlp_update"
	VideoMetadataScanJobType      = "video_metadata_scan"
	PlaylistImportJobType         = "playlist_import"
	DownloadOriginManual          = "manual"
	DownloadOriginChannelAuto     = "channel_auto"
	MediaOriginImported           = "imported"
	DefaultChannelCatalogLimit    = 500
	DefaultChannelCatalogPageSize = 30
	maxChannelCatalogOutputBytes  = 4 * 1024 * 1024
	maxCatalogThumbnailURLLength  = 2048
	maxErrorOutput                = 64 * 1024
	maxInFlightDownloadProgress   = 0.85
)

var (
	ErrDownloadURLRequired    = errors.New("download URL is required")
	ErrUnsupportedURLScheme   = errors.New("download URL must use http or https")
	ErrUnsupportedChannelURL  = errors.New("channel URL must be a supported YouTube channel URL")
	ErrUnsupportedVideoURL    = errors.New("download URL must be a supported single video URL")
	ErrUnsupportedPlaylistURL = errors.New("playlist URL must be a YouTube playlist link with a list id")
)

type Config struct {
	YTDLPPath          string
	YTDLPSleepInterval time.Duration
	DataRoot           string
	MediaRoot          string
	FormatSelector     string
	YTDLPCookiesFile   string
	MinFreeSpaceBytes  uint64
	Stat               diskspace.StatFunc
	PreviewsEnabled    bool
	SubtitlesEnabled   bool
	FFMPEGPath         string
	PreviewRunner      previews.Runner
	JobStore           *jobs.Store
	// RetentionWatchedCleanupDisabled opts out of watched-media retention
	// (KAPSEL_RETENTION_WATCHED_AFTER=0s).
	RetentionWatchedCleanupDisabled bool
	// RetentionIncludeManual opts manually downloaded media into the stale
	// retention rule (KAPSEL_RETENTION_INCLUDE_MANUAL).
	RetentionIncludeManual bool
	ytdlpSleep             func(context.Context, time.Duration) error
	ytdlpSleepJitter       func(time.Duration) time.Duration
	ytdlpNow               func() time.Time
}

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (r ExecRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	stdout := limitedBuffer{max: command.MaxStdoutBytes}
	stderrMax := maxErrorOutput
	if command.MaxStdoutBytes <= 0 {
		stderrMax = 0
	}
	stderr := limitedBuffer{max: stderrMax}
	progress := progressWriter{buffer: &stderr, progress: command.Progress}
	stdoutProgress := stdoutProgressWriter{buffer: &stdout, progress: &progress}
	backend := r.Backend
	if backend == nil {
		backend = sandbox.BasicBackend{}
	}
	kind := command.Kind
	if kind == "" {
		kind = sandbox.KindYTDLP
	}
	err := backend.Run(ctx, sandbox.Spec{
		Name:    command.Name,
		Args:    command.Args,
		Kind:    kind,
		Dir:     command.Dir,
		Access:  command.Access,
		Network: command.Network,
	}, sandbox.IO{Stdout: &stdoutProgress, Stderr: &progress})
	stdoutProgress.Flush()
	progress.Flush()
	if err != nil {
		stdoutBytes := stdout.CappedBytes(maxErrorOutput)
		if command.MaxStdoutBytes <= 0 {
			stdoutBytes = stdout.Bytes()
		}
		return combinedOutput(stdoutBytes, stderr.Bytes()), err
	}
	if progress.err != nil {
		return combinedOutput(stdout.CappedBytes(maxErrorOutput), stderr.Bytes()), progress.err
	}

	return stdout.Bytes(), nil
}

func (w *stdoutProgressWriter) Write(p []byte) (int, error) {
	accepted := len(p)
	text := strings.ReplaceAll(string(p), "\r", "\n")
	parts := strings.Split(w.pending+text, "\n")
	w.pending = parts[len(parts)-1]
	for _, line := range parts[:len(parts)-1] {
		w.recordLine(line, true)
	}

	return accepted, nil
}

func (w *stdoutProgressWriter) Flush() {
	if w.pending == "" {
		return
	}
	w.recordLine(w.pending, false)
	w.pending = ""
}

func (w *stdoutProgressWriter) recordLine(line string, trailingNewline bool) {
	if _, ok := parseYTDLPDownloadProgress(line); ok {
		if w.progress != nil {
			w.progress.recordLine(line)
		}
		return
	}
	if isYTDLPStdoutStatusLine(line) {
		return
	}
	if w.buffer == nil {
		return
	}

	_, _ = w.buffer.Write([]byte(line))
	if trailingNewline {
		_, _ = w.buffer.Write([]byte("\n"))
	}
}

func (w *progressWriter) Write(p []byte) (int, error) {
	accepted := len(p)
	if w.buffer != nil {
		_, _ = w.buffer.Write(p)
	}
	if w.progress == nil || w.err != nil {
		return accepted, nil
	}

	text := strings.ReplaceAll(string(p), "\r", "\n")
	parts := strings.Split(w.pending+text, "\n")
	w.pending = parts[len(parts)-1]
	for _, line := range parts[:len(parts)-1] {
		w.recordLine(line)
	}

	return accepted, nil
}

func (w *progressWriter) Flush() {
	if w.pending == "" || w.progress == nil || w.err != nil {
		w.pending = ""
		return
	}
	w.recordLine(w.pending)
	w.pending = ""
}

func (w *progressWriter) recordLine(line string) {
	if w.progress == nil || w.err != nil {
		return
	}
	progress, ok := parseYTDLPDownloadProgress(line)
	if progress > maxInFlightDownloadProgress {
		progress = maxInFlightDownloadProgress
	}
	if !ok || progress < w.lastProgress+0.005 {
		return
	}
	if err := w.progress(progress); err != nil {
		w.err = err
		return
	}
	w.lastProgress = progress
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 {
		return b.Buffer.Write(p)
	}

	accepted := len(p)
	remaining := b.max - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}

	return accepted, nil
}

func (b *limitedBuffer) CappedBytes(max int) []byte {
	if max <= 0 || b.Len() <= max {
		return b.Bytes()
	}

	return b.Bytes()[:max]
}

type Downloader struct {
	db                 *sql.DB
	store              *jobs.Store
	config             Config
	runner             Runner
	ytdlpMu            sync.Mutex
	lastYTDLPStartedAt time.Time
}

func NewDownloader(db *sql.DB, config Config, runner Runner) *Downloader {
	if config.YTDLPPath == "" {
		config.YTDLPPath = defaultYTDLPPath
	}
	if config.FormatSelector == "" {
		config.FormatSelector = DefaultFormatSelector
	}
	if config.FFMPEGPath == "" {
		config.FFMPEGPath = previews.DefaultFFMPEGPath
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	return &Downloader{db: db, store: config.JobStore, config: config, runner: runner}
}

func (e ytdlpRetryError) Error() string {
	return e.err.Error()
}

func (e ytdlpRetryError) Unwrap() error {
	return e.err
}

func (e ytdlpRetryError) RetryDelay() time.Duration {
	return e.delay
}

func (d *Downloader) HandleVideoMetadataScan(ctx context.Context, job jobs.Job) error {
	if d.db == nil {
		return errors.New("video metadata scan handler missing database")
	}
	if err := d.requireJobStoreForJob(job); err != nil {
		return err
	}

	var payload Payload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return err
	}
	if err := d.ensureDiskSpace(); err != nil {
		return err
	}
	command, err := d.BuildVideoMetadataScanCommand(payload.URL)
	if err != nil {
		return err
	}
	output, err := d.runYTDLP(ctx, command)
	if err != nil {
		return ytdlpJobError(command, output, err)
	}
	value, err := parseDownloadMetadataOutput(output, false)
	if err != nil {
		return err
	}
	video := catalogVideoFromMetadata(value)
	if video.ID == "" {
		return errors.New("video metadata scan returned no usable video id")
	}

	if video.ChannelID != "" {
		if err := d.upsertChannel(ctx, d.db, video.ChannelID, video.ChannelName, "", ""); err != nil {
			return err
		}
	}
	if err := d.upsertCatalogVideo(ctx, d.db, video, false); err != nil {
		return err
	}

	return d.setJobResult(ctx, job.ID, videoMetadataScanResult{
		VideoID:     video.ID,
		Title:       video.Title,
		ChannelID:   video.ChannelID,
		ChannelName: video.ChannelName,
	})
}

// HandlePlaylistImport runs a playlist_import job: it fetches a YouTube
// playlist's flat entry list and imports it in one pass — upsert the playlist
// under its deterministic yt-<listID> id, hydrate catalog rows for every entry
// from the flat dump (title, duration, channel, thumbnail), and link them all
// into the playlist immediately. Catalog positions are preserved so importing a
// playlist never reorders an existing channel catalog, and no metadata scans
// are enqueued: the flat dump already carries the browsable metadata, so the
// playlist is complete on first import.
func (d *Downloader) HandlePlaylistImport(ctx context.Context, job jobs.Job) error {
	if d.db == nil {
		return errors.New("playlist import handler missing database")
	}

	var payload PlaylistImportPayload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return err
	}
	playlistURL, listID, err := NormalizePlaylistURL(payload.URL)
	if err != nil {
		return err
	}
	if err := d.ensureDiskSpace(); err != nil {
		return err
	}
	command, err := d.BuildPlaylistImportCommand(playlistURL)
	if err != nil {
		return err
	}
	output, err := d.runYTDLP(ctx, command)
	if err != nil {
		return ytdlpJobError(command, output, err)
	}
	metadata, err := parsePlaylistImportOutput(output)
	if err != nil {
		return err
	}
	playlistID := "yt-" + listID
	title := strings.TrimSpace(firstNonEmpty(metadata.Title, listID))
	if !isSafeMetadataValue(title) {
		return errors.New("playlist title contains unsafe text")
	}
	description := strings.TrimSpace(metadata.Description)
	if !isSafeMetadataText(description) {
		return errors.New("playlist description contains unsafe text")
	}
	channelID := firstNonEmpty(metadata.ChannelID, metadata.UploaderID)
	channelName := firstNonEmpty(metadata.Channel, metadata.Uploader, channelID)

	// Flat entries map to catalog videos with the same shape channel scans use;
	// dedupe happens inside catalogVideosFromEntries. New rows are not members
	// of any channel catalog yet, so they get catalog_position -1.
	videos := catalogVideosFromEntries(metadata.Entries, channelID, channelName, false)
	for index := range videos {
		videos[index].CatalogPosition = -1
	}
	if len(videos) == 0 {
		return errors.New("playlist contains no videos")
	}
	// Skipped counts only entries that could not be mapped to a video row
	// (no usable id); collapsed duplicates are linked, not skipped.
	linkedByID := map[string]struct{}{}
	for _, video := range videos {
		linkedByID[video.ID] = struct{}{}
	}
	skipped := 0
	for _, entry := range metadata.Entries {
		if _, ok := linkedByID[channelEntryVideoID(entry)]; !ok {
			skipped++
		}
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Create/refresh the uploader channel so the playlist and its videos can
	// reference it (the channel thumbnail is fetched by the channel's own scan;
	// the playlist thumbnail is not the channel's).
	if channelID != "" {
		if err := d.upsertChannel(ctx, tx, channelID, channelName, description, ""); err != nil {
			return err
		}
	}
	if _, err := playlistimport.UpsertPlaylist(ctx, tx, playlistimport.PlaylistIdentity{
		ID:          playlistID,
		ExternalID:  listID,
		Title:       title,
		Description: description,
		ChannelID:   channelID,
	}); err != nil {
		return err
	}
	for _, video := range videos {
		if err := d.writeCatalogVideo(ctx, tx, video, true); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM playlist_entries WHERE playlist_id = ?", playlistID); err != nil {
		return err
	}
	for position, video := range videos {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO playlist_entries (playlist_id, video_id, position)
VALUES (?, ?, ?)`, playlistID, video.ID, position); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	return d.setJobResult(ctx, job.ID, playlistImportResult{
		PlaylistID: playlistID,
		Title:      title,
		Linked:     len(videos),
		Skipped:    skipped,
	})
}

// parsePlaylistImportOutput parses a yt-dlp flat playlist dump
// (--flat-playlist --dump-single-json).
func parsePlaylistImportOutput(output []byte) (channelMetadata, error) {
	var metadata channelMetadata
	if err := json.Unmarshal(output, &metadata); err != nil {
		return channelMetadata{}, err
	}

	return metadata, nil
}

// playlistEnqueuer adapts the job store to playlistimport.Enqueuer so the CSV,
// CLI, and URL-import paths share one linking implementation.
type playlistEnqueuer struct {
	store *jobs.Store
}

// NewPlaylistImportEnqueuer returns a playlistimport.Enqueuer backed by the
// download helpers (metadata scans for missing videos, full downloads in
// ModeDownload).
func NewPlaylistImportEnqueuer(store *jobs.Store) playlistimport.Enqueuer {
	return playlistEnqueuer{store: store}
}

func (e playlistEnqueuer) EnqueuePlaylistVideo(ctx context.Context, videoID string, mode playlistimport.Mode) error {
	if e.store == nil {
		return errors.New("playlist import enqueue missing job store")
	}
	payload := Payload{URL: "https://www.youtube.com/watch?v=" + videoID}
	if mode == playlistimport.ModeDownload {
		_, err := EnqueueDownload(ctx, e.store, payload)
		return err
	}
	_, err := EnqueueVideoMetadataScan(ctx, e.store, payload)

	return err
}

func (d *Downloader) ensureDiskSpace() error {
	if d.config.MinFreeSpaceBytes == 0 {
		return nil
	}

	return diskspace.NewChecker(d.config.MinFreeSpaceBytes, d.config.Stat).Ensure(d.config.DataRoot, d.config.MediaRoot)
}

func NormalizeURL(rawURL string) (string, error) {
	value := strings.TrimSpace(rawURL)
	if value == "" {
		return "", ErrDownloadURLRequired
	}

	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", ErrUnsupportedURLScheme
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", ErrUnsupportedURLScheme
	}

	return parsed.String(), nil
}

func NormalizeDirectVideoURL(rawURL string) (string, error) {
	downloadURL, err := NormalizeURL(rawURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(downloadURL)
	if err != nil {
		return "", ErrUnsupportedVideoURL
	}
	if isYouTubeHost(parsed.Hostname()) || strings.EqualFold(parsed.Hostname(), "youtu.be") {
		videoURL, err := normalizeDirectYouTubeVideoURL(parsed)
		if err != nil {
			return "", ErrUnsupportedVideoURL
		}

		return videoURL, nil
	}

	return "", ErrUnsupportedVideoURL
}

func NormalizeDownloadURL(rawURL string) (string, error) {
	return NormalizeDirectVideoURL(rawURL)
}

type channelMetadata struct {
	ID          string              `json:"id"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Thumbnail   string              `json:"thumbnail"`
	Thumbnails  []thumbnailMetadata `json:"thumbnails"`
	ChannelID   string              `json:"channel_id"`
	Channel     string              `json:"channel"`
	UploaderID  string              `json:"uploader_id"`
	Uploader    string              `json:"uploader"`
	Entries     []channelEntry      `json:"entries"`
}

type channelEntry struct {
	ID          string              `json:"id"`
	URL         string              `json:"url"`
	WebpageURL  string              `json:"webpage_url"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Duration    float64             `json:"duration"`
	ViewCount   *int                `json:"view_count"`
	UploadDate  string              `json:"upload_date"`
	Timestamp   int64               `json:"timestamp"`
	Thumbnail   string              `json:"thumbnail"`
	Thumbnails  []thumbnailMetadata `json:"thumbnails"`
	ChannelID   string              `json:"channel_id"`
	Channel     string              `json:"channel"`
	UploaderID  string              `json:"uploader_id"`
	Uploader    string              `json:"uploader"`
	Entries     []channelEntry      `json:"entries"`
}

type catalogVideo struct {
	ID              string
	Title           string
	Description     string
	PublishedAt     string
	CatalogPosition int
	DurationSeconds int
	ViewCount       int
	HasViewCount    bool
	ThumbnailURL    string
	ChannelID       string
	ChannelName     string
}

func (d *Downloader) videoSearchText(ctx context.Context, exec sqlExecutor, id string) (string, string, error) {
	var title string
	var description string
	if err := exec.QueryRowContext(ctx, "SELECT title, description FROM videos WHERE id = ?", id).Scan(&title, &description); err != nil {
		return "", "", err
	}

	return title, description, nil
}

func succeededAutoDownloadExists(ctx context.Context, exec sqlExecutor, videoID string) (bool, error) {
	var exists bool
	if err := exec.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1
  FROM downloads
  WHERE video_id = ?
    AND status = 'succeeded'
    AND origin = ?
)`, videoID, DownloadOriginChannelAuto).Scan(&exists); err != nil {
		return false, err
	}

	return exists, nil
}

// catalogVideoFromMetadata builds a catalog video row from a single-video
// metadata dump (the shape produced by --dump-single-json). It mirrors
// catalogVideoFromEntry for the playlist metadata-scan path, where a whole
// video object is available instead of a flat playlist entry.
func catalogVideoFromMetadata(value metadata) catalogVideo {
	videoID := strings.TrimSpace(value.ID)
	if !isLikelyYouTubeVideoID(videoID) {
		if normalized, err := NormalizeYouTubeVideoURL(value.WebpageURL); err == nil {
			videoID = videoIDFromWatchURL(normalized)
		}
	}
	if !isLikelyYouTubeVideoID(videoID) {
		return catalogVideo{}
	}
	title := strings.TrimSpace(firstNonEmpty(value.Title, videoID))
	description := strings.TrimSpace(value.Description)
	if !isSafeMetadataValue(title) || !isSafeMetadataText(description) {
		return catalogVideo{}
	}
	channelID := firstNonEmpty(value.ChannelID, value.UploaderID)
	channelName := firstNonEmpty(value.Channel, value.Uploader, channelID)

	viewCount, hasViewCount := viewCountValue(value.ViewCount)

	return catalogVideo{
		ID:              videoID,
		Title:           title,
		Description:     description,
		PublishedAt:     catalogUploadDate(value.UploadDate),
		DurationSeconds: durationSeconds(value.Duration),
		ViewCount:       viewCount,
		HasViewCount:    hasViewCount,
		ThumbnailURL:    catalogThumbnailURL(value.Thumbnail, value.Thumbnails),
		ChannelID:       channelID,
		ChannelName:     channelName,
	}
}

func (d *Downloader) validatedSubtitles(metadata metadata) ([]validatedSubtitle, error) {
	if len(metadata.RequestedSubtitles) == 0 {
		return nil, nil
	}
	languages := make([]string, 0, len(metadata.RequestedSubtitles))
	for language := range metadata.RequestedSubtitles {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	subtitles := make([]validatedSubtitle, 0, len(languages))
	for _, key := range languages {
		subtitle := metadata.RequestedSubtitles[key]
		path, err := d.validatedAssetPath("subtitle", firstNonEmpty(subtitle.Filepath, subtitle.Path), false)
		if err != nil {
			return nil, err
		}
		if path == "" {
			continue
		}
		language := cleanSubtitleLanguage(firstNonEmpty(subtitle.Language, key))
		if language == "" {
			continue
		}
		format := cleanSubtitleFormat(firstNonEmpty(subtitle.Ext, filepath.Ext(path)))
		if format == "" {
			continue
		}
		text, err := d.readSubtitleText(path)
		if err != nil {
			return nil, err
		}
		subtitles = append(subtitles, validatedSubtitle{Language: language, Name: subtitle.Name, Source: "downloaded", Format: format, Path: path, Text: text})
	}

	return subtitles, nil
}
