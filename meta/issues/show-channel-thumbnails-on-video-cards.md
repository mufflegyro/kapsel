# Show channel thumbnails on video cards

## Summary

Video cards in the archive feed show channel initials even when the channel has a thumbnail image available.

## Requirements

- Include channel thumbnail URLs in video list responses.
- Render the channel thumbnail image on video cards when available.
- Keep the initials fallback for channels without thumbnails.

## Acceptance Criteria

- Backend coverage proves video list items include channel thumbnail URLs.
- Frontend smoke coverage or existing checks verify video-card avatars still render correctly.

## Notes

- The watch page channel lockup already renders channel thumbnails, so this should reuse the existing channel thumbnail API/media behavior.
