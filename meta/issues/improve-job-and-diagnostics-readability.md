# Improve job and diagnostics readability

## Summary

Make dense operational surfaces easier to scan while preserving Kapsel's debuggability for failed jobs, readiness checks, and diagnostics.

## Requirements

- Summarize long job errors before showing raw command output.
- Preserve full error details in an accessible, copyable form for debugging.
- Avoid letting raw logs dominate the downloads page by default.
- Abbreviate long job IDs where useful while keeping the full ID available for copy or inspection.
- Keep settings diagnostics copyable, but reduce the default visual weight of raw JSON.
- Add a compact readiness summary to settings so users can understand node health at a glance.
- Ensure any destructive storage maintenance action has clear danger styling and confirmation.

## Acceptance Criteria

- Failed jobs show a short error summary plus access to full raw logs.
- Full logs remain copyable and readable in a bounded scroll area or details panel.
- Job cards no longer require full UUIDs to dominate the primary card heading.
- Settings shows a high-level readiness summary before individual checks.
- Raw diagnostics JSON can be hidden/collapsed without removing one-click copy support.
- Browser coverage or documented manual verification covers a failed job and settings diagnostics state.

## Notes

- Do not over-collapse errors: failed jobs are operationally important and should remain discoverable.
- Syntax highlighting for diagnostics JSON is optional and likely lower value than preserving copyable raw text.
- The review screenshots showed ffmpeg output overwhelming the downloads page.
