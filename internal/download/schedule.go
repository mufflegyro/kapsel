// This file owns scheduling policy for the download package: the Ensure*
// functions that decide whether a scheduled job of a given type should exist
// right now, and the enqueue helpers they build on.
//
// Ownership boundaries (see docs/scheduler.md):
//
//   - The composition root (internal/app) owns only cadence: one ticker loop
//     per scheduler family, which calls the Ensure* policy once per tick and
//     logs failures. It never queries the job table and never runs domain work
//     inline — durable scheduled work is always represented as jobs.
//   - The Ensure* functions here own policy: dedupe against active jobs,
//     interval throttling, failure handling, and run_after computation
//     (including jitter). They read the job table only through jobs.Store
//     methods, never with raw SQL.
//   - jobs.Store owns the job table itself: job lifecycle, dedupe lookups, and
//     the shared scheduling introspection helpers (HasActiveJobByType,
//     LatestJobOfType). It holds no scheduling policy.

package download

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"kapsel/internal/jobs"
)

const DefaultChannelAutoSyncInterval = 24 * time.Hour

const DefaultRetentionInterval = 24 * time.Hour

const DefaultYTDLPUpdateInterval = 24 * time.Hour

// ChannelAutoScheduleOptions configures EnsureChannelAutoDownloadJobs. Interval
// defaults to DefaultChannelAutoSyncInterval; Jitter defaults to a random
// duration over the interval.
type ChannelAutoScheduleOptions struct {
	Now      func() time.Time
	Interval time.Duration
	Jitter   func(time.Duration) time.Duration
}

// RetentionScheduleOptions configures EnsureRetentionJobs. Interval defaults to
// DefaultRetentionInterval.
type RetentionScheduleOptions struct {
	Now      func() time.Time
	Interval time.Duration
}

type YTDLPUpdateScheduleOptions struct {
	Now      func() time.Time
	Interval time.Duration
}

// scheduledJobDue reports whether a fresh periodic job of jobType should be
// enqueued at now. It is the shared policy core for the tick-driven
// schedulers: a job is due when no active (queued/running, not
// cancel-requested) job of the type exists and the latest job either failed or
// is older than the throttle interval.
//
// A failed job re-arms immediately: the scheduler's next tick is the retry,
// with no exponential backoff, because these jobs are local, idempotent, and
// bounded, and a persistent failure stays visible as a failed job in the jobs
// UI. The updater's release-check scheduler is the exception — it backs off
// failures because it calls a rate-limited external API (see
// internal/updater).
func scheduledJobDue(ctx context.Context, store *jobs.Store, jobType string, interval time.Duration, now time.Time) (bool, error) {
	if interval <= 0 {
		interval = DefaultRetentionInterval
	}
	active, err := store.HasActiveJobByType(ctx, jobType)
	if err != nil {
		return false, err
	}
	if active {
		return false, nil
	}
	latest, found, err := store.LatestJobOfType(ctx, jobType)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil
	}
	if latest.Status != jobs.StatusFailed {
		createdAt, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(latest.CreatedAt))
		if parseErr == nil && now.Sub(createdAt.UTC()) < interval {
			return false, nil
		}
	}

	return true, nil
}

// EnsureChannelAutoDownloadJobs enqueues the next channel_auto_download job for
// every subscribed channel that is due. Cadence lives with the composition
// root (a ticker loop calls this once per tick); the policy — per-channel
// dedupe against active auto jobs, run_after computed from last_scanned_at
// plus interval and jitter — lives here.
func EnsureChannelAutoDownloadJobs(ctx context.Context, db *sql.DB, store *jobs.Store, options ChannelAutoScheduleOptions) (int, error) {
	if db == nil {
		return 0, errors.New("channel auto scheduler missing database")
	}
	if store == nil {
		return 0, errors.New("channel auto scheduler missing job store")
	}
	nowFunc := options.Now
	if nowFunc == nil {
		nowFunc = func() time.Time { return time.Now().UTC() }
	}
	interval := options.Interval
	if interval <= 0 {
		interval = DefaultChannelAutoSyncInterval
	}
	jitter := options.Jitter
	if jitter == nil {
		jitter = randomDelay
	}

	channels, err := subscribedChannels(ctx, db)
	if err != nil {
		return 0, err
	}
	created := 0
	now := nowFunc().UTC()
	for _, channel := range channels {
		channelURL, err := channelURLFromExternalID(channel.ExternalID)
		if err != nil {
			continue
		}
		_, wasCreated, err := EnqueueChannelAutoDownload(ctx, store, ChannelAutoDownloadPayload{URL: channelURL, ChannelID: channel.ID}, channel.LastScannedAt, nextChannelAutoRun(now, channel.LastScannedAt, interval, jitter))
		if err != nil {
			return created, err
		}
		if wasCreated {
			created++
		}
	}

	return created, nil
}

