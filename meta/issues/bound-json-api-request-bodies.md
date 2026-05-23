# Bound JSON API request bodies

## Summary

Several JSON mutation endpoints decode request bodies without `http.MaxBytesReader`, allowing unnecessarily large bodies to consume memory and CPU.

## Requirements

- Add bounded JSON decoding for login, download, channel, and TubeArchivist import endpoints.
- Reject trailing JSON and unknown fields where feasible.
- Keep endpoint-specific limits small and documented in code.

## Acceptance Criteria

- Tests prove oversized JSON bodies are rejected for affected endpoints.
- Tests prove valid payloads still work.
- `go test ./...` passes.

## Notes

- Review refs: `internal/server/server.go:253`, `internal/server/server.go:833`, `internal/server/server.go:868`, `internal/server/server.go:1026`.
