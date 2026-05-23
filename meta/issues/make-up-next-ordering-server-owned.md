# Make Up next ordering server owned

## Summary

The dedicated Up next endpoint now selects and orders candidates, but the frontend still defensively re-sorts the response. This leaves two authorities for recommendation ranking.

## Requirements

- Treat `/api/videos/{id}/up-next` as the canonical owner of Up next ordering.
- Remove duplicate frontend tier computation or limit it to stable rendering fallback only.
- Keep newest-first ordering within backend tiers.
- Preserve autoplay target behavior.

## Acceptance Criteria

- Frontend renders Up next recommendations in endpoint order.
- Browser smoke still verifies playable-first Up next behavior.
- Backend tests remain the source of truth for tier ordering.

## Notes

- Review references: `internal/server/server.go:1623`, `frontend/src/App.svelte:151`, and `frontend/src/App.svelte:1359`.
- The server may return explicit rank/tier metadata later if the UI needs to explain grouping.
