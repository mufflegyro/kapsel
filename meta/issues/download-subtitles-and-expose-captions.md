# Download subtitles and expose captions

## Summary

Download subtitles during archive ingestion, store them durably, expose compatible caption tracks in the player, and index transcript text for search.

## Requirements

- Configure `yt-dlp` to download subtitles when available.
- Decide whether automatic captions are included by default or configured separately.
- Store subtitle language, source, format, path, and searchable text.
- Expose WebVTT-compatible tracks to the Video.js player.
- Import subtitle metadata and text from TubeArchivist backups when available.

## Acceptance Criteria

- Tests cover subtitle download command arguments and subtitle metadata ingestion.
- Tests cover transcript indexing and highlighted search snippets.
- The watch page exposes at least one compatible subtitle track when present.
- Large subtitle text is not returned unbounded through video detail APIs.

## Notes

- Start with downloaded/manual subtitles and WebVTT/SRT support before advanced transcript UI.
