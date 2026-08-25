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
