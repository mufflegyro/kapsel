# Polish topbar action shortcuts

## Summary

Move the job queue shortcut into the upper-right topbar next to settings and make both shortcuts read as compact icon actions.

## Requirements

- Show the queue shortcut beside settings in the topbar.
- Keep both shortcuts keyboard accessible with stable accessible names.
- Preserve the existing jobs route and settings route behavior.
- Keep the mobile topbar usable without hiding these primary shortcuts.

## Acceptance Criteria

- Users can open the queue from the upper-right topbar.
- Users can open settings from the upper-right topbar.
- The shortcuts are visually consistent icon-style actions.
- Frontend checks and smoke tests pass.

## Notes

- Keep the change focused on topbar markup and styles.
