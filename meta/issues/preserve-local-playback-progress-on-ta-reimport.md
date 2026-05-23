# Preserve local playback progress on TA reimport

## Summary

Re-importing older TubeArchivist data can overwrite newer local playback progress or clear a local watched state.

## Requirements

- Avoid downgrading local watched state during TA reimport.
- Avoid decreasing local playback position unless there is an explicit reason or force behavior.
- Preserve useful duration updates without losing newer local progress.

## Acceptance Criteria

- Reimporting a video with older TA progress does not clear `watched=true`.
- Reimporting a video with lower TA position does not reduce a higher local position.
- Regression tests cover stale reimport scenarios.

## Notes

- Review references: `internal/taimport/importer.go:578` and `internal/taimport/importer.go:915-923`.
