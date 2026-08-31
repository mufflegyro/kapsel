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
- **Directional note:** Yummle is an offline, per-person archive. The identity model should evolve into a **social graph between independent Yummle archives**, not into a big centralized archive with many accounts — each archive is its own node, run by its own user, and separate archives can follow or interact with each other based on trust (see `add-user-management-and-login-system.md` and `add-video-sharing-to-followed-or-favoured-users.md`). In theory anyone can run their own Yummle server and share content or picks with any other server. So while this issue stays single-user/local, the identity it establishes should be stable and portable (stable user/instance id, no reliance on env-config credentials as the only identity) so it can later address peers across servers.
