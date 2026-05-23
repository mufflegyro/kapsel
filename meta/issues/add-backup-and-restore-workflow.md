# Add backup and restore workflow

## Summary

Provide a supported way to back up and restore Kapsel metadata and configuration for a local deployment.

## Requirements

- Add a backup command or API that safely snapshots SQLite metadata.
- Include enough configuration metadata to restore a usable archive.
- Document media-file backup expectations separately from database backup.
- Add restore validation before replacing an active database.
- Prevent restore while jobs are running unless explicitly forced.

## Acceptance Criteria

- Tests cover backup creation, restore validation, incompatible backup rejection, and active-job safeguards.
- README documents backup and restore procedures.
- Backups include schema version information.
- Restore failures do not corrupt the existing database.

## Notes

- Media files can remain filesystem-managed; this issue should not require a monolithic media archive file.
