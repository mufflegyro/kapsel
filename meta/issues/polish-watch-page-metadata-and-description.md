# Polish watch page metadata and description

## Summary

Improve the watch-page channel lockup and description area so archived video details feel more useful and less visually rough.

## Requirements

- Style the watch description scrollbar to match the app theme.
- Link URLs in video descriptions.
- Link video timestamps in descriptions so they seek the current player.
- Show the channel thumbnail image in the watch channel lockup when available.
- Remove saved playback-position text from the description metadata line.

## Acceptance Criteria

- Watch-page descriptions render URLs and timestamps as actionable links/buttons.
- Timestamp activation seeks the watch-page video.
- Channel lockup displays thumbnail imagery when available and falls back to initials otherwise.
- The metadata line no longer includes playback position text.

## Notes

- Keep the description rendering safe and avoid injecting raw HTML.
