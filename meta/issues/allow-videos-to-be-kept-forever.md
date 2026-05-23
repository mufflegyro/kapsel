# Allow videos to be kept forever

## Summary

Allow users to mark individual videos as protected so automated retention or cleanup never removes their downloaded media.

## Requirements

- Add persistent per-video state for a "keep forever" flag.
- Expose the flag in video detail responses and relevant list responses.
- Add a video detail control to toggle the flag.
- Ensure retention and storage cleanup paths respect the flag before deleting media.
- Keep the flag independent of watched state, manual download state, and channel subscription state.

## Acceptance Criteria

- Tests cover setting and clearing the keep-forever flag.
- Tests cover retention skipping keep-forever videos.
- The video detail UI can toggle the flag and reflects the saved state.
- List/detail APIs expose enough state for future UI affordances.

## Notes

- This is a follow-up to the auto-download retention policy.
- Prefer explicit wording such as "Keep forever" over ambiguous favorites or likes.
