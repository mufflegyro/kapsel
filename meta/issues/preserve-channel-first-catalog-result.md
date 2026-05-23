# Preserve channel-first catalog result

## Summary

Channel-first downloads sync the channel catalog but discard the catalog result when the first-video ingest overwrites job result JSON.

## Requirements

- Preserve catalog sync details for channel-first downloads.
- Preserve first-video ingest details.
- Keep existing job dashboard behavior understandable.

## Acceptance Criteria

- A test proves channel-first job results include catalog sync information and first-video ingest information.
- Existing download tests pass.
- `go test ./...` passes.

## Notes

- Review ref: `internal/download/downloader.go:302-315`.
