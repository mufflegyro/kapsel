# Preserve archived timestamp on redownload

## Summary

Redownloading an existing video overwrites `archived_at`, losing the original archive timestamp.

## Requirements

- Preserve existing `archived_at` when a video already has one.
- Set `archived_at` for newly archived videos and for existing rows missing a value.
- Keep `updated_at` reflecting the latest metadata update.

## Acceptance Criteria

- A regression test proves redownload keeps the original `archived_at`.
- Existing download tests pass.
- `go test ./...` passes.

## Notes

- Review ref: `internal/download/downloader.go:1236-1251`.
