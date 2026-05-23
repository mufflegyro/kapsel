# Harden SQLite concurrency and schema versioning

## Summary

Make database behavior safer under concurrent web requests and background jobs, and prevent accidental startup against unsupported schema versions.

## Requirements

- Review SQLite connection pool settings for concurrent reads, job heartbeats, imports, and progress writes.
- Keep WAL mode, foreign keys, and busy timeout behavior explicit.
- Record and check schema version compatibility at startup.
- Refuse to start when the database schema is newer than the binary understands.
- Document forward-only migration expectations.

## Acceptance Criteria

- Tests cover startup against current, older, and unsupported newer schema versions.
- Tests or stress-style integration coverage exercise concurrent job and HTTP database access without `database is locked` failures.
- README documents migration and downgrade behavior.
- Existing migrations still run on a clean database.

## Notes

- Keep this SQLite-only; do not introduce an external database service.
