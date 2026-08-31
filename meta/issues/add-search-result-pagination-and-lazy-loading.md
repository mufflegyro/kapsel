# Add search result pagination with lazy loading

## Summary

The search count is now honest (`total` / `distinct_owners` count the full match set), but the UI can never show more than the first page: `loadSearch()` issues a one-shot `GET /api/search?q=…&limit=50` and the search page has no load-more wiring. The endpoint itself has no pagination at all — `search.Query` is `{Term, Limit}` with `MaxLimit = 50` and no `offset`/`page` parameter — so on an archive with 256 distinct matches for "island", the label says 256 but only 50 tiles are reachable and scrolling loads nothing.

## Requirements

- **Add `offset` to the search API.** `GET /api/search` accepts `&offset=` (bounded via the existing `boundedInt` helper, min 0); `limit` stays the page size (capped at `MaxLimit`). Return `offset` in the response so the client can compute the next page.
- **Dedupe server-side before slicing (required).** The FTS result list contains multiple docs per owner (title + description + subtitles/comments for one video). The client currently dedupes after the 50-row cut; raw-offset pagination over that list would re-show (and client-dedup away) already-seen owners, shrinking pages and desyncing `hasMore`. Move the dedupe into the Go pipeline — mirror of `dedupeSearchResults`: key `type-id`, prefer the `title`/`name` field doc — applied to the hydrated results *before* `LIMIT ? OFFSET ?`. Every returned row is then distinct, pages never repeat, and received-count vs. `distinct_owners` yields a correct `hasMore`.
- **Frontend mirrors the library load-more pattern.** The home grid already has the machinery to copy: sentinel element + `IntersectionObserver` (`rootMargin: 600px`), a visible "Load more" button fallback, loading/status text (a11y live region), and guards against duplicate concurrent page requests. `loadMoreSearch()` appends `offset += 50`; stop when a page returns fewer than the page size or when received rows reach `distinct_owners`.
- **Keep the global newest-first episode order across pages.** `searchEpisodeResults` is a reactive derived (dedupe → filter videos → `searchPublishedDesc`), so appended pages re-sort the whole grid newest-first automatically — cross-page order stays monotonic and the recency-first browsing intent holds across all results.

## Acceptance Criteria

- On the live archive ("island", 256 distinct), scrolling/load-more keeps appending tiles until all matches are reachable; the count label stays "…results" and never disagrees with what is reachable.
- No owner appears on two pages; appending never duplicates or reorders already-visible tiles backward.
- Auto-loading stops cleanly at the last page; the manual button works when `IntersectionObserver` is unavailable; concurrent requests are avoided.
- Unit tests: offset slicing; dedupe-then-slice invariants (page1 ∪ page2 == the full distinct set, no repeated owner); handler offset bounds (negative → 0, capped).
- Endpoint test: `&offset=` over a dup-heavy fixture returns clean distinct pages.
- Smoke coverage: search a term with >50 matches, load more, assert rows grow, no duplicate tiles, label unchanged.

## Related

- `re-rank-search-with-recency-and-field-weights.md` — this issue is its plumbing prerequisite: the server-side dedupe mandated here is exactly the step the re-rank needs before slicing a re-ranked pool.
- `make-search-results-episode-first.md` — display layer landed 2026-08-31; its newest-first client sort is what keeps appended pages globally ordered.
- `add-home-infinite-scroll.md`, `add-paginated-video-library-api.md` — the reusable page/load-more pattern to mirror.

## Notes

- No FTS or schema change: FTS5 returns every matched row and `offset` merely slices, so all distinct matches become reachable without touching the index.
- Performance is unaffected by pagination: server cost is dominated by the FTS matched-set scan (measured on the live instance: ~13 ms at 260 matches, ~200 ms for ultra-common single tokens — both unscaled by `LIMIT`/`OFFSET`); the extra work per page is one bounded hydration query.
- Open interplay with the re-rank proposal: when a re-ranked pool with a cap lands, the reachable count must not exceed the pool (raise the cap or cap the reported count at it). For "island" (256 distinct) a ~400 pool is fine.
- Status: open. Not started; awaiting sign-off on the approach.