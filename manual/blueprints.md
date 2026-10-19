# Writing a blueprint

A blueprint is a git repo (or a folder inside one) that describes how agents should build software. This page is the full reference for the format. If you want a working example to copy, see [examples/starter-blueprint](../examples/starter-blueprint/), or run `fabrica new my-blueprint` to get your own copy of it (`fabrica new --empty my-blueprint` starts one from scratch).

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
evals/                            # reserved for later; ignored for now, with a warning
```

Everything is YAML: plain `.yaml` files for pure data, and YAML frontmatter on top of Markdown for files with prose (routines, rules, skills).

Fabrica reads its own files **strictly**: unknown keys are errors. That catches typos. If you write `alwaysapply: true` instead of `alwaysApply: true`, a lenient reader would quietly ignore your setting. Fabrica stops and tells you. The one exception is a skill's `SKILL.md`, which is a standard format other tools add their own fields to, so Fabrica only checks the fields it needs and leaves the rest alone.

An item's name comes from its file or folder name: `routines/pr-review.md` is the routine `pr-review`. Names use lowercase letters, digits and single hyphens, like `pr-review` (that's the Agent Skills naming rule, used for every kind of item so there's only one rule to remember).

Files Fabrica doesn't recognize inside these folders (say, a `README.md` in `skills/`) get a warning and are ignored. Anything at the top level of the blueprint, like a `README.md` next to `fabrica.yaml`, is yours to do with as you like.

Run `fabrica check` inside the blueprint to see every problem at once.

## `fabrica.yaml`

```yaml
description: Acme's way of building software      # optional

imports:
  base:                                  # an alias you pick
    git: github.com/acme/base-blueprint
    ref: v1.2
    exclude: [skills/pr-review]          # we ship our own pr-review
    params:
      slack_channel: "#eng"              # a new default for base's routines
  community:
    git: github.com/someone/skills       # a plain skills repo, not a blueprint
    ref: 3f2a1c9
    path: skills
    only: [skills/pdf, skills/changelog]

expects: [test, deploy?]                 # skills product repos should provide
```

All three keys are optional. An empty `fabrica.yaml` is a valid blueprint.

A blueprint has no global name of its own. It's identified by where it lives (git URL and ref), as chosen by whoever uses it.

## Imports

Blueprints can import other blueprints, in whole or in part. They can also import repos that aren't blueprints at all, like a collection of skills.

### Where an import lives

| Key | Required | What it means |
|---|---|---|
| `git` | yes | Where the repo lives. |
| `ref` | yes | A branch, tag or commit. `fabrica.lock` records the exact commit it pointed at. |
| `path` | no | A folder inside the repo. For a blueprint, the folder holding its `fabrica.yaml`. For a plain repo, the folder holding its skills (default `skills`). |
| `only` | no | Take only these items. |
| `exclude` | no | Take everything except these items. |
| `params` | no | New defaults for the params of the routines it brings. See [Setting params on imports](#setting-params-on-imports). |

`git` can be:

- a short `host/owner/repo` like `github.com/acme/base-blueprint`, which Fabrica turns into `https://github.com/acme/base-blueprint.git`,
- a full URL (`https://…`, `ssh://…`, `git@github.com:acme/base-blueprint.git`),
- a local path, handy while you're trying things out.

Fabrica runs your real `git`, so your credential helpers and SSH keys just work. If you'd rather use SSH for the short form, tell git to rewrite it, with no Fabrica setting needed:

```sh
git config --global url."git@github.com:".insteadOf "https://github.com/"
```

### Aliases

Each import gets an alias that *you* choose (`base` and `community` above). Aliases only matter inside your manifest and in messages. Because the alias is yours, forking or mirroring an import never breaks anything: change the URL and keep the alias.

### Names and namespaces

After imports are resolved, a blueprint is one **flat** set of names, with a separate namespace for each kind of item. You refer to items by kind and name, just like their paths: `skills/pr-review`, `routines/pr-review`, `rules/tests-first`. A skill and a routine can both be called `pr-review`.

