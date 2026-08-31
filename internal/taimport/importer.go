package taimport

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"kapsel/internal/assetpath"
	"kapsel/internal/denorm"
	"kapsel/internal/diskspace"
	"kapsel/internal/jobs"
)

const JobType = "ta_import"

var maxImportEntryBytes int64 = 64 * 1024 * 1024

const maxThumbnailURLLength = 2048

var ErrRootRequired = errors.New("TubeArchivist import root is required")
var ErrRootMustBeAbsolute = errors.New("TubeArchivist import root must be absolute")
var ErrRootOutsideImportRoot = errors.New("TubeArchivist import root is outside configured import root")

type Report struct {
	Channels  int    `json:"channels"`
	Videos    int    `json:"videos"`
	Playlists int    `json:"playlists"`
	Comments  int    `json:"comments"`
	Skipped   []Skip `json:"skipped"`
}

type Skip struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

type Payload struct {
	Root string `json:"root"`
}

func EnqueueJob(ctx context.Context, store *jobs.Store, payload Payload) (jobs.Job, error) {
	if store == nil {
		return jobs.Job{}, errors.New("TubeArchivist import enqueue missing job store")
	}
	payload, err := NormalizePayload(payload)
	if err != nil {
		return jobs.Job{}, err
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return jobs.Job{}, err
	}
	job, _, err := store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: JobType, PayloadJSON: string(payloadJSON)}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return store.ActiveByPayloadTx(ctx, tx, JobType, string(payloadJSON))
	})

	return job, err
}

type JobHandler struct {
	db                *sql.DB
	store             *jobs.Store
	importRoot        string
	dataRoot          string
	minFreeSpaceBytes uint64
	stat              diskspace.StatFunc
}

func NewJobHandler(db *sql.DB, store *jobs.Store, importRoot ...string) JobHandler {
	handler := JobHandler{db: db, store: store}
	if len(importRoot) > 0 {
		handler.importRoot = importRoot[0]
	}

	return handler
}

func (h JobHandler) WithDiskSpace(dataRoot string, minFreeSpaceBytes uint64, stat diskspace.StatFunc) JobHandler {
	h.dataRoot = dataRoot
	h.minFreeSpaceBytes = minFreeSpaceBytes
	h.stat = stat

	return h
}

func (h JobHandler) Handle(ctx context.Context, job jobs.Job) error {
	var payload Payload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return err
	}
	payload, err := h.normalizePayload(payload)
	if err != nil {
		return err
	}
	if err := h.ensureDiskSpace(); err != nil {
		return err
	}
	report, importErr := importWithProgress(ctx, h.db, payload.Root, func(progress float64) error {
		if h.store == nil {
			return nil
		}
		// Import progress is best-effort UI state; final result persistence remains correctness-critical.
		_ = h.store.ReportProgress(ctx, job.ID, progress)
		return nil
	})
	resultJSON, err := json.Marshal(report)
	if err != nil {
		return err
	}
	if h.store != nil {
		if importErr != nil {
			_ = h.store.SetPartialResult(context.WithoutCancel(ctx), job.ID, string(resultJSON))
		} else if err := h.store.CompleteWithResult(context.WithoutCancel(ctx), job.ID, string(resultJSON)); err != nil {
			return err
		}
	}
	if importErr != nil {
		return importErr
	}

	return nil
}

func (h JobHandler) ensureDiskSpace() error {
	if h.minFreeSpaceBytes == 0 {
		return nil
	}

	return diskspace.NewChecker(h.minFreeSpaceBytes, h.stat).Ensure(h.dataRoot)
}

func (h JobHandler) normalizePayload(payload Payload) (Payload, error) {
	if h.importRoot != "" {
		return NormalizePayloadForImportRoot(payload, h.importRoot)
	}

	return NormalizePayload(payload)
}

func NormalizePayload(payload Payload) (Payload, error) {
	payload.Root = strings.TrimSpace(payload.Root)
	if payload.Root == "" {
		return Payload{}, ErrRootRequired
	}
	if !filepath.IsAbs(payload.Root) {
		return Payload{}, ErrRootMustBeAbsolute
	}
	payload.Root = filepath.Clean(payload.Root)

	return payload, nil
}

