# Writing a factory

A factory is a git repo (or a folder inside one) that describes how agents should build software. This page is the full reference for the format. If you want a working example to copy, see [examples/starter-factory](../examples/starter-factory/), or run `fabrica new my-factory` to get your own copy of it (`fabrica new --empty my-factory` starts one from scratch).

## Layout

The folder names are fixed. You can't rename them.

```
fabrica.yaml                      # the manifest
fabrica.lock                      # written by `fabrica lock`; don't edit it by hand
skills/<name>/SKILL.md            # standard Agent Skills, with no Fabrica extras
routines/<name>.md                # one file per routine
rules/<name>.mdc                  # one file per rule, in Cursor's format
connectors/<name>.yaml            # one file per connector
providers/<name>/provider.yaml    # optional custom providers (plus api and handoff.md)
evals/                            # reserved for later; v0.1 ignores it and warns
```

Everything is YAML: plain `.yaml` files for pure data, and YAML frontmatter on top of Markdown for files with prose (routines, rules, skills).

Fabrica reads its own files **strictly**: unknown keys are errors. That catches typos. If you write `alwaysapply: true` instead of `alwaysApply: true`, a lenient reader would quietly ignore your setting. Fabrica stops and tells you. The one exception is a skill's `SKILL.md`, which is a standard format other tools add their own fields to, so Fabrica only checks the fields it needs and leaves the rest alone.

An item's name comes from its file or folder name: `routines/pr-review.md` is the routine `pr-review`. Names use lowercase letters, digits and single hyphens, like `pr-review` (that's the Agent Skills naming rule, used for every kind of item so there's only one rule to remember).

Files Fabrica doesn't recognize inside these folders (say, a `README.md` in `skills/`) get a warning and are ignored. Anything at the top level of the factory, like a `README.md` next to `fabrica.yaml`, is yours to do with as you like.

Run `fabrica check` inside the factory to see every problem at once.

## `fabrica.yaml`

```yaml
description: Acme's way of building software      # optional

imports:
  base:                                  # an alias you pick
    git: github.com/acme/base-factory
    ref: v1.2
    exclude: [skills/pr-review]          # we ship our own pr-review
  community:
    git: github.com/someone/skills       # a plain skills repo, not a factory
    ref: 3f2a1c9
    path: skills
    only: [skills/pdf, skills/changelog]

expects: [test, deploy?]                 # skills product repos should provide
```

All three keys are optional. An empty `fabrica.yaml` is a valid factory.

A factory has no global name of its own. It's identified by where it lives (git URL and ref), as chosen by whoever uses it.

## Imports

Factories can import other factories, in whole or in part. They can also import repos that aren't factories at all, like a collection of skills.

### Where an import lives

| Key | Required | What it means |
|---|---|---|
| `git` | yes | Where the repo lives. |
| `ref` | yes | A branch, tag or commit. `fabrica.lock` records the exact commit it pointed at. |
| `path` | no | A folder inside the repo. For a factory, the folder holding its `fabrica.yaml`. For a plain repo, the folder holding its skills (default `skills`). |
| `only` | no | Take only these items. |
| `exclude` | no | Take everything except these items. |

`git` can be:

- a short `host/owner/repo` like `github.com/acme/base-factory`, which Fabrica turns into `https://github.com/acme/base-factory.git`,
- a full URL (`https://…`, `ssh://…`, `git@github.com:acme/base-factory.git`),
- a local path, handy while you're trying things out.

Fabrica runs your real `git`, so your credential helpers and SSH keys just work. If you'd rather use SSH for the short form, tell git to rewrite it, with no Fabrica setting needed:

```sh
git config --global url."git@github.com:".insteadOf "https://github.com/"
```

### Aliases

Each import gets an alias that *you* choose (`base` and `community` above). Aliases only matter inside your manifest and in messages. Because the alias is yours, forking or mirroring an import never breaks anything: change the URL and keep the alias.

### Names and namespaces

After imports are resolved, a factory is one **flat** set of names, with a separate namespace for each kind of item. You refer to items by kind and name, just like their paths: `skills/pr-review`, `routines/pr-review`, `rules/tests-first`. A skill and a routine can both be called `pr-review`.

### Collisions are errors

If two imports both bring `skills/pr-review`, that's an error, and you fix it with `only:` or `exclude:`. Defining an item yourself that an import also brings is an error too (see [Overriding](#overriding) for how to do that on purpose).

### Filtering

Each import can use **either** `only:` (take just these) **or** `exclude:` (take everything except these), not both. Every entry must be an item the import really has, so a typo like `skills/pfd` is an error rather than a silent no-op.

