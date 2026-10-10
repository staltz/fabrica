---
description: Before your commits leave your machine, review them like a reviewer would, and hold the push if there's a blocker
trigger: { on: git.pre_push }
skills: [pr-review]
---
You were about to push. Before you do, review the commits that are about to leave this machine, the same way a reviewer would review a pull request. Finding a problem now is much cheaper than finding it after someone else has started reviewing.

1. **Load the `pr-review` skill** and read its `checklist.md`.
2. **Find what's being pushed.** Usually that's `git log --oneline @{upstream}..HEAD` for the commits and `git diff @{upstream}...HEAD` for the changes. If the branch has no upstream yet, compare against the point where it left the default branch, for example `git diff "$(git merge-base HEAD origin/main)"...HEAD`.
3. **Review that diff with the skill.** Also look hard at the things that slip into your own work: leftover debug prints, commented-out code, TODOs you meant to finish, files you didn't mean to add, and anything that looks like a secret.
4. **Run the tests if they're stale.** If the repo provides a `test` skill, and you haven't run the tests since your last change, run the ones that cover this change.
5. **Decide.**
   - **If you found a blocker**, don't push. Tell the user what you found, with the file and line, and offer to fix it. A fix is a new commit, and this review runs again on the next push.
   - **If you found only should, nit or question findings, or nothing**, give the user a short summary (a few lines at most), then run the same push command again.
   - **If the user tells you to push anyway**, do it. It's their call.

This review stays here, in this conversation. Don't post it anywhere else.
