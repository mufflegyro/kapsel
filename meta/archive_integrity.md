# Archive Integrity Invariants

These invariants define the valid relationship between SQLite metadata and files under Kapsel's configured storage roots. They are the baseline for download hardening, channel catalog sync, storage maintenance, backup, and restore behavior.

## Video States

- `catalog-only`: Metadata was discovered from a channel scan, import, or search source, but no local media file is archived yet. Catalog-only videos may have title, description, duration, publication date, thumbnail, and source URL metadata. They must not have a playable media path.
- `downloaded`: The video has a validated local media file and may have thumbnail, subtitle, comment, and derived preview assets. Downloaded videos require a valid media path under configured storage roots before they can be shown as playable.
- `missing`: The database has a record for a video that previously referenced local media, but the media file is absent or unavailable. Missing videos must not be presented as playable until repaired, re-downloaded, or explicitly marked catalog-only.
- `failed`: A download, import, scan, or metadata refresh failed before producing a consistent archive record. Failed state can keep job and diagnostic context, but it must not expose a playable media path.
- `partial`: Work is in progress or was interrupted before finalization. Partial state must not expose local media as playable and should be safe for retry or cleanup.

## Asset Ownership

- Media files belong to exactly one downloaded video record unless a future deduplication issue defines shared ownership explicitly.
- Thumbnail files belong to a video, channel, playlist, or derived catalog record. Missing thumbnails must use deterministic UI fallbacks rather than unsafe external hotlinks.
- Subtitle files belong to one video and one language/source tuple. Subtitle text can be mirrored into search documents, but large transcript bodies must remain bounded in APIs.
- Comment records belong to one video and may link to parent comments. Comment search documents must point back to their owning video.
- Derived preview files, including timeline hover previews, belong to one video and must be regenerated or deleted with that video according to maintenance policy.
- All file paths stored in SQLite must be canonical relative paths under configured storage roots. Absolute paths, parent-directory traversal, and paths outside configured storage roots are invalid.

## Idempotency Expectations

- Repeating the same TubeArchivist import updates canonical records instead of creating duplicates.
- Repeating the same direct video download updates or reuses the existing video record for the same source/external ID.
- Repeating a channel catalog sync updates catalog-only metadata without replacing downloaded media state.
- Repeating thumbnail, subtitle, comment, and derived preview ingestion replaces or upserts owned assets for the same owner/kind tuple.
- Failed or partial jobs must be retryable without creating duplicate videos, duplicate media assets, or visible half-ingested playable records.

## Validation Coverage

- `internal/archive.ValidateVideoFileMetadata` defines reusable validation for valid and invalid video/file metadata states.
- [Harden download path and metadata validation](issues/harden-download-path-and-metadata-validation.md) must use these invariants before marking a job succeeded.
- [Make download ingestion atomic and idempotent](issues/make-download-ingestion-atomic-and-idempotent.md) must preserve these invariants across retries and interrupted work.
- [Add storage maintenance and orphan cleanup](issues/add-storage-maintenance-and-orphan-cleanup.md) must use these states to distinguish safe cleanup from repairable missing media.
- [Sync channel video catalog metadata](issues/sync-channel-video-catalog-metadata.md) must preserve downloaded state while creating or updating catalog-only records.
