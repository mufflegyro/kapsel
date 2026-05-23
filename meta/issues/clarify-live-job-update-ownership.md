# Clarify live job update ownership

## Summary

The downloads page receives broad live job snapshots and reconstructs filtered/paginated page state on the client. This duplicates server-side list semantics and can drift as job filtering or ordering evolves.

## Requirements

- Keep live updates bounded and observable.
- Avoid duplicating server pagination/filter policy in frontend merge logic.
- Preserve responsive updates for visible job rows.

## Acceptance Criteria

- Live update handling has a clear ownership model: either server sends page-aware updates or client applies deltas and refetches aggregates.
- Existing live update smoke tests continue to pass.
- Job dashboard behavior remains stable for filtered and non-first pages.

## Notes

- Review references: `internal/server/live.go:102`, `frontend/src/App.svelte:671`, and `frontend/src/App.svelte:708`.
- This can be handled after public job DTOs are introduced.
