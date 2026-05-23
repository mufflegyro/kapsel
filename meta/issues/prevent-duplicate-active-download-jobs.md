# Prevent duplicate active download jobs

## Summary

Concurrent download requests can enqueue duplicate active jobs for the same target because duplicate checks are missing or non-atomic.

## Requirements

- Prevent duplicate queued/running direct-download jobs for the same normalized payload.
- Make catalog video duplicate detection atomic with enqueue.
- Return the existing active job when a duplicate request arrives.

## Acceptance Criteria

- Concurrent requests for the same direct video URL create at most one active job.
- Concurrent catalog download requests create at most one active job.
- Regression coverage exercises concurrent enqueue attempts.

## Notes

- Review references: `internal/server/server.go:880-912` and `internal/server/server.go:991-1037`.
