package download

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"kapsel/internal/assetpath"
	"kapsel/internal/denorm"
	"kapsel/internal/previews"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var thumbnailFileExtensions = []string{".jpg", ".jpeg", ".webp", ".png", ".avif"}

func (d *Downloader) handlePayload(ctx context.Context, jobID string, payloadJSON string) (ingestResult, error) {
	if d.db == nil {
		return ingestResult{}, errors.New("download handler missing database")
	}

	var payload Payload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return ingestResult{}, err
	}
	downloadURL, err := NormalizeDownloadURL(payload.URL)
	if err != nil {
		return ingestResult{}, err
	}
	if err := d.ensureDiskSpace(); err != nil {
		return ingestResult{}, err
	}

	command, err := d.BuildCommand(downloadURL)
	if err != nil {
		return ingestResult{}, err
	}
	if jobID != "" {
		store, err := d.jobStore()
		if err != nil {
			return ingestResult{}, err
		}
		command.Progress = func(progress float64) error {
			// Download progress is best-effort UI state; lease renewal is runner-owned.
			_ = store.ReportProgress(ctx, jobID, progress)
			return nil
		}
	}
	output, runErr := d.runYTDLP(ctx, command)
	if runErr != nil && isMembersOnlyYTDLPFailure(output, runErr) {
		videoID := videoIDFromWatchURL(downloadURL)
		if videoID != "" {
			if err := d.markVideoMembersOnly(ctx, videoID); err != nil {
				return ingestResult{}, ytdlpJobError(command, output, runErr)
			}
		}
		return ingestResult{Action: "members_only_skipped"}, nil
	}
	downloadedMetadata, err := parseDownloadMetadataOutput(output, runErr != nil)
	if err != nil {
		if runErr != nil {
			return ingestResult{}, ytdlpJobError(command, output, runErr)
		}
		return ingestResult{}, err
	}
	if runErr != nil {
		if !isRecoverableYTDLPDownloadExit(runErr, downloadedMetadata) {
			return ingestResult{}, ytdlpJobError(command, output, runErr)
		}
		if _, err := d.validateMetadata(downloadedMetadata); err != nil {
			return ingestResult{}, fmt.Errorf("%w; downloaded media validation failed: %v", ytdlpJobError(command, output, runErr), err)
		}
		slog.Warn("yt-dlp exited after producing downloaded media metadata; continuing ingest", "video_id", downloadedMetadata.ID, "job_id", jobID, "error", SanitizeDiagnosticText(ytdlpCommandError(command, output, runErr).Error()))
	}
	if d.config.SubtitlesEnabled && hasOriginalAutomaticSubtitles(downloadedMetadata) {
		command, err := d.BuildOriginalAutomaticSubtitleCommand(downloadURL)
		if err != nil {
			return ingestResult{}, err
		}
		output, err := d.runYTDLP(ctx, command)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return ingestResult{}, ytdlpJobError(command, output, err)
			}
			slog.Warn("original automatic subtitle download failed", "video_id", downloadedMetadata.ID, "job_id", jobID, "error", SanitizeDiagnosticText(ytdlpCommandError(command, output, err).Error()))
			return d.ingest(ctx, downloadedMetadata, downloadURL, payloadJSON, jobID, downloadOrigin(payload.Origin))
		}
		automaticMetadata, err := parseDownloadMetadataOutput(output, false)
		if err != nil {
			return ingestResult{}, err
		}
		mergeRequestedSubtitles(&downloadedMetadata, automaticMetadata.RequestedSubtitles)
	}

	return d.ingest(ctx, downloadedMetadata, downloadURL, payloadJSON, jobID, downloadOrigin(payload.Origin))
}

type metadata struct {
	ID                 string                        `json:"id"`
	Title              string                        `json:"title"`
	Description        string                        `json:"description"`
	Duration           float64                       `json:"duration"`
	ViewCount          *int                          `json:"view_count"`
	UploadDate         string                        `json:"upload_date"`
	Thumbnail          string                        `json:"thumbnail"`
	Thumbnails         []thumbnailMetadata           `json:"thumbnails"`
	ChannelID          string                        `json:"channel_id"`
	Channel            string                        `json:"channel"`
	UploaderID         string                        `json:"uploader_id"`
	Uploader           string                        `json:"uploader"`
	WebpageURL         string                        `json:"webpage_url"`
	Filepath           string                        `json:"filepath"`
	ThumbnailPath      string                        `json:"thumbnail_path"`
	ThumbnailFilepath  string                        `json:"__thumbnail_filepath"`
	RequestedSubtitles map[string]subtitleMetadata   `json:"requested_subtitles"`
	AutomaticCaptions  map[string][]subtitleMetadata `json:"automatic_captions"`
	RequestedDownloads []struct {
		Filepath string `json:"filepath"`
	} `json:"requested_downloads"`
}

