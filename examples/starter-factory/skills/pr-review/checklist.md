# Review checklist

Go through this while you review. You don't need to mention every item in your review. It's here so the important things don't get forgotten.

Each section starts with the question that matters most.

## Does it do what it says?

- The change matches the PR description and the linked issue.
- Nothing unrelated slipped in (a drive-by refactor, a stray file, a version bump).
- If the description and the code disagree, ask which one is right.

## Is it correct?

- Empty, missing and null values are handled: empty lists, empty strings, missing keys, zero.
- Boundaries are right: off-by-one, `<` versus `<=`, first and last items, time zones, end of month.
- Errors are handled where they can actually happen, and not silently swallowed.
- Anything that runs twice, or at the same time as itself, still works. Think of retries, double clicks and two workers picking up the same job.
- Resources get cleaned up: files closed, connections released, timers cancelled.

## Is it tested?

- New behavior has a test, and bug fixes have a test that would have caught the bug.
- The tests check outcomes (what's returned, saved or sent), not private details.
- The tests would actually fail if the code were wrong. A test that can't fail is worse than no test, because it looks like coverage.
- No tests were deleted, skipped or loosened without a reason in the description.
- Tests don't depend on the real clock, randomness, the network or the order they run in.

## Is it safe?

- No secrets in code, tests, fixtures, config or logs.
- User input is validated, and never put straight into SQL, shell commands, file paths or HTML.
- Permission checks are on the server side, and on every path that reaches the data, not only the main one.
- Logs and error messages don't leak personal data or tokens.
- New dependencies are needed, maintained and from a source you'd trust.

## Will it hurt to change later?

- Public APIs, file formats, events and database schemas are changed on purpose, and stay backward compatible or are clearly marked as breaking.
- Database migrations can run on a live system without locking big tables for long, and there's a way back if they go wrong.
- Config and feature flags have sensible defaults.
- If the change affects how the software is deployed (new environment variables, migrations, infrastructure), check it against the repo's own `deploy` skill if it has one, and make sure the description says what has to happen at deploy time.

## Will it be fast enough?

- No database query or network call inside a loop that could have been one call.
- Nothing loads a whole table or file into memory when it could stream or page.
- Only flag performance when the code is on a hot path or the data can get big. Don't guess about microseconds.

## Can the next person understand it?

- Names say what things are. A reader shouldn't need the PR description to understand the code.
- Comments explain why, not what. Tricky code has a comment; obvious code doesn't need one.
- No dead code, commented-out code or debug prints left behind.
- Functions do one thing. If you need "and" to describe one, it may want splitting.

## Are the docs and history up to date?

- User-facing changes have a line in the changelog (see the `changelog` skill).
- READMEs and docs that describe the changed behavior are updated in the same PR.
- Commit messages explain why the change was made (see the `commit-messages` skill).
