# Kapsel

Kapsel is an experimental rewrite of a personal video archive: a small, fast, single-node app for saving, searching, and watching a local collection of online videos.

![Kapsel watch page showing an archived video](docs/kapsel-watch-screenshot.jpg)

The intended stack is:

- Go backend
- Embedded Svelte frontend
- SQLite as the primary database
- SQLite FTS5 for local search
- Filesystem storage for media, thumbnails, subtitles, and derived assets

## Scope

Kapsel targets a single user or small household running one instance on one machine. The first usable version should handle a personal archive within these expected limits:

- Videos: up to 10,000 archived videos
- Channels: up to 1,000 subscribed or indexed channels
- Playlists: up to 1,000 playlists
- Subtitles: searchable when available, with bounded API results
- Comments: optional and searchable when indexed, always paginated or capped

The first usable version should include:

- Importing or adding videos to an archive
- Downloading media and thumbnails through a durable local job queue
- Browsing recent videos, channels, and playlists
- Searching local metadata with SQLite FTS5
- Playing archived media with seek support
- Tracking watched state and playback progress
- Basic subscription scans for channels and playlists
- A migration path for core TubeArchivist archive data

## Goals

- Keep deployment simple: one application plus a media directory and SQLite database.
- Optimize for personal archives with thousands of videos, not distributed enterprise scale.
- Make search, playback, subscriptions, and task progress feel immediate.
- Prefer explicit, boring data structures over extra services.
- Preserve enough import compatibility to migrate from an existing TubeArchivist archive.

## Non-Goals

- No Elasticsearch dependency for the initial design.
- No Redis or external queue dependency for single-node operation.
- No multi-node worker architecture until there is a concrete need.
- No feature parity chase before the core archive loop is fast and reliable.
- No public multi-user permissions model in the first usable version; local personal use comes first.
- No distributed workers, remote object storage, or clustered database support without a documented need.
- No unbounded comments, subtitles, task logs, search results, or page sizes.

## Intentionally Dropped From TubeArchivist First Pass

- Elasticsearch-backed search: SQLite FTS5 is enough for the target archive size and avoids an extra service.
- Redis-backed transient state: playback progress, tasks, notifications, and settings should live in SQLite unless proven otherwise.
- Celery workers: local jobs should run through an in-process worker backed by a durable jobs table.
- nginx `auth_request` media protection: media access should be handled directly with lightweight auth or signed URLs.
- Advanced admin and multi-user management: useful later, but not required for the initial personal archive workflow.
- Full historical task retention: task state should be useful and bounded, not an ever-growing operational log.

## Data Storage Strategy

- SQLite is the source of truth for metadata, settings, jobs, playback progress, and searchable text references.
- Media files, thumbnails, subtitle files, and derived assets live on the filesystem; SQLite stores stable relative paths and asset metadata.
- Video, channel, and playlist IDs use source-specific external IDs so imports stay traceable.
- Large text such as descriptions, subtitles, and comments is normalized into dedicated tables and can be mirrored into `search_documents` for FTS indexing.
- Comments and subtitles must be queried with pagination or caps; no API should return an entire large comment tree by default.
- The database is opened with WAL mode, foreign keys, a busy timeout, immediate write transactions, and a small bounded Go connection pool.

## Database Migrations

Startup runs embedded SQLite migrations on a file-backed database before serving requests or starting the local job runner. Migrations are forward-only: run newer Kapsel binaries to upgrade the schema, and restore a compatible database backup plus any matching filesystem state before running an older binary.

Kapsel records applied migrations in `schema_migrations` and refuses to start when the database contains a schema version newer than the binary understands. Older databases are upgraded in place; current databases are left unchanged.

## Backup And Restore

Use the built-in backup command to snapshot SQLite metadata and restore it on a compatible Kapsel version:

```sh
kapsel backup /path/to/kapsel-backup.zip
kapsel restore /path/to/kapsel-backup.zip
```

