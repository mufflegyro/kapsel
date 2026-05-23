# Add yt-dlp readiness and version diagnostics

## Summary

Make `yt-dlp` availability and version visible before users attempt downloads or channel scans.

## Requirements

- Check configured `yt-dlp` path and version from the backend.
- Expose readiness state through settings or diagnostics APIs.
- Document minimum tested `yt-dlp` version and update guidance.
- Surface extraction failures with enough context to debug without leaking secrets.
- Keep checks fast and bounded.

## Acceptance Criteria

- Tests cover missing executable, failing executable, and valid version output.
- Settings/readiness UI can show `yt-dlp` status.
- README documents `KAPSEL_YTDLP_PATH` and version expectations.
- Download jobs produce clear errors when `yt-dlp` is unavailable.

## Notes

- Do not auto-update `yt-dlp` in this issue; detect and explain first.
