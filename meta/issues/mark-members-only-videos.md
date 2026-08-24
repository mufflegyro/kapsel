# Mark members-only videos and disable their download

## Summary

Downloading a YouTube members-only video fails with a raw yt-dlp error like:

```
ERROR: [youtube] hD37el3bCw4: This video is available to this channel's members on level: Friends of the Pod (or any higher level). Join this channel to get access to members-only content and other exclusive perks.
```

Kapsel currently leaves the video as `catalog-only` and shows a confusing generic download failure. Detect members-only videos, disable the download action, mark the video so the UI labels it "Members only" instead of "Metadata only", and stop retrying the download since it cannot succeed without membership.

## Requirements

- Detect the members-only failure message from yt-dlp and mark the affected video (persisted column) so the state is durable, not just the transient job error.
- Never retry a members-only download; finish the job with a clear "members only" reason.
- Disable the download action in the UI for members-only videos (VideoCard and watch page).
- Change the visual tag from "Metadata only" to "Members only" for members-only videos.
- Keep the video browsable as catalog metadata (title, description, thumbnail) with a clear explanation.

## Acceptance Criteria

- A members-only video download marks the video `members_only` and does not retry.
- The media-only and watch page UI show "Members only" instead of "Metadata only" and do not offer a download button.
- The failure message surfaced to the user is friendly (e.g. "Members only — join the channel to watch"), not the raw yt-dlp trace.
- Regression tests cover the failure classification, DB marker, retry suppression, and API `members_only` exposure.
- Frontend `pnpm check` passes after the component changes.

## Notes

- The members-only message pattern is stable enough to match on "available to this channel's members", "members-only", or "join this channel".
- Implementation mirrors how channel catalog-only deletion and archive_state are already exposed (`archive_state`, `can_download` in video responses).