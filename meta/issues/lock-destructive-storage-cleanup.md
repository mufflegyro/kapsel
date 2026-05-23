# Lock destructive storage cleanup

## Summary

`storage-cleanup --delete` can run while the server or jobs are writing media files, allowing in-flight files to be classified as orphaned and deleted before database references are committed.

## Requirements

- Prevent destructive storage cleanup from running concurrently with the app/job runner.
- Use the same app lock strategy as server startup and restore, or an equivalent guard.
- Keep dry-run cleanup available without destructive side effects.

## Acceptance Criteria

- `kapsel storage-cleanup --delete --confirm` fails or waits safely when the app lock is held.
- Dry-run behavior remains unchanged.
- Regression coverage proves destructive cleanup respects the app lock.

## Notes

- Review references: `cmd/kapsel/main.go:157-164`, `internal/storage/storage.go:156-185`, and in-flight media writes in `internal/download/downloader.go`.
