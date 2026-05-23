# Add disk-space guards for downloads

## Summary

Prevent downloads and imports from filling the local disk without warning.

## Requirements

- Check available disk space for configured data and media roots.
- Add configurable minimum free-space headroom.
- Refuse to start large archive work when space is below the configured threshold.
- Surface low-space warnings in readiness/settings and job errors.
- Keep checks portable across supported local deployment targets.

## Acceptance Criteria

- Tests cover threshold parsing, low-space rejection, and sufficient-space acceptance through injectable filesystem stats.
- Download jobs fail early with clear low-space errors when below threshold.
- Settings/readiness can show low-space warnings.
- README documents the free-space configuration.

## Notes

- Do not attempt exact remote video size prediction in the first pass; start with configurable headroom checks.
