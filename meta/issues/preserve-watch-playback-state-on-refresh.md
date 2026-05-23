# Preserve watch playback state on refresh

## Summary

The watch player can reset when video detail metadata is refreshed while playback is active, such as after timeline preview generation finishes or a signed media URL is refreshed. These resets can drop the active playback speed.

## Requirements

- Preserve the selected playback rate when watch metadata refreshes update the video element.
- Reduce stale media URL refresh churn by increasing the default signed media URL lifetime to roughly one day.
- Keep signed media URLs configurable through `KAPSEL_MEDIA_URL_TTL`.

## Acceptance Criteria

- Timeline preview completion does not lose the current playback speed.
- Signed media URL refresh does not lose the current playback speed.
- The default signed media URL TTL is 24 hours and remains documented/configurable.

## Current Status

- Implemented preview metadata refresh without replacing a still-valid watch media URL.
- Preserved playback rate across intentional signed media URL source refreshes.
- Increased the default signed media URL TTL to 24 hours.
- Verified with full Go tests, Svelte check, frontend build, full browser smoke, and diff whitespace checks.
