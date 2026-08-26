# Save YouTube videos to the queue from the browser (userscript prototype)

## Summary

Follow-up to the topbar quick-queue: make it possible to queue a YouTube video
into Yummle's worker queue from *inside* YouTube, without visiting Yummle.
Prototype this as a Tampermonkey/Violentmonkey userscript that injects a small
save button onto YouTube video thumbnails (hover to reveal, like TubeArchivist
Companion). Clicking it POSTs the video URL to the existing
`POST /api/downloads` endpoint — the same queue the topbar "Queue a video"
panel uses — so queued videos appear on the Downloads page with live status.
Zero backend changes: the enqueue API, URL normalization, and duplicate-active-
job dedupe already exist.

This is deliberately a userscript prototype, not a packaged extension: it
validates the injection UX and the API contract with the smallest possible
artifact. A follow-up can graduate it to a Firefox MV3 WebExtension (background
`fetch` with `host_permissions`, options page), reusing this script's
selectors and button design. TubeArchivist Companion
(`tubearchivist/browser-extension`, GPL-3.0) is the UX reference; the
userscript here is written fresh (no GPL code) and targets Yummle's API.

## Requirements

- Script file `scripts/yummle-save.user.js` with a `==UserScript==` header:
  - `@match https://www.youtube.com/*` and `https://m.youtube.com/*`,
    `@exclude` embeds, `@noframes`, `@run-at document-idle`.
  - `@grant GM_xmlhttpRequest, GM_getValue, GM_setValue, GM_registerMenuCommand`.
  - `@connect 127.0.0.1` and `@connect localhost` so the cross-origin POST to
    the local server needs no user prompt; a custom server URL is entered via a
    menu command and must be added to `@connect` by the user.
- Button injection on YouTube video thumbnails:
  - Find anchors matching `a[href*="/watch?v="]` and `a[href^="/shorts/"]`;
    extract the 11-character video id; skip already-processed anchors.
  - Add a circular download-icon button overlaid on the thumbnail's top-left
    corner (small `position: relative` mutation on the anchor, absolutely
    positioned child, high z-index). Hidden by default, revealed on anchor
    hover; always visible on touch (`@media (hover: none)`).
  - Use a `MutationObserver` on `document.body` for added nodes plus a periodic
    rescan as a belt-and-braces fallback against YouTube's lazy rendering and
    SPA re-renders (YouTube re-renders thumbnails constantly).
- Queueing behaviour:
  - Click → `GM_xmlhttpRequest` POST `${server}/api/downloads` with
    `{"url": "https://www.youtube.com/watch?v=<id>"}`, JSON headers,
    ~15s timeout. `GM_xmlhttpRequest` bypasses CORS for the whitelisted
    `@connect` host, so no server-side CORS headers are needed.
  - Button state feedback: idle (download icon) → in-flight → queued (green
    check, ~2.5s) or error (red cross, tooltip with the server error /
    "unreachable"), then reset to idle. Ignore clicks while in-flight/queued.
  - The server already dedupes active download jobs, so re-clicking the same
    video is safe.
- Configuration:
  - `GM_registerMenuCommand` entries: "Set Yummle server URL" (prompt, stored
    via `GM_setValue`) and "Open Yummle downloads" (opens `<server>/downloads`).
  - Default server `http://127.0.0.1:18080` (matches the local test instance).
- Auth note: works out of the box with `KAPSEL_AUTH_MODE=disabled`. With auth
  enabled, `GM_xmlhttpRequest` sends the browser's cookies for the target
  origin, so being logged into Yummle in the same browser profile is expected
  to carry the session; a token-based auth path is a follow-up.

## Acceptance Criteria

- Installed via Tampermonkey or Violentmonkey in Firefox, on
  `https://www.youtube.com/` the save icon appears on video thumbnails (watch
  grid, channel/playlist views, search results) on hover.
- Clicking a thumbnail's save icon queues the video: it appears in
  `GET /api/jobs` and on the Downloads page, without leaving YouTube.
- Shorts links (`/shorts/<id>`) queue correctly; non-video links are ignored.
- The button shows queued/error feedback and re-arms afterwards; repeated
  clicks on the same video do not create duplicate active jobs.
- Changing the server URL via the menu command is reflected on the next click.
- `node --check` passes on the script; no backend changes in this issue.

## Notes

- Deliverable is the prototype script + issue; README/install docs and the
  packaged Firefox WebExtension (background-script fetch with
  `host_permissions`, options page for server + auth, icon in the toolbar) are
  the follow-up issue.
- The watch page's "Up next" and related-video rows get buttons too (same
  selector) — accepted for the prototype; the WebExtension follow-up can scope
  this to feed/result surfaces.
- YouTube DOM churn is the long-term maintenance cost; keep selectors broad
  and the processed-anchor guard idempotent so re-renders are cheap.
