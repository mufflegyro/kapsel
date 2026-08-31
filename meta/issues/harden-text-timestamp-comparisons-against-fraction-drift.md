# Harden text timestamp comparisons against RFC3339Nano fraction drift

## Summary

Timestamps are stored as text and compared lexicographically across three formats and three table families. RFC3339Nano's trailing-zero stripping makes same-second lexicographic order diverge from numeric order (`…00.1Z` sorts above `…00.100000001Z`, because `'Z'` > digits). Today's production impact is bounded and accepted; any fix needs design attention because the formats are mixed, and the jobs-table rows are transient while catalog rows are permanent — so a jobs-only fix would half-apply.

## Root cause

- `internal/jobs/store.go` `timeText` (line ~1031) writes RFC3339Nano (variable fraction, zeros stripped) into `jobs.run_after`, `locked_at`, `created_at`, `updated_at`, `completed_at`. `Claim` filters `run_after <= nowText` and stale recovery compares `locked_at <= staleBeforeText` as text; `ORDER BY run_after` sorts the same text, so sort and filter stay mutually consistent.
- When two timestamps fall in the same second and the earlier one's printed fraction is a truncated prefix of the later one's, the string comparison flips. Observed in tests: `TestClaimFailsExhaustedStaleRunningJob` intermittently missed fresh jobs (test-side fixed in `2eca6c3` by sampling claim times on a one-second margin, `futureClaimTime()`).

## Accepted production impact (current behavior)

A lexicographic miss delays a job's pickup by at most one runner loop (`idleDelay` default 1s); the next `runLoopOnce` claims it. No loss, no permanent misorder, and stale recovery runs on a 15-minute window that sub-second quirks cannot touch. Accepted as of the 2026-08-30 discussion; this is the same "de-flake without changing semantics" boundary as `6cf2ffb` and `eaed716`.

## Sibling comparisons (why a jobs-only fix half-applies)

- `internal/download/retention.go` compares `watched_at <= cutoff` (candidate query and per-candidate recheck) where `watched_at` is a COALESCE of `user_progress.updated_at` / `videos.updated_at` / `videos.media_downloaded_at`, and the cutoffs are Go-side RFC3339Nano (`retention.go:58,64`).
- The `updated_at` columns default to SQLite's `strftime('%Y-%m-%dT%H:%M:%fZ','now')` — fixed 3-digit milliseconds (`migrations/001_initial.sql`) — but Go writers also land RFC3339Nano in the same columns (`taimport/importer.go:614,697,1019`). Cross-format pairs like `…00.100Z` vs `…00.1Z` can flip an individual comparison; within retention this is bounded well inside the 24h watched grace and currently errs on the conservative side. Documented as pre-existing in `docs/deployment.md` (Media Retention).

## Hardening options

1. **Fixed-width fractions everywhere.** SQLite's `strftime` tops out at milliseconds, so the canonical format would be 3-digit milliseconds uniformly (Go: `.Format("2006-01-02T15:04:05.000Z")`). Lexicographic order becomes numeric order for all pairs. Requires rewriting existing rows — `jobs` rows are transient and would self-heal, but `videos`/`user_progress` rows are permanent, and mixed old/new rows create the symmetric old-vs-new misorder unless the migration rewrites history. Also changes the timestamp strings surfaced in API responses.
2. **Value-based comparison.** Switch filters/sorts to `julianday()`/`unixepoch()` expressions — correct across all formats, no data rewrite — but expression-wrapped columns lose index use unless expression indexes or generated columns are added, and every comparison site must be converted. A custom SQLite collation registered at connection setup is a variant of this (comparison-time normalization, zero data changes) if `modernc.org/sqlite` supports it — verify before designing around it.

## Non-goals

- Do not change the test-side `futureClaimTime()` convention; it is the accepted dodge for the test cliff.
- Do not "fix" one table family only: any change must cover the jobs table and the retention comparison family together or not at all.

## Resolution (2026-08-31)

Went with hardening option 2's **custom collation variant**: `modernc.org/sqlite` v1.50.0 supports `RegisterCollationUtf8` (verified — driver-level, process-lifetime, applies to every connection opened after registration, and `internal/database`'s package init runs before any `sql.Open`), so the fix is comparison-time normalization with zero data rewrites and no API-visible format changes (option 1's two blockers).

- New `internal/database/collation.go`: registers the `RFC3339_NANO` collation at package init. The comparator parses both sides with `time.Parse(time.RFC3339Nano, ...)` (accepts Z, offsets, and any fraction width) and falls back to `strings.Compare` for non-timestamp text, preserving BINARY order for the `''` COALESCE fallbacks.
- Every comparison site opted in via `COLLATE RFC3339_NANO`: jobs `Claim` (filter, stale-recovery re-check under the tx, and `ORDER BY run_after, created_at`), the three stale-recovery updates, and the retention candidate/recheck queries (`watched_at <= cutoff`, `downloaded_at <= cutoff`, the channel-rank window `ORDER BY COALESCE(...)`, and `ORDER BY downloaded_at`). Expression-level COLLATE loses index use on those terms, but the jobs table is transient/small and the retention queries are already filtered scans.
- Regression tests: `internal/database/collation_test.go` (comparator pairs + collated filter/sort over mixed fractions), `TestClaimOrdersSameSecondFractionalRunAfter` (claim must pick the numerically earlier fraction; BINARY picks the later one), and `TestAutoDownloadRetentionKeepsWatchedMediaNewerThanCutoffByFraction` (watched 100ns after the cutoff must be retained; BINARY removed it).
- `futureClaimTime()` stays as-is per the non-goals. The collation also surfaced a latent test-side reliance on the old quirk in `internal/updater`: `TestEnsureReleaseCheckJobs*` sampled a truncated-second claim time before enqueueing, which only worked because BINARY order made a fraction-carrying `run_after` claimable (`.123…Z` < `Z`). Both tests now claim with the live clock sampled after the enqueue. Status: **landed 2026-08-31**.

## References

- `2eca6c3` — test-side fix and `futureClaimTime` doc comment (root-cause write-up)
- `6cf2ffb`, `eaed716` — prior timing-cliff de-flaking philosophy
- `internal/jobs/store.go` (`timeText`, `Claim`), `internal/download/retention.go` (watched cutoffs), `internal/database/migrations/001_initial.sql` (strftime defaults)
