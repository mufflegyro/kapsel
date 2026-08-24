# Fix YouTube download 403 and raise default resolution to 1080p

## Summary

Downloads from YouTube intermittently failed with `HTTP Error 403: Forbidden` after the media-transfer phase. Root cause: the Homebrew stable `yt-dlp` `2026.07.04` was outdated against YouTube's current player and generated media URLs that YouTube rejected with 403. The bundled nightly builds (`2026.08.19`/`2026.08.20`) fixed it. While here, raise the default format selector ceiling from 720p to 1080p.

## Requirements

- Prefer a freshly updated project-local `yt-dlp` (nightly) over the lagging Homebrew stable.
- Keep running on macOS without a custom JS-runtime flag by selecting Homebrew's Deno runtime.
- Raise the default `KAPSEL_YTDLP_FORMAT` selector to a `height<=1080` ceiling.
- Keep `--check-formats` so failing direct MP4 formats fall back to a working selection.

## Acceptance Criteria

- A representative @betterstack video downloads successfully at 1080p with the bundled nightly.
- Verified output is `h264`, `1920x1080` via `ffprobe`.
- Defaults are consistent across `internal/download` and `internal/config`, with passing tests.
- The deploy env example documents the 1080p selector.

## Notes

- Reproduced on 2026-08-24: same machine/command/video fails with `2026.07.04` and succeeds at 1080p with `2026.08.19`/`2026.08.20` nightlies.
- The current downloaded file is `1920x1080`, bitrate ~431 kbps, ~28 MB (job `d401794a-...`).
- Non-fatal cosmetic item: automatic-subtitle (`en-orig`) fetch can hit `HTTP Error 429` but the download job still succeeds; consider tightening later.
- Follow-up: add a Youtarr-style daily `yt-dlp --update-to nightly` so the bundled binary does not rot against future YouTube changes.
