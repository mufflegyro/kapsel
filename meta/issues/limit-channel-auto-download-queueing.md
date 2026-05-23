# Limit channel auto-download queueing

## Summary

Subscribed channel auto-downloads currently queue every missing video from the scanned catalog page before retention cleanup can remove older media. Auto-downloads should only consider the two most recent catalog videos per channel and should not backfill older videos.

## Requirements

- Consider only the first two unique catalog videos for a subscribed channel auto-download run.
- Preserve catalog sync behavior for the scanned page.
- Keep existing duplicate-active-job and already-downloaded checks.
- Keep retention cleanup as a separate safety net for older auto-downloaded media.

## Acceptance Criteria

- Regression tests cover auto-download queueing no more than two missing videos from a scanned page.
- Regression tests cover preserving existing skip behavior for already-downloaded or active jobs.
- Relevant Go tests pass.

## Notes

- The existing retention policy keeps the newest two downloaded videos per channel, but queueing must be bounded before download work starts.
- Implemented by considering only the first two unique catalog videos when enqueueing channel auto-download jobs, without backfilling older videos when those two are already downloaded or active.
- Verified with targeted downloader tests, `go test ./...`, and `git diff --check`.
- Deployed to CT `119` on 2026-05-07; `/api/health` returned `OK` and `/api/jobs` reported `0` rows after clearing existing jobs.
