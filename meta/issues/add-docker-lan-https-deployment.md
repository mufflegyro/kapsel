# Add a Docker deployment for LAN/HTTPS use

## Summary

Add a Docker deployment that binds Kapsel on `0.0.0.0` so it can sit behind a local reverse proxy or be reached on a home network, and points download/media storage at a mounted volume.

## Requirements

- Provide a `Dockerfile` (or compose service) that builds the release binary with the embedded frontend.
- Bind configurable to `0.0.0.0` (e.g. `KAPSEL_ADDR=:8080`) for reverse-proxy/home-network use, and document the security implications (auth required).
- Mount a volume for `KAPSEL_MEDIA_ROOT` and keep the SQLite DB on a separate mounted data volume.
- Run `yt-dlp` and `ffmpeg` inside the image (or document a sidecar) so downloads and previews work without host tools.
- Provide `docker-compose.yml` with env passthrough, a healthcheck, and restart policy.
- Document HTTPS termination: either bind to the LAN and put a reverse proxy (Caddy/Traefik/nginx) in front, or document self-signed generation for local `https://<hostname>` use.

## Acceptance Criteria

- `docker compose up` starts Kapsel bound on `0.0.0.0` with the embedded UI.
- Downloads land on the mounted media volume and survive container recreation.
- The container starts a fresh DB on first run and migrates on upgrade.
- Auth is documented as required when binding to a non-loopback interface.
- A smoke test or documented manual verification path covers a download + playback in the container.

## Notes

- Existing packaging issue `package-kapsel-for-local-deployment.md` covers builds and service examples; this issue is the Docker-specific bind/mount/HTTPS path.
- The nightly yt-dlp wrapper (`bin/kapsel-ytdlp`) must be made available inside the image; the image should bundle or fetch a current yt-dlp so updates keep working.
- Consider `KAPSEL_YTDLP_UPDATE_INTERVAL` (auto-update) so the container keeps yt-dlp current.

## Implementation plan (2026-08-24)

- [x] `deploy/docker/Dockerfile`: multi-stage (node → frontend embed, golang → static binary, debian-slim runtime with ffmpeg + curl + deno + yt-dlp nightly standalone).
- [x] `deploy/docker/kapsel-ytdlp`: Linux wrapper mirroring `bin/kapsel-ytdlp`, selecting the bundled nightly and Deno JS runtime.
- [x] `deploy/docker/docker-entrypoint.sh`: create/chown mount points, drop privileges to a non-root `kapsel` user.
- [x] `deploy/docker/kapsel.env.example`: container paths (`/data`, `/media`, `/imports`), `KAPSEL_ADDR=:8080` (0.0.0.0), wrapper yt-dlp path.
- [x] `docker-compose.yml`: build context repo root, env_file, `0.0.0.0:8080:8080` publish, named volumes for data/media/imports, healthcheck, restart policy.
- [x] `.dockerignore` at repo root (data, test-data, dist, node_modules, playlists, subscriptions.csv, backups).
- [x] `DOCKER.md`: quick start, auth-required warning for non-loopback binding, HTTPS termination (Caddy reverse proxy + self-signed), upgrade/backup, download+playback verification path.
- [x] `scripts/docker-smoke.sh`: fresh-volume startup, health, migration, tool availability, media persistence across recreation.
- [x] Build image + run smoke locally, then commit.