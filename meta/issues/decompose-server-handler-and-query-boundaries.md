# Decompose server handler and query boundaries

## Summary

`internal/server/server.go` mixes route registration, handlers, SQL construction, DTOs, media URL signing, diagnostics, VTT generation, active job lookup, and SponsorBlock loading. The file has become a central coupling point for unrelated domains.

## Requirements

- Keep `server.go` as a thin route registration and middleware hub.
- Extract video, channel, playlist, job, media, and diagnostics handlers incrementally.
- Move reusable query construction out of individual handlers.
- Preserve existing API behavior during each extraction.

## Acceptance Criteria

- At least one cohesive handler group is moved out of `server.go`.
- Extracted handlers continue to use existing auth and configuration wiring.
- Existing server tests pass after extraction.
- No endpoint behavior changes unless covered by a focused issue.

## Notes

- Review references: `internal/server/server.go:188`, `internal/server/server.go:1548`, `internal/server/server.go:1623`, `internal/server/server.go:2214`, `internal/server/server.go:2469`, and `internal/server/server.go:2635`.
- The video handlers are the highest-leverage first extraction because they include listing, detail, progress, Up next, subtitles, chapters, and media URL concerns.
