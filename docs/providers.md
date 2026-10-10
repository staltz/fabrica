# Providers

A **provider** maps Fabrica's ideas onto one runtime. Factories describe *what* should happen, and providers translate that for each specific tool.

- **Built-in providers** are compiled into the `fabrica` binary. v0.1 has three cloud ones (Claude routines, Cursor automations and Codex) and three local ones (Claude Code, Cursor IDE and Codex CLI).
- **Folder providers** ship inside any factory, in `providers/<name>/`, and are imported like everything else. They're for runtimes Fabrica doesn't support out of the box.

## Capability matrices

The heart of every provider is its **capability matrix**: plain data that lists, for each trigger, connector, side effect, skill transport and way of passing the Fabrica token, whether the runtime supports it **natively**, through **emulation**, or **not at all**. It also lists the provider's apply modes.

Because the matrix is plain data, `fabrica check` and `fabrica plan` can tell you a routine won't work on a runtime without running any provider code. And published together, the matrices make a public table of what each runtime supports, which also makes a case to runtime vendors for adding the missing APIs.

What the values mean:

- **Triggers** are `native` or `unsupported`. Triggers can't be emulated.
- **Connectors** are `native` (the runtime has the integration built in), `emulated` (it doesn't, but Fabrica can register the connector's `fallback:` MCP server instead) or `unsupported`. MCP servers are listed by transport: `mcp.http`, `mcp.sse`, `mcp.stdio`.
- **Side effects** are `native` (the runtime does it itself), `emulated` (Fabrica turns it into prompt instructions, plus the connector that carries it out) or `unsupported`. A side effect is looked up by its exact name, then `connector.*`, then `*`.
- Anything a matrix doesn't list is unsupported.

### The built-in providers

These matrices are our best reading of each runtime's docs as of October 2026. They're plain YAML in [internal/provider/builtin](../internal/provider/builtin/), and corrections are very welcome.

**Cloud runtimes**

| | `claude-code-cloud` (Claude routines) | `cursor` (Cursor automations) | `codex` (Codex cloud) |
|---|---|---|---|
| Apply mode | handoff | handoff | handoff |
| Skills | text-only | text-only | text-only |
| Fabrica token | in the URL | in a header | in the URL |
| `schedule` | native | native | native |
| `github.pull_request.opened` | native | native | native |
| `github.pull_request.synchronize` | native | native | — |
| `github.pull_request.reopened` | native | native | — |
| `github.pull_request.ready_for_review` | native | — | — |
| `github.pull_request.closed` | native | — | — |
| `github.pull_request.labeled` | — | native | — |
| `github.push` | — | native | — |
| `github.release.published` | native | — | — |
| Built-in connectors | github, slack, linear, notion | github, slack, linear | github |
| MCP servers | http, sse | http | http |
| Side effects | all emulated | all emulated | all emulated |

None of these three has a public API for creating and listing triggered automations yet, so the built-in cloud providers use **handoff** mode. Claude routines can only be fired through their API; Cursor's Cloud Agents API can launch agents but not create automations; Codex has no automation API. When an API appears, a provider can switch to `api` mode without changing anything else.

**Local runtimes**

| | `claude-code-local` | `cursor-ide` | `codex-cli` |
|---|---|---|---|
| Apply mode | api (writes config files) | api | api |
| Skills | unpacked on disk | unpacked on disk | unpacked on disk |
| MCP server config | `~/.claude.json` | `~/.cursor/mcp.json` | `~/.codex/config.toml` |
| Hooks config | `~/.claude/settings.json` | `~/.cursor/hooks.json` | `~/.codex/hooks.json` |
| `session.start` | native (SessionStart) | — | native (SessionStart) |
| `prompt.submit` | native (UserPromptSubmit) | — | native (UserPromptSubmit) |
| `session.stop` | native (Stop) | native (stop) | native (Stop) |
| `git.pre_commit`, `git.pre_push` | native (PreToolUse on Bash) | native (beforeShellExecution) | native (PreToolUse on Bash) |
| Connectors | MCP over http, sse, stdio; github, slack and linear through their fallbacks | same | MCP over http and stdio; github, slack and linear through their fallbacks |

A few things worth knowing about local hooks:

- Cursor's `sessionStart` hook is fire-and-forget and `beforeSubmitPrompt` can't add context, so `session.start` and `prompt.submit` aren't supported in Cursor.
- Cursor also runs the hooks in `~/.claude/settings.json`. Fabrica's Claude Code hooks stay quiet when they notice they're running inside Cursor, so nothing runs twice.
- Codex skips new or changed hooks until you trust them in its `/hooks` screen. After an apply that changes a hook, open Codex and approve it.
- `git.pre_push` and `git.pre_commit` hold the git command back once per session (and per commit), tell the agent to run the routine, and let the command through when it's tried again.

## Apply modes

Each provider supports one or more of these, tried in order:

