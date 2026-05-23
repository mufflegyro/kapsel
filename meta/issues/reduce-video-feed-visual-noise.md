# Reduce video feed visual noise

## Summary

Video cards and the library sort toolbar repeat state that is already visible elsewhere in the feed, making the home and channel grids noisier than necessary.

## Requirements

- Keep the thumbnail-level `Metadata only` indicator for catalog-only videos.
- Remove redundant media availability text below video titles on feed cards.
- Remove the toolbar title and current-sort label when the sort select already communicates the active sort.
- Keep pagination/feed summary text visible.

## Acceptance Criteria

- Catalog-only cards no longer show `Metadata only` or `No media file downloaded yet` below the channel/title metadata.
- Playable cards no longer show `Playable local media` or `Ready to watch locally` below the channel/title metadata.
- The library toolbar no longer shows `Archive feed` or `Newest first`, while preserving the result summary and sort selector.
- Frontend checks and browser smoke tests pass.
