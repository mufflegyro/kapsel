# Build channel and playlist management

## Summary

Make channels and playlists first-class browsable objects instead of only metadata attached to videos.

## Requirements

- Add paginated channel list APIs.
- Add paginated playlist list and detail APIs.
- Add frontend pages for channels, playlists, and playlist videos.
- Surface subscription state where available.
- Support deleting or hiding local channel and playlist metadata only when safe.

## Acceptance Criteria

- Tests cover channel list pagination, playlist list pagination, and playlist detail video ordering.
- The sidebar or account area links to channels and playlists.
- Channel and playlist pages handle empty states and large collections.
- Playlist video results remain bounded.

## Notes

- Avoid destructive media deletion in this issue unless explicitly scoped later.
