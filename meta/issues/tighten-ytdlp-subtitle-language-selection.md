# Tighten yt-dlp subtitle language selection

## Summary

Kapsel currently passes `--write-auto-subs --sub-langs en.*,.*-orig` to yt-dlp, which can match YouTube auto-translated captions such as `en-zh-Hant` (`English from Chinese (Traditional)`). We should download manual subtitle tracks plus original-language auto-caption tracks while avoiding auto-translation endpoints.

## Requirements

- Select manual subtitle tracks in their native languages.
- Select original-language auto-caption tracks when yt-dlp reports them.
- Do not request automatic subtitle translation tracks.
- Avoid unnecessary subtitle download requests that increase rate-limit exposure.
- Preserve imported subtitle storage and transcript indexing behavior.

## Acceptance Criteria

- A regression test covers the main media command and verifies it uses `--write-subs --sub-langs all` without `--write-auto-subs`.
- A regression test covers the subtitles-only command and verifies it uses `--no-simulate --skip-download --write-auto-subs --sub-langs .*-orig`.
- Download ingestion merges requested manual subtitles with requested original auto-caption subtitles.
- Download ingestion still succeeds with media and manual subtitles if the original auto-caption-only command fails.
- Cancellation or timeout during the original auto-caption-only command still fails the job instead of being treated as a best-effort subtitle miss.
- Download ingestion does not run a subtitles-only command when yt-dlp reports only auto-translated automatic captions.
- Auto-translated captions such as `en-zh-Hant` are not requested by default.

## Notes

- Observed on YouTube video `yXbJe-rUNP8`, where yt-dlp lists `en-zh-Hant` as `English from Chinese (Traditional)`.
- The failed deployed job hit `HTTP Error 429` while downloading `en-zh-Hant`; Deno installation does not address that rate-limit failure.
- For `yXbJe-rUNP8`, `--write-subs --sub-langs all` selected the original/manual tracks `zh-Hant`, `en-CA`, and `fr-FR` without translated `en-*` variants.
- For `yXbJe-rUNP8`, `--write-auto-subs --sub-langs .*-orig` selected only `en-orig`.
- The subtitles-only command must include `--no-simulate`; otherwise `--dump-single-json` exits successfully without writing the caption file.
