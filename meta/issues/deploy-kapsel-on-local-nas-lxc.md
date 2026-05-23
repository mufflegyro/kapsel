# Deploy Kapsel on local NAS LXC

## Summary

Deploy Kapsel to a new local-only LXC, expose it through the existing reverse proxy, run it with authentication disabled for the trusted LAN, and import the existing TubeArchivist archive data.

## Requirements

- Keep the deployment runbook local-only and ignored by git.
- Create a new LXC instead of modifying the existing TubeArchivist instance.
- Install the release Kapsel binary and systemd service with explicit data, media, import, and database paths.
- Configure `KAPSEL_AUTH_MODE=disabled` only for this local-only deployment.
- Configure the existing reverse proxy to route a local hostname to the Kapsel LXC.
- Import TubeArchivist data without mutating the TubeArchivist source service or data.

## Acceptance Criteria

- `LOCAL_DEPLOYMENT_RUNBOOK.md` exists locally and is ignored by git.
- The Kapsel LXC serves `/api/health` locally.
- The local hostname reaches the Kapsel web UI from the LAN.
- Settings show auth disabled, media signing configured, storage paths writable, `yt-dlp` available, and `ffmpeg` available.
- TubeArchivist import completes with a reviewed report.
- Imported videos/channels/playlists/comments are visible through the Kapsel UI or APIs.

## Notes

- This is a trusted-LAN deployment, not a public internet deployment.
- Use read-only discovery and explicit confirmation before destructive Proxmox or proxy changes.
- Existing TubeArchivist data is the import source and should remain intact.
- Deployed to a local LXC; `kapsel.service` serves `/api/health` locally and through the reverse proxy.
- Settings/readiness verification passed with auth disabled, media signing configured, a writable import root, writable storage, `yt-dlp`, and `ffmpeg` configured.
- TubeArchivist import succeeded with channels, videos, comments, and skipped malformed/orphaned records reviewed.
- API spot checks confirmed imported channels, videos, comments, thumbnails, and search results are visible.
