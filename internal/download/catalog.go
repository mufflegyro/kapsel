package download

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"kapsel/internal/denorm"
	"kapsel/internal/jobs"
	"net/url"
	"strings"
	"time"
)

func (d *Downloader) HandleChannelFirst(ctx context.Context, job jobs.Job) error {
	if d.db == nil {
		return errors.New("download handler missing database")
	}
	if err := d.requireJobStoreForJob(job); err != nil {
		return err
	}

	var payload Payload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return err
	}
	channelURL, err := NormalizeChannelURL(payload.URL)
	if err != nil {
		return err
	}
	if err := d.ensureDiskSpace(); err != nil {
		return err
	}
	command, err := d.BuildChannelFirstCommand(channelURL)
	if err != nil {
		return err
	}
	output, err := d.runYTDLP(ctx, command)
	if err != nil {
		return ytdlpJobError(command, output, err)
	}
	catalogResult, err := d.syncChannelCatalog(ctx, output, "")
	if err != nil {
		return err
	}
	if payload.ScanOnly {
		return d.finishChannelFirstScanOnlyJob(ctx, job.ID, catalogResult)
	}
	videoURL, err := firstChannelVideoURL(output)
	if err != nil {
		return err
	}

	return d.finishChannelFirstJob(ctx, job.ID, catalogResult, videoURL)
}

func (d *Downloader) HandleChannelScan(ctx context.Context, job jobs.Job) error {
	if d.db == nil {
		return errors.New("channel scan handler missing database")
	}
	if err := d.requireJobStoreForJob(job); err != nil {
		return err
	}

	var payload ChannelScanPayload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return err
	}
	channelURL, err := NormalizeChannelURL(payload.URL)
	if err != nil {
		return err
	}
	if err := d.ensureDiskSpace(); err != nil {
		return err
	}
	command, err := d.BuildChannelFirstCommand(channelURL)
	if err != nil {
		return err
	}
	output, err := d.runYTDLP(ctx, command)
	if err != nil {
		return ytdlpJobError(command, output, err)
	}
	result, err := d.syncChannelCatalog(ctx, output, payload.ChannelID)
	if err != nil {
		return err
	}
	if result.ChannelID == "" {
		result.ChannelID = payload.ChannelID
	}

	return d.finishChannelJob(ctx, job.ID, result.ChannelID, false, result)
}

func (d *Downloader) HandleChannelAutoDownload(ctx context.Context, job jobs.Job) error {
	if d.db == nil {
		return errors.New("channel auto-download handler missing database")
	}
	if err := d.requireJobStoreForJob(job); err != nil {
		return err
	}

	var payload ChannelAutoDownloadPayload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return err
	}
	channelURL, err := NormalizeChannelURL(payload.URL)
	if err != nil {
		return err
	}
	if strings.TrimSpace(payload.ChannelID) == "" {
		return errors.New("channel auto-download payload missing channel id")
	}
	stale, err := d.channelAutoJobStale(ctx, payload.ChannelID, job.CreatedAt)
	if err != nil {
		return err
	}
	if stale {
		return d.setJobResult(ctx, job.ID, channelAutoDownloadResult{ChannelID: payload.ChannelID, Skipped: true})
	}
	if err := d.ensureDiskSpace(); err != nil {
		return err
	}

	result, err := d.syncChannelAutoDownloads(ctx, ChannelAutoDownloadPayload{URL: channelURL, ChannelID: payload.ChannelID})
	if err != nil {
		return err
	}
	if result.Incomplete || result.Skipped {
		return d.setJobResult(ctx, job.ID, result)
	}

	return d.finishChannelJob(ctx, job.ID, result.ChannelID, false, result)
}

func (d *Downloader) channelAutoJobStale(ctx context.Context, channelID string, createdAt string) (bool, error) {
	jobCreatedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(createdAt))
	if err != nil {
		return false, nil
	}
	subscribed, lastScannedAt, err := d.channelAutoSubscriptionState(ctx, channelID)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if subscribed != 1 {
		return true, nil
	}
	lastScanned, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(lastScannedAt))
	if err != nil {
		return false, nil
	}

	return lastScanned.After(jobCreatedAt), nil
}

