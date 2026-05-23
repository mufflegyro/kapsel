# Sync channel video catalog metadata

## Summary

Let channel scans load and sync metadata for all videos on a channel, even before those videos are downloaded.

## Requirements

- Add a catalog-only video state for videos discovered from channel scans but not downloaded.
- Sync title, description, duration, publication date, and thumbnail for discovered channel videos.
- Show catalog-only videos on channel pages with black-and-white thumbnails.
- Make catalog-only videos clearly non-playable until downloaded.
- Keep scans paginated or otherwise bounded for large channels.

## Acceptance Criteria

- Tests cover inserting new catalog-only videos, updating existing metadata, duplicate suppression, and preserving downloaded media state.
- Channel pages show downloaded and catalog-only videos distinctly.
- Catalog-only cards include a download action instead of opening a missing player.
- Large channel catalog APIs are bounded.

## Notes

- This is metadata sync, not automatic download of every channel video.
