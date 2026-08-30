# Add user management and a managed login system

## Summary

Kapsel currently supports single-user local auth (`KAPSEL_AUTH_USERNAME` + password hash). Add proper user management so a household or small team can have multiple accounts with managed credentials, plus a login system that supports them.

## Requirements

- Add a `users` table with unique usernames and per-user password hashes.
- Support creating, listing, renaming, and disabling/removing users via CLI (and optionally API for an admin).
- Keep the existing single-user env-based auth working as a first-run bootstrap path (e.g. the first admin user creates further accounts from the UI/CLI).
- Login sessions should remain scoped per user; sessions invalid when a user is disabled or deleted.
- Protect user-management endpoints behind an admin-only role.
- Keep password hashing consistent with the existing `kapsel hash-password` (argon2id) so credentials are portable.

## Acceptance Criteria

- A CLI command creates an initial admin user with a password.
- Additional users can be created and disabled; disabled users cannot log in and existing sessions are revoked.
- Multiple users can each have index state while sharing the same archive on disk.
- Tests cover user CRUD, password hashing, login for each user, and disabling a user's session.
- Readme documents the bootstrap flow and how it differs from the single-user env auth.

## Notes

- Existing single-user flow: `KAPSEL_AUTH_MODE`, `KAPSEL_AUTH_USERNAME`, `KAPSEL_AUTH_PASSWORD_HASH` in `internal/config`, managed by `internal/auth`.
- **Update approvals must be role-gated when this lands.** The self-update feature (`POST /api/updates/{id}/approve|dismiss`, `internal/server/server.go`) is currently gated only by `requireAuth` — the auth package has a single user and no roles, so "any authenticated user" == "archive admin" today. Once multiple accounts exist, an approve action replaces the server binary and restarts the process; leaving it available to every logged-in household member would quietly turn the "admin-approved update" guarantee into "any logged-in user replaces the binary". The multi-user work must add an admin role check on the update endpoints (and treat the recorded `approved_by` as a real accountable identity, not just a label).
- Decision: per-user watched state/labels can be a follow-up; this issue covers authentication/identity, not per-user content partitioning.
- **Directional note (preferred architecture):** Yummle is an offline archive, so the user system should be shaped like a **social graph**, not a big managed multi-account archive. Separate Yummle archives run by separate users are the real "users" of the system: each archive is a node that can follow, trust, or interact with other archives, and anyone in theory can run their own Yummle server and share its content or picks with any other server. In-instance multi-user is a **genuine core requirement** (household members sharing one archive); the archive-to-archive social graph is an added layer on top of it, not a replacement for in-instance accounts. Where the schema or API choices have a cost to making this work cross-server (stable user/instance identity, follow/favourite relations, share records), prefer the shape that generalizes to archive-to-archive interaction over the shape optimized for many accounts on one box (see `add-video-sharing-to-followed-or-favoured-users.md`).
- **Scope clarifications (brainstorm):** (a) Multi-user within an archive is still a genuine requirement for household members — it is not merely a bootstrap path. (b) Sharing between archives is **metadata only** (picks, titles, descriptions, recommendations) — the actual media is not pulled or pushed across servers. Whether an archive pulls the content of a shared/seen video is a decision its owner makes locally (e.g. download it from the original source or skip it), so cross-archive sharing never moves bytes of media by default.