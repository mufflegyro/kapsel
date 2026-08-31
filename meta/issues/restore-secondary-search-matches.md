# Restore channel and playlist matches to search results

> **Landed 2026-08-31.** `Search` now splits the re-ranked pool before
> slicing: episode rows (anything resolving to a video) feed the
> offset/limit window exactly as before, while non-video rows (channels,
> playlists) are appended after the window in re-rank score order, capped
> at `secondaryResultCap = 8` (`internal/search/search.go`). The cap is
> window-independent — deterministic per query, identical on every offset
> page, never consuming episode slots. Verified on a real archive: `music`
> (1012 matches) returns 50 episodes + the `sub art` / `Darko Audio` /
> `Xander Ewald` channel cards; `playlist` shows its channel card again.
> Known residual (tracked in `add-search-result-pagination-and-lazy-loading.md`,
> not this issue): channel docs whose raw BM25 rank falls outside the
> 200-doc pool are unavailable to the block — on the verification archive
> only 3 of the matching channel descriptions sat inside the pool. Smoke
> coverage: the fixture gained 55 filler episodes + a channel-description
> match, and the smoke asserts a 50-tile window with a non-empty secondary
> block for `filler` (56 results).

## Summary

The search re-rank (5585876) merged videos, channels, and playlists into **one** re-ranked list and then sliced the 50-row page. Channel/playlist docs are therefore forced to win a slot against recency-boosted video titles — and they systematically lose, so the channels & playlists block is empty for most queries.

Live evidence on the production archive (query `music`, 1065 matches): exactly 3 channel docs match, all via `description` (`sub art`, `Darko Audio`, `Xander Ewald`); they re-rank to positions 89/101/107 of the 200-doc pool — comfortably inside the pool, but every slot in the 50-row window goes to a video, so the UI shows **0 channel cards**. Pre-re-rank, raw-BM25 order let such description matches surface in the top 50 (the query "music" previously returned channel hits).

Score arithmetic explains it: video `title` docs get ×3 weight × recency (≈×1 for fresh uploads); channel/playlist `description` docs get ×1 and no recency → outscored ~3×+ even at equal BM25. Playlists are hit identically (their docs match via title/description; on common terms they lose the same window fight — e.g. `playlist` → 61 matches, 0 cards on page 1).

This contradicts the re-rank proposal's own recommendation (d): *"whether the secondary channels/playlists block should also be re-ranked (recommendation: no — keep relevance order)"*.

## Requirements

- **Split the re-ranked pool into two lists before slicing.** Episodes: the re-ranked video rows, windowed by `offset`/`limit` exactly as today (50 per page). Secondary: the top-scored non-video rows (channels/playlists), returned with the response **regardless of the episode window**, capped at a small quota (e.g. 8), ordered by re-rank score so the block keeps relevance order.
- The UI already renders non-video rows as the `.search-secondary` block above the episode grid — no frontend change needed; the server just has to include the rows again.
- `distinct_owners` already counts channel/playlist owners, so the count label stays honest with no change.
- `offset` pages episodes only; the secondary quota is deterministic per query (window-independent), so it never duplicates across pages and never shrinks when the pool grows.
- Keep the existing dedupe and episode-first display behavior untouched.

## Acceptance Criteria

- `music` on the production archive returns the three matching channel cards (`sub art`, `Darko Audio`, `Xander Ewald`) in the secondary block alongside the 50 episodes on page 1.
- Playlist-title queries (`Doctor Downvote`, `Otherworld TV`, …) still return their playlist card; a query whose only channel/playlist matches are description docs (`documentary`, `mix`, `music`) shows those cards again.
- The 50-video page and its newest-first ordering are unchanged; the secondary block does not consume episode slots.
- `go test ./internal/search/...` covers: (a) secondary docs beyond the episode window still returned, (b) secondary quota cap, (c) secondary rows stable across `offset` pages, (d) episodes unchanged with/without secondary matches.
- Smoke: extend the fixture with a channel-description match so a >50-episode query asserts the secondary block is non-empty.

## Related

- `re-rank-search-with-recency-and-field-weights.md` — the change that caused this; its recommendation (d) ("keep secondary in relevance order, don't re-rank into the episode window") is what this restores.
- `add-search-result-pagination-and-lazy-loading.md` — the episode-window coherence issue (server-side dedupe before slicing, fixed pool) is tracked there; this issue is about the secondary block, not the window mechanics.

## Notes

- Residual tension to confirm in review: a description-only channel match is a weaker signal than a name/title match — but "weaker" justified hiding it behind 50 recency-boosted titles; the proposal's intent was that the secondary block *always* surfaces when something matches, in relevance order.
- No schema, indexing, or frontend change expected. Status: delivered 2026-08-31.