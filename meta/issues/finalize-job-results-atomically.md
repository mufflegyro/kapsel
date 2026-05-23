# Finalize job results atomically

## Summary

Handlers currently write results before the runner transitions job status. This creates a two-phase gap where domain work and result JSON can commit, but the job can still appear `running` until `Runner.finish` completes or stale recovery repairs it.

## Requirements

- Make final result persistence and terminal status transition a single logical operation.
- Keep the runner as the clear owner of job disposition where practical.
- Preserve domain transactions that need to commit downloaded/imported data atomically.
- Avoid making partial reports look like final successful results.

## Acceptance Criteria

- A successful handler can provide a final result that is stored with `succeeded` status in one store operation, or the remaining two-phase gap is explicitly documented and tested.
- A failed handler cannot accidentally publish a final result unless the domain work is intentionally committed and retry is unsafe.
- Tests cover crash windows or failure windows around result write and status completion.
- Public job summaries continue to render after completion.

## Notes

- Advisor priority: high, but likely after the result contract and store-ownership issues.
- Relevant references: `internal/jobs/runner.go:126-168`, `internal/download/downloader.go:2014-2094`, and `internal/download/downloader.go:2271-2306`.
- Possible designs include `Store.CompleteWithResult`, a `HandlerResult` return value, or a final-result API that the runner owns.
- Direct download and preview handlers commit domain rows together with final job result/status where feasible; media files and preview sprites remain filesystem side effects outside SQLite atomicity.
- TubeArchivist import intentionally remains multi-transactional because a backup can span many entity batches. If final job completion fails after import rows commit, retry remains safe and idempotent; coverage lives in `TestImportJobCompletionFailureLeavesRetrySafeCommittedRows`.
