# Add CSRF protection for state-changing APIs

## Summary

State-changing endpoints rely on the session cookie and `SameSite=Lax` behavior. This provides useful browser protection, but there is no explicit CSRF token, origin validation, non-simple custom header, or JSON content-type enforcement for mutations.

## Requirements

- Add an application-level CSRF defense for unsafe HTTP methods.
- Reject cross-origin unsafe requests before they reach mutation handlers.
- Require mutation requests with JSON bodies to use `Content-Type: application/json`.
- Preserve same-origin SPA behavior and local development usability.
- Keep unauthenticated health/read-only endpoints unaffected.

## Acceptance Criteria

- Unsafe methods such as `POST`, `PUT`, and `DELETE` require a valid same-origin signal, CSRF token, or custom header.
- JSON mutation handlers reject simple form-compatible content types.
- Bodyless mutation endpoints are covered by the same CSRF defense.
- Backend tests cover accepted same-origin mutations and rejected cross-origin or missing-CSRF mutations.
- Frontend API helpers send any required CSRF token or custom header.

## Notes

- Security review severity: Medium.
- Relevant references: `internal/auth/auth.go:105-113`, `internal/server/server.go:248-299`, `internal/server/server.go:397-411`, and `internal/server/job_handlers.go:31-46`.
- `SameSite=Lax` should remain as defense in depth, but should not be the only mutation defense.
