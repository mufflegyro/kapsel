# Split download worker responsibilities

## Summary

`internal/download/downloader.go` now contains yt-dlp execution, direct download ingestion, catalog sync, channel job handling, auto-download scheduling, retention cleanup, preview enqueueing, search/media denormalization, and job result writes. The package responsibilities are related but too dense for safe future changes.

## Requirements

- Split the downloader implementation into focused files or small package-internal components without changing behavior.
- Keep APIs simple and avoid adding a generic workflow framework.
- Preserve the composition root in `internal/app/app.go`.
- Do this after job lifecycle/result ownership issues are clearer, so the split does not preserve confusing boundaries.

## Acceptance Criteria

- yt-dlp command execution and retry/pacing helpers are separated from ingestion and catalog sync code.
- Download ingestion, channel jobs/catalog sync, retention, and scheduling are in separate focused files or components.
- Existing downloader tests continue to pass without large behavioral rewrites.
- New file boundaries are documented by names and small exported surface area.

## Notes

- Advisor priority: medium, after lifecycle cleanup.
- Relevant reference: `internal/download/downloader.go` overall.
- This is a follow-up to the archived `Split downloader domain responsibilities` issue, based on new job architecture review findings.

## Resolution

Implemented as a same-package file split — zero behavior change, zero new abstraction (no generic workflow framework). Sequenced after `clarify-scheduler-job-ownership.md` landed, so the split did not preserve confusing boundaries.

The extraction was done with an AST-driven script (`go/ast` decl inventory → bucket mapping → per-file write + `goimports`), handling grouped `const (...)`/`var (...)` specs by re-synthesizing the declaration keyword for moved specs and appending to pre-existing files. Two extraction hazards surfaced and were fixed: stale line numbers after an earlier gofmt pass, and grouped-const specs losing their wrapper when moved individually.

New layout (was a single 3324-line `downloader.go`):

- `downloader.go` (672) — Downloader core: Config, construction, shared plumbing; carries the package doc mapping all files.
- `ytdlp.go` (591) — yt-dlp execution, command building, pacing/retry, failure classification, self-update.
- `ingest.go` (648) — download ingestion: payload handling, metadata validation, persistence of videos/subtitles/thumbnails/search denormalization.
- `catalog.go` (724) — channel jobs, catalog sync, auto-download sync, channel upserts.
- `enqueue.go` (284) — public enqueue API and dedupe.
- `urls.go` (242) — URL normalization / YouTube URL helpers.
- `retention.go` — gained `HandleRetention`/`ApplyAutoDownloadRetention` + retention options/consts alongside the existing RetentionCleaner.
- `schedule.go` (364) — Ensure* scheduling policy (from the ownership issue; see `docs/scheduler.md`).
- `handlers.go` (171) — job-type dispatcher + shared job-result helpers.

Verification: `go build ./...` clean, `go vet` clean, full `go test ./...` — all 27 packages pass with no test changes. Status: **landed 2026-08-31**.
