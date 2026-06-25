# Fix production download failures

## Summary

Production downloads started failing around 2026-06-23 or 2026-06-24. Investigate whether the Kapsel deployment needs a `yt-dlp` update, cookie refresh, configuration fix, or code change.

## Requirements

- Inspect the deployed Kapsel service and recent failed download jobs.
- Check runtime `yt-dlp`, Deno, cookies-file configuration, service health, and disk space.
- Apply the smallest safe fix for the root cause.
- Avoid exposing or copying the YouTube cookies file contents.

## Acceptance Criteria

- Root cause is identified from production evidence.
- A representative download succeeds after the fix, or the remaining blocker is documented.
- Any durable deployment context discovered during the incident is recorded in the appropriate local notes.

## Notes

- Kapsel production deployment details are recorded in the OpenCode Obsidian vault under `projects/kapsel.md`.
- On 2026-06-25, production `yt-dlp` was updated from stable `2026.03.17` to stable `2026.06.09`, then to nightly `2026.06.24.234707`; direct MP4 media downloads still failed with HTTP 403.
- A direct CT test with `--check-formats` skipped failing direct formats `136`, `135`, `134`, `133`, `160`, `140`, and `140-drc`, then selected working HLS format `95` for a representative failed video.
- Deployed a dirty build from local `db72972` plus this issue's patch to CT `119`; checksum `79fb19e68e73b0b74efd3079fcee9d690980cff2661b25fdee70f6eecb3ac38f`.
- Verification after deploy: readiness passed with `yt-dlp` nightly `2026.06.24.234707`, and retrying job `b7441f34-6922-441d-9036-357590df0d8b` succeeded for video `vhYS1diXZMM`.
