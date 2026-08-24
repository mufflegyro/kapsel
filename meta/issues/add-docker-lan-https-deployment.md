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