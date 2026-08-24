# Add metadata-only scan mode to import-playlists

## Summary

`kapsel import-playlists` today can either link videos already in the archive
(default) or enqueue a full media download for each missing video
(`--download`). There is no middle path: fetch just the video metadata into
the catalog — like channel imports do with `--scan-only` — so missing playlist
videos become linkable catalog rows (title, channel, thumbnail, duration,
media_path NULL) with a later "Download" button, without transferring media.

## Requirements

- **Default behavior change:** `kapsel import-playlists <file>.csv` enqueues a
  metadata-only job for each video missing from the archive (no media
  download), so missing playlist videos become linkable catalog rows.
- Add a `--link-only` flag that preserves the old default (link existing
  videos, report missing, enqueue nothing).
- Add a `--download` flag (existing) that enqueues a full media download for
  each missing video instead of a metadata scan.
- The metadata job runs yt-dlp with a skip-download, dump-single-json style
  command (as the channel scan path does), upserts a catalog video row
  (`media_path` NULL, catalog/media origin), and links the playlist entry so a
  re-run of the import links it.
- Failed metadata fetches are reported in the import/scan report and do not
  abort the remaining entries.

## Acceptance Criteria

- `kapsel import-playlists <playlist>.csv` (no flags) creates the playlist,
  links videos already in the archive, enqueues metadata-only jobs for the
  missing videos, and does not download any media.
- `kapsel import-playlists --link-only <playlist>.csv` keeps the previous
  behavior: link existing videos, report missing, enqueue nothing.
- `kapsel import-playlists --download <playlist>.csv` enqueues full media
  downloads for missing videos.
- After the metadata jobs complete, re-running the import links the previously
  missing videos (playlist_entries rows are created).
- Playlist view shows the scanned videos with metadata and a download action.
- Job type for the metadata fetch is visible in the downloads/jobs UI.
- Covered by unit tests (command flag parsing, report fields, link-after-scan
  flow) and a documented manual verification path.

## Notes

- Mirror the channel path: `import-subscriptions --scan-only` →
  `channel_first_download` jobs with `ScanOnly: true` → `syncChannelCatalog`
  upserts catalog rows. Playlists need an analogous single-video metadata job
  (e.g. `video_metadata_scan` job type) or a reuse of the direct-video payload
  with a scan-only flag.
- Current implementation: `cmd/kapsel/main.go` `runImportPlaylists` accepts
  only `--download`; `internal/playlistimport/ImportEntries` links existing
  videos and enqueues downloads for missing ones via
  `download.EnqueueDownload`. The `download.Payload.ScanOnly` flag is only
  honored on the channel first-download path today.
