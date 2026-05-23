# Bootstrap Go service skeleton

## Summary

Create the minimal Go backend structure with tests, formatting, lint-friendly layout, and a basic health endpoint.

## Requirements

- Initialize a Go module.
- Add a minimal HTTP server.
- Add a health endpoint.
- Add automated tests for the health endpoint.
- Add basic commands or documentation for running tests and the server.

## Acceptance Criteria

- `go test ./...` passes.
- The server can be started locally.
- `GET /api/health` returns a successful response.
- The structure leaves room for API, database, jobs, and media packages without premature abstraction.

## Notes

- Use the standard library where practical for the first pass.
