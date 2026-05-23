# Prioritize same-channel Up next videos

## Summary

The watch page Up next overlay and recommendations should prefer unfinished videos from the same channel before falling back to the broader library.

## Requirements

- Exclude the currently playing video from Up next and recommendations.
- Prefer same-channel videos that are not fully watched, ordered newest first by video date.
- After same-channel unfinished videos, prefer locally available videos ordered newest first.
- After locally available videos, include the remaining videos ordered newest first.
- Use the same ordering for the autoplay Up next target and the recommendations list.

## Acceptance Criteria

- Frontend smoke coverage proves a same-channel unfinished video outranks newer unrelated available videos for Up next.
- Frontend smoke coverage proves the recommendations list uses the same priority order.
- Relevant frontend checks pass.

## Notes

- "Video date" uses the existing published/archived/newest metadata already present in video cards.
- Local availability should use the same frontend signal used for catalog-only display.
- Implemented with a shared ordered candidate list for both the autoplay overlay target and the recommendations sidebar.
- Verified with focused and full browser smoke coverage, `pnpm check`, `go test ./...`, and `git diff --check`.
