# Skip SponsorBlock sponsor segments

## Summary

Kapsel should optionally use SponsorBlock's public segment API to skip sponsor segments while playing archived videos.

## Requirements

- Fetch only `sponsor` category segments from SponsorBlock.
- Cache fetched sponsor segments locally so playback does not depend on repeated external requests.
- Expose sponsor segments with video details.
- During playback, automatically seek past sponsor segments.
- Do not add voting, segment submission, or non-sponsor category behavior.

## Acceptance Criteria

- Backend tests cover successful SponsorBlock segment fetch and cache reuse.
- Frontend smoke coverage proves playback skips a sponsor segment.
- Relevant backend and frontend checks pass.

## Notes

- This adds one external service integration because the user explicitly requested SponsorBlock-style sponsor skipping.
