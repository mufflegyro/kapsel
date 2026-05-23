# Add delete video media action

## Summary

Watch pages need an explicit action to remove a downloaded media file while keeping the video's metadata and catalog record.

## Requirements

- Add a watch page button for videos that currently have local media.
- Prompt for confirmation before deleting media.
- Delete only local media data and derived playable state, not the video metadata row.
- Refresh the watch page state after a successful delete so the video becomes metadata-only.

## Acceptance Criteria

- Deleting video media removes the media file reference and local file data while preserving the video record.
- The delete action is confirmed by the user before sending the mutation.
- The watch page no longer offers playback after the delete succeeds, but still displays metadata.
- Backend and frontend regression coverage verifies the media-only delete behavior.

## Notes

- This is different from deleting a video record; the archive metadata should stay available for future re-downloads and catalog browsing.