### Collisions are errors

If two imports both bring `skills/pr-review`, that's an error, and you fix it with `only:` or `exclude:`. Defining an item yourself that an import also brings is an error too (see [Overriding](#overriding) for how to do that on purpose).

### Filtering

Each import can use **either** `only:` (take just these) **or** `exclude:` (take everything except these), not both. Every entry must be an item the import really has, so a typo like `skills/pfd` is an error rather than a silent no-op.

`only:` is literal: it doesn't pull in what the listed items need. If you take `routines/pr-review` but not the `skills/pr-review` it uses, `fabrica check` and `fabrica lock` tell you exactly what to add:

```
✗ routines/pr-review.md (from import base): the routine lists skill "pr-review" in skills:, but this blueprint has no skills/pr-review
  hint: import "base" has it: add skills/pr-review to its only: list (only: doesn't pull in what items need on its own)
```

A broken item inside an import only matters if you use it. If an imported skill has a bad `SKILL.md`, leave it out with `exclude:` and the problem goes away.

### Overriding

To replace an imported item, `exclude:` it and define your own item with the **same name**. Everything that refers to it by name, imported items included, now uses yours. `fabrica upgrade` and `fabrica plan` still tell you when upstream changes something you've overridden, so you can decide whether your version needs the same change.

### Setting params on imports

If you like an imported routine and only want a different number, don't override it: set its params on the import.

```yaml
imports:
  refactor:
    git: github.com/staltz/fabrica-blueprint-refactor
    ref: master
    params:
      split_proceed_threshold: 20
      cruft_proceed_threshold: 3
```

These become the new defaults of those params, in every routine the import brings, including the ones it imports itself. Factories can still set them like any other default. From weakest to strongest, a param's value comes from:

1. the routine's own `default:`
2. the importing blueprint's `imports.<alias>.params` (when imports are nested, the outermost blueprint's wins)
3. the factory's `params:`
4. the target's own `params:`

A few rules:

- Each value is a single value (text, a number or a boolean), like a factory's params.
- Every param has to be declared by a routine the import brings, after `only:` and `exclude:`, so a typo is an error. So is a param of a routine you excluded: if you replaced `routines/split` with your own, set the default in your own file.
- A required param you set gets a default, so factories using your blueprint don't have to set it anymore.
- The routine isn't an override, and `fabrica upgrade` compares upstream's own file, so the defaults you set never show up as a change.

### No renaming

Skills and routines refer to each other by name *in prose* ("run the `test` skill"), and Fabrica can't safely rewrite prose. So you can't rename items on import: it would silently break those references.

### Importing repos that aren't blueprints

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
    git: github.com/acme/base-blueprint
    ref: v1.2
    commit: 9c1e4b2a7d3f…
```

A few rules:

- Locking never moves a pin that's already there. Only `fabrica upgrade` does, and it shows you a diff first.
- If you change an import's `git`, `ref` or `path`, its old pin no longer counts, and `fabrica check` says to run `fabrica lock`. Changing only `only:`, `exclude:` or `params:` doesn't need a new pin.
- An imported blueprint's own imports come from **its** `fabrica.lock`, at the commit you pinned. So your lock alone pins the whole tree.
- `lock` only writes the file when the blueprint has no errors with the new pins.

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

It asks before writing the lock (or pass `--yes`). It refuses to write a lock that would leave the blueprint with errors, like a new collision.

### One blueprint per consumer

Every routine and every session uses exactly one blueprint. To combine blueprints, you write a blueprint that imports them. Conflicts are resolved once, at lock time, never while an agent is running.

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

A routine is **triggers + skills + connectors + side effects + params + a prompt** (and, if you like, the models to run it on), written as abstract intent, independent of any runtime.

```markdown
<!-- routines/pr-review.md -->
---
description: Review every new pull request          # optional
trigger: { on: github.pull_request.opened }
skills: [pr-review]                                 # must exist in this blueprint
connectors: [github, slack?]                        # '?' means optional
side_effects:
  - github.pr_comment
  - slack.notify: { channel: "{{params.slack_channel}}" }
