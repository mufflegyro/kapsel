# Disable implicit yt-dlp config loading

## Summary

`yt-dlp` may read configuration files from the current working directory and other default locations. The deployed service uses `/var/lib/kapsel` as its working directory, which is writable by the service user, so a writable `yt-dlp.conf` could affect future invocations.

## Requirements

- Prevent `yt-dlp` from loading implicit config files for every Kapsel-managed command.
- Keep explicit Kapsel options such as format selector, cookies file, output paths, subtitles, and catalog options working.
- Cover all command builders that invoke `yt-dlp`.

## Acceptance Criteria

- `BuildCommand`, `BuildOriginalAutomaticSubtitleCommand`, and `BuildChannelCatalogPageCommand` include `--no-config` or an equivalent explicit config isolation mechanism.
- Tests assert every generated `yt-dlp` command disables implicit config loading.
- Existing download, subtitle, and channel catalog command tests continue to pass.

## Notes

- Security review severity: High.
- Relevant references: `deploy/kapsel.service:12` and `internal/download/downloader.go:772-871`.
- This is a smaller near-term mitigation that should happen before the broader external-tool sandboxing issue is complete.
