# Hydrate search results with archive records

## Summary

Upgrade search results from raw owner references to useful video, channel, playlist, subtitle, and comment result cards.

## Requirements

- Add a search response shape that includes safe display metadata for supported owner types.
- Preserve server-side snippet escaping and highlight safety.
- Link results to the correct route.
- Keep result limits capped.
- Add frontend result cards that match the YouTube-like UI.

## Acceptance Criteria

- Tests cover hydrated video, channel, and playlist search results.
- Tests preserve XSS protection for highlighted snippets.
- The frontend search page shows titles, thumbnails or avatars, owner type, and snippet context.
- Search remains bounded at the configured maximum limit.

## Notes

- Consider structured highlight ranges long-term, but preserving safe HTML snippets is acceptable for this step.
