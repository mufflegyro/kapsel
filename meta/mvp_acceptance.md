# MVP Household Archive Acceptance Test

This checklist defines the product-level path Kapsel must satisfy before it should be considered a usable v1.0 household video archive. It intentionally favors a boring, local-first loop over feature breadth.

## v1.0 Acceptance Path

- [ ] Complete first-run setup and configuration readiness checks before archive actions are exposed. Satisfied by [Build settings and first-run readiness UI](issues/build-settings-and-first-run-readiness-ui.md), [Add yt-dlp readiness and version diagnostics](issues/add-ytdlp-readiness-and-version-diagnostics.md), and [Harden SQLite concurrency and schema versioning](issues/harden-sqlite-concurrency-and-schema-versioning.md).
- [ ] Require explicit local authentication for non-development use, including safe session handling for mutating APIs. Satisfied by [Add local authentication and session protection](issues/add-local-authentication-and-session-protection.md).
- [ ] Add a direct video URL, enqueue a durable download job, see job status, and end with a playable archived video. Satisfied by [Set browser-safe 720p download defaults](issues/set-browser-safe-720p-download-defaults.md), [Harden download path and metadata validation](issues/harden-download-path-and-metadata-validation.md), [Make download ingestion atomic and idempotent](issues/make-download-ingestion-atomic-and-idempotent.md), [Build read-only durable job dashboard](issues/build-read-only-durable-job-dashboard.md), and [Add direct video download flow](issues/add-direct-video-download-flow.md).
- [ ] Add a channel, sync the channel catalog, show catalog-only videos with black-and-white thumbnails, and let the user choose a catalog-only video to download. Satisfied by [Build thumbnail and preview pipeline](issues/build-thumbnail-and-preview-pipeline.md), [Sync channel video catalog metadata](issues/sync-channel-video-catalog-metadata.md), and [Add manual channel scan and selective downloads](issues/add-manual-channel-scan-and-selective-downloads.md).
- [ ] Watch an archived video in the browser, pause, refresh or restart the app, and resume from saved playback progress. Satisfied by [Persist playback progress from the web player](issues/persist-playback-progress-from-the-web-player.md).
- [ ] Search local archive metadata and open hydrated video or channel results instead of raw search references. Satisfied by [Hydrate search results with archive records](issues/hydrate-search-results-with-archive-records.md).
- [ ] Restart Kapsel during or after archive activity without exposing half-ingested videos as playable content. Satisfied by [Define archive integrity invariants](issues/define-archive-integrity-invariants.md), [Make download ingestion atomic and idempotent](issues/make-download-ingestion-atomic-and-idempotent.md), and [Add storage maintenance and orphan cleanup](issues/add-storage-maintenance-and-orphan-cleanup.md).
- [ ] Back up the SQLite metadata and restore it into a clean data directory with clear media-directory expectations. Satisfied by [Add backup and restore workflow](issues/add-backup-and-restore-workflow.md) and [Package Kapsel for local deployment](issues/package-kapsel-for-local-deployment.md).
- [ ] Run the v1.0 path in deterministic smoke tests without live YouTube network calls by using fixtures, fake job runners, or seeded archive data. Satisfied by [Add browser end-to-end smoke tests](issues/add-browser-end-to-end-smoke-tests.md).
- [ ] Surface basic failure recovery guidance when downloads, scans, imports, disk-space checks, or readiness checks fail. Satisfied by [Add disk-space guards for downloads](issues/add-disk-space-guards-for-downloads.md), [Build read-only durable job dashboard](issues/build-read-only-durable-job-dashboard.md), [Add job cancellation and retry controls](issues/add-job-cancellation-and-retry-controls.md), and [Add observability and diagnostics](issues/add-observability-and-diagnostics.md).

## Smoke Test Mapping

- Backend smoke: migrate a fresh SQLite database, verify readiness, enqueue fake download and channel scan jobs, persist progress, search seeded records, and verify backup/restore metadata checks. Covered by [Add browser end-to-end smoke tests](issues/add-browser-end-to-end-smoke-tests.md), [Define archive integrity invariants](issues/define-archive-integrity-invariants.md), and [Add backup and restore workflow](issues/add-backup-and-restore-workflow.md).
- Browser smoke: open first-run/settings, log in, queue a direct video, view the job dashboard, open home, open a watch page, save/resume progress, search, open a channel page, and verify catalog-only download actions. Covered by [Add browser end-to-end smoke tests](issues/add-browser-end-to-end-smoke-tests.md).
- Fixture strategy: tests should use local fixture metadata, fixture thumbnails, fake `yt-dlp` output, and seeded media files so the acceptance path can run without live YouTube network calls.

## Deferred After v1.0

- Scheduled automatic subscription scans are deferred until manual channel scan and selective downloads are reliable. Tracked after [Add manual channel scan and selective downloads](issues/add-manual-channel-scan-and-selective-downloads.md).
- Timeline hover previews are useful polish but not required for the first product loop. Tracked by [Generate timeline hover previews](issues/generate-timeline-hover-previews.md).
- Downloaded subtitles and transcript search are important but can follow the core download/watch/search loop. Tracked by [Download subtitles and expose captions](issues/download-subtitles-and-expose-captions.md).
- Comment import and bounded comment browsing are deferred archive enrichment. Tracked by [Import comments with bounded browsing](issues/import-comments-with-bounded-browsing.md).
- Full channel and playlist management can follow catalog sync, direct downloads, and hydrated search. Tracked by [Build channel and playlist management](issues/build-channel-and-playlist-management.md).