func (d *Downloader) channelAutoSubscriptionState(ctx context.Context, channelID string) (int, string, error) {
	var subscribed int
	var lastScannedAt string
	err := d.db.QueryRowContext(ctx, "SELECT subscribed, COALESCE(last_scanned_at, '') FROM channels WHERE id = ?", channelID).Scan(&subscribed, &lastScannedAt)

	return subscribed, lastScannedAt, err
}

type channelCatalogResult struct {
	ChannelID string `json:"channel_id"`
	Videos    int    `json:"videos"`
}

type channelCatalogPageResult struct {
	ChannelID  string
	Videos     []catalogVideo
	RawEntries int
}

type channelAutoDownloadResult struct {
	ChannelID       string `json:"channel_id"`
	Videos          int    `json:"videos"`
	Pages           int    `json:"pages"`
	DownloadsQueued int    `json:"downloads_queued"`
	Skipped         bool   `json:"skipped,omitempty"`
	Incomplete      bool   `json:"incomplete,omitempty"`
}

type videoMetadataScanResult struct {
	VideoID     string `json:"video_id"`
	Title       string `json:"title,omitempty"`
	ChannelID   string `json:"channel_id,omitempty"`
	ChannelName string `json:"channel_name,omitempty"`
}

type channelFirstResult struct {
	ChannelID     string               `json:"channel_id"`
	Videos        int                  `json:"videos"`
	FirstVideoURL string               `json:"first_video_url"`
	DownloadJobID string               `json:"download_job_id"`
	Catalog       channelCatalogResult `json:"catalog"`
}

func firstChannelVideoURL(output []byte) (string, error) {
	var metadata channelMetadata
	if err := json.Unmarshal(output, &metadata); err != nil {
		return "", err
	}
	if len(metadata.Entries) == 0 {
		return "", errors.New("channel contains no videos")
	}
	return firstChannelVideoURLFromEntries(metadata.Entries)
}

func firstChannelVideoURLFromEntries(entries []channelEntry) (string, error) {
	var unsupportedErr error
	for _, entry := range entries {
		videoURL, err := firstChannelVideoURLFromEntry(entry)
		if err == nil {
			return videoURL, nil
		}
		if errors.Is(err, ErrUnsupportedChannelURL) && unsupportedErr == nil {
			unsupportedErr = err
		}
	}
	if unsupportedErr != nil {
		return "", unsupportedErr
	}

	return "", errors.New("channel first video is missing url")
}

func firstChannelVideoURLFromEntry(entry channelEntry) (string, error) {
	var unsupportedErr error
	for _, candidate := range []string{entry.WebpageURL, entry.URL} {
		if candidate == "" {
			continue
		}
		if normalized, err := NormalizeYouTubeVideoURL(candidate); err == nil {
			return normalized, nil
		} else if errors.Is(err, ErrUnsupportedChannelURL) {
			unsupportedErr = err
		}
		if isLikelyYouTubeVideoID(candidate) {
			return youtubeWatchURL(candidate), nil
		}
	}
	if isLikelyYouTubeVideoID(entry.ID) {
		return youtubeWatchURL(entry.ID), nil
	}

	videoURL, err := firstChannelVideoURLFromEntries(entry.Entries)
	if err == nil {
		return videoURL, nil
	}
	if unsupportedErr != nil {
		return "", unsupportedErr
	}

	return "", err
}

func (d *Downloader) syncChannelCatalog(ctx context.Context, output []byte, requestedChannelID string) (channelCatalogResult, error) {
	page, err := d.syncChannelCatalogPage(ctx, output, requestedChannelID, 0)
	if err != nil {
		return channelCatalogResult{}, err
	}

	return channelCatalogResult{ChannelID: page.ChannelID, Videos: len(page.Videos)}, nil
}

