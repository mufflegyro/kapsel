# Split video list endpoints by route

## Summary

The generic `/api/videos` endpoint currently serves home feed, channel list, playlist-like filtering, and generic library use cases through query flags. This makes page-specific behavior depend on parameter combinations instead of explicit route intent.

## Requirements

- Add route-specific video list endpoints for home and channel video lists.
- Keep each endpoint paginated and bounded.
- Preserve current response shape while callers migrate.
- Share lower-level query helpers instead of duplicating SQL.

## Acceptance Criteria

- Home feed no longer depends on `/api/videos?home=1`.
- Channel pages no longer depend on `/api/videos?channel=...`.
- Existing `/api/videos` behavior remains available until callers are migrated or explicitly deprecated.
- Backend tests cover home and channel list ordering through their dedicated endpoints.

## Notes

- Review references: `internal/server/server.go:1548`, `internal/server/server.go:1798`, `frontend/src/App.svelte:278`, and `frontend/src/App.svelte:405`.
- This follows the same lesson as the Up next fix: page-specific data should have a page-specific API boundary.
