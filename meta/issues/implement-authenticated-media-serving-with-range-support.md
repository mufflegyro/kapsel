# Implement authenticated media serving with range support

## Summary

Serve archived media and cached assets efficiently without routing every file request through an expensive application auth endpoint.

## Requirements

- Serve video files with HTTP range support.
- Serve thumbnails and derived assets with appropriate cache headers.
- Protect media access using a lightweight authenticated path or signed URLs.
- Add tests for range responses and unauthorized access.

## Acceptance Criteria

- Video seeking works through range requests.
- Thumbnail requests do not trigger heavy backend work.
- Unauthorized users cannot access protected media.
- Cache behavior is documented.

## Notes

- This issue exists because the source project's media auth path was a major suspected performance problem.
