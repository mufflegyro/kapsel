# Hide members-only videos from all views

## Summary

After marking members-only videos, they still appear in every browse view (home, library, channel page, playlist, search, up-next) as catalog-only clutter that can never be downloaded or watched. Hide members-only videos from all video list views so only downloadable/watchable content shows.

## Requirements

- Exclude `members_only = 1` videos from the home feed, library list, channel video list, playlist video list, search results, and up-next candidates.
- Keep the video rows in the database (they are catalog metadata); only filter them from list/search queries.
- Keep a members-only video reachable when opened directly by URL (e.g. watch page), so its metadata and "Members only — join the channel to watch" state remain inspectable.

## Acceptance Criteria

- All video list endpoint responses exclude members-only videos.
- Search results do not include members-only videos.
- Pagination totals reflect the filtered set.
- Opening a members-only video by its direct URL still works and shows the members-only state.
- Covered by server/search regression tests.

## Notes

- This complements `mark-members-only-videos.md`: marking persists the flag; this issue hides flagged videos from views.
- Members-only can never be downloaded, so hiding is safe browsing behavior; direct-URL access preserves metadata visibility.