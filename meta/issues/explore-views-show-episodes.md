# Make Explore views show episodes instead of a channel list

## Summary

The sidebar "Explore" section currently links each category (`Music`, `Gaming`, `Podcasts`, `Education`) to a plain full-text search (`/search?q=<label>`). That search spans videos, channels, and playlists and surfaces a list of channels for broad terms. Change Explore so each category view shows episodes (videos) — the latest/newest videos matching that category — rather than a channel list.

## Requirements

- Explore category views must render video cards (episodes) with playable/downloadable state, like the home/library video lists, not a channel list.
- Each category should resolve to a defined set of videos: at minimum match videos whose channel or video metadata matches the category term, ordered by recency (e.g. `published_at`/`archived_at` descending) so the view reads as "recent episodes".
- Keep the existing plain search behavior for the `/search` route; Explore should use its own endpoint (e.g. `GET /api/explore/{category}`) or a filtered search query (`?type=video`) rather than reusing the unfiltered search.
- Categories should be server- or config-defined rather than hardcoded in the Svelte sidebar if that makes the endpoint meaningful; otherwise keep the existing list but route it through the new endpoint.
- Empty categories render an empty-state with a clear message (no results imported yet).

## Acceptance Criteria

- Clicking an Explore category navigates to a view listing episode video cards for that category, ordered with newest first.
- The channel-list-shaped results currently returned by Explore are no longer shown for category views.
- Backend unit coverage for the Explore endpoint: category → matching videos, ordering, empty category, and unknown category behavior.
- Browser smoke or documented manual verification path covers clicking each Explore category and seeing episode cards.

## Notes

- Current implementation: `frontend/src/App.svelte` defines `sidebarExplore = ['Music', 'Gaming', 'Podcasts', 'Education']` and links each to `/search?q=<label>`; `GET /api/search` (`internal/server/server.go`, `searchDocuments`) returns mixed owner types (`videos`, `channels`, `playlists`) ordered by FTS rank.
- Design decision to confirm: whether Explore should match on the video's own fields, its channel's name/description, or both — and whether categories remain a fixed list or come from config (e.g. `KAPSEL_EXPLORE_CATEGORIES`).
