# Honor max attempts for stale jobs

## Summary

Normal job failure respects `max_attempts`, but stale running job recovery can reclaim and rerun jobs without checking whether the attempt limit has already been reached.

## Requirements

- Apply the same attempt-limit policy to stale running jobs as normal failed jobs.
- Record a clear failure reason when a stale job is no longer retryable.
- Preserve stale recovery for retryable jobs and successful jobs with a final committed result.

## Acceptance Criteria

- A stale running job with `attempts >= max_attempts` is not claimed for another run.
- Such a job becomes `failed` with a useful diagnostic, unless it has a final committed result that should become `succeeded`.
- Tests cover stale `MaxAttempts: 1` jobs and retryable stale jobs.
- Existing stale cancellation and stale successful-result behavior remains covered.

## Notes

- Advisor priority: high.
- Relevant references: `internal/jobs/store.go:340-359` and `internal/jobs/store.go:479-505`.