// EnsureRetentionJobs enqueues an hourly-bounded retention cleanup job when
// one is due: no active retention job exists and the latest one succeeded
// longer ago than the interval, or the latest attempt failed (failures re-arm
// immediately; see scheduledJobDue). The retention job itself is created
// through EnqueueRetentionCleanup and remains observable in the jobs UI.
func EnsureRetentionJobs(ctx context.Context, store *jobs.Store, options RetentionScheduleOptions) (int, error) {
	if store == nil {
		return 0, errors.New("retention scheduler missing job store")
	}
	nowFunc := options.Now
	if nowFunc == nil {
		nowFunc = func() time.Time { return time.Now().UTC() }
	}
	interval := options.Interval
	if interval <= 0 {
		interval = DefaultRetentionInterval
	}
	now := nowFunc().UTC()

	due, err := scheduledJobDue(ctx, store, RetentionJobType, interval, now)
	if err != nil || !due {
		return 0, err
	}

	_, created, err := EnqueueRetentionCleanup(ctx, store, now)
	if err != nil {
		return 0, err
	}
	if !created {
		return 0, nil
	}

	return 1, nil
}

// EnsureYTDLPUpdateJobs enqueues a yt-dlp self-update job when one is due,
// with the same dedupe/throttle/failure policy as retention (see
// scheduledJobDue).
func EnsureYTDLPUpdateJobs(ctx context.Context, store *jobs.Store, options YTDLPUpdateScheduleOptions) (int, error) {
	if store == nil {
		return 0, errors.New("yt-dlp update scheduler missing job store")
	}
	nowFunc := options.Now
	if nowFunc == nil {
		nowFunc = func() time.Time { return time.Now().UTC() }
	}
	interval := options.Interval
	if interval <= 0 {
		interval = DefaultYTDLPUpdateInterval
	}
	now := nowFunc().UTC()

	due, err := scheduledJobDue(ctx, store, YTDLPUpdateJobType, interval, now)
	if err != nil || !due {
		return 0, err
	}

	_, created, err := EnqueueYTDLPUpdate(ctx, store, now)
	if err != nil {
		return 0, err
	}
	if !created {
		return 0, nil
	}

	return 1, nil
}

// EnqueueChannelAutoDownload enqueues a channel auto-download job for a
// channel, deduplicated against the channel's current active auto job: an
// active job counts as current unless the channel has been scanned after the
// job was created.
func EnqueueChannelAutoDownload(ctx context.Context, store *jobs.Store, payload ChannelAutoDownloadPayload, lastScannedAt string, runAfter time.Time) (jobs.Job, bool, error) {
	if store == nil {
		return jobs.Job{}, false, errors.New("channel auto scheduler missing job store")
	}
	payload.ChannelID = strings.TrimSpace(payload.ChannelID)
	if payload.ChannelID == "" {
		return jobs.Job{}, false, errors.New("channel auto-download payload missing channel id")
	}
	channelURL, err := NormalizeChannelURL(payload.URL)
	if err != nil {
		return jobs.Job{}, false, err
	}
	payload.URL = channelURL
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return jobs.Job{}, false, err
	}

	return store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: ChannelAutoDownloadJobType, PayloadJSON: string(payloadJSON), MaxAttempts: 1, RunAfter: runAfter}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return activeChannelAutoDownloadJob(ctx, store, tx, payload.ChannelID, lastScannedAt)
	})
}

// EnqueueRetentionCleanup enqueues the periodic retention cleanup job with an
// empty payload, deduplicated against active retention jobs.
func EnqueueRetentionCleanup(ctx context.Context, store *jobs.Store, runAfter time.Time) (jobs.Job, bool, error) {
	if store == nil {
		return jobs.Job{}, false, errors.New("retention scheduler missing job store")
	}

	return store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: RetentionJobType, PayloadJSON: `{}`, MaxAttempts: 1, RunAfter: runAfter}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return store.ActiveByPayloadWithoutCancelRequestedTx(ctx, tx, RetentionJobType, `{}`)
	})
}

