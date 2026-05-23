# Delete emptied imported comments

## Summary

TubeArchivist comment import deletes search documents for empty comments but leaves the old comment rows visible.

## Requirements

- Delete or clear comment rows when imported comment text becomes empty.
- Keep search index cleanup in the same transaction.
- Preserve existing non-empty comment import behavior.

## Acceptance Criteria

- A regression test proves reimporting a previously non-empty comment as empty removes it from comments API/listing storage.
- Existing comment import tests pass.
- `go test ./...` passes.

## Notes

- Review refs: `internal/taimport/importer.go:613-639`.
