# Use public job DTOs

## Summary

Some job API responses expose the internal `jobs.Job` storage shape, including raw payload/result JSON. The list endpoint already uses a narrower DTO, but detail and mutation/live paths should consistently expose only public fields.

## Requirements

- Define one public job DTO for HTTP and live update responses.
- Avoid returning raw `payload_json` to normal browser clients.
- Preserve `result_summary` or other intentionally public result context.
- Keep diagnostic access explicit if raw job internals are needed.

## Acceptance Criteria

- `GET /api/jobs/{id}`, cancel/retry responses, and live job updates use the public DTO.
- Frontend job dashboard still renders status, progress, target links, errors, and summaries.
- Server tests cover absence of raw payload fields in public responses.

## Notes

- Review references: `internal/jobs/store.go:38`, `internal/server/server.go:743`, `internal/server/server.go:804`, and `frontend/src/App.svelte:782`.
- This is a boundary and privacy cleanup, not a job runner behavior change.
