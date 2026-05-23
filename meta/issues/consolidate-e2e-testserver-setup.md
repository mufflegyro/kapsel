# Consolidate e2e testserver setup

## Summary

The browser smoke test server duplicates app wiring and schema-specific seed SQL. This makes e2e tests more likely to drift from production initialization and database shape.

## Requirements

- Share app initialization or fixture setup between e2e and production paths where practical.
- Keep e2e fixtures deterministic and fast.
- Avoid spreading schema details across testserver code when reusable helpers can own them.

## Acceptance Criteria

- E2E fixture setup uses a focused helper or shared test support package.
- Browser smoke tests continue to run with isolated temporary state.
- Schema changes require fewer e2e-specific updates.

## Notes

- Review reference: `internal/e2e/testserver/main.go`.
- This is lower urgency than route/API boundary work but will reduce maintenance cost as tests grow.
