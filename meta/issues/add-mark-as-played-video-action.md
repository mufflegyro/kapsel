# Add mark as played video action

## Summary

Add a direct action on the video detail page that lets a user mark the current video as played without needing to scrub or finish playback.

## Requirements

- Show a mark-as-played action on video detail pages for playable videos.
- Persist the same watched/progress state used by playback completion.
- Update the visible page state after the action succeeds.
- Avoid showing an unnecessary action once the video is already watched.

## Acceptance Criteria

- The video detail page includes a working "Mark as played" control for unwatched videos.
- Activating the control stores watched progress for the current video.
- The UI reflects the watched state after the request succeeds.
- Relevant frontend/backend checks pass.

## Notes

- Use the existing playback progress API if it already supports writing watched state directly.
