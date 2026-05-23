# Add observability and diagnostics

## Summary

Make the single-node app easier to operate by adding structured diagnostics for health, jobs, downloads, database, and storage.

## Requirements

- Add a readiness endpoint separate from basic health.
- Include database connectivity, migration status, media root accessibility, and `yt-dlp` availability.
- Add structured logging around job lifecycle and download failures.
- Add bounded diagnostic APIs for recent errors or system status.
- Avoid exposing secrets or unbounded logs.

## Acceptance Criteria

- Tests cover readiness success and failure states.
- Logs include job ID and type for job lifecycle events.
- Diagnostic API responses are bounded and redact sensitive values.
- README documents health and readiness endpoints.

## Notes

- Keep this lightweight; no metrics database or external observability stack is required.
