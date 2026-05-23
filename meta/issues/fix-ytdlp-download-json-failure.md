# Fix yt-dlp download JSON failure

## Summary

Some video downloads appear to complete the actual media transfer but then fail with an error whose detail is yt-dlp JSON metadata, for example `yt-dlp command failed at "/usr/local/bin/yt-dlp": {"id": ... "formats": ...}`. This suggests Kapsel is treating a post-download yt-dlp exit/status or output-shape problem as a failed download after data has already been written.

## Requirements

- Identify why yt-dlp metadata JSON is surfaced as the command failure after the download phase.
- Preserve real yt-dlp failures as actionable job errors.
- Avoid marking a download failed when yt-dlp produced the expected downloaded media and metadata needed by Kapsel.
- Store the full redacted yt-dlp failure diagnostic in the job row so post-failure investigation does not depend on truncated UI text.
- Keep list/diagnostics API surfaces bounded where needed without truncating the durable database error.

## Acceptance Criteria

- A regression test covers the observed failure mode where yt-dlp returns JSON output around a downloaded video.
- Successful downloads that provide media paths are stored as completed videos, not failed jobs.
- Failed yt-dlp commands store full redacted stdout/stderr diagnostics in `jobs.error`.
- Public diagnostic summary endpoints may truncate for UI safety, but the database row preserves the full redacted error.

## Notes

- Reported example video id: `dJ1mzBetNTg`.
- The UI error begins with `Could not download video: yt-dlp command failed at "/usr/local/bin/yt-dlp": {"id": ... "formats": ...}`.
