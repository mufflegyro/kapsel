# Clarify scheduler job ownership

## Summary

Scheduler loops live in `app.go`, while scheduling policy and job-table checks live inside the download package. This is acceptable today, but ownership is unclear and some scheduler queries bypass `jobs.Store` abstractions.

## Requirements

- Define which layer owns scheduler loops and which layer owns scheduling policy.
- Keep durable scheduled work represented as jobs rather than inline background work.
- Route scheduler job-table checks through `jobs.Store` or documented store helpers where practical.
- Avoid moving to an external scheduler.

## Acceptance Criteria

- Channel auto-download and retention scheduling responsibilities are documented or separated into focused helpers.
- Scheduler active-job checks use shared store methods where practical.
- Retention scheduling has clear behavior after failures, including whether backoff is needed.
- Tests cover scheduler dedupe and failure/backoff behavior.

## Notes

- Advisor priority: low to medium.
- Relevant references: `internal/app/app.go:117-153`, `internal/download/downloader.go:413-528`, and `internal/download/downloader.go:1567-1689`.
