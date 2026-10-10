# Fabrica — Product Requirements

**Status:** draft, pre-v0.1. Last updated 2026-10-10.

Where files live, what format they use and their overall shape are **decided**. A few field names inside files may still change during implementation. Anything not decided yet is listed in [Later](#later).

---

## TL;DR

Fabrica lets you write down *how your AI agents build software* (skills, routines, rules) **once**, in a git repo, and run that same setup on many AI coding tools: Claude, Cursor, Codex and more.

Agents always fetch your setup live from a small Fabrica server, so improving your process is just a git merge. Every agent run is recorded, so you can see what happened.

---

## The problem

Teams using AI coding agents end up with their "how we work" knowledge scattered all over the place:

- The same skills get copied into every repo's `.claude/skills/` folder.
- `AGENTS.md` files mix "how this app works" with "how we write code in general".
- A PR-review bot is set up by hand in three different tools, and the three slowly drift apart.
- Nobody knows what the agents actually did last week.

When you improve your process, you have to update it everywhere. When you switch tools, you start over.

## What Fabrica does

Fabrica treats your agent setup as its own versioned thing, called a **factory**, kept separate from the software it builds. Think of it as "higher-order version control": your product repo versions your code, and your factory repo versions *how that code gets built*.

The workflow:

1. **Write a factory** (skills, routines, rules, connectors) in a git repo.
2. **Say which factory runs where** (which product repos, which tools) in your **home repo**.
3. **Run `fabrica apply`.**

From then on, agents in those tools load your factory live over MCP, and every run is recorded.

## Who it's for

- **Solo developers** who want their personal way of working available in every repo and every tool.
- **Team leads and platform teams** who want one standard process (PR review, releases, testing discipline) across many repos and tools.
- **Factory authors** who publish reusable factories or skill collections for others to import.

## Goals

- Write once, run on many agent tools.
- Process changes take effect by merging to git, with no redeploy.
- Product repos stay about the product.
- No hosted service. Everything is self-hosted, and git is where people collaborate.
- When something is wrong or ambiguous, fail loudly instead of quietly doing the wrong thing.

## Non-goals (for now)

- A Fabrica cloud service. There will never be an official hosted one.
- Fabrica doing computer use itself. When a tool has no API, Fabrica writes instructions that a human or a computer-use agent can follow.
- Wrapping git. Fabrica sits *beside* git.
- Plugins, bots, evals and dependency graphs. See [Later](#later).

---

## Principles

These explain *why* the rules below are the way they are.

1. **Process lives in factories. Product knowledge lives in product repos.** "Write the test first" belongs to a factory. "This app uses pnpm and Postgres" belongs to the product repo.
2. **Product repos are factory-agnostic but Fabrica-aware.** A product repo never says *which* factory works on it, and any number of people or teams can point different factories at the same repo. The only Fabrica-specific thing in a product repo is one fixed sentence at the top of its `AGENTS.md`.
3. **Universal, not tied to any one tool.** Factories describe *what* should happen. Providers translate that for each specific tool.
4. **Forward-looking.** When the ecosystem is missing something (like skills over MCP), Fabrica uses the simplest substitute that works and switches to the real thing once it exists.
5. **Minimal.** Few concepts, few commands, no hosted service. Git holds everything people write and review. The only thing stored outside git is run history.
6. **Fail closed.** If there's a conflict or something required is missing, Fabrica stops and says so as loudly as it can. It never guesses.
7. **Start strict, loosen later.** Loosening a rule later never breaks anyone, but tightening one does. So when in doubt, v0.1 starts strict.

---

## Glossary

| Term | What it means |
|---|---|
| **Factory** | A git repo (or a folder in one) that describes how agents should build software: skills, routines, rules, connectors. |
| **Product repo** / **workspace** | The repo with your actual product code, which agents work on. |
| **Home repo** | Your (or your team's) git repo that says which factory runs on which product repos and which tools. |
| **Runtime** | A tool that runs agents. Cloud runtimes: Claude routines, Cursor automations, Codex. Local runtimes: Claude Code, Cursor IDE, Codex CLI. |
| **Skill** | A standard Agent Skills folder (`SKILL.md` + files). The agent loads it when it's relevant. |
| **Routine** | Something an agent does automatically when a trigger fires, e.g. "review every new PR". |
| **Rule** | An instruction that's always active (or active for certain files), in Cursor's rule format. |
| **Connector** | An integration the agent needs: a built-in one like GitHub, or an MCP server. |
| **Provider** | The piece that translates Fabrica's concepts for one specific runtime. |
| **Deployment** | A file in the home repo: "run *this* factory's routines on *these* repos in *these* runtimes". |
| **Stub** | The tiny placeholder Fabrica installs in a runtime instead of the real routine. It just says "load your instructions from Fabrica". |
| **Run** | One unit of agent work, either a routine firing or an interactive session in your editor. Recorded in run history. |
| **Expects** | Skills a factory hopes the *product repo* will provide, like "how this repo runs its tests". |

---

## The three kinds of repository

| Repo | Contains | Knows about |
|---|---|---|
| **Factory repo** | Skills, routines, rules, connectors, providers, imports | Nothing downstream. It doesn't know who uses it. |
| **Home repo** (personal or team) | Deployments, deployment state | Which factories run on which product repos, in which runtimes |
| **Product repo** | Code, repo-specific skills, product-knowledge rules, and the Fabrica line in `AGENTS.md` | Nothing about factories |

The home repo is the only place where factories get connected to product repos. It's an ordinary git repo (e.g. `~/.fabrica`, pushed to a remote), so it's versioned, durable and works across machines. A **team home repo** is the same thing, owned by an organization.

---

## Part 1: The factory repo

### Layout

Directory names are fixed. You can't rename them.

```
fabrica.yaml                      # the manifest
fabrica.lock                      # written by `fabrica lock`, don't edit by hand
skills/<name>/SKILL.md            # standard Agent Skills, no Fabrica extras
routines/<name>.md                # one file per routine
rules/<name>.mdc                  # one file per rule, Cursor format
connectors/<name>.yaml            # one file per connector
providers/<name>/provider.yaml    # optional custom providers (+ api, handoff.md)
evals/                            # reserved for the future; ignored in v0.1
```

Everything is **YAML**: plain `.yaml` files for pure data, and YAML frontmatter on top of Markdown for files with prose (routines, rules, skills). Fabrica reads YAML strictly. Unknown keys are errors, which catches typos.

An item's name comes from its file or folder name. `routines/pr-review.md` is the routine `pr-review`.

A factory usually sits at the root of its repo, but it can also live in a subfolder (see `path:` under [Imports](#imports)).

### `fabrica.yaml`

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

expects: [test, deploy?]                 # see "Expects" below
```

A factory has no global name of its own. It's identified by where it lives (git URL + ref), as chosen by whoever uses it.

### Imports

Factories can import other factories, in whole or in part.

**Aliases.** Each import gets an alias that *you* choose (`base`, `community` above). Aliases only matter inside your manifest and in error messages. Because the alias is yours, forking or mirroring an import never breaks anything: you change the URL and keep the alias.

**Names and namespaces.** After imports are resolved, a factory is one **flat** set of names, with a separate namespace for each kind of item. You refer to items by kind and name, just like their paths: `skills/pr-review`, `routines/pr-review`, `rules/tests-first`. A skill and a routine can both be called `pr-review`.

**Collisions are errors.** If two imports both bring `skills/pr-review`, `fabrica lock` fails, and you fix it with `only:` or `exclude:`.

**Filtering.** Each import can use **either** `only:` (take just these) **or** `exclude:` (take everything except these), not both. `only:` is literal: it doesn't automatically pull in what the listed items depend on. If something's missing, `lock` tells you exactly what to add.

**Overriding.** To replace an imported item, `exclude:` it and define your own item with the **same name**. Everything else that refers to it by name, imported items included, now uses yours. `upgrade` and `plan` still tell you when upstream changed something you've overridden.

**No renaming.** Skills and routines refer to each other by name *in prose* ("run the `test` skill"), and Fabrica can't safely rewrite prose. So renaming on import isn't allowed: it would silently break those references.

**Importing repos that aren't factories.** Any repo without a `fabrica.yaml` can still be imported:
- Its skills are found at `<path>/*/SKILL.md` (default `path: skills`).
- Its rule files can also be imported: `.cursor/rules/*.mdc` come in unchanged, and a root `AGENTS.md` comes in as one always-on rule called `rules/agents-md`.

This makes the whole existing ecosystem of skill and rule collections usable without asking anyone to change their repo.

**Pinning.** `fabrica.lock` records the exact commit for every import:

```yaml
imports:
  base:
    git: github.com/acme/base-factory
    ref: v1.2
    commit: 9c1e4b2…
```

Upstream changes only arrive when you run `fabrica upgrade`, which shows you a diff first.

**One factory per consumer.** Every routine and every session uses exactly one factory. To combine factories, you write a factory that imports them. Conflicts are resolved once, at lock time, never while an agent is running.

### Skills

A skill is a standard [Agent Skills](https://agentskills.io/specification) folder with a `SKILL.md`, **completely unchanged**. Fabrica adds no fields to it, so any skill from anywhere works, and Fabrica's skills work in tools that know nothing about Fabrica.

Skills often mention other skills in their text (`/changelog`, "use the `test` skill"). Fabrica doesn't require skills to declare these. Instead, `check` scans skill text and **warns** when a mentioned name is a skill in one of your imports that you forgot to bring in.

### Routines

A routine is **trigger + skills + connectors + side effects + params + a prompt**, written as abstract intent, independent of any runtime:

```markdown
<!-- routines/pr-review.md -->
---
trigger: { on: github.pull_request.opened }
skills: [pr-review]                   # must exist in this factory
connectors: [github, slack?]          # '?' = optional
side_effects:
  - github.pr_comment
  - slack.notify: { channel: "{{params.slack_channel}}" }
params:
  slack_channel: { required: false }
---
Review the PR using the `pr-review` skill...
```

- **`skills:`** lists the factory skills the routine uses. They **must exist** in the factory, or `lock`/`check` fails.
- `check` also **warns** if the prompt mentions a factory skill that's missing from `skills:`.
- Each provider maps every trigger, connector and side effect to **native** (the runtime has it), **emulated** (turned into prompt instructions plus the required connector) or **unsupported** (an error in `check`/`plan`). Triggers can't be emulated.

### Rules

Rules are **always-on instructions**, the process half of what people usually put in `AGENTS.md`. They follow [Cursor's rule format](https://cursor.com/docs/context/rules) as closely as possible:

```markdown
<!-- rules/tests-first.mdc -->
---
description: Test-first workflow
globs:
alwaysApply: true
---
Write a failing test before changing behavior. ...
```

They activate the way Cursor rules do:
- `alwaysApply: true`: always active.
- `globs`: active when the agent touches matching files.
- `description` only: the agent decides when the rule is relevant.
- None of the above: only when asked for by name.

### Connectors

One concept covers both built-in integrations and MCP servers:

```yaml
# connectors/github.yaml
kind: native          # maps to the runtime's built-in GitHub integration
```

```yaml
# connectors/linear.yaml
kind: mcp
url: https://mcp.linear.app/mcp
transport: http
auth: oauth           # or bearer, etc. Secrets are referenced by name, never stored here.
```

Providers map connectors to a runtime's native integration where one exists, and otherwise register the MCP server in the runtime's config. Fabrica's own MCP server is added to every deployment automatically.

### Expects

Some factory skills need something only the product repo can provide. A `release` skill might need "how *this* repo runs its tests". The factory declares these once, in `fabrica.yaml`:

```yaml
expects: [test, deploy?]     # '?' = optional
```

- The product repo provides them as ordinary repo-local skills with matching names, using no Fabrica syntax at all.
- The factory may also ship its own skill with the same name as a fallback default.
- `fabrica check <product-repo>` reports any expectations the repo doesn't meet.
- Imported factories' `expects:` lists are **not** inherited. Your list is the whole contract your factory presents, and `lock` warns if an import expects something your list doesn't cover.

### Custom providers

A factory can ship providers for runtimes Fabrica doesn't support out of the box. See [Part 7](#part-7-providers-and-apply-modes). In v0.1, a custom provider can't replace a built-in one.

### `evals/`

Reserved for future skill and routine testing. In v0.1, `check` warns if the folder exists and Fabrica ignores it.

---

## Part 2: The product repo (workspace)

A product repo stays about the product. It may contain:

- Its code.
- **Repo-specific skills** (in the usual places: `.claude/skills/`, `.agents/skills/`, …), including ones that satisfy a factory's `expects:`.
- **Product-knowledge rules** in `AGENTS.md`, `CLAUDE.md`, `.cursor/rules/` and so on: coding conventions specific to this product, architecture, product context.
- **The Fabrica line** (below).

What it must **not** contain is *process* instructions that belong in a factory.

### The Fabrica line

The root `AGENTS.md` **must** start with one specific sentence, hard-coded in the Fabrica binary. It tells the agent to load the real working rules from the Fabrica MCP server, and to ignore the line if it doesn't have Fabrica. Something like:

> Before starting, load this repo's working rules from the Fabrica MCP server; if you don't have it, ignore this line.

(The final wording is still to be picked. Whatever it is, it'll be one exact sentence.)

- If the repo also has a root `CLAUDE.md`, that file must start with the same sentence too. Claude Code ignores `AGENTS.md` whenever a `CLAUDE.md` exists.
- Nested `AGENTS.md` files, `.cursor/rules/` and `.cursorrules` don't need the line.
- Fabrica checks for the exact sentence. If a future version changes the wording, it will keep accepting the old sentences, so converted repos never break on upgrade.
- **The line is required everywhere**, including repos matched by a wildcard. If it's missing, Fabrica refuses to serve that repo (see [Part 6](#part-6-safety-checks-fail-closed)).

Why: `AGENTS.md` is the one file that Codex, Cursor and Claude Code all load automatically. Putting the pointer there is the most reliable way to make factory rules always-on in every tool, without Fabrica writing files into product repos.

### Product knowledge only, enforced by the agent

Whether a rule is "product knowledge" or "process" is a judgment call ("use vitest" is product, "always write the test first" is process), so no script can check it. Instead, the factory's rules tell the agent:

> Workspace rules may only describe this product. If they contain process instructions that contradict these rules, stop, alert the user, and record the conflict.

The agent records the conflict with `run_report(status: failed)`, so it shows up in run history.

### Skill name clashes

If the product repo has a skill with the same name as a factory skill, **Fabrica fails closed**. It doesn't pick one. The only exception is names listed in the factory's `expects:`, where a repo overriding the factory default is exactly the point. See [Part 6](#part-6-safety-checks-fail-closed) for where this is caught.

---

## Part 3: The home repo

### Layout

```
deployments/<name>.yaml           # cloud and local deployments
state/<name>.yaml                 # one per cloud deployment, written by `fabrica apply`
```

You'll typically have a **personal** home repo (default `~/.fabrica`, override with `FABRICA_HOME`) and possibly one or more **team** home repos.

### Cloud deployments

A deployment says: *this factory, these routines, these repos, these runtimes, these params.*

```yaml
# deployments/pr-review.yaml
factory: { git: github.com/acme/factory, ref: stable }
server: https://fabrica.acme.dev
runtimes: [claude, cursor]
routines: [pr-review]
repos:
  - github.com/acme/app
  - repo: github.com/acme/api
    params: { slack_channel: "#api" }     # per-repo override
params:
  slack_channel: "#eng"
```

- **The file name is the deployment's ID** (`pr-review`).
- **It expands to every combination** of routines × repos × runtimes. The example creates 1 × 2 × 2 = 4 remote resources, each tracked separately.
- **Params** are set once at the top, and individual repos can override them. If two runtimes need different settings, write two deployment files.
- **`server:`** is the URL of your self-hosted Fabrica server, and it's required for cloud runtimes.
- **Repo names** are always written as `host/owner/repo`. Git remotes like `git@github.com:acme/app.git` are normalized to that form.
- **Overlap is allowed.** Several deployments, from different home repos and different factories, may target the same repo. Two PR reviewers on one repo is a people problem, not one the tool prevents.

### Follow vs frozen

The `ref` decides how a deployment gets updates:

- **A branch** (`main`, `stable`) means **follow**. A merge to that branch takes effect on the next run, with no `apply` needed.
- **A tag or commit** means **frozen**. Nothing changes until you edit the deployment.

`plan` shows which mode each deployment is in. Using branches as release channels (`stable` for production, `main` for canaries) works naturally: promoting a change is a git merge, reviewed in the factory repo.

### Local deployments

A **local deployment** is the same kind of file, with local runtimes:

```yaml
# deployments/my-default.yaml
factory: { git: github.com/me/factory, ref: main }
runtimes: [claude-code, cursor-ide]
repos: ["*"]                     # every repo not matched by another local deployment
```

```yaml
# deployments/oss-work.yaml
factory: { git: github.com/me/oss-factory, ref: main }
runtimes: [claude-code, cursor-ide]
repos: [github.com/oss/project]
routines: [review-before-push]   # optional local routines
```

What happens:

- **`fabrica apply` on your laptop** installs one user-level `fabrica` MCP entry in each local tool (Claude Code, Cursor, …), plus any local routines (e.g. as tool hooks). It never writes into a product repo.
- **Choosing a factory needs no `apply`.** Each time a session starts, `fabrica mcp` reads your local deployments, matches the workspace's git remote, and serves that factory. Change a local deployment and the next session picks it up.
- **New machine?** Clone your home repo and run `fabrica apply` once.
- **The wildcard** `repos: ["*"]` (quoted, because a bare `*` means something else in YAML) is only allowed for local runtimes. A specific repo always beats the wildcard. At most one wildcard deployment per runtime.
- **Two specific local deployments for the same repo and runtime is an error.** A session must get exactly one factory.
- **If no local deployment matches**, `fabrica mcp` serves nothing and says why.
- **Local deployments only live in your personal home repo.** Putting one in a team home repo is a `check` error. Team home repos are cloud-only in v0.1.
- **CI ignores local runtimes,** since there's nothing local for it to set up.

### State

**Cloud deployments** each have one state file, `state/<deployment>.yaml`. Only Fabrica writes it, and it's committed to the home repo. It records every remote resource the deployment owns, what was installed in each one, any pending handoffs, and the deployment's lock. Each deployment has its own file, so people applying *different* deployments never conflict.

```yaml
# state/pr-review.yaml (written by Fabrica, don't edit by hand)
lock:                                    # only present while an apply is running
  holder: ci github.com/acme/home/actions/runs/4812
  since: 2026-10-10T12:00:00Z
resources:
  - id: fabrica.acme.dev/pr-review#pr-review@github.com/acme/app
    runtime: claude
    remote_id: trig_01Hx…
    factory_commit: 9c1e4b2…
    shape: { trigger: github.pull_request.opened, connectors: [github, slack], fingerprint: 7f3a9c }
pending: []                              # handoffs waiting for `fabrica confirm`
```

**Resource identity.** Every remote resource has a stable identity built from its endpoint, routine and repo, e.g. `fabrica.acme.dev/pr-review#pr-review@github.com/acme/app`. Nothing random goes into it. An endpoint belongs to exactly one home repo and deployment (see [The remote server](#the-remote-server)), so two home repos never claim each other's resources. Providers write the identity into the resource. It goes in a name or metadata field when the runtime has one, and it's always in the stub prompt. It works like the identifying mark on local installs.

**Changes and errors.** `plan` compares what the home repo and its factories ask for with what's installed, and sorts everything it finds into two kinds:

- **Changes** are work for `apply`: resources to create, update or delete, and handoffs to issue. This includes repairing stubs that went stale after a merge. When a routine's trigger or connectors change to ones the runtime supports, its installed stub is an **update**. A stub whose routine, repo or runtime is no longer in its deployment is a **delete** (see [Stale stubs](#stale-stubs)).
- **Errors** are problems `apply` can't fix: anything marked ❌ for `plan` in [Part 6](#part-6-safety-checks-fail-closed), such as a trigger the runtime doesn't support, a problem in a product repo, duplicate identities or a held lock. `apply` won't start while the plan has any. An error in one repo blocks the whole deployment, so to unblock the others, fix that repo or remove it from the deployment.

`plan` exits with `0` when there's nothing to do, `2` when there are only changes, and `1` when there are errors. A scheduled `plan` in the home repo's CI fails on both, so stale stubs get noticed as well as errors. It's deliberately `plan` and not `apply`: a new trigger or connector is exactly what someone should review before it's installed (see [Risks](#risks-and-trade-offs-weve-accepted)).

**How `apply` changes a cloud deployment:**

1. **Plan** against the latest home repo. If it finds errors, list them and stop. If nothing needs to change, stop without committing.
2. **Take the lock.** Commit a `lock:` entry to the state file and push. Git only accepts a push on top of the latest commit, so when two applies race, exactly one push gets through. The other pulls, sees the lock, and stops with a message naming who holds it. Nothing remote has been touched yet, so losing the race is always safe.
3. **Refresh.** Look up all of this deployment's resources in each runtime **by identity**. Any resource that exists but is missing from state gets adopted, e.g. one created by an apply that crashed before recording it.
4. **Re-plan and apply.** If the re-plan finds errors (e.g. duplicates the refresh turned up), remove the lock and stop without touching any resource. Every create first looks the resource up by identity and only creates it if it's missing, so a retry never makes a duplicate.
5. **Record and release.** Write the results and remove the lock in one commit, then push. If the push is rejected because of unrelated commits (e.g. another deployment's state), rebase and push again. While the lock is held, nobody else writes this file.

- **A crashed apply leaves its lock in place.** Fabrica can't tell a crashed apply from a slow one, so it doesn't guess. `plan` and `apply` show who holds the lock and since when, and `fabrica unlock <deployment>` removes it. The next apply's refresh adopts whatever the crashed one created.
- **`fabrica confirm` takes the same lock,** since it also writes the state file.
- **Providers must be able to find their resources.** `api` mode is only available for a runtime if its API can list resources and read their identity back. Otherwise the provider uses `handoff`. Pending handoffs are recorded in state, so a second apply never issues the same handoff twice.
- **Duplicates are errors.** If the refresh finds two resources with the same identity (e.g. someone copied one by hand), `plan` fails and names both. Fabrica doesn't pick one to delete.
- **Who applies:** CI on the home repo is recommended, with the CI's own concurrency setting making applies wait in line instead of racing. Individuals can apply too, and the lock keeps that safe.

**Local deployments have no state file.** Everything Fabrica installs locally carries an identifying mark (the MCP entry is named `fabrica`, hooks call `fabrica …`), so Fabrica finds its own installs just by reading the tool's config.
- **Secrets** never live in factory or home repos. They come from the OS keychain, CI secrets or each runtime's own auth.

---

## Part 4: How it runs

### Stubs

Fabrica never installs a routine's real content into a runtime. It installs a tiny **stub**: the trigger, the connectors, and a short prompt along the lines of:

> You are routine `pr-review` (`fabrica.acme.dev/pr-review#pr-review@github.com/acme/app`). Call `run_begin` with routine `pr-review`, runtime `claude`, repo `github.com/acme/app` and stub `7f3a9c`, then load the routine with `routine_get` from the Fabrica MCP server and follow it.

Fabrica writes a stub for each routine × repo × runtime, so it can include exact context values like these.

Why stubs:
- Editing a routine's prompt, skills or rules takes effect on the next run, with no re-apply (for deployments that follow a branch).
- Re-applying is only needed when a routine's **shape** changes, which is rare. That makes tools with weak or missing APIs bearable.

### Stub shape

A stub's **shape** is everything it bakes into the runtime: the trigger, plus the connectors and their definitions (including connectors that emulated side effects need). Prompts, skills, rules, params and side effects are served live, so they aren't part of the shape.

`apply` records each stub's shape in state and writes a short fingerprint of it into the stub (`stub 7f3a9c` above). Local routines carry the fingerprint the same way, e.g. in their hook command.

### Stale stubs

A stub stays installed until `apply` changes or deletes it, but the home repo and the factory can change at any time. So before a routine run starts, `run_begin` checks that its stub is still current. Both servers check two things, in this order:

1. **The stub is still deployed.** In the latest home repo, the deployment being served (remotely, the endpoint's; locally, the one matching the workspace) must still include the stub's routine, repo and runtime. Removing any of them from the deployment, or deleting the deployment file, stops the stub as soon as the server has fetched the change. That holds even while `apply` hasn't deleted the stub yet, or the deletion waits on a handoff. The fingerprint can't catch this: if a deployment covers `app` and `api` and you remove `app`, `app`'s stub still has the same trigger and connectors, and the deployment's token still works.
2. **The stub's shape still matches.** The server works out the routine's shape at the factory commit it's about to serve, and compares its fingerprint with the stub's. This catches a changed trigger or connector in the factory, and deployment edits that were merged but not applied yet, such as a new `ref:`.

**If either check fails, the server refuses.** `run_begin` returns an error saying which check failed and that the deployment needs `fabrica apply`. The run is recorded as `failed`, and `routine_get` won't serve that routine in that run. A stub never runs a routine that's no longer deployed for its repo, expects a different trigger, or needs a connector the stub doesn't have.

- Both checks compare the latest home repo with the values inside the stub, never with state. So there's no window while an apply is in progress, after one crashes, or while a handoff is pending.
- On the remote server, routines only run from stubs. A `run_begin` that names a routine but leaves out its runtime, repo or fingerprint is refused too.
- Repairing a stale stub is `apply`'s job, so `plan` lists it as a change, never as an error: an outdated stub is an **update**, and one that's no longer deployed is a **delete** (see [State](#state)). The scheduled `plan` in the home repo's CI exits nonzero until they're applied.

v0.1 refuses outdated stubs instead of serving the last compatible version. That's the stricter choice, and switching to holding back later wouldn't break anyone (see [Later](#later)).

### The Fabrica MCP server

MCP is the **only** way factory content reaches agents. There's no copying files into repos and no git-fetch fallback. One binary, two modes:

| | `fabrica mcp` (local) | `fabrica mcp --http` (remote) |
|---|---|---|
| Used by | Claude Code, Cursor IDE, Codex CLI on your machine | Cloud runtimes (Claude routines, Cursor automations, Codex) |
| Transport | stdio | HTTP |
| Picks the factory from | Your personal home repo's local deployments | One home repo's cloud deployments |
| Run history in | `~/.fabrica/runs.db` | The server's own SQLite database |

Each MCP session is served from **one factory commit**, fixed when the session starts. A merge that lands mid-run never changes what that run sees, and `factory_sha` in run history is exact.

### MCP tools

| Tool | What it does |
|---|---|
| `skills_list()` | Names and descriptions of all skills. Remotely, skills this runtime can't use are marked as unavailable |
| `skill_load(name)` | A skill's `SKILL.md` and its file list. Locally, also the folder on disk where the skill was unpacked |
| `skill_file(name, path)` | One text file from a skill |
| `rules_get()` | All always-on rules in full, plus an index of the other rules (name, description, globs) |
| `rule_load(name)` | One rule |
| `routine_get(name)` | A routine's full definition, with the rules included |
| `run_begin(intent, routine?, runtime?, repo?, stub?)` | Starts a run and returns a `run_id`. Refuses routine runs whose stub is outdated or no longer deployed (see [Stale stubs](#stale-stubs)) |
| `run_report(run_id, summary, status?, links?)` | Records a checkpoint for a run |

Until runtimes support skills over MCP natively, skills are **emulated** with the plain tools above. The server's MCP `instructions`, the stub and the Fabrica line all tell the agent to list and load what's relevant, and how to resolve a skill's relative paths (below). When native skills-over-MCP arrives, the emulation goes away.

### Skill files

A skill is a whole folder, not just its `SKILL.md`. Skills refer to their own files by relative path ("run `scripts/extract.py`"), scripts import sibling modules, and some files are binary (templates, images, fonts). Fabrica's contract: **relative paths in a skill resolve against the skill's folder**, and a skill only works where that whole folder exists exactly as it is in git.

- **Local: unpacked on disk.** On `skill_load`, the local server unpacks the skill's folder into a read-only cache outside the workspace, e.g. `~/.cache/fabrica/skills/<tree-hash>/<name>/`. Binary files and executable bits are kept, and the folder keeps the skill's name, as the spec requires. `skill_load` returns that path as the skill's folder. The agent runs scripts from there, just like a normally installed skill. Nothing is written into the product repo. Every local runtime can use every skill.
- **Cloud: text-only skills in v0.1.** A remote server can't put files on a cloud runtime's disk. Remotely, the agent reads a skill's files one at a time with `skill_file`. This only works for **text-only** skills: every file is Markdown, plain text or data (`.md`, `.txt`, `.json`, `.yaml`, `.yml`, `.csv`), and none is executable. Any other skill (scripts, binaries, templates in other formats) needs its folder on disk and can't be used remotely. `check`/`plan` fail when a cloud routine lists one in `skills:`. If the agent finds one some other way, `skills_list` marks it unavailable and `skill_load` refuses it with the reason.

Each provider's capability matrix declares its **skill transport**: `directory` (unpacked on disk) or `text` (read file by file). Getting whole skill folders into cloud runtimes is planned (see [Later](#later)), and adding it won't break text-only skills.

### The remote server

You host it yourself: a container or a small VM. There's **no official hosted service**.

- **It serves exactly what one home repo deploys.** You point it at a home repo, and it serves the factory at the ref of each of that repo's cloud deployments, and nothing else. Adding a factory to the server means merging a deployment into the home repo. (Exact CLI flags are still to be decided.)
- **One endpoint per deployment:** `https://fabrica.acme.dev/pr-review`. Fabrica builds the URL from the deployment's `server:` and name, so nobody types it by hand.
- **Auth.** Each deployment gets a secret token, computed as `HMAC(server secret, deployment name)`. The server can check tokens without storing any, and `apply` (holding the same secret, e.g. as a CI secret) computes them and puts them into each runtime's connector.
  - The token goes in an `Authorization: Bearer …` header where the runtime allows headers, or in the URL (`…/pr-review/t/<token>`) where it only accepts a URL. Each provider's capability matrix says which.
  - To rotate, change the server secret and re-apply.
  - OAuth is planned for later.
- **It needs:** read access to the home repo and the factories, the server secret, and a **persistent volume** for its run-history database.
- **It re-fetches regularly,** so merges to followed branches and new deployments show up shortly after they land.

### The local server

- One user-level registration per local tool, installed by `fabrica apply`.
- At each session start it reads your local deployments, matches the workspace, and serves that factory.
- It also inspects the workspace (via MCP roots) to enforce the [safety checks](#part-6-safety-checks-fail-closed): the Fabrica line must be present, and no skill names may clash.
- It unpacks skill folders into its cache when they're loaded (see [Skill files](#skill-files)).

---

## Part 5: Run history

Every run, whether a routine firing in the cloud or you working in your editor, is recorded. This history can't be filled in after the fact, so v0.1 records from day one, even though tools for browsing it come later.

### How a run is recorded

1. **At the start**, the agent calls `run_begin`. For routines, the stub tells it to. In interactive sessions, the MCP `instructions` and the Fabrica line tell it to.
2. **As it works**, the agent calls `run_report` whenever it has progress to record. These are **checkpoints**. There's no "end", because nobody can reliably know when a run is over.
3. **The server links** every `skill_load` in the same MCP session (after `run_begin`) to the run.

### What a run record contains

| Field | Written by | Notes |
|---|---|---|
| `run_id` | server | |
| `started_at` | server | |
| `intent` | the agent | A summary of the original request, **at most 20 words**, in casual plain English. Written by the agent in every run, including routine runs. |
| `routine`, `runtime`, `repo` | stub (routines) or server | The local server works out `repo` from the workspace itself. |
| `deployment` | server | From the endpoint (remote) or the matched local deployment (local). |
| `factory_sha` | server | The exact factory commit that was served. |
| `skills_loaded` | server | Seen from `skill_load` calls. |
| Checkpoints | the agent | Each has a timestamp (server), a `summary` (same 20-word style), a `status` (`progress`, `succeeded` or `failed`; default `progress`) and optional `links` (PR, issue, commit URLs). |

The server rejects an `intent` or `summary` longer than 20 words and asks for a shorter one.

### Where it's stored

- **Remote:** a SQLite database on the server's persistent volume.
- **Local:** `~/.fabrica/runs.db`, with the same schema. It never leaves your machine.

v0.1 has **no built-in way to read run history**. You open the SQLite file with any SQLite tool. That means the database schema is effectively a public interface from day one, and later versions must migrate it carefully or provide a compatibility view.

---

## Part 6: Safety checks (fail closed)

Fabrica catches problems as early as it can, and refuses to continue when something is wrong.

In the first column, ❌ is an error: `apply` won't start until it's fixed. 🔧 is a stale install that `apply` repairs, so `plan` lists it as a change, not an error (see [State](#state)). ⚠️ is a warning and never stops anything.

| Problem | `lock` / `check` / `plan` | Local server, at runtime | Remote server, at runtime |
|---|---|---|---|
| Two imports bring the same item | ❌ error | — | — |
| A routine's `skills:` entry doesn't exist | ❌ error | — | — |
| An item is used with `only:` but its dependency isn't imported | ❌ error, says what to add | — | — |
| Fabrica line missing from root `AGENTS.md` (or root `CLAUDE.md`) | ❌ error, for each deployed repo | ❌ refuses to serve | not detectable |
| Repo skill has the same name as a factory skill (not in `expects:`) | ❌ error, for each deployed repo | ❌ `run_begin` and `skill_load` refuse | not detectable |
| Repo rules contain process instructions that conflict with factory rules | can't be checked by code | the agent stops, alerts and reports `failed` | the agent stops, alerts and reports `failed` |
| Two specific local deployments for the same repo and runtime | ❌ error | — | — |
| Local deployment in a team home repo | ❌ error | — | — |
| Cloud deployment without `server:` | ❌ error | — | — |
| Trigger, connector or side effect unsupported by a runtime | ❌ error | — | — |
| A routine's trigger or connectors changed since apply, to ones the runtime supports (stub outdated) | 🔧 update in `plan` | ❌ `run_begin` refuses | ❌ `run_begin` refuses |
| A routine, repo or runtime was removed from a deployment, but its stub is still installed (stub no longer deployed) | 🔧 delete in `plan` | ❌ `run_begin` refuses | ❌ `run_begin` refuses |
| A cloud routine's `skills:` lists a skill that isn't text-only | ❌ error | — (works locally) | ❌ `skill_load` refuses |
| Another apply holds the deployment's lock | ❌ `apply` stops, names the holder | — | — |
| Two remote resources share one identity | ❌ error in `plan` | — | — |
| Product repo doesn't provide something in `expects:` | ⚠️ reported | — | — |
| Prose mentions a skill you forgot to import, or a routine uses a skill not in its `skills:` | ⚠️ warning | — | — |
| Import expects something your `expects:` doesn't list | ⚠️ warning | — | — |
| `evals/` folder exists | ⚠️ warning (reserved) | — | — |

**Remote runs** can't inspect the workspace, so they rely on `check`/`plan`, which read each deployed repo's default branch. Running `fabrica plan` on a schedule in the home repo's CI catches problems that appear *after* apply: the CI job fails.

When the local server refuses, it explains why in its MCP `instructions`, returns an error from `run_begin`, and records the run as `failed`.

---

## Part 7: Providers and apply modes

A **provider** maps Fabrica's concepts onto one runtime.

- **Built-in** providers are compiled into the `fabrica` binary. v0.1 has cloud: Claude routines, Cursor automations, Codex. Local: Claude Code, Cursor IDE, Codex CLI.
- **Folder providers** ship inside any factory and are imported like everything else:

```
providers/<name>/
  provider.yaml    # capability matrix (plain data)
  api              # optional executable, any language, JSON over stdio
  handoff.md       # template for handoff instructions
```

The **capability matrix** lists, for each trigger, connector, side effect, skill transport and MCP auth method (`mcp_auth`), whether the runtime supports it **natively**, through **emulation**, or **not at all**. It also lists the supported apply modes, and whether the runtime's API can find resources by [identity](#state), which `api` mode requires. Because it's plain data, `check` needs no code to run. Published together, the matrices form a public table of what each runtime supports, which also makes a case to runtime vendors for adding the missing APIs.

Folder providers can run code (`api`) with your runtime credentials during `apply`. Imports are pinned by commit and `upgrade` shows diffs, so new provider code never arrives unseen.

### Apply modes

Each provider supports one or more of these, tried in order:

1. **`api`**: Fabrica creates, updates and deletes resources through the runtime's API. This is the goal for every runtime. Local runtimes count as `api`, since Fabrica writes their config files directly.
2. **`handoff`**: Fabrica writes step-by-step instructions that a human, or an AI with computer use, can follow. **Fabrica never does computer use itself.**
   - The handoff ends with `fabrica confirm <id> [--remote-id …]`, and until then the change is **pending** in state.
   - Without a remote ID, the resource is tracked as **unverifiable**: no drift detection and no automatic deletion. `plan` says so.
   - Until an update or delete handoff is done, the old stub can still fire, but the server refuses its runs (see [Stale stubs](#stale-stubs)).
3. **`unsupported`**: `apply` fails loudly and gives the reason.

---

## Part 8: The CLI

Written in Go and distributed as static binaries, installed with `curl … | bash`.

Fabrica **sits beside git and never wraps it.** Factory and home repos are plain git repos, edited and reviewed with normal git and PR workflows. The CLI only ever writes to git in one place: committing and pushing state in the home repo. (The remote server also writes, but only to its run-history database.)

| Command | What it does |
|---|---|
| `fabrica new` | Starts a new factory repo, from the starter factory or from scratch |
| `fabrica check` | Lints a factory, validates deployments against capability matrices, checks product repos (Fabrica line, skill clashes, unmet `expects:`) |
| `fabrica lock` | Resolves imports into `fabrica.lock` |
| `fabrica upgrade` | Moves imports to newer versions and shows a diff, including changes to items you've overridden |
| `fabrica plan` | Shows what `apply` would do for each runtime: creates, updates and deletes, native / emulated / unsupported, apply mode, follow vs frozen, pending handoffs, and any errors that block `apply`. Exits `0` when in sync, `2` with changes, `1` with errors |
| `fabrica apply` | Reconciles deployments with runtimes. Cloud: takes the deployment's lock, then commits and pushes state. Local: installs MCP entries and local routines on this machine |
| `fabrica confirm` | Closes a pending handoff |
| `fabrica unlock` | Removes a lock left behind by a crashed apply |
| `fabrica mcp [--http]` | Serves a factory over MCP, locally or remotely |

Exact flags and syntax are still to be decided. Integration with git workflows happens through hooks and CI (e.g. `fabrica check` as a pre-commit hook, `fabrica apply` in CI), not through wrapper commands.

---

## User stories

**1. Solo developer, everywhere at once.**
Maya writes a personal factory with her favorite skills and rules. In her personal home repo she adds a local deployment with `repos: ["*"]` and runs `fabrica apply`. Now every Claude Code and Cursor session she opens gets her factory, as long as the repo has the Fabrica line. In a repo without it, Fabrica tells her exactly what's missing instead of quietly half-working.

**2. Team lead, one reviewer for twenty repos.**
Sam's team factory imports a community skills repo plus their own `pr-review` skill. One deployment file lists twenty repos, each with its own Slack channel, on Claude and Cursor. CI runs `fabrica apply` on every merge to the home repo. Four weeks later, Sam checks the run-history database to see how often reviews failed and why.

**3. Factory author, shipping an improvement.**
Priya improves the `pr-review` skill and merges it to the factory's `stable` branch. Every deployment following `stable` uses the new version on its next run. Nobody re-applies, and there are no handoffs.

**4. A conflicting repo.**
A product repo adds its own `pr-review` skill, which clashes with the factory's. The nightly `fabrica plan` in the home repo's CI fails and names the repo and the skill. Locally, the Fabrica server refuses to start runs in that repo until the clash is resolved.

---

## v0.1 scope

**In:**
- Factory format: skills, routines, rules (Cursor-style), connectors, folder providers, imports (including plain skill and rule repos), `fabrica.lock`, `expects:`.
- Home repo: cloud and local deployments, follow/frozen refs, per-deployment state with a git-based apply lock, stable resource identities.
- Commands: `check`, `lock`, `upgrade`, `plan`, `apply`, `confirm`, `unlock`, `mcp` (stdio and `--http`).
- Built-in providers: Claude routines, Cursor automations and Codex (cloud), plus Claude Code, Cursor IDE and Codex CLI (local). Each uses `api` where available and `handoff` otherwise.
- Remote server: home-repo-driven endpoints, HMAC tokens, stale-stub checks (still deployed, shape fingerprint), run-history database.
- Skills: whole folders unpacked on disk locally, text-only skills in cloud runtimes.
- Run recording (local and remote).
- The Fabrica line and all the fail-closed checks above.
- An example public starter factory with around five real skills, routines and rules.

**Success test:** one person defines a PR-review routine once and has it running on both Claude and Cursor against one repo. Skills and rules are served live over MCP, every run is recorded in run history, and the only change to the product repo is the Fabrica line at the top of its `AGENTS.md`.

---

## Risks and trade-offs we've accepted

- **No grants.** A deployment that follows a branch picks up new side effects (e.g. a routine that starts merging PRs) without anyone reviewing them in the home repo. New *connectors* and trigger changes still need a re-apply, and the server refuses to run the routine until then, so those get reviewed. The planned fix is in [Later](#later).
- **Changing a routine's trigger or connectors stops it wherever it's followed,** until each home repo re-applies. A refused run is a lost event (e.g. a PR that doesn't get reviewed). We chose that over running a routine with the wrong trigger or missing connectors.
- **A crashed apply locks its deployment** until someone runs `fabrica unlock`. CI applies to that deployment fail loudly until then.
- **Cloud routines can only use text-only skills.** Many published skills include scripts, so they work locally but not in cloud runtimes yet.
- **The factory's branch protection is a security boundary.** Anyone who can push to a followed branch changes what every deployment following it does.
- **Declared side effects are an honor system.** A factory author can write anything in prose. The real limit is what each connector's credentials allow.
- **Remote runs can't inspect the workspace.** Clashes and a missing Fabrica line are only caught by `check`/`plan` on the default branch, not on the branch a run is working on.
- **"Product knowledge only" is enforced by the agent,** so some conflicts will slip through.
- **Tokens in URLs can leak** through logs or screenshots. This is only used where a runtime offers nothing better.
- **The run-history schema is effectively public** because there's no read API yet.
- **Run records depend on agents calling `run_begin` and `run_report`.** Server-observed fields are reliable, but agent-written ones are best effort.
- **Third-party repos can't be used without the Fabrica line.** That's deliberate (start strict, loosen later), but it limits the wildcard default to repos you've converted.
- **Changing the Fabrica sentence** means every future binary must keep accepting the old ones.

---

## Later

Deferred on purpose, roughly in priority order. Adding any of these later shouldn't break v0.1 setups.

1. **Grants.** Planned approach: add each routine's declared side effects to its recorded [stub shape](#stub-shape), so the server refuses a routine that now asks for more, until someone re-applies.
2. **Reading run history.** A `fabrica runs` command, MCP tools so agents can read history, and "feedback into the factory" (e.g. a routine that reads recent failures and proposes factory fixes).
3. **Whole skill folders in cloud runtimes.** For example, `skill_load` could return a short-lived download URL that the sandbox unpacks into a temporary folder, or a provider could use the runtime's environment setup. This depends on each runtime's sandbox network access, so it would be a new skill transport in the capability matrix.
4. **OAuth** for remote endpoints.
5. **Bots and bot-platform providers.** Persona + routines + skill allowlist, deployable on bot platforms.
6. **`fabrica graph`** (v0.2). Visualizes routines → skills → expectations and connectors. Skill-to-skill edges will be *inferred* from prose, never declared.
7. **Skill and routine evals,** using the reserved `evals/` folder, and probably real run history as input.
8. **Plugins and `fabrica export`.** Requiring runtime plugins, or packaging a factory as a plugin.
9. **Overriding built-in providers** with folder providers.
10. **Serving a factory from a local working tree** (`path:` in local deployments) for a faster authoring loop.
11. **Local deployments in team home repos** (team-wide editor defaults).
12. **Loosening strict rules** where real use shows it's needed: the Fabrica line on third-party repos, multiple local deployments per repo, `only:` pulling in dependencies automatically, a home-wide default `server:`, holding back outdated stubs (serving the last commit whose shape still matches) instead of refusing them, applies waiting for a held lock instead of stopping, and more file types counting as text-only skills.
13. **Run lifecycle:** detecting runs that ended, or that never reported.
14. **Runtime clash detection for remote runs** (the server reading the product repo itself).
