# Docker Deployment

Kapsel ships as a single container: one Go binary with the embedded web UI,
one SQLite database, and filesystem media storage. The image bundles
`yt-dlp` (nightly standalone), `ffmpeg`, and Deno (for YouTube JS challenges)
so downloads and timeline previews work without host tools.

The container binds `0.0.0.0:8080` by default (`KAPSEL_ADDR=:8080`) so it can
be published on a home network or sit behind a reverse proxy.

## What's in the image

| Component | Location | Notes |
|-----------|----------|-------|
| Kapsel binary | `/opt/kapsel/kapsel` | Static build, frontend embedded, runs as unprivileged `kapsel` user |
| yt-dlp | `/usr/local/bin/yt-dlp` | Nightly standalone; auto-updated in place by Kapsel (`KAPSEL_YTDLP_UPDATE_INTERVAL`, default `24h`) |
| yt-dlp wrapper | `/usr/local/bin/kapsel-ytdlp` | Selects Deno as JS runtime (`--js-runtimes deno:/usr/local/bin/deno`) |
| Deno | `/usr/local/bin/deno` | JS runtime for current YouTube client challenges |
| ffmpeg | `/usr/bin/ffmpeg` | Timeline previews and format merging |

Storage paths are the named volumes from `docker-compose.yml`:

- `/data` → SQLite database and runtime state (`KAPSEL_DATA_DIR`, `KAPSEL_DB_PATH`)
- `/media` → downloaded videos, thumbnails, subtitles, previews (`KAPSEL_MEDIA_ROOT`)
- `/imports` → allowlisted directory for API-triggered imports (`KAPSEL_IMPORT_ROOT`)

## Quick start

From the repository root:

```sh
cp deploy/docker/kapsel.env.example deploy/docker/kapsel.env
```

Edit `deploy/docker/kapsel.env`:

1. Set `KAPSEL_AUTH_USERNAME` and generate `KAPSEL_AUTH_PASSWORD_HASH` without
   putting the password in shell history:

   ```sh
   read -rsp 'Kapsel password: ' KAPSEL_PASSWORD
   printf '\n'
   printf '%s\n' "$KAPSEL_PASSWORD" | docker compose run --rm kapsel hash-password
   unset KAPSEL_PASSWORD
   ```

2. Replace `KAPSEL_SESSION_SECRET` and `KAPSEL_MEDIA_SIGNING_SECRET` with
   random values (e.g. `openssl rand -base64 32`).

Then build and start:

```sh
docker compose up -d --build
curl http://127.0.0.1:8080/api/health
```

Open `http://127.0.0.1:8080`, log in with the credentials from the env file,
and confirm `/settings` shows auth, signing secrets, storage, `yt-dlp`, and
`ffmpeg` as ready.

## Binding to 0.0.0.0 and security

`KAPSEL_ADDR=:8080` (set in `docker-compose.yml` under `environment`) makes
the HTTP server listen on all container interfaces. The compose `ports`
mapping `"8080:8080"` publishes that on **all host interfaces**, so the
service is reachable from any machine that can route to the host.

**Authentication is required.** The image defaults to `KAPSEL_AUTH_MODE=required`
and the entrypoint warns loudly if you start with auth disabled while bound to
a non-loopback address. Never run with `KAPSEL_AUTH_MODE=disabled` on a
network you do not fully trust — Kapsel has no other built-in access control.

To publish loopback-only (e.g. when a host reverse proxy terminates TLS and
forwards to the container), change the ports mapping in `docker-compose.yml`:

```yaml
    ports:
      - "127.0.0.1:8080:8080"
```

## HTTPS termination

Kapsel serves plain HTTP; TLS is terminated in front of it. Two supported
paths:

### 1. Reverse proxy on the host

Run Caddy (or Traefik/nginx) on the host and forward to the loopback-published
port. With Caddy, set `KAPSEL_SESSION_COOKIE_SECURE=true` and use a
`Caddyfile` like:

```
kapsel.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Caddy obtains a public certificate via ACME when the name resolves publicly,
or you can use `tls internal` for a private name (see below).

### 2. Caddy container on the same Docker network (self-signed or internal CA)

Append a Caddy service to `docker-compose.yml`:

```yaml
  caddy:
    image: caddy:2
    restart: unless-stopped
    ports:
      - "443:443"
      - "80:80"
    volumes:
      - caddy-data:/data
      - ./deploy/docker/Caddyfile:/etc/caddy/Caddyfile:ro
    depends_on:
      kapsel:
        condition: service_healthy
