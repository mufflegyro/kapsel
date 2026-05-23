# Add macOS sandbox-exec media tool sandbox

## Summary

Agent Safehouse demonstrates a macOS-native deny-first sandbox using `sandbox-exec` policy profiles. Kapsel should evaluate and, if practical, add an optional macOS `sandbox-exec` backend for local development runs of external media tools such as `yt-dlp` and `ffmpeg`.

## Requirements

- Keep the primary external media tool sandbox portable and Linux-deployable; do not make macOS tooling required for normal Kapsel deployments.
- Implement any macOS-specific sandboxing behind Darwin-specific build tags or an optional runtime backend.
- Use deny-by-default `sandbox-exec` profiles that grant only the command's required inputs, outputs, toolchain/runtime paths, network access where needed, and scratch directories.
- Keep `yt-dlp` behavior intact for downloads and channel scans, including network access and configured cookies file access when needed.
- Keep `ffmpeg` preview behavior intact while limiting it to required media input, output, runtime libraries, and scratch paths.
- Preserve process cancellation behavior from the shared sandbox runner, including subprocess-tree cleanup where feasible on macOS.
- Document that `sandbox-exec` is a macOS-local hardening path and not the Linux deployment sandbox boundary.

## Acceptance Criteria

- A feasibility note documents whether `sandbox-exec` is available and suitable on supported macOS versions used for Kapsel development.
- If implemented, Kapsel can choose a Darwin `sandbox-exec` backend without affecting Linux builds or tests.
- The macOS backend uses generated or templated profiles with explicit read-only and read-write path grants for `yt-dlp` and `ffmpeg`.
- Tests cover profile generation and environment minimization on all platforms, plus Darwin-only execution behavior when `sandbox-exec` is available.
- Documentation references the macOS backend separately from the Linux/systemd sandbox assumptions.

## Notes

- User reference: https://agent-safehouse.dev/
- Agent Safehouse uses `sandbox-exec` with composable deny-first profiles and grants read/write access to a selected workdir while denying sensitive home-directory paths by default.
- The Safehouse LLM instructions reference profile modules such as `00-base.sb`, `10-system-runtime.sb`, `20-network.sb`, toolchain profiles, and wrappers that generate concrete workdir grants at launch time.
- Kapsel can borrow the idea of narrow generated profiles without adopting Agent Safehouse as a runtime dependency.
- This should remain a follow-up to the portable process sandbox unless implementation proves small and isolated.