func (d *Downloader) syncChannelCatalogPage(ctx context.Context, output []byte, requestedChannelID string, positionOffset int) (channelCatalogPageResult, error) {
	var metadata channelMetadata
	if err := json.Unmarshal(output, &metadata); err != nil {
		return channelCatalogPageResult{}, err
	}

	channelID := firstNonEmpty(requestedChannelID, metadata.ChannelID, metadata.UploaderID, metadata.ID)
	channelName := firstNonEmpty(metadata.Channel, metadata.Uploader, metadata.Title, channelID)
	channelDescription := strings.TrimSpace(metadata.Description)
	if !isSafeMetadataValue(channelName) || !isSafeMetadataText(channelDescription) {
		return channelCatalogPageResult{}, errors.New("channel metadata contains unsafe text")
	}
	channelThumbnailURL := catalogThumbnailURL(metadata.Thumbnail, metadata.Thumbnails)
	videos := catalogVideosFromEntries(metadata.Entries, channelID, channelName, requestedChannelID != "")
	for index := range videos {
		videos[index].CatalogPosition += positionOffset
	}
	result := channelCatalogPageResult{ChannelID: channelID, Videos: videos, RawEntries: len(metadata.Entries)}
	if channelID == "" && len(videos) == 0 {
		return result, nil
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return channelCatalogPageResult{}, err
	}
	defer tx.Rollback()

	if channelID != "" {
		if err := d.upsertChannel(ctx, tx, channelID, channelName, channelDescription, channelThumbnailURL); err != nil {
			return channelCatalogPageResult{}, err
		}
	}
	for _, video := range videos {
		if err := d.writeCatalogVideo(ctx, tx, video, false); err != nil {
			return channelCatalogPageResult{}, err
		}
	}

	return result, tx.Commit()
}

// writeCatalogVideo upserts the catalog row for one flat-dump video: the
// channel it belongs to (creating the channel row when needed), the video row
// itself, and its search documents. preservePosition keeps an existing
// catalog_position untouched (playlist imports must not reorder channel
// catalogs); channel scans pass false so their ordering is authoritative.
func (d *Downloader) writeCatalogVideo(ctx context.Context, exec sqlExecutor, video catalogVideo, preservePosition bool) error {
	if video.ChannelID != "" {
		if err := d.upsertChannel(ctx, exec, video.ChannelID, video.ChannelName, "", ""); err != nil {
			return err
		}
	}
	if err := d.upsertCatalogVideo(ctx, exec, video, preservePosition); err != nil {
		return err
	}
	videoTitle, videoDescription, err := d.videoSearchText(ctx, exec, video.ID)
	if err != nil {
		return err
	}
	if err := d.upsertSearch(ctx, exec, "video", video.ID, "title", videoTitle); err != nil {
		return err
	}
	if err := d.upsertSearch(ctx, exec, "video", video.ID, "description", videoDescription); err != nil {
		return err
	}

	return denorm.SyncVideoChannelSearchDocument(ctx, exec, video.ID, video.ChannelName, videoTitle)
}

func (d *Downloader) syncChannelAutoDownloads(ctx context.Context, payload ChannelAutoDownloadPayload) (channelAutoDownloadResult, error) {
	result := channelAutoDownloadResult{ChannelID: payload.ChannelID}
	candidates := []catalogVideo{}
	pageSize := DefaultChannelCatalogPageSize
	maxPages := (DefaultChannelCatalogLimit + pageSize - 1) / pageSize
	complete := false
	for page := 0; page < maxPages; page++ {
		start := page*pageSize + 1
		end := start + pageSize - 1
		if end > DefaultChannelCatalogLimit {
			end = DefaultChannelCatalogLimit
		}
		requestedCount := end - start + 1
		hitLimitPage := end >= DefaultChannelCatalogLimit
		command, err := d.BuildChannelCatalogPageCommand(payload.URL, start, end)
		if err != nil {
			return channelAutoDownloadResult{}, err
		}
		output, err := d.runYTDLP(ctx, command)
		if err != nil {
			return channelAutoDownloadResult{}, ytdlpJobError(command, output, err)
		}
		catalogPage, err := d.syncChannelCatalogPage(ctx, output, payload.ChannelID, page*pageSize)
		if err != nil {
			return channelAutoDownloadResult{}, err
		}
		if result.ChannelID == "" {
			result.ChannelID = catalogPage.ChannelID
		}
		result.Pages++
		result.Videos += len(catalogPage.Videos)
		candidates = append(candidates, catalogPage.Videos...)
		if catalogPage.RawEntries == 0 {
			complete = true
			break
		}
		overlaps, err := d.catalogPageHasDownloadedOverlap(ctx, catalogPage.Videos)
		if err != nil {
			return channelAutoDownloadResult{}, err
		}
		if overlaps {
			complete = true
			break
		}
		if catalogPage.RawEntries < requestedCount {
			complete = true
			break
		}
		if hitLimitPage {
			break
		}
	}

	queued, subscribed, err := d.enqueueMissingCatalogDownloadsIfSubscribed(ctx, payload.ChannelID, candidates)
	if err != nil {
		return channelAutoDownloadResult{}, err
	}
	if !subscribed {
		result.Skipped = true
		return result, nil
	}
	result.DownloadsQueued = queued
	result.Incomplete = !complete

	return result, nil
}

