# Fix subtitle display toggling

## Summary

Subtitle rendering duplicates captions after disabling and re-enabling them, captions render too narrowly, and caption display should start disabled unless the user explicitly enables it.

## Requirements

- Do not show subtitles by default on first playback.
- Remember the user's subtitle enabled/disabled choice across watch navigation.
- Avoid duplicate caption overlays after toggling captions off and on.
- Let caption overlays use the full player width.

## Acceptance Criteria

- Browser coverage verifies captions start disabled with no saved preference.
- Browser coverage verifies enabling captions persists and restores on the next watch page load.
- Browser coverage verifies repeated caption toggles leave only one active showing text track.
- Frontend checks and browser smoke tests pass.

## Notes

- Match the existing persistence pattern used for cinema mode and playback speed.
