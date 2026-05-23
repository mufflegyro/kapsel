# Harden queue live update edge cases

## Summary

The jobs dashboard live-update path is stable after the page-boundary merge fix, but the review identified adjacent edge cases that should be hardened separately.

## Requirements

- Preserve job result summaries when `/api/jobs/{id}` fallback updates are merged into list-backed job cards.
- Make job ordering semantics robust for timestamp precision edge cases, or provide an explicit sortable key so REST and live merges cannot drift.
- Document or refine `/api/live` snapshot semantics, including that snapshots contain recent jobs plus extra active jobs and may exceed the reported page size.
- Add regression coverage for filtered queue views, empty filtered pages, page 2+ behavior, and ordering tie-breakers where feasible.

## Acceptance Criteria

- Fallback live job fetches do not erase `result_summary` from visible jobs.
- Queue ordering remains consistent between REST responses and websocket merges for timestamp and ID tie-breakers.
- Filtered queue live updates have regression coverage or a documented manual verification path.
- Existing frontend smoke and backend tests continue to pass.

## Notes

- Split out from the jobs dashboard oscillation fix so the production regression can remain a focused change.
- Reviewers specifically called out RFC3339Nano string-order edge cases, stale filtered-view totals, and fallback merge behavior as follow-up work.
