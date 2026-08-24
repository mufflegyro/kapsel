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
- Decision: per-user watched state/labels can be a follow-up; this issue covers authentication/identity, not per-user content partitioning.