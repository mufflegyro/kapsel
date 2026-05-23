# Show Up next thumbnail background

## Summary

The Up next autoplay overlay should preview the next video's thumbnail behind the countdown instead of showing a plain dark panel.

## Requirements

- When an Up next target has a thumbnail URL, show it as the overlay background.
- Keep the countdown, title, and actions readable over the image.
- Preserve the existing dark fallback when the target has no thumbnail URL.
- Avoid changing autoplay timing or navigation behavior.

## Acceptance Criteria

- Browser smoke coverage verifies the overlay exposes the target thumbnail background.
- Existing Up next overlay and countdown behavior remains passing.
- Relevant frontend checks pass.

## Notes

- Implemented with a full-player thumbnail image and dark gradient scrim behind the existing Up next countdown panel.
- Verified with focused Up next smoke coverage, `pnpm check`, `go test ./meta`, and `git diff --check`.
