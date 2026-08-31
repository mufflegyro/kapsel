# Add the ability to send Yummle videos to followed or favourited users

## Summary

Yummle should let a user send a Yummle video to another user who they are following or have in their favourites. The receiving user gets the video delivered to them, even if the video lives in the sender's archive. This is a future-development feature that is intentionally scoped to land **after** both local authentication (`add-local-authentication-and-session-protection.md`) and user management (`add-user-management-and-login-system.md`) are complete, since it depends on real identities and a social graph between them.

## Requirements

- Add a social graph between users: a `follows` relation (who follows whom) and a `favourites` relation (who a user has favourited), manageable from the UI (follow/unfollow, favourite/unfavourite).
- Add a "Send video" action on the video/watch page that lists eligible recipients: users the current user follows **or** has favourited, with a clear way to pick one or more recipients.
- Record each send as a share record (video, sender, recipient, timestamp) so sends are auditable and re-sendable.
- Deliver the send to the recipient: a "Shared with me" / "Sent to you" view that lists videos sent to the logged-in user, along with sender name and date.
- Keep sharing local-first: the sent video remains stored in the sender's archive; the recipient gets access to watch it through the app (signed media URLs scoped to an authenticated session), not a separate copy of the media files.
- A deleted/disabled sender or a removed relationship should make previously sent shares inaccessible to the recipient (mirroring existing session-revocation behaviour), without deleting the sender's media.
- CLI support for inspecting shares (e.g. list shares for a video or a user) alongside the UI flow.

## Acceptance Criteria

- Authenticated users can follow and unfavour/unfavourite other users, and the relationship list updates accordingly.
- The send action is only visible for videos and only offers recipients who are followed or favourited; a video can be sent to multiple recipients at once.
- A recipient sees sent videos in a dedicated "Shared with me" view and can play them while authenticated.
- Disabling a user revokes both their sending access and access to shares they no longer should retain.
- Tests cover follow/favourite CRUD, recipient eligibility, multi-recipient sends, share listing, and access revocation on disable.
- README documents sharing as part of the multi-user workflow.

## Notes

- Depends on: `add-local-authentication-and-session-protection.md` and `add-user-management-and-login-system.md` being scoped and merged; the social graph (follow/favourite) may be extracted into its own prerequisite issue if it grows larger than this feature.
- No media copying: sharing reuses the existing signed, authenticated media-serving path, so per-user watched state/labels (already deferred in the user-management issue) can extend naturally to shared videos later.
- Keep this single-node and local-first, consistent with the rest of the project; no public web, no external delivery channels (email, social media) in scope.