`only:` is literal: it doesn't pull in what the listed items need. If you take `routines/pr-review` but not the `skills/pr-review` it uses, `fabrica check` and `fabrica lock` tell you exactly what to add:

```
✗ routines/pr-review.md (from import base): the routine lists skill "pr-review" in skills:, but this factory has no skills/pr-review
  hint: import "base" has it: add skills/pr-review to its only: list (only: doesn't pull in what items need on its own)
```

A broken item inside an import only matters if you use it. If an imported skill has a bad `SKILL.md`, leave it out with `exclude:` and the problem goes away.

### Overriding

To replace an imported item, `exclude:` it and define your own item with the **same name**. Everything that refers to it by name, imported items included, now uses yours. `fabrica upgrade` and `fabrica plan` still tell you when upstream changes something you've overridden, so you can decide whether your version needs the same change.

### No renaming

Skills and routines refer to each other by name *in prose* ("run the `test` skill"), and Fabrica can't safely rewrite prose. So you can't rename items on import: it would silently break those references.

### Importing repos that aren't factories

Any repo without a `fabrica.yaml` can still be imported:

- Its skills are found at `<path>/*/SKILL.md` (default `path: skills`).
- Its Cursor rules, `.cursor/rules/*.mdc` at the repo root, come in unchanged.
- A root `AGENTS.md` comes in as one always-on rule, called `rules/agents-md`.

That makes the whole existing ecosystem of skill and rule collections usable, without asking anyone to change their repo. If an import finds nothing at all, that's an error, since it usually means `git:` or `path:` is wrong.

### Pinning with `fabrica.lock`

`fabrica lock` resolves each import's ref and writes the exact commit:

```yaml
# Written by `fabrica lock`. Don't edit by hand.
# It pins every import to an exact commit; `fabrica upgrade` moves the pins.
imports:
  base:
    git: github.com/acme/base-factory
    ref: v1.2
    commit: 9c1e4b2a7d3f…
```

A few rules:

- Locking never moves a pin that's already there. Only `fabrica upgrade` does, and it shows you a diff first.
- If you change an import's `git`, `ref` or `path`, its old pin no longer counts, and `fabrica check` says to run `fabrica lock`. Changing only `only:` or `exclude:` doesn't need a new pin.
- An imported factory's own imports come from **its** `fabrica.lock`, at the commit you pinned. So your lock alone pins the whole tree.
- `lock` only writes the file when the factory has no errors with the new pins.

### Upgrading

```sh
fabrica upgrade            # every import
fabrica upgrade base       # just one
fabrica upgrade --dry-run  # only show what would change
```

`upgrade` moves imports to the newest commit of their ref and shows what changed in each:

```
base (main): 9c1e4b2 → 1f0a3d7
  ~ providers/acme-runner   (provider code can run with your runtime credentials during apply; review it)
  - rules/old-rule
  ~ skills/pr-review   (you override this; check whether your version needs the same change)
  + skills/release
```

It asks before writing the lock (or pass `--yes`). It refuses to write a lock that would leave the factory with errors, like a new collision.

### One factory per consumer

Every routine and every session uses exactly one factory. To combine factories, you write a factory that imports them. Conflicts are resolved once, at lock time, never while an agent is running.

## Skills

