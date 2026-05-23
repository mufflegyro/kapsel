# Bootstrap embedded Svelte frontend

## Summary

Create the initial Svelte frontend and wire it so production assets can be embedded and served by the Go backend.

## Requirements

- Add a minimal Svelte application.
- Add a basic shell with routes for library, search, downloads, and settings placeholders.
- Configure production build output for embedding in the Go service.
- Document frontend development commands.

## Acceptance Criteria

- Frontend development server runs locally.
- Production frontend build succeeds.
- Go backend can serve the built frontend assets.
- Initial routes load on desktop and mobile viewport widths.

## Notes

- Keep visual design minimal until core flows exist.
