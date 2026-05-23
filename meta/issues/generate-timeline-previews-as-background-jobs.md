# Generate timeline previews as background jobs

## Summary

Move timeline preview generation out of the synchronous download ingestion path so a video becomes playable as soon as download ingestion succeeds, even if ffmpeg preview generation is slow or fails.

## Requirements

- Queue durable preview generation work after successful media ingestion when previews are enabled.
- Keep the download job successful when preview generation fails.
- Make preview job completion observable through the existing jobs/live update flow.
- Avoid retrying already generated previews unnecessarily.
- Improve ffmpeg preview generation resource usage for long or high-framerate videos where feasible.

## Acceptance Criteria

- Tests cover download completion queuing a separate preview job instead of running ffmpeg inline.
- Tests cover preview job failure not failing the original download job.
- Tests cover successful preview jobs persisting timeline preview metadata.
- Existing browser smoke tests continue to pass.

## Notes

- Reported failure: preview command for a 37-minute 720p60 MP4 was killed while the parent download job was otherwise usable.
- Existing websocket job notifications can be reused for preview availability instead of adding a new notification channel.
