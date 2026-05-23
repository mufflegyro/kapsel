# Add channel web flow

## Summary

Allow a user to add a channel from the web interface and enqueue a durable job that downloads the first video from that channel.

## Requirements

- Add a web form for entering a channel URL.
- Add an API endpoint that accepts a channel URL and enqueues a durable job.
- Resolve the first channel video through `yt-dlp` without blocking the HTTP handler.
- Reuse the existing single-video download ingestion path for the selected video.
- Surface queued/running/succeeded/failed feedback in the web UI.
- Use Video.js v10 for actual video playback in the detail view.

## Acceptance Criteria

- Tests cover channel enqueueing and unsupported URL rejection.
- Tests cover resolving the first channel entry and importing the resulting video metadata.
- Frontend build succeeds.
- The library can refresh after a successful channel-first-video job.
- Video detail playback uses Video.js rather than a bare native video element.
