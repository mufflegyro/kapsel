# Normalize job progress reporting

## Summary

Progress and lease heartbeat are coupled through `jobs.Store.Heartbeat`, while handlers have inconsistent policies for heartbeat errors. Download progress treats heartbeat failures as best-effort, while TubeArchivist import can fail the whole job on a progress heartbeat error.

## Requirements

- Define whether handler-reported progress is best-effort UI state or correctness-critical state.
- Keep runner-owned lease renewal separate from domain progress updates where practical.
- Apply a consistent heartbeat/progress error policy across download, import, preview, channel, and retention jobs.
- Document expected progress semantics per job type.

## Acceptance Criteria

- Progress update failures do not unexpectedly fail long-running domain work unless explicitly intended.
- Runner liveness heartbeat remains reliable even if progress reporting is best-effort.
- Tests cover progress update errors for at least download and import jobs.
- Comments or documentation explain progress ranges for job types that report coarse progress.

## Notes

- Advisor priority: medium.
- Relevant references: `internal/jobs/runner.go:102-117`, `internal/download/downloader.go:944-947`, `internal/taimport/importer.go:89-94`, and `internal/previews/previews.go:98-120`.
- A narrow reporter interface may be enough; avoid introducing a workflow engine.
