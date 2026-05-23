# Prefer playable Up next videos

## Summary

Adjust watch-page Up next recommendations so playable videos are always preferred before catalog-only metadata when possible.

## Requirements

- Rank same-channel started videos before other recommendations.
- Rank unstarted available same-channel videos next.
- Rank available videos from other channels before unavailable videos.
- Keep catalog-only or otherwise unavailable videos after playable options.

## Acceptance Criteria

- The Up next list follows the requested playable-first priority order.
- The automatic Up next overlay chooses a playable video when one is available.
- Browser smoke coverage verifies the ordering.

## Notes

- Preserve existing newest-first ordering within the same priority tier.
