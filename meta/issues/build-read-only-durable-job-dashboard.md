# Build read-only durable job dashboard

## Summary

Replace the placeholder downloads page with a read-only dashboard for queued, running, completed, failed, and cancelled jobs.

## Requirements

- Add paginated job listing APIs with status filters.
- Expose job type, status, progress, error text, timestamps, and result summaries.
- Render a responsive downloads dashboard in the frontend.
- Poll for updates without requiring a page refresh.
- Keep job list responses bounded.

## Acceptance Criteria

- Tests cover job listing pagination and status filtering.
- The downloads page shows current and recent jobs with progress and errors.
- Failed jobs surface actionable error text.
- Job list responses do not expose unbounded payload history.

## Notes

- Cancellation, retry, and logs are separate follow-up issues.
