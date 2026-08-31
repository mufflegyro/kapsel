# Add settings page for auth and archive maintenance configuration

## Summary

Surface the configuration that currently lives only in environment variables through an in-app settings page, starting with authentication and video archive maintenance settings such as cleanup and download cycles.

## Requirements

- Add a settings page (or grouped sections) in the frontend, gated to authenticated users.
- Cover authentication settings: auth mode, username, session TTL, and related auth/session options.
- Cover video archive maintenance settings: channel auto-download interval, retention/watched-after cleanup values, and disk-space guard minimums.
- Persist settings to the database or a settings store loaded at startup, so changes survive restarts without editing environment variables.
- Keep environment variables as an override or bootstrap mechanism, with a clear precedence rule (e.g. explicit env var wins over stored setting, or stored setting wins and env is only used on first start).
- Reflect the active values in the API responses and UI (read-back), including validation errors from the server (e.g. invalid TTLs, paths, or retention values).
- Do not surface every variable — keep this page scoped to auth and archive maintenance; other config (ytdlp, ffmpeg, media signing) can follow separately.

## Acceptance Criteria

- An authenticated user can change auth and archive maintenance settings from the settings page and the changes take effect without an app restart where supported, or trigger a clearly communicated restart/apply flow where they do not.
- Settings persist across app restarts.
- Invalid values are rejected with a server-side error surfaced in the UI.
- API endpoints for reading and updating settings exist and are authorized.
- Tests cover persistence, precedence between env vars and stored settings, and validation.

## Related

- `add-application-configuration-and-runtime-wiring.md` — establishes the base config loading and env-var surface; this issue builds on that foundation by adding stored settings and a UI on top of it.

## Notes

- Reuse `internal/config` for value validation (durations, sizes, paths) so stored values pass through the same checks as env values.
- Consider whether live-appliable settings (intervals, retention) can be applied to the scheduler in-place versus requiring a restart, and document which is which on the settings page.
