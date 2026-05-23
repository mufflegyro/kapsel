# Add daily channel auto downloads

## Summary

Add basic automatic channel syncing that discovers and downloads new videos without polling every channel at the same time or fetching more catalog pages than needed.

## Requirements

- Schedule subscribed-channel auto-download syncs by default about once per day.
- Randomize scheduled sync times so channels do not all hit YouTube together.
- Keep sync work durable and observable through the existing job queue.
- Fetch only the first page of channel videos by default for auto syncs.
- Fetch additional channel pages during auto syncs only when the newest fetched set does not overlap already-downloaded local videos.
- Queue downloads only for catalog videos that are not already locally downloaded.

## Acceptance Criteria

- A scheduler creates durable auto-download jobs for subscribed channels that do not already have active scheduled auto jobs.
- Auto jobs run at jittered daily times, and the scheduler creates the next durable job after completion.
- The auto job scans one page when it finds an already-downloaded video in the newest page.
- The auto job scans another page when the newest page has no already-downloaded video.
- Relevant Go tests pass.

## Notes

- Keep this backend-only for now; UI controls can be added later if needed.
- The first implementation should stay small and use the existing jobs table rather than adding an external scheduler.
- Adding a channel through the current channel-first flow marks that channel subscribed for auto downloads; non-subscribed imported channels are left alone.
