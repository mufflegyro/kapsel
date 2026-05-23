# Prefer available videos in home watching sort

## Summary

The home `watching` sort should still show unfinished recently watched videos first, but its fallback should prefer locally available videos before catalog-only entries.

## Requirements

- Keep unfinished videos with playback progress first, ordered by most recent progress update.
- After that group, sort locally available videos before catalog-only videos.
- Preserve the existing newest ordering within each fallback group.
- Keep explicit `sort=newest` unchanged.

## Acceptance Criteria

- Home watching-sort tests cover available videos appearing before newer catalog-only videos after the in-progress group.
- Explicit home `sort=newest` still returns true newest ordering.
- Relevant backend tests pass.

## Notes

- The SQL sort uses the persisted `media_path` as the local availability signal, consistent with existing list sorting.
- Implemented by adding an available-media group between the in-progress group and the existing newest fallback.
- Verified with targeted server tests, `go test ./internal/server ./meta`, and `git diff --check`.