The backup zip contains `metadata.json` and a SQLite `kapsel.db` snapshot created with SQLite's online `VACUUM INTO` path. `metadata.json` records the backup format version, schema migration version, creation time, and restore-relevant configuration such as data, database, import, media, auth status, and tool paths. It does not include password hashes or signing/session secrets.

Choose a backup output path outside the configured database and SQLite sidecar paths; Kapsel refuses to write a backup over `KAPSEL_DB_PATH`, its WAL/SHM files, or its runtime lock.

Restore is an offline operation: stop the Kapsel server/job runner before replacing the configured database. The running app holds an exclusive database lock, and restore refuses to run while that lock is held. Restore validates the backup format, schema version, SQLite integrity, and foreign keys before replacement. If the current database has queued or running jobs, restore is refused so pending work is not silently discarded. Use `kapsel restore --force /path/to/kapsel-backup.zip` only after intentionally abandoning those jobs; `--force` does not bypass the running-app lock.

Media files are not bundled into metadata backups. Back up `KAPSEL_MEDIA_ROOT` separately with filesystem tooling such as snapshots, rsync, Time Machine, or your NAS backup system. Restore the database and matching media directory together when you need a fully playable archive. Treat backup archives as sensitive because they contain archive metadata, local paths, playback progress, and other SQLite state.

## Storage Maintenance

Use the storage maintenance commands to inspect filesystem usage and clean unreferenced files conservatively:

```sh
kapsel storage-report
kapsel storage-cleanup
kapsel storage-cleanup --delete --confirm
```

`storage-report` prints referenced media, thumbnail, subtitle, derived asset, and database usage. It also reports files under `KAPSEL_MEDIA_ROOT` that are not referenced by SQLite and metadata rows that point at missing files. `storage-cleanup` defaults to a dry run and only deletes orphan files when both `--delete` and `--confirm` are provided. Cleanup never follows media-root symlinks or deletes files outside the configured media root.

## Search Prototype

Search is backed by SQLite FTS5 through `search_documents_fts`, an external-content index over `search_documents`.
`GET /api/search?q=...` exposes bounded search results over HTTP.

The search response shape is:

```json
{
  "data": [
    {
      "owner_type": "video",
      "owner_id": "youtube-id",
      "field": "title",
      "snippet": "matched <mark>text</mark>",
      "rank": -1.23
    }
  ],
  "limit": 20
}
```

Supported query parameters:

- `q`: required search query.
- `limit`: default `20`, capped at `50`.

Empty queries and queries longer than 512 bytes return `400 Bad Request` with a JSON error body.
Snippets HTML-escape indexed text and preserve only server-generated `<mark>` highlight tags.

Known limitations compared with Elasticsearch:

- Search currently uses simple quoted FTS terms, not fuzzy matching or typo tolerance.
- Ranking uses SQLite `bm25` defaults with no per-field tuning yet.
- Results are intentionally capped at 50 per query.
- Playlist result routes are minimal until full playlist management lands.

## Job Runner Prototype

Jobs are stored in SQLite and processed by an in-process runner. The first retry policy is intentionally simple:

- Jobs start as `queued`, become `running` when claimed, and end as `succeeded`, `failed`, or `cancelled`.
- Claiming a job increments `attempts` and sets `locked_at`.
- Failed jobs return to `queued` until `attempts` reaches `max_attempts`; the next failure marks them `failed`.
- Running jobs can be marked with `cancel_requested`; context-aware handlers receive cancellation and the runner marks the job `cancelled`.
- Stale `running` jobs can be reclaimed after the runner's stale timeout.
- Job state can be queried through `GET /api/jobs/{id}` when the server is configured with a job store.
- Completed jobs can expose structured report details in `result_json`.

## Media Serving Prototype

Media is served through lightweight signed URLs instead of an application auth subrequest for every file.

