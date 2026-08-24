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

### Data safety guardrails

These rules exist because a destructive shell command deleted the test archive
(68k video records) via a quoting failure. They are mandatory.

1.  **Never test or experiment against a real archive database.** Before any
    command or change that could delete, truncate, or rewrite archive data
    (including `rm`, DB edits, or destructive testing), take a backup first
    with `kapsel backup <path>.zip` and verify the backup file exists and is
    non-empty. If a command can modify data and there is no verified backup,
    stop and ask.

2.  **One destructive operation per `bash` call.** If you need to `rm` files,
    do it in one call. If you need to run `sqlite3` writes, do it in a
    *separate* call. Never mix cleanup and data operations in the same
    invocation. Newline collapse in tool calls can silently merge commands,
    turning `rm` + `sqlite3` into one line where `rm` eats the database path
    as an argument.

3.  **Always use `--` to terminate `rm` arguments.** Write `rm -f -- <path>`
    so that any argument that follows is treated as a path, not a flag.
    Never use `rm -f` without `--` when the arguments could be dynamic.

4.  **Prefer `kapsel` CLI commands over raw `sqlite3` writes.** The app
    provides `backup`, `import-subscriptions`, `import-playlists`, and other
    commands that encapsulate DB mutations. If something isn't exposed,
    create a command rather than running raw SQL. Raw sqlite3 bypasses
    the app's safety layer, schema migrations, and lock enforcement.

5.  **Before any `rm` or `sqlite3` write, verify the target path exists and
    is what you expect.** Use `ls` or `read` to confirm. If the path is
    derived from a variable or command substitution, resolve it first.

6.  **Never run `rm -f` without first verifying the exact arguments** that
    will be expanded (e.g., by running the same command with `echo`).
    `rm -f` is silent on success — it won't report deleting something
    unexpected.

7.  **When you need to do something destructive, stop and plan the exact
    sequence of commands in separate invocations.** Don't batch them into
    one `bash` call. Write a temp script if the sequence is complex.
    
    Violation example (this is what lost the DB):
    ```bash
    # DON'T: rm cleanup + sqlite3 write in one call
    rm -f test-data/kapsel.db.lock test-data/kapsel.pid
    sqlite3 test-data/kapsel.db "UPDATE videos SET ..."
    ```
    After newline collapse this becomes one line where `rm` receives
    `sqlite3`, `test-data/kapsel.db`, and the SQL as file arguments.
    
    Correct:
    ```bash
    # Do cleanup first, in its own call
    rm -f -- test-data/kapsel.db.lock test-data/kapsel.pid
    ```
    Then in a separate call:
    ```bash
    sqlite3 test-data/kapsel.db "UPDATE videos SET ..."
    ```

8.  **Take a backup before every session that involves DB mutation, not just
    before single commands.** The `kapsel backup` command is fast (VACUUM INTO,
    not a copy). A session-level backup means an accidental `rm` or schema
    change loses at most one session of work.

9.  **If you realize you're about to do something risky, stop and ask.**
    There is no penalty for asking. The penalty for not asking is data loss.

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
