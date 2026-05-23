# Reduce playback progress cache fanout

## Summary

Playback progress sync updates the current video plus library and channel route caches on every successful sync. This creates cross-route state coupling and avoidable array churn from the watch page.

## Requirements

- Keep the active watch page state current after progress sync.
- Avoid directly rewriting unrelated route caches on every progress tick.
- Use a narrow invalidation or dirty-state mechanism for list pages.
- Preserve visible playback progress behavior after navigation.

## Acceptance Criteria

- Progress sync updates `video.item` and a focused shared invalidation signal, not every route list cache.
- Home/channel list pages refresh or reconcile progress when they are active or next entered.
- Browser smoke continues to cover progress persistence and thumbnail progress.

## Notes

- Review references: `frontend/src/App.svelte:922`, `frontend/src/App.svelte:1819`, and `frontend/src/App.svelte:1843`.
- This should follow or be part of watch-page extraction.
