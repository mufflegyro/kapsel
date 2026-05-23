# Build settings and first-run readiness UI

## Summary

Turn the settings page into a useful product surface for configuration visibility, environment validation, and first-run readiness.

## Requirements

- Show configured data, media, import, database, and `yt-dlp` paths.
- Show whether media signing, authentication, and import roots are configured safely.
- Surface runtime warnings for ephemeral signing secrets or missing tools.
- Provide copyable diagnostics for support/debugging.
- Keep secrets redacted.

## Acceptance Criteria

- Tests cover settings API output, secret redaction, and missing-tool warnings.
- The settings page displays readiness checks with pass/warn/error states.
- The UI never exposes raw secret values.
- README references the settings readiness page for first-run setup.

## Notes

- This should be read-only initially unless a later issue introduces persisted settings changes.