func NormalizePayloadForImportRoot(payload Payload, importRoot string) (Payload, error) {
	payload, err := NormalizePayload(payload)
	if err != nil {
		return Payload{}, err
	}
	importRoot = strings.TrimSpace(importRoot)
	if importRoot == "" {
		return Payload{}, ErrRootOutsideImportRoot
	}
	allowedRoot, err := resolvePath(importRoot)
	if err != nil {
		return Payload{}, err
	}
	requestedRoot, err := containedPath(allowedRoot, payload.Root)
	if err != nil {
		return Payload{}, err
	}
	info, err := os.Stat(requestedRoot)
	if err != nil {
		return Payload{}, err
	}
	if !info.IsDir() {
		return Payload{}, fmt.Errorf("TubeArchivist import root is not a directory")
	}
	payload.Root = requestedRoot

	return payload, nil
}

func Import(ctx context.Context, db *sql.DB, root string) (Report, error) {
	return importWithProgress(ctx, db, root, nil)
}

func importWithProgress(ctx context.Context, db *sql.DB, root string, progress func(float64) error) (Report, error) {
	var report Report
	if err := ctx.Err(); err != nil {
		return report, err
	}
	backups, err := findBackups(root)
	if err != nil {
		return report, err
	}

	if len(backups) == 0 {
		if progress != nil {
			if err := progress(0.99); err != nil {
				return report, err
			}
		}
		if err := ctx.Err(); err != nil {
			return report, err
		}
		return report, nil
	}
	for i, backup := range backups {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		start := float64(i) / float64(len(backups))
		end := float64(i+1) / float64(len(backups))
		if progress != nil {
			if err := progress(start); err != nil {
				return report, err
			}
		}
		zipProgress := func(p float64) error {
			if progress != nil {
				return progress(start + p*(end-start))
			}
			return nil
		}
		if err := importZip(ctx, db, backup, &report, zipProgress); err != nil {
			return report, err
		}
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if progress != nil {
			if err := progress(end); err != nil {
				return report, err
			}
		}
		if err := ctx.Err(); err != nil {
			return report, err
		}
	}

	return report, nil
}

func findBackups(root string) ([]string, error) {
	resolvedRoot, err := resolvePath(root)
	if err != nil {
		return nil, err
	}
	patterns := []string{
		filepath.Join(resolvedRoot, "cache", "backup", "*.zip"),
		filepath.Join(resolvedRoot, "backup", "*.zip"),
		filepath.Join(resolvedRoot, "*.zip"),
	}

	seen := map[string]bool{}
	var backups []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			resolvedMatch, err := containedPath(resolvedRoot, match)
			if err != nil {
				return nil, err
			}
			if !seen[resolvedMatch] {
				seen[resolvedMatch] = true
				backups = append(backups, resolvedMatch)
			}
		}
	}

	return backups, nil
}

func resolvePath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	return filepath.EvalSymlinks(filepath.Clean(absPath))
}

func containedPath(root string, path string) (string, error) {
	resolvedPath, err := resolvePath(path)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, resolvedPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", ErrRootOutsideImportRoot
	}

	return resolvedPath, nil
}

func importZip(ctx context.Context, db *sql.DB, path string, report *Report, progress func(float64) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	reader, err := zip.OpenReader(filepath.Clean(path))
	if err != nil {
		return err
	}
	defer reader.Close()

	var channels, videos, playlists, comments []*zip.File
	for _, file := range reader.File {
		switch backupKind(file.Name) {
		case "channel":
			channels = append(channels, file)
		case "video":
			videos = append(videos, file)
		case "playlist":
			playlists = append(playlists, file)
		case "comment":
			comments = append(comments, file)
		}
	}
	total := len(channels) + len(videos) + len(playlists) + len(comments)
	done := 0
	for _, file := range append(append(append(channels, videos...), playlists...), comments...) {
		if err := ctx.Err(); err != nil {
			return err
		}
		kind := backupKind(file.Name)
		if kind == "" {
			continue
		}
		if err := importFile(ctx, db, file, kind, report, progress); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		done++
		if progress != nil && total > 0 {
			if err := progress(float64(done) / float64(total)); err != nil {
				return err
			}
		}
	}

	return nil
}

func backupKind(name string) string {
	base := filepath.Base(name)
	switch {
	case strings.HasPrefix(base, "es_channel-"):
		return "channel"
	case strings.HasPrefix(base, "es_video-"):
		return "video"
	case strings.HasPrefix(base, "es_playlist-"):
		return "playlist"
	case strings.HasPrefix(base, "es_comment-"):
		return "comment"
	default:
		return ""
	}
}

