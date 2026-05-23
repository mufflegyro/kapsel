# Rework channel-first download boundary

## Summary

The `channel_first_download` job is a composite workflow: it syncs channel catalog metadata and then performs an inline first-video download. The nested download is invoked with an empty job id, which suppresses normal download progress and result handling for that inner unit of work.

## Requirements

- Decide whether channel-first is a single composite UX job or a workflow of smaller jobs.
- Make progress, result, cancellation, and retry semantics explicit for both the catalog and first-download phases.
- Preserve the current add-channel user flow.

## Acceptance Criteria

- Channel-first jobs no longer rely on an undocumented empty job id sentinel for nested download behavior.
- The first-video download either appears as its own normal `download` job or reports progress/result through the parent job intentionally.
- Tests cover catalog-success/download-failure behavior and cancellation during the nested download phase.
- UI behavior for adding a channel remains coherent.

## Notes

- Advisor priority: medium.
- Relevant references: `internal/download/downloader.go:1017-1068`, `internal/download/downloader.go:921-987`, and `internal/download/downloader.go:2096-2116`.
