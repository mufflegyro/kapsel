# Centralize shared utility helpers

## Summary

Small utility helpers and path-safety routines are duplicated across download, import, storage, and server code paths.

## Requirements

- Consolidate helpers where doing so reduces concrete divergence risk.
- Prioritize symlink/path-safety helpers over cosmetic utility extraction.
- Avoid broad refactors that do not reduce a specific maintenance risk.

## Acceptance Criteria

- Shared helper extraction preserves existing behavior under tests.
- Duplicate path-safety behavior is reduced or documented as intentionally separate.
- `go test ./...` passes.

## Notes

- Duplicate helpers include `firstNonEmpty`, `nullEmpty`, `boolInt`, subtitle format/language cleaners, and media-root symlink walking.
