# Add hide watched checkbox to the sort toolbar

## Summary

The sort toolbar (the "Sort by" dropdown) on the home/library and channel pages should offer a "Hide watched" checkbox. When enabled, videos already marked watched disappear from the sorted feed in every sort mode, not just For You.

## Requirements

- Add a "Hide watched" checkbox next to the "Sort by" select in `VideoSortToolbar.svelte`, rendered only when the parent wires the new props (home and channel instances).
- When enabled, watched videos are excluded server-side via a `hide_watched=1` query parameter on `GET /api/home/videos` and `GET /api/channels/{id}/videos`. Client-side post-fetch filtering is explicitly out: both endpoints are server-sorted and paginated (COUNT + LIMIT/OFFSET), so filtering after fetch would leave sparse pages and wrong pagination totals.
- Reuse the existing `homeUnwatchedVideoFilter()` SQL condition (`v.watched = 0 AND NOT EXISTS (... user_progress.watched = 1)`) in `listVideos`, `listHomeVideos`, and `listChannelVideos` when the parameter is present, so pagination counts stay correct.
- Persist the choice the same way sort is persisted: a `?hide_watched=1` URL param, plus localStorage stickiness for the home page (mirroring `homeVideoSortStorageKey`).
- Default the checkbox to off everywhere; the home default sort remains For You.
- Leave the checkbox visible with the For You sort selected; that sort already excludes watched videos, so the combination is a harmless no-op rather than special-casing the UI.

## Acceptance Criteria

- Backend regression coverage: with `hide_watched=1`, videos with `videos.watched = 1` or `user_progress.watched = 1` are absent from both endpoints, and the reported pagination total excludes them.
- With the parameter absent, responses are byte-for-byte identical to today's behavior (no default change).
- The checkbox state survives a reload on the home page and round-trips through the URL like `sort` does.
- Toggling the checkbox refetches the list from page 1 on both home and channel views.
- `go test ./...`, `pnpm check`, and `pnpm browser-smoke` pass.

## Notes

- Scoping findings (2026-08-30):
  - The toolbar lives in `frontend/src/components/VideoSortToolbar.svelte` and is used in exactly two places (`App.svelte` library + channel instances).
  - Watched state is dual-tracked: the `videos.watched` column and per-user `user_progress.watched` rows; the existing filter snippet already covers both.
  - `playbackProgressInvalidation` already triggers a silent list reload after progress updates, so a video watched during playback disappears from a filtered list on return — that is the desired behavior for this feature, no extra wiring needed.
- Out of scope: up-next filtering on the watch page, playlist views (no sort toolbar there), search results, the `/api/videos/{id}` detail response, and any changes to which sort is the default.
- Estimated effort: small — roughly 15 lines in `internal/server/server.go` plus handler tests, ~40 lines across the toolbar component and `App.svelte` wiring. Fits a single focused session.
- Implemented 2026-08-30 (pending user sign-off): `hide_watched` accepts `1`/`true`/`yes` via `hideWatchedParam()`; home sticky preference stored under `kapsel.homeHideWatched`. Verified with new `TestVideoListEndpointHideWatchedFilter`, `go test ./...`, `pnpm check`, and `pnpm browser-smoke` on port 18099 (`KAPSEL_E2E_PORT`).
- Pre-existing failure note: `catalog download success snapshots refresh route data once` fails on a clean worktree at 1a9d4ec (4/4 runs), so it is unrelated to this feature and needs separate investigation — filed as `fix-flaky-catalog-download-success-refresh-e2e-test.md`.
- Review follow-ups addressed 2026-08-30: the regression-test loop now covers `/api/videos` (non-home) as well; a channel-page e2e case exercises the channel toolbar toggle; frontend `hideWatchedFromSearch()` accepts the same truthy spellings as the backend (`1`/`true`/`yes`).
