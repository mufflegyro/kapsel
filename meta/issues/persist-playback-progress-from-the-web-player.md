# Persist playback progress from the web player

## Summary

Track watched state and playback position from the Video.js player so the archive remembers where each video was left.

## Requirements

- Add bounded API endpoints for reading and updating progress.
- Update progress from the player without excessive write frequency.
- Mark videos watched near completion.
- Restore initial playback position when opening a video.
- Keep progress durable in SQLite.

## Acceptance Criteria

- Tests cover progress upsert, watched-state transitions, invalid payload rejection, and route bounds.
- The watch page resumes from saved progress for archived media.
- Progress updates survive server restart.
- The home grid reflects watched/in-progress state after updates.

## Notes

- Throttle client updates and avoid writing on every `timeupdate` event.
