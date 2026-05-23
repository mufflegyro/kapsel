# Sandbox external media tools

## Summary

Kapsel runs `yt-dlp` and `ffmpeg` as child processes of the main service. These tools handle untrusted remote media and complex local media files, but currently run with the same process privileges and inherited environment as the application.

## Requirements

- Run external media tools with the least practical privilege and access.
- Prevent child processes from reading app secrets from inherited environment variables.
- Restrict child process filesystem access to required inputs and per-job outputs.
- Keep cancellation reliable for whole subprocess trees, including children spawned by `yt-dlp` or `ffmpeg`.
- Preserve existing download and preview behavior for normal media.

## Acceptance Criteria

- `yt-dlp` and `ffmpeg` commands no longer inherit the full Kapsel environment by default.
- External commands run in a bounded execution context with explicit working directory, filesystem permissions, timeout or cancellation handling, and process-group cleanup.
- Tests cover cancellation terminating subprocess work and command environment minimization where feasible.
- Deployment documentation or service configuration documents the sandbox boundary and remaining assumptions.

## Notes

- Security review severity: High.
- Relevant references: `internal/download/downloader.go:131`, `internal/previews/previews.go:84`, and `deploy/kapsel.service:12-19`.
- Consider systemd transient units, a helper worker, containerized execution, `bubblewrap`, or another local sandbox appropriate for a personal single-host application.
- This can be implemented incrementally, but the issue should not be closed until both `yt-dlp` and `ffmpeg` have materially reduced privilege.

## Advisor Implementation Plan

Consensus direction: implement portable process sandboxing first, keep it small and testable, and treat Linux filesystem isolation (`bubblewrap`, Landlock, or similar) as an optional second stage rather than the first dependency.

### Phase 1: Core Sandbox Package

- Add a small `internal/sandbox` package used by both media tool runners.
- Define a command spec with at least command name, args, working directory, explicit environment, kill grace, and stdout/stderr writers.
- Never inherit the full parent environment by default.
- Always run commands with an explicit working directory.
- Create per-command scratch directories for `TMPDIR`, `XDG_CACHE_HOME`, and `XDG_CONFIG_HOME`.
- On Unix/Linux, start commands in a new process group.
- On cancellation, send `SIGTERM` to the process group, wait briefly, then send `SIGKILL`.
- Return `context.Canceled` or `context.DeadlineExceeded` when cancellation caused termination.
- Keep non-Unix or unsupported-platform behavior build-tagged or gracefully degraded so Darwin tests and development still work.

### Phase 2: Wire `yt-dlp` And `ffmpeg`

- Update `internal/download/downloader.go` `ExecRunner.Run` to use the sandbox runner while preserving the existing stdout/stderr progress pipeline.
- Update `internal/previews/previews.go` `ExecRunner.Run` to use the sandbox runner instead of direct `CombinedOutput`.
- Keep output capture bounded, including ffmpeg failure output.
- Use `MediaRoot` as the normal working directory for media commands unless a more specific per-job directory is introduced.
- Preserve existing download, channel scan, subtitle, and timeline preview behavior.

### Phase 3: Environment Policy

- Use an allowlist rather than a denylist.
- Allow only variables needed by command-line media tools, such as `PATH`, locale variables, `TZ`, and system certificate variables.
- Set sandbox-owned values for temp/cache/config directories.
- Explicitly exclude all `KAPSEL_*` variables and arbitrary parent environment values.
- Do not pass proxy variables by default unless Kapsel explicitly supports proxy configuration later.
- Treat the yt-dlp cookies file as an intentional input passed by command arguments and documented as readable by `yt-dlp` when configured.

### Phase 4: Tests

- Add sandbox package tests proving child commands do not receive `KAPSEL_*` secrets or unrelated parent env values.
- Test that commands run with the configured working directory.
- Test cancellation of a command that spawns a child process, verifying subprocess work is terminated and does not continue after job cancellation.
- Test graceful termination plus kill escalation where feasible.
- Add or update download runner tests to preserve progress parsing/output behavior through the sandbox wrapper.
- Add or update preview runner tests for bounded failure output and sandboxed environment behavior.
- Put Linux-only process group or filesystem-enforcement assertions behind build tags and skip cleanly when the host cannot support them.

### Phase 5: Deployment Documentation

- Document the child-process sandbox boundary in deployment docs.
- Document that media tools run with minimized environment, explicit working directory, per-command scratch dirs, and process-group cleanup on Linux.
- Document remaining assumptions: portable process sandboxing is not a full filesystem sandbox, `yt-dlp` needs network, and the configured cookies file is intentionally readable by `yt-dlp`.
- Keep service-level hardening (`User=kapsel`, `NoNewPrivileges`, `ProtectSystem`, `ReadWritePaths`) as defense in depth.
- If optional `bubblewrap` or Landlock support is added, document how it interacts with systemd namespace restrictions and LXC capabilities.

### Optional Phase 6: Linux Filesystem Isolation

- Do not make Docker, `systemd-run`, or a separate worker service mandatory.
- Consider optional Linux-only filesystem enforcement after the portable sandbox lands.
- `bubblewrap` can provide mount namespace isolation but may require host/LXC namespace support and extra deployment documentation.
- Landlock can provide unprivileged path restrictions on supporting kernels but must gracefully fall back when unavailable.
- If filesystem isolation is added, model explicit read-only inputs and read-write outputs for each command: cookies file read-only for `yt-dlp`, media input read-only for `ffmpeg`, output/scratch directories read-write, and no access to app secrets.

### Non-Goals

- Do not introduce Docker, Kubernetes, Redis, or an external job worker for this issue.
- Do not rely on `exec.CommandContext` alone for cancellation.
- Do not pass `os.Environ()` to media tools.
- Do not run Kapsel as root to use `chroot` or setuid tricks.
- Do not add short hardcoded download timeouts that break large media downloads.

## Implementation Notes

- First implementation should land the backend-shaped `basic` Go sandbox only.
- The `basic` backend is expected to satisfy environment minimization, explicit working directory, scratch directories, and process-group cleanup.
- The `basic` backend records intended file access and network policy but does not enforce filesystem or network isolation.
- The issue should remain open after the `basic` backend if stronger filesystem isolation is still desired for Linux or macOS backends.

## Current Status

- Implemented the portable backend-shaped `basic` sandbox for `yt-dlp` and `ffmpeg`.
- Review findings addressed: relative media/cookie paths are resolved before sandbox execution, pre-canceled contexts do not start tools, and process-group cleanup uses a cached PGID so same-group grandchildren are killed even after the direct child exits.
- Verified with `go test -count=1 ./...`, `go test -count=1 ./meta`, and `git diff --check`.
- Remaining gap: stronger filesystem and network enforcement still needs a backend such as `bubblewrap`, Landlock, or macOS `sandbox-exec`.
