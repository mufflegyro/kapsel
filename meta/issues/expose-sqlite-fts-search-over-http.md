# Expose SQLite FTS search over HTTP

## Summary

Add an HTTP endpoint for the existing SQLite FTS search package.

## Requirements

- Add `GET /api/search?q=...`.
- Enforce result limits and default result size.
- Return owner type, owner ID, field, snippet, and rank.
- Add frontend-ready error handling for empty or invalid search queries.

## Acceptance Criteria

- Tests cover matching results, empty queries, and max limit enforcement.
- Response shape matches the documented search prototype.
- Search endpoint does not expose unbounded payloads.

## Notes

- Frontend wiring can happen in a separate issue if needed.
