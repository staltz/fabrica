---
description: Every Monday morning, look for outdated or vulnerable dependencies and report them in one GitHub issue
trigger: { on: schedule, cron: "0 9 * * 1" }
connectors: [github]
side_effects:
  - github.open_issue: { label: "{{params.issue_label}}" }
  - github.issue_comment
params:
  issue_label:
    required: false
    default: dependencies
    description: The label of the issue where the weekly reports go
---
It's time for the weekly dependency check. Find the dependencies in this repo that are out of date or have known security problems, and report them in one GitHub issue. Don't change any code: this routine only reports, and a person decides what to update.

1. **Find every dependency list.** Look for package manifests and their lockfiles anywhere in the repo, including subfolders. For example `package.json`, `go.mod`, `requirements.txt`, `pyproject.toml`, `Gemfile`, `Cargo.toml` or `pom.xml`. Check all the ecosystems you find, not only the first one.
2. **Check each dependency** against the version the lockfile actually pins:
   - **Security advisories** for that version. These matter most. Use the GitHub advisory database and the ecosystem's own audit tools where you can.
   - **Major updates.** A new major version usually means breaking changes.
   - **Minor and patch updates.** Usually safe, but still worth a test run.
3. **Don't guess.** Look up the latest versions and advisories; don't go by memory. If you can't check something, say "couldn't check" and why. A wrong "all clear" is worse than an honest gap.
4. **Read the release notes** for every security fix and every major update. Write one or two plain sentences on what changes and how risky it looks for this repo, and link to the advisory or the release notes.
5. **Write the report** with three sections, in this order:
   - **Security**: each advisory, how severe it is, and the version that fixes it.
   - **Major updates**: each one, with your short note on what breaks.
   - **Minor and patch updates**: a table with the name, the current version and the latest version.

   End with suggested next steps, safest and smallest first, like "apply the 6 patch updates in one PR, then look at the security fix for X".
6. **Post it in one place.** Look for an open issue with the label "{{params.issue_label}}" that earlier runs of this routine opened.
   - If there is one, add this week's report to it as a comment.
   - If there isn't, open a new issue titled "Weekly dependency check", with the label "{{params.issue_label}}", and put the report in it.
   - If everything is up to date and there's no open issue, don't open one. Report the run as succeeded and say that everything is current.

Keep it to one issue, so the team has one place to look instead of a new issue every week. Don't open pull requests, push branches or close issues.
