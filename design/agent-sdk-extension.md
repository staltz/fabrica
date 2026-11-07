# Design: a Fabrica extension for Agent SDK agents

**Status:** proposal. Not built. Internal; nothing in the manual links here.

**Builds on:** blueprint agents and the `cursor-agents` runtime ([manual/agents.md](../manual/agents.md)), which deploy Agent SDK projects but leave them outside the factory plane.

## The problem

Today a blueprint agent is deployed by Fabrica and then forgotten by it:

- **No run history.** Its sessions never call `run_begin` or `run_report`, so `fabrica runs` and the dashboard can't see it. Cursor's Deployed Agents page is the only place to look.
- **No shared process.** The blueprint's skills and rules are served to every routine, but an agent only has its own `bot/skills/` and `instructions.md`. A team that writes its review checklist as a blueprint skill has to copy it into each agent.
- **No stale check.** A routine's stub is refused by `run_begin` once it's no longer deployed, or its shape changed. A deployment Fabrica forgot keeps running, until an apply deletes it.

## The idea

Ship an Agent SDK **extension**, as an npm package. An agent opts in by mounting it, which is one file in the Agent SDK's own convention:

```ts
// agents/pr-reviewer/bot/extensions/fabrica.ts
import fabrica from "@staltz/fabrica-agent-sdk";

export default fabrica({
  skills: ["pr-review", "changelog"], // blueprint skills this agent may load
  rules: true,                        // the blueprint's always-on rules
  runs: true,                         // record sessions in run history
});
```

