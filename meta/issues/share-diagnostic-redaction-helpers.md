# Share diagnostic redaction helpers

## Summary

Job log and diagnostic redaction regexes are duplicated, increasing the risk that a new secret pattern is sanitized in one path but leaked in another.

## Requirements

- Extract shared redaction helpers for URLs and common secret patterns.
- Use the shared helpers from job logging and download diagnostics.
- Preserve current truncation and readability behavior.

## Acceptance Criteria

- Existing log/diagnostic sanitization tests pass or new tests cover both call sites.
- No behavior regression for existing redaction examples.
- `go test ./...` passes.

## Notes

- Review refs: `internal/jobs/runner.go:26-33`, `internal/download/diagnostics.go:21-28`.
