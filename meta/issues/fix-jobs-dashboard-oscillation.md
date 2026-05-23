# Fix jobs dashboard oscillation

## Summary

The downloads/jobs dashboard can visibly oscillate between different first-page job lists while live updates and polling are active.

## Requirements

- Diagnose whether the oscillation is caused by actual job state changes, API ordering, websocket snapshots, or frontend merge logic.
- Keep the jobs dashboard stable while still reflecting live job progress and status changes.
- Preserve bounded job pagination and filtering behavior.

## Acceptance Criteria

- The first page of the jobs dashboard no longer alternates between incompatible snapshots for the same filter.
- Live updates still refresh active job status and progress.
- Regression coverage verifies the dashboard does not replace the current page with stale or incompatible live/poll snapshots.

## Notes

- Reported after deployment when the all-jobs view alternated between a running video download and queued jobs at the top.