params:
  slack_channel: { required: false, default: "#eng", description: Where to post a summary }
memory: false                                       # optional; true keeps notes between runs
models:                                             # optional; the first one a runtime has wins
  - { model: claude-opus-5-5, effort: high }
  - { model: gpt-5.6-sol, effort: high }
  - default                                         # anywhere else, the runtime's own default
---
Review the PR using the `pr-review` skill, post one comment, then tell {{params.slack_channel}}.
```

| Key | What it is |
|---|---|
| `description` | Optional. A one-line summary. |
| `trigger` | Required, unless there's `triggers:`. `on:` is the event. A `schedule` trigger also needs `cron:` (five fields, UTC). |
| `triggers` | Instead of `trigger:`, a list of them, for a routine that runs on any of several events. See [Several triggers](#several-triggers). |
| `skills` | The blueprint skills the routine uses. They **must exist** in the blueprint. |
| `connectors` | The connectors the routine needs. `name?` marks one as optional. Don't list `fabrica`: it's added automatically. |
| `side_effects` | What the routine may do to the outside world. Each is `connector.action`, optionally with settings. Its connector must be in `connectors:`. |
| `params` | Values factories can set. Each has `required` (default false), `default` and `description`. A required param can't have a default. |
| `memory` | Optional. `true` if the routine keeps notes between runs; default `false`. See [Memory](#memory). |
| `models` | Optional. The models to run the routine on, each with an effort, in order of preference. See [Models](#models). |
| `runtimes` | Only for an [init routine](#init-routines): where to run it, in order of preference. |

The Markdown body is the routine's prompt. It must not be empty.

### Triggers

Triggers are plain names. Which ones work depends on the runtime, and `fabrica plan` tells you. Triggers can't be emulated: if a runtime doesn't have one, that's an error. These are the ones the built-in providers know:

| Trigger | Where |
|---|---|
| `github.pull_request.opened`, `.synchronize`, `.reopened`, …, `github.issues.opened` | Cloud runtimes (see [providers](providers.md) for each one's list) |
| `schedule` (with `cron:`) | Cloud runtimes and Claude Code (desktop app scheduled tasks) |
| `session.start` | Claude Code, Codex CLI and Copilot CLI |
| `prompt.submit` | Claude Code and Codex CLI |
| `session.stop` | Claude Code, Codex CLI, Copilot CLI and Cursor IDE |
| `git.pre_commit`, `git.pre_push` | Claude Code, Codex CLI, Copilot CLI and Cursor IDE |
| `init` | Fired by Fabrica itself, each time `fabrica apply` creates a factory. See [Init routines](#init-routines). |

#### Several triggers

A routine that should run on any of several events lists them under `triggers:` instead of `trigger:`:

```yaml
triggers:
  - { on: github.pull_request.opened }
  - { on: github.pull_request.synchronize }
  - { on: schedule, cron: "0 9 * * 1" }
```

Each event can appear once (so one schedule per routine: write a routine for each schedule), and `init` can't share a routine with other triggers. Every runtime the routine runs on needs every one of its triggers.

How a runtime gets them depends on what it can do:

- **One stub with all of them**, on runtimes whose capability matrix says `multiple_triggers: true`, like `cursor-cloud`: one Cursor automation fires on any of them. Its identity is the routine's, as usual.
- **A stub per trigger** everywhere else: each one is a separate resource in the runtime, with the trigger in its identity, like `4ckr#pr-review:github.pull_request.synchronize@github.com/acme/app` (see [Identities](concepts.md#identities)). Locally, that's a hook for each event and a scheduled task for the schedule.

Either way it's the same routine: one prompt, one memory, one run history. Adding a trigger to a routine with one, or going back to one, renames its stubs, so `plan` shows the old ones as deletes and the new ones as creates.