func importFile(ctx context.Context, db *sql.DB, file *zip.File, kind string, report *Report, progress func(float64) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := readImportEntry(ctx, file)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	for i := 0; i < len(lines); i += 2 {
		if err := ctx.Err(); err != nil {
			return err
		}
		if progress != nil && i > 0 && i%200 == 0 {
			if err := progress(0); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if i+1 >= len(lines) {
			report.Skipped = append(report.Skipped, Skip{File: file.Name, Reason: "missing source line"})
			continue
		}

		source := []byte(lines[i+1])
		switch kind {
		case "channel":
			if err := importChannel(ctx, db, source); err != nil {
				report.Skipped = append(report.Skipped, Skip{File: file.Name, Reason: err.Error()})
				continue
			}
			report.Channels++
		case "video":
			if err := importVideo(ctx, db, source); err != nil {
				report.Skipped = append(report.Skipped, Skip{File: file.Name, Reason: err.Error()})
				continue
			}
			report.Videos++
		case "playlist":
			if err := importPlaylist(ctx, db, source); err != nil {
				report.Skipped = append(report.Skipped, Skip{File: file.Name, Reason: err.Error()})
				continue
			}
			report.Playlists++
		case "comment":
			count, err := importComment(ctx, db, source)
			if err != nil {
				report.Skipped = append(report.Skipped, Skip{File: file.Name, Reason: err.Error()})
				continue
			}
			report.Comments += count
		}
	}

	return nil
}

func readImportEntry(ctx context.Context, file *zip.File) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if maxImportEntryBytes >= 0 && file.UncompressedSize64 > uint64(maxImportEntryBytes) {
		return nil, fmt.Errorf("TubeArchivist backup entry %s exceeds %d byte limit", file.Name, maxImportEntryBytes)
	}
	opened, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer opened.Close()

	reader := io.LimitReader(opened, maxImportEntryBytes+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(body)) > maxImportEntryBytes {
		return nil, fmt.Errorf("TubeArchivist backup entry %s exceeds %d byte limit", file.Name, maxImportEntryBytes)
	}

	return body, nil
}

type channelDoc struct {
	ID          string `json:"channel_id"`
	Name        string `json:"channel_name"`
	Description string `json:"channel_description"`
	Subscribed  bool   `json:"channel_subscribed"`
	ThumbURL    string `json:"channel_thumb_url"`
}

