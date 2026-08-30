# Fix flaky catalog download success refresh e2e test

## Summary

The `catalog download success snapshots refresh route data once` browser smoke test fails on a clean tree, independent of feature work. After a succeeded download job event, the video detail route data is expected to be re-fetched exactly once more (2 total requests), but the refetch never fires and only 1 request is observed.

## Requirements

- Diagnose why the video detail refetch after a succeeded download job does not fire (affects both desktop and mobile projects).
- Restore the test to green on a clean tree at the current main commit without weakening its "refresh route data once, not per duplicate event" assertion.

## Acceptance Criteria

- `pnpm exec playwright test -g "catalog download success snapshots refresh route data once"` passes with `--repeat-each=3` on both projects.
- A full `pnpm browser-smoke` run is green (aside from any newly documented, unrelated issues).

## Notes

- Failure signature (2026-08-30): `expect.poll(() => videoRequests).toBe(2)` times out with `Received: 1` at smoke.spec.js:677 after `emitLiveJobs([succeededJob])`.
- Reproduced 4/4 runs on a clean worktree at 1a9d4ec (separate worktree, `KAPSEL_E2E_PORT=18098`), desktop and mobile both — so it predates and is unrelated to the hide-watched toolbar work.
- Candidate areas to inspect: the live-jobs invalidation path for video detail, and the `runRouteLoad()` session-gating ordering that can skip route loads.
