# Build YouTube-like archive UI

## Summary

Replace the current placeholder archive frontend with a YouTube-like interface adapted to Kapsel's style and local archive data.

## Requirements

- Add a persistent top navigation bar with brand, search, quick actions, and account affordance.
- Add a responsive sidebar with primary archive navigation and local archive actions.
- Build a homepage with topic chips, add-channel affordance, and a dense video grid.
- Build a watch page with a large Video.js player, video metadata, channel/action row, description card, comments placeholder, and recommendations.
- Build a channel page with profile header, tabs, and a channel-filtered video grid.
- Preserve the existing add-channel job flow and library refresh behavior.
- Keep the interface responsive on desktop and mobile.

## Acceptance Criteria

- Frontend build succeeds.
- Existing backend tests pass.
- Channel pages can load archive channel metadata and videos.
- The visual structure is recognizably YouTube-like while using Kapsel colors and styling.
- Existing Video.js playback remains wired on video detail pages.
