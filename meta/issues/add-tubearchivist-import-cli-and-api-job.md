# Add TubeArchivist import CLI and API job

## Summary

Expose the TubeArchivist importer through user-facing entry points instead of only package-level tests.

## Requirements

- Add a CLI import command or subcommand for local imports.
- Add an API endpoint that enqueues a TubeArchivist import job.
- Run imports through the durable job runner.
- Persist import report details in the job result or a related table.

## Acceptance Criteria

- Tests cover CLI or command-level import invocation.
- Tests cover API import job enqueueing.
- Import job progress/errors can be queried after completion.
- Malformed records still report as skipped without aborting safe imports.

## Notes

- Keep direct package importer support for tests and future UI flows.
