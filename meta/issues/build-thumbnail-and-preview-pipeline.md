# Build thumbnail and preview pipeline

## Summary

Ensure thumbnails and preview imagery show up reliably across home, watch, channel, search, and recommendation views.

## Requirements

- Download thumbnails for newly archived videos when available.
- Import thumbnails from TubeArchivist backups when available.
- Store thumbnail assets with safe relative paths and signed URLs.
- Provide stable fallback thumbnails when source thumbnails are missing.
- Support grayscale styling for catalog-only channel videos that are not downloaded yet.

## Acceptance Criteria

- Tests cover downloaded thumbnail ingestion, imported thumbnail metadata, missing-thumbnail fallback, and signed thumbnail URL generation.
- Home, watch, channel, search, and recommendation cards display thumbnails or deterministic fallbacks.
- Catalog-only videos can render black-and-white thumbnails without implying playable media exists.
- Thumbnail paths cannot escape configured media roots.

## Notes

- Timeline hover previews are tracked separately in `generate-timeline-hover-previews.md` so the still-thumbnail pipeline can land first.