type videoDoc struct {
	ID          string          `json:"youtube_id"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Published   json.RawMessage `json:"published"`
	Downloaded  json.RawMessage `json:"date_downloaded"`
	MediaURL    string          `json:"media_url"`
	ThumbURL    string          `json:"vid_thumb_url"`
	Channel     struct {
		ID   string `json:"channel_id"`
		Name string `json:"channel_name"`
	} `json:"channel"`
	Player struct {
		Duration int  `json:"duration"`
		Position int  `json:"position"`
		Watched  bool `json:"watched"`
	} `json:"player"`
	Stats struct {
		ViewCount *int `json:"view_count"`
	} `json:"stats"`
	Subtitles []subtitleDoc `json:"subtitles"`
}

type subtitleDoc struct {
	Language string `json:"lang"`
	Name     string `json:"name"`
	Source   string `json:"source"`
	Format   string `json:"format"`
	Path     string `json:"path"`
	MediaURL string `json:"media_url"`
	Text     string `json:"text"`
}

type playlistDoc struct {
	ID          string `json:"playlist_id"`
	Name        string `json:"playlist_name"`
	Description string `json:"playlist_description"`
	Subscribed  bool   `json:"playlist_subscribed"`
	ChannelID   string `json:"playlist_channel_id"`
	Entries     []struct {
		VideoID    string `json:"youtube_id"`
		Position   int    `json:"idx"`
		Downloaded bool   `json:"downloaded"`
	} `json:"playlist_entries"`
}

type commentDoc struct {
	commentFields
	Comments []commentFields `json:"comment_comments"`
}

type commentFields struct {
	ID           string          `json:"comment_id"`
	AltID        string          `json:"id"`
	VideoID      string          `json:"youtube_id"`
	AltVideoID   string          `json:"video_id"`
	ParentID     string          `json:"comment_parent"`
	AltParentID  string          `json:"parent_id"`
	Author       string          `json:"comment_author"`
	AltAuthor    string          `json:"author"`
	Text         string          `json:"comment_text"`
	AltText      string          `json:"text"`
	Published    string          `json:"comment_published"`
	AltPublished string          `json:"published_at"`
	Timestamp    json.RawMessage `json:"comment_timestamp"`
	LikeCount    int             `json:"comment_likecount"`
}

type normalizedComment struct {
	ID          string
	VideoID     string
	ParentID    string
	Author      string
	Text        string
	PublishedAt string
	LikeCount   int
}

type sqlRunner interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func importChannel(ctx context.Context, db *sql.DB, source []byte) error {
	var doc channelDoc
	if err := json.Unmarshal(source, &doc); err != nil {
		return err
	}
	if doc.ID == "" {
		return fmt.Errorf("channel missing id")
	}
	thumbnailPath, thumbnailURL, err := cleanThumbnailReference(doc.ThumbURL)
	if err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := upsertChannel(ctx, tx, doc.ID, doc.Name, doc.Description, thumbnailURL, doc.Subscribed, false); err != nil {
		return err
	}
	if err := upsertMediaAsset(ctx, tx, "channel", doc.ID, "thumbnail", thumbnailPath); err != nil {
		return err
	}

	return tx.Commit()
}

func importVideo(ctx context.Context, db *sql.DB, source []byte) error {
	var doc videoDoc
	if err := json.Unmarshal(source, &doc); err != nil {
		return err
	}
	if doc.ID == "" {
		return fmt.Errorf("video missing id")
	}
	mediaPath, err := cleanAssetPath(doc.MediaURL)
	if err != nil {
		return err
	}
	thumbnailPath, thumbnailURL, err := cleanThumbnailReference(doc.ThumbURL)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if doc.Channel.ID != "" {
		if err := upsertChannel(ctx, tx, doc.Channel.ID, doc.Channel.Name, "", "", false, true); err != nil {
			return err
		}
	}

	now := time.Now().UTC()
	updatedAt := now.Format(time.RFC3339Nano)
	archivedAt := ""
	if mediaPath != "" {
		archivedAt = downloadedAt(doc.Downloaded, now.Format(time.RFC3339))
	}
	viewCount, hasViewCount := viewCountValue(doc.Stats.ViewCount)
	_, err = tx.ExecContext(ctx, `
INSERT INTO videos (
	  id, external_id, channel_id, title, description, published_at, duration_seconds, view_count, media_path, thumbnail_path, thumbnail_url, media_origin, media_downloaded_at, archived_at, watched, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
	  media_origin = excluded.media_origin,
	  media_downloaded_at = excluded.media_downloaded_at,
	  archived_at = CASE WHEN videos.archived_at IS NOT NULL AND videos.archived_at <> '' THEN videos.archived_at ELSE excluded.archived_at END,
	  watched = CASE WHEN videos.watched = 1 THEN 1 ELSE excluded.watched END,
  updated_at = excluded.updated_at`,
		doc.ID,
		doc.ID,
		nullEmpty(doc.Channel.ID),
		doc.Title,
		doc.Description,
		nullEmpty(publishedAt(doc.Published)),
		doc.Player.Duration,
		viewCount,
		mediaPath,
		thumbnailPath,
		thumbnailURL,
		"imported",
		archivedAt,
		nullEmpty(archivedAt),
		boolInt(doc.Player.Watched),
		updatedAt,
		hasViewCount,
	)
	if err != nil {
		return err
	}

	if err := upsertMediaAsset(ctx, tx, "video", doc.ID, "media", mediaPath); err != nil {
		return err
	}
	if err := upsertMediaAsset(ctx, tx, "video", doc.ID, "thumbnail", thumbnailPath); err != nil {
		return err
	}
	if err := upsertProgress(ctx, tx, doc); err != nil {
		return err
	}
	for _, subtitle := range doc.Subtitles {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := importSubtitle(ctx, tx, doc.ID, subtitle); err != nil {
			return err
		}
	}
	if err := upsertSearchDocument(ctx, tx, "video", doc.ID, "title", doc.Title); err != nil {
		return err
	}
	if err := upsertSearchDocument(ctx, tx, "video", doc.ID, "description", doc.Description); err != nil {
		return err
	}
	if err := denorm.SyncVideoChannelSearchDocument(ctx, tx, doc.ID, doc.Channel.Name, doc.Title); err != nil {
		return err
	}

	return tx.Commit()
}

func importPlaylist(ctx context.Context, db *sql.DB, source []byte) error {
	var doc playlistDoc
	if err := json.Unmarshal(source, &doc); err != nil {
		return err
	}
	if doc.ID == "" {
		return fmt.Errorf("playlist missing id")
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
INSERT INTO playlists (id, external_id, channel_id, title, description, subscribed, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  channel_id = excluded.channel_id,
  title = excluded.title,
  description = excluded.description,
  subscribed = excluded.subscribed,
  updated_at = excluded.updated_at`,
		doc.ID,
		doc.ID,
		nullEmpty(doc.ChannelID),
		doc.Name,
		doc.Description,
		boolInt(doc.Subscribed),
		now,
	)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM playlist_entries WHERE playlist_id = ?", doc.ID); err != nil {
		return err
	}
	for _, entry := range doc.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.Downloaded || entry.VideoID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO playlist_entries (playlist_id, video_id, position)
