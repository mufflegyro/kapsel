# Add job cancellation and retry controls

## Summary

Add safe controls for cancelling queued/running jobs and retrying failed jobs from the job dashboard.

## Requirements

- Add API endpoints for requesting cancellation.
- Add API endpoints or store methods for retrying failed jobs when safe.
- Show cancel and retry controls only for eligible job states.
- Preserve clear state transitions for cancellation, retry, and repeated failures.
- Avoid retrying jobs that would duplicate already committed state unless idempotency is proven.

## Acceptance Criteria

- Tests cover cancelling queued jobs, cancelling running jobs, retrying failed jobs, and rejecting invalid transitions.
- The dashboard updates controls based on current job state.
- Retried jobs keep useful history or timestamps for debugging.
- Existing durable runner tests continue to pass.

## Notes

- Start with retry for jobs that failed before committing archive state, then expand as ingestion idempotency improves.
