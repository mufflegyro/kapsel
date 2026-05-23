# Add paginated video library API

## Summary

Add a bounded API for browsing imported or archived videos.

## Requirements

- Add `GET /api/videos` with pagination.
- Support basic sorting by published date and creation/import date.
- Include minimal channel and playback progress fields needed by the frontend.
- Add optional channel and playlist filters.
- Keep page size capped server-side.

## Acceptance Criteria

- Tests cover default pagination, custom page size, and max page size enforcement.
- Tests cover sorting and at least one filter.
- Response shape is documented.
- The endpoint never returns unbounded video lists.

## Notes

- This should build on the existing `GET /api/videos/{id}` endpoint.
