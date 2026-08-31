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
## Notes

- This is a follow-up to the implemented `add-auto-download-retention-policy.md` (auto-only) and `expire-watched-auto-downloads.md`.
- Decision needed: implement as a global setting, a per-video flag, or both. A global setting is the smaller first step; per-video can be layered later.
- Retention cleanup runs via the scheduled hourly job; no new scheduler is required.

## Resolution

Implemented as a global operator opt-in, matching the established retention-config pattern (env-only, like `KAPSEL_RETENTION_WATCHED_AFTER` — the `settings` table remains unused). The watched branch was already origin-agnostic (per `expire-watched-auto-downloads`), so the gap was the stale branch, which was auto-only:

- `KAPSEL_RETENTION_INCLUDE_MANUAL` (default off, `boolOrDefault`) → `config.RetentionIncludeManual` → `download.Config.RetentionIncludeManual` → merged in `ApplyAutoDownloadRetention` (operator opt-in cannot be revoked per call, mirroring the watched-cleanup opt-out).
- With the opt-in, the stale branch widens in `retention.go`: channel-bound manual downloads join the per-channel newest-2 ranking (`media_origin IN ('channel_auto','manual')`), and channel-less manual downloads (direct URL, `channel_id IS NULL`) become eligible once unstarted + past the stale cutoff via a new `manual_unranked` arm. Started, watched-recent, and `keep_forever` protections are unchanged; imported media never join the stale branch.
- Both eligibility queries (candidate scan + transactional recheck) now render from one shared `retentionEligibilityQuery` builder so the two can never drift; default mode renders byte-identical semantics to the previous hardcoded queries.

Tests: `TestAutoDownloadRetentionKeepsUnwatchedManualMediaByDefault` (default off, all origins), `TestRetentionIncludeManualOptsManualMediaIntoStaleCleanup` (opted-in: third-ranked channel manual + channel-less manual removed; newest two, started, keep-forever, imported kept), `TestRetentionIncludeManualConfigReachesRetentionJob` (env flag through the real retention job path), `TestAutoDownloadRetentionRechecksManualOriginEligibility` (recheck-level default-off skip / opted-in removal), plus config parse assertions. Documented in `docs/deployment.md` and `deploy/kapsel.env.example`. Status: **landed 2026-08-31**.
