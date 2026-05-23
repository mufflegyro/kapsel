# Adopt Svelte 5 rune tooling and guidance

## Summary

Bring the frontend closer to current Svelte 5 conventions by adding Svelte-aware checks, migrating small components to runes, and documenting contributor guidance.

## Requirements

- Add a frontend check command for Svelte diagnostics.
- Update Svelte-related frontend tooling where appropriate.
- Migrate simple standalone Svelte components from legacy `export let`/`$:` syntax to Svelte 5 runes.
- Document Svelte 5 guidance in `AGENTS.md` for future contributors.
- Avoid a risky wholesale migration of `App.svelte` unless it stays small and easily verifiable.

## Acceptance Criteria

- `pnpm check` runs successfully in `frontend/`.
- Migrated components build and browser-smoke successfully.
- `AGENTS.md` explains the preferred Svelte 5 style and when legacy syntax is acceptable.
- Any deferred migration work is clearly documented.

## Notes

- `App.svelte` remains on legacy reactivity for now because it is a large stateful router/shell; migrate it as a focused follow-up instead of bundling that risk with tooling setup.
- `RichText.svelte` also remains legacy for now because its measurement effects caused browser-smoke regressions when converted directly; migrate it separately with dedicated coverage.
- Simple leaf components are the safer first migration target for Svelte 5 runes.