VALUES (?, ?, ?)
ON CONFLICT(playlist_id, video_id) DO UPDATE SET position = excluded.position`, doc.ID, entry.VideoID, entry.Position); err != nil {
			return err
		}
	}
	if err := upsertSearchDocument(ctx, tx, "playlist", doc.ID, "title", doc.Name); err != nil {
		return err
	}
	if err := upsertSearchDocument(ctx, tx, "playlist", doc.ID, "description", doc.Description); err != nil {
		return err
	}

	return tx.Commit()
}

func importComment(ctx context.Context, db *sql.DB, source []byte) (int, error) {
	var doc commentDoc
	if err := json.Unmarshal(source, &doc); err != nil {
		return 0, err
	}
	entries := doc.commentEntries()
	comments := make([]normalizedComment, 0, len(entries))
	deleteIDs := []string{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		comment, err := normalizeComment(entry)
		if err != nil {
			return 0, err
		}
		if comment.Text == "" {
			deleteIDs = append(deleteIDs, comment.ID)
			continue
		}
		comments = append(comments, comment)
	}

	if len(comments) == 0 && len(deleteIDs) == 0 {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, id := range deleteIDs {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := deleteComment(ctx, tx, id); err != nil {
			return 0, err
		}
	}
	count, err := importComments(ctx, tx, comments)
	if err != nil {
		return 0, err
	}
	if len(deleteIDs) > 0 {
		if err := deleteOrphanedCommentSearchDocuments(ctx, tx); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return count, nil
}

func (d commentDoc) commentEntries() []commentFields {
	videoID := firstNonEmpty(d.VideoID, d.AltVideoID)
	if len(d.Comments) > 0 {
		entries := make([]commentFields, 0, len(d.Comments))
		for _, entry := range d.Comments {
			if firstNonEmpty(entry.VideoID, entry.AltVideoID) == "" {
				entry.VideoID = videoID
			}
			entries = append(entries, entry)
		}

		return entries
	}

	return []commentFields{d.commentFields}
}

func normalizeComment(entry commentFields) (normalizedComment, error) {
	id := strings.TrimSpace(firstNonEmpty(entry.ID, entry.AltID))
	videoID := strings.TrimSpace(firstNonEmpty(entry.VideoID, entry.AltVideoID))
	text := strings.TrimSpace(firstNonEmpty(entry.Text, entry.AltText))
	if id == "" {
		return normalizedComment{}, fmt.Errorf("comment missing id")
	}
	if videoID == "" {
		return normalizedComment{}, fmt.Errorf("comment missing video id")
	}

	return normalizedComment{
		ID:          id,
		VideoID:     videoID,
		ParentID:    normalizeCommentParent(firstNonEmpty(entry.ParentID, entry.AltParentID), id),
		Author:      strings.TrimSpace(firstNonEmpty(entry.Author, entry.AltAuthor)),
		Text:        text,
		PublishedAt: commentPublishedAt(entry),
		LikeCount:   entry.LikeCount,
	}, nil
}

func importComments(ctx context.Context, db sqlRunner, comments []normalizedComment) (int, error) {
	pending := make(map[string]normalizedComment, len(comments))
	for _, comment := range comments {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		pending[comment.ID] = comment
	}

	imported := 0
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return imported, err
		}
		progress := false
		for id, comment := range pending {
			if err := ctx.Err(); err != nil {
				return imported, err
			}
			if comment.ParentID != "" {
				if _, waitsForParent := pending[comment.ParentID]; waitsForParent {
					continue
				}
				exists, err := commentExists(ctx, db, comment.ParentID)
				if err != nil {
					return imported, err
				}
				if !exists {
					comment.ParentID = ""
				}
			}
			if err := upsertComment(ctx, db, comment); err != nil {
				return imported, err
			}
			delete(pending, id)
			imported++
			progress = true
		}
		if !progress {
			for id, comment := range pending {
				comment.ParentID = ""
				pending[id] = comment
			}
		}
	}

	return imported, nil
}

func deleteComment(ctx context.Context, db sqlRunner, id string) error {
	_, err := db.ExecContext(ctx, "DELETE FROM comments WHERE id = ?", id)

	return err
}

func deleteOrphanedCommentSearchDocuments(ctx context.Context, db sqlRunner) error {
	_, err := db.ExecContext(ctx, `
