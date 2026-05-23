# Batch search result hydration

## Summary

Search result hydration currently performs per-result queries and hardcodes entity table knowledge in the search package. This creates N+1 query behavior and couples FTS to downstream schemas.

## Requirements

- Group search results by owner type before hydration.
- Hydrate videos, comments, channels, and playlists with bounded batch queries.
- Keep snippets and result ordering intact.
- Keep FTS query behavior unchanged.

## Acceptance Criteria

- Hydration uses a bounded number of queries per owner type rather than per result.
- Search endpoint tests still pass and cover hydrated records.
- The search package has a clearer boundary between FTS matching and record hydration.

## Notes

- Review references: `internal/search/search.go:105`, `internal/search/search.go:138`, `internal/search/search.go:153`, `internal/search/search.go:192`, and `internal/search/search.go:198`.
- This is a performance and boundary cleanup, not a ranking change.
