# Prototype SQLite FTS search

## Summary

Prototype local search using SQLite FTS5 for video, channel, playlist, subtitle, and comment text.

## Requirements

- Create FTS tables or external-content FTS indexes for searchable entities.
- Add a search API with bounded result sizes.
- Add tests for title, channel, subtitle, and comment matches.
- Document known limitations compared with Elasticsearch.

## Acceptance Criteria

- Search returns relevant results for seeded fixture data.
- Result counts and payloads are bounded.
- Search tests pass with `go test ./...`.
- The API response shape is documented.

## Notes

- Prefer simple ranking first; advanced relevance tuning can wait.
