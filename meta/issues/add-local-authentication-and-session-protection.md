# Add local authentication and session protection

## Summary

Protect the web UI, API routes, and signed media URL generation with a simple local authentication model suitable for one user or a small household.

## Requirements

- Add a login flow that does not require external identity providers.
- Store password credentials securely using a modern password hash.
- Protect API routes that mutate archive state or expose private archive metadata.
- Keep media URLs signed and scoped to authenticated users where practical.
- Provide a development-safe mode that is explicit and documented.

## Acceptance Criteria

- Tests cover login success, login failure, session expiry, and protected route rejection.
- Mutating archive endpoints reject unauthenticated requests by default.
- The frontend displays a usable login state and logout action.
- README documents how to configure the first account and session secret.

## Notes

- Keep this single-node and local-first; do not add OAuth or a separate auth service.
