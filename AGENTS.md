# AGENTS.md

Guidance for automated and human contributors working in this repository.

## Project Principles

- Kapsel is a small personal archive application, not a distributed system by default.
- Favor one well-tested binary, one SQLite database, and straightforward filesystem storage.
- Add external services only when a specific issue proves they are necessary.
- Optimize for clarity, debuggability, and predictable resource use.

## Workflow Rules

- Work from `meta/issues.md`; add or update an issue before starting non-trivial work.
- Practice TDD for behavior changes: write a failing test first, implement the smallest change, then refactor if needed.
- Commit small topical changes early and often.
- Keep each commit scoped to one issue or one coherent task.
- Do not batch unrelated cleanup with feature work.
- Follow the review loop for each issue: write the issue, implement the fix, get a review, then commit.
- Prefer deleting complexity over wrapping it in new abstractions.
- **Never test or experiment against a real archive database.** Before any command or change that could delete, truncate, or rewrite archive data (including `rm`, DB edits, or destructive testing), take a backup first with `kapsel backup <path>.zip` and verify the backup file exists and is non-empty. If a command can modify data and there is no verified backup, stop and ask.
- **Never run destructive shell commands with multi-line input or unquoted arguments.** Prefer separate `bash` invocations per destructive step (`rm`, `sqlite3` writes, `mv`) and avoid letting command arguments span lines; a quoting failure can silently delete the wrong path.

## Engineering Rules

- Keep APIs paginated or otherwise bounded from the first implementation.
- Keep background jobs durable, cancellable, and observable.
- Keep media serving efficient, including HTTP range requests.
- Keep SQLite schema changes explicit and migrated.
- Use SQLite transactions for state that must stay consistent.
- Use FTS5 for local search before considering a separate search service.
- Do not introduce Redis, Elasticsearch, or a separate job broker without a documented issue explaining the need.

## Frontend Rules

- Keep the Svelte frontend embedded and deployable with the Go service.
- Avoid eager loading large feature areas when route-level splitting is practical.
- Avoid repeated refetching caused by UI-only state changes.
- Prefer accessible, fast, responsive UI over visual complexity.
- Prefer Svelte 5 rune syntax for new or substantially touched components: `$props`, `$state`, `$derived`, and `$effect` instead of new `export let` or `$:` usage.
- Keep existing legacy Svelte syntax only when a file is already large and migrating it would be a separate risky refactor; document the deferral in the issue.
- Use Svelte 5 event attributes such as `onclick`, `onsubmit`, and `onchange`; do not introduce legacy `on:` event directives in new code.
- Use snippets and `{@render ...}` for new component composition unless an existing legacy pattern makes a smaller change safer.
- Run `pnpm check` from `frontend/` after Svelte component changes, along with the relevant build or browser smoke checks.

## Testing Rules

- Every bug fix should include a regression test when feasible.
- Every new backend behavior should have unit or integration coverage.
- Every schema migration should be covered by a test or a documented manual verification path.
- If a test is not feasible yet, document the gap in the issue before closing it.