type subtitleMetadata struct {
	Filepath string `json:"filepath"`
	Path     string `json:"path"`
	Ext      string `json:"ext"`
	Name     string `json:"name"`
	Language string `json:"language"`
}

type thumbnailMetadata struct {
	URL string `json:"url"`
}

type validatedMetadata struct {
	metadata      metadata
	mediaPath     string
	thumbnailPath string
	subtitles     []validatedSubtitle
}

type validatedSubtitle struct {
	Language string
	Name     string
	Source   string
	Format   string
	Path     string
	Text     string
}

type ingestResult struct {
	VideoID string `json:"video_id"`
	Action  string `json:"action"`
}

func hasOriginalAutomaticSubtitles(value metadata) bool {
	for language := range value.AutomaticCaptions {
		if strings.HasSuffix(language, "-orig") {
			return true
		}
	}

	return false
}

func mergeRequestedSubtitles(target *metadata, subtitles map[string]subtitleMetadata) {
	if len(subtitles) == 0 {
		return
	}
	if target.RequestedSubtitles == nil {
		target.RequestedSubtitles = map[string]subtitleMetadata{}
	}
	for language, subtitle := range subtitles {
		target.RequestedSubtitles[language] = subtitle
	}
}

func (d *Downloader) ingest(ctx context.Context, metadata metadata, rawURL string, payloadJSON string, jobID string, origin string) (ingestResult, error) {
	validated, err := d.validateMetadata(metadata)
	if err != nil {
		return ingestResult{}, err
	}
	metadata = validated.metadata
	mediaPath := validated.mediaPath
	thumbnailPath := validated.thumbnailPath
	subtitles := validated.subtitles
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return ingestResult{}, err
	}
	defer tx.Rollback()

	videoID, err := d.canonicalVideoID(ctx, tx, metadata.ID)
	if err != nil {
		return ingestResult{}, err
	}
	exists, err := d.videoExists(ctx, tx, videoID)
	if err != nil {
		return ingestResult{}, err
	}
	result := ingestResult{VideoID: videoID, Action: "created"}
	if exists {
		result.Action = "updated"
	}

	channelID := firstNonEmpty(metadata.ChannelID, metadata.UploaderID)
	channelName := firstNonEmpty(metadata.Channel, metadata.Uploader, channelID)
	if channelID != "" {
		if err := d.upsertChannel(ctx, tx, channelID, channelName, "", ""); err != nil {
			return ingestResult{}, err
		}
	}

	if err := d.upsertVideo(ctx, tx, videoID, metadata, channelID, mediaPath, thumbnailPath, origin); err != nil {
		return ingestResult{}, err
	}
	actualVideoID, err := d.canonicalVideoID(ctx, tx, metadata.ID)
	if err != nil {
		return ingestResult{}, err
	}
	if actualVideoID != videoID {
		videoID = actualVideoID
		result.VideoID = actualVideoID
		result.Action = "updated"
	}

	if err := d.upsertAsset(ctx, tx, "video", videoID, "media", mediaPath); err != nil {
		return ingestResult{}, err
	}
	if err := d.syncOptionalAsset(ctx, tx, "video", videoID, "thumbnail", thumbnailPath); err != nil {
		return ingestResult{}, err
	}
	if err := d.replaceDownloadedSubtitles(ctx, tx, videoID, subtitles); err != nil {
		return ingestResult{}, err
	}
	if err := d.upsertSearch(ctx, tx, "video", videoID, "title", firstNonEmpty(metadata.Title, metadata.ID)); err != nil {
		return ingestResult{}, err
	}
	if err := d.upsertSearch(ctx, tx, "video", videoID, "description", metadata.Description); err != nil {
		return ingestResult{}, err
	}
	if err := denorm.SyncVideoChannelSearchDocument(ctx, tx, videoID, channelName, firstNonEmpty(metadata.Title, metadata.ID)); err != nil {
		return ingestResult{}, err
	}
	if err := d.upsertDownload(ctx, tx, videoID, metadata, rawURL, payloadJSON, origin); err != nil {
		return ingestResult{}, err
	}
	if d.config.PreviewsEnabled {
		if err := d.enqueuePreviewJob(ctx, tx, videoID); err != nil {
			return ingestResult{}, err
		}
	}
	if err := d.setJobResultTx(ctx, tx, jobID, result); err != nil {
		return ingestResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ingestResult{}, err
	}

	return result, nil
}