1. **`api`**: Fabrica creates, updates and deletes resources through the runtime's API. This is the goal for every runtime. Local runtimes count as `api`, since Fabrica writes their config files directly. `api` mode is only allowed when the API can list resources and read their identity back (`find_by_identity: true`), because without that Fabrica couldn't find, or avoid duplicating, what it made.
2. **`handoff`**: Fabrica writes step-by-step instructions that a person, or an AI with computer use, can follow. **Fabrica never does computer use itself.** The handoff ends with `fabrica confirm <id> [--remote-id …]`, and until then the change is pending in state. See [Handoffs](home-repos.md#handoffs).
3. **unsupported**: when a provider has no mode Fabrica can use, `plan` and `apply` fail loudly and say why.

## Folder providers

A factory can ship a provider for a runtime Fabrica doesn't know:

```
providers/<name>/
  provider.yaml    # the capability matrix (plain data)
  api              # optional executable, any language, JSON over stdio
  handoff.md       # template for handoff instructions
```

In v0.1, folder providers describe cloud runtimes only, and can't replace a built-in provider (a folder provider called `claude-code-cloud` is an error). Deployments use them by name, like any runtime: `runtimes: [acme-runner]`.

Folder providers can run code (`api`) with your runtime credentials during `apply`. Imports are pinned by commit and `fabrica upgrade` shows diffs (calling out provider changes in particular), so new provider code never arrives unseen.

There's a complete, runnable example in [examples/custom-provider](../examples/custom-provider/).

### `provider.yaml`

```yaml
kind: cloud                          # cloud (local is reserved for built-ins in v0.1)
description: Acme's in-house agent runner
apply_modes: [api, handoff]          # tried in order
find_by_identity: true               # the API can list resources and read their identity back
skill_transport: text                # text (read file by file); cloud runtimes can't use directory
mcp_auth:
  header: native                     # can send Authorization: Bearer …
  url: native                        # can take the token in the URL
triggers:
  github.pull_request.opened: native
  schedule: native
connectors:
  github: native
  mcp.http: native
side_effects:
  github.pr_comment: native
  "*": emulated
```

Fabrica reads it strictly, like its other files. If `apply_modes` lists `api`, there must be an executable `api` next to it; if it lists `handoff`, there must be a `handoff.md`.

### The `api` protocol

The protocol is deliberately boring, so you can write a provider in any language. Fabrica runs `api <op>`, writes one JSON request to its stdin, and reads one JSON response from its stdout. A non-zero exit, or an `"error"` field in the response, means the operation failed, and whatever the program printed on stderr is shown to the user. The environment is passed through, so the program can read its credentials from environment variables. Each call times out after two minutes.

Every request has `"version": 1` and `"op"`. The operations are:

| Op | Request | Response |
|---|---|---|
| `list` | `deployment`, `endpoint` | `{"resources": [{"identity", "remote_id", "fingerprint"?}]}`: every resource whose identity starts with the endpoint minus its scheme, plus `#` (like `fabrica.acme.dev/pr-review#`) |
| `create` | `deployment`, `resource` | `{"remote_id": "…"}` |
| `update` | `deployment`, `remote_id`, `resource` | `{}` |
| `delete` | `deployment`, `remote_id` | `{}` |

A `resource` looks like this:

```json
{
  "identity": "fabrica.acme.dev/pr-review#pr-review@github.com/acme/app",
  "deployment": "pr-review",
  "routine": "pr-review",
  "repo": "github.com/acme/app",
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

Your program must store the `identity` somewhere it can read back (a name or a metadata field), so `list` can return it. If it can also store the `fingerprint`, return it from `list`, which helps `plan` spot drift. The `fabrica.token` is a secret: never log it. When `placement` is `url`, the token is already in `fabrica.url`.

### `handoff.md`

`handoff.md` is a Go [text/template](https://pkg.go.dev/text/template). It's filled in once per change, with these fields:

| Field | What it is |
|---|---|
| `.ID` | The handoff's ID, for `fabrica confirm`. |
| `.Action` | `create`, `update` or `delete`. |
| `.Runtime`, `.Deployment`, `.Routine`, `.Repo` | What it's about. |
| `.Identity` | The resource's Fabrica identity. Ask for it to be used as the resource's name. |
| `.RemoteID` | The runtime's own ID, for updates and deletes, when known. |
| `.Trigger.On`, `.Trigger.Cron`, `.Trigger.Description` | The trigger, and a sentence describing it. |
| `.Connectors` | A list, each with `.Name` and `.Setup` (a sentence saying how to set it up). |
| `.Fabrica.URL`, `.Fabrica.Header` | The Fabrica server URL (with the token in it, for URL placement) and the `Authorization` header value (for header placement). |
| `.Prompt`, `.Fingerprint` | The stub prompt to paste in, and its fingerprint. |
| `.ConfirmCommand` | The exact `fabrica confirm …` line to end with. |

There's an extra template function, `indent N text`, which indents every line of the text by N spaces (handy for pasting the prompt inside a list item). Missing fields are errors, so a typo in the template fails loudly. The built-in templates in [internal/provider/builtin](../internal/provider/builtin/) are good starting points.