- Signed media URLs include `expires` and `signature` query parameters.
- Signatures bind the relative media path and expiry using HMAC-SHA256.
- Unauthorized, expired, or path-mismatched requests are rejected before file serving.
- Go's file server handles the actual file response, including HTTP range requests for seeking.
- Media responses currently use `Cache-Control: private, max-age=86400` for cached thumbnails and derived assets.
- The server can mount media with `WithMedia(root, signer)` once application configuration exists.
- Video detail APIs return signed `media_url` fields for playback, while list APIs only return signed thumbnails and metadata needed for cards.
- The URL TTL controls authorization for future media requests; already-fetched private responses may remain in the browser cache for the media response cache lifetime.

## TubeArchivist Import Prototype

The importer reads TubeArchivist JSON backup zip files from `cache/backup`, `backup`, or the provided root directory.

Supported first-pass imports:

- `es_channel-*` documents into channels and channel search documents.
- `es_video-*` documents into videos, media assets, playback progress, and video search documents.
- `es_playlist-*` documents into playlists, downloaded playlist entries, and playlist search documents.
- `es_comment-*` documents into comments and comment search documents.
- Core media and thumbnail paths are copied as metadata references; files are not moved yet.
- Malformed or unsupported records are reported in the import report and do not abort the whole import when safe.

Known skipped fields:

- Full task history, download queue state, schedules, application settings, cookies, and users.
- Rich stream metadata, SponsorBlock segments, subtitle files, and non-core TubeArchivist configuration.
- Filesystem validation and media relocation; this prototype imports metadata only.

Import entry points:

- CLI: `kapsel import-ta <tubearchivist-root>` imports immediately and prints the JSON report.
- API: `POST /api/imports/tubearchivist` with `{"root":"/path/to/tubearchivist"}` enqueues a durable `ta_import` job when the absolute root is inside `KAPSEL_IMPORT_ROOT` and exists as a directory.
- Import jobs and the `import-ta` CLI check configured data-root free-space headroom before importing records into SQLite.
- Import jobs heartbeat progress while running; completion sets `progress` to `1`.
- Job reports are stored in `result_json` and can be read through `GET /api/jobs/{id}` after completion or failure.

## Comment Browsing API

`GET /api/videos/{id}/comments` returns top-level imported comments for a video with bounded pagination. Use `?parent=<comment-id>` to fetch one page of direct replies for a parent comment. Comment trees are intentionally not returned recursively.

## Video Library API

`GET /api/videos` returns a bounded list of videos:

```json
{
  "data": [
    {
      "id": "video-id",
      "title": "Video title",
      "description": "Short or full description",
      "published_at": "2026-05-03",
      "thumbnail_url": "/media/thumbs/video-id.jpg?expires=...&signature=...",
      "archive_state": "downloaded",
      "channel": { "id": "channel-id", "name": "Channel name" },
      "progress": { "position_seconds": 42, "watched": false }
    }
  ],
  "pagination": { "page": 1, "page_size": 20, "total": 1 }
}
```

Supported query parameters:

- `page`: one-based page number, default `1`.
- `page_size`: default `20`, capped at `50`.
- `sort`: `published` or `created`, default `published`.
- `order`: `asc` or `desc`, default `desc`.
- `channel`: filter by channel ID.
- `playlist`: filter by playlist ID.

## Development Workflow

- Work from issues in `meta/issues.md`.
- Use TDD for behavior changes: write or update a failing test first, then implement the smallest fix.
- Make small topical commits early and often.
- Keep commits focused on one issue or one clearly related change.
- Prefer simple designs that can be replaced over abstractions that predict the future.
- Keep page sizes, API responses, and background work bounded from the start.

## Development Commands

