# Prevent live-update refresh loops

## Summary

Some pages can appear to refresh continuously, likely after job websocket updates schedule repeated REST refreshes. Refreshing the browser page stops the behavior, which suggests client-side live update state can enter a bad loop.

## Requirements

- Identify which websocket or polling path repeatedly reloads page data.
- Avoid full page/list refreshes unless required to reconcile missing or out-of-window data.
- Prefer merging live job updates into visible state without replacing unrelated route state.
- Keep fallback polling/retry behavior bounded so failed refreshes do not spin indefinitely.

## Acceptance Criteria

- Visible job updates are merged without forcing repeated full refreshes of the current page.
- Refresh scheduling is bounded or deduplicated when websocket snapshots arrive frequently.
- Regression coverage exercises the refresh-loop trigger.
- Existing live job update smoke coverage still passes.
