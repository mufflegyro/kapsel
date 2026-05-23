# Define MVP household archive acceptance test

## Summary

Define the product-level acceptance path that proves Kapsel is usable as a local household video archive rather than only a proof of concept.

## Requirements

- Cover first-run setup, configuration readiness, and authentication expectations.
- Cover adding a direct video, adding a channel, watching media, resuming playback, and searching metadata.
- Cover a channel catalog scan that shows non-downloaded videos as selectable archive candidates.
- Cover app restart, backup, restore, and basic failure recovery expectations.
- Keep the acceptance path runnable without relying on live YouTube network calls where possible.

## Acceptance Criteria

- A documented MVP checklist exists in the repo.
- Each checklist item links to the issue or test area that will satisfy it.
- The checklist clearly separates v1.0 requirements from deferred nice-to-haves.
- Browser and backend smoke tests can eventually map to this checklist.

## Notes

- This issue is planning work, but it should produce a concrete acceptance artifact that future implementation issues can target.
