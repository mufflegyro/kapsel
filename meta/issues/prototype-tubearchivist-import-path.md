# Prototype TubeArchivist import path

## Summary

Create a prototype importer that can read an existing TubeArchivist archive layout and populate Kapsel's SQLite database.

## Requirements

- Identify which TubeArchivist data sources are needed for a useful migration.
- Import video, channel, playlist, media path, thumbnail, and watched/progress metadata where available.
- Report unsupported or skipped data clearly.
- Add fixture-based importer tests.

## Acceptance Criteria

- A small fixture archive imports successfully.
- Imported videos are visible through the backend API.
- Import errors are reported without aborting the whole import when safe.
- Unsupported fields are documented.

## Notes

- Full migration compatibility can be broken into follow-up issues after the prototype.
