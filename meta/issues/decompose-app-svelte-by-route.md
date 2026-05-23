# Decompose App.svelte by route

## Summary

Incrementally split the large frontend app shell into route-focused components so routing, data loading, playback controls, job dashboard behavior, and settings UI are easier to reason about and test.

## Requirements

- Keep the current app behavior and visual design intact.
- Start with the watch page because it owns playback, Up next, comments, progress sync, and action state.
- Extract route-specific UI into focused Svelte components when touching those areas.
- Prefer local component state over broad stores unless state is genuinely shared.
- Avoid watch-page actions directly mutating unrelated route caches such as home, channel, and playlist lists.
- Keep existing legacy syntax in `App.svelte` until a focused extraction makes Svelte 5 rune syntax safer for the new component.
- Do not introduce a sweeping state-management rewrite as part of this issue.

## Acceptance Criteria

- At least two major routes or route sections are extracted from `App.svelte` into focused components.
- Extracted components use Svelte 5 event attributes and rune syntax where practical.
- Browser smoke tests continue to pass.
- `pnpm check` passes.

## Notes

- Good first candidates: jobs dashboard, settings diagnostics, watch page, and channel page.
- This is a maintainability follow-up, not a feature rewrite.
- Architecture review found the same class of hidden coupling that caused the Up next bug: route data for library, watch page, channels, playlists, jobs, comments, and playback all share one reactive scope in `frontend/src/App.svelte`.
