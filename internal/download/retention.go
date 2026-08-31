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
	"kapsel/internal/jobs"
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
	IncludeManual bool
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
	candidates, err := c.retentionCandidates(ctx, staleCutoff, watchedCutoff, limit, options.IncludeManual)
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
// branch additionally removes unstarted auto media beyond the newest 2 per
// channel. With IncludeManual, manually downloaded media joins the stale
// branch: channel-bound manual downloads rank alongside auto-downloads (newest
// 2 per channel kept), and channel-less manual downloads are eligible once
// unstarted and stale. Imported media never join the stale branch.
func (c *RetentionCleaner) retentionCandidates(ctx context.Context, staleCutoff string, watchedCutoff string, limit int, includeManual bool) ([]retentionCandidate, error) {
	eligibilityCTEs, selection, staleOrigins, manualStaleBind := retentionEligibilityQuery(includeManual)
	args := []any{}
	for _, origin := range staleOrigins {
		args = append(args, origin)
	}
	args = append(args, watchedCutoff, staleCutoff)
	if manualStaleBind {
		args = append(args, staleCutoff)
	}
	args = append(args, limit)
	rows, err := c.db.QueryContext(ctx, `
WITH `+eligibilityCTEs+`
SELECT id, media_path, downloaded_at, media_origin
FROM (
`+selection+`)
ORDER BY downloaded_at COLLATE RFC3339_NANO ASC, id ASC
LIMIT ?`, args...)
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
		candidate.IncludeManual = includeManual
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return candidates, nil
}

// retentionEligibilityQuery renders the SQL fragments shared by the candidate
// scan and the transactional recheck, so the two eligibility rules cannot
// drift. It returns the WITH-list CTEs and the UNION selection of the watched
// and stale branches; callers wrap the selection with their own projection.
// includeManual widens stale-branch eligibility to manually downloaded media
// (imported media never join); see RetentionOptions.IncludeManual. origins
// are the media origins the rendered query binds, in order; manualStaleBind
// reports whether the rendered selection consumes one extra stale-cutoff bind
// for the channel-less manual arm.
func retentionEligibilityQuery(includeManual bool) (eligibilityCTEs string, selection string, origins []string, manualStaleBind bool) {
	origins = []string{DownloadOriginChannelAuto}
	manualUnrankedCTE := ""
	manualStaleArm := ""
	manualStaleBind = false
	if includeManual {
		origins = append(origins, DownloadOriginManual)
		manualUnrankedCTE = `,
manual_unranked AS (
  SELECT
    v.id,
    v.media_path,
    v.media_downloaded_at AS downloaded_at,
    v.media_origin AS media_origin,
    COALESCE(up.position_seconds, 0) AS position_seconds,
    COALESCE(up.watched, 0) AS progress_watched,
    v.watched AS video_watched,
    v.keep_forever AS keep_forever
  FROM videos v
  LEFT JOIN user_progress up ON up.video_id = v.id
  WHERE v.channel_id IS NULL
    AND v.media_path <> ''
    AND v.media_origin = '` + DownloadOriginManual + `'
    AND v.media_downloaded_at <> ''
)`
		manualStaleArm = `
  UNION
  SELECT id, media_path, downloaded_at, media_origin
  FROM manual_unranked
  WHERE keep_forever = 0
    AND position_seconds = 0
    AND progress_watched = 0
    AND video_watched = 0
    AND downloaded_at COLLATE RFC3339_NANO <= ?`
		manualStaleBind = true
	}
	originPlaceholders := make([]string, len(origins))
	for i := range originPlaceholders {
		originPlaceholders[i] = "?"
	}
	eligibilityCTEs = `ranked_auto_downloads AS (
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
      ORDER BY COALESCE(NULLIF(v.published_at, ''), NULLIF(v.archived_at, ''), v.updated_at, v.created_at, '') COLLATE RFC3339_NANO DESC, v.id DESC
    ) AS channel_download_rank
  FROM videos v
  LEFT JOIN user_progress up ON up.video_id = v.id
  WHERE v.channel_id IS NOT NULL
    AND v.media_path <> ''
    AND v.media_origin IN (` + strings.Join(originPlaceholders, ", ") + `)
    AND v.media_downloaded_at <> ''
)` + manualUnrankedCTE + `,
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
)`
	selection = `  SELECT id, media_path, downloaded_at, media_origin
  FROM watched_with_media
  WHERE watched_at <> '' AND watched_at COLLATE RFC3339_NANO <= ?
  UNION
  SELECT id, media_path, downloaded_at, media_origin
  FROM ranked_auto_downloads
  WHERE keep_forever = 0
    AND channel_download_rank > 2
    AND position_seconds = 0
    AND progress_watched = 0
    AND video_watched = 0
    AND downloaded_at COLLATE RFC3339_NANO <= ?` + manualStaleArm
	return eligibilityCTEs, selection, origins, manualStaleBind
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
	eligibilityCTEs, selection, origins, manualStaleBind := retentionEligibilityQuery(candidate.IncludeManual)
	args := []any{}
	for _, origin := range origins {
		args = append(args, origin)
	}
	args = append(args, candidate.WatchedCutoff, candidate.StaleCutoff)
	if manualStaleBind {
		args = append(args, candidate.StaleCutoff)
	}
	args = append(args, candidate.VideoID, candidate.MediaPath, candidate.DownloadedAt, candidate.MediaOrigin)
	err := tx.QueryRowContext(ctx, `
WITH `+eligibilityCTEs+`
SELECT EXISTS(
  SELECT 1
  FROM (
`+selection+`
  )
  WHERE id = ?
    AND media_path = ?
    AND downloaded_at = ?
    AND media_origin = ?
)`, args...).Scan(&eligible)

	return eligible, err
}

