# Allow multiple direct download queueing

## Summary

The downloads page currently prevents adding another video while a previous direct download is still active. Users should be able to queue additional videos without waiting for the current job to finish.

## Requirements

- Allow the direct download form to submit another valid video URL while an earlier download job is queued or running.
- Keep duplicate active download protection for the same URL/payload.
- Keep status feedback for the most recently submitted direct download.

## Acceptance Criteria

- A user can submit a second distinct direct video URL while the first download job is still active.
- The frontend does not disable or block the direct download form solely because a prior job is active.
- Regression coverage verifies the behavior.

## Notes

- This applies to the direct download form on the downloads page, not catalog video download buttons.
