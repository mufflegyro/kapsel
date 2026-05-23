# Source-date-first newest sort

## Summary

The `newest` video sort can place videos that only have local indexing fallback dates ahead of videos with real or approximate source dates. This makes recently indexed undated catalog entries look newer than videos with known source dates.

## Requirements

- Prefer videos with source-derived dates when sorting by `newest` or equivalent date sorts.
- Keep catalog-position and local indexing fallback ordering for videos without source-derived dates.
- Preserve existing channel-scoped behavior and pagination bounds.

## Acceptance Criteria

- A video with `published_at` sorts ahead of an undated video whose local fallback date is newer.
- Undated videos still sort predictably by catalog/index fallback after dated videos.
- Relevant backend tests pass.

## Notes

- `published_at` includes exact upload dates and approximate catalog dates from yt-dlp.
