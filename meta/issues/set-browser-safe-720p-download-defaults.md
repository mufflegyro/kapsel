# Set browser-safe 720p download defaults

## Summary

Default downloads to browser-friendly media at or below 720p so newly archived videos are likely to play in the web UI.

## Requirements

- Add explicit `yt-dlp` format selection for default downloads.
- Prefer 720p-or-lower browser-playable formats when available.
- Keep the default configurable for users who want higher quality later.
- Apply the same default to direct video and channel-triggered downloads.
- Document the quality and compatibility policy.

## Acceptance Criteria

- Tests assert generated `yt-dlp` commands include the configured format selector.
- Default configuration targets 720p-or-lower playback-compatible media.
- Existing download and channel-first tests continue to pass.
- README documents the default quality and override mechanism.

## Notes

- Avoid introducing transcoding as part of this issue unless a later product decision requires `ffmpeg`.
