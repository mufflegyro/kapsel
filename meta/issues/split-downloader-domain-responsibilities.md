# Split downloader domain responsibilities

## Summary

The downloader package currently owns yt-dlp command construction, ingestion, catalog sync, search indexing, preview scheduling, retention cleanup, URL normalization, and some job scheduling. These are related but not the same concern.

## Requirements

- Separate command execution/download concerns from ingestion and catalog persistence.
- Move retention cleanup into a focused cleaner type or package boundary.
- Delegate search indexing and preview job scheduling to focused helpers.
- Preserve the current public job handlers while extracting internals.

## Acceptance Criteria

- At least one major responsibility is extracted behind a focused type or helper.
- Tests continue to cover download ingestion, catalog sync, and retention behavior.
- No unrelated cleanup is batched with behavior changes.

## Notes

- Review references: `internal/download/downloader.go:1285`, `internal/download/downloader.go:1494`, `internal/download/downloader.go:1599`, `internal/download/downloader.go:2031`, and `internal/download/downloader.go:2547`.
- Good first extraction: catalog sync persistence or retention cleanup, because both have clear boundaries and existing tests.
