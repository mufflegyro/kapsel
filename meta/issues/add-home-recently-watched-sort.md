# Add home recently watched sort

## Summary

The home page should default to showing videos that were watched most recently and are not finished before the rest of the archive.

## Requirements

- Add a home-page sort mode that prioritizes downloaded videos with recent unfinished playback progress.
- Sort those unfinished videos by most recent progress update first.
- Keep finished videos out of the prioritized in-progress group.
- Make the new sort the default for the home page.
- Preserve existing bounded pagination behavior.

## Acceptance Criteria

- Home API sorting has regression coverage for recently watched unfinished videos appearing first.
- The frontend uses the new sort as the home page default.
- Relevant backend and frontend checks pass.

## Notes

- "Unfinished" should use the existing watched/completion state rather than adding a new progress model.
- Implemented with the `watching` sort, which prioritizes downloaded videos with positive playback progress, `watched = 0`, and newest `user_progress.updated_at` first.
- Verified with targeted server tests, `go test ./...`, `pnpm check`, `pnpm browser-smoke`, and `git diff --check`.
