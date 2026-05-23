# Reject overbroad TubeArchivist API import roots

## Summary

Settings diagnostics report filesystem-root TubeArchivist API import roots as unsafe, but the API still registers and accepts them as allowlists.

## Requirements

- Enforce the same overbroad-root rejection in API import normalization or endpoint setup.
- Do not register or accept API-triggered imports from the filesystem root.
- Preserve normal imports confined to a specific configured directory.

## Acceptance Criteria

- `KAPSEL_IMPORT_ROOT=/` or an equivalent filesystem root is rejected by the API path, not only diagnostics.
- Valid non-root import roots continue to work.
- Regression coverage verifies overbroad import roots are refused.

## Notes

- Review references: `internal/server/server.go:210-212`, `internal/server/server.go:652-663`, and `internal/taimport/importer.go:137-163`.
