# Reject missing WebSocket origins

## Summary

The live jobs WebSocket origin check accepts requests with no `Origin` header. Browsers normally send `Origin` for WebSocket handshakes, but accepting a missing value weakens cross-origin WebSocket protections for custom or unusual clients.

## Requirements

- Reject WebSocket upgrades with a missing or invalid `Origin` when authentication or cookies are in use.
- Preserve legitimate same-origin browser WebSocket connections.
- Keep behavior clear for local non-browser diagnostics if such clients are intentionally supported.

## Acceptance Criteria

- `validWebSocketOrigin` rejects missing `Origin` for normal app requests, or requires an equivalent authenticated WebSocket token.
- Tests cover same-origin acceptance, cross-origin rejection, malformed origin rejection, and missing-origin rejection.
- Browser smoke tests continue to receive live job updates.

## Notes

- Security review severity: Medium.
- Relevant reference: `internal/server/live.go:211-222`.
- If a non-browser client path is needed, it should use explicit authentication rather than bypassing origin checks implicitly.
