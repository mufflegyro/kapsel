# Add channel auto-download toggle

## Summary

Let users enable or disable automatic daily downloads for individual channels from the channel detail page.

## Requirements

- Add a bounded API endpoint for updating a channel's auto-download subscription state.
- Surface a clear toggle on the channel detail page using the existing `subscribed` state.
- Refresh the channel detail and list state after a successful toggle.
- Keep the large legacy `App.svelte` syntax unchanged except for the smallest safe UI change.

## Acceptance Criteria

- Toggling a channel updates `channels.subscribed` in the database.
- Missing channels return 404 and invalid payloads return 400.
- The channel page shows whether daily auto-download is on or off and lets the user change it.
- Relevant Go and frontend checks pass.

## Notes

- This is a manual per-channel opt-in/out control; existing auto-download scheduling remains backend-driven.
