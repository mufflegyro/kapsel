# Centralize job enqueue deduplication

## Summary

Enqueue and duplicate-prevention policy is spread across API handlers, downloader helpers, scheduler code, and exact payload comparisons. Some jobs dedupe active work, while channel scans, channel creation, imports, and maintenance jobs have different behavior.

## Requirements

- Centralize job creation rules behind small domain helpers or store methods.
- Use stable canonical payloads or explicit dedupe keys for active-job checks.
- Keep validation and normalization close to the domain that understands the payload.
- Avoid introducing an external queue or broad workflow engine.

## Acceptance Criteria

- Manual downloads, catalog downloads, channel scans, preview jobs, imports, and scheduled jobs have documented dedupe behavior.
- Concurrent semantically equivalent enqueue requests cannot create duplicate active jobs where dedupe is expected.
- Server handlers call focused enqueue helpers rather than open-coding normalization, active lookup, and enqueue sequences.
- Tests cover duplicate requests for at least downloads, channel scans, and imports or document intentionally allowing duplicates.

## Notes

- Advisor priority: medium.
- Relevant references: `internal/server/server.go:807-840`, `internal/server/server.go:852-883`, `internal/server/server.go:928-973`, `internal/server/server.go:1055-1089`, and `internal/download/downloader.go:343-371`.
- A generic `FindOrEnqueue` helper with canonical payloads may be enough.

## Dedupe Behavior

- Manual downloads and catalog download API requests normalize to a canonical direct YouTube video URL and reuse any active download job for that video, regardless of origin.
- Channel auto catalog downloads reuse non-cancel-requested active download jobs for the same canonical video URL.
- Channel scans normalize the channel URL and channel ID, then reuse an active scan job with the same canonical payload.
- Timeline preview jobs are keyed by canonical `video_id` payloads and reuse active preview jobs.
- TubeArchivist imports clean the absolute import root and reuse active import jobs for the same root.
- Channel auto-download scheduling keeps one current non-cancel-requested active job per channel; an active job created before the channel's latest scan is stale and may be replaced.
- Retention cleanup scheduling keeps one non-cancel-requested active cleanup job at a time, with the existing interval gate still preventing routine re-enqueue.
