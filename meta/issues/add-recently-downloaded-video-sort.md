# Add recently downloaded video sort

## Summary

Add a video sort order that shows persisted downloaded/imported videos by most recent download/import time.

## Requirements

- Expose a `downloaded` video sort value through the existing home and channel video list APIs.
- Order videos with persisted media rows before undownloaded catalog-only entries.
- Use the best available persisted download/import timestamp for stable newest-first ordering.
- Add the sort option to the frontend video sort selector.

## Acceptance Criteria

- Backend tests cover the recently downloaded sort order.
- The frontend sort selector includes a clear Recently Downloaded option.
- Existing sort aliases and defaults continue to work.
- Relevant Go and frontend checks pass.

## Notes

- Keep the change within the existing paginated video list query path.
