# Route job state writes through Store

## Summary

`jobs.Store` is intended to own the job lifecycle, but job state is still updated through ad hoc store construction and at least one raw SQL result update outside the store. This weakens lifecycle invariants and makes future queue changes harder.

## Requirements

- Make `jobs.Store` the only writer of job lifecycle fields such as result, progress, status, error, and cancellation state.
- Add transaction-aware store APIs where domain handlers need to set job result inside a domain transaction.
- Pass a shared `*jobs.Store` or narrow job reporter dependency into worker handlers instead of constructing stores from `*sql.DB` in handler bodies.
- Remove raw `UPDATE jobs ...` statements outside `internal/jobs` unless there is a documented exception.

## Acceptance Criteria

- Downloader result writes use `jobs.Store` APIs, including transaction-aware variants where needed.
- Preview and download handlers no longer create hidden `jobs.NewStore(db)` instances for lifecycle writes.
- A repository search shows job lifecycle field updates are centralized in `internal/jobs` or explicitly documented scheduler queries.
- Tests continue to cover result writing, progress heartbeat, cancellation, and retry safety.

## Notes

- Advisor priority: high.
- Relevant references: `internal/jobs/store.go:420-430`, `internal/download/downloader.go:2271-2282`, `internal/previews/previews.go:119`, and `internal/download/downloader.go:943-947`.
- A first small step is adding `Store.SetResultTx` and replacing downloader's private `setJobResult` SQL.
- Repository searches may still find raw `UPDATE jobs` statements in `_test.go` fixture setup; production lifecycle writes belong in `internal/jobs`.