// EnqueueYTDLPUpdate enqueues a yt-dlp self-update job, deduplicated against
// active update jobs.
func EnqueueYTDLPUpdate(ctx context.Context, store *jobs.Store, runAfter time.Time) (jobs.Job, bool, error) {
	if store == nil {
		return jobs.Job{}, false, errors.New("yt-dlp update scheduler missing job store")
	}

	return store.FindOrEnqueue(ctx, jobs.EnqueueParams{Type: YTDLPUpdateJobType, PayloadJSON: `{}`, MaxAttempts: 1, RunAfter: runAfter}, func(ctx context.Context, tx *sql.Tx) (jobs.Job, bool, error) {
		return store.ActiveByPayloadWithoutCancelRequestedTx(ctx, tx, YTDLPUpdateJobType, `{}`)
	})
}

// nextChannelAutoRun computes when a channel's next auto-download scan should
// run: one interval (plus jitter) after the last scan, rounded up to the next
// interval boundary so unsubscribed re-subscriptions do not all fire at once.
func nextChannelAutoRun(now time.Time, lastScannedAt string, interval time.Duration, jitter func(time.Duration) time.Duration) time.Time {
	if interval <= 0 {
		interval = DefaultChannelAutoSyncInterval
	}
	if jitter == nil {
		jitter = randomDelay
	}
	now = now.UTC()
	if scannedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(lastScannedAt)); err == nil {
		runAfter := scannedAt.UTC().Truncate(interval).Add(interval).Add(jitter(interval)).UTC()
		for !runAfter.After(now) {
			runAfter = runAfter.Add(interval)
		}
		return runAfter
	}

	return now.Add(jitter(interval)).UTC()
}

type autoDownloadChannel struct {
	ID            string
	ExternalID    string
	LastScannedAt string
}

// subscribedChannels lists channels with auto-download enabled. This reads the
// channels table — domain data owned by the download package — not the job
// table, so plain SQL is allowed here.
func subscribedChannels(ctx context.Context, db *sql.DB) ([]autoDownloadChannel, error) {
	rows, err := db.QueryContext(ctx, "SELECT id, external_id, COALESCE(last_scanned_at, '') FROM channels WHERE subscribed = 1 ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	channels := []autoDownloadChannel{}
	for rows.Next() {
		var channel autoDownloadChannel
		if err := rows.Scan(&channel.ID, &channel.ExternalID, &channel.LastScannedAt); err != nil {
			return nil, err
		}
		channels = append(channels, channel)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return channels, nil
}

type activeChannelAutoJob struct {
	CreatedAt string
}

// hasCurrentChannelAutoJob reports whether an active auto-download job still
// covers lastScannedAt: true while the job is the freshest state for the
// channel (scan not newer than the job), or when timestamps cannot be parsed
// (fail closed: keep deduping until a newer scan proves the job stale).
func hasCurrentChannelAutoJob(active []activeChannelAutoJob, lastScannedAt string) bool {
	if len(active) == 0 {
		return false
	}
	lastScanned, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(lastScannedAt))
	if err != nil {
		return true
	}
	for _, job := range active {
		createdAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(job.CreatedAt))
		if err != nil || !lastScanned.After(createdAt) {
			return true
		}
	}

	return false
}

func activeChannelAutoDownloadJob(ctx context.Context, store *jobs.Store, tx *sql.Tx, channelID string, lastScannedAt string) (jobs.Job, bool, error) {
	activeJobs, err := store.ActiveByTypeWithoutCancelRequestedTx(ctx, tx, ChannelAutoDownloadJobType, jobs.MaxActiveLookupLimit)
	if err != nil {
		return jobs.Job{}, false, err
	}
	for _, job := range activeJobs {
		var payload ChannelAutoDownloadPayload
		if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
			continue
		}
		if strings.TrimSpace(payload.ChannelID) != channelID {
			continue
		}
		if hasCurrentChannelAutoJob([]activeChannelAutoJob{{CreatedAt: job.CreatedAt}}, lastScannedAt) {
			return job, true, nil
		}
	}

	return jobs.Job{}, false, nil
}
