# Investigate the mark-as-played button's desktop vs mobile behavior difference

## Summary

The video detail page's "Mark as played" button appears to behave differently on desktop
vs mobile: on one platform it marks the video watched and the button disappears, on the
other it either does not stick, sends a degenerate payload, or never becomes visible in
the first place. The exact symptom has not been pinned down yet — this issue scopes a
short investigation to characterize the difference, fix the root cause, and lock the two
platforms into identical behavior with mobile e2e coverage.

## Background (what the code does today)

- **Button render:** `frontend/src/routes/WatchRoute.svelte` (~line 143) renders the
  button only when `video.item.media_url && !videoIsWatched(video.item)`, gated during
  flight by `markPlayedAction.status === 'loading'`.
- **Action:** `markVideoPlayed()` in `frontend/src/App.svelte` (~line 1555) PUTs
  `playedProgressPayload(item)` to `/api/videos/{id}/progress` with `watched: true`.
- **Payload source:** `playedProgressPayload(item)` (~line 1596) prefers the bound media
  element's live `duration` (`watchMediaElement?.duration`), falling back to
  `item.duration_seconds` / `item.progress.duration_seconds`, and sets
  `position = duration` when a duration is known. If none of the fallbacks are set, the
  payload degenerates to `{position_seconds: 0, duration_seconds: 0, watched: true}`.
- **Server:** `updateVideoProgress` (`internal/server/server.go` ~line 2048) merges
  payload, current progress, and near-completion; keeps watched monotonic; zero-duration
  payloads fall back to the current row's duration (which may itself be 0).
- **Layout/visibility CSS:** `.watch-row` and `.action-row` in
  `frontend/src/style.css` — at the `max-width: 780px` breakpoint the row becomes
  `flex-direction: column` with `justify-content: start` (vs `end` on desktop), and
  buttons wrap via `flex-wrap: wrap`.
- **Test coverage:** Playwright runs the same smoke suite on a `desktop` project
  (1280×800) and a `mobile` project (Pixel 5) — only the explore-link-editor test is
  viewport-skipped today. The mark-played e2e tests (`frontend/e2e/smoke.spec.js`
  ~lines 1914–1933, 2006–2035) so far assume the media element's duration is available.

## Requirements

- **Characterize the difference first:** run the watch-page mark-played flow on both
  Playwright projects (and, if possible, a real mobile device) and record exactly what
  diverges — button visibility, payload sent, resulting `/progress` state, and whether
  the button disappears afterward.
- **Find the root cause among the candidates:**
  - Media-element duration availability (autoplay/metadata-loading differences on
    touch devices; `preload="metadata"` + `onloadedmetadata` restore timing) feeding
    `playedProgressPayload`, producing a degenerate zero-duration payload on one platform.
  - Watched-state source merge (`item.progress.watched` vs `item.watched`) disagreeing
    between the two platform sessions.
  - Responsive layout pushing the button out of the tap area or off-screen at ≤780px.
  - Any `ontimeupdate`/pause-drive progress writes racing the explicit mark-played PUT.
- **Make both platforms behave identically** once the cause is known — prefer the
  smallest change that removes the divergence (e.g. a deterministic payload that does
  not depend on live media-element state, or a layout fix).
- **Add coverage:** a mobile-project e2e assertion that marks the fixture video played
  and checks the button disappears and `/progress` reports `watched: true`, mirroring
  the existing desktop assertion.

## Acceptance Criteria

- A written note in this issue (or the PR) states the observed desktop vs mobile
  difference and its root cause.
- The mark-played flow behaves the same on the desktop and mobile Playwright projects:
  same button lifecycle (visible → gone, or same hold-state rule), same payload sent,
  same `/progress` result.
- The e2e suite runs the mark-played scenario on the mobile project and passes.
- No regression on the existing desktop mark-played tests (`smoke.spec.js`).

## Notes

- Reference sites: `frontend/src/routes/WatchRoute.svelte:143-145`,
  `frontend/src/App.svelte:1555-1571` (`markVideoPlayed`), `1596-1600`
  (`playedProgressPayload`), `2192-2195` (`videoIsWatched`),
  `frontend/src/style.css` `.watch-row`/`.action-row` (~1993/2043) and the
  `max-width: 780px` block (~2930), `internal/server/server.go:2048-2087`
  (`updateVideoProgress`), `frontend/playwright.config.js` (desktop/mobile projects),
  `frontend/e2e/smoke.spec.js:1914-1933,2006-2035`.
- Once the media element is loaded (`onloadedmetadata={restoreAndAutoplay}`), desktop
  `watchMediaElement.duration` is authoritative, so the desktop e2e asserts
  `position_seconds: 125, duration_seconds: 125`. The mobile path is the one to stress:
  metadata may not be loaded when the button is tapped, which is the most likely driver
  of the payload divergence.
- Keep this scoped: it is an investigation + parity fix, not a redesign of the
  mark-played/keep-forever/delete action cluster.
- Estimated effort: small — one focused investigation pass plus ~20–40 lines of frontend
  and/or CSS with an added mobile e2e assertion.