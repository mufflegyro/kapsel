# Add manual channel scan and selective downloads

## Summary

Add a user-controlled channel scan flow that discovers videos and lets users choose which catalog-only videos to download.

## Requirements

- Add a durable manual channel scan job.
- Add channel page controls to start a scan and show scan status.
- Add per-video download actions for catalog-only videos.
- Enqueue selected videos as durable download jobs.
- Avoid automatic bulk downloads unless a later policy explicitly enables them.

## Acceptance Criteria

- Tests cover scan job enqueueing, scan result persistence, duplicate suppression, and selected video download enqueueing.
- The channel page can trigger a manual scan and refresh catalog results.
- Download buttons appear only for catalog-only videos with enough source metadata.
- Job dashboard links scan and download jobs back to the channel/video where possible.

## Notes

- Scheduled subscription scanning should remain deferred until manual scan behavior is reliable.
