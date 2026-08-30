package download

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kapsel/internal/assetpath"
)

type RetentionCleaner struct {
	db        *sql.DB
	mediaRoot string
}

type retentionCandidate struct {
	VideoID       string
	MediaPath     string
	DownloadedAt  string
	MediaOrigin   string
	StaleCutoff   string
	WatchedCutoff string
}

func NewRetentionCleaner(db *sql.DB, mediaRoot string) *RetentionCleaner {
	return &RetentionCleaner{db: db, mediaRoot: mediaRoot}
}

func (c *RetentionCleaner) Apply(ctx context.Context, options RetentionOptions) (RetentionResult, error) {
	if c.db == nil {
		return RetentionResult{}, errors.New("retention requires database")
	}
	if strings.TrimSpace(c.mediaRoot) == "" {
		return RetentionResult{}, errors.New("retention requires media root")
	}
	nowFunc := options.Now
	if nowFunc == nil {
		nowFunc = func() time.Time { return time.Now().UTC() }
	}
	staleAfter := options.StaleAfter
	if staleAfter <= 0 {
		staleAfter = DefaultRetentionStaleAfter
	}
	watchedAfter := options.WatchedAfter
	if watchedAfter <= 0 {
		watchedAfter = DefaultRetentionWatchedAfter
	}
	limit := options.Limit
	if limit <= 0 || limit > DefaultRetentionLimit {
		limit = DefaultRetentionLimit
	}
	now := nowFunc().UTC()
	staleCutoff := now.Add(-staleAfter).Format(time.RFC3339Nano)
	// An empty watched cutoff disables the watched branch: watched_at is never
	// empty for watched rows, so "watched_at <> '' AND watched_at <= ''" matches
	// nothing. The stale branch keeps its own cutoff either way.
	watchedCutoff := ""
	if !options.WatchedCleanupDisabled {
		watchedCutoff = now.Add(-watchedAfter).Format(time.RFC3339Nano)
	}
	candidates, err := c.retentionCandidates(ctx, staleCutoff, watchedCutoff, limit)
	if err != nil {
		return RetentionResult{}, err
	}

	result := RetentionResult{Checked: len(candidates)}
	for _, candidate := range candidates {
		removed, err := c.removeRetainedVideoMedia(ctx, candidate)
		if err != nil {
			return result, err
		}
		if !removed {
			continue
		}
		result.Removed++
		result.RemovedVideoIDs = append(result.RemovedVideoIDs, candidate.VideoID)
	}

	return result, nil
}

