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

## Media Retention

A retention job runs daily and deletes local media files the archive no longer keeps. Removal takes the media file and the record of the downloaded media — its `media_assets` entry and the video's media columns — while the video record (metadata, watch state, download history) stays, so anything cleaned up can be re-downloaded later. Marking a video as **Keep forever** always protects it from cleanup.

- **Watched media is cleaned up.** Once a video is marked watched — via the watch toggle or by watched playback progress — its media is removed at the next daily run after `KAPSEL_RETENTION_WATCHED_AFTER` (default `24h`). This applies to every media origin: channel auto-downloads, manual downloads, and imports. The timer restarts on any watch-progress write, so a video being re-watched is not deleted mid-playback. Set `KAPSEL_RETENTION_WATCHED_AFTER=0s` to disable watched-media cleanup entirely.
- **Stale channel auto-downloads are cleaned up.** Unstarted, unwatched auto-downloads beyond the newest 2 per channel are removed once older than 14 days. This rule never touches manual or imported media.

The asymmetry is deliberate: media that was started but never finished, and unwatched manual or imported media, stay until they are watched (or marked Keep forever). Retention only ever shrinks what is already watched or superseded.

## Upgrades

Kapsel migrations are forward-only. A newer binary upgrades older databases on startup after opening SQLite. If rollback is needed, restore a matching database and media backup before running an older binary.

### In-App Self-Update

Kapsel can install its own releases. A background scheduler checks the GitHub repository configured with `KAPSEL_UPDATE_REPO` (default `mufflegyro/yummle`) every `KAPSEL_UPDATE_CHECK_INTERVAL` (default `24h`; set `0s` to disable the scheduled check — the manual "Check now" button in Settings still works).

Discovered updates never install on their own. A pending offer appears in **Settings → Updates**, where an admin can approve or dismiss it. Approving enqueues an apply job that:

1. Re-fetches the release by tag and refuses any tag or platform-asset mismatch.
2. Verifies the downloaded binary's SHA-256 against the release's `checksums.txt`.
3. Writes a full database backup into `<data dir>/backups/` and aborts the update if that backup fails or is empty.
4. Atomically replaces the binary (the previous version is kept next to it as `kapsel.previous`) and restarts the server in place.

Failure handling: the pipeline aborts before touching the binary when anything upstream fails, so a GitHub outage, a corrupted download, or a failing backup leaves the running archive untouched. Interrupted attempts are retried by the job runner (with exponential backoff across failed checks) and can be re-approved from the same panel. Each apply keeps up to 5 `pre-update-*.zip` snapshots in the backups directory; older ones are pruned automatically.

Trust model note: the SHA-256 sidecar verifies the download was not corrupted, not that the release itself is trustworthy — the sidecar comes from the same release, so a compromised release/repo account would pass verification. Admin approval is the human trust gate. (Signing releases with minisign/cosign and pinning an out-of-band public key would harden this, but is out of scope for a single-operator archive.)

Notes:

- The binary path must be writable by the kapsel service user. In the Docker image the binary lives at root-owned `/opt/kapsel/kapsel`, so self-update reports a permission error there — update containers by pulling a new image instead.
- If the process dies between the binary swap and the database commit, the next attempt reconciles: it sees the running binary already reports the target version and records the offer applied without downloading or swapping again (preserving the `.previous` rollback copy).
- Development builds (no stamped version) never check or install updates.

### Publishing A Release

Releases are built and published by the `Release` workflow
(`.github/workflows/release.yml`) when a `v*` tag is pushed:

```sh
git tag v1.2.0
git push origin v1.2.0
```

The workflow runs `go vet` and the test suite first — a release never
publishes from a red build — then builds the embedded frontend,
cross-compiles statically linked (CGO_ENABLED=0) plain `kapsel_<os>_<arch>`
binaries for linux/amd64, linux/arm64, darwin/amd64, and darwin/arm64 with
the tag stamped as the version, generates `checksums.txt`
(`"<sha256>  <asset>"` per line), and attaches all artifacts to the GitHub
release. Those names and formats are what the updater's asset selection and
checksum verification expect — a release without the platform binary or its
checksum line is skipped (or fails verification) rather than installed.
Manual workflow runs build the same artifacts without publishing, for
inspecting a candidate build (stamped `dev`, so they are never eligible for
in-app updates).

The release body comes from `release-notes/<tag>.md` in the tagged commit
when that file exists (e.g. `release-notes/v1.2.0.md` for the `v1.2.0`
tag); otherwise GitHub generates notes from the commit list.

Recommended manual upgrade flow:

```sh
sudo systemctl stop kapsel.service
sudo cp -a /var/lib/kapsel /var/backups/kapsel-data-$(date +%Y%m%d%H%M%S)
sudo cp -a /srv/kapsel/media /var/backups/kapsel-media-$(date +%Y%m%d%H%M%S)
sudo install -m 0755 dist/kapsel /opt/kapsel/kapsel
sudo systemctl start kapsel.service
curl http://127.0.0.1:8080/api/health
```

For large media roots, use filesystem snapshots or incremental backup tooling instead of `cp -a`. Always stop the service or otherwise quiesce SQLite before copying the database files.