func (d *Downloader) catalogPageHasDownloadedOverlap(ctx context.Context, videos []catalogVideo) (bool, error) {
	for _, video := range videos {
		var mediaPath string
		err := d.db.QueryRowContext(ctx, "SELECT media_path FROM videos WHERE id = ?", video.ID).Scan(&mediaPath)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(mediaPath) != "" {
			return true, nil
		}
	}

	return false, nil
}

func (d *Downloader) enqueueMissingCatalogDownloadsIfSubscribed(ctx context.Context, channelID string, videos []catalogVideo) (int, bool, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	var subscribed int
	if err := tx.QueryRowContext(ctx, "SELECT subscribed FROM channels WHERE id = ?", channelID).Scan(&subscribed); errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	} else if err != nil {
		return 0, false, err
	}
	if subscribed != 1 {
		return 0, false, nil
	}
	queued, err := d.enqueueMissingCatalogDownloads(ctx, tx, videos)
	if err != nil {
		return 0, false, err
	}

	return queued, true, tx.Commit()
}

func (d *Downloader) enqueueMissingCatalogDownloads(ctx context.Context, tx *sql.Tx, videos []catalogVideo) (int, error) {
	store, err := d.jobStore()
	if err != nil {
		return 0, err
	}
	queued := 0
	considered := 0
	seen := map[string]struct{}{}
	for _, video := range videos {
		if _, ok := seen[video.ID]; ok {
			continue
		}
		seen[video.ID] = struct{}{}
		if considered >= DefaultChannelAutoDownloadLimit {
			break
		}
		considered++
		payload := Payload{URL: youtubeWatchURL(video.ID), Origin: DownloadOriginChannelAuto}
		needed, err := d.catalogDownloadNeeded(ctx, tx, video.ID, payload)
		if err != nil {
			return queued, err
		}
		if !needed {
			continue
		}
		_, created, err := enqueueDownloadTx(ctx, store, tx, payload, false)
		if err != nil {
			return queued, err
		}
		if created {
			queued++
		}
	}

	return queued, nil
}

func (d *Downloader) catalogDownloadNeeded(ctx context.Context, exec sqlExecutor, videoID string, payload Payload) (bool, error) {
	var mediaPath string
	err := exec.QueryRowContext(ctx, "SELECT media_path FROM videos WHERE id = ?", videoID).Scan(&mediaPath)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if strings.TrimSpace(mediaPath) != "" {
		return false, nil
	}
	if _, err := NormalizeDownloadURL(payload.URL); err != nil {
		return false, nil
	}
	if downloadOrigin(payload.Origin) == DownloadOriginChannelAuto {
		alreadyAutoDownloaded, err := succeededAutoDownloadExists(ctx, exec, videoID)
		if err != nil {
			return false, err
		}
		if alreadyAutoDownloaded {
			return false, nil
		}
	}

	return true, nil
}

