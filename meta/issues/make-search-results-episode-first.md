# Make search results display as an episode thumbnail list instead of a channel list

## Summary

The search backend already returns matching videos (episodes) alongside channel and playlist documents; the problem is purely presentational. The `/search` view (`frontend/src/App.svelte`) renders every result in the same flat `.result-list` row layout — thumbnail-or-letter, title, snippet, matched field. BM25 puts channel-name matches first for broad terms, so the page reads as a channel list: a channel row leads, and matching videos trail as compact text rows. Change the results view so episode hits display as an episode thumbnail list (primary content) and channel/playlist matches move into a clearly separate compact secondary block. No API or FTS/ranking changes — this is a display-only change.

## Requirements

- **Partition by type at render time (frontend only):** split `searchPage.results` into video-type (`record.type === 'video'`) and non-video (`channel`, `playlist`) sets. Render episodes first as an episode/thumbnail list — bigger thumbnail (reusing signed `thumbnail_url` and the existing `thumbnailStyle`), duration badge from `record.duration_seconds`, title, and channel line — visually matching the library tile affordances rather than the channel-row look.
- **Channels and playlists become a compact secondary block** below the episodes, labeled (e.g. "Channels & playlists"), using the existing row layout and `resultHref`/`resultTitle`/`resultMeta` semantics so exact channel-name searches stay one click from the channel page. Show the block only when non-video matches exist.
- **Empty-episodes case:** when a term matched only channels/playlists, render the secondary block plus a short note (e.g. "No matching episodes in the archive yet") instead of an empty state or a channel-led primary list.
- **Do not change ranking or retrieval:** keep the server query, `limit=50` fetch, and result order as-is; only the presentation changes.
- Keep the idle/loading/error/zero-result states as they are today.

## Acceptance Criteria

- Searching a broad term (e.g. "Music", "Gaming") shows episode thumbnails as the primary content; no channel row leads the list.
- A term matching a channel name still surfaces that channel in the secondary block, and clicking it navigates to the channel page.
- A term with only channel/playlist matches shows the secondary block with the "no matching episodes" note, not a channel-led list.
- Matching videos navigate to their watch page; thumbnails are signed and render (existing behavior preserved for video rows, just laid out as episodes).
- No changes under `internal/`; existing search API tests untouched. `pnpm check` and `pnpm browser-smoke` pass.

## Related

- `add-explore-links-from-search.md` — **prerequisite for that issue**: Explore links are saved `/search?q=` shortcuts, so this display change (episode thumbnails as the primary view) is what gives saved Explore links their value; its search-result acceptance criterion inherits from here.
- `explore-views-show-episodes.md` — shares the episode-list visual language; if that issue lands, the two can share an episode-tile component/styling rather than duplicating it.

## Notes

- Current render site: `frontend/src/App.svelte` search branch (~lines 3532–3552) — a single `{#each searchPage.results}` over `.result-list`; related CSS `.result-list`/`.result-thumb`/`.result-copy` in `frontend/src/style.css` (~line 2323). Helper functions `resultHref`/`resultTitle`/`resultMeta` (~lines 2008–2026) stay in use for the secondary block.
- `VideoCard.svelte` is not directly reusable for episode rows as-is: it expects full video items (`media_url`, progress, download buttons, channel avatar). Either a lightweight episode tile (thumbnail + duration + title + channel) or a thin adapter is the expected approach — decide in implementation.
- Result order stays as served (BM25). An optional follow-up would sort the episode list newest-first; out of scope here unless review wants it.
- Review amendment (2026-08-31): the episode grid is now sorted `published_at` descending (client-side, `record.published_at`; missing dates sort last, ties keep BM25 order). The Channels & playlists block keeps BM25 order.
- Deferred optional backend item: indexing channel names onto video search docs would surface episodes even when a term matches *only* channel names. Not needed for this display change; revisit if empty-episode terms turn out to matter in practice.
- Estimated effort: small — ~40–60 lines across the search branch of `App.svelte` plus ~30 lines of CSS; no backend work.
- Review amendments (2026-08-31, post-implementation): search results are deduped per owner (title/description/subtitle/comment docs of one video collapse to one tile, preferring the title row); the count subtitle reports unique videos/channels/playlists, not raw rows; tiles matching on a non-title field show a small "Matched in …" tag; the results subtitle is a `role="status"` live region; the episode grid drops the extra max-width cap to match the library grid.