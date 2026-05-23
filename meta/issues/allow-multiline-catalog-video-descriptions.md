# Allow multiline catalog video descriptions

## Summary

Catalog scans reject otherwise valid videos when descriptions contain newlines or tabs, which are common in YouTube metadata.

## Requirements

- Validate catalog video descriptions with text-safe metadata rules that allow normal newlines, carriage returns, and tabs.
- Keep title validation strict for single-line metadata.
- Continue rejecting unsafe control characters.

## Acceptance Criteria

- Catalog entries with multiline descriptions are imported.
- Catalog entries with unsafe control characters are still rejected.
- Regression coverage is added for multiline catalog descriptions.

## Notes

- Review references: `internal/download/downloader.go:892-895`, `internal/download/downloader.go:1375-1396`.
