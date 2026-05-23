# Add auto-download retention policy

## Summary

Add an Escapepod-style retention policy for automatically downloaded channel videos so Kapsel keeps a useful rolling offline cache without growing indefinitely.

## Requirements

- Apply the retention policy to videos downloaded by automatic channel downloads.
- Keep the latest two downloaded videos per channel regardless of watch state.
- Keep videos that have been started, based on local playback progress.
- Keep videos that were downloaded manually rather than by automatic channel download.
- Remove downloaded media for videos that have not been listened to or watched in more than two weeks when they are not protected by another keep rule.
- Preserve catalog metadata when media is removed so the video can still appear as catalog-only and be downloaded again later.
- Make retention work bounded, durable, observable, and safe to retry.

## Acceptance Criteria

- Tests cover latest-two retention per channel.
- Tests cover started videos being retained.
- Tests cover manually downloaded videos being retained.
- Tests cover stale, unstarted auto-downloaded videos having media removed after two weeks.
- Tests verify metadata remains after media removal.
- Retention activity is visible through existing job or diagnostics surfaces.

## Notes

- Treat "episode" as a downloaded video in Kapsel terminology.
- The two-week cutoff should use local playback activity, not the video's published date.
- This issue should not add a UI override; use the separate keep-forever issue for that.
