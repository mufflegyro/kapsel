# Make download ingestion atomic and idempotent

## Summary

Ensure interrupted or repeated downloads do not leave duplicate, half-visible, or inconsistent archive state.

## Requirements

- Use explicit states for in-progress, succeeded, failed, and catalog-only videos where needed.
- Ensure re-downloading the same source video updates existing records instead of duplicating them.
- Make database writes for one ingested video transactional.
- Define cleanup behavior for temporary or partial files.
- Support safe retry behavior for failed downloads.

## Acceptance Criteria

- Tests cover duplicate download attempts, failed ingestion rollback, retry after failure, and idempotent metadata updates.
- Partially ingested videos are not shown as playable.
- Successful retries produce one canonical video record.
- Job results clearly identify whether the video was newly archived or updated.

## Notes

- Full crash recovery can be incremental, but visible archive state must remain consistent.