func catalogVideosFromEntries(entries []channelEntry, channelID string, channelName string, forceChannel bool) []catalogVideo {
	seen := map[string]struct{}{}
	videos := []catalogVideo{}
	position := 0
	appendCatalogVideos(&videos, seen, entries, channelID, channelName, forceChannel, &position)

	return videos
}

func appendCatalogVideos(videos *[]catalogVideo, seen map[string]struct{}, entries []channelEntry, channelID string, channelName string, forceChannel bool, position *int) {
	for _, entry := range entries {
		entryChannelID := channelID
		entryChannelName := channelName
		if !forceChannel {
			entryChannelID = firstNonEmpty(entry.ChannelID, entry.UploaderID, channelID)
			entryChannelName = firstNonEmpty(entry.Channel, entry.Uploader, channelName, entryChannelID)
		}
		if video, ok := catalogVideoFromEntry(entry, entryChannelID, entryChannelName); ok {
			if _, exists := seen[video.ID]; !exists {
				seen[video.ID] = struct{}{}
				video.CatalogPosition = *position
				*position += 1
				*videos = append(*videos, video)
			}
		}
		if len(entry.Entries) > 0 {
			appendCatalogVideos(videos, seen, entry.Entries, entryChannelID, entryChannelName, forceChannel, position)
		}
	}
}

func catalogVideoFromEntry(entry channelEntry, channelID string, channelName string) (catalogVideo, bool) {
	videoID := channelEntryVideoID(entry)
	if videoID == "" {
		return catalogVideo{}, false
	}
	title := strings.TrimSpace(firstNonEmpty(entry.Title, videoID))
	description := strings.TrimSpace(entry.Description)
	if !isSafeMetadataValue(title) || !isSafeMetadataText(description) {
		return catalogVideo{}, false
	}
	duration := durationSeconds(entry.Duration)
	viewCount, hasViewCount := viewCountValue(entry.ViewCount)

	return catalogVideo{
		ID:              videoID,
		Title:           title,
		Description:     description,
		PublishedAt:     catalogPublishedAt(entry),
		DurationSeconds: duration,
		ViewCount:       viewCount,
		HasViewCount:    hasViewCount,
		ThumbnailURL:    catalogThumbnailURL(entry.Thumbnail, entry.Thumbnails),
		ChannelID:       channelID,
		ChannelName:     channelName,
	}, true
}

func channelEntryVideoID(entry channelEntry) string {
	if isLikelyYouTubeVideoID(entry.ID) {
		return entry.ID
	}
	for _, candidate := range []string{entry.WebpageURL, entry.URL} {
		if normalized, err := NormalizeYouTubeVideoURL(candidate); err == nil {
			return videoIDFromWatchURL(normalized)
		}
		if isLikelyYouTubeVideoID(candidate) {
			return candidate
		}
	}

	return ""
}

func catalogPublishedAt(entry channelEntry) string {
	if len(entry.UploadDate) == 8 {
		return entry.UploadDate[0:4] + "-" + entry.UploadDate[4:6] + "-" + entry.UploadDate[6:8]
	}
	if entry.Timestamp > 0 {
		return time.Unix(entry.Timestamp, 0).UTC().Format("2006-01-02")
	}

	return ""
}

func catalogUploadDate(uploadDate string) string {
	if len(uploadDate) == 8 {
		return uploadDate[0:4] + "-" + uploadDate[4:6] + "-" + uploadDate[6:8]
	}

	return ""
}

func catalogThumbnailURL(raw string, thumbnails []thumbnailMetadata) string {
	if value := validCatalogThumbnailURL(raw); value != "" {
		return value
	}
	for _, thumbnail := range thumbnails {
		if value := validCatalogThumbnailURL(thumbnail.URL); value != "" {
			return value
		}
	}

	return ""
}

func validCatalogThumbnailURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > maxCatalogThumbnailURLLength || !isSafeMetadataValue(value) {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	if !isAllowedCatalogThumbnailHost(parsed.Hostname()) {
		return ""
	}

	return parsed.String()
}

func isAllowedCatalogThumbnailHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "img.youtube.com" || host == "ytimg.com" || strings.HasSuffix(host, ".ytimg.com") || host == "yt3.ggpht.com" || host == "yt3.googleusercontent.com"
}

func (d *Downloader) upsertChannel(ctx context.Context, exec sqlExecutor, id string, name string, description string, thumbnailURL string) error {
	previousName, err := denorm.ChannelName(ctx, exec, id)
	if err != nil {
		return err
	}
	_, err = exec.ExecContext(ctx, `
INSERT INTO channels (id, external_id, name, description, thumbnail_url, updated_at)
VALUES (?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
ON CONFLICT(id) DO UPDATE SET
  name = excluded.name,
	description = CASE WHEN excluded.description <> '' THEN excluded.description ELSE channels.description END,
	thumbnail_url = CASE WHEN excluded.thumbnail_url <> '' THEN excluded.thumbnail_url ELSE channels.thumbnail_url END,
  updated_at = excluded.updated_at`, id, id, name, strings.TrimSpace(description), thumbnailURL)
	if err != nil {
		return err
	}
	if err := d.upsertSearch(ctx, exec, "channel", id, "name", name); err != nil {
		return err
	}
	if strings.TrimSpace(description) != "" {
		if err := d.upsertSearch(ctx, exec, "channel", id, "description", description); err != nil {
			return err
		}
	}

	// Refresh the per-video channel search docs so a channel rename is
	// reflected everywhere — but only when the name actually changed; see the
	// importer's upsertChannel for the O(videos)-per-video rationale.
	if previousName != name {
		return denorm.SyncChannelVideoSearchDocuments(ctx, exec, id, name)
	}
	return nil
}

func (d *Downloader) markChannelScanned(ctx context.Context, exec sqlExecutor, id string) error {
	_, err := exec.ExecContext(ctx, "UPDATE channels SET last_scanned_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?", id)

	return err
}

func (d *Downloader) markChannelSubscribed(ctx context.Context, exec sqlExecutor, id string) error {
	_, err := exec.ExecContext(ctx, "UPDATE channels SET subscribed = 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?", id)

	return err
}

func (d *Downloader) upsertCatalogVideo(ctx context.Context, exec sqlExecutor, video catalogVideo, preservePosition bool) error {
	_, err := exec.ExecContext(ctx, `
INSERT INTO videos (
	  id, external_id, channel_id, title, description, published_at, catalog_position, duration_seconds, view_count, thumbnail_url, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
ON CONFLICT(id) DO UPDATE SET
	  channel_id = excluded.channel_id,
	  title = CASE
	    WHEN videos.media_path <> '' AND excluded.description = '' AND excluded.duration_seconds = 0 THEN videos.title
	    ELSE excluded.title
	  END,
	  description = CASE WHEN excluded.description <> '' THEN excluded.description ELSE videos.description END,
	  published_at = CASE
	    WHEN excluded.published_at IS NULL THEN videos.published_at
	    WHEN videos.media_path <> '' AND videos.published_at IS NOT NULL AND videos.published_at <> '' THEN videos.published_at
	    ELSE excluded.published_at
	  END,
	  catalog_position = CASE WHEN ? THEN videos.catalog_position ELSE excluded.catalog_position END,
	  duration_seconds = CASE WHEN excluded.duration_seconds > 0 THEN excluded.duration_seconds ELSE videos.duration_seconds END,
	  view_count = CASE WHEN ? THEN excluded.view_count ELSE videos.view_count END,
	  thumbnail_url = CASE WHEN excluded.thumbnail_url <> '' THEN excluded.thumbnail_url ELSE videos.thumbnail_url END,
  updated_at = excluded.updated_at`,
		video.ID,
		video.ID,
		nullEmpty(video.ChannelID),
		firstNonEmpty(video.Title, video.ID),
		video.Description,
		nullEmpty(video.PublishedAt),
		video.CatalogPosition,
		video.DurationSeconds,
		video.ViewCount,
		video.ThumbnailURL,
		preservePosition,
		video.HasViewCount,
	)

	return err
}
