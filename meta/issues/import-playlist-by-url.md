# Import a YouTube playlist by link from the playlist page

## Summary

The playlist CSV upload (`/api/playlists/import`, `playlistimport`) lets a user
import a per-playlist CSV export from the browser. The upload issue explicitly
deferred this follow-up: importing a public YouTube playlist by its link.
Add a text field to the same import form on `/playlists` that accepts a
YouTube playlist URL and enqueues a background `playlist_import` job that
fetches the playlist from YouTube (yt-dlp flat dump) and imports it with the
same linking semantics as the CSV path: upsert the playlist, link videos
already in the archive, enqueue metadata scans for missing ones.

Unlike the CSV upload (synchronous parse), fetching a playlist from YouTube
takes time and runs yt-dlp in the media sandbox, so it must be a durable job
like `channel_scan`, not a synchronous request.

## Requirements

- Backend job type `playlist_import` handled by the existing downloader (it
  owns the yt-dlp sandbox, cookies, retries). Payload: `{ "url": "..." }`.
- URL validation: accept YouTube playlist links with a `list` query parameter
  (`https://www.youtube.com/playlist?list=<id>`, `watch?v=..&list=<id>`,
  `youtu.be/..?list=<id>`); reject non-YouTube URLs and missing/empty list ids.
- Job flow: normalize URL → run `yt-dlp --flat-playlist --dump-single-json`
  in the sandbox → parse entries → import with the CSV path's semantics:
  - Playlist id is deterministic: `yt-<listID>` with `external_id = <listID>`,
    so re-importing the same playlist refreshes it (idempotent).
  - Title comes from the fetched playlist metadata (fallback: list id).
  - Link videos already in the archive; enqueue metadata scans for missing
    videos (default `ModeMetadataScan`, deduplicated per URL).
  - Link the playlist to its channel only when that channel already exists in
    the archive (do not create channels as a side effect).
- API: `POST /api/playlists/import-url` (auth-gated, bounded JSON body)
  returns `202` with the enqueued job (public job DTO), mirroring the channel
  scan endpoints. Invalid URLs return `400` and enqueue nothing.
- Frontend: in the same import form on `/playlists`, add a text field for a
  YouTube playlist link plus an "Import from link" button. On submit, enqueue
  the job and show queued/running/succeeded/failed status inline (mirroring
  the channel scan UI), then reload the playlist list on success.
- Register the handler in `internal/app` runner map; add a jobs-page label for
  `playlist_import`.

## Acceptance Criteria

- Posting a valid playlist URL returns a queued `playlist_import` job and the
  playlist appears (after the job runs) with id `yt-<listID>`, its real title,
  and entries linked from the archive.
- Re-posting the same URL while the job is active does not enqueue a second
  job; re-running later refreshes the same playlist instead of duplicating.
- Posting a non-YouTube URL or a URL without a `list` parameter returns a
  clear `400` and enqueues nothing.
- Missing videos get deduplicated metadata scans (same as the CSV path).
- Covered by unit tests (URL normalization, command build, handler with a
  fake runner, idempotent re-import) and a server handler test.

## Notes

- Import direction: `playlistimport` currently imports `download` for its
  enqueue helpers. The `playlist_import` handler must live in `download`
  (sandbox/yt-dlp access), so `playlistimport` inverts that edge: it defines a
  tiny `Enqueuer` interface (`EnqueuePlaylistVideo(ctx, videoID, mode)`) and
  the CSV/CLI/URL paths all hand it a `download.NewPlaylistImportEnqueuer`.
  This keeps one source of truth for playlist linking.
- The playlist import intentionally does not hydrate catalog rows from the
  flat dump: `upsertCatalogVideo` would overwrite `catalog_position` for
  videos that already belong to a channel catalog, reordering channel pages.
  Missing videos go through the existing `video_metadata_scan` jobs instead,
  exactly like the CSV path.
- `BuildPlaylistImportCommand` reuses the channel-catalog flat-dump shape
  (`--flat-playlist --dump-single-json`) with the same 4 MiB stdout bound;
  flat entries are small, so this covers any realistic playlist.