```

with `deploy/docker/Caddyfile`:

```
https://kapsel.lan {
    tls internal
    reverse_proxy kapsel:8080
}
```

`tls internal` serves a locally-trusted certificate; install the Caddy root
CA (`caddy-data/caddy/pki/authorities/local/root.crt`) on devices that should
trust it. For self-signed without the internal CA, generate a certificate
with `openssl req -x509 -newkey rsa:2048 -nodes -days 365 ...` and configure
Caddy with `tls /path/cert.pem /path/key.pem`.

When HTTPS terminates in front of Kapsel, set `KAPSEL_SESSION_COOKIE_SECURE=true`
in `deploy/docker/kapsel.env`. Keep it `false` for plain HTTP on a trusted LAN.

## Volumes and backups

The three named volumes (`kapsel-data`, `kapsel-media`, `kapsel-imports`)
are the required backup set, matching `KAPSEL_DATA_DIR`, `KAPSEL_MEDIA_ROOT`,
and `KAPSEL_IMPORT_ROOT`. They survive `docker compose down` and container
recreation; use `docker compose down -v` only when you intend to wipe them.

Back up the catalog with Kapsel's own backup command (a `VACUUM INTO` snapshot
that is safe to run while the service is up):

```sh
docker compose exec kapsel /opt/kapsel/kapsel backup /media/backups/kapsel-$(date +%Y%m%d-%H%M%S).zip
```

Copy the backup zip and the media volume out of the container (or use volume
snapshots / rsync of a bind mount) to a safe location. Restore by copying the
backup into the container and running:

```sh
docker compose exec kapsel /opt/kapsel/kapsel restore /media/backups/<backup>.zip
```

## Upgrades

Migrations are forward-only and run automatically on startup: a newer image
upgrades older databases. Before upgrading, back up (see above):

```sh
docker compose pull        # or rebuild: docker compose build
docker compose up -d
curl http://127.0.0.1:8080/api/health
```

For rollback, restore a matching database + media backup before running the
older image.

## Verify downloads and playback (smoke test)

A full end-to-end check in the container, using the API (replace the
credentials and video URL):

```sh
# 1. Health and tooling
curl -fsS http://127.0.0.1:8080/api/health
docker compose exec kapsel yt-dlp --version
docker compose exec kapsel ffmpeg -version | head -1

# 2. Login to get a session cookie
curl -fsS -c /tmp/kapsel.cookies -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<password>"}' \
  http://127.0.0.1:8080/api/login

# 3. Enqueue a download
curl -fsS -b /tmp/kapsel.cookies -H 'Content-Type: application/json' \
  -d '{"url":"https://www.youtube.com/watch?v=<VIDEO_ID>"}' \
  http://127.0.0.1:8080/api/downloads

# 4. Poll until the job succeeds
curl -fsS -b /tmp/kapsel.cookies http://127.0.0.1:8080/api/jobs \
  | python3 -m json.tool | grep -E '"type"|"status"' | head

# 5. The video now has a signed media_url; verify HTTP 200 + range 206
curl -fsS -b /tmp/kapsel.cookies http://127.0.0.1:8080/api/videos \
  | python3 -c 'import json,sys; v=json.load(sys.stdin)[0]; print(v["media_url"])' \
  > /tmp/kapsel-media-url
curl -fsSI "$(cat /tmp/kapsel-media-url)"          # expect 200
curl -fsS -H 'Range: bytes=0-1023' -o /dev/null -w '%{http_code}\n' "$(cat /tmp/kapsel-media-url)"  # expect 206

# 6. Downloads land on the mounted volume
docker compose exec kapsel ls -la /media

# 7. Media survives container recreation
docker compose restart
curl -fsS http://127.0.0.1:8080/api/health
docker compose exec kapsel ls -la /media
```

Then open the video in the browser and confirm playback (which exercises
HTTP range requests and the media handler).

A deterministic infrastructure smoke test (fresh volumes, health, migration,
tool availability, media persistence) is automated in
[`scripts/docker-smoke.sh`](scripts/docker-smoke.sh); it does not require
YouTube access.

## Bring in an existing archive (channels, catalog, media)

Kapsel stores `media_path` **relative to `KAPSEL_MEDIA_ROOT`** (e.g.
`ac1JdboPirs.mp4`), so an existing archive keeps working in the container as
long as the same files land at the same media root — no re-linking needed.

Two hard rules before pointing the container at a real archive:

1. **Stop the host server first.** Kapsel takes an exclusive SQLite lock
   (`kapsel.db.lock`); the host binary and the container cannot share one
   database. Stopping the host server also checkpoints the WAL so the main
   DB file is consistent.
2. **Back up first.** Per the project's data-safety guardrails, take a
   verified `kapsel backup` before the container first writes to the archive:
   `./dist/kapsel backup backups/pre-docker-<date>.zip` (host) or
   `docker compose exec kapsel /opt/kapsel/kapsel backup /media/backups/...zip`
   (once it is inside).

### Option A — bind-mount the existing directories (Linux-friendly)

Point the container at the current data and media directories instead of the
named volumes (override in `docker-compose.yml`):

```yaml
    volumes:
      - /var/lib/kapsel:/data
      - /srv/kapsel/media:/media
      - /srv/kapsel/imports:/imports
