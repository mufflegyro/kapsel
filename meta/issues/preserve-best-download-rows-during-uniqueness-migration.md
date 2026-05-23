# Preserve best download rows during uniqueness migration

## Summary

Migration 004 deduplicates historical download rows before adding the unique `(source, external_id)` index by keeping `max(id)`. For old databases with duplicate rows, that can discard a more useful successful row when a newer duplicate is failed or incomplete.

## Requirements

- Preserve the best available historical download row for each `(source, external_id)` group during migration.
- Prefer succeeded rows over failed, queued, running, or incomplete rows.
- Keep the uniqueness constraint behavior unchanged after migration.

## Acceptance Criteria

- A migration regression test covers duplicate historical rows where the newest row is not the best row.
- After migration, exactly one row remains for each non-empty `(source, external_id)` pair.
- The retained row preserves the most useful status/history according to the documented preference.
- Existing migration tests continue to pass.

## Notes

- This only affects databases that have not yet applied migration 004.
- Current runtime ingestion already uses uniqueness and UPSERT behavior for duplicate downloads.
