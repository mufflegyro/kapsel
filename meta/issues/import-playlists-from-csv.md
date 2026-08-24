# Import playlists from CSV exports

## Summary

The app has a playlists table and UI, but playlists can only enter via the
TubeArchivist ZIP import. Users with Google Takeout exports have channel
subscriptions (already importable via `import-subscriptions`) but no way to
restore their YouTube playlists. Add a sibling `import-playlists` command that
reads one CSV per playlist and links videos already in the archive, optionally
enqueueing downloads for missing videos.

## Requirements

- New `kapsel import-playlists <file.csv>...` command, sibling to
  `import-subscriptions`.
- Each CSV file becomes one playlist. Playlist title is derived from the file
  base name (e.g. `DnB-videos.csv` → "DnB-videos").
- CSV layout follows the example `DnB-videos.csv`:
  - Header with a `Video ID` column (required), plus any extra columns
    (e.g. `Playlist Video Creation Timestamp`) which are tolerated but unused
    for ordering; row order defines playlist position.
  - Rows are video IDs; rows without a usable video ID are skipped.
- For each video ID, look up `videos` by `source = 'youtube' AND external_id`.
  - Found → insert a `playlist_entries` row at the next position.
  - Not found → count as missing; with `--download`, enqueue a direct-video
    download job for `https://www.youtube.com/watch?v=<id>` so re-running the
    import after downloads complete links them.
- Idempotent: re-importing the same file replaces that playlist's entries
  (same deterministic playlist id per file) rather than duplicating.
- Playlist search documents are synced so playlists stay searchable.
- Report JSON mirrors the subscriptions report: playlists created, entries
  linked, videos missing/enqueued, errors.

## Acceptance Criteria

- `kapsel import-playlists DnB-videos.csv` creates one playlist titled
  "DnB-videos" with entries for videos already in the archive, in row order.
- Videos not in the archive are reported; with `--download` a download job is
  enqueued for each missing video.
- Re-running the same file does not duplicate entries and refreshes positions.
- Playlist appears in the playlists list UI and is searchable by title.
- Covered by parser and command tests.

## Notes

- Standard Google Takeout `playlists.csv` (one file, all playlists, with
  playlist id/title columns) is a possible follow-up; this issue targets the
  per-playlist export format shown in the example.
- Direct-URL downloads reuse `download.EnqueueDownload`; members-only videos
  stay hidden from views as before.
