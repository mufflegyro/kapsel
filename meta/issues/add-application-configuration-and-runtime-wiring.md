# Add application configuration and runtime wiring

## Summary

Wire the existing packages into the runnable `kapsel` binary through explicit application configuration.

## Requirements

- Define configuration for listen address, database path, media root, and media signing secret.
- Load configuration from environment variables with safe development defaults.
- Open SQLite, run migrations, create the job store, and mount media serving at startup.
- Ensure required runtime directories are created when missing.
- Keep package initialization testable without starting a real network listener.

## Acceptance Criteria

- Tests cover default config loading and environment overrides.
- Tests cover application setup creating directories and applying migrations.
- The backend binary uses the configured database, job store, and media root.
- `GET /api/health` still works after runtime wiring.

## Notes

- Prefer small `internal/config` and `internal/app` packages over adding logic directly to `main`.
