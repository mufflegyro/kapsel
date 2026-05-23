# Remove media URLs from list responses

## Summary

Video list responses currently include signed playable media URLs even though cards only need metadata, thumbnails, archive state, and progress. This couples broad list pages to media serving and hands out playback URLs for many videos at once.

## Requirements

- Stop returning `media_url` from list-style DTOs.
- Keep `media_url` on video detail responses where playback happens.
- Preserve `archive_state`, `can_download`, thumbnails, and progress for list cards.
- Verify frontend list views do not depend on `media_url`.

## Acceptance Criteria

- Home, channel, playlist, search-hydrated lists, and Up next cards render without list `media_url` fields.
- `GET /api/videos/{id}` still returns a playable `media_url` when media is available.
- Backend tests cover list responses and detail response separately.

## Notes

- Review references: `internal/server/server.go:1389`, `internal/server/server.go:1734`, `internal/server/server.go:1735`, and `frontend/src/components/VideoCard.svelte`.
- This reduces the blast radius of list endpoints and keeps playback authorization scoped to selected videos.
