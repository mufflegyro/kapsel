# Add optional player volume normalization

## Summary

Video volume can vary noticeably between archived media. Add an optional in-browser player control that smooths perceived loudness without modifying stored media files.

## Requirements

- Add a watch-player control near the existing cinema mode control for enabling or disabling volume normalization.
- Implement normalization in the browser using Web Audio API processing so it is reversible and does not alter media files.
- Keep the feature optional, easy to disable, and persisted across videos.
- Preserve normal playback when Web Audio is unavailable or cannot be initialized.
- Avoid connecting the same media element to multiple `MediaElementAudioSourceNode` instances.

## Acceptance Criteria

- The watch player exposes a visible, accessible button for toggling volume normalization.
- When enabled, playback audio is routed through a compressor/limiter-style Web Audio graph.
- When disabled, playback returns to unprocessed audio output.
- The setting persists in local storage and applies to subsequent videos.
- Frontend checks pass after the Svelte change.

## Notes

- This is not true LUFS normalization; it is an optional in-browser dynamics processor for perceived consistency.
- If this is not good enough, a later issue can add server-side loudness analysis and per-video gain metadata.
