# Remember cinema mode preference

## Summary

Persist cinema mode so users who prefer the wide watch layout get it automatically on later videos.

## Requirements

- Store cinema mode preference in frontend storage.
- Restore cinema mode on watch-page video loads.
- Keep non-playable catalog-only pages from getting stuck in a cinema layout with no player control.

## Acceptance Criteria

- Browser smoke coverage proves cinema mode remains active across a later video page visit.
- Browser smoke coverage continues to prove cinema mode can be toggled off.

## Notes

- This is a frontend preference and does not need backend persistence.
