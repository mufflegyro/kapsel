# DEVLOG

## 2026-06-25

- Investigated production YouTube download failures in CT `119`. `yt-dlp` stable and nightly still received `HTTP Error 403: Forbidden` for direct MP4 media URLs, while `--check-formats` skipped the failing direct formats and selected a working HLS format. Added `--check-formats` to Kapsel video download commands.
- Deployed dirty build `db72972-dirty-20260625090843` to CT `119` with checksum `79fb19e68e73b0b74efd3079fcee9d690980cff2661b25fdee70f6eecb3ac38f`; representative retry job `b7441f34-6922-441d-9036-357590df0d8b` succeeded.

## 2026-08-24 — Incident: local archive DB deleted during testing; recovery in progress

**What happened:** While testing the new `import-playlists` command against the
local macOS archive (`test-data/kapsel.db`), a multi-line shell command with an
unquoted SQL argument caused `rm` to receive the sqlite3 arguments as file
paths. `rm` deleted `test-data/kapsel.db` (68,812 video records) and the built
`dist/kapsel` binary. The 0-byte file left behind was a fresh empty DB created
by the following sqlite3 invocation, not a remnant of the original.

**What was lost:** The SQLite catalog (videos, channels, playlists, jobs,
watch history). Media files in `test-data/media/` (10 complete downloads +
`*.info.json` metadata) were NOT affected. `subscriptions.csv` was NOT affected.

**Why it happened:** The command joined a `rm` and a `sqlite3` call onto one
line and the quoting collapsed, so `rm -f` swallowed the DB path as an argument.
This is a shell-quoting failure on my part, not a tool bug.

**Immediate recovery (in progress):**
1. Rebuilt the binary: `go build -o dist/kapsel ./cmd/kapsel`.
2. Restarted the server; the empty DB was migrated to schema 15.
3. Re-imported the channel catalog from `subscriptions.csv`:
   `./dist/kapsel import-subscriptions --scan-only subscriptions.csv`
   → 286 channels enqueued as `channel_first_download` jobs; the job runner
   re-fetches each channel's catalog from YouTube, rebuilding video metadata.
   Expected ~2h to finish. Status: 0 failed, progressing.
4. Playlists are reconstructible from the untouched `playlists/*.csv` exports
   (96 files) via the new `import-playlists` command once the catalog returns.
5. Media re-link: `scripts/relink_media.py` reads each intact `<id>.info.json`
   and re-points the rebuilt video rows at the existing files
   (`media_path`, `thumbnail_path`, `media_origin='manual'`,
   `media_downloaded_at`, `archived_at`) without re-downloading.
   **Done:** 8 videos with complete media re-linked and serving (HTTP 200 +
   range 206 verified); rows for 9 manual-download channels were recreated
   from info.json since those channels are not in subscriptions.csv. 3
   videos were already incomplete before the incident (no complete media on
   disk) and keep their intact metadata for later re-download.

**Permanent guardrails (committed):**
- `AGENTS.md` now requires a verified `kapsel backup <path>.zip` before any
  command that could delete/rewrite archive data, and warns against
  multi-line or unquoted destructive shell commands.
- Backup verified working: `test-data/backups/2026-08-24-catalog-rebuild.zip`
  (707 KB, `VACUUM INTO` snapshot of the rebuilding DB).
- `.gitignore` now also excludes the `playlists/` Takeout export directory.

## 2026-08-25 — Auto-download flood: queue cancelled, scheduler disabled

**Symptom:** the `/downloads` page showed ~1143 jobs tracked locally and the
user saw "thousands of items queued". Root cause: 242 subscribed channels each
get a `channel_auto_download` job whenever the scheduler runs, and the local
server had been started without `KAPSEL_CHANNEL_AUTO_DOWNLOAD_INTERVAL`, so it
used the 24h default and kept re-enqueuing. The queue itself was the flood; the
page total (1143) also counts historical succeeded/failed/cancelled rows.

**Actions taken (with DB backup first):**
1. Backup before mutating anything:
   `test-data/backups/pre-queue-cleanup-20260825-130904.zip` (20 MB, verified).
   Note: an initial `kapsel backup` without env vars backed up the wrong DB
   (`data/kapsel.db`, 0 videos); re-ran with `KAPSEL_DATA_DIR`/`KAPSEL_DB_PATH`
   pointing at `./test-data`.
2. Cancelled all queued/running download-type jobs via the app's own API
   (`POST /api/jobs/{id}/cancel`): 295 total (291 `channel_auto_download`,
   4 `download`). Result: 0 queued/running download jobs.
3. Restarted the server with `KAPSEL_CHANNEL_AUTO_DOWNLOAD_INTERVAL=0`
   (scheduler permanently off for channel auto-download). A restart without
   that env var reverts to the 24h default.
4. Recorded the env var in `macos-local.env.example` with a comment.

**Not done (awaiting explicit user decision):** the downloaded media files are
still on disk (162 `channel_auto` files ~65.8 GB + 11 `manual` ~4.4 GB). A
purge via `DELETE /api/videos/{id}/media` was offered; user chose to keep
auto-download off and not purge yet.

