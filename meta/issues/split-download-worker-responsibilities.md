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
