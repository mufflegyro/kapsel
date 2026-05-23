# Add HTTP server request timeouts

## Summary

The production HTTP server configures `ReadHeaderTimeout` but not request body, write, or idle timeouts, allowing slow clients to hold connections open.

## Requirements

- Add reasonable `ReadTimeout`, `WriteTimeout`, and `IdleTimeout` values to the production HTTP server.
- Avoid breaking normal local media playback and API requests.
- Keep the E2E test server behavior deterministic.

## Acceptance Criteria

- Tests or focused inspection confirm the production server config includes the new timeouts.
- Existing backend tests pass.
- Browser smoke tests pass if timeout behavior affects frontend serving.

## Notes

- Review ref: `cmd/kapsel/main.go:249-253`.