- Install pinned tools: `mise install`
- Run tests: `mise run test`
- Start the dev server with rebuild/restart-on-change: `mise run dev`
- Import a TubeArchivist backup: `mise exec -- go run ./cmd/kapsel import-ta /path/to/tubearchivist`
- Create a metadata backup: `mise exec -- go run ./cmd/kapsel backup ./kapsel-backup.zip`
- Restore a metadata backup: `mise exec -- go run ./cmd/kapsel restore ./kapsel-backup.zip`
- Inspect storage maintenance: `mise exec -- go run ./cmd/kapsel storage-report`
- Generate a local account password hash by passing the password on stdin to `kapsel hash-password`; see `docs/deployment.md` for a shell-history-safe example.
- Install frontend dependencies: `mise exec -- pnpm --dir frontend install`
- Install the Playwright Chromium browser: `mise run browser-smoke-install`
- Start the frontend dev server: `mise run frontend-dev`
- Build embedded frontend assets: `mise run frontend-build`
- Run browser smoke tests: `mise run browser-smoke`
- Build a release binary with embedded frontend assets: `mise run release-build`
- Override the listen address: `KAPSEL_ADDR=127.0.0.1:8081 mise run dev`
- Check health: `curl http://127.0.0.1:8080/api/health`

`mise run dev` watches Go sources and frontend source files. On each change it rebuilds the embedded Svelte assets and restarts the Go server.

`mise run browser-smoke` builds the embedded frontend, starts a temporary Kapsel server with deterministic SQLite fixture data, and runs Playwright smoke coverage in desktop and mobile Chromium viewports. The E2E server does not start the job worker, so add-channel coverage verifies queued job UI without making real `yt-dlp` network calls. The browser suite also blocks non-loopback network requests. If Playwright reports a missing browser executable on a fresh machine, install the Chromium test browser once with `mise run browser-smoke-install`.

For container-free local deployment, see `docs/deployment.md`. The release binary embeds the frontend and does not need Go, Node, pnpm, or Vite at runtime.

## Runtime Configuration

Kapsel reads configuration from environment variables and uses development-safe defaults when values are missing.

- `KAPSEL_ADDR`: HTTP listen address, default `:8080`.
- `KAPSEL_AUTH_MODE`: local auth mode, default `required`. Set to `disabled` only for explicit development on a trusted loopback/private machine.
- `KAPSEL_AUTH_USERNAME`: username for the first local account when auth is required.
- `KAPSEL_AUTH_PASSWORD_HASH`: Argon2id password hash for the first local account. Generate one by passing the password on stdin to `kapsel hash-password`.
- `KAPSEL_DATA_DIR`: base runtime data directory, default `data`.
- `KAPSEL_DB_PATH`: SQLite database path, default `$KAPSEL_DATA_DIR/kapsel.db`.
- `KAPSEL_IMPORT_ROOT`: allowlisted root for API-triggered TubeArchivist imports, default `$KAPSEL_DATA_DIR/imports`.
- `KAPSEL_MEDIA_ROOT`: media root directory, default `$KAPSEL_DATA_DIR/media`.
- `KAPSEL_MEDIA_SIGNING_SECRET`: HMAC secret for signed media URLs. When unset, Kapsel generates a random per-process secret, so set this for stable URLs across restarts.
- `KAPSEL_MEDIA_URL_TTL`: signed media URL lifetime, default `24h`.
- `KAPSEL_MIN_FREE_SPACE`: minimum free-space headroom required before downloads and imports start, default `1GiB`. Use `0` to disable the guard; values accept byte units such as `512MiB`, `2GiB`, or `10GB`.
- `KAPSEL_PREVIEWS_ENABLED`: enable ffmpeg-based timeline hover preview generation for newly downloaded videos. When unset, Kapsel enables previews automatically if the configured `ffmpeg` executable is available.
- `KAPSEL_SESSION_COOKIE_SECURE`: set session cookies with the `Secure` attribute for HTTPS deployments, default `false` for local HTTP development.
- `KAPSEL_SESSION_SECRET`: HMAC secret for browser session cookies. Set this before using auth so sessions survive restarts.
- `KAPSEL_SESSION_TTL`: browser session lifetime, default `168h`.
- `KAPSEL_FFMPEG_PATH`: `ffmpeg` executable path used when timeline previews are enabled, default `ffmpeg`.
- `KAPSEL_YTDLP_COOKIES_FILE`: optional Netscape `cookies.txt` path passed to yt-dlp with `--cookies`. Treat this file like a password and keep it outside the repository with restrictive permissions.
- `KAPSEL_YTDLP_FORMAT`: `yt-dlp` format selector for video downloads. The default prefers H.264/AAC MP4 media at or below 720p and falls back to the best format at or below 720p when browser-safe MP4 is unavailable.
- `KAPSEL_YTDLP_PATH`: `yt-dlp` executable path, default `yt-dlp`. Kapsel checks this path with `yt-dlp --version`; the minimum version tested during development is `2026.03.17`.

