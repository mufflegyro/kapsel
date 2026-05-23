# Generate signed media URLs in API responses

## Summary

Return short-lived signed media and thumbnail URLs from API responses so the frontend can play and display archived files.

## Requirements

- Add URL generation for video media paths and thumbnail paths.
- Include signed URLs in video detail and video list responses.
- Keep signing expiry configurable.
- Avoid exposing raw filesystem paths directly to the frontend.

## Acceptance Criteria

- Tests verify generated URLs work with the media handler.
- Tests verify expired or tampered URLs are rejected.
- Video API responses include signed URLs when paths are available.
- API documentation describes expiry and cache behavior.

## Notes

- This builds on the signed media serving prototype.
