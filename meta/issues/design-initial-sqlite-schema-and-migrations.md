# Design initial SQLite schema and migrations

## Summary

Design the first SQLite schema for archive metadata, playback progress, jobs, settings, and search indexing.

## Requirements

- Define tables for videos, channels, playlists, playlist entries, downloads, user progress, jobs, and settings.
- Decide how media paths, thumbnails, subtitles, comments, and external IDs are represented.
- Add a migration mechanism.
- Enable SQLite WAL mode in the application setup.
- Add tests covering migration from an empty database.

## Acceptance Criteria

- A fresh database can be created through migrations.
- Schema creation is deterministic and tested.
- Key lookup paths have indexes.
- Large fields and searchable text have a documented storage strategy.

## Notes

- Keep the schema normalized enough to avoid Elasticsearch-style denormalized drift.