Startup creates the database parent directory and media root when missing, opens SQLite, runs migrations, wires the job store, starts the local job runner, and mounts signed media serving.

First account setup is environment-driven for now:

1. Generate a password hash with `kapsel hash-password`; pass the password on stdin and avoid putting it in shell history.
2. Set `KAPSEL_AUTH_USERNAME`, `KAPSEL_AUTH_PASSWORD_HASH`, and `KAPSEL_SESSION_SECRET` before starting the server.
3. Leave `KAPSEL_AUTH_MODE=required` for normal use. Set `KAPSEL_SESSION_COOKIE_SECURE=true` when serving Kapsel over HTTPS. Use `KAPSEL_AUTH_MODE=disabled` only as an explicit development mode; mutating and private metadata APIs are otherwise protected by default.

## Readiness Diagnostics

Open `/settings` after first startup to review configured paths, media signing, authentication status, import-root safety, local tool readiness, and storage headroom. The page uses only redacted values and includes a copyable diagnostics block for support/debugging.

`GET /api/health` is a basic liveness endpoint and returns `OK` when the HTTP process can serve requests. Use `GET /api/diagnostics/readiness` for operational readiness. Readiness reports an aggregate `status` plus bounded, redacted checks for database connectivity, applied versus supported schema version, media-root accessibility, configured `yt-dlp` availability/version, and data/media storage headroom. The tool diagnostic checks configured `yt-dlp` with a bounded `--version` command. The storage diagnostic checks available space for the configured data and media roots against `KAPSEL_MIN_FREE_SPACE` and reports low-space warnings.

`GET /api/settings` returns the read-only settings payload used by the page, including redacted configuration and pass/warn/error readiness checks. `GET /api/diagnostics/errors?limit=20` returns recent failed job errors without payloads, capped at 50 rows, with URLs and common secret fields redacted. Kapsel does not auto-update `yt-dlp`; update it with your package manager or configured install method, then re-check readiness.

## Download Job Prototype

`POST /api/downloads` enqueues a non-blocking download job:

```json
{ "url": "https://www.youtube.com/watch?v=..." }
```

The endpoint returns `202 Accepted` with the queued job. The job runner executes `yt-dlp` out of band, so HTTP handlers do not block on network or media processing.
Only absolute `http` and `https` URLs are accepted; unsupported schemes such as `file://` are rejected before a job is queued or executed.
Before starting `yt-dlp`, download and channel-first jobs check `KAPSEL_DATA_DIR` and `KAPSEL_MEDIA_ROOT` for the configured free-space headroom. Low-space jobs fail early with a clear job error and do not start `yt-dlp`.

The initial `yt-dlp` invocation is:

```text
yt-dlp --no-playlist --no-simulate --newline --progress --dump-single-json --write-info-json --write-thumbnail --write-subs --sub-langs all --convert-subs vtt --format <selector> --merge-output-format mp4 --paths <media-root> --output %(id)s.%(ext)s <url>
```

The default `<selector>` is `bv[height<=720][ext=mp4][vcodec^=avc1][acodec=none]+ba[ext=m4a][acodec^=mp4a]/b[height<=720][ext=mp4][vcodec^=avc1][acodec^=mp4a]/b[height<=720][ext=mp4]/best[height<=720]`. Override `KAPSEL_YTDLP_FORMAT` if you prefer a different quality or compatibility tradeoff. Kapsel does not transcode in this path.
When `KAPSEL_YTDLP_COOKIES_FILE` is set, Kapsel adds `--cookies <cookies-file>` to video, subtitle, and channel metadata yt-dlp commands. Export cookies from a browser to Netscape format, install the file outside the repo, and make it readable only by the Kapsel service user.

