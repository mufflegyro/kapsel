# Show add channel only on empty home

## Summary

The home page currently shows the add-channel prompt even after the archive has known channels or videos. It should only appear for a new setup with no known archive content.

## Requirements

- Hide the home add-channel area when there are known channels or videos.
- Keep the add-channel area available on a new/empty setup.
- Preserve the existing add-channel flow behavior when the prompt is shown.

## Acceptance Criteria

- Frontend smoke coverage verifies the prompt is hidden on a populated home page.
- Frontend smoke coverage verifies the prompt is shown when there are no known videos or channels.
- Relevant frontend checks pass.

## Notes

- Treat either known videos or known channels as a populated setup.
- Implemented by hiding the home add-channel form unless the loaded home video total is zero and a lightweight channel count check also returns zero.
- Verified with `pnpm check`, `pnpm browser-smoke`, `go test ./meta`, and `git diff --check`.
