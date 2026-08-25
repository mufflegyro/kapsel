# Add browser end-to-end smoke tests

## Summary

Add automated browser smoke coverage for the product-critical web flows.

## Requirements

- Add a browser test runner suitable for Svelte and the Go backend.
- Test home feed rendering, watch page playback shell, search, channel page, and add-channel job status UI.
- Seed deterministic test data without requiring network access.
- Run in CI or through a documented local command.
- Keep tests fast enough for routine development.

## Acceptance Criteria

- A single command runs the browser smoke suite.
- Tests cover desktop and one mobile viewport.
- Tests do not require real `yt-dlp` network calls.
- README documents how to run the browser tests.

## Notes

- Prefer a small smoke suite over exhaustive frontend unit tests at this stage.

## Status 2026-08-25

- Playwright upgraded 1.59.1 → 1.62.0 to match the already-cached chromium
  v1234 browsers (bare `npx playwright install` had pulled mismatched v1234
  builds; the pinned 1.59.1 needed v1217). Suite now runs: 98 passed.
- Added a playlist-CSV-upload smoke test (uses `setInputFiles`, which the
  `wb` WebKit CLI could not do). Passes desktop + mobile.
- **Known failure (pre-existing, not from the playlist feature):**
  `catalog download success snapshots refresh route data once` fails on both
  projects — after a fake-websocket "succeeded" job emission the app never
  re-fetches the video detail. App code unchanged; same emitLiveJobs hook
  passes the other ~90 live-update tests. Suspected Playwright 1.59→1.62
  timing/behavior delta. See DEVLOG 2026-08-25.