// retentionCandidates selects media eligible for cleanup. Watched videos are
// always eligible once the watched grace has passed, regardless of media origin
// or channel download rank; keep_forever is the only protection. The stale
// branch additionally removes unstarted channel-auto media beyond the newest 2
// per channel.
func (c *RetentionCleaner) retentionCandidates(ctx context.Context, staleCutoff string, watchedCutoff string, limit int) ([]retentionCandidate, error) {
	rows, err := c.db.QueryContext(ctx, `
WITH ranked_auto_downloads AS (
	  SELECT
	    v.id,
	    v.media_path,
	    v.media_downloaded_at AS downloaded_at,
	    v.media_origin AS media_origin,
		COALESCE(up.position_seconds, 0) AS position_seconds,
		COALESCE(up.watched, 0) AS progress_watched,
		v.watched AS video_watched,
		v.keep_forever AS keep_forever,
		ROW_NUMBER() OVER (
      PARTITION BY v.channel_id
      ORDER BY COALESCE(NULLIF(v.published_at, ''), NULLIF(v.archived_at, ''), v.updated_at, v.created_at, '') DESC, v.id DESC
    ) AS channel_download_rank
	  FROM videos v
	  LEFT JOIN user_progress up ON up.video_id = v.id
	  WHERE v.channel_id IS NOT NULL
	    AND v.media_path <> ''
	    AND v.media_origin = ?
	    AND v.media_downloaded_at <> ''
),
watched_with_media AS (
  SELECT
    v.id,
    v.media_path,
    v.media_downloaded_at AS downloaded_at,
    v.media_origin AS media_origin,
    CASE
      WHEN COALESCE(up.watched, 0) = 1 THEN COALESCE(NULLIF(up.updated_at, ''), NULLIF(v.updated_at, ''), v.media_downloaded_at)
      WHEN v.watched = 1 THEN COALESCE(NULLIF(v.updated_at, ''), NULLIF(up.updated_at, ''), v.media_downloaded_at)
      ELSE ''
    END AS watched_at
  FROM videos v
  LEFT JOIN user_progress up ON up.video_id = v.id
  WHERE v.media_path <> ''
    AND v.media_downloaded_at <> ''
    AND v.keep_forever = 0
)
SELECT id, media_path, downloaded_at, media_origin
FROM (
  SELECT id, media_path, downloaded_at, media_origin
  FROM watched_with_media
  WHERE watched_at <> '' AND watched_at <= ?
  UNION
  SELECT id, media_path, downloaded_at, media_origin
  FROM ranked_auto_downloads
  WHERE keep_forever = 0
    AND channel_download_rank > 2
    AND position_seconds = 0
    AND progress_watched = 0
    AND video_watched = 0
    AND downloaded_at <= ?
)
ORDER BY downloaded_at ASC, id ASC
LIMIT ?`, DownloadOriginChannelAuto, watchedCutoff, staleCutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	candidates := []retentionCandidate{}
	for rows.Next() {
		var candidate retentionCandidate
		if err := rows.Scan(&candidate.VideoID, &candidate.MediaPath, &candidate.DownloadedAt, &candidate.MediaOrigin); err != nil {
			return nil, err
		}
		candidate.StaleCutoff = staleCutoff
		candidate.WatchedCutoff = watchedCutoff
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return candidates, nil
}

func (c *RetentionCleaner) removeRetainedVideoMedia(ctx context.Context, candidate retentionCandidate) (bool, error) {
	cleaned, info, err := assetpath.Lstat(c.mediaRoot, candidate.MediaPath)
	if errors.Is(err, assetpath.ErrSymlink) {
		return false, fmt.Errorf("retention media file path contains symlink: %s", candidate.MediaPath)
	}
	if errors.Is(err, assetpath.ErrInvalid) {
		return false, fmt.Errorf("retention media file path is unsafe: %s", candidate.MediaPath)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	fileExists := err == nil
	if fileExists {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("refusing to remove non-regular retained media file %s", candidate.MediaPath)
		}
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if candidate.StaleCutoff != "" {
		eligible, err := retentionCandidateStillEligible(ctx, tx, candidate)
		if err != nil {
			return false, err
		}
		if !eligible {
			return false, tx.Commit()
		}
	}

	update, err := tx.ExecContext(ctx, "UPDATE videos SET media_path = '', media_origin = 'imported', media_downloaded_at = '', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND media_path = ? AND media_origin = ? AND media_downloaded_at = ? AND keep_forever = 0", candidate.VideoID, candidate.MediaPath, candidate.MediaOrigin, candidate.DownloadedAt)
	if err != nil {
		return false, err
	}
	changed, err := update.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed == 0 {
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM media_assets WHERE owner_type = 'video' AND owner_id = ? AND kind = 'media' AND path = ?", candidate.VideoID, candidate.MediaPath); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}

	if fileExists {
		if err := os.Remove(filepath.Join(c.mediaRoot, filepath.FromSlash(cleaned))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}

	return true, nil
}

func retentionCandidateStillEligible(ctx context.Context, tx *sql.Tx, candidate retentionCandidate) (bool, error) {
	var eligible bool
	err := tx.QueryRowContext(ctx, `
WITH ranked_auto_downloads AS (
  SELECT
    v.id,
    v.media_path,
    v.media_downloaded_at AS downloaded_at,
    v.media_origin AS media_origin,
    COALESCE(up.position_seconds, 0) AS position_seconds,
    COALESCE(up.watched, 0) AS progress_watched,
    v.watched AS video_watched,
    v.keep_forever AS keep_forever,
    ROW_NUMBER() OVER (
      PARTITION BY v.channel_id
      ORDER BY COALESCE(NULLIF(v.published_at, ''), NULLIF(v.archived_at, ''), v.updated_at, v.created_at, '') DESC, v.id DESC
    ) AS channel_download_rank
  FROM videos v
  LEFT JOIN user_progress up ON up.video_id = v.id
  WHERE v.channel_id IS NOT NULL
    AND v.media_path <> ''
    AND v.media_origin = ?
    AND v.media_downloaded_at <> ''
),
watched_with_media AS (
  SELECT
    v.id,
    v.media_path,
    v.media_downloaded_at AS downloaded_at,
    v.media_origin AS media_origin,
    CASE
      WHEN COALESCE(up.watched, 0) = 1 THEN COALESCE(NULLIF(up.updated_at, ''), NULLIF(v.updated_at, ''), v.media_downloaded_at)
      WHEN v.watched = 1 THEN COALESCE(NULLIF(v.updated_at, ''), NULLIF(up.updated_at, ''), v.media_downloaded_at)
      ELSE ''
    END AS watched_at
  FROM videos v
  LEFT JOIN user_progress up ON up.video_id = v.id
  WHERE v.media_path <> ''
    AND v.media_downloaded_at <> ''
    AND v.keep_forever = 0
)
SELECT EXISTS(
  SELECT 1
  FROM (
    SELECT id, media_path, downloaded_at, media_origin
    FROM watched_with_media
    WHERE watched_at <> '' AND watched_at <= ?
    UNION
    SELECT id, media_path, downloaded_at, media_origin
    FROM ranked_auto_downloads
    WHERE keep_forever = 0
      AND channel_download_rank > 2
      AND position_seconds = 0
      AND progress_watched = 0
      AND video_watched = 0
      AND downloaded_at <= ?
  )
  WHERE id = ?
    AND media_path = ?
    AND downloaded_at = ?
    AND media_origin = ?
)`, DownloadOriginChannelAuto, candidate.WatchedCutoff, candidate.StaleCutoff, candidate.VideoID, candidate.MediaPath, candidate.DownloadedAt, candidate.MediaOrigin).Scan(&eligible)

	return eligible, err
}
