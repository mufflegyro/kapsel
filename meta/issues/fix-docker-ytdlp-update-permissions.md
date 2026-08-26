# Fix yt-dlp auto-update in the Docker image (permissions)

## Summary

The Docker image's auto-update job (`KAPSEL_YTDLP_UPDATE_INTERVAL=24h`) fails
with:

```
ERROR: Insufficient permissions to write to /usr/local/bin/yt-dlp (exit status 100)
```

Root cause: the container runs as the non-root `kapsel` service user
(`setpriv --reuid=kapsel` in `docker-entrypoint.sh`), but yt-dlp is installed
at `/usr/local/bin/yt-dlp` — a root-owned file in a root-owned directory.
`yt-dlp --update-to nightly` renames a temp file over its own executable, so
it needs write access to **both** the file and its directory; neither is
granted to `kapsel`. The wrapper's comment assumed "/usr/local/bin must
remain writable in the container (it is a writable layer by default)" — the
writable layer does not help when file and directory are root-owned and the
process runs unprivileged.

Not a regression: the Docker deployment is a single commit (dc1cfe1, 2026-08-24)
that shipped the non-root user together with the 24h update interval; the
Dockerfile has not changed since. The update has never been able to write in
place inside the container.

## Fix

Move the yt-dlp nightly binary from `/usr/local/bin/yt-dlp` to
`/var/lib/kapsel/bin/yt-dlp`, a directory owned by the `kapsel` service user
(file and directory both writable by `kapsel`, so the in-place update works).
The wrapper `/usr/local/bin/kapsel-ytdlp` execs the new path; the wrapper
path (`KAPSEL_YTDLP_PATH`) and the sandbox ReadWrite grant (computed from the
binary's directory) need no other change. Deno stays in `/usr/local/bin`
(root-owned is fine — only yt-dlp is updated in place).

## Acceptance Criteria

- `docker exec -u kapsel <container> sh -c "test -w /var/lib/kapsel/bin/yt-dlp && echo x > /var/lib/kapsel/bin/.smoke-write && rm -f /var/lib/kapsel/bin/.smoke-write"` succeeds (file write + directory write).
- `yt-dlp --update-to nightly` (or the `ytdlp_update` job) run as the `kapsel`
  user no longer fails with the permissions error.
- yt-dlp readiness check still passes and reports the bundled nightly version.
- Covered by `scripts/docker-smoke.sh` (new step-5 assertion), which builds
  the image and verifies the update target is writable by the service user.
