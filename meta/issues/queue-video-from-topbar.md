# Queue a video from the topbar on any page

## Summary

The topbar shows two circular actions on every view: the first links to
`/downloads` ("Open queue") and the second to `/settings`. Downloading a
video currently requires navigating to the Downloads page first. Change the
first topbar action so that instead of navigating it pops up a slimline
popover with a video URL text input and an "Add video" button, letting a user
queue a video to the worker queue from any page. The Settings action stays a
link.

The full Downloads page must stay reachable: the sidebar already links to it,
and the popover itself keeps a small "Open queue" link.

## Requirements

- Frontend only; reuse the existing `POST /api/downloads` enqueue flow and
  the existing `videoJob` state machine / `addVideo()` helper so queued state
  stays consistent between the popover and the Downloads page.
- The topbar action becomes a `button` that toggles the popover
  (`aria-expanded`, `aria-haspopup="dialog"`, keeps the download icon and
  circular styling).
- Popover content: slim URL input + "Add video" submit button, inline
  queued/running/succeeded/failed status (compact copy mirroring the
  Downloads page), and an "Open queue" link to `/downloads`.
- Popover behavior: auto-focus the input on open, close on Escape and on
  outside pointer-down, return focus to the trigger on close. Sticky topbar
  means the popover must render above content (`z-index`).
- Same shared `videoURL` binding and disabled logic as the Downloads page
  (`videoSubmitDisabled`).

## Acceptance Criteria

- From any route (home, watch, channels, playlists, settings, downloads),
  clicking the first topbar circle opens the popover without navigating; the
  URL stays put.
- Pasting a valid video URL and pressing "Add video" enqueues a download job
  (`POST /api/downloads`), shows "Video download queued." inline, and does
  not navigate away.
- Escape closes the popover and restores focus to the trigger; clicking
  outside closes it.
- The Downloads page is still reachable from the sidebar and from the
  popover's "Open queue" link.
- Covered by the e2e smoke suite: the popover can queue a video from the home
  page, and the sidebar still reaches the Downloads page.

## Notes

- No backend change: `addVideo` already refreshes the jobs list when
  `path === '/downloads'` and updates `videoJob` through the existing poll /
  live-websocket path, so the popover status stays live with zero extra
  plumbing.
- The popover only queues single videos (as requested); channel queueing
  stays on the Downloads page.
