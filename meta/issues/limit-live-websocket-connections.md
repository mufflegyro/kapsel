# Limit live WebSocket connections

## Summary

Each `/api/live` WebSocket connection keeps a goroutine and polls recent jobs every second. There is no global, per-client, or per-session connection cap, so many connections can create avoidable memory, socket, and database load.

## Requirements

- Add a bounded connection policy for live WebSocket clients.
- Reject excess connections with a clear HTTP error before upgrading.
- Ensure connection counters are decremented on disconnect and error paths.
- Preserve normal single-browser and reconnect behavior.

## Acceptance Criteria

- Live WebSocket upgrades are limited by a documented global and/or per-client cap.
- Excess connection attempts return `429 Too Many Requests` or another explicit non-upgrade error.
- Tests cover limit enforcement and cleanup after disconnect.
- Browser smoke live-update flows still pass.

## Notes

- Security review severity: Medium.
- Relevant references: `internal/server/live.go:40-84` and `internal/server/live.go:102-133`.
- Consider a conservative default that fits a household deployment, such as a small per-client cap plus a larger global cap.
