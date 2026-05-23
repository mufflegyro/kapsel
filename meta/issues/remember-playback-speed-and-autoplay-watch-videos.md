# Remember playback speed and autoplay watch videos

## Summary

Make watch-page playback feel continuous by reusing the user's selected playback speed and attempting playback automatically when a video page opens.

## Requirements

- Persist the user's playback speed selection in the frontend.
- Apply the persisted playback speed when loading videos.
- Attempt to play videos automatically when a watch page opens.
- Keep saved playback-position restoration before autoplay.
- Ignore browser autoplay rejections without surfacing errors.

## Acceptance Criteria

- Browser smoke coverage proves a changed playback rate is restored on a later watch-page visit.
- Browser smoke coverage proves video page load attempts playback.
- Existing playback progress behavior remains covered.

## Notes

- Browser autoplay policies may still reject unmuted playback in some contexts; the app should make the attempt and fail quietly.
