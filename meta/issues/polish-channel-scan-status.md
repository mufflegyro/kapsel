# Polish channel scan status

## Summary

The status message shown below the channel page scan controls looks visually disconnected from the action row.

## Requirements

- Make the scan status read as part of the channel action area.
- Preserve accessible live status semantics.
- Keep the existing channel page visual language.

## Acceptance Criteria

- Scan queued/running/success/error states render in a compact, visually integrated treatment.
- Svelte checks and frontend build pass.

## Current Status

- Replaced the loose status line with an integrated channel-action status pill.
- Preserved live-region semantics and made error states alert/assertive.
- Review findings addressed: long errors wrap inside the pill, and error states use explicit red border/text styling.
- Verified with `pnpm check`, `pnpm build`, channel-page browser smoke, and `git diff --check`.