const DefaultChannelAutoDownloadLimit = 2

const DefaultRetentionLimit = 100

const DefaultRetentionStaleAfter = 14 * 24 * time.Hour

const DefaultRetentionWatchedAfter = 24 * time.Hour

type RetentionOptions struct {
	Now          func() time.Time
	StaleAfter   time.Duration
	WatchedAfter time.Duration
	// WatchedCleanupDisabled turns off removal of watched media entirely;
	// only the stale channel-auto rule then applies.
	WatchedCleanupDisabled bool
	// IncludeManual opts manually downloaded media into the stale rule:
	// channel-bound manual downloads rank alongside auto-downloads (newest 2
	// per channel kept) and channel-less manual downloads become eligible
	// once unstarted and past the stale cutoff. Imported media never join
	// the stale branch, and watched-media cleanup is unaffected (it already
	// covers every origin).
	IncludeManual bool
	Limit         int
}

type RetentionResult struct {
	Checked         int      `json:"checked"`
	Removed         int      `json:"removed"`
	RemovedVideoIDs []string `json:"removed_video_ids,omitempty"`
}

func (d *Downloader) HandleRetention(ctx context.Context, job jobs.Job) error {
	if d.db == nil {
		return errors.New("retention handler missing database")
	}
	if err := d.requireJobStoreForJob(job); err != nil {
		return err
	}
	options := RetentionOptions{}
	result, err := d.ApplyAutoDownloadRetention(ctx, options)
	if err != nil {
		if result.Checked > 0 || result.Removed > 0 {
			_ = d.setPartialJobResult(ctx, job.ID, result)
		}
		return err
	}

	return d.setJobResult(ctx, job.ID, result)
}

func (d *Downloader) ApplyAutoDownloadRetention(ctx context.Context, options RetentionOptions) (RetentionResult, error) {
	// The operator-level opt-out cannot be re-enabled per call.
	options.WatchedCleanupDisabled = options.WatchedCleanupDisabled || d.config.RetentionWatchedCleanupDisabled
	// The operator-level opt-in cannot be revoked per call.
	options.IncludeManual = options.IncludeManual || d.config.RetentionIncludeManual
	return NewRetentionCleaner(d.db, d.config.MediaRoot).Apply(ctx, options)
}

func downloadOrigin(value string) string {
	switch strings.TrimSpace(value) {
	case DownloadOriginChannelAuto:
		return DownloadOriginChannelAuto
	default:
		return DownloadOriginManual
	}
}
