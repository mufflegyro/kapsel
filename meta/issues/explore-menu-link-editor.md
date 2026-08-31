# Explore menu link editor: add, edit, and remove search links from a pop-up

## Summary

The sidebar Explore section renders a hardcoded list (`Music`, `Gaming`, `Podcasts`, `Education`) defined in `frontend/src/App.svelte` line 26, where every entry is a shortcut to a plain search (`/search?q=<label>`). This issue makes the list user-manageable **from the menu itself** with a pop-up text editor: a control in the Explore section opens a small dialog with a text field where the user can **add a new search link** (type a query, hit Save) or **edit an existing one** (change its text — the label and the search query are the same value, matching the current model), plus a per-link **remove** control. The list persists in `localStorage` under `kapsel.exploreLinks` like the existing sticky UI choices.

This supersedes `add-explore-links-from-search.md` (which scoped save-from-search-page + remove only, and explicitly left *renaming* out) and generalizes it: the pop-up is the single add/edit entry point for the whole management surface.

## Requirements

- **Add via pop-up**: the Explore section header gets a small "+" control (aria-label `Add Explore link`). Clicking it opens a pop-up (reuse the app's popover pattern — `role="dialog" aria-modal="false"`, cf. the quick-queue panel) containing a text input and Add/Cancel buttons. Submitting a trimmed, non-empty query appends it as a new link (`label` = query text, `href` = `/search?q=<encodeURIComponent(query)>`), deduped case-insensitively against existing links (no-op + "already saved" state if it exists). The input autofocuses; Enter submits, Escape cancels.
- **Edit via pop-up**: each existing link gains an edit control (pencil, `aria-label="Edit <label> from Explore"`) that opens the same pop-up prefilled with the link text. Saving replaces the link in place (same position, dedupe against *other* links). Editing a default link marks the list as modified so it persists like any user change.
- **Remove**: per-link remove control (`aria-label="Remove <label> from Explore"`, hover/focus-visible affordance) deletes the link; applies to defaults and user-added links alike.
- **Persistence and defaults**: JSON array of strings under `kapsel.exploreLinks` (new key alongside `kapsel.homeVideoSort` etc.), read/written with the existing try/catch pattern (`savedHomeVideoSort()` / `setVideoSort()` ~lines 327–416). With nothing stored, seed with the four current defaults so a fresh profile is unchanged (existing smoke assertion for `Music` in `.side-section a` must keep passing); the first user modification persists the resulting list. Storage failures degrade to in-memory-only editing.
- **Empty state**: when every link is removed, render a hint ("Search the archive and pin a query here") instead of an empty section, with the "+" control still available to add the first link.
- **Layout integrity (updated 2026-08-31: desktop-size only feature)**: the whole editor affordance surface (section "+" control and per-link edit/remove) renders only at desktop sizes. In the icon-only collapse at `max-width: 1180px` both the section head and the per-link controls are hidden, so that mode keeps showing plain links exactly like before the feature; below `max-width: 780px` the entire Explore section stays hidden as before. Long saved queries clamp with ellipsis like other sidebar labels.
- Scope: frontend-only (`frontend/src/App.svelte` + `frontend/src/style.css` + `frontend/e2e/smoke.spec.js`). No backend or API changes — Explore links remain client-side shortcuts to the existing `GET /api/search`.

## Acceptance Criteria

- From the sidebar Explore "+" the user adds a query; the link appears immediately, navigates to `/search?q=<query>`, and shows the same results as typing the query in the topbar.
- Adding an existing query (case-insensitively) is a no-op with a clear "already saved" state; adding an empty/whitespace query is rejected.
- Clicking a link's edit control opens the pop-up prefilled; changing the text rewrites the link in place (label, href, and sidebar icon all update), and the query navigates to the new value.
- Removing a link (default or added) removes it; the list survives a full reload; clearing `kapsel.exploreLinks` restores the four defaults.
- A fresh browser profile still shows `Music`/`Gaming`/`Podcasts`/`Education`; the existing smoke assertions stay green.
- Removing all links shows the empty-state hint and the "+" still works.
- `pnpm check` passes and the smoke run (add → edit → remove → reload-persistence) is added to `frontend/e2e/smoke.spec.js`.

## Related

- `add-explore-links-from-search.md` — **superseded by this issue**; its remove-control scope and localStorage design carry over. The search-page "Save to Explore" button it proposed becomes an optional extension (open the pop-up prefilled with the current query); not required by this issue's acceptance criteria.
- `explore-views-show-episodes.md` — proposes turning Explore categories into server-defined episode views. If categories become server/config-defined, user-saved links should merge alongside (or override) them, and add/edit/remove applies to both kinds; the `kapsel.exploreLinks` list stays the user-data layer either way.
- `make-search-results-episode-first.md` — Explore links target `/search?q=` shortcuts whose display inherits that (already landed) change.

## Notes

- Current implementation: `sidebarExplore = ['Music', 'Gaming', 'Podcasts', 'Education']` at `App.svelte:26`; section rendered at lines 3328–3335 as `{#each sidebarExplore as label}` → `<a href="/search?q=...">`. Sticky-preference precedent: `kapsel.*` keys at lines 31–36, try/catch helpers at ~327–416. Popover precedent: quick-queue panel at ~3283 (`role="dialog" aria-modal="false"`).
- Open decision (product): whether a link should ever carry a display label different from its query. v1 keeps `label == query` (single text field — matches the current model and "text edit box" phrasing); a two-field editor (label + query) can follow as a separate issue.
- The editor affordances are a desktop-size feature (user decision 2026-08-31): at ≤1180px the section head (with the add control) and per-link edit/remove hide, leaving the pre-editor plain links; the sidebar smoke test for the editor is skipped below 1180px viewports (`test.skip` on viewport width, so the mobile Playwright project skips it).
- Dialog-state announcement is symmetric (2026-08-31 follow-up): the "+" and every per-link edit control carry `aria-haspopup="dialog"`, a mode-aware `aria-expanded` (edit buttons reflect their own row via the editor index), and `aria-controls="explore-editor-panel"`; the panel carries the matching `id`, so edit mode announces the dialog state just like add mode.
- Estimated effort: small — ~100–150 lines across `App.svelte` and `style.css`, one new smoke test; no backend, schema, or fixture work. Status: **landed 2026-08-31 in v1.3.2**.