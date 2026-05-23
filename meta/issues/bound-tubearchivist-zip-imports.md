# Bound TubeArchivist zip imports

## Summary

TubeArchivist backup import reads matching zip entries fully into memory, which can exhaust memory with large or compressed input.

## Requirements

- Add explicit per-entry uncompressed size limits for imported backup entries.
- Use bounded reads or streaming parsing instead of unbounded `io.ReadAll`.
- Report oversized entries as skipped or failed with a clear error.

## Acceptance Criteria

- A regression test with an oversized backup entry fails safely without reading the entire entry.
- Existing TubeArchivist import tests pass.
- `go test ./...` passes.

## Notes

- Review ref: `internal/taimport/importer.go:306-318`.
