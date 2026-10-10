---
description: Review every new pull request, post the review as a comment, and tell the team on Slack
trigger: { on: github.pull_request.opened }
skills: [pr-review]
connectors: [github, slack?]
side_effects:
  - github.pr_comment
  - slack.notify: { channel: "{{params.slack_channel}}" }
params:
  slack_channel:
    required: false
    default: "#eng"
    description: The Slack channel that hears about each finished review
---
A new pull request was opened. Review it, and post your review as one comment on the PR.

1. **Load the `pr-review` skill**, and read its `checklist.md`, before you look at the code.
2. **Read the PR.** Read the title, the description, any linked issues and the whole diff. Check the CI status too, if it's there yet.
3. **Review it the way the skill says.** Read in the skill's order, label every finding with its severity, and point to exact files and lines. Only report real problems. Don't pad the review to look thorough.
4. **Post the review as a single comment on the PR.** Start with the summary (what the change does, the verdict, the counts), then the findings, blockers first. If you found nothing worth changing, say so in a sentence or two.
5. **Tell the team.** If Slack is available, send one short message to {{params.slack_channel}} with the PR title, its link and the verdict with counts, like "ready after fixes: 1 blocker, 2 should". If Slack isn't available, skip this step.

Some limits, and why:

- **Don't approve, request changes, push commits or merge.** The review is advice. A person decides what happens to the PR.
- **If the PR is a draft**, still review it, but say at the top that it's a draft and that you focused on the approach more than the details.
- **If the diff is too big to review well**, say so, review the riskiest parts, and suggest how to split it.
- **If you can't read the diff** (a permission error, a timeout), don't guess. Report the run as failed and say what went wrong.
