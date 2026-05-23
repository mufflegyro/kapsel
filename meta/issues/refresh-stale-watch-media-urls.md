# Refresh stale watch media URLs

## Summary

The watch page keeps the signed `media_url` returned when the video detail loads. If the page sits idle past the media URL TTL, later playback or range requests can fail with a browser media pipeline data-source error.

## Requirements

- Refresh the signed media URL before playback resumes after it has expired or is close to expiring.
- Preserve the current video position and avoid disrupting active playback.
- Keep media URLs signed and short-lived; do not disable media URL expiration.

## Acceptance Criteria

- Returning to an idle watch page after the signed media URL expires refreshes the video detail/media URL before playback resumes.
- A media element error caused by a stale signed URL gets one automatic refresh/retry path.
- Tests or documented verification cover stale media URL refresh behavior.

## Notes

- The signed URL expiry is already exposed as the `expires` query parameter.
- The backend correctly rejects expired `/media/...` URLs; the frontend needs to avoid keeping stale URLs in the player indefinitely.

## Current Status

- Implemented wake/timer refresh for expiring watch media URLs.
- Added one-shot media error retry for expired signed URLs.
- Preserves playback position across refresh, including after timestamp seeks.
- Passive refresh suppresses autoplay; explicit play/error refreshes can resume playback.
- Verified with frontend browser smoke, Svelte check, full Go tests, metadata tests, and diff whitespace checks.
