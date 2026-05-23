# Polish watch player controls

## Summary

Keep watch player controls relevant and compact by hiding subtitle controls when a video has no archived subtitle tracks and replacing the cinema text button with an icon button that uses a tooltip.

## Requirements

- Do not show a captions/subtitles button when the video has no subtitle tracks.
- Keep subtitle toggling available when subtitle tracks exist.
- Replace the visible "Cinema" text control with an icon-only control.
- Preserve accessible labels and tooltip text for entering and exiting cinema mode.

## Acceptance Criteria

- Browser coverage verifies videos without subtitles do not show the captions control.
- Browser coverage verifies videos with subtitles still expose the captions control.
- Browser coverage verifies the cinema control has icon UI with tooltip/accessible labels instead of visible "Cinema" text.
- Existing browser smoke tests continue to pass.
