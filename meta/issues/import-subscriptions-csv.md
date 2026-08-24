# Import subscriptions from Google Takeout subscriptions.csv

## Summary

Kapsel can add individual channels via the API/UI, but has no bulk import path. Add a CLI command (and optionally an API job) to import a list of channels from a Google Takeout `subscriptions.csv` file by enqueueing the existing channel-first flow for each channel.

## Requirements

- Parse a Google Takeout `subscriptions.csv` file (header + rows with `Channel Id` and `Channel Url` columns).
- Normalize each channel URL with the existing channel URL normalizer and enqueue a `channel_first_download` job per channel, matching the manual add-channel flow so the channel is marked subscribed for auto downloads.
- Skip empty or invalid rows gracefully; report counts of imported, skipped, and failed rows.
- Reuse the established CLI pattern (`import-ta`) so it runs against the configured data dir/DB and does not require a running server.
- Be bounded and observable: no unbounded file size, and a clear report on completion.

## Acceptance Criteria

- `kapsel import-subscriptions <subscriptions.csv>` parses and enqueues channel jobs for valid rows.
- Unit tests cover CSV parsing (header, quoting, BOM/whitespace), URL normalization (channel/@handle), and skipping malformed rows.
- Duplicate/active channel jobs are suppressed like the manual add-channel flow.
- Import report lists counts and any failures.

## Notes

- Google Takeout `subscriptions.csv` columns: `Channel Id,Channel Url,Channel Title`.
- Reuse `download.EnqueueChannelFirst` and `download.NormalizeChannelURL`.
- Follow-up could expose an authenticated API endpoint (e.g. `POST /api/imports/subscriptions`) after the CLI lands.
