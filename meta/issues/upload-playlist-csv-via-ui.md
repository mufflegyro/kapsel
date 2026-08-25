# Upload a playlist CSV from the playlist page

## Summary

`import-playlists` is CLI-only today: a user must `ssh` into the node and run
`kapsel import-playlists <file.csv>` to get a per-playlist CSV export into the
archive. The playlist page should expose the same capability in the browser.
Add an upload control on `/playlists` that accepts a single `playlist.csv`
(multipart file upload) and runs the existing `playlistimport.ImportFile`
path to create or update the playlist, then refreshes the list.

Follow-up issue (separate): adding public YouTube playlist links by URL.
This issue covers the CSV upload only.

## Requirements

- Backend endpoint `POST /api/playlists/import` (auth-gated) that accepts a
  multipart `multipart/form-data` upload with a file field (e.g. `file`).
- Validate the upload: require one file, reject oversized bodies, and run the
  existing `playlistimport.ImportFile` in the default mode (metadata scan).
  On success return a report shaped like the CLI report plus the created
  playlist id/title so the UI can navigate/confirm.
- The playlist title is derived from the uploaded file's base name, matching
  the CLI's `DnB-videos.csv` → "DnB-videos" behavior, so re-uploading the
  same filename refreshes that playlist idempotently.
- The uploaded content is written to a temporary file (never trusted from a
  request body directly into the DB) before being passed to the importer.
- Frontend: add an upload form to the `/playlists` route. On success, reload
  the playlist list so the new playlist appears. Show inline status/error.
  Follow existing Svelte patterns; use fetch with a `FormData` body.

## Acceptance Criteria

- Uploading `DnB-videos.csv` via `/playlists` creates (or updates) the
  "DnB-videos" playlist and it appears in the list without a manual CLI step.
- Uploading the same filename again replaces that playlist's entries rather
  than duplicating (idempotent, consistent with the CLI).
- A non-CSV/oversized/invalid upload returns a clear error and does not
  mutate the archive.
- Covered by a server handler test and a playlistimport-level test.

## Notes

- Backend handler lives in `internal/server` next to the other playlist
  handlers, reusing `playlistimport`.ImportFile so there is one source of
  truth for CSV parsing/linking/lifecycle.
- The uploaded tmp file is cleaned up after import.

## Implementation notes

- `playlistimport.PlaylistIdentity(path)` was extracted from `upsertPlaylist`
  so the HTTP handler and the CLI derive the same deterministic playlist
  id/title from the file name.
- The upload is parsed from the request body via `playlistimport.Parse` and
  imported with `ImportEntries` using the original uploaded file name; no
  temp file is written (earlier draft staged one; removed). Body size is
  bounded with `http.MaxBytesReader` + `ParseMultipartForm`.
- UI: the upload form was added directly to the large legacy `App.svelte`
  playlists route using the file's existing Svelte 4 syntax; migrating this
  file to Svelte 5 runes is deferred as a separate risky refactor per
  AGENTS.md.
