---
name: pr-review
description: How to review a pull request, a branch or a local diff. Covers what to read first, what to check, how to label each finding by severity (blocker, should, nit, question, praise), and how to write comments the author can act on. Use it whenever you're asked to review someone's change, and before you push your own.
---

# Reviewing a change

A review has one job: catch problems while they're still cheap to fix, and help the author ship. It's not a chance to rewrite the change the way you would have written it.

The full list of things to look for is in `checklist.md`, next to this file. Read it once before you start. This file explains how to run the review and how to write it up.

## Before you read any code

1. **Find out what the change is for.** Read the PR title, the description and any linked issue. Write down, in one sentence, what the change is supposed to do. If you can't, that's your first finding: ask the author.
2. **Look at the size.** Count the changed lines, leaving out lockfiles, generated files and snapshots. Reviews get noticeably worse past about 400 lines. Past 800, say so in your review, suggest how the change could be split, and review the riskiest parts first.
3. **Check CI.** If the build or tests are failing, say so at the top. Don't spend time on naming while the build is red.

## Read in this order

You pay the most attention at the start, so spend it on what matters most.

1. **The tests.** They tell you what the author thinks the code does. New behavior with no test is a finding by itself.
2. **The core change.** The files where behavior actually changes. Ask: is it correct? What happens with empty input, a missing value, a timeout, a retry, or two requests at the same time?
3. **The edges.** Public APIs, database migrations, config, file formats. Anything other code or other people depend on. These are expensive to change once they ship.
4. **Everything else.** Renames, moved files, formatting.

## Label every finding

Start each finding with one of these labels. It tells the author what they must fix and what they can skip. Without labels, people treat every comment as a blocker, or none of them.

| Label | Meaning | Examples |
|---|---|---|
| **blocker** | Must be fixed before merging. | Bugs, data loss, security holes, a broken public API, new behavior without tests, failing CI. |
| **should** | Worth fixing in this PR, but a good reason not to is fine. | A misleading name, a missing error check on an unlikely path, a test that's hard to follow. |
| **nit** | Small and optional. | A typo, a tiny style point the linter doesn't catch. |
| **question** | You don't understand something yet. Not a request to change anything. | "Is this meant to run for archived accounts too?" |
| **praise** | Something done well. | A clear test, a nice simplification. |

Some rules of thumb:

- **Don't invent problems to look thorough.** A review with no blockers and one nit is a perfectly good review.
- **Keep nits few.** If you have more than five, keep the three that matter most.
- **Praise only when it's true.** It tells people what to keep doing, so it has to be honest.
- **When in doubt between blocker and should, ask yourself:** "Would I be OK if this shipped to users today?" If yes, it's not a blocker.

## Write comments people can act on

A good comment says **where**, **what**, **why** and **what to do**.

- **Point to the exact place.** File and line, like `src/billing/invoice.ts:42`.
- **Say what's wrong and why it matters.** "This can be null" is weak. "This is null when the user has no address, and then line 48 throws" is useful.
- **Suggest a fix.** A small code snippet is best. If you're not sure what the fix is, say that too.
- **Talk about the code, not the person.** "This function does two things" instead of "you made this too complicated".
- **Ask when you're not sure.** A question is better than a confident claim that turns out wrong.
- **One problem per comment.** Then each one can be resolved on its own.
- **Say a repeated problem once.** Then list the other places: "Same thing in `a.ts:10`, `b.ts:22` and `c.ts:31`."

Here's what a good finding looks like:

> **blocker** `src/orders/refund.py:88`: `amount` is compared with `>`, but a refund can be exactly the order total, so full refunds are rejected. Use `>=`, and add a test for the full-refund case.

And a weak one, for contrast:

> Refund logic looks off, please double check.

The weak one makes the author do the reviewing.

## Write the summary

Put a short summary at the top of the review:

1. **What the change does**, in one or two sentences, in your own words. If you got it wrong, the author will notice, and that's useful too.
2. **The verdict**, one of:
   - **ready to merge**: no blockers.
   - **ready after fixes**: there are blockers, but they're small and clear.
   - **needs another pass**: there's a bigger question about the approach, worth talking about before more code gets written.
3. **The counts**, like "1 blocker, 2 should, 1 nit".

Then list the findings, blockers first, then should, then the rest.

## Things to avoid

- **Don't comment on what tools already enforce.** If the formatter or linter would catch it, leave it to them.
- **Don't ask for unrelated refactors.** If you spot one, mention it once at the end as a follow-up idea, never as a blocker.
- **Don't approve, merge or push commits** unless you were asked to. A review is advice. People make the call.
- **Don't review what isn't in the diff**, unless the change breaks it. "While you're here" requests make PRs grow.

## Reviewing your own change

Before you push, review your own diff the same way. Read it as if someone else wrote it. This is where you catch the leftover debug print, the commented-out code, the TODO you meant to finish and the test you forgot to add. If you find a blocker in your own work, fix it before you push.
