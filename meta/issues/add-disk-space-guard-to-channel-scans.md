# Add disk-space guard to channel scans

## Summary

Channel scans call yt-dlp without the disk-space guard used by direct downloads and channel-first downloads.

## Requirements

- Run the configured disk-space check before channel scan yt-dlp execution.
- Return a clear error when disk headroom is insufficient.
- Keep direct download and channel-first behavior unchanged.

## Acceptance Criteria

- Channel scans fail before invoking yt-dlp when configured free-space requirements are not met.
- Regression coverage verifies the low-disk channel scan path.

## Notes

- Review reference: `internal/download/downloader.go:494-523`.
