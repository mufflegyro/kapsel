# Import TubeArchivist backups in dependency order

## Summary

TubeArchivist zip import currently processes entries in archive order, so playlists and comments can be skipped if they appear before their referenced videos.

## Requirements

- Import dependency-bearing records in stable phases instead of raw zip order.
- Ensure channels and videos are imported before playlists and comments that reference them.
- Preserve skip reporting for genuinely invalid records.

## Acceptance Criteria

- A zip with playlist entries before video entries imports the playlist entries successfully.
- A zip with comments before video entries imports comments successfully after videos exist.
- Existing import progress/reporting remains coherent.

## Notes

- Review references: `internal/taimport/importer.go:259-280`, `internal/taimport/importer.go:612-643`, and `internal/taimport/importer.go:802-812`.
