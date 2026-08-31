# Re-rank search results with recency and field weights

> **Landed 2026-08-31.** Final decisions: multiplicative exponential recency
> decay with a **3-year half-life** (`recencyHalfLife`), not the 18 months
> floated in discussion — the constraint below (strong ~2019 title beats a
> weak caption from last week) fails at 18 months (2019 titles decay to
> ×0.03 and lose to fresh captions; crossover sits near ~7.8 years, so the
> half-life must exceed it). Field weights: `title`/channel `name` ×3,
> per-video `channel` doc ×2, `description` ×1, transcript/comment `text*`
> ×0.5, unknown ×1 — multiplicative on BM25, so the ordering is invariant
> to corpus-scale shifts in absolute bm25 values. Candidate pool: 200 docs
> fetched by raw BM25, hydrated, re-scored in Go, then offset/limit applied.
> Undated and future-dated rows and channel/playlist docs get neutral decay
> (×1), keeping the secondary block in BM25 relevance order per (d). The
> endpoint gained an `offset` parameter (clamped ≤10000, echoed in the
> response) so pages past 50 are reachable; UI lazy-loading is tracked in
> `add-search-result-pagination-and-lazy-loading.md`.

## Summary

Search retrieval and matching are fixed (multiword AND semantics, honest counts), but ranking is still pure BM25 over a single FTS `text` column, which produces two systematic distortions on real archives:

1. **No recency signal.** Ranking is `ORDER BY bm25(...) LIMIT 50`. Old videos with short titles dominate: on a live archive, a search for "island" returned 50/50 title matches dominated by 2016–2019 uploads; a 2026-08-27 video whose title contains the query ranked below the cutoff entirely.
2. **Field unfairness via length normalization.** Title, description, caption transcripts, and comments all live in one indexed column. BM25 length-normalizes, so a caption with one query occurrence in ~60k tokens scores far below a 3-word title (replica measurements: old short titles −3.2…−2.7, recent 10-token titles −2.6, descriptions −1.8, captions −1.6…−1.8). Caption matches effectively never reach the top 50.

This issue is deliberately scoped as a product decision: the weights decide how strongly recency should beat textual relevance, and whether a caption match should ever outrank an old title match. Those are editorial calls, not bug fixes.

## Requirements

- **Index channel names onto video docs (channel fan-out) — landed 2026-08-31.** Live testing on 2026-08-31 showed why this is no longer optional: with multiword AND semantics, `adam stew island` returns 0 rows because no document contains all three tokens — Adam Stew's video docs carry "island" but not his name, and his channel docs carry his name but not "island". Combining a channel with a topic is a natural search and currently can never match. Fix: sync an extra search doc per video (owner_type `video`, field `channel`, text = channel name + video title) at import/sync, plus an idempotent backfill for existing archives, mirroring the existing denorm fan-out. Note the consequence to review: `adam stew` alone will then match all of that channel's videos (the channel row still leads on BM25 and appears in the secondary block; recency re-ranking then decides which videos surface first). [Delivered in the same release as the multiword fix; the combined name+title doc text is what lets channel+topic AND queries match.]
- **Enlarge the candidate pool, re-rank after hydration.** The FTS `LIMIT` happens before hydration, so page-level reordering alone cannot fix this. Fetch a larger internal pool (e.g. top 200–400 by BM25), hydrate, then re-rank in Go and return the configured page.
- **Re-rank score:** `rank = bm25 + recency_decay(age) + field_boost(title > description > caption/comment)`. Weights as named constants in `internal/search`; unit tests pin the ordering behavior, not magic numbers.
- **Recency decay** should be gentle (e.g. a bounded bonus/penalty over years, not a cliff) so a strong title match from 2019 still beats a weak caption match from last week — exact balance is the product decision to confirm in review.
- **Field boost** uses the already-known `field` of each matched doc; no new indexing is required.
- **Keep counts honest:** the new `Stats` counting must reflect the same match set (it already counts all rows, independent of the page, so it is unaffected).
- Keep the episode-first display, dedupe, and newest-first client ordering as-is; they operate on whatever order the server returns (the client sorts episodes by `published_at`, so the visible change from re-ranking is *which* episodes make the page, and how the secondary block orders).

## Acceptance Criteria

- `adam stew island` returns Adam Stew's island videos (the 2026-08-27 "I Bought a Tiny Off-Grid Cabin on a Remote Island" among them) instead of 0 rows.
- On an archive where "island" previously returned an all-2016-leaning page (live: 30 of the top 50 published 2021 or earlier, only 5 from 2026), the first page includes recent videos containing the term in title or caption.
- The 2026-08-27 "…Remote Island" video, which live at #16 of 24 for `remote island` behind 15 older titles (and is absent from the top 50 of 260 for `island`), surfaces on the first page for those queries.
- A caption-only match for a recent video can appear within the first page for a term that also matches many old titles (the exact threshold is part of the weight decision).
- `go test ./internal/search/...` pins the re-ranking order and the channel fan-out with deterministic fixtures; existing tests stay green.
- No FTS schema migration: `owner_type`/`field` already exist (unindexed columns usable as predicates), re-ranking happens in Go after hydration, and the channel doc is just another row in `search_documents`.

## Related

- `make-search-results-episode-first.md` — display layer this ranks into; landed 2026-08-31.
- Multiword AND matching and `Stats` counting landed alongside this proposal's filing; they are prerequisites (without A, multiword queries return nothing to rank).

## Notes

- Open decisions — resolved 2026-08-31 at implementation: (a) decay shape is
  a multiplicative exponential with a 3-year half-life (bounded, no cliff —
  a 2019 title still beats a fresh caption; crossover ≈ 7.8 years);
  (b) captions beat titles only for titles older than ≈ 8 years at equal
  BM25 — the weight gap (×3 vs ×0.5) needs ~4 half-lives to overcome;
  (c) internal pool is 200 (hydrating + scoring 200 rows is cheap; covers
  any realistic first page including offsets); (d) secondary channels/
  playlists block keeps BM25 relevance order (their docs are undated, so
  decay is neutral — as recommended).
- Behavior pins in `internal/search`: fresh-above-old for identical docs,
  title-above-comment at equal text and age, old-title-above-fresh-comment
  at 7 years, pool surfacing a fresh video that raw BM25 ranks 61st behind
  60 old short titles, and exact offset windows.
- Estimated effort: moderate — Go-side changes in `Search()` plus tests; no frontend or schema work.
