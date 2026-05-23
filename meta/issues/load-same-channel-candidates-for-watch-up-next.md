# Load same-channel candidates for watch Up next

## Summary

The watch page Up next ordering can only prioritize same-channel videos that are already present in the generic home feed response, so pages whose channel is absent from that feed still show unrelated recommendations first.

## Requirements

- On watch pages, load a bounded newest page of videos from the current video's channel.
- Merge same-channel candidates with the existing library candidates before applying Up next priority ordering.
- Preserve the existing fallback to available and remaining videos when same-channel candidates are unavailable.
- Avoid showing stale candidates from a previously viewed channel while navigating between videos.

## Acceptance Criteria

- Frontend smoke coverage proves same-channel videos are prioritized even when they are absent from the home feed response.
- Existing Up next overlay and recommendation ordering coverage remains passing.
- Relevant frontend checks pass.

## Notes

- Reproduced on `/videos/kueFI6h13LU`: the home feed returned unrelated LivAverageGamer catalog videos, while the current channel query returned the expected SNESdrunk candidates.
- Implemented by loading `/api/videos?channel=<current-channel>&sort=newest` on watch pages and merging those rows into the Up next candidate list before sorting.
- Verified with focused and full browser smoke coverage, `pnpm check`, `go test ./...`, and `git diff --check`.
