# Synchronize denormalized watched state

## Summary

Watched state exists both on `videos.watched` and `user_progress.watched`. Imports can set the video-level flag, while playback progress writes only update `user_progress`, so consumers must remember how to merge both sources.

## Requirements

- Keep watched state monotonic across playback and imports.
- When playback marks a video watched, update denormalized `videos.watched` consistently.
- Preserve imported watched state and local progress semantics.

## Acceptance Criteria

- `PUT /api/videos/{id}/progress` with watched state also keeps `videos.watched` in sync.
- Tests cover imported watched rows, local progress rows, and retention eligibility behavior.
- Existing progress and retention tests pass.

## Notes

- Review references: `internal/database/migrations/001_initial.sql:26`, `internal/database/migrations/001_initial.sql:76`, `internal/server/server.go:1588`, `internal/server/server.go:1893`, and `internal/download/downloader.go:1647`.
- This is likely a small correctness fix with broad confidence payoff.