A skill is a standard [Agent Skills](https://agentskills.io/specification) folder with a `SKILL.md`, **completely unchanged**. Fabrica adds no fields to it, so any skill from anywhere works, and Fabrica's skills work in tools that know nothing about Fabrica.

```markdown
<!-- skills/pr-review/SKILL.md -->
---
name: pr-review
description: How we review pull requests. Use it when reviewing someone else's changes.
---
Read the diff first, then the tests. See checklist.md for what to look for.
```

Fabrica checks that:

- `name` matches the folder's name (the spec requires it),
- `description` is there and at most 1024 characters (agents use it to decide when the skill is relevant),
- the folder has no symlinks (a skill has to work wherever its folder is copied, exactly as it is in git).

### Skill files and relative paths

A skill is a whole folder, not just its `SKILL.md`. Skills refer to their own files by relative path ("run `scripts/extract.py`"), and some files are binary. The rule is: **relative paths in a skill resolve against the skill's folder.**

- **Locally**, Fabrica unpacks the folder into a read-only cache, like `~/.cache/fabrica/skills/<hash>/pr-review/`, keeping executable bits, and tells the agent where it is. Every local runtime can use every skill.
- **In the cloud**, the agent reads files one at a time over MCP. That only works for **text-only** skills: every file is Markdown, plain text or data (`.md`, `.txt`, `.json`, `.yaml`, `.yml`, `.csv`) and none is executable. A cloud routine that lists any other skill in `skills:` is an error in `check` and `plan`.

### Mentions

Skills often mention other skills in their text (`/changelog`, "use the `test` skill"). You don't have to declare those. Instead, `fabrica check` scans skill text and **warns** when a skill mentions one that's in one of your imports but that you didn't bring in.

## Routines

A routine is **trigger + skills + connectors + side effects + params + a prompt**, written as abstract intent, independent of any runtime.

```markdown
<!-- routines/pr-review.md -->
---
description: Review every new pull request          # optional
trigger: { on: github.pull_request.opened }
skills: [pr-review]                                 # must exist in this factory
connectors: [github, slack?]                        # '?' means optional
side_effects:
  - github.pr_comment
  - slack.notify: { channel: "{{params.slack_channel}}" }
params:
  slack_channel: { required: false, default: "#eng", description: Where to post a summary }
memory: false                                       # optional; true keeps notes between runs
---
Review the PR using the `pr-review` skill, post one comment, then tell {{params.slack_channel}}.
```

| Key | What it is |
|---|---|
| `description` | Optional. A one-line summary. |
| `trigger` | Required. `on:` is the event. A `schedule` trigger also needs `cron:` (five fields, UTC). |
| `skills` | The factory skills the routine uses. They **must exist** in the factory. |
| `connectors` | The connectors the routine needs. `name?` marks one as optional. Don't list `fabrica`: it's added automatically. |
| `side_effects` | What the routine may do to the outside world. Each is `connector.action`, optionally with settings. Its connector must be in `connectors:`. |
| `params` | Values deployments can set. Each has `required` (default false), `default` and `description`. A required param can't have a default. |
| `memory` | Optional. `true` if the routine keeps notes between runs; default `false`. See [Memory](#memory). |

The Markdown body is the routine's prompt. It must not be empty.

### Triggers

Triggers are plain names. Which ones work depends on the runtime, and `fabrica plan` tells you. Triggers can't be emulated: if a runtime doesn't have one, that's an error. These are the ones the built-in providers know:

| Trigger | Where |
|---|---|
| `github.pull_request.opened`, `.synchronize`, `.reopened`, … | Cloud runtimes (see [providers](providers.md) for each one's list) |
| `schedule` (with `cron:`) | Cloud runtimes and Claude Code (desktop app scheduled tasks) |
| `session.start` | Claude Code and Codex CLI |
| `prompt.submit` | Claude Code and Codex CLI |
| `session.stop` | Claude Code, Codex CLI and Cursor IDE |
| `git.pre_commit`, `git.pre_push` | Claude Code, Codex CLI and Cursor IDE |

### Side effects

Each provider maps every side effect to **native** (the runtime does it itself), **emulated** (Fabrica turns it into prompt instructions, plus the connector that carries it out) or **unsupported** (an error). Settings like `channel:` are filled in when the routine is served, with `{{params.x}}` replaced.

When an optional connector isn't available on a runtime, the routine still runs there, and its side effects through that connector are skipped. The agent is told so.

Side effects are listed in what the agent sees as "allowed side effects", and it's told not to do anything else to the outside world. Be aware that this is an honor system: the real limit is what each connector's credentials allow.

### Params

Deployments set params, and individual repos can override them (see [Home repos](home-repos.md#params)). Use `{{params.name}}` in the prompt and in side effect settings. Every placeholder must be declared under `params:`, and a deployment setting a param that no routine declares is an error, which catches typos on both sides.

### Memory

Some routines get better when they remember things between runs: a weekly check that knows what it reported last week, or a reviewer that has learned where a repo keeps its tests. Others shouldn't remember anything, because notes left by one run (maybe written from a stranger's PR text) would steer the next. The routine decides, with one key:

```yaml
memory: true
```

The routine file is the source of truth, and every runtime mirrors it:

- **Runtimes with their own memory feature** (`memory: native` in the [capability matrix](providers.md#capability-matrices), like Cursor automations) get it turned **on** for routines with `memory: true` and **off** for every other routine, even where the runtime turns it on by default. The setting is part of the stub's [shape](concepts.md#stubs-and-shapes), so changing `memory:` means a re-apply there, and `plan` shows the stub as an update.
- **Runtimes without one** (`memory: emulated`, like Claude routines, Codex and every local tool) get their memory from Fabrica. Fabrica keeps one Markdown memory per deployment, routine and repo, in its run-history database. `routine_get` shows the memory to each run, and the run replaces it with `memory_save` before it finishes. Their handoffs say to leave any memory setting of the runtime's own off. Nothing is baked into the stub, so changing `memory:` takes effect on the next run.
- A runtime whose matrix doesn't list `memory` can't run a routine with `memory: true`: that's an error in `check` and `plan`.

Fabrica's memories are capped at 16 KiB each, since every run reads the whole thing. Agents are told to keep notes, not logs; never to keep secrets; and never to keep instructions that came from untrusted input. Your factory's own process (what to remember, and when) belongs in the routine's prompt.

### Checks

- A routine's `skills:` entry that doesn't exist is an error.
- `fabrica check` warns when the prompt mentions a factory skill that's missing from `skills:`.

## Rules

Rules are **always-on instructions**: the process half of what people usually put in `AGENTS.md`. They follow [Cursor's rule format](https://cursor.com/docs/context/rules):

```markdown
<!-- rules/tests-first.mdc -->
---
description: Test-first workflow
globs:
alwaysApply: true
---
Write a failing test before changing behavior.
```

Rules activate the way Cursor rules do:

| Frontmatter | When the rule applies |
|---|---|
| `alwaysApply: true` | Always. `rules_get` returns it in full. |
| `globs` | When the agent works on matching files. |
| `description` only | When the agent decides it's relevant. |
| None of the above | Only when someone asks for it by name. |

Only those three keys are allowed. `globs` can be a list, or Cursor's comma-separated form (`globs: *.ts, *.tsx`), which Fabrica reads even though it isn't valid YAML. A rule file must use the `.mdc` extension; a `.md` file in `rules/` is an error, since it's almost certainly meant to be a rule.

## Connectors

One idea covers both built-in integrations and MCP servers.

```yaml
# connectors/github.yaml
kind: native          # the runtime's built-in GitHub integration
description: GitHub, for reading PRs and posting reviews
fallback:             # optional: an MCP server for runtimes without a built-in one
  url: https://api.githubcopilot.com/mcp/
  auth: bearer
  secret: GITHUB_TOKEN
```

```yaml
# connectors/linear.yaml
kind: mcp
url: https://mcp.linear.app/mcp
transport: http       # http (the default for a url) or sse
auth: oauth           # none (the default), oauth or bearer
```

```yaml
# connectors/local-tool.yaml
kind: mcp
command: my-mcp-server
args: [--quiet]
env: [MY_TOOL_TOKEN]  # environment variables passed through, by name
```

| Key | For | What it is |
|---|---|---|
| `kind` | all | `native` or `mcp`. |
| `description` | all | Optional. |
| `fallback` | `native` | An MCP server (same keys as below) used on runtimes that don't have the built-in integration. The capability matrix marks the connector as *emulated* there. |
| `url` | `mcp` | An `https://` URL (plain `http` only for localhost). |
| `transport` | `mcp` | `http`, `sse` or `stdio`. Defaults to `http` with a `url` and `stdio` with a `command`. |
| `auth` | `mcp` | `none`, `oauth` or `bearer`. |
| `secret` | `mcp` | With `auth: bearer`, the **name** of the secret holding the token, like `LINEAR_API_KEY`. |
| `command`, `args`, `env` | `mcp` | A local (stdio) server. `env` lists environment variable names to pass through. |

**Secrets never live in factories** (or home repos). You reference them by name, and the value comes from each runtime's own secret store, your CI secrets or your environment.

The name `fabrica` is taken: Fabrica's own MCP server is added to every deployment automatically.

## Expects

Some factory skills need something only the product repo can provide. A `release` skill might need "how *this* repo runs its tests". The factory declares these once:

```yaml
expects: [test, deploy?]     # '?' means optional
```

- The product repo provides them as ordinary repo-local skills with matching names (like `.agents/skills/test/SKILL.md`), with no Fabrica syntax at all.
- The factory may also ship its own skill with the same name, as a fallback default.
- `fabrica check` in a product repo reports expectations the repo doesn't meet.
- Expected names are the one exception to the skill-clash rule: a repo skill with an expected name is supposed to override the factory's default.
- Imported factories' `expects:` lists are **not** inherited. Your list is the whole contract your factory presents, and Fabrica warns when an import expects something your list doesn't cover.

## Custom providers

A factory can ship providers for runtimes Fabrica doesn't support out of the box, in `providers/<name>/`. They're imported like everything else. See [Providers](providers.md#folder-providers). In v0.1, a custom provider can't replace a built-in one.

## `evals/`

Reserved for future skill and routine testing. In v0.1, `fabrica check` warns if the folder exists, and Fabrica ignores it.
