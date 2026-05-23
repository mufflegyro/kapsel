# Add cinema mode to the watch player

## Summary

Add a player-control toggle that expands the watch video to full content width and moves the Up next queue below the player.

## Requirements

- Add a cinema mode control to the video player controls.
- Toggle cinema mode without interrupting playback.
- Expand the player to the full watch-page content width in cinema mode.
- Move the Up next recommendations below the player in cinema mode.
- Keep the normal two-column watch layout when cinema mode is off.

## Acceptance Criteria

- Browser smoke coverage proves the cinema control toggles the watch page layout.
- The player spans the watch page width in cinema mode.
- The Up next recommendations render below the player in cinema mode.
- The normal watch layout can be restored.

## Notes

- This should behave like YouTube theater/cinema mode, not browser fullscreen.