The agent stays a normal Agent SDK project: it validates, runs and evals without Fabrica (each feature turns itself off when `FABRICA_URL` isn't set, with a warning). The extension namespaces everything it adds as `fabrica__<name>`, so it can't collide with the agent's own tools and skills.

## What the extension contributes

### 1. A connection to the factory's Fabrica server

A host connection named `fabrica`, built with `defineConnection`:

```ts
{ url: "${FABRICA_URL}", headers: { authorization: "Bearer ${FABRICA_TOKEN}" } }
```

Templates resolve each time the connection opens, so they work with hosted secrets. A host connection keeps the raw Fabrica tools away from the model: the extension's own tools and hooks call it through `ctx.host.mcp`.

Apply already knows both values. In `cursor-agents`' deploy, it would:

- set `FABRICA_URL` (the factory's endpoint), `FABRICA_TOKEN` (the factory's token) and `FABRICA_AGENT` (the agent's identity and fingerprint, like `acme#agents/pr-reviewer@ad922b8ae182`) as deployment secrets, with `agent-sdk secret set`, next to the agent's own;
- set `FABRICA_URL` in the environment of `agent-sdk deploy` itself, so the extension's `hosting()` can add the server's host to the deployment's egress list. `hosting()` runs on the machine that deploys, at load time.

`fabrica check` can't read the mount. Apply can: it already installs the agent's dependencies, and `agent-sdk info --json` lists each mounted extension with its contributions. So apply learns which agents mount the extension, and only those get the Fabrica secrets. Agents that don't mount it are unchanged.

### 2. Run history, recorded by code

Hooks are observe-only and run in the serving process for every session, which makes them a good fit for run history. Unlike routines, this wouldn't be an honor system:

| Event | What the hook does |
|---|---|
| A session's first `message.received` | `run_begin`, with the agent's identity and fingerprint from `FABRICA_AGENT`, and `session` set to the Agent SDK session ID. It keeps the `run_id` in `ctx.host.kv`, under the session's ID. |
| `turn.completed` | `run_report`, status `progress`, with the turn's token usage and cost. On a `task`-mode session, a completed turn is the end, so `succeeded`. |
| `turn.failed` | `run_report`, status `failed` (a `cancelled` status is a cancellation, not a failure). |

It skips eval sessions (`ctx.session.purpose === "eval"`), and gives every call a timeout, since a slow hook holds the session's later events back.

The **intent** is the open question here. `run_begin` wants a casual summary of the request, at most 20 words, and a hook can't summarize. Options:

- (a) a fixed description from the channel, like "GitHub pull request event" or "Slack mention in #eng";
- (b) a `fabrica__run_intent` tool the model calls once, with instructions saying to, plus (a) as a fallback when it doesn't;
- (c) the first 20 words of the message, which puts untrusted text into run history.

(a) with (b) on top looks right. (c) shouldn't happen.

**On the server side**, agent runs are a new kind of run:

- `run_begin` takes `agent` instead of `routine`, `runtime` and `stub`. The stale check becomes: the factory still deploys this agent on `cursor-agents`, and the fingerprint matches the agent's at the commit the server serves. That stops a deployment Fabrica forgot, or one that's stale, at its next session.
- The run-history schema gains an `agent` column: bump `runs.SchemaVersion`, add the migration, and keep `run_overview` compatible.
- `fabrica runs`, the dashboard and the retro bundle show agent runs next to routine runs.

One catch: the fingerprint excludes the commit, so a blueprint merge that doesn't touch the agent leaves it in sync. But with a followed ref, the server serves a newer commit. The check compares the agent's content hash, not the commit, so that works out.

### 3. Blueprint skills, served live

The Agent SDK advertises skills by description and loads them on demand, from `/agent/skills`. The extension can contribute one skill per name in `skills:`, as `fabrica__<name>`:

- Its description comes from the blueprint, fetched when the extension loads (`skills_list`).
- Its body is a short pointer: "Call `fabrica__skill_load` with `<name>`." That tool, which the extension also contributes, loads the live `SKILL.md` and file list through `ctx.host.mcp`. A `fabrica__skill_file` tool reads the skill's other files.

So the descriptions are fixed for the life of the deployment (until the next deploy), but the content is live, like it is for routines. The other way, `defineAgent({ skills: (ctx) => FileSystem })`, would mount the blueprint's skills as files. But it replaces the agent's whole skills tree, and it's agent config, which an extension can't set.

Skills with scripts or binary files would only work as far as `skill_file` goes: there's no folder on disk, the same limit cloud routines have.

### 4. Rules

Two choices, and the trade-off is about eval reproducibility:

- **Live:** the extension's instructions say to call `fabrica__rules_get` first, every turn. That's always current, but it costs a tool call per turn, and evals then depend on the server's content.
- **At load:** the extension fetches `rules_get` once when it loads, and appends the text to its instructions (extension instructions follow the root agent's). That's free per turn, and fixed until the next deploy or restart, like the skill descriptions.

**At load** looks better for agents: it's how the rest of an agent behaves, and apply redeploys when the agent changes anyway. That leaves one gap: a rule change alone doesn't redeploy anything. So either the fingerprint covers the blueprint's always-on rules for agents that mount the extension with `rules: true`, or we accept that rule changes reach agents only at their next deploy. The first is more honest; it makes `plan` show an update.

### 5. Memory

Leave it out. The Agent SDK has its own memory (a journal hook, `ctx.host.kv` and `ctx.host.files`), which survives redeploys and is per agent. Fabrica's emulated memory exists for runtimes that have none. An agent mixing both would have two memories with different rules.

## Evals

An agent's evals should keep working offline. In an eval session, the extension runs from a snapshot instead of the server: a `fabrica.snapshot.json` the blueprint's CI writes with a `fabrica` command that doesn't exist yet. Without one, it turns itself off. That keeps hillclimbing reproducible, and keeps eval runs out of run history.

## Security

- The factory token goes in a deployment secret, never in source. It's the same token the factory's routines use, with the same reach: read the blueprint, write run history. A rotated server secret is already an update in `plan`, and that update would set the new token.
- Agents read untrusted input: PR diffs, Slack messages, webhook bodies. The Fabrica tools the extension gives the model are read-only (`skill_load`, `skill_file`, `run_intent`). Run history is written by hooks, not by the model, and memory stays out.
- The extension is trusted host code in the agent's process, like every extension. Its package is public, and its version is pinned by each agent's lockfile.

## Rollout

1. The server: agent runs in `run_begin` and `run_report`, the schema migration, and show them in `fabrica runs` and the dashboard.
2. Apply: read `agent-sdk info --json` after installing, detect the mount, and set the `FABRICA_*` secrets (and `FABRICA_URL` for the deploy).
3. The extension, with `runs` only. Use it in a dogfood agent for a while.
4. Then `skills` and `rules`.
5. Then eval snapshots.

## Open questions

- The package's name and home: in this repo (a `js/` folder, released with Fabrica) or its own repo? Its version has to keep up with the server's MCP tools, which argues for this repo.
- Should `run_begin` refuse a session whose agent was removed from the factory, or only record it as failed? Refusing is consistent with stubs. But a hook can't stop the turn (hooks are observe-only), so refusing would only stop the recording. Stopping the agent itself would need a tool the model is told to call first, or a channel wrapper.
- What intent to record, (a) or (a) with (b).
- Whether rule changes should redeploy agents (the fingerprint question above).
- Self-hosted agents (`agent-sdk serve`) would use the same extension. They'd need a runtime of their own, which is a separate design.
