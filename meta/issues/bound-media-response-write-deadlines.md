# Bound media response write deadlines

## Summary

Media serving clears the HTTP server write deadline because legitimate playback can outlive the API timeout. This keeps playback working, but a slow client with a valid signed URL can hold a media response open indefinitely.

## Requirements

- Keep long media playback and range requests working.
- Avoid unbounded write lifetimes for stalled or extremely slow clients.
- Preserve signed URL verification and efficient file serving.

## Acceptance Criteria

- Media responses use a finite deadline, idle timeout, throughput guard, or equivalent bounded streaming policy.
- Tests cover normal range/media serving and timeout behavior where practical.
- The configured behavior is documented if the timeout is tunable.
- Browser playback smoke checks continue to pass.

## Notes

- Security review severity: Medium.
- Relevant reference: `internal/media/media.go:105-108`.
- The fix may use generous media-specific deadlines rather than the API write timeout, as long as stalled connections are eventually released.
