# Guard heartbeats to running jobs

## Summary

Job heartbeats update rows by id only, so delayed heartbeats can touch completed, failed, or cancelled jobs.

## Requirements

- Restrict heartbeat updates to jobs still in the running state.
- Preserve progress clamping and monotonic progress behavior for running jobs.
- Avoid updating `locked_at` or `updated_at` on terminal jobs.

## Acceptance Criteria

- A regression test proves heartbeats do not mutate terminal jobs.
- Existing job store tests pass.
- `go test ./internal/jobs ./...` or full `go test ./...` passes.

## Notes

- Review ref: `internal/jobs/store.go:357-372`.
