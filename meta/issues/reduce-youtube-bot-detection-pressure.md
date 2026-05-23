# Reduce YouTube bot-detection pressure

## Summary

Kapsel currently retries yt-dlp failures quickly and performs channel/download yt-dlp calls without pacing. Recent deployed logs show repeated `Sign in to confirm you're not a bot` and age/cookie-related failures, with retries happening within seconds. Tube Archivist mitigates this by adding randomized sleep between yt-dlp-heavy operations, supporting cookies, optionally using a PO-token provider, and aborting bot-detection flows instead of hammering the same request.

## Requirements

- Add configurable pacing around yt-dlp calls, with a Tube Archivist-style default near 10 seconds randomized by roughly +/- 50%.
- Delay retries for yt-dlp failures instead of immediately re-running the same job.
- Detect YouTube bot/age-auth messages and avoid multiple immediate attempts for the same URL.
- Keep existing cookie-file support and document how it interacts with pacing.
- Consider PO-token provider support as a follow-up if pacing and cookies are insufficient.

## Acceptance Criteria

- Consecutive yt-dlp download/channel jobs are spaced out by the configured randomized interval.
- A `not a bot` yt-dlp failure does not trigger three immediate attempts within seconds.
- The behavior is covered by backend tests for command pacing or retry scheduling.
- Configuration defaults and environment documentation explain the pacing knob.

## Notes

- Tube Archivist references:
- `../tubearchivist/backend/appsettings/src/config.py` defaults `downloads.sleep_interval` to `10`.
- `../tubearchivist/backend/common/src/helper.py` randomizes sleep to +/- 50%.
- `../tubearchivist/backend/download/src/yt_dlp_base.py` detects `not a bot` and aborts the current flow.
- `../tubearchivist/backend/download/src/yt_dlp_handler.py` sleeps between queued downloads.
- Kapsel deployed logs showed immediate retry loops for bot-detection failures on May 10, 2026.
