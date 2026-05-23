# Mount TubeArchivist media COW clone

## Summary

Give Kapsel a writable ZFS copy-on-write clone of the current TubeArchivist media tree so Kapsel can play imported media while TubeArchivist usage is phased out slowly.

## Requirements

- Snapshot the current TubeArchivist ZFS-backed storage before exposing media to Kapsel.
- Create a writable ZFS clone rather than copying the full media library.
- Bind-mount only the cloned TubeArchivist media directory into the Kapsel LXC at `/srv/kapsel/media`.
- Preserve existing Kapsel media files before the mount hides the current media root.
- Avoid mutating the live TubeArchivist container or media dataset.

## Acceptance Criteria

- Kapsel LXC starts with `/srv/kapsel/media` mounted from the cloned media tree.
- Imported videos with existing TubeArchivist media files report `archive_state: downloaded` and expose a signed `media_url`.
- Kapsel can write to the mounted media tree for future downloads.
- The local deployment runbook records the snapshot, clone, mount, and rollback path.

## Notes

- TubeArchivist source dataset is `Storage/subvol-105-disk-0`.
- TubeArchivist media tree is `/var/lib/docker/volumes/tubearchivist_media/_data` inside that dataset.
- Created snapshot `Storage/subvol-105-disk-0@kapsel-media-20260507` and clone `Storage/kapsel-ta-media-cow`.
- Added CT `119` mountpoint `mp0` from `/Storage/kapsel-ta-media-cow/var/lib/docker/volumes/tubearchivist_media/_data` to `/srv/kapsel/media` with `backup=0`.
- Preserved the pre-mount Kapsel media root at `/Storage/kapsel-media-before-cow-20260507-final` and copied it into the clone without overwriting TA files.
- Verified Kapsel health, Kapsel-user write access to `/srv/kapsel/media`, imported media availability for `R5omhngdnvM`, signed media range serving, and storage maintenance with `missing_references: 0`.