### Side effects

Each provider maps every side effect to **native** (the runtime does it itself), **emulated** (Fabrica turns it into prompt instructions, plus the connector that carries it out) or **unsupported** (an error). Settings like `channel:` are filled in when the routine is served, with `{{params.x}}` replaced.

A native side effect is one of the runtime's own features, so the routine file is its source of truth: the stub turns it **on** for routines that list it and **off** for every other routine, even where the runtime turns it on by default. That makes native side effects part of the stub's [shape](concepts.md#stubs-and-shapes): adding one to a routine, or removing it, means a re-apply on runtimes that have it, and `plan` shows the stub as an update. (Emulated side effects are only prompt instructions, so changing them takes effect on the next run.) Local hooks run inside the session you already have open, and [init routines](#init-routines) in a one-off session Fabrica starts, so there's no stub to set them up: a hook or init routine that lists a native side effect is an error, even when the routine's schedule trigger alone would be fine.

Some side effects can only be native, because they outlive the run, so no prompt instruction can do them. A runtime has them only if its capability matrix names them exactly: `"*": emulated` doesn't cover them, so on every other runtime they're an error in `check` and `plan`. For now there's one: `github.pr_autofix`.

When an optional connector isn't available on a runtime, the routine still runs there, and its side effects through that connector are skipped. The agent is told so.

Side effects are listed in what the agent sees as "allowed side effects", and it's told not to do anything else to the outside world. Be aware that this is an honor system: the real limit is what each connector's credentials allow.

#### Auto-fixing pull requests

Some routines open pull requests: a weekly dependency update, a fix for a flaky test. Some runtimes can then **auto-fix** them: after the pull request opens, the runtime keeps watching its CI and review comments, and pushes fixes, so the pull request gets to green without anyone running the routine again. Claude calls this *Auto-fix pull requests*. A routine asks for it with the `github.pr_autofix` side effect:

```yaml
connectors: [github]
side_effects:
  - github.open_pr
  - github.pr_autofix
```

- **Claude routines and Claude Code's desktop scheduled tasks** have it natively: it's the **Auto-fix pull requests** toggle, under **Advanced settings** on the **Behavior** tab ("Watch CI and review comments on PRs this scheduled task opens, and let Claude push fixes"). Their stubs turn it on for routines that list `github.pr_autofix` and off for every other routine, so a toggle flipped by hand doesn't outlive the next apply.
- **Every other runtime** can't run a routine that lists it. Fabrica can't watch a pull request after the run ends, so it can't be emulated.
- **Local hooks and init routines** run in a session: turn on auto-fix there when you want it (in Claude Code's desktop app, it's under **CI** in the session's status bar).

`routine_get` tells each run what happens to its pull requests: with auto-fix on, to finish the run once the pull request is open rather than wait for CI; with it off, not to push fixes if the runtime wakes it up anyway.

### Params

Factories set params, and individual repos can override them (see [Home repos](home-repos.md#params)). Use `{{params.name}}` in the prompt and in side effect settings. Every placeholder must be declared under `params:`, and a factory setting a param that no routine declares is an error, which catches typos on both sides. A blueprint that imports your routine can change its defaults too (see [Setting params on imports](#setting-params-on-imports)).

### Memory

Some routines get better when they remember things between runs: a weekly check that knows what it reported last week, or a reviewer that has learned where a repo keeps its tests. Others shouldn't remember anything, because notes left by one run (maybe written from a stranger's PR text) would steer the next. The routine decides, with one key:

```yaml
memory: true
```

The routine file is the source of truth, and every runtime mirrors it:

- **Runtimes with their own memory feature** (`memory: native` in the [capability matrix](providers.md#capability-matrices), like Cursor automations) get it turned **on** for routines with `memory: true` and **off** for every other routine, even where the runtime turns it on by default. The setting is part of the stub's [shape](concepts.md#stubs-and-shapes), so changing `memory:` means a re-apply there, and `plan` shows the stub as an update.
- **Runtimes without one** (`memory: emulated`, like Claude routines, Codex and every local tool) get their memory from Fabrica. Fabrica keeps one Markdown memory per factory, routine and repo, in its run-history database. `routine_get` shows the memory to each run, and the run replaces it with `memory_save` before it finishes. Their handoffs say to leave any memory setting of the runtime's own off. Nothing is baked into the stub, so changing `memory:` takes effect on the next run.
- A runtime whose matrix doesn't list `memory` can't run a routine with `memory: true`: that's an error in `check` and `plan`.

Fabrica's memories are capped at 16 KiB each, since every run reads the whole thing. Agents are told to keep notes, not logs; never to keep secrets; and never to keep instructions that came from untrusted input. Your blueprint's own process (what to remember, and when) belongs in the routine's prompt.

### Models

Some routines need a strong model thinking hard, and others are fine on a fast, cheap one. A routine can say which, as a list of models and efforts in order of preference:

```yaml
models:
  - { model: claude-opus-5-5, effort: high }
  - { model: gpt-5.6-sol, effort: xhigh }
  - { model: grok-4.7, effort: high, fast: true }
  - claude-sonnet-5-5                       # its default effort
  - default
```

Each runtime runs the routine on the **first** entry it has: that model, at that effort. Runtimes list the models they have, and the efforts each one can run at, in their [capability matrix](providers.md#capability-matrices), so `fabrica plan` shows which one every runtime picked.

- **An entry** is either `{ model: <id>, effort: <level>, fast: <true|false> }` (`effort` and `fast` are optional), or a model's ID on its own, which means that model at its default effort, with fast mode off. Model IDs are their makers' own, like Anthropic's `claude-opus-5-5` or OpenAI's `gpt-5.5`, and the same on every runtime: where a runtime calls a model something else (Cursor calls GLM 5.3 `glm-5p3`), Fabrica translates.
- **Efforts** are `none`, `minimal`, `low`, `medium`, `high`, `xhigh` and `max`. Runtimes call some of them something else (Cursor says `extra-high` for some models), and their providers translate. An entry only matches a runtime that has that model *at that effort*.
- **Fast mode** is a faster variant of a model that some runtimes have (Cursor has it for Grok, Composer, Opus and GPT models), usually at a higher price. `fast: true` asks for it, and the entry then only matches runtimes with a fast mode for that model. Without it (or with `fast: false`), fast mode is off, even on runtimes that would run the model fast by default.
- **`default`** matches every runtime, and means the runtime's own default model. It can only come last, since nothing after it would ever be picked.
- If a runtime has none of the entries, that's an error in `check` and `plan`, which says what the runtime has instead. End the list with `default` if the routine can run anywhere.
- With no `models:` at all, every runtime uses its own default.

The model is part of the stub's [shape](concepts.md#stubs-and-shapes) on runtimes that let a stub pick one, so changing it there means a re-apply, and `plan` shows the stub as an update. Local runtimes can't pick one (hooks run inside the session you already have open, on its model), so only `default` matches there. The same goes for Copilot automations, which always run on Copilot's Auto.

### Init routines

Some blueprints need a one-off session before their routines are any use: open an issue in each repo listing what it's missing, create the labels the routines use, write the `test` skill the blueprint [expects](#expects), set up a Slack channel. That's an **init routine**: a routine whose trigger is `init`.

```markdown
<!-- routines/onboard.md -->
---
description: Get a new factory's repos ready for this blueprint
trigger: { on: init }
runtimes: [cursor-cloud, claude-code-local, claude-code-cloud]   # optional; the first one available wins
skills: [repo-setup]
connectors: [github]        # give it a fallback: MCP server, so Claude Code can use it locally
side_effects: [github.issue_open]
models: [{ model: claude-opus-5-5, effort: high }, default]
---
For each of the factory's repos, check that it provides the skills this blueprint expects, and open one issue listing what's missing.
```

Fabrica runs it **when it creates a factory**: `fabrica apply` starts a session for it the first time it applies the factory, right after the factory's stubs, and again when it recreates the factory after a `fabrica destroy`. Not on the factory's other applies, and not for a factory that was created before its blueprint had an init routine (until it's destroyed and recreated). The factory's [state](home-repos.md#state) remembers.

- **It's a routine like any other**, with skills, connectors, side effects, params and models, served live over MCP. Only its trigger is special: no runtime fires `init`, Fabrica does, by starting a session.
- **One per blueprint.** Two init routines (say, one of yours and one an import brings) are an error; leave one out with `exclude:`.
- **No memory.** It runs once, so `memory: true` is an error.
- **It runs for cloud factories**, which keep state to remember it ran. Local factories don't run it.
- **Factories don't list it** under `routines:`; it comes with the blueprint. A factory turns it off with `init: false` (see [Home repos](home-repos.md#init)).

#### Where it runs

`runtimes:` lists where you'd like it to run, in order of preference. Without it, the factory's own runtimes are the list, in their order. Fabrica picks:

1. the **first runtime it can start the session on by itself**: through the runtime's API (Cursor's cloud agents, with a Cursor API key; a folder provider with a `launch` operation), or with a local tool's command line (`claude`, when `fabrica apply` runs on a machine that has Claude Code, and never in CI);
2. otherwise, the **first runtime a person can start it on**, from a handoff. Apply writes the instructions, and `fabrica confirm` records that it's started.

So a list like `[claude-code-local, cursor-cloud, claude-code-cloud]` runs the session on your laptop when you apply from there, as a Cursor cloud agent when CI applies with a Cursor key, and otherwise asks a person to start it in Claude Code on the web. A runtime that can't run the routine at all (it lacks a connector the routine needs, or none of its models) is skipped, and `fabrica plan` says why for each one. If no runtime is left, that's an error.

Each provider says how it can start sessions, in its capability matrix (`launch_modes`, see [Providers](providers.md#init-sessions)). The session is served by the factory's own Fabrica server, wherever it runs, so on a local runtime it can only use [text-only skills](#skill-files-and-relative-paths) too.

The session runs in the factory's **first target**, and its prompt names every target, so it can reach the others through its connectors (a Cursor cloud agent gets them all as its repos).

### Checks

- A routine's `skills:` entry that doesn't exist is an error.
- `fabrica check` warns when the prompt mentions a blueprint skill that's missing from `skills:`.

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

**Secrets never live in blueprints** (or home repos). You reference them by name, and the value comes from each runtime's own secret store, your CI secrets or your environment.

The name `fabrica` is taken: Fabrica's own MCP server is added to every factory automatically.

## Expects

Some blueprint skills need something only the product repo can provide. A `release` skill might need "how *this* repo runs its tests". The blueprint declares these once:

```yaml
expects: [test, deploy?]     # '?' means optional
```

- The product repo provides them as ordinary repo-local skills with matching names (like `.agents/skills/test/SKILL.md`), with no Fabrica syntax at all.
- The blueprint may also ship its own skill with the same name, as a fallback default.
- `fabrica check` in a product repo reports expectations the repo doesn't meet.
- Expected names are the one exception to the skill-clash rule: a repo skill with an expected name is supposed to override the blueprint's default.
- Imported blueprints' `expects:` lists are **not** inherited. Your list is the whole contract your blueprint presents, and Fabrica warns when an import expects something your list doesn't cover.

## Custom providers

A blueprint can ship providers for runtimes Fabrica doesn't support out of the box, in `providers/<name>/`. They're imported like everything else. See [Providers](providers.md#folder-providers). For now, a custom provider can't replace a built-in one.

## `evals/`

Reserved for future skill and routine testing. For now, `fabrica check` warns if the folder exists, and Fabrica ignores it.
