# Refine For You feed

## Summary

The home feed currently labels the default personalized sort as "Continue Watching" and can include videos that have already been watched completely. Rename the UI label to "For You" and keep fully watched videos out of that feed.

## Requirements

- Rename the default home sort label from "Continue Watching" to "For You" without changing the existing sort URL value unless necessary.
- Exclude videos marked fully watched from the default personalized home feed.
- Keep partially watched and unwatched playable/catalog videos eligible for the feed according to the existing ordering.

## Acceptance Criteria

- The sort control shows "For You" for the default home feed.
- The default home feed does not return videos whose watched state is complete.
- Explicit home `sort=watching` uses the same watched-video exclusion as the default home feed.
- Explicit non-watching home sorts still allow watched videos to be browsed.
- Empty For You copy distinguishes an all-watched feed from a truly empty archive.
- Regression coverage exercises the exclusion of fully watched videos from the default feed.
- Existing frontend and backend checks for home feed sorting still pass.
