# Expire watched auto-downloads

## Summary

Auto-download retention currently keeps watched videos indefinitely. Watched auto-downloaded media should age out soon after viewing unless explicitly protected.

## Requirements

- Remove watched channel-auto media after 24 hours.
- Keep `keep_forever` as the indefinite protection override.
- Preserve existing protections for manual and imported media.
- Preserve existing metadata-only behavior when retained media is removed.

## Acceptance Criteria

- Watched channel-auto media older than 24 hours is removed by retention cleanup.
- Recently watched channel-auto media is kept until it crosses the 24-hour threshold.
- Keep-forever watched media is not removed.
- Manual and imported media remain outside auto-download retention.

## Current Status

- Implemented watched channel-auto retention with a 24-hour watched cutoff.
- Watched media cleanup is independent of the latest-two-per-channel protection.
- Review finding addressed: deletion revalidates watched and stale retention eligibility inside the delete transaction before clearing media.
- Existing unstarted stale auto-download cleanup still uses the two-week cutoff and latest-two rule.
- Verified with focused retention tests, full Go tests, metadata tests, and diff whitespace checks.
