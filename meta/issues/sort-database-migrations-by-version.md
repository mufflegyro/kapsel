# Sort database migrations by version

## Summary

Database migrations are sorted lexicographically before parsing versions, making future unpadded migration names order-sensitive.

## Requirements

- Parse migration versions before sorting.
- Sort migrations by integer version rather than path string.
- Preserve validation for contiguous versions and duplicate versions.

## Acceptance Criteria

- Migration loading applies `2_*.sql` before `10_*.sql` if such names are ever present.
- Existing zero-padded migration behavior remains unchanged.
- Regression coverage exercises numeric sorting independent of lexicographic path order.

## Notes

- Review reference: `internal/database/database.go:171-197`.
