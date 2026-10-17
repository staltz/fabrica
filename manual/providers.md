# Providers

A **provider** maps Fabrica's ideas onto one runtime. Blueprints describe *what* should happen, and providers translate that for each specific tool.

- **Built-in providers** are compiled into the `fabrica` binary. v0.1 has four cloud ones (Claude routines, Cursor automations, Codex and Copilot automations) and four local ones (Claude Code, Cursor IDE, Codex CLI and Copilot CLI).
- **Folder providers** ship inside any blueprint, in `providers/<name>/`, and are imported like everything else. They're for runtimes Fabrica doesn't support out of the box.

## Capability matrices

The heart of every provider is its **capability matrix**: plain data that lists, for each trigger, connector, side effect, skill transport and way of passing the Fabrica token, whether the runtime supports it **natively**, through **emulation**, or **not at all**. It also lists the provider's apply modes.

Because the matrix is plain data, `fabrica check` and `fabrica plan` can tell you a routine won't work on a runtime without running any provider code. And published together, the matrices make a public table of what each runtime supports, which also makes a case to runtime vendors for adding the missing APIs.

What the values mean:

- **Triggers** are `native` or `unsupported`. Triggers can't be emulated. `multiple_triggers: true` says one resource can fire on several triggers, so a routine with several gets one stub with all of them; without it, it gets a stub per trigger (see [Several triggers](blueprints.md#several-triggers)). Local runtimes always get one per trigger, since a hook fires on one event.
- **Connectors** are `native` (the runtime has the integration built in), `emulated` (it doesn't, but Fabrica can register the connector's `fallback:` MCP server instead) or `unsupported`. MCP servers are listed by transport: `mcp.http`, `mcp.sse`, `mcp.stdio`.
- **Side effects** are `native` (the runtime does it itself), `emulated` (Fabrica turns it into prompt instructions, plus the connector that carries it out) or `unsupported`. A side effect is looked up by its exact name, then `connector.*`, then `*`. A side effect named exactly as `native` is one of the runtime's own features: each stub turns it on for routines that list it and off for the rest. Some side effects can only be native, because they outlive the run (`github.pr_autofix`, which auto-fixes the pull requests a run opens): wildcards never match them, and listing one as `emulated` is an error.
- **Models** list the models a stub can ask the runtime to run its routine on, each with the efforts it can run at (`none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`; an empty list means the runtime has no effort setting for it). A routine runs on the first of its [`models:`](blueprints.md#models) the runtime lists. A runtime that doesn't list any picks its model itself, and only a routine's `default` entry matches it. Models go by their makers' IDs, like blueprints name them; **model IDs** (`model_ids:`) say what the runtime calls the ones it calls something else. **Fast models** (`fast_models:`) are the ones the runtime can run in a fast mode: a stub for one of them always sets fast mode, on when the routine's entry says `fast: true` and off otherwise, so the runtime's own default never decides.
- **Launch modes** say how Fabrica can start a one-off session there, for an [init routine](blueprints.md#init-routines): `api` (it starts the session itself) and `handoff` (it writes instructions for a person). A runtime without them can't run init routines. See [Init sessions](#init-sessions).
- **Memory** is `native` (the runtime has its own memory feature, which each stub turns on or off to mirror the routine's [`memory:`](blueprints.md#memory) key), `emulated` (Fabrica keeps the memory and serves it over MCP) or `unsupported` (routines with `memory: true` can't run there).
- Anything a matrix doesn't list is unsupported.

### The built-in providers

These matrices are our best reading of each runtime's docs as of October 2026. They're plain YAML in [internal/provider/builtin](../internal/provider/builtin/), and corrections are very welcome.

**Cloud runtimes**

| | `claude-code-cloud` (Claude routines) | `cursor-cloud` (Cursor automations) | `codex` (Codex cloud) | `copilot-cloud` (Copilot automations) |
|---|---|---|---|---|
| Apply mode | handoff | api, with a Cursor API key ([below](#cursor-automations-through-cursors-api)); handoff without | handoff | handoff |
| Skills | text-only | text-only | text-only | text-only |
| Fabrica token | in the URL | in a header | in the URL | in a header, from an Agents secret |
| `schedule` | native | native | native | native (hourly, daily or weekly) |
| `github.pull_request.opened` | native | native | native | native |
| `github.pull_request.synchronize` | native | native | — | native |
| `github.pull_request.reopened` | native | — | — | — |
| `github.pull_request.ready_for_review` | native | — | — | — |
| `github.pull_request.closed` | native | — | — | — |
| `github.pull_request.labeled` | — | native | — | — |
| `github.issues.opened` | — | — | — | native |
| `github.push` | — | native | — | — |
| `github.release.published` | native | — | — | — |
| Built-in connectors | github, slack, linear, notion | github, slack (linear through its fallback) | github | github |
| MCP servers | http, sse | http | http | http, sse, stdio |
| Side effects | `github.pr_autofix` native (*Auto-fix pull requests*), the rest emulated | all emulated | all emulated | all emulated |
| Memory | emulated | native (the automation's memory tool) | emulated | emulated |
| Models | Claude's: Fable, Opus, Sonnet and Haiku, at `low` to `max` | Cursor's whole cloud agent catalog, Auto for `default` | OpenAI's GPT models, at `low` to `xhigh` | — (always Auto) |
| Init sessions | handoff (a Claude Code session on the web) | api, as a cloud agent, with a Cursor API key; handoff without | handoff (a Codex cloud task) | — |

Cursor's memory tool is on by default, so every Cursor automation gets it turned on or off: on only for routines with `memory: true`. Claude routines have no documented memory setting, and the memories of Codex and Copilot work per project or repository rather than per task, so routines with `memory: true` get their memory from Fabrica there, and the handoffs say to leave any memory of the runtime's own off.

Claude routines auto-fix the pull requests a run opens when their *Auto-fix pull requests* toggle is on (under **Advanced settings**, on the **Behavior** tab), so every Claude routine gets it turned on or off: on only for routines that list the `github.pr_autofix` side effect. The others have no such setting for their automations, so routines that list it can't run there.

Three of these four have no API for creating triggered automations yet, so they use **handoff** mode: Claude routines can only be fired through their API; Codex has no automation API; GitHub's Agent Tasks API can start Copilot sessions but not create automations. Cursor's automations do have one, the API behind Cursor's [Terraform provider](https://github.com/cursor/terraform-provider-cursor), so `cursor-cloud` uses `api` mode whenever it has a Cursor API key. A Claude routine's handoff can also be followed from the Claude Code CLI, whose `/schedule` command creates and edits routines.

### Cursor automations through Cursor's API

Give Fabrica a [Cursor API key](https://cursor.com/dashboard/api) wherever you run `fabrica apply`, and it creates, updates and deletes Cursor automations itself. On your laptop, `fabrica auth login cursor-cloud` opens the dashboard page where you make one, checks the key you paste with Cursor, and saves it in your keychain. In CI, set `CURSOR_API_KEY` as a secret (`fabrica auth env CURSOR_API_KEY | gh secret set --env-file -` copies yours). Without a key, `cursor-cloud` falls back to handoffs, and `plan` says why. `CURSOR_TOKEN`, the name Cursor's Terraform provider uses, works too, and `CURSOR_ENDPOINT` overrides the API's address (`https://api2.cursor.sh`). See [Credentials](auth.md).

Fabrica doesn't run Terraform. Cursor doesn't publish docs for this API, but its Terraform provider is a client for it: Fabrica makes the same calls (Connect RPC to `aiserver.v1.AutomationsService`, after exchanging the API key for a session token, like the provider does), in the protocol's JSON encoding, with no extra dependencies.

Each stub becomes one automation:

- **Name:** the stub's identity. **Description:** says Fabrica manages it.
- **Triggers** on the stub's repo (an automation takes several, so `cursor-cloud` has `multiple_triggers: true`, and a routine with several triggers gets one automation with all of them): `github.pull_request.opened` is Cursor's *Pull request opened* (which also fires when a draft is marked ready for review), `synchronize` is *Pull request pushed*, `labeled` is *Pull request label changed* (when any label is added), `github.push` is *Push to branch*, and `schedule` is a cron schedule. Cursor has no trigger for a reopened pull request.
- **Tools:** the Fabrica MCP server, plus one tool per connector: `github` turns on *Comment on pull request*, `slack` turns on *Send to Slack* for any channel (routines name their channels in params, which are served live), and every MCP connector, including `linear` through its fallback, adds its MCP server.
- **Memory:** the automation's memory tool is on for routines with `memory: true` and off for the rest.
- **Model:** the routine's model, as one of the variants in Cursor's model catalog: the routine's effort as the model's effort parameter (Cursor calls it `effort`, `reasoning` or `reasoning_effort`, depending on the model), fast mode on if the routine's entry says `fast: true` and off otherwise (Cursor runs Grok, Composer, GPT-5.2 and GPT-5.3 Codex fast by default; GPT-5.4 and 5.5 only run fast with the 272k context window), and Cursor's defaults for the rest (context window, thinking). Without one, Cursor picks (Auto).
- **Prompt:** the stub prompt. **Repository:** the stub's repo.

Automations keep Cursor's default sharing setting, so they belong to whoever owns the API key. In CI, use a key that isn't tied to one person's account if you can.

Two things work differently from runtimes whose API can list resources:

- **Cursor's API can't list automations**, only read one back by its ID. So the matrix says `find_by_remote_id: true` instead of `find_by_identity: true`: `plan` and `apply` look up each automation that state records, by ID, to spot ones deleted or renamed by hand (a rename gets put back) and ones whose prompt no longer carries the stub's fingerprint (they get updated). Apply commits state twice around each create, while it still holds the lock: once before the call, and again when the new ID comes back. If the answer is lost, or apply stops before the ID is recorded, the next plan reports `refresh.uncertain-create` and will not create another (a second automation would run alongside the first). Look for it in Cursor, then `fabrica confirm <id> --remote-id <its ID>` if it's there, or `fabrica confirm <id> --absent` if it isn't.
- **Cursor's API can't set up MCP servers.** An automation only *names* its MCP servers, and Cursor looks them up in the MCP settings of the automation's owner. So each factory's Fabrica server is named `fabrica-<factory>` (each factory has its own address and token), and when an apply creates or updates Cursor automations, it writes `~/.cache/fabrica/handoffs/<factory>/cursor-cloud-setup.md`, listing each MCP server to have in Cursor's settings, with its URL and the `Authorization` header that carries the factory's token. You set them up once, and again when the file changes, like after the server secret is rotated. The file holds the token, so it's readable only by you, like a handoff.

Copilot reads MCP servers from the repository's settings (**Settings → Copilot → MCP servers**), not from each automation, so every Copilot automation in a repository shares one `fabrica` server, and only one factory can deploy to a repository on `copilot-cloud` (`fabrica check` says so when two do). The handoff puts the Fabrica token in an Agents secret called `COPILOT_MCP_FABRICA_AUTHORIZATION`, so it never shows in the configuration. Copilot automations only run in private and internal repositories for now, and their schedules recur hourly, daily or weekly, so the handoff asks you to pick the one closest to the routine's cron.

**Local runtimes**

| | `claude-code-local` | `cursor-local` | `codex-cli` | `copilot-local` |
|---|---|---|---|---|
| Apply mode | api (writes config files) | api | api | api |
| Skills | unpacked on disk | unpacked on disk | unpacked on disk | unpacked on disk |
| MCP server config | `~/.claude.json` | `~/.cursor/mcp.json` | `~/.codex/config.toml` | `~/.copilot/mcp-config.json` |
| Hooks config | `~/.claude/settings.json` | `~/.cursor/hooks.json` | `~/.codex/hooks.json` | `~/.copilot/hooks/fabrica.json` |
| `session.start` | native (SessionStart) | — | native (SessionStart) | native (sessionStart) |
| `prompt.submit` | native (UserPromptSubmit) | — | native (UserPromptSubmit) | — |
| `session.stop` | native (Stop) | native (stop) | native (Stop) | native (agentStop) |
| `git.pre_commit`, `git.pre_push` | native (PreToolUse on Bash) | native (beforeShellExecution) | native (PreToolUse on Bash) | native (preToolUse on bash) |
| `schedule` | native (desktop app scheduled task) | — | — | — |
| Connectors | MCP over http, sse, stdio; github, slack and linear through their fallbacks | same | MCP over http and stdio; github, slack and linear through their fallbacks | same as Claude Code |
| Side effects | `github.pr_autofix` native for `schedule` (the scheduled task's setting), the rest emulated | all emulated | all emulated | all emulated |
| Memory | emulated (in `runs.db`) | emulated | emulated | emulated |
| Models | — (your session's) | — | — | — |
| Init sessions | api: `claude -p` on the machine that applies, never in CI | — | — | — |

A few things worth knowing about local hooks:

- Cursor's `sessionStart` hook is fire-and-forget and `beforeSubmitPrompt` can't add context, so `session.start` and `prompt.submit` aren't supported in Cursor.
- Cursor also runs the hooks in `~/.claude/settings.json`. Fabrica's Claude Code hooks stay quiet when they notice they're running inside Cursor, so nothing runs twice.
- Codex skips new or changed hooks until you trust them in its `/hooks` screen. After an apply that changes a hook, open Codex and approve it.
- Copilot CLI drops the output of `userPromptSubmitted` hooks that come from config files, so `prompt.submit` isn't supported there. Its hooks get a file of their own, `~/.copilot/hooks/fabrica.json`, rather than the `hooks` field of `~/.copilot/settings.json`, which can have comments that Fabrica would lose. VS Code also runs the hooks in that folder, so Fabrica's Copilot hooks stay quiet when VS Code calls them, since its MCP server isn't registered there. `COPILOT_HOME`, if set, moves all of `~/.copilot`, and Fabrica follows it.
- `git.pre_push` and `git.pre_commit` hold the git command back once per session (and per commit), tell the agent to run the routine, and let the command through when it's tried again.

A `schedule` routine on Claude Code isn't a hook. It becomes a [desktop app scheduled task](https://code.claude.com/docs/en/desktop-scheduled-tasks), one per repo, so its factory has to list its targets (`targets: ["*"]` is an error). Fabrica writes the task's prompt to `~/.claude/scheduled-tasks/fabrica-<routine>-<hash>/SKILL.md`, but the app keeps the schedule and the folder somewhere else, and it ignores task files it didn't create. So when an apply adds a task, it tells you to register it once: it prints a prompt, between `-----BEGIN PROMPT-----` and `-----END PROMPT-----` lines, to send Claude in a desktop session on that repo, asking it to create a scheduled task from that file, keeping its ID, description and prompt. When an apply changes a task's cron, it tells you to update the schedule in the app (Code tab → Routines). The app keeps a task's *Auto-fix pull requests* toggle (under **Advanced settings**, on the **Behavior** tab) outside the file too, so the registration prompt asks for it to be turned on for routines that list `github.pr_autofix` and off for the rest, and when that changes, the apply tells you to flip it in the app. Fabrica records the setting at the end of the task's description, so it knows what it last asked for. Fabrica converts the cron to local time for you, because the app reads cron in local time. Prompt changes need nothing more, because the app picks them up on the next run. Removing a task deletes its folder, and you delete it from the app's list yourself. These tasks only run while the app is open and the computer is awake.

## Init sessions

An [init routine](blueprints.md#init-routines) doesn't get a stub. It gets one session, which `fabrica apply` starts the first time it creates a factory, on the first of the routine's runtimes that's available. A provider's `launch_modes` say how Fabrica can start that session, tried like apply modes:

1. **`api`**: Fabrica starts it itself, and it's available when the provider's credentials are set (or, for a local runtime, when its command line is installed, and not in CI).
2. **`handoff`**: Fabrica writes instructions from the provider's `launch.md`, and the session counts as started once `fabrica confirm` says so.

Fabrica first looks for a runtime it can start the session on itself, in the routine's order, and only then for one with a handoff. Either way, the session gets the init's prompt, which works like a stub's: it says who the session is, and to call `run_begin` and `routine_get`. It reaches the factory's own Fabrica server, with the factory's token, however it's started, and the server checks it like a stub: the factory still runs its init, on that repo and runtime, with the same shape.

Every init has an ID (like `i_3f2a1c9e04`) that stays the same when apply tries again after a failure. **A launcher must never start a second session for an ID it already started one for**, since a try can get further than apply learns.

The built-in ones:

- **`cursor-cloud`** starts a cloud agent through Cursor's [Cloud Agents API](https://cursor.com/docs/cloud-agent/api/endpoints) (`POST /v1/agents`), with the same Cursor API key automations use. The launch carries everything: the prompt, the factory's targets as the agent's repos (the first one is where it starts), the model, and the MCP servers inline, the factory's Fabrica server with its `Authorization` header among them. So unlike automations, nothing needs setting up in Cursor's settings. The agent's ID is derived from the init's ID, and Cursor refuses a second agent with the same one. Connectors need an MCP server (Cursor's API has no built-in integrations to give an agent, except GitHub, which every agent has), and a bearer secret is read from `fabrica apply`'s environment by name. `CURSOR_AGENTS_ENDPOINT` overrides the API's address (`https://api.cursor.com`).
- **`claude-code-local`** runs `claude -p` on the machine that runs `fabrica apply`, in the background, in a fresh clone of the factory's first target under `$FABRICA_CACHE/sessions/<factory>/<init ID>/`. It gets exactly the init's MCP servers (`--mcp-config`, from a file only you can read, since it holds the token; `--strict-mcp-config`), so it's served by the factory's Fabrica server, whatever local factories you have. Nobody answers permission prompts, so it runs with `--permission-mode auto`. Its session ID is derived from the init's ID, and its output goes to `session.log` in the same folder. `FABRICA_CLAUDE_BIN` says which `claude` to run.
- **`claude-code-cloud`** and **`codex`** write handoffs: start a Claude Code session on the web, or a Codex cloud task, with the connectors, model and prompt they list.

## Apply modes

Each provider supports one or more of these, tried in order:

1. **`api`**: Fabrica creates, updates and deletes resources through the runtime's API. This is the goal for every runtime. Local runtimes count as `api`, since Fabrica writes their config files directly. `api` mode is only allowed when Fabrica can find what it made, so it never duplicates it: either the API can list resources and read their identity back (`find_by_identity: true`), or it can at least read one back by its ID (`find_by_remote_id: true`, like Cursor's). In that second case apply records each create in the home repo before the call and again once it has the new ID, and a create whose ID was never learned is not retried until `fabrica confirm` says whether the resource exists. A cloud provider uses `api` mode only when every credential its matrix declares (under [`credentials:`](#provideryaml)) is set, in its environment variable or saved by `fabrica auth login`, and falls back to its next mode otherwise.
2. **`handoff`**: Fabrica writes step-by-step instructions that a person, or an AI with computer use, can follow. **Fabrica never does computer use itself.** The handoff ends with `fabrica confirm <id> [--remote-id …]`, and until then the change is pending in state. See [Handoffs](home-repos.md#handoffs).
3. **unsupported**: when a provider has no mode Fabrica can use, `plan` and `apply` fail loudly and say why.

## Folder providers

A blueprint can ship a provider for a runtime Fabrica doesn't know:

```
providers/<name>/
  provider.yaml    # the capability matrix (plain data)
  api              # optional executable, any language, JSON over stdio
  handoff.md       # template for handoff instructions
  launch.md        # optional template for starting an init session by hand
```

In v0.1, folder providers describe cloud runtimes only, and can't replace a built-in provider (a folder provider called `claude-code-cloud` is an error). Factories use them by name, like any runtime: `runtimes: [acme-runner]`.

Folder providers can run code (`api`) with your runtime credentials during `apply`. Imports are pinned by commit and `fabrica upgrade` shows diffs (calling out provider changes in particular), so new provider code never arrives unseen.

There's a complete, runnable example in [examples/custom-provider](../examples/custom-provider/).

### `provider.yaml`

```yaml
kind: cloud                          # cloud (local is reserved for built-ins in v0.1)
description: Acme's in-house agent runner
apply_modes: [api, handoff]          # tried in order
launch_modes: [api, handoff]         # optional: how Fabrica can start init sessions
find_by_identity: true               # the API can list resources and read their identity back (folder providers need this for api)
skill_transport: text                # text (read file by file); cloud runtimes can't use directory
mcp_auth:
  header: native                     # can send Authorization: Bearer …
  url: native                        # can take the token in the URL
multiple_triggers: true              # optional: one resource can fire on several triggers
triggers:
  github.pull_request.opened: native
  schedule: native
connectors:
  github: native
  mcp.http: native
side_effects:
  github.pr_comment: native          # its own feature: each stub turns it on or off
  "*": emulated
memory: emulated                     # native, emulated or unsupported (the default)
models:                              # optional: the models a stub can pick, with their efforts
  acme-large: [low, medium, high]
  acme-small: []                     # no effort setting
fast_models: [acme-large]            # optional: the models with a fast mode
model_ids:                           # optional: the runtime's own ID for a model it calls something else
  acme-small: acme-small-v2
credentials:                         # optional: what the api program needs
  ACME_API_KEY:
    description: an Acme API key
    url: https://acme.dev/settings/api-keys   # optional: where you make one
    instructions: Click New key, and copy it. # optional: what to do there
```

Fabrica reads it strictly, like its other files. If `apply_modes` lists `api`, there must be an executable `api` next to it, and `find_by_identity` must be true (the protocol below has no way to look one resource up by ID, so `find_by_remote_id` is only for built-in providers); if it lists `handoff`, there must be a `handoff.md`, which has to list `.Triggers` if the matrix says `multiple_triggers: true`. Likewise, `api` in `launch_modes` needs the `api` program (with the `launch` operation), and `handoff` there needs a `launch.md`.

`credentials:` lists what the `api` program needs, keyed by environment variable (not `FABRICA_…`). `fabrica auth` lists them, and `fabrica auth login` asks for them, opening `url` when there is one. Fabrica only uses `api` mode when they're all set; otherwise the runtime falls back to `handoff`, or, without it, `plan` reports an error that names what's missing. An entry can also list `aliases:`, other variables that work too. See [Credentials](auth.md#for-provider-authors).

### The `api` protocol

The protocol is deliberately boring, so you can write a provider in any language. Fabrica runs `api <op>`, writes one JSON request to its stdin, and reads one JSON response from its stdout. A non-zero exit, or an `"error"` field in the response, means the operation failed, and whatever the program printed on stderr is shown to the user. The environment is passed through, so the program can read its credentials from environment variables, and Fabrica adds the ones it declares under `credentials:` that `fabrica auth login` saved. Each call times out after two minutes.

Every request has `"version": 2` and `"op"`. The operations are:

| Op | Request | Response |
|---|---|---|
| `list` | `factory`, `prefix` | `{"resources": [{"identity", "remote_id", "fingerprint"?}]}`: every resource whose identity starts with `prefix` (like `4ckr#`). Fabrica may list several prefixes for one factory, while it renames stubs from an earlier tag. |
| `create` | `factory`, `resource` | `{"remote_id": "…"}` |
| `update` | `factory`, `remote_id`, `resource` | `{}` |
| `delete` | `factory`, `remote_id` | `{}` |
| `launch` | `factory`, `session` | `{"session_id": "…", "url"?: "…"}`: only with `api` in `launch_modes` |

A `resource` looks like this:

```json
{
  "identity": "4ckr#pr-review@github.com/acme/app",
  "factory": "pr-review",
  "routine": "pr-review",
  "repo": "github.com/acme/app",
  "triggers": [{ "on": "github.pull_request.opened" }],
  "trigger": { "on": "github.pull_request.opened" },
  "connectors": [
    { "name": "github", "kind": "native" },
    { "name": "linear", "kind": "mcp", "url": "https://mcp.linear.app/mcp", "transport": "http", "auth": "oauth" }
  ],
  "fabrica": { "url": "https://fabrica.acme.dev/pr-review", "placement": "header", "token": "fab_…" },
  "prompt": "You are routine `pr-review` (…). Call `run_begin` with …",
  "fingerprint": "e128f7aa2995"
}
```

`triggers` lists what fires the resource, each with `on` and, for `schedule`, `cron`. There's one, unless the matrix says `multiple_triggers: true`, and then a routine with several triggers sends them all. `trigger` is that same one again, for a resource with one, and is missing for a resource with several, so a provider without `multiple_triggers` can keep reading `trigger`.

On runtimes with `memory: native`, a resource also has `"memory": "on"` or `"off"`: what to set the runtime's own memory feature to, mirroring the routine's `memory:` key.

On runtimes whose matrix names side effects as `native`, a resource also has `"side_effects"`, like `{"github.pr_comment": "off"}`: what to set each of the runtime's own features to, `on` when the routine lists that side effect and `off` otherwise.

On runtimes that list `models:`, a resource whose routine picked one of them also has `"model": {"name": "acme-large", "effort": "high", "fast": "off"}`, with the runtime's own ID for the model (from `model_ids:`, if it's there) (without `effort` for the model's default effort). `fast` is `"on"` or `"off"` for the models in `fast_models:`, and missing for the rest. Without `model`, leave the runtime's default.

A `session` is an [init session](#init-sessions) to start. It has the same `identity`, `factory`, `routine`, `repo`, `connectors`, `fabrica`, `model`, `prompt` and `fingerprint` fields as a resource, plus `id`, the init's ID, and `repos`, every target of the factory (`repo`, where the session starts, first). `launch` must never start a second session for an `id` it already started one for: return the first one's `session_id` instead.

Your program must store the `identity` somewhere it can read back (a name or a metadata field), so `list` can return it. If it can also store the `fingerprint`, return it from `list`, which helps `plan` spot drift. The `fabrica.token` is a secret: never log it. When `placement` is `url`, the token is already in `fabrica.url`.

### `handoff.md`

`handoff.md` is a Go [text/template](https://pkg.go.dev/text/template). It's filled in once per change, with these fields:

| Field | What it is |
|---|---|
| `.ID` | The handoff's ID, for `fabrica confirm`. |
| `.Action` | `create`, `update` or `delete`. |
| `.Runtime`, `.Factory`, `.Routine`, `.Repo` | What it's about. |
| `.Identity` | The resource's Fabrica identity. Ask for it to be used as the resource's name. |
| `.FormerIdentity` | The name the resource has now, when it's being renamed from an earlier identity (the factory's tag changed), or empty. Look for the resource under `{{or .FormerIdentity .Identity}}`. |
| `.RemoteID` | The runtime's own ID, for updates and deletes, when known. |
| `.Trigger.On`, `.Trigger.Cron`, `.Trigger.Description` | The trigger, and a sentence describing it. Empty when the stub has several. |
| `.Triggers` | Every trigger of the stub, each with `.On`, `.Cron` and `.Description`. There's more than one only with `multiple_triggers: true`. |
| `.Connectors` | A list, each with `.Name` and `.Setup` (a sentence saying how to set it up). |
| `.SideEffects` | A list of the runtime's own features for its native side effects, each with `.Name` (like `github.pr_autofix`), `.Setting` (`on` or `off`) and `.Setup` (a sentence saying what to set). Handoffs for runtimes with native side effects should have a step for them, so the runtime mirrors the routine. |
| `.Memory.Needed`, `.Memory.Support`, `.Memory.Setup` | Whether the routine has `memory: true`, how the runtime provides memory (`native`, `emulated` or `unsupported`), and a sentence saying what to do with the runtime's own memory setting. Every handoff should have a memory step, so the runtime mirrors the routine. |
| `.Fabrica.URL`, `.Fabrica.Header` | The Fabrica server URL (with the token in it, for URL placement) and the `Authorization` header value (for header placement). |
| `.Model.Name`, `.Model.Effort`, `.Model.Fast`, `.Model.Setup` | The model to pick (the runtime's own ID for it) and its effort (both empty for the runtime's default, and the effort alone for the model's default effort), its fast mode (`on` or `off` for the models in `fast_models:`, empty for the rest), and a sentence saying what to pick. |
| `.Prompt`, `.Fingerprint` | The stub prompt to paste in, and its fingerprint. |
| `.ConfirmCommand` | The exact `fabrica confirm …` line to end with. |

### `launch.md`

`launch.md` is a template too, filled in when an [init session](#init-sessions) has to be started by hand, with these fields: `.ID` (the init's ID, for `fabrica confirm`), `.Runtime`, `.Factory`, `.Routine`, `.Identity`, `.Repo` (where to start it), `.Repos` (every target, `.Repo` first), `.Connectors`, `.Model`, `.Fabrica`, `.Prompt`, `.Fingerprint` and `.ConfirmCommand`, all like `handoff.md`'s.

There's an extra template function, `indent N text`, which indents every line of the text by N spaces (handy for pasting the prompt inside a list item). Missing fields are errors, so a typo in the template fails loudly. The built-in templates in [internal/provider/builtin](../internal/provider/builtin/) are good starting points.
