# Import comments with bounded browsing

## Summary

Import archived comments where available and render a paginated comments section on video watch pages.

## Requirements

- Import comment metadata and text from supported TubeArchivist backups.
- Add bounded comment list APIs by video ID.
- Preserve parent/reply relationships without returning unbounded trees.
- Index comment text for search.
- Render comments on the watch page with pagination or load-more behavior.

## Acceptance Criteria

- Tests cover comment import, parent comments, replies, pagination, and search indexing.
- The watch page shows a comments count and first page of comments when available.
- Large comment sets remain bounded by API limits.
- Search can return comment results linked to the source video.

## Notes

- Comment writing is not required; this is archive browsing only.
