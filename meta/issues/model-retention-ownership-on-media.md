# Model retention ownership on media

## Summary

Auto-download retention currently decides cleanup eligibility by joining videos to `downloads.origin = 'channel_auto'`. That makes cleanup depend on job/audit history instead of durable media ownership, and imported media with valid `media_path` records has no explicit retention origin.

## Requirements

- Add an explicit retention ownership field to durable media metadata, such as `videos.media_origin` or `media_assets.origin`.
- Preserve conservative behavior for existing data: imported TubeArchivist media should not become auto-cleanup eligible by default.
- Mark future manual downloads, channel auto-downloads, and imported media with clear origins.
- Update retention candidate selection to use durable media ownership metadata instead of `downloads.origin`.
- Keep `keep_forever` as an override that protects media regardless of origin.
- Keep `downloads` as job/audit history rather than the source of truth for cleanup eligibility.

## Acceptance Criteria

- A schema migration records media retention origin for existing and future media.
- Manual downloads remain protected from auto-download retention.
- Channel auto-download retention still removes only eligible auto-owned media.
- Imported TubeArchivist media is classified separately and is not auto-cleaned unless explicitly opted into a future policy.
- Tests cover manual, channel-auto, and imported media origins in retention candidate selection.

## Notes

- This became visible after mounting a COW clone of TubeArchivist media: imported videos have real media files but no `downloads` rows.
- Current behavior is safe but awkward: imported media is playable and excluded from retention because it lacks download history.
- Implemented with `videos.media_origin` and `videos.media_downloaded_at` in migration `012_video_media_origin.sql`.
- Existing media is backfilled from successful download history where present; media without download history stays `imported`.
- Retention now selects candidates from `videos.media_origin = 'channel_auto'` and `videos.media_downloaded_at`, and re-checks current origin/timestamp before deletion.
- TubeArchivist imports classify imported media as `imported`; manual and channel-auto downloads set the corresponding media origin.
- Deployed to CT `119` on 2026-05-07; schema version is `12`, readiness passes, imported media remains playable, and deployed media origin counts are `imported: 1708`, `channel_auto: 33`.
