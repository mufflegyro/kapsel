# Implement durable local job runner

## Summary

Implement an in-process job runner backed by SQLite so downloads, scans, imports, and maintenance tasks survive restarts without Redis or Celery.

## Requirements

- Add a jobs table with status, type, payload, priority, attempts, progress, errors, timestamps, and cancellation state.
- Implement worker locking and stale job recovery.
- Support cancellation through context-aware jobs.
- Add tests for enqueue, claim, complete, fail, retry, and cancel behavior.

## Acceptance Criteria

- Jobs are durable across process restarts.
- Failed jobs can retry according to a documented policy.
- Cancelled jobs stop safely when the job implementation supports cancellation.
- Job state can be queried by an API endpoint.

## Notes

- Keep the first version single-process and single-node.
