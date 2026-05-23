# Make playback watched state monotonic

## Summary

Concurrent or out-of-order progress updates can clear `watched=true` because watched preservation is computed before the UPSERT.

## Requirements

- Preserve `watched=true` at the SQL conflict update level.
- Keep near-completion behavior that marks videos watched.
- Avoid allowing stale progress writes to downgrade watched state.

## Acceptance Criteria

- Once a video is watched, later progress writes cannot set it back to unwatched through the progress API.
- Concurrent or out-of-order progress regression coverage is added.
- Existing progress position and duration behavior remains compatible.

## Notes

- Review references: `internal/server/server.go:1605-1657`.
