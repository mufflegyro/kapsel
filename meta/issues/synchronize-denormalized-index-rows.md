# Synchronize denormalized index rows

## Summary

Search documents and media asset rows can become stale when metadata fields are cleared or owning records are deleted.

## Requirements

- Add shared helpers that upsert non-empty denormalized rows and delete empty/stale rows.
- Apply them consistently to download, import, and delete paths.
- Keep updates transactional with their owning record where practical.

## Acceptance Criteria

- Regression tests prove cleared metadata no longer remains searchable.
- Regression tests prove obsolete media asset rows do not remain referenced after metadata changes.
- Existing search, import, storage, and server tests pass.

## Notes

- Review refs: `internal/taimport/importer.go:526-548`, `internal/download/downloader.go:1307-1310`, `internal/server/server.go:1777-1825`.
