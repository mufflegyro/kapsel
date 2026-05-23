# Recover panics in job runner handlers

## Summary

Job handler panics can leave the runner waiting forever because the handler goroutine exits without sending a result to `RunOnce`.

## Requirements

- Recover panics from job handlers launched by the durable runner.
- Convert recovered panics into job failures with useful error details.
- Ensure `RunOnce` never hangs waiting on a handler that panicked.

## Acceptance Criteria

- A panicking handler marks the job failed instead of leaving it running.
- The runner returns from `RunOnce` after a handler panic.
- Regression coverage proves panic recovery and failure recording.

## Notes

- Review reference: `internal/jobs/runner.go:84-87` and shutdown wait path at `internal/jobs/runner.go:112-115`.
