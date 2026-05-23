# Prototype yt-dlp download job

## Summary

Prototype a durable download job that invokes `yt-dlp` and stores the resulting core metadata and file paths.

## Requirements

- Decide how `yt-dlp` is discovered and invoked.
- Add a job handler for downloading a single URL.
- Capture title, source ID, channel metadata, duration, media path, and thumbnail path where available.
- Report download failures through job error state.

## Acceptance Criteria

- Tests cover command construction without requiring network access.
- Tests cover successful metadata ingestion from a fixture result.
- Failed command execution marks the job failed or retryable according to job policy.
- The implementation does not block HTTP request handlers directly.

## Notes

- Networked end-to-end download tests should be optional and skipped by default.
