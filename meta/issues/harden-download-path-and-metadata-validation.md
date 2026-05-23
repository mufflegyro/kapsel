# Harden download path and metadata validation

## Summary

Validate `yt-dlp` metadata and file paths before inserting archive records or exposing files through signed URLs.

## Requirements

- Reject metadata without a valid video ID or usable title.
- Normalize downloaded media, thumbnail, and subtitle paths relative to configured roots.
- Reject paths that escape storage roots or point at unexpected locations.
- Verify required media files exist before marking a download succeeded.
- Preserve safe behavior for missing optional assets such as thumbnails or subtitles.

## Acceptance Criteria

- Tests cover path traversal rejection, absolute path rejection, missing media file rejection, and malformed metadata.
- Failed validation leaves the job failed with a clear error and no inconsistent downloaded-video row.
- Existing happy-path download tests continue to pass.
- Validation helpers are reused by future thumbnail/subtitle ingestion.

## Notes

- Keep shell-free command execution; hardening here is about inputs and outputs, not shell escaping.
