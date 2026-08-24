# Allow deleting channels that only have catalog metadata

## Summary

`DELETE /api/channels/{id}` refused to delete any channel that had video rows, which blocked removing channels whose only content is catalog metadata (no downloaded media). A subscriptions.csv import creates exactly this situation: each imported channel has up to 500 catalog video rows with empty `media_path`. Relax the guard so catalog-only channels can be removed, while still protecting downloaded media and playlists.

## Requirements

- Keep deleting channels that have no videos at all (existing behavior).
- Allow deleting channels whose videos all have empty `media_path` (catalog metadata only), removing those metadata rows and their search documents / media assets in the same transaction.
- Refuse deletion when the channel has any video with downloaded media (`media_path <> ''`) or any playlist.
- Surface a Delete/Remove action in the UI on both the channel list and the channel detail page, matching the Scan channel and Auto-download controls.
- Keep the operation safe and explicit (confirmation before deletion).

## Acceptance Criteria

- Deleting a channel with only catalog metadata returns 204 and removes the channel row, its catalog video rows, and their denormalized rows.
- Deleting a channel with downloaded media or playlists returns 409 and changes nothing.
- The existing empty-channel delete path still works.
- Regression tests cover the catalog-only delete, the media/playlist refusal, and the empty-channel path.

## Notes

- Root cause: the old guard checked for *any* video row, but catalog scans store metadata rows with empty `media_path`, so every imported channel appeared "non-empty".
- The delete does not remove files from disk (it refuses when media exists), so no media-root cleanup is needed.
- Frontend: Remove channel buttons on the channel list and channel detail page; existing backend route is reused.