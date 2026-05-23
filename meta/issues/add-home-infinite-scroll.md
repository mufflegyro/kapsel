# Add home infinite scroll

## Summary

The home video grid should automatically load the next page when the user reaches the bottom, so browsing the main archive feed does not require manual pagination.

## Requirements

- Auto-load additional home videos when the bottom of the home grid enters the viewport.
- Keep existing explicit pagination behavior for non-home scoped lists such as channels, playlists, and search-adjacent views.
- Avoid duplicate concurrent page requests.
- Preserve accessibility with a visible loading/status affordance and a manual fallback control.

## Acceptance Criteria

- The home route appends the next video page when bottomed out.
- Existing home items remain visible and are not replaced by the next page.
- Auto-loading stops at the last page.
- Browser smoke coverage verifies the home feed loads more videos automatically.

## Notes

- Requested after the local NAS import made the home feed large enough to browse continuously.
