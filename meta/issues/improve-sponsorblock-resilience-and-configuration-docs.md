# Improve SponsorBlock resilience and configuration docs

## Summary

SponsorBlock segment lookups currently happen inline on video detail cache misses. Transient SponsorBlock failures are swallowed without backoff or negative caching, so an outage can repeatedly delay video detail responses. The default-enabled integration is also missing from the example deployment environment file.

## Requirements

- Keep SponsorBlock sponsor-segment fetching optional and read-only.
- Avoid repeated slow external SponsorBlock calls during transient failures.
- Preserve video detail availability when SponsorBlock is unavailable.
- Document `KAPSEL_SPONSORBLOCK_ENABLED` in the deployment environment example.

## Acceptance Criteria

- A transient SponsorBlock error does not repeatedly trigger immediate external retries for the same video.
- Video detail responses remain successful when SponsorBlock is unavailable.
- Successful SponsorBlock fetches and cached segment reuse continue to work.
- Deployment configuration docs show how to disable SponsorBlock explicitly.

## Notes

- SponsorBlock defaults to enabled because the user requested default-on sponsor skipping.
- The current client timeout is five seconds, so resilience should protect the main video detail path from repeated outage latency.
