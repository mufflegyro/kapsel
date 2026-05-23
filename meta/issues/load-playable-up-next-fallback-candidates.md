# Load dedicated playable Up next candidates

## Summary

The deployed watch page ranks loaded Up next candidates correctly, but it reuses the global library feed as its cross-channel fallback pool. After catalog scans gained approximate dates, that can fill Up next with same-channel catalog-only entries before older playable videos from other channels are available to rank.

## Requirements

- Add a bounded video-specific Up next API endpoint.
- Load watch-page Up next candidates from that endpoint instead of the global library feed.
- Preserve the intended tier order: same-channel started playable, same-channel unstarted playable, other playable, then unavailable.

## Acceptance Criteria

- The watch page requests `/api/videos/{id}/up-next` for recommendations.
- The endpoint returns available other-channel videos before catalog-only same-channel entries when no playable same-channel item is available.
- Browser smoke coverage verifies the dedicated request and ordering.

## Notes

- Avoid using the home/library feed as a hidden dependency for the watch page.
