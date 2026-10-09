# Fabrica — Design

Status: draft, pre-v0.1. Config syntax shown in this document is **illustrative only**; file formats and names are deliberately undecided (see [Open questions](#open-questions)).

## Purpose

Fabrica is an open source tool for defining a **software factory** — the skills, routines, bots and connectors that describe *how agents build software* — and deploying it onto many agent runtimes (Claude routines, Cursor automations, Codex, bot platforms, IDEs, …).

It is "higher-order version control": the factory is versioned separately from the software it builds.

## Principles

1. **Process lives in factories; product knowledge lives in product repos.** Generic skills about how to build software never live in the product repository.
2. **Product repos are factory-agnostic.** A product repo declares nothing about which factory (if any) works on it. Any number of people or teams may point different factories at the same repo.
3. **Universal, not tied to any runtime.** Factories express abstract intent; providers map it onto concrete runtimes.
4. **Forward-looking.** Where an ecosystem capability is missing (e.g. skills-over-MCP, runtime write APIs), Fabrica uses the simplest working substitute and switches to the real thing as soon as it exists.
5. **Minimal.** Few primitives, few commands, no hosted service, git as the storage and collaboration layer.

## The three kinds of repository

| Repo | Contains | Knows about |
|---|---|---|
| **Factory repo** | Skills, routines, bots, connectors, providers, imports, `fabrica.lock` | Nothing downstream. It does not know who uses it. |
| **Home repo** (personal or team) | Deployments, local factory selection, deployment state | Which factories run against which product repos, on which runtimes |
| **Product repo** | Code, plus repo-specific skills | Nothing about factories |

The home repo is the only place where bindings between factories and product repos live. It is an ordinary git repo (e.g. `~/.fabrica`, pushed to a remote) so it is durable, versioned and portable across machines. A "team home repo" is the same thing owned by an organization.

## Primitives (inside a factory repo)

### Skill
A folder with a `SKILL.md` in the standard Agent Skills format, unchanged. A skill may declare **`expects:`** — skills that the *workspace* (the product repo the agent is working on) must provide:

```yaml
---
name: release
expects:
  - test          # "how this repo runs its tests"
  - deploy?       # optional
---
1. Run the `test` skill. ...
```

Names under `expects:` carry no prefix; the field itself gives them their meaning: *resolved from the workspace's own skills*, falling back to a default the factory may ship. Factories expect; workspaces provide. `fabrica check` reports unmet expectations for a given product repo. The product repo's skills are plain repo-local skills with no Fabrica syntax.

### Routine
**Trigger + prompt + connectors + side effects + params.** Routines are written as *abstract intent*, independent of any runtime:

```yaml
---
trigger: { on: github.pull_request.opened }
connectors: [github, slack?]          # '?' = optional
side_effects:
  - github.pr_comment
  - slack.notify: { channel: "{{params.slack_channel}}" }
params:
  slack_channel: { required: false }
---
Review the PR using the `pr-review` skill...
```

Each provider maps every trigger, connector and side effect to one of: **native** (the runtime has the feature), **emulated** (compiled into prompt instructions + the required connector), or **unsupported** (an error in `check`/`plan`). Triggers cannot be emulated.

### Bot (optional)
A grouping of **identity/persona + routines + skill allowlist**, deployable onto bot platforms (e.g. Grok Bot, OpenMausBot, ChatGPT Dot). Bots are optional: free-standing routines remain deployable on their own. On runtimes without a bot concept, a bot deploys as its routines, with persona and skill allowlist applied through the stub and the MCP endpoint.

### Connector
One primitive covering both integrations and MCP servers, with two kinds:

- `native` — maps to a runtime's built-in integration (e.g. `github`, `slack`).
- `mcp` — an MCP server definition: URL, transport, auth kind (`oauth`, `bearer`, …). Secrets are referenced by name, **never stored** in the factory.

Routines and bots reference connectors by ID, as they do skills. Providers map connectors to native integrations where available, otherwise register the MCP server in the runtime's config. The factory's own `fabrica mcp` endpoint is added to every deployment automatically.

### Provider (optional, custom)
See [Providers](#providers). A factory may ship providers for runtimes Fabrica doesn't support out of the box.

### Composition: imports
Factories can import other factories (whole, or cherry-picked parts).

- Every item has a fully-qualified ID, `owner/factory:name`; short names resolve only when unambiguous.
- **Collisions are errors.** Overriding an imported item requires an explicit `replaces`.
- Imports are **content-pinned**: `fabrica.lock` records a commit SHA for every import. Upstream changes arrive only through `fabrica upgrade`, which shows a diff.
- **Every consumer (a routine, a bot, a local session) uses exactly one factory.** Combining factories is done by authoring a factory that imports others, so conflicts are resolved once, at lock time, never at run time.

## Runtime model

### Stubs
Fabrica never deploys a routine's real content to a runtime. It deploys a thin, stable **stub**: the trigger, the connectors, and a short prompt that says "you are routine `X` of factory `F@ref`; load it from the Fabrica MCP server and follow it."

Consequences:
- Editing a routine's prompt or skills takes effect on the next run with no re-apply (subject to the pinned ref).
- Re-applying is only needed when triggers, connectors or pins change, which is rare. That makes weak or missing runtime APIs tolerable.

### MCP: the single transport
`fabrica mcp` serves a factory to agents. It is one binary with two transports:

- `fabrica mcp` — stdio, for local IDEs and CLI agents.
- `fabrica mcp --http` — HTTP, for cloud runtimes. It is stateless and read-only (it serves a git ref) and self-hosted by the user. **There is no official hosted service.**

**One endpoint per `factory@ref`** (and optionally per bot, which serves only that bot's skill allowlist). The version is chosen once, at deployment time, never by the model at call time.

Until runtimes support skills-over-MCP natively, skills are **emulated with plain MCP tools**:

- `skills_list()` → names and descriptions
- `skill_load(name)` → `SKILL.md` and file listing
- `skill_file(name, path)` → supporting files
- `routine_get(name)` → a routine's full definition, for stubs

The server's MCP `instructions` and the stub prompt tell the agent to list and load relevant skills. When skills-over-MCP becomes available, the server will expose skills natively and the emulation will be dropped.

There is no file materialization and no git-fetch fallback. MCP is the only transport.

### Local use
`fabrica mcp` (stdio) is registered once in each local tool. It serves exactly one factory, chosen through the home repo:

- a global default factory, and
- optional per-product-repo overrides, matched by the workspace's git remote URL.

The binding lives entirely on the user's side; the product repo is untouched.

## Providers

A provider maps Fabrica's abstract model onto one runtime. Providers come in two forms:

- **Built-in**: compiled into the `fabrica` binary, for the major runtimes (v0.1: Claude routines, Cursor automations, Codex).
- **Folder providers**: shipped inside any factory and imported/namespaced like everything else. A folder provider may `replaces` a built-in one.

```
providers/<name>/
  provider.<ext>     # capability matrix: triggers, connectors, side effects,
                     # skill transport → native | emulated | unsupported;
                     # supported apply modes
  api                # optional executable, any language, JSON over stdio (CRUD on remote resources)
  handoff.md         # template for handoff instructions
```

The capability matrix is plain data, so `check` needs no code. Published together, the matrices form a public table of what each runtime supports, which also serves as a case to runtime vendors for adding APIs.

### Apply modes
Each provider supports one or more apply modes, tried in fallback order:

1. **`api`**: Fabrica creates, updates and deletes the remote resources through an API or SDK. This is the goal for every runtime.
2. **`handoff`**: Fabrica writes instructions that either a human or an AI tool with computer use (ChatGPT, Claude, Cursor, …) can follow. **Fabrica itself never does computer use.** The handoff ends with `fabrica confirm <id> [--remote-id …]`. Until confirmed, the change is **pending** in state. Without a remote ID, the resource is tracked as **unverifiable** (no drift detection, no automatic deletion), and `plan` says so.
3. **`unsupported`**: `apply` fails loudly, with the reason.

## Deployments and state

A **deployment** lives in a home repo and says: *factory@ref → which routines/bots → which product repo(s) → which runtimes*, plus params. Several deployments, from different home repos and different factories, may target the same product repo. Duplicate behavior (e.g. two PR reviewers) is a social problem, not one the tool prevents.

**State** maps every deployed resource to its remote IDs (and pending handoffs). It is committed to the home repo by `apply`.

- **Locking:** `apply` pulls, plans, applies and pushes. If the push is rejected because someone else applied, it re-pulls and re-plans.
- **Who applies:** CI on the home repo is recommended, but individuals may apply too.
- **Secrets** never live in factory or home repos. They come from the OS keychain, CI secrets, or each runtime's own auth.

## CLI

Written in Go and distributed as static binaries installed via `curl … | bash`.

Fabrica **sits beside git and never wraps it.** Factory and home repos are plain git repos, edited and reviewed with normal git and PR workflows. Fabrica reads git (refs, imports) and writes to git in exactly one place: committing and pushing state in the home repo.

| Command | Purpose |
|---|---|
| `fabrica check` | Lint a factory; validate deployments against provider capability matrices; report unmet `expects:` for a product repo |
| `fabrica lock` | Resolve imports into `fabrica.lock` |
| `fabrica upgrade` | Move imports to newer versions, showing a diff |
| `fabrica plan` | Show what `apply` would do per runtime: native / emulated / unsupported, apply mode, pending handoffs |
| `fabrica apply` | Reconcile deployments with runtimes; commit and push state |
| `fabrica confirm` | Close a pending handoff |
| `fabrica mcp [--http]` | Serve a factory over MCP |
| `fabrica graph` | *(v0.2)* Visualize bots → routines → skills → expectations / connectors |

Integration with git workflows is done through hooks and CI (e.g. pre-commit `fabrica check`, CI `fabrica apply`), not through wrapper commands.

## v0.1 scope

**In:**
- Factory format: skills (with `expects:`), routines, optional bots, connectors, folder providers, imports and `fabrica.lock`.
- Home repo: deployments, local factory selection, state, git-push locking.
- Commands: `check`, `lock`, `upgrade`, `plan`, `apply`, `confirm`, `mcp` (stdio and `--http`).
- Built-in providers for Claude routines, Cursor automations and Codex, using `api` where available and `handoff` otherwise.
- An example public starter factory with around five real skills and routines.

**Success test:** one person defines a PR-review routine once and has it running on both Claude and Cursor against one repo, with skills served live over MCP and the product repo untouched.

## Open questions

Deferred, in rough priority order:

1. **Config formats** (TOML / YAML / JSON / other) for factory manifests, routines, bots, connectors, deployments, local factory selection and state. File naming too (`ROUTINE.md`, `BOT.md`, …). To be decided **before** v0.1 ships.
2. **Plugins** (in the sense of Claude plugins and ChatGPT plugins): whether and how factories reference or define them.
3. **Grants**: whether deployments must explicitly grant a routine's side effects and connectors.
4. **`fabrica graph`** (planned for v0.2).
5. **Bot-platform providers**.
6. **Run observability**: run history, outcomes, feedback into the factory.
7. **Skill evals / testing**.
8. **`fabrica export`**: packaging a factory as a runtime-native plugin.
