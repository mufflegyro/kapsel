# Reject unexpected bodies on bodyless mutation endpoints

## Summary

Several mutation endpoints do not define a request payload but currently ignore any request body and still perform the mutation. This allows clients to send arbitrary or oversized bodies to routes that should be bodyless.

## Requirements

- Reject non-empty request bodies on bodyless mutation endpoints.
- Cover logout, job cancel/retry, channel scan, catalog video download, channel delete, and playlist delete routes.
- Update frontend callers that currently send `{}` to bodyless POST endpoints.
- Use bounded body checks so unexpected oversized bodies cannot be consumed unbounded.
- Preserve valid no-body behavior for each endpoint.

## Acceptance Criteria

- Tests prove non-empty and oversized bodies are rejected without performing the mutation.
- Tests prove no-body requests still work for each affected endpoint.
- Frontend smoke coverage continues to pass after callers stop sending `{}` bodies.
- `go test ./...` passes.

## Notes

- Follow-up from review of `Bound JSON API request bodies`.
- Review refs: `internal/server/server.go:172`, `internal/server/server.go:183`, `internal/server/server.go:186`, `internal/server/server.go:199-205`, `internal/server/server.go:712-720`, `internal/server/server.go:920-1010`, `internal/server/server.go:1792-1841`.
