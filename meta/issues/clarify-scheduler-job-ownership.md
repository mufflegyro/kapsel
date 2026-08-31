# Clarify scheduler job ownership

## Summary

Scheduler loops live in `app.go`, while scheduling policy and job-table checks live inside the download package. This is acceptable today, but ownership is unclear and some scheduler queries bypass `jobs.Store` abstractions.

## Requirements

- Define which layer owns scheduler loops and which layer owns scheduling policy.
- Keep durable scheduled work represented as jobs rather than inline background work.
- Route scheduler job-table checks through `jobs.Store` or documented store helpers where practical.
- Avoid moving to an external scheduler.

## Acceptance Criteria

- Channel auto-download and retention scheduling responsibilities are documented or separated into focused helpers.
- Scheduler active-job checks use shared store methods where practical.
- Retention scheduling has clear behavior after failures, including whether backoff is needed.
- Tests cover scheduler dedupe and failure/backoff behavior.

## Notes

- Advisor priority: low to medium.
- Relevant references: `internal/app/app.go:117-153`, `internal/download/downloader.go:413-528`, and `internal/download/downloader.go:1567-1689`.

## Resolution (2026-08-31)

Landed as a three-layer ownership model, documented in `docs/scheduler.md`:

- **Composition root (`internal/app`)** owns cadence only: the four scheduler
  loops collapsed into one `runPeriodicScheduler` helper (ticker + error log +
  ctx lifecycle); loops never touch the job table and never run domain work
  inline.
- **Scheduling policy** moved to a focused `internal/download/schedule.go`
  (`Ensure*` functions + pacing helpers + a shared `scheduledJobDue` core).
  Retention and yt-dlp-update Ensure functions no longer take a raw `*sql.DB`
  — retention and yt-dlp updates need no database handle at all.
- **`jobs.Store`** owns the job table: new documented introspection helpers
  `HasActiveJobByType` and `LatestJobOfType` (created_at ranking now uses the
  `RFC3339_NANO` collation). The updater's `EnsureReleaseCheckJobs` also routes
  through them; its exponential failure backoff is unchanged.
- **Retention failure behavior decided and documented** (no backoff): a failed
  retention job re-arms at the next hourly tick — the pass is local, idempotent,
  and bounded, and a persistent failure stays visible as a failed job.
  Release checks keep their existing backoff (rate-limited external API).
- **Tests added**: retention throttle-after-success, retention re-arm after
  failure, store introspection helpers (active/latest semantics incl.
  cancel-requested exclusion and fraction-ordering), on top of the existing
  channel-auto dedupe and updater backoff tests.
