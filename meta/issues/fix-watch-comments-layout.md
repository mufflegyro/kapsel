# Fix watch comments layout

## Summary

Imported comments on the watch page are rendering as a wide horizontal row with excessive empty vertical space instead of readable stacked comments.

## Requirements

- Render comments as a vertical, readable list on desktop and mobile.
- Keep author, date, text, likes, and replies information visible without horizontal overflow.
- Preserve the existing watch page visual language.

## Acceptance Criteria

- Comments under a watched video stack top-to-bottom in the available column.
- The comments panel no longer reserves a large empty body before comments.
- Frontend validation or browser smoke verification covers the comments layout.

## Notes

- Reported from the local NAS deployment after importing TubeArchivist comments.
- Fixed by scoping the comments heading flex styles to `.comments-heading`, keeping `.comment-list` as a grid, and preventing stretched grid rows in the comments panel.
- Verified with `pnpm check`, `pnpm browser-smoke`, and a live deployed layout check on a local watch page.
