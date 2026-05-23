# Bound runner shutdown waits

## Summary

On shutdown, the runner cancels the active job context and then waits for the handler goroutine to return. Most subprocess-backed work respects context, but import loops and large reads have limited explicit cancellation checks, so shutdown can block longer than expected.

## Requirements

- Keep cooperative cancellation for normal jobs.
- Add explicit context checks in long-running non-subprocess loops.
- Avoid leaving completed work in an incorrect state during shutdown.
- Preserve stale recovery for jobs that cannot finish before process exit.

## Acceptance Criteria

- Long import loops check `ctx.Err()` before and after expensive reads or batches.
- Runner shutdown has a documented bounded behavior or a clear reason for waiting unbounded.
- Tests cover cancellation of an import-like handler and runner shutdown with a slow handler.
- Existing runner shutdown finalization tests continue to pass.

## Notes

- Advisor priority: medium.
- Relevant references: `internal/jobs/runner.go:118-121`, `internal/taimport/importer.go:173-213`, and `internal/taimport/importer.go:327-397`.
