# Finalize jobs on runner shutdown

## Summary

When the runner context is cancelled, `RunOnce` waits for the active handler but returns `ctx.Err()` without finalizing the job state.

## Requirements

- Finalize the active job after its handler exits during runner shutdown.
- Preserve existing cancellation semantics for explicitly cancelled jobs.
- Avoid leaving completed or failed work in `running` until stale recovery.

## Acceptance Criteria

- If a handler completes successfully during shutdown, the job is marked succeeded.
- If a handler returns an error during shutdown, the job is marked failed or cancelled according to existing rules.
- Regression coverage exercises the `ctx.Done()` path.

## Notes

- Review reference: `internal/jobs/runner.go:112-115`.