DELETE FROM search_documents
WHERE owner_type = 'comment'
  AND NOT EXISTS (SELECT 1 FROM comments WHERE comments.id = search_documents.owner_id)`)

	return err
}

func upsertComment(ctx context.Context, db sqlRunner, comment normalizedComment) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO comments (id, video_id, parent_id, author, text, published_at, like_count)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  video_id = excluded.video_id,
  parent_id = excluded.parent_id,
  author = excluded.author,
  text = excluded.text,
  published_at = excluded.published_at,
  like_count = excluded.like_count`, comment.ID, comment.VideoID, nullEmpty(comment.ParentID), comment.Author, comment.Text, nullEmpty(comment.PublishedAt), comment.LikeCount)
	if err != nil {
		return err
	}

	return upsertSearchDocument(ctx, db, "comment", comment.ID, "text", comment.Text)
}

func commentExists(ctx context.Context, db sqlRunner, id string) (bool, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM comments WHERE id = ?)", id).Scan(&exists); err != nil {
		return false, err
	}

	return exists, nil
}

func normalizeCommentParent(parentID string, commentID string) string {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" || strings.EqualFold(parentID, "root") || parentID == commentID {
		return ""
	}

	return parentID
}

func commentPublishedAt(entry commentFields) string {
	if value := commentTimestamp(entry.Timestamp); value > 0 {
		return time.Unix(value, 0).UTC().Format(time.RFC3339)
	}

	return strings.TrimSpace(firstNonEmpty(entry.Published, entry.AltPublished))
}

func commentTimestamp(raw json.RawMessage) int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return int64(number)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		value, _ := strconv.ParseFloat(strings.TrimSpace(text), 64)
		return int64(value)
	}

	return 0
}

func publishedAt(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil && number > 0 {
		return time.Unix(int64(number), 0).UTC().Format(time.RFC3339)
	}

	return ""
}

func downloadedAt(raw json.RawMessage, fallback string) string {
	if len(raw) == 0 || string(raw) == "null" {
		return fallback
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return time.Unix(int64(number), 0).UTC().Format(time.RFC3339)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return fallback
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fallback
	}
	if value, err := strconv.ParseFloat(text, 64); err == nil {
		return time.Unix(int64(value), 0).UTC().Format(time.RFC3339)
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.UTC().Format(time.RFC3339)
		}
	}

	return fallback
}

func upsertChannel(ctx context.Context, db sqlRunner, id string, name string, description string, thumbnailURL string, subscribed bool, preserveThumbnailURL bool) error {
	if name == "" {
		name = id
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(ctx, `
INSERT INTO channels (id, external_id, name, description, thumbnail_url, subscribed, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  name = excluded.name,
  description = CASE WHEN excluded.description != '' THEN excluded.description ELSE channels.description END,
  thumbnail_url = CASE WHEN ? THEN CASE WHEN excluded.thumbnail_url != '' THEN excluded.thumbnail_url ELSE channels.thumbnail_url END ELSE excluded.thumbnail_url END,
  subscribed = CASE WHEN excluded.subscribed = 1 THEN 1 ELSE channels.subscribed END,
	updated_at = excluded.updated_at`, id, id, name, description, thumbnailURL, boolInt(subscribed), now, preserveThumbnailURL)
	if err != nil {
		return err
	}

	if err := upsertSearchDocument(ctx, db, "channel", id, "name", name); err != nil {
		return err
	}

	// Refresh the per-video channel search docs so a channel rename is
	// reflected everywhere; the sync skips videos whose doc text already
	// matches, keeping per-video import loops cheap.
	return denorm.SyncChannelVideoSearchDocuments(ctx, db, id, name)
}

func upsertMediaAsset(ctx context.Context, db sqlRunner, ownerType string, ownerID string, kind string, path string) error {
	return denorm.SyncMediaAsset(ctx, db, ownerType, ownerID, kind, path)
}

func upsertProgress(ctx context.Context, db sqlRunner, doc videoDoc) error {
	if _, err := db.ExecContext(ctx, `
INSERT INTO user_progress (video_id, position_seconds, duration_seconds, watched)
VALUES (?, ?, ?, ?)
ON CONFLICT(video_id) DO UPDATE SET
  position_seconds = CASE WHEN excluded.position_seconds > user_progress.position_seconds THEN excluded.position_seconds ELSE user_progress.position_seconds END,
  duration_seconds = excluded.duration_seconds,
  watched = CASE WHEN user_progress.watched = 1 THEN 1 ELSE excluded.watched END,
  updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`, doc.ID, doc.Player.Position, doc.Player.Duration, boolInt(doc.Player.Watched)); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `
UPDATE videos
SET watched = 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND watched = 0 AND EXISTS (
  SELECT 1 FROM user_progress WHERE video_id = ? AND watched = 1
)`, doc.ID, doc.ID)

	return err
}

