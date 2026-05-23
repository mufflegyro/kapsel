# Tighten browse navigation and grid polish

## Summary

Improve browsing clarity and scanability across the home feed, sidebar, and video grid without changing Kapsel's core YouTube-like layout.

## Requirements

- Visually separate primary navigation from Explore/category items in the sidebar.
- Make video card titles align more consistently in the grid.
- Show users how much of the archive feed they are viewing without making the home page feel like a generic search-results table.
- Keep search placement and top-level actions consistent enough that users can build muscle memory.
- Verify responsive behavior for the grid, watch detail/sidebar layout, job cards, and settings grids.
- Audit keyboard focus states and contrast for custom controls, cards, badges, and filter pills.

## Acceptance Criteria

- Explore/category items have a lower visual hierarchy than Home, Channels, Playlists, Downloads, and Settings.
- Video card titles are clamped or otherwise constrained so grid rows scan cleanly.
- The home feed provides a clear "showing X of Y", load-more, or pagination affordance for bounded browsing.
- Desktop and mobile checks verify the home grid, watch page, downloads job cards, and settings page remain usable.
- Keyboard focus indicators are visible for sidebar items, filter pills, video cards, and action buttons.

## Notes

- Prefer a personal-archive-feeling "Showing X of Y" or load-more pattern over heavy numbered pagination unless pagination is already the smallest consistent change.
- Search placement was noted as a consistency concern, but lower priority than catalog/local clarity and dense operational readability.
