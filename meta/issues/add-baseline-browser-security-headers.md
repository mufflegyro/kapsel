# Add baseline browser security headers

## Summary

Kapsel does not set baseline browser security headers on frontend, API, or media responses. This increases the impact of any future XSS, MIME sniffing, clickjacking, or referrer leakage issue.

## Requirements

- Add security headers through shared middleware or equivalent central response handling.
- Include protections for framing, MIME sniffing, referrer policy, and script/content loading.
- Avoid breaking Svelte assets, media playback, WebSocket connections, thumbnails, captions, and timeline previews.

## Acceptance Criteria

- Responses include `X-Content-Type-Options: nosniff`.
- Responses include framing protection via `Content-Security-Policy` `frame-ancestors` and/or `X-Frame-Options`.
- Responses include a conservative `Referrer-Policy`.
- A Content Security Policy is added or explicitly documented as deferred with a narrower follow-up issue.
- Tests cover representative API, frontend, and media responses for expected headers.
- Browser smoke tests continue to pass.

## Notes

- Security review severity: Medium.
- Relevant reference: `internal/server/server.go:248-308`.
- CSP must allow current app needs, including same-origin scripts/styles, media, WebSockets, and any currently displayed external thumbnail URLs if applicable.
