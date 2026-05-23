# Rollback comment import transactions on panic

## Summary

`importComment` opens a transaction without the deferred rollback used by the other TA import paths, so panic paths can leak the transaction until connection cleanup.

## Requirements

- Add a deferred rollback after starting the comment import transaction.
- Keep explicit rollback behavior on normal error paths safe and idempotent.
- Preserve successful commit behavior.

## Acceptance Criteria

- `importComment` matches the transaction cleanup pattern used by channel, video, and playlist imports.
- Tests or reviewable coverage verify errors still roll back and successful imports still commit.

## Notes

- Review reference: `internal/taimport/importer.go:680-703`.
