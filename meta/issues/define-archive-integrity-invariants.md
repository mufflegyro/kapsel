# Define archive integrity invariants

## Summary

Define and enforce the core consistency rules between SQLite metadata, media files, thumbnails, subtitles, jobs, and search documents.

## Requirements

- Document valid states for downloaded, catalog-only, missing, failed, and partially ingested videos.
- Define ownership rules for media, thumbnail, subtitle, comment, and derived preview files.
- Define idempotency expectations for repeated imports, downloads, scans, and metadata refreshes.
- Add validation helpers or tests that can be reused by maintenance and ingestion code.
- Ensure future cleanup and backup work can reason about these states.

## Acceptance Criteria

- Tests cover valid and invalid video/file metadata states.
- Catalog-only videos can exist without media files while downloaded videos require valid media references.
- The documented invariants are referenced by download hardening and storage maintenance issues.
- No invariant allows paths outside configured storage roots.

## Notes

- This issue should precede destructive cleanup and broad channel catalog syncing.
