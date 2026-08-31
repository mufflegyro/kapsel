# Manage sidebar Explore links: create from searches, remove existing ones

> **Superseded 2026-08-31** by `explore-menu-link-editor.md` — the pop-up
> editor (add + edit + remove from the menu) covers this issue's scope and
> adds renaming; the search-page "Save to Explore" button it proposed is an
> optional extension there. Kept for reference; see the new issue for the
> current plan.

## Summary

The sidebar "Explore" section currently renders a hardcoded list (`Music`, `Gaming`, `Podcasts`, `Education`) defined in `frontend/src/App.svelte`, where every entry is a shortcut to a plain search (`/search?q=<label>`). Make the list user-manageable in both directions: a search result page can save the current query as a new Explore link, and any Explore link (default or user-added) can be removed from the sidebar. The list is a per-browser UI preference, so it persists in localStorage under a `kapsel.*` key like the existing sticky UI choices.

## Requirements

- **Create from searches**: on the `/search` route, when a non-empty query is present, show a "Save to Explore" action next to the page heading. Clicking it appends the trimmed query as a new Explore link (`label` = query text, `href` = `/search?q=<encodeURIComponent(query)>`). Button feedback: on save, flip to a "Saved to Explore" state; keep deduplicating so the same query can't be saved twice.
- **Remove existing ones**: each Explore link in the sidebar gets a remove control (hover/focus-visible affordance, `aria-label="Remove <label> from Explore"`) that deletes it from the list and persists. This applies to the four default links as well as user-added ones.
- **Persistence and defaults**: store the JSON list under a new localStorage key (e.g. `kapsel.exploreLinks`, mirroring `kapsel.homeVideoSort` / `kapsel.homeHideWatched`). With nothing stored, seed with the current four defaults so a fresh profile is unchanged; the first user modification persists the resulting list. The existing smoke assertion (`home browse chrome shows feed position, aligned titles, and quieter explore links`, expects `Music` in `.side-section a`) must keep passing on a fresh profile.
- **Empty state**: when every link has been removed, render a small hint in the Explore section (e.g. "Search the archive and save a query to pin it here") instead of an empty section.
- **Layout integrity**: the remove control must not break the icon-only collapse at `max-width: 1180px` (labels hidden, links centered) — hide the remove control in that mode. Long saved queries should clamp with ellipsis like other sidebar labels rather than pushing the layout.
- Scope: frontend-only (`App.svelte` + `frontend/src/style.css`). No backend or API changes — Explore links remain client-side shortcuts to the existing `GET /api/search`.

## Acceptance Criteria

- Searching from the topbar and clicking "Save to Explore" adds the query to the sidebar Explore section immediately; clicking the new link navigates to `/search?q=<query>` and shows the same results.
- Saving the same query twice is a no-op and the button reads as already saved.
- Removing a link (default or added) removes it from the sidebar and the change survives a full reload; clearing the localStorage key restores the four defaults.
- A fresh browser profile still shows `Music`/`Gaming`/`Podcasts`/`Education` and the existing e2e smoke checks stay green.
- Removing all links shows the empty-state hint.
- `pnpm check` and `pnpm browser-smoke` pass.

## Related

- `explore-views-show-episodes.md` — proposes turning Explore categories into server-defined episode views. These two issues touch the same hardcoded `sidebarExplore` list; if categories become server/config-defined, user-saved links should merge alongside (or override) them rather than being replaced, and the removal affordance applies to both kinds.
- `make-search-results-episode-first.md` — **prerequisite**: Explore links target plain `/search?q=` shortcuts, so this issue only delivers its full value once search results display episodes with thumbnails as the primary view (channel/playlist matches secondary); its acceptance criterion on search-result rendering inherits from that change.

## Notes

- Current implementation: `frontend/src/App.svelte` line 26 defines `const sidebarExplore = ['Music', 'Gaming', 'Podcasts', 'Education']`; the section renders at lines 3289–3296, each entry as an `<a href="/search?q=...">`. Sticky-preference precedent (localStorage + try/catch) is in `savedHomeVideoSort()` / `setVideoSort()` (~lines 327–411) and the `kapsel.*` storage keys at lines 30–31.
- The `{#each sidebarExplore as label}` loop should become keyed (`(label)`) once removal is added; dedupe on save keeps keys unique.
- Search feedback copy reads from a transient status (`role="status"`), cleared after a short timeout, consistent with other job-status notices in the app.
- Out of scope: renaming a saved link (label ≠ query), server-side sync of the list across browsers, and reordering links. Drag-to-reorder or a settings-page manager can follow as a separate issue if wanted.
- Estimated effort: small — ~50 lines across `App.svelte` and `style.css` plus a smoke-spec addition; no backend work.