---
name: commit-messages
description: How to write commit messages in the Conventional Commits format, with a short subject that says what changed and a body that says why. Includes the list of types, how to mark breaking changes, and good and bad examples. Use it whenever you write a commit message, squash commits or write a pull request title.
---

# Commit messages

A commit message is a note to the person who reads it a year from now, wondering why this line of code exists. Often that person is you. The diff already shows **what** changed. The message is the only place that can say **why**.

## Check the repo first

Run `git log --oneline -15` before you write your first commit in a repo. If the history clearly uses a different format, match it, and mention that to the user. A history that mixes two formats is worse than either one.

## The format

We use [Conventional Commits](https://www.conventionalcommits.org/):

```text
type(scope): short summary in the imperative

Why this change is needed, in a few plain sentences. What was wrong
or missing, and why this is the right fix. Wrap lines at about 72
characters.

Refs: #123
```

- The **subject** is the first line. It's required.
- The **body** comes after one blank line. Skip it only when the subject says everything, like for a typo fix.
- **Footers** come last, after another blank line.

## Types

| Type | Use it for |
|---|---|
| `feat` | New behavior that users can notice. |
| `fix` | A bug fix. |
| `refactor` | Changing how code is written without changing what it does. |
| `perf` | Making something faster or lighter, without changing what it does. |
| `test` | Adding or fixing tests only. |
| `docs` | Documentation only. |
| `build` | The build system or dependencies. |
| `ci` | CI configuration. |
| `chore` | Housekeeping that fits nowhere else. Use it rarely. |
| `revert` | Undoing an earlier commit. |

If a commit seems to need two types, it's probably two commits.

The **scope** is optional. It's the part of the codebase the change is in, like `feat(billing): ...`. Use the names the repo already uses in its history. Leave it out if the change is spread out, or if there's no clear name.

## The subject line

- **Use the imperative**, as if giving an order: "add", "fix", "remove". Not "added" or "adds". A good test: it should finish the sentence "If applied, this commit will ...".
- **Keep it short.** Aim for 50 characters, and never go past 72. Tools cut off long subjects.
- **Lowercase after the colon, and no period at the end.**
- **Be specific.** "fix bug" and "update code" say nothing. Say which bug, or what changed.

## The body: why, not what

Answer the questions the diff can't:

- What was wrong, or what did someone need?
- Why this approach, and not the obvious other one?
- Anything surprising, like a side effect, a limitation or a follow-up that's still needed.

Don't narrate the diff ("changed X to Y, then renamed Z"). The reader can see that.

## Breaking changes

If the commit breaks something that other code or users rely on, mark it in two places:

- Add `!` after the type: `feat(api)!: remove the v1 endpoints`.
- Add a `BREAKING CHANGE:` footer that says what breaks and what people should do instead.

## Footers

- `Refs: #123` links an issue without closing it.
- `Fixes #123` closes the issue when the commit lands on the default branch. Only use it when the issue is really fully done.
- Keep any trailers your tools add, like `Co-Authored-By:`, as they are.

## Examples

A good fix:

```text
fix(auth): keep users signed in across a daylight saving change

Session expiry was computed in local time, so when the clocks went
back an hour, sessions created in that hour expired immediately and
people were signed out. Store and compare expiry times in UTC instead.

Fixes #482
```

A good feature:

```text
feat(export): let users download reports as CSV

Several teams were copying tables out of the PDF by hand to get the
numbers into spreadsheets. CSV uses the same columns as the on-screen
table, so it matches what people already see.

Refs: #511
```

A good breaking change:

```text
feat(api)!: require an explicit page size for list endpoints

Unbounded list calls were timing out for the largest accounts and
slowing the database for everyone else.

BREAKING CHANGE: list endpoints now return 400 without a `limit`
parameter. Pass `limit` (at most 200) and follow `next` for more.
```

Bad subjects, and better ones:

| Bad | Better | What was wrong |
|---|---|---|
| `fixed stuff` | `fix(cart): stop counting removed items in the total` | Says nothing. |
| `Update UserService.ts` | `refactor(users): split loading and saving into two functions` | Names a file, not a change. |
| `feat: Added the new export feature and also fixed the login bug.` | Two commits: `feat(export): ...` and `fix(auth): ...` | Two changes, past tense, capital letter, period. |
| `wip` | Squash it into a real commit before you push. | Means nothing to anyone else. |

## One change per commit

Each commit should do one thing, and the code should build and pass its tests after each one. That way any commit can be reverted on its own, and `git bisect` can find the commit that broke something. If you need the word "and" in the subject, think about splitting the commit.

## Pull request titles

A PR title follows the same rules as a subject line, because it often becomes the subject of the squashed commit. The PR description is like a long body: why the change is needed, what you tried, how you tested it.