## 2026-08-25 — Playwright 1.62 upgrade + playlist upload smoke test

**Context:** local Playwright was unusable: `@playwright/test` 1.59.1 expected
chromium v1217, but the browser cache had v1234 (installed by a bare `npx
playwright install` resolving to a newer Playwright). `wb` (aduermael/wb, a
macOS WebKit browser CLI) was tried first for the playlist-button smoke test:
it navigates/screenshots fine but has no file-input upload command and its
`eval` throws on this build, so it cannot drive a CSV upload.

**Fix:** upgraded `@playwright/test` to 1.62.0 (matches chromium v1234 already
in cache — zero browser downloads) via `pnpm add -D @playwright/test@1.62.0`.

**Result:** `pnpm browser-smoke` (playwright test, desktop + mobile) = **98
passed, 1 known failure**:
- `catalog download success snapshots refresh route data once` (smoke.spec.js:519)
  fails on both projects: after a fake-websocket "job succeeded" emission the
  app never issues the second video-detail GET (trace confirmed). App code is
  git-clean and the same emitLiveJobs hook passes ~90 other tests, so this is
  a Playwright 1.59→1.62 timing/behavior delta, not a playlist regression.
  Not yet root-caused; documented here rather than silently ignored.

**Added:** `playlist CSV upload imports a playlist into the library` smoke
test — uploads a per-run-unique CSV via `setInputFiles` on `#playlist-csv-file`,
clicks Import playlist, asserts status ("1 linked, 1 missing") and that the
playlist appears in the list. Passes desktop + mobile (~200-350ms each).

## 2026-08-25 — Playlist import by URL (follow-up to CSV upload)

**Added** the deferred follow-up from the playlist CSV upload issue: the
Playlists page import form now also accepts a public YouTube playlist link.

- New `playlist_import` background job (handled by the downloader, same
  sandbox/cookies/retry path as `channel_scan`): validates the link
  (`NormalizePlaylistURL`, requires a YouTube host + `list` param), runs
  `yt-dlp --flat-playlist --dump-single-json`, and imports via the shared
  `playlistimport.ImportInto` — upsert playlist under deterministic id
  `yt-<listID>`, link videos already in the archive, enqueue deduplicated
  metadata scans for missing ones, link the channel only when it already
  exists in the archive.
- `playlistimport` now defines a tiny `Enqueuer` interface instead of
  importing `download` (which would have been a cycle once `download` hosts
  the job handler); CSV/CLI/URL paths all use `download.NewPlaylistImportEnqueuer`.
- API: `POST /api/playlists/import-url` (auth-gated, bounded JSON body) →
  `202` + public job DTO; invalid links `400` without enqueuing.
- UI: same import form on `/playlists` gained a URL text field + "Import from
  link" button; status shows queued/running/succeeded (parses the job result
  summary: linked/missing/scans queued) and the playlist list reloads on
  success. Jobs page labels the type "Playlist import".
- Tests: playlistimport identity + channel-linking coverage; download URL
  normalization, command build, enqueue dedupe, handler with fake runner
  (links/enqueues/idempotent, empty-playlist failure); server endpoint
  (202/400/auth); e2e smoke for the field (invalid link error, valid link
  enqueues) — 4 passed desktop+mobile. `go test ./...` green, `pnpm check` 0
  errors.

## 2026-08-25 — Playlist URL import fix: populate the playlist on first import

**Reported live:** importing `PLA-srqGetlqm88EpQgAlFpWxuLfGvI7T7` ("Smeatharpe
Rave Days") created the playlist but added no videos; the last metadata scan
looked stuck. Root cause: the first implementation mirrored the CSV path
(link videos already in the archive, enqueue `video_metadata_scan` for the
rest), so with 0 archived videos it linked 0 and deferred everything to
scans — the playlist stayed empty until a re-import. The "stuck" job was a
scan for `18W9WYw9HaA`, a video removed from YouTube for copyright; it was
retrying (attempts 2/3), not hung.

**Fix (committed, redeployed):** the URL import now hydrates catalog rows
from the flat dump (title/duration/channel/thumbnail — same shape as channel
scans) and links every entry immediately, so the playlist is complete on
first import and no metadata scans are enqueued.

- `upsertCatalogVideo` gained a `preservePosition` flag (catalog_position is
  preserved on conflict for playlist imports, so channel catalogs are never
  reordered; new playlist rows get `catalog_position -1`, i.e. not members of
  any channel catalog).
- Shared `writeCatalogVideo` helper (channel upsert + catalog row + search
  docs) now used by both channel scans and playlist imports.
- `playlistimport.UpsertPlaylist` exported and tx-friendly (playlist row +
  title/description search docs) with an optional description; CSV/CLI
  `ImportInto` unchanged.
- Result semantics: `linked` = entries linked, `skipped` = entries with no
  usable id (collapsed duplicates count as linked).
- Tests: handler test now asserts all entries linked with no scan jobs,
  uploader channel created, existing video keeps `catalog_position 7`, new
  rows at -1. `go test ./...` green.
