# Add direct video download flow

## Summary

Let users add a single video URL from the web UI, queue the existing download job, and follow job status through completion.

## Requirements

- Add a direct video URL form to the UI.
- Use the existing `POST /api/downloads` endpoint.
- Poll or subscribe to job status without blocking the request.
- Refresh affected library views after successful download.
- Validate unsupported URLs before enqueueing.

## Acceptance Criteria

- Tests cover API rejection for unsupported URLs and successful enqueueing.
- The frontend can queue a single video and show queued/running/succeeded/failed states.
- The library updates after a successful single-video download.
- Existing channel-first download behavior remains intact.

## Notes

- This should share as much UI behavior as practical with the channel add flow.
