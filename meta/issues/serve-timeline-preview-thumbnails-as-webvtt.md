# Serve timeline preview thumbnails as WebVTT

## Summary

Expose generated timeline preview sprites through a backend WebVTT thumbnail track so the Video.js control timeline, not the whole player surface, owns hover previews.

## Requirements

- Add a backend endpoint that returns WebVTT cues for a video's generated timeline preview sprite.
- Include a backend VTT URL in video detail responses when preview metadata exists.
- Wire the frontend player to provide the VTT as a `kind="metadata"` track with label `thumbnails`.
- Remove the custom whole-player hover preview overlay.

## Acceptance Criteria

- Backend tests cover the WebVTT response format and signed media sprite references.
- Video detail responses expose a VTT URL for videos with previews.
- Video.js receives a metadata thumbnails track and videos without previews still play normally.

## Notes

- Video.js v10 `media-slider-thumbnail` discovers thumbnail cues from a metadata track labelled `thumbnails`.
