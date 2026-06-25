# DEVLOG

## 2026-06-25

- Investigated production YouTube download failures in CT `119`. `yt-dlp` stable and nightly still received `HTTP Error 403: Forbidden` for direct MP4 media URLs, while `--check-formats` skipped the failing direct formats and selected a working HLS format. Added `--check-formats` to Kapsel video download commands.
- Deployed dirty build `db72972-dirty-20260625090843` to CT `119` with checksum `79fb19e68e73b0b74efd3079fcee9d690980cff2661b25fdee70f6eecb3ac38f`; representative retry job `b7441f34-6922-441d-9036-357590df0d8b` succeeded.
