# Wait for job runner during server shutdown

## Summary

Server shutdown cancels the job runner but does not wait for it before closing application resources.

## Requirements

- Track the job runner goroutine started by `runServer`.
- Wait for `application.RunJobs` to exit during graceful shutdown.
- Preserve error logging for unexpected runner failures.

## Acceptance Criteria

- Shutdown does not close the app/database before the job runner exits.
- A test or small integration-style coverage verifies the shutdown wait behavior where practical.
- Existing server shutdown behavior remains bounded by the configured shutdown timeout or an explicit timeout decision.

## Notes

- Review reference: `cmd/kapsel/main.go:248-280`.
