# Add up-next autoplay

## Summary

When a video finishes playing, queue the next video in the current sort order and start it automatically after a 5-second countdown.

## Requirements

- When a video ends, show an "Up next" overlay with the next video's title and a countdown.
- The countdown starts at 5 seconds and auto-plays the next video when it reaches zero.
- The user can cancel the countdown to stay on the current video.
- The user can click "Play now" to skip the countdown.
- Escape key cancels the countdown.
- The next video is the item after the current video in the library sort order; falls back to the first different video.
- If there is no next video, no overlay appears.
- The countdown target is captured at start to avoid race conditions with library refreshes.

## Acceptance Criteria

- [x] Finishing a video shows an "Up next" card with the next video title and a 5-second countdown.
- [x] Clicking cancel dismisses the overlay and stays on the current video.
- [x] Clicking "Play now" immediately navigates to the next video.
- [x] Escape key cancels the countdown.
- [x] When the countdown expires, the next video loads and begins playback.
- [x] No overlay appears when there is no next video.
- [x] Overlay is accessible (role=dialog, aria-modal=false, aria-label, tabindex, Escape support).
- [x] Timer is cleaned up on route change, cancel, play-now, and component destroy.
- [x] Browser smoke covers the countdown, cancel, play-now, and autoplay behavior.

## Implementation

- `frontend/src/App.svelte`: upNextCountdown/upNextTimer/upNextTarget state, startUpNext/cancelUpNext/playUpNextNow functions, overlay markup with role=dialog.
- `frontend/src/style.css`: .up-next-overlay, .up-next-countdown-ring, .up-next-info, .up-next-actions styles.
- `frontend/e2e/smoke.spec.js`: two test cases for cancel/play-now and auto-navigate.
- `internal/web/static/`: rebuilt Vite assets.
