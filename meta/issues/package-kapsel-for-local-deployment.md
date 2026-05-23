# Package Kapsel for local deployment

## Summary

Create a repeatable local deployment path for running Kapsel as an actual product rather than a development checkout.

## Requirements

- Add release build commands for the Go binary with embedded frontend assets.
- Provide a Docker or container-free local deployment option.
- Document required volumes, environment variables, and upgrade steps.
- Include a service example for long-running local use.
- Ensure the release path does not depend on frontend dev tooling at runtime.

## Acceptance Criteria

- A documented command builds a runnable release binary from a clean checkout.
- Deployment docs cover data directory, media directory, import root, signing secret, and authentication settings.
- A smoke test verifies the packaged binary serves health and frontend routes.
- Upgrade notes include database migration expectations and backup guidance.

## Notes

- Keep deployment single-node and simple; avoid Kubernetes or multi-service orchestration.