```

The entrypoint chowns the three top-level mount points to the container's
`kapsel` user at startup. On Linux, align the kapsel uid with a real user;
on macOS bind mounts, `chown` inside the container can shift host file
ownership — prefer Option B there.

### Option B — copy the archive into named volumes (safest, recommended)

1. Stop the host server and back up the database.
2. `docker compose up -d` with fresh named volumes, then stop the service
   again — `kapsel restore` needs the exclusive DB lock, so the server must
   not be running while it writes:

   ```sh
   docker compose stop
   ```

3. Copy the backup zip into the container's `/imports` and restore it
   (a fresh one-shot container mounts the same named volumes):

   ```sh
   docker cp backups/pre-docker-<date>.zip mytube-kapsel-1:/imports/backup.zip
   docker compose run --rm kapsel restore /imports/backup.zip
   docker compose start
   ```

4. Copy the media files into the `/media` volume, keeping `kapsel` ownership
   so retention cleanup can still delete files:

   ```sh
   docker run --rm -u kapsel \
       -v "<project>_kapsel-media:/media" \
       -v "$PWD/test-data/media:/src:ro" \
       --entrypoint sh kapsel:local -c 'cp -a /src/. /media/'
   ```

   `<project>` is the compose project name (the directory name by default,
   `mytube` here; verify with `docker volume ls | grep kapsel-media`).

   The restore replaces only the database; the media volume keeps the files.
   Because paths are stored relative to the media root, playback and previews
   work immediately.

### Option C — re-import just the channel list

For a fresh archive that only needs the channel catalog back (metadata is
re-fetched from YouTube, no media):

```sh
docker compose run --rm -v "$PWD/subscriptions.csv:/imports/subscriptions.csv:ro" \
    kapsel import-subscriptions --scan-only /imports/subscriptions.csv
```

This enqueues `channel_first_download` jobs that rebuild each channel's
catalog from YouTube, like the post-incident rebuild did.

### Import playlists in the container

`kapsel import-playlists` needs the exclusive DB lock, so the server must be
**stopped** while the one-shot import container runs (the metadata-scan jobs
it enqueues are processed by the server's job runner once it starts again).
The default mode enqueues metadata-only scans — no media is downloaded.

1. Back up first (safe while the server runs, `VACUUM INTO`):

   ```sh
   docker compose exec kapsel /opt/kapsel/kapsel backup /media/backups/pre-playlists-$(date +%Y%m%d-%H%M%S).zip
   ```

2. Stop the server to release the lock:

   ```sh
   docker compose stop
   ```

3. Import one or more CSV exports, binding them read-only into `/imports`:

   ```sh
   docker compose run --rm \
     -v "$PWD/playlists/DnB-videos.csv:/imports/DnB-videos.csv:ro" \
     kapsel import-playlists /imports/DnB-videos.csv
   ```

   The report lists `linked` (already in the archive) and `enqueued`
   (metadata-only `video_metadata_scan` jobs for the missing episodes). To
   import several exports in one run, pass each path:
   `kapsel import-playlists /imports/A.csv /imports/B.csv`.

4. Start the server again; its job runner processes the metadata scans:

   ```sh
   docker compose start
   # watch until the scans finish:
   docker compose exec kapsel /opt/kapsel/kapsel storage-report
   ```

5. Re-run the import to link the now-cataloged episodes into the playlist
   (stop, run, start again — same as steps 2–3). The second run reports
   `linked: N` for every episode whose metadata scan succeeded.

   ```sh
   docker compose stop
   docker compose run --rm \
     -v "$PWD/playlists/DnB-videos.csv:/imports/DnB-videos.csv:ro" \
     kapsel import-playlists /imports/DnB-videos.csv
   docker compose start
   ```

Add `--link-only` to skip enqueueing anything, or `--download` to enqueue
full media downloads instead of metadata scans.

## Troubleshooting

- **Downloads fail with `HTTP Error 403: Forbidden`** — the bundled nightly
  yt-dlp is refreshed automatically every `KAPSEL_YTDLP_UPDATE_INTERVAL`; if a
  fresh YouTube client change lands, run `docker compose restart` after the
  update job or rebuild the image to fetch the newest nightly.
- **`yt-dlp` shows an error about a JS runtime** — verify
  `/usr/local/bin/deno` exists (`docker compose exec kapsel deno --version`).
- **Media files not visible after recreation** — confirm the container env
  still points at `/data`, `/media`, `/imports` and those are the mounted
  volumes; changing paths in `deploy/docker/kapsel.env` after first start
  moves the app to fresh (empty) storage.
- **Permission errors on bind mounts** — the entrypoint chowns the three
  mount points to the `kapsel` user at startup; on SELinux hosts add `:z`
  (or `:Z`) to the bind mount.
- **The container is healthy but the host URL shows a different app** —
  another process on the host already listens on the published port (e.g.
  `8080`), and the platform's port-forward routes to it instead. Check with
  `lsof -i :8080` / `ss -tlnp | grep 8080` and publish a free host port:
  `"18080:8080"` in `docker-compose.yml`.
- **Login over HTTPS fails** — set `KAPSEL_SESSION_COOKIE_SECURE=true` when
  TLS terminates in front of Kapsel.
