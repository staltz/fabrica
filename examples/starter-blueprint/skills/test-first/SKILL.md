---
name: test-first
description: Red, green, refactor. Write a failing test before you change what code does, make it pass with the smallest change, then clean up while the tests stay green. Use it when you add a feature, fix a bug or change behavior. It relies on the product repo's own `test` skill for how to run tests there.
---

# Test first

The loop is short:

1. **Red**: write a test for the behavior you want, and watch it fail.
2. **Green**: write the least code that makes it pass.
3. **Refactor**: tidy up, with the tests still passing.

Then do it again for the next bit of behavior.

Why bother writing the test first? Because a test you watched fail proves two things: that it really tests the behavior, and that your change is what made it pass. A test written after the code often passes by accident, and nobody ever finds out that it checks nothing.

## Step 0: find out how this repo runs its tests

Every repo does this differently, so this skill doesn't say how. Load the repo's own `test` skill. It should tell you the command for the whole suite, how to run a single test, and anything special the tests need, like a database or an environment variable.

If the repo has no `test` skill:

- Look at the README, the CI config, and any `Makefile` or package scripts.
- Tell the user that you had to work it out, and how you ran the tests.
- Suggest they add a `test` skill (for example in `.agents/skills/test/SKILL.md`) so the next agent doesn't have to guess.

Before you change anything, **run the existing tests once**. If some already fail, stop and tell the user. Otherwise you won't be able to tell your failures apart from the ones that were already there.

## Red: write one failing test

- **Pick the smallest step.** One test, one behavior. "Rejects an empty email" is a step. "Validates the signup form" is five steps.
- **Name the test after the behavior**, not the function. `rejects_refund_larger_than_order` tells you what broke when it fails. `test_refund_2` doesn't.
- **Arrange, act, assert.** Set up only what this test needs, do one thing, then check the result.
- **Check outcomes, not internals.** Assert on what a caller would notice: the return value, what got saved, what got sent. Tests that poke at private details break every time you refactor, even when nothing is wrong.
- **For a bug, reproduce the report.** Write the test with the exact input from the bug report. It's your proof the bug is real, and later your proof it's fixed.

Now run it and **watch it fail**. Read the failure message. It should fail because the assertion failed, not because of a typo, an import error or a missing fixture.

- If it fails for the wrong reason, fix the test before you touch the code.
- If it passes, stop. Either the behavior already exists, or the test isn't testing what you think. Find out which before you go on.

## Green: make it pass with the least code

- **Write only what the test needs.** It's fine if it looks a bit naive. The next test will push it further.
- **Don't add what no test asks for.** "While I'm here" code is untested code.
- **Run the test again**, then run the tests around it (the whole file or package) to make sure you didn't break a neighbor.

## Refactor: clean up while it's green

Now that the behavior is pinned down by a test, make the code nicer: clearer names, less duplication, shorter functions.

- Take small steps and run the tests after each one.
- If something goes red, undo the last step instead of debugging a big change.
- Refactor the tests too. Shared setup can move into a helper, as long as each test still reads clearly on its own.
- Don't change behavior during this step. If you want new behavior, that's a new red test.

## Keep the loop short

Each loop should take minutes, not hours. If you've written a lot of code without running a test, the loop got too long. Go back to the last green state and take a smaller step.

## When you can skip the failing test

- **Pure refactors**, where existing tests already cover the behavior. Run them before and after.
- **Docs, comments and config** with no logic in them.
- **Throwaway spikes** to learn how something works. Throw the spike away afterwards, and test-drive the real version.

If you skip the test for any other reason, like "this is too hard to test", say so in your summary and explain why. Never skip it silently.

## When code is hard to test

If writing the test is painful, that tells you something about the code. Usually it's mixing logic with input and output (database, network, files, the clock).

- Pull the logic out into a plain function that takes plain values and returns plain values. Test that.
- Keep the part that does the input and output thin, so there's little left to go wrong there.
- Pass in the clock, random numbers and outside services instead of reaching for them directly, so the test can control them.
- Don't use sleeps to wait for things in tests. They make tests slow and flaky. Wait for the actual event, or control time directly.

## Before you say you're done

- Run the tests that cover what you changed, and say what you ran.
- Report failures honestly, including ones you couldn't fix.
- Don't delete, skip or loosen a failing test to get green. If a test really is wrong, fix it, and say why in the commit message.
