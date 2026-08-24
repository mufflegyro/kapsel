# Opt-in retention for manual downloads

## Summary

Auto-download retention currently applies only to videos in the `channel_auto` origin; manually downloaded videos are never auto-deleted. Add an opt-in so manual downloads can be included in the same watched/stale retention cleanup when the user explicitly selects it.

## Requirements

- Keep manual (and imported) videos out of retention by default — no behavior change for existing users.
- Add an opt-in control so a user can opt manual downloads into the same retention rules as auto-downloads.
- Apply the standard retention rules when opted in: latest-two per channel, started-videos kept, unstarted-stale removed after two weeks, watched cutoffs, and `keep_forever` override.
- Preserve catalog metadata when media is removed (video becomes catalog-only and re-downloadable), matching the existing retention behavior.
- Keep the decision bounded, durable, observable, and safe to retry.

## Acceptance Criteria

- Default: manual downloads are never eligible for retention removal.
- When the opt-in is enabled, manually downloaded videos follow the documented retention rules.
- `keep_forever` still fully protects opted-in manual media.
- Tests cover the default off behavior and the opted-in behavior (manual media removed after the applicable cutoff).
- The opt-in is discoverable in the UI or settings and clearly documented.

## Notes

- This is a follow-up to the implemented `add-auto-download-retention-policy.md` (auto-only) and `expire-watched-auto-downloads.md`.
- Decision needed: implement as a global setting, a per-video flag, or both. A global setting is the smaller first step; per-video can be layered later.
- Retention cleanup runs via the scheduled hourly job; no new scheduler is required.
