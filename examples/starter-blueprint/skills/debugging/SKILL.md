---
name: debugging
description: A calm, step-by-step way to find and fix bugs. Reproduce the problem, shrink it, test one hypothesis at a time, bisect when you're lost, then make the smallest fix that addresses the cause, with a test that proves it. Use it when something is broken, a test fails and you don't know why, or code behaves differently from what you expected.
---

# Debugging

Debugging goes wrong in a predictable way: you guess at a fix, it doesn't work, you guess again, and an hour later you've changed six things and don't know which one mattered. The steps below are slower for the first ten minutes and much faster after that.

Keep short notes as you go: what you tried, what happened, what you ruled out. They stop you from going in circles, and they're what you hand over if you get stuck.

## 1. Reproduce it

You can't fix what you can't see fail.

- Write down the exact steps, the input, what you expected and what actually happened.
- Get it to fail **on demand**. A bug that shows up "sometimes" isn't reproduced yet.
- Once you can, turn it into a failing automated test (the `test-first` skill explains how). Then you have a fast way to check every idea, and proof when it's fixed.

If you can't reproduce it, don't guess-fix. Collect more facts: logs, the exact version that's running, the data involved, the environment, the time it happened. Ask the user for them if you need to.

## 2. Read the whole error

It sounds obvious, and it's still the most skipped step.

- Read the full message and stack trace, not only the last line.
- Find the first frame that's in this repo's code. That's usually where to start.
- If there are many errors, the **first** one is usually the real one. The rest are often knock-on effects.
- Search the codebase for the exact error text. It tells you where it's raised.

## 3. Make it smaller

The smaller the failing case, the fewer places the bug can hide.

- Cut the input down: fewer records, a shorter file, a simpler request.
- Remove steps one at a time. When the bug disappears, the last thing you removed is involved.
- Aim for the smallest case that still fails. Often, by the time you get there, the cause is obvious.

## 4. One hypothesis at a time

Now think, then test the thinking.

1. **Write the hypothesis down**: "I think X happens because Y. If that's true, then when I do W, I'll see Z."
2. **Pick the cheapest experiment** that could prove it wrong: a log line, a breakpoint, a quick check in a REPL, a smaller test.
3. **Change one thing at a time.** If you change two things and the bug goes away, you don't know which one fixed it.
4. **Write down the result**, whether it confirmed the idea or ruled it out. Ruling things out is progress.

Question your assumptions, especially the ones that feel too obvious to check:

- Is the code you're reading the code that's actually running? Check the branch, rebuild, clear caches, restart the process.
- Is the config, data or environment what you think it is? Print it (leaving out secrets) instead of assuming.
- Did it ever really work? Check before you go looking for what changed.

## 5. Bisect when you're lost

If it used to work and now it doesn't, let git find the commit that broke it:

```bash
git bisect start
git bisect bad                 # the current commit is broken
git bisect good <commit>       # a commit you know worked
# git checks out a commit in the middle; test it, then say:
git bisect good                # or: git bisect bad
# repeat until git names the first bad commit, then:
git bisect reset
```

If you have a command that exits non-zero when the bug is there (like one failing test), git can do the whole search for you with `git bisect run <command>`.

The same halving idea works without git: comment out half the code path, or half the input, and see which half the bug lives in. Then halve again.

## 6. Fix the cause, not the symptom

When you find where it goes wrong, ask "why?" until you reach the real cause. "The value is null" is a symptom. "We read the setting before the config file is loaded" is a cause.

- **Make the smallest fix** that addresses the cause.
- **Don't clean up unrelated code in the same change.** It makes the fix harder to review and harder to revert.
- **Keep the test** that reproduces the bug. It stops the bug from coming back.

Fixes that usually hide a bug instead of fixing it:

- Adding a retry or a sleep.
- Catching an exception and ignoring it.
- Adding a null check without knowing why the value was null.
- Deleting or loosening the failing test.

Sometimes one of these really is the right answer. If so, explain why in the commit message.

## 7. Finish properly

- Run the full set of tests that cover the area, not only the new one.
- Remove debug prints, temporary logs and commented-out experiments.
- Write a commit message that explains the cause, not only the fix (see the `commit-messages` skill). The next person to debug this area will thank you.
- If the same mistake probably exists elsewhere, mention where. Fix those too only if they're small and clearly the same bug; otherwise suggest a follow-up.

## When you're stuck

- **Explain the problem from scratch**, in writing, as if to someone who knows nothing about it. Halfway through, you often spot the gap.
- **Go back to your notes** and check what you've actually proven versus what you've assumed.
- **Set a limit.** If three or four hypotheses in a row have failed, or about half an hour has gone by without progress, stop. Tell the user what you know: how to reproduce it, what you've ruled out, and your best remaining guesses. That's a useful result, not a failure.