func importSubtitle(ctx context.Context, db sqlRunner, videoID string, doc subtitleDoc) error {
	language := cleanSubtitleLanguage(doc.Language)
	if language == "" {
		return nil
	}
	path, err := cleanAssetPath(firstNonEmpty(doc.Path, doc.MediaURL))
	if err != nil {
		return err
	}
	format := cleanSubtitleFormat(firstNonEmpty(doc.Format, filepath.Ext(path)))
	if format == "" {
		return nil
	}
	source := strings.TrimSpace(doc.Source)
	if source == "" {
		source = "imported"
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO subtitles (video_id, language, name, source, format, path, text)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(video_id, language, source) DO UPDATE SET
  name = excluded.name,
  format = excluded.format,
  path = excluded.path,
  text = excluded.text`, videoID, language, doc.Name, source, format, path, doc.Text)
	if err != nil {
		return err
	}
	field := "text:" + language + ":" + source
	if doc.Text == "" {
		return deleteSearchDocument(ctx, db, "subtitle", videoID, field)
	}

	return upsertSearchDocument(ctx, db, "subtitle", videoID, field, doc.Text)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}

	return ""
}

func upsertSearchDocument(ctx context.Context, db sqlRunner, ownerType string, ownerID string, field string, text string) error {
	return denorm.SyncSearchDocument(ctx, db, ownerType, ownerID, field, text)
}

func deleteSearchDocument(ctx context.Context, db sqlRunner, ownerType string, ownerID string, field string) error {
	return denorm.DeleteSearchDocument(ctx, db, ownerType, ownerID, field)
}

func cleanThumbnailReference(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", nil
	}
	if thumbnailReferenceIsRemote(value) {
		thumbnailURL, ok := cleanRemoteThumbnailURL(value)
		if !ok {
			return "", "", assetpath.ErrInvalid
		}

		return "", thumbnailURL, nil
	}
	path, err := cleanAssetPath(value)
	if err != nil {
		return "", "", err
	}

	return path, "", nil
}

func thumbnailReferenceIsRemote(value string) bool {
	return strings.Contains(value, "://") || strings.HasPrefix(value, "//")
}

func cleanRemoteThumbnailURL(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxThumbnailURLLength {
		return "", false
	}
	if strings.HasPrefix(value, "//") {
		value = "https:" + value
	}
	for _, char := range value {
		if char < 0x20 {
			return "", false
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	if !isAllowedThumbnailHost(parsed.Hostname()) {
		return "", false
	}

	return parsed.String(), true
}

func isAllowedThumbnailHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "img.youtube.com" || host == "ytimg.com" || strings.HasSuffix(host, ".ytimg.com") || host == "yt3.ggpht.com" || host == "yt3.googleusercontent.com"
}

func cleanAssetPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.Contains(value, "://") {
		return "", assetpath.ErrInvalid
	}

	return assetpath.Clean(strings.TrimLeft(value, "/"))
}

func boolInt(value bool) int {
	if value {
		return 1
	}

	return 0
}

func viewCountValue(value *int) (int, bool) {
	if value == nil || *value < 0 {
		return 0, false
	}

	return *value, true
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

func nullEmpty(value string) any {
	if value == "" {
		return nil
	}

	return value
}
