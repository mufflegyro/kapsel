# Serve description timestamps as video chapters

## Summary

Expose timestamped video descriptions as a WebVTT chapters track so chapter navigation can be added later when the player UI is ready.

## Requirements

- Add a backend endpoint for `GET /api/videos/{id}/chapters.vtt`.
- Parse common description chapter lines such as `00:00 - Intro`, `30:26 - Topic`, and `1:02:03 - Closing`.
- Emit valid WebVTT chapter cues using the next chapter timestamp or video duration as cue end time.
- Attach the chapters track to watch-page videos when media is playable.
- Keep parsing bounded and safe; chapter labels must be plain text in VTT output.

## Acceptance Criteria

- Videos with timestamped descriptions expose a `chapters` text track in the watch player.
- The chapters endpoint returns `text/vtt; charset=utf-8` with correctly ordered cues.
- Descriptions without parseable chapters return no chapter track or a not-found chapters endpoint.
- The chapter endpoint is protected by the same auth behavior as other video detail endpoints.

## Notes

- The motivating format is:

```text
00:00 - G16 review
30:26 - Culture battle around G16
35:22 - G16 bombs
40:20 - Ghostbusters Afterlife f**king sucks
52:30 - Ghostbusters Wreckoning
```
