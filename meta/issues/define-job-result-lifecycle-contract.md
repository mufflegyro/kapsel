# Define job result lifecycle contract

## Summary

`jobs.result_json` currently carries too many meanings: final user-facing result, UI summary source, retry-safety marker, stale-recovery success latch, and sometimes partial diagnostic report. This makes crash recovery and cancellation behavior hard to reason about.

## Requirements

- Define what `result_json` means for queued, running, succeeded, failed, and cancelled jobs.
- Distinguish final committed results from partial diagnostic reports.
- Ensure stale recovery does not treat arbitrary non-empty result JSON as proof of success.
- Preserve useful job summaries in the UI.

## Acceptance Criteria

- The job lifecycle contract is documented in code near `jobs.Job`, `Runner.finish`, or store transition methods.
- Partial reports from import or retention failures cannot cause a stale running job to become `succeeded` incorrectly.
- Tests cover a handler writing partial result data, returning an error, and stale recovery after a simulated crash.
- Existing successful result summaries still appear in job APIs and the downloads dashboard.

## Notes

- Advisor priority: highest-leverage architecture cleanup.
- Relevant references: `internal/jobs/store.go:323-329`, `internal/jobs/runner.go:131-156`, `internal/taimport/importer.go:95-105`, and `internal/download/downloader.go:910-918`.
- Possible designs include separate final/partial result fields, an explicit committed marker, or stricter `SetFinalResult` semantics.
