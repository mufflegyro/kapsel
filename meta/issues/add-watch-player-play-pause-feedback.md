# Add watch player play/pause feedback

## Summary

Kapsel should show a transient centered play or pause icon when playback starts or pauses, similar to YouTube's player feedback.

## Requirements

- Show a pause icon briefly when playback is paused.
- Show a play icon briefly when playback resumes.
- Fade the feedback out quickly after the play or pause state change.
- Do not block player controls or playback interaction.

## Acceptance Criteria

- Browser smoke coverage verifies initial/autoplay start is quiet, play-after-pause and pause feedback appear, and the feedback fades.
- Frontend checks pass.

## Notes

- Keep this as lightweight local UI feedback rather than introducing another player dependency.
