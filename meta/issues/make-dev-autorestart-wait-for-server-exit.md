# Make dev autorestart wait for server exit

## Summary

The `mise dev` autorestart command can start a replacement server before the previous `go run` child has released the Kapsel database lock, causing startup to fail with `kapsel database lock is already held`.

## Requirements

- Keep the development server rebuild-and-run workflow.
- Ensure restart signals are forwarded to the server process.
- Ensure the supervised command does not exit until the server process has exited.
- Avoid weakening the production database lock.

## Acceptance Criteria

- `mise dev` uses a supervised command that waits for the server child on termination.
- Script syntax is validated.
- Relevant metadata checks pass.

## Notes

- The application lock is correctly preventing concurrent DB users; the issue is the watcher command lifecycle.
- Implemented by replacing the inline `pnpm build && go run` dev command with `scripts/dev-server.sh`, which builds a dev binary and `exec`s it so watchexec supervises the actual server process.
- Verified with `sh -n scripts/dev-server.sh`, `go test ./internal/applock ./internal/app ./meta`, and `git diff --check`.
