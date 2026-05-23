# Generate timeline hover previews

## Summary

Generate and serve preview thumbnail assets that Video.js can display while hovering or scrubbing over the video timeline.

## Requirements

- Generate timeline preview thumbnails for downloaded videos using a local media tool.
- Store preview sprites or individual preview images under the configured media/derived-assets root.
- Store enough metadata for the player to map timeline time ranges to preview image coordinates or files.
- Expose signed preview asset URLs to the watch page.
- Wire Video.js timeline preview support so users see preview images while hovering or scrubbing the progress bar.
- Keep preview generation durable, observable, and safe to retry.

## Acceptance Criteria

- Tests cover preview metadata generation, path validation, signed preview URLs, and retry/idempotency behavior.
- The watch page receives preview metadata for videos that have generated previews.
- Video.js shows timeline preview thumbnails when preview metadata exists.
- Videos without previews still play normally and do not show broken hover UI.
- Generated preview files cannot escape configured storage roots.

## Notes

- Prefer a browser/player-compatible format such as WebVTT thumbnail cues pointing at a sprite image if Video.js v10 supports it cleanly.
- This may require an `ffmpeg` readiness check; keep that dependency explicit and optional until preview generation is enabled.
