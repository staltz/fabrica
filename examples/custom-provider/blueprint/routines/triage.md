---
description: Give every new issue a quick first look
trigger: { on: github.issues.opened }
skills: [triage]
connectors: [github]
side_effects:
  - github.issue_comment
---
A new issue was just opened. Triage it using the `triage` skill, then post one comment on the issue with what you found.
