# Add automatic yt-dlp nightly updates

## Summary

Kapsel relies on a project-local `yt-dlp` (currently a bundled `bin/yt-dlp-nightly`). YouTube changes its player and format handling frequently, so an outdated `yt-dlp` produces `HTTP Error 403` against direct MP4 media URLs (see `fix-youtube-download-403-and-raise-default-to-1080p.md`). Add automatic updates of the bundled `yt-dlp` on a daily schedule so Kapsel always runs a current nightly, mirroring Youtarr's approach.

## Requirements

- Check the configured `yt-dlp` for an available newer nightly on a daily schedule (Youtarr uses ~4:00 AM local time).
- Update the `yt-dlp` binary in place by running `yt-dlp --update-to nightly` (or `--update-to @latest` on the configured release channel).
- Never update while a download job is actively running.
- Keep the update durable, observable, and safe to retry; do not block downloads while updating.
- Surface the current, latest, and update-available state in the existing diagnostics/settings surfaces so it is observable.
- Make updates safe on managed platforms where the binary is read-only or not updatable in place; degrade gracefully.

## Acceptance Criteria

- A scheduler creates/triggers a durable update job on the configured cadence.
- The update job runs `yt-dlp --update-to nightly` against the configured `KAPSEL_YTDLP_PATH`.
- An active download blocks the update until safe, or the update is skipped/warned.
- A verified post-update `yt-dlp --version` is reported.
- State (current version, latest version, last attempt, error) is exposed via diagnostics and does not block other jobs on failure.
- Covered by unit tests for the update decision logic and the no-update-while-downloading guard.

## Notes

- Reference: Youtarr's `server/modules/ytdlpModule.js` checks GitHub API for the latest version, caches it, compares date-based versions, and runs `yt-dlp --update-to @latest` daily at 4:00 AM, refusing while a download is in progress.
- This is backend-only; an in-app manual "Check for updates" button can be a small follow-up.
- The configured release channel is currently the bundled nightly; keep it configurable in principle.
