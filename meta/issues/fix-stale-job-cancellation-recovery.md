# Fix stale job cancellation recovery

## Summary

Stale job recovery can mark cancelled running jobs with non-empty `result_json` as succeeded because the recovery query does not account for `cancel_requested`.

## Requirements

- Ensure stale cancelled jobs are not promoted to succeeded solely because `result_json` is non-empty.
- Preserve intended recovery for genuinely completed stale jobs.
- Define consistent behavior for cancelled jobs with partial results.

## Acceptance Criteria

- A regression test proves a stale running job with `cancel_requested = 1` and non-empty `result_json` is not marked succeeded.
- Existing job store tests pass.
- `go test ./internal/jobs ./...` or full `go test ./...` passes.

## Notes

- Review ref: `internal/jobs/store.go:250-259`.
