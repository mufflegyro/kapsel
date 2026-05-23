# Keep job runner alive after transient errors

## Summary

The job runner loop returns on any `RunOnce` error, so a transient database or heartbeat failure can stop background processing while HTTP continues serving.

## Requirements

- Keep the runner loop alive after recoverable `RunOnce` errors.
- Exit promptly on context cancellation.
- Log or expose transient errors without silently stopping the worker.

## Acceptance Criteria

- A test proves `RunLoop` continues after a transient `RunOnce` error.
- A test proves context cancellation still exits the loop.
- `go test ./internal/jobs ./...` or full `go test ./...` passes.

## Notes

- Review refs: `internal/jobs/runner.go:44-59`, `internal/jobs/runner.go:99-108`.
