# Local Deployment

> For a container deployment (Docker image with bundled yt-dlp/ffmpeg/deno,
> `0.0.0.0` binding, and mounted media/download volumes), see
> [`DOCKER.md`](../DOCKER.md) instead. This page covers a host install under
> systemd.

Kapsel deploys as one Go binary with embedded frontend assets, one SQLite database, and filesystem media storage. The release binary does not require Go, Node, pnpm, or Vite at runtime.

## Build A Release Binary

From a clean checkout with mise installed:

```sh
mise install
mise exec -- pnpm --dir frontend install --frozen-lockfile
mise run release-build
```

The binary is written to `dist/kapsel`. The release task rebuilds `frontend/` into `internal/web/static/` before compiling so the web UI is embedded in the binary.

## Runtime Dependencies

The running server needs:

- `dist/kapsel` copied to the host, for example `/opt/kapsel/kapsel`.
- `yt-dlp` available at `KAPSEL_YTDLP_PATH` for downloads and channel scans.
- `ffmpeg` available at `KAPSEL_FFMPEG_PATH` when timeline previews are enabled.
- Writable data, media, and import directories.

## Install Binary And Service User

Create the service account before installing files that reference `kapsel` ownership. This example is for systemd-based Linux hosts; adapt the user creation command for your distribution if needed.

```sh
id -u kapsel >/dev/null 2>&1 || sudo useradd --system --user-group --home-dir /var/lib/kapsel --shell /usr/sbin/nologin kapsel
sudo install -d -m 0755 /opt/kapsel
sudo install -m 0755 dist/kapsel /opt/kapsel/kapsel
```

## Required Directories

Use separate persistent directories so upgrades are just binary replacements:

- Data directory: stores SQLite and local runtime state. Example: `/var/lib/kapsel`.
- Media root: stores downloaded videos, thumbnails, subtitles, and derived previews. Example: `/srv/kapsel/media`.
- Import root: allowlisted directory for API-triggered TubeArchivist imports. Example: `/srv/kapsel/imports`.

These paths correspond to `KAPSEL_DATA_DIR`, `KAPSEL_DB_PATH`, `KAPSEL_MEDIA_ROOT`, and `KAPSEL_IMPORT_ROOT`. Treat the data directory and media root as the required backup set.

## First Account

Generate a password hash without putting the password in shell history:

```sh
read -rsp 'Kapsel password: ' KAPSEL_PASSWORD
printf '\n'
printf '%s\n' "$KAPSEL_PASSWORD" | /opt/kapsel/kapsel hash-password
unset KAPSEL_PASSWORD
```

Set these before starting the service:

- `KAPSEL_AUTH_MODE=required`
- `KAPSEL_AUTH_USERNAME=<username>`
- `KAPSEL_AUTH_PASSWORD_HASH=<hash output>`
- `KAPSEL_SESSION_SECRET=<random secret>`
- `KAPSEL_MEDIA_SIGNING_SECRET=<random secret>`

Use `KAPSEL_AUTH_MODE=disabled` only for explicit local development on a trusted machine. Set `KAPSEL_SESSION_COOKIE_SECURE=true` when serving Kapsel over HTTPS.

## Environment File

Start from `deploy/kapsel.env.example` and replace the secrets:

```sh
sudo install -d -m 0750 -o kapsel -g kapsel /etc/kapsel
sudo install -m 0640 -o root -g kapsel deploy/kapsel.env.example /etc/kapsel/kapsel.env
```

Create persistent directories:

```sh
sudo install -d -m 0750 -o kapsel -g kapsel /var/lib/kapsel /srv/kapsel/media /srv/kapsel/imports
```

## systemd Service Example

Install and start the service file after `/etc/kapsel/kapsel.env` has been edited:

```sh
sudo install -m 0644 deploy/kapsel.service /etc/systemd/system/kapsel.service
sudo systemctl daemon-reload
sudo systemctl enable --now kapsel.service
```

Check the service:

```sh
curl http://127.0.0.1:8080/api/health
```

Open `/settings` after first startup to confirm auth, signing, storage, `yt-dlp`, and `ffmpeg` readiness.

## External Tool Sandbox

Kapsel runs `yt-dlp` and `ffmpeg` through an internal sandbox runner instead of inheriting the full service process environment.

The default `basic` backend applies these controls:

- Child processes receive a minimized allowlisted environment, not `/etc/kapsel/kapsel.env` or arbitrary parent variables.
- Per-command scratch directories are used for `HOME`, `TMPDIR`, `XDG_CACHE_HOME`, `XDG_CONFIG_HOME`, and `XDG_DATA_HOME`.
- Commands run with an explicit working directory, normally `KAPSEL_MEDIA_ROOT` for media tools.
- On Unix-like systems, commands run in a new process group; job cancellation requests terminate the process group and then escalate to kill after a grace period.

This backend is process hardening, not a full filesystem sandbox. The child process can still access files that the `kapsel` service user and systemd unit allow. The service file's `User=kapsel`, `NoNewPrivileges`, `ProtectSystem`, and `ReadWritePaths` settings remain the deployment filesystem boundary. `yt-dlp` still requires network access, and the configured cookies file is intentionally passed to `yt-dlp` when enabled.

Kapsel records each media command's intended file access and network policy for stronger future backends. The default `basic` backend does not enforce those file or network grants.

Future Linux or macOS backends can enforce the same command access model with stronger filesystem isolation, such as `bubblewrap`, Landlock, or macOS `sandbox-exec`.

## Upgrades

Kapsel migrations are forward-only. A newer binary upgrades older databases on startup after opening SQLite. If rollback is needed, restore a matching database and media backup before running an older binary.

Recommended upgrade flow:

```sh
sudo systemctl stop kapsel.service
sudo cp -a /var/lib/kapsel /var/backups/kapsel-data-$(date +%Y%m%d%H%M%S)
sudo cp -a /srv/kapsel/media /var/backups/kapsel-media-$(date +%Y%m%d%H%M%S)
sudo install -m 0755 dist/kapsel /opt/kapsel/kapsel
sudo systemctl start kapsel.service
curl http://127.0.0.1:8080/api/health
```

For large media roots, use filesystem snapshots or incremental backup tooling instead of `cp -a`. Always stop the service or otherwise quiesce SQLite before copying the database files.
