# Use approximate catalog release dates

## Summary

Kapsel currently discovers channel catalog videos with `yt-dlp --flat-playlist` and only stores `published_at` when flat playlist entries include `upload_date` or `timestamp`. On the current local deployment, catalog-only videos do not include those fields with the previous command, so newly scanned channel catalogs sort by scrape/update time instead of release date. Use yt-dlp's approximate channel-page date extraction so catalog-only videos get a stable release-date approximation without a per-video metadata call.

## Decision

Implement approximate page-level dates. Exact per-video metadata hydration is not needed for now.

## Requirements

- Add `youtubetab:approximate_date` to flat channel catalog extraction.
- Preserve the fast flat channel scan as the discovery path.
- Continue to parse `timestamp` and `upload_date` into `published_at` for catalog videos.
- Avoid exact per-video metadata calls for this behavior.

## Acceptance Criteria

- Flat channel catalog commands request yt-dlp approximate dates.
- New catalog-only videos can fill `published_at` from yt-dlp's parsed `timestamp` without downloading media or fetching each video page.
- Regression tests cover the yt-dlp extractor argument and timestamp-to-date parsing.
- Existing channel catalog tests continue to pass.

## Notes

- TubeArchivist channel subscription scans first use flat extraction to discover video IDs, then by default fetch full metadata for each missing video before adding it to `ta_download` pending queue. Its default `subscriptions.extract_flat` is `false`.
- TubeArchivist stores pending-video `published` from `timestamp` first, then from `upload_date`; its Elasticsearch mapping accepts epoch seconds or date strings.
- Kapsel currently stores downloaded video `published_at` from full download metadata and catalog video `published_at` only from flat-entry `upload_date` or `timestamp`.
- Current deployed data check: downloaded videos have `published_at`; catalog-only videos do not.
- yt-dlp can parse the relative date text already present on channel grid pages by adding `--extractor-args youtubetab:approximate_date` to flat playlist extraction. Verified locally and in CT `119` with `yt-dlp 2026.03.17` against `https://www.youtube.com/@OneyPlays/videos`.
- With `youtubetab:approximate_date`, flat entries include a `timestamp` parsed from visible relative text such as `2 days ago`; this does not require one extra call per video, but it is approximate rather than the exact upload timestamp/date from a full video metadata fetch.
- Matching TubeArchivist's default exact behavior would still mean one extra YouTube metadata extraction per catalog video that needs hydration, unless we find another exact batch-capable source. That remains out of scope for now.
