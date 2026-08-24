# Add YouTube metadata search with add-to-archive actions

## Summary

Kapsel only searches its local SQLite/FTS index. Add the ability to search YouTube metadata (titles, channels, videos) from the UI using yt-dlp or the YouTube API, and to add results to the archive directly — channels and individual videos — like Youtarr's search.

## Requirements

- Add a search UI that queries YouTube for videos/channels (via `yt-dlp ytsearch` or an optional YouTube Data API key), separate from the local FTS search.
- Show rich result cards (thumbnail, title, channel, duration) for videos and channels.
- From a video result, queue a direct download (existing `POST /api/downloads` flow).
- From a channel result, queue a channel-first add (existing channel flow, optionally catalog-only/scan-only).
- Respect existing pacing and dedupe: adding an already-present channel/video should not re-enqueue.
- Keep the search bounded (page size, request limits) and observable.

## Acceptance Criteria

- A UI search returns live YouTube results for a query.
- Video results queue downloads; channel results queue channel adds.
- Results already in the library are marked as such.
- The search respects the existing yt-dlp pacing and retry rules.
- Tests cover the search query building, bounded pages, and the enqueue actions.

## Notes

- Reference: Youtarr uses an optional YouTube Data API key with silent yt-dlp fallback; Kapsel can start with `yt-dlp ytsearch` only and add an API key later.
- This complements the local FTS search (`prototype-sqlite-fts-search.md` / `expose-sqlite-fts-search-over-http.md`) rather than replacing it.
- The bundled nightly yt-dlp already handles `ytsearch`; the add-to-archive paths already exist and are reused.