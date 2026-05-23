# Harden signed media path serving

## Summary

Signed media URL generation and serving can follow symlinks under `MediaRoot`, which can expose files outside the configured media tree if a database media path points at a symlink.

## Requirements

- Reject media paths that traverse through symlinked parents or symlinked final files before signing URLs.
- Reject the same unsafe paths when serving `/media/...` requests.
- Keep existing signed media URL behavior for regular files under `MediaRoot`.
- Prefer a shared media path resolver instead of duplicating path safety checks.

## Acceptance Criteria

- A regression test proves a symlink under `MediaRoot` does not receive a signed URL and cannot be served.
- Existing media signing and serving tests continue to pass.
- `go test ./...` passes.

## Notes

- Review refs: `internal/server/server.go:2074-2088`, `internal/media/media.go:79-99`.
