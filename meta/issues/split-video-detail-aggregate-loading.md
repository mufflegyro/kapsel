# Split video detail aggregate loading

## Summary

The video detail endpoint aggregates video metadata, channel info, progress, media URLs, active jobs, timeline previews, subtitles, and SponsorBlock segments in one request. Some of these dependencies can delay or destabilize the watch page even when core playback data is available.

## Requirements

- Keep core video detail loading focused on metadata, playback media, channel, and progress.
- Move optional or side-effecting concerns behind helper boundaries or separate endpoints.
- Avoid blocking core video detail on external SponsorBlock fetches.
- Keep active job lookup behavior covered while extracting it from the main handler.

## Acceptance Criteria

- SponsorBlock loading no longer blocks the core video detail response, or is explicitly isolated with a short non-fatal path.
- Active download/preview job lookup is delegated to a focused helper or service.
- Existing watch-page smoke tests continue to pass.

## Notes

- Review references: `internal/server/server.go:2469`, `internal/server/server.go:2554`, `internal/server/server.go:2571`, `internal/server/server.go:2577`, `internal/server/server.go:2603`, and `internal/server/server.go:2635`.
- A natural first step is a dedicated `/api/videos/{id}/sponsor-segments` endpoint or a background fetch path.
