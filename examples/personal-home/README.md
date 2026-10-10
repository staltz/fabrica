# Example personal home repo

Your personal home repo lives at `~/.fabrica` (or wherever `FABRICA_HOME` points). It's the only home repo that can have **local** deployments: the ones for the AI tools on your machine.

```
deployments/
  my-default.yaml   my factory, everywhere, in Claude Code and Cursor
  oss-work.yaml     a different factory for one project, with a local routine
.gitignore          keeps runs.db (local run history) out of git
```

To use it, copy it to `~/.fabrica`, change the factory URLs to yours, and run:

```sh
cd ~/.fabrica && git init
fabrica plan
fabrica apply
```

`apply` adds a user-level `fabrica` MCP server to Claude Code and Cursor, and a hook for `review-before-push`. It never writes into a product repo. You only need to run it again when the installs change, like adding a local tool or changing a local routine's trigger. Which factory serves a repo is worked out fresh each time a session starts, so editing these files needs no apply.

Push this repo somewhere private, and on your next laptop, clone it to `~/.fabrica` and run `fabrica apply` once.
