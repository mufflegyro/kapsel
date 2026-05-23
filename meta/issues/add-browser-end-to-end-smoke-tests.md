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
