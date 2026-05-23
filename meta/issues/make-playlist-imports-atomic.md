# Make playlist imports atomic

## Summary

Playlist import upserts entries one by one without removing stale entries or wrapping the replacement in a transaction.

## Requirements

- Import playlist metadata and entries atomically.
- Remove entries that disappeared upstream during reimport.
- Avoid transient unique-position conflicts during reorder/import.

## Acceptance Criteria

- A regression test proves reimport removes stale playlist entries.
- A regression test proves reordered positions import without partial state.
- Existing TubeArchivist import tests pass.
- `go test ./...` passes.

## Notes

- Review refs: `internal/taimport/importer.go:554-604`, `internal/database/migrations/001_initial.sql:49-56`.