Successful jobs ingest core metadata into SQLite:

- channel ID and name
- video ID, title, description, publish date, duration
- media path and thumbnail path when reported by `yt-dlp`
- subtitle metadata and searchable subtitle text when subtitles are available
- searchable title/description text
- a completed download record

Before a job is marked `succeeded`, Kapsel validates that `yt-dlp` returned a usable video ID, title, and media path. Media and optional asset paths are normalized under `KAPSEL_MEDIA_ROOT`; traversal paths and absolute paths outside the media root are rejected. The required media file must exist as a regular file. Missing optional assets such as thumbnails are omitted without failing the download.

Ingesting one downloaded video is transactional: Kapsel updates channel, video, asset, search, and download records together after validation passes. Re-downloading the same source video updates the existing archive rows and the existing `downloads` record instead of creating duplicates. The completed job `result_json` reports the `video_id` and whether the archive row was `created` or `updated`. Partial or temporary files left by `yt-dlp` are ignored by ingest until a validated final media file exists under `KAPSEL_MEDIA_ROOT`, so partial downloads are not exposed as playable videos.

When previews are enabled, the download job also runs `ffmpeg` against the validated local media file before the archive row is exposed. Kapsel enables previews by default when `ffmpeg` is available, and `KAPSEL_PREVIEWS_ENABLED=false` disables them explicitly. Kapsel writes a deterministic sprite under `derived/previews/<video-id>/sprite.jpg`, stores timeline cue metadata in SQLite, and returns signed preview metadata on `GET /api/videos/{id}` for the watch page hover overlay. Preview generation is retry-safe because the same video overwrites the same derived sprite and metadata row.

Manual subtitles are downloaded in the main `yt-dlp` invocation and converted to WebVTT when available. If the returned metadata reports original-language automatic captions, Kapsel runs a second subtitles-only command:

```text
yt-dlp --no-playlist --no-simulate --skip-download --dump-single-json --write-auto-subs --sub-langs .*-orig --convert-subs vtt --paths <media-root> --output %(id)s.%(ext)s <url>
```

Kapsel stores subtitle language, source, format, safe relative path, and transcript text for search, but video detail APIs only return bounded track metadata and signed caption URLs. Original-language automatic captions are fetched through the separate subtitles-only command; auto-translated variants are not requested by default.

Failed jobs follow the durable job retry policy. With the default `max_attempts`, failures return to `queued` until attempts are exhausted; then the job becomes `failed` with a sanitized command error. Kapsel redacts URL userinfo, query strings, fragments, and common `Authorization`, `Cookie`, token, key, password, and secret fields from persisted `yt-dlp` errors, but operators should still avoid verbose `yt-dlp` output that may include credentials or local paths. If `yt-dlp` is missing or not executable, the job error points at the configured `KAPSEL_YTDLP_PATH`.

## Channel Add Flow

The library page includes an add-channel form. `POST /api/channels` accepts a channel URL and enqueues a durable `channel_first_download` job:

```json
{ "url": "https://www.youtube.com/@channel" }
```

The job first asks `yt-dlp` for the first channel entry without downloading media:

```text
yt-dlp --flat-playlist --extractor-args youtubetab:approximate_date --playlist-end 1 --dump-single-json <channel-url>
```

It then reuses the single-video download command for the first entry. The frontend polls `GET /api/jobs/{id}` for job state and refreshes the library after success.
Video detail playback uses Video.js v10 HTML custom elements from `@videojs/html`.

## Repository Layout

```text
.
├── AGENTS.md
├── README.md
├── mise.toml
├── cmd/
│   └── kapsel/
├── frontend/
│   ├── e2e/
│   └── src/
├── internal/
│   ├── database/
│   ├── e2e/
│   ├── server/
│   └── web/
└── meta/
    ├── issues.md
    ├── issues_archive.md
    └── issues/
```

This layout is intentionally minimal until the first implementation issue is started.
