# Centralize job creation and active lookups

## Summary

Some code paths bypass `jobs.Store` and insert/query job rows directly. This spreads queue schema knowledge into downloader and server code and makes job lifecycle changes harder to keep consistent.

## Requirements

- Route all job creation through `jobs.Store` or a focused store method.
- Move active job lookup by payload/video into the job or domain layer.
- Preserve duplicate-active-job prevention behavior.
- Keep job payload matching bounded and testable.

## Acceptance Criteria

- Downloader code no longer inserts rows into `jobs` directly.
- Server active job lookup delegates to a focused helper instead of owning raw payload scans.
- Existing job, download, and server tests pass.

## Notes

- Review references: `internal/jobs/store.go:114`, `internal/download/downloader.go:1494`, `internal/server/server.go:1188`, and `internal/server/server.go:1208`.
- This also supports future public job DTO cleanup.
