# Share video list query shape

## Summary

Video list endpoints duplicate a large SELECT/JOIN projection and then share one scanner. This creates schema drift risk whenever list responses gain or lose fields.

## Requirements

- Extract a shared video list SELECT projection and JOIN clause.
- Keep endpoint-specific WHERE, ORDER BY, and LIMIT logic explicit.
- Preserve existing `videoListItem` JSON output.

## Acceptance Criteria

- `listVideos`, `listUpNextVideos`, and `listPlaylistVideos` use the same projection source.
- Tests cover that list endpoints still return the expected common fields.
- Existing server tests pass.

## Notes

- Review references: `internal/server/server.go:1548`, `internal/server/server.go:1623`, `internal/server/server.go:1697`, and `internal/server/server.go:2214`.
- This should be a low-behavior-change refactor before adding more video list fields.
