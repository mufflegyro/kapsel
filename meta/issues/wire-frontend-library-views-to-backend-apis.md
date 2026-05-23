# Wire frontend library views to backend APIs

## Summary

Connect the Svelte shell to the video library API so imported videos are visible in the browser.

## Requirements

- Fetch videos from `GET /api/videos` on the library route.
- Render loading, empty, error, and populated states.
- Add a video detail route that uses `GET /api/videos/{id}`.
- Keep the layout usable on desktop and mobile.

## Acceptance Criteria

- Frontend build succeeds.
- Library route renders fixture API data in a browser-testable shape.
- Empty and error states are visible and understandable.
- No UI state change triggers duplicate unnecessary fetches.

## Notes

- Keep styling simple but avoid generic placeholder slop.