func (d *Downloader) enqueuePreviewJob(ctx context.Context, tx *sql.Tx, videoID string) error {
	// Preview generation is best-effort; do not repeatedly restart ffmpeg after a resource-related failure.
	store, err := d.jobStore()
	if err != nil {
		return err
	}
	_, _, err = previews.EnqueueJobTx(ctx, store, tx, videoID)

	return err
}

func (d *Downloader) canonicalVideoID(ctx context.Context, exec sqlExecutor, externalID string) (string, error) {
	var id string
	err := exec.QueryRowContext(ctx, "SELECT id FROM videos WHERE source = 'youtube' AND external_id = ?", externalID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return externalID, nil
	}
	if err != nil {
		return "", err
	}

	return id, nil
}

func (d *Downloader) validateMetadata(metadata metadata) (validatedMetadata, error) {
	metadata.ID = strings.TrimSpace(metadata.ID)
	metadata.Title = strings.TrimSpace(metadata.Title)
	if metadata.ID == "" {
		return validatedMetadata{}, errors.New("download metadata missing video id")
	}
	if !isSafeMetadataValue(metadata.ID) || strings.ContainsAny(metadata.ID, `/\`) {
		return validatedMetadata{}, errors.New("download metadata invalid video id")
	}
	if metadata.Title == "" {
		return validatedMetadata{}, errors.New("download metadata missing title")
	}
	if !isSafeMetadataValue(metadata.Title) {
		return validatedMetadata{}, errors.New("download metadata invalid title")
	}

	mediaPath, err := d.validatedAssetPath("media", metadata.mediaPath(), true)
	if err != nil {
		return validatedMetadata{}, err
	}
	thumbnailPath, err := d.validatedThumbnailPath(metadata)
	if err != nil {
		return validatedMetadata{}, err
	}
	subtitles, err := d.validatedSubtitles(metadata)
	if err != nil {
		return validatedMetadata{}, err
	}

	return validatedMetadata{metadata: metadata, mediaPath: mediaPath, thumbnailPath: thumbnailPath, subtitles: subtitles}, nil
}

func (d *Downloader) validatedThumbnailPath(metadata metadata) (string, error) {
	rawThumbnailPath := firstNonEmpty(metadata.ThumbnailPath, metadata.ThumbnailFilepath)
	thumbnailPath, err := d.validatedAssetPath("thumbnail", rawThumbnailPath, false)
	if err != nil || thumbnailPath != "" {
		return thumbnailPath, err
	}
	if strings.TrimSpace(rawThumbnailPath) != "" {
		return "", nil
	}

	return d.discoverDownloadedThumbnail(metadata.ID)
}

func (d *Downloader) discoverDownloadedThumbnail(videoID string) (string, error) {
	for _, extension := range thumbnailFileExtensions {
		thumbnailPath, err := d.validatedAssetPath("thumbnail", videoID+extension, false)
		if err != nil || thumbnailPath != "" {
			return thumbnailPath, err
		}
	}

	return "", nil
}

func (d *Downloader) videoExists(ctx context.Context, exec sqlExecutor, id string) (bool, error) {
	var exists bool
	if err := exec.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM videos WHERE id = ?)", id).Scan(&exists); err != nil {
		return false, err
	}

	return exists, nil
}

func (d *Downloader) upsertDownload(ctx context.Context, exec sqlExecutor, videoID string, metadata metadata, rawURL string, payloadJSON string, origin string) error {
	downloadURL := firstNonEmpty(metadata.WebpageURL, rawURL)
	_, err := exec.ExecContext(ctx, `
INSERT INTO downloads (video_id, source, external_id, url, status, origin, error, payload_json, updated_at)
VALUES (?, 'youtube', ?, ?, 'succeeded', ?, '', ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
ON CONFLICT(source, external_id) WHERE external_id <> '' DO UPDATE SET
  video_id = excluded.video_id,
  url = excluded.url,
  status = excluded.status,
	origin = CASE WHEN downloads.origin = 'manual' OR excluded.origin = 'manual' THEN 'manual' ELSE excluded.origin END,
  error = '',
  payload_json = excluded.payload_json,
	updated_at = excluded.updated_at`, videoID, metadata.ID, downloadURL, downloadOrigin(origin), payloadJSON)

	return err
}

func (d *Downloader) replaceDownloadedSubtitles(ctx context.Context, exec sqlExecutor, videoID string, subtitles []validatedSubtitle) error {
	if _, err := exec.ExecContext(ctx, "DELETE FROM subtitles WHERE video_id = ? AND source = 'downloaded'", videoID); err != nil {
		return err
	}
	if _, err := exec.ExecContext(ctx, "DELETE FROM search_documents WHERE owner_type = 'subtitle' AND owner_id = ? AND field LIKE 'text:%:downloaded'", videoID); err != nil {
		return err
	}
	for _, subtitle := range subtitles {
		if err := d.upsertSubtitle(ctx, exec, videoID, subtitle); err != nil {
			return err
		}
	}

	return nil
}

func (d *Downloader) upsertSubtitle(ctx context.Context, exec sqlExecutor, videoID string, subtitle validatedSubtitle) error {
	_, err := exec.ExecContext(ctx, `
INSERT INTO subtitles (video_id, language, name, source, format, path, text)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(video_id, language, source) DO UPDATE SET
  name = excluded.name,
  format = excluded.format,
  path = excluded.path,
  text = excluded.text`, videoID, subtitle.Language, subtitle.Name, subtitle.Source, subtitle.Format, subtitle.Path, subtitle.Text)
	if err != nil {
		return err
	}

	return d.upsertSearch(ctx, exec, "subtitle", videoID, subtitleSearchField(subtitle), subtitle.Text)
}

func subtitleSearchField(subtitle validatedSubtitle) string {
	return "text:" + subtitle.Language + ":" + subtitle.Source
}

func (d *Downloader) readSubtitleText(path string) (string, error) {
	body, err := os.ReadFile(filepath.Join(d.config.MediaRoot, filepath.FromSlash(path)))
	if err != nil {
		return "", err
	}

	return string(body), nil
}

func cleanSubtitleLanguage(value string) string {
	value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "-"))
	if value == "" {
		return ""
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' {
			continue
		}
		return ""
	}

	return value
}

func cleanSubtitleFormat(value string) string {
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), ".")
	switch value {
	case "vtt", "srt":
		return value
	default:
		return ""
	}
}

func isSafeMetadataValue(value string) bool {
	for _, char := range value {
		if char < 0x20 {
			return false
		}
	}

	return true
}

func isSafeMetadataText(value string) bool {
	for _, char := range value {
		if char == '\n' || char == '\r' || char == '\t' {
			continue
		}
		if char < 0x20 {
			return false
		}
	}

	return true
}

func (d *Downloader) validatedAssetPath(kind string, raw string, required bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if required {
			return "", fmt.Errorf("download %s path missing", kind)
		}
		return "", nil
	}

	path, err := d.assetPath(raw)
	if err != nil {
		return "", fmt.Errorf("invalid download %s path: %w", kind, err)
	}
	exists, err := d.assetExists(path, kind)
	if err != nil {
		return "", err
	}
	if !exists {
		if required {
			return "", fmt.Errorf("download %s file missing: %s", kind, path)
		}
		return "", nil
	}

	return path, nil
}

func (d *Downloader) assetExists(path string, kind string) (bool, error) {
	info, err := d.lstatAsset(path, kind)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("download %s file check failed: %w", kind, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("download %s file is a symlink: %s", kind, path)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("download %s file is not a regular file: %s", kind, path)
	}

	return true, nil
}

func (d *Downloader) lstatAsset(path string, kind string) (os.FileInfo, error) {
	_, info, err := assetpath.Lstat(d.config.MediaRoot, path)
	if errors.Is(err, assetpath.ErrSymlink) {
		return nil, fmt.Errorf("download %s file path contains symlink: %s", kind, path)
	}
	if errors.Is(err, assetpath.ErrInvalid) {
		return nil, fmt.Errorf("download %s file path is unsafe: %s", kind, path)
	}

	return info, err
}

func (d *Downloader) syncOptionalAsset(ctx context.Context, exec sqlExecutor, ownerType string, ownerID string, kind string, path string) error {
	return denorm.SyncMediaAsset(ctx, exec, ownerType, ownerID, kind, path)
}

func (d *Downloader) assetPath(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}

	return assetpath.FromMediaRoot(d.config.MediaRoot, raw)
}

func (d *Downloader) upsertVideo(ctx context.Context, exec sqlExecutor, videoID string, metadata metadata, channelID string, mediaPath string, thumbnailPath string, origin string) error {
	viewCount, hasViewCount := viewCountValue(metadata.ViewCount)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	mediaOrigin := downloadOrigin(origin)
	_, err := exec.ExecContext(ctx, `
INSERT INTO videos (
	  id, external_id, channel_id, title, description, published_at, duration_seconds, view_count, media_path, thumbnail_path, thumbnail_url, media_origin, media_downloaded_at, archived_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	  channel_id = excluded.channel_id,
	  title = excluded.title,
	  description = excluded.description,
	  published_at = excluded.published_at,
	  duration_seconds = excluded.duration_seconds,
	  view_count = CASE WHEN ? THEN excluded.view_count ELSE videos.view_count END,
	  media_path = excluded.media_path,
	  thumbnail_path = excluded.thumbnail_path,
	  thumbnail_url = excluded.thumbnail_url,
	  media_origin = CASE WHEN videos.media_origin = 'manual' OR excluded.media_origin = 'manual' THEN 'manual' ELSE excluded.media_origin END,
	  media_downloaded_at = excluded.media_downloaded_at,
	  archived_at = CASE WHEN videos.archived_at IS NOT NULL AND videos.archived_at <> '' THEN videos.archived_at ELSE excluded.archived_at END,
	  updated_at = excluded.updated_at
ON CONFLICT(source, external_id) DO UPDATE SET
	  channel_id = excluded.channel_id,
	  title = excluded.title,
	  description = excluded.description,
	  published_at = excluded.published_at,
	  duration_seconds = excluded.duration_seconds,
	  view_count = CASE WHEN ? THEN excluded.view_count ELSE videos.view_count END,
	  media_path = excluded.media_path,
	  thumbnail_path = excluded.thumbnail_path,
	  thumbnail_url = excluded.thumbnail_url,
	  media_origin = CASE WHEN videos.media_origin = 'manual' OR excluded.media_origin = 'manual' THEN 'manual' ELSE excluded.media_origin END,
	  media_downloaded_at = excluded.media_downloaded_at,
	  archived_at = CASE WHEN videos.archived_at IS NOT NULL AND videos.archived_at <> '' THEN videos.archived_at ELSE excluded.archived_at END,
	  updated_at = excluded.updated_at`,
		videoID,
		metadata.ID,
		nullEmpty(channelID),
		firstNonEmpty(metadata.Title, metadata.ID),
		metadata.Description,
		dateFromUploadDate(metadata.UploadDate),
		durationSeconds(metadata.Duration),
		viewCount,
		mediaPath,
		thumbnailPath,
		catalogThumbnailURL(metadata.Thumbnail, metadata.Thumbnails),
		mediaOrigin,
		now,
		now,
		now,
		hasViewCount,
		hasViewCount,
	)

	return err
}

func (d *Downloader) upsertAsset(ctx context.Context, exec sqlExecutor, ownerType string, ownerID string, kind string, path string) error {
	return denorm.SyncMediaAsset(ctx, exec, ownerType, ownerID, kind, path)
}

func (d *Downloader) upsertSearch(ctx context.Context, exec sqlExecutor, ownerType string, ownerID string, field string, text string) error {
	return denorm.SyncSearchDocument(ctx, exec, ownerType, ownerID, field, text)
}

func (m metadata) mediaPath() string {
	if len(m.RequestedDownloads) > 0 && m.RequestedDownloads[0].Filepath != "" {
		return m.RequestedDownloads[0].Filepath
	}

	return m.Filepath
}

func dateFromUploadDate(value string) any {
	if len(value) != 8 {
		return nullEmpty(value)
	}

	return value[0:4] + "-" + value[4:6] + "-" + value[6:8]
}

func durationSeconds(value float64) int {
	if value <= 0 {
		return 0
	}

	return int(value)
}

func viewCountValue(value *int) (int, bool) {
	if value == nil || *value < 0 {
		return 0, false
	}

	return *value, true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}

func nullEmpty(value string) any {
	if value == "" {
		return nil
	}

	return value
}
