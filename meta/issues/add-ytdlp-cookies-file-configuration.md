# Add yt-dlp cookies file configuration

## Summary

Downloads can be more reliable when yt-dlp has access to a browser-exported YouTube cookies file. Kapsel should support a configured cookies file without storing cookie contents in the repository or exposing them in diagnostics.

## Requirements

- Add a `KAPSEL_YTDLP_COOKIES_FILE` configuration option.
- Pass the configured cookies file to yt-dlp commands using `--cookies`.
- Apply the cookies file to direct downloads, subtitle follow-up downloads, and channel catalog commands.
- Document safe cookie-file handling for deployment.

## Acceptance Criteria

- Command-builder tests cover `--cookies` on video, subtitle, and channel commands.
- Config tests cover loading `KAPSEL_YTDLP_COOKIES_FILE`.
- Deployment env example documents the cookies-file option without including cookie contents.
- Existing download tests pass.

## Notes

- Cookie files are credentials. They should live outside the repo, be owned by the Kapsel service account, and use restrictive permissions.
