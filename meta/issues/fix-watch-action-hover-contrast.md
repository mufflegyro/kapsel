# Fix watch action hover contrast

## Summary

Watch page action buttons can become hard to read on hover because the shared action row hover color is combined with a later catalog button hover background.

## Requirements

- Keep the "Mark as played" and "Keep forever" button text readable when hovered.
- Preserve existing primary download button contrast.
- Check nearby shared button hover styles for the same dark-text-on-dark-background pattern.

## Acceptance Criteria

- Watch action hover states use contrasting foreground and background colors.
- The frontend check and build pass after the CSS change.
- Embedded frontend assets are rebuilt.

## Notes

- The issue is caused by `.action-row button:hover` setting `color: var(--bg)` before `.catalog-download:hover:not(:disabled)` changes the hover background to a translucent accent surface.
