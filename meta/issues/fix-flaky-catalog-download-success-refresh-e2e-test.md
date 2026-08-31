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
- Root cause (diagnosed 2026-08-31, not the `runRouteLoad()` candidate): the test clicks download, the app sets the optimistic `{status:'loading', job:null}` catalog state, and `emitLiveJobs` can deliver the succeeded snapshot **before the mocked POST response resolves** (`toHaveText('Downloading video')` already passes in the loading state, so nothing in the test serializes the order). `applyLiveJob` then finds no catalog entry whose `job.id` matches and drops the update silently — `watchCatalogVideoJob` scheduled no poll (`liveConnected` is true) and the fake job does not exist server-side, so the `succeeded` state is lost permanently. The same race exists in production: the server's WS broadcast of a newly created job can beat the POST response to the browser.
- Fix: `downloadCatalogItem` re-applies the fresher stashed live state for the accepted job (`pendingLiveJobsSnapshot`, which `handleLiveMessage` refreshes on every snapshot) after the POST resolves — `applyLiveJob` routes it through `applyCatalogVideoJobUpdate`, whose `jobUpdateIsOlder` guard keeps the accepted `queued` state authoritative when the stash is staler. Status: **landed 2026-08-31**.
