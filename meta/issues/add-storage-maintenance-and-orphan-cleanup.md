# Add storage maintenance and orphan cleanup

## Summary

Add tools for understanding local storage use and cleaning orphaned metadata or files safely.

## Requirements

- Report media, thumbnail, subtitle, database, and derived asset storage usage.
- Detect files under media roots that are not referenced by SQLite.
- Detect metadata rows that reference missing files.
- Provide dry-run cleanup before destructive actions.
- Expose maintenance actions through CLI and UI where appropriate.

## Acceptance Criteria

- Tests cover orphan detection, missing-file detection, and dry-run output.
- Destructive cleanup requires explicit confirmation or a separate command.
- The settings or maintenance page shows storage usage summaries.
- Cleanup never escapes configured storage roots.

## Notes

- Prefer conservative reporting before deletion.
