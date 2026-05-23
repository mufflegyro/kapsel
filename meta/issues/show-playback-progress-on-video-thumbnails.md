# Show playback progress on video thumbnails

## Summary

Show a subtle playback progress bar along the bottom edge of video thumbnails in overview grids and lists, similar to YouTube, so users can quickly see which videos have been started.

## Requirements

- Render a progress bar on video cards when local playback progress exists.
- Use watched/progress metadata already returned by video list APIs when possible.
- Use a red played segment and muted gray remaining segment without obscuring duration badges.
- Hide the bar for videos with no meaningful progress.
- Keep the indicator accessible and responsive on desktop and mobile.

## Acceptance Criteria

- Browser coverage verifies a started video card shows a progress indicator.
- Browser coverage verifies an unstarted video card does not show a progress indicator.
- The indicator appears in the library feed and channel video grids.
- The indicator updates after playback progress is persisted and the user returns to an overview.

## Notes

- This is a UI follow-up to make the retention policy's "started" concept visible.
- Avoid adding a heavy progress UI; the goal is a subtle thumbnail-edge indicator.
