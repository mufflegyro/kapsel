# Exclude watched videos from Up next

## Summary

The watch page Up next recommendations can include videos that have already been watched. Up next should focus on unwatched playable videos.

## Requirements

- Exclude videos marked watched in either `videos.watched` or `user_progress.watched` from Up next candidates.
- Preserve the existing playable-first and same-channel ordering behavior for remaining candidates.
- Keep bounded Up next responses.

## Acceptance Criteria

- Watched videos do not appear in `/api/videos/{id}/up-next` responses.
- The watch page Up next UI no longer shows watched videos.
- Regression coverage verifies watched candidates are filtered out.

## Notes

- The current video should continue to be excluded as before.
