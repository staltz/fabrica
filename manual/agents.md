# Agents

An **agent** is a [Cursor Agent SDK](https://cursor.com/docs/agent-sdk) project that lives in a blueprint, next to its skills and routines. Fabrica deploys it to Cursor's hosting for each factory that wants it, from the blueprint's own git repo, at the commit the factory resolved.

You write the agent the way the Agent SDK docs say to: a `package.json`, a `bot/` folder of Markdown and TypeScript, and evals beside it. Fabrica adds nothing to it. The agent's own channels, schedules, tools, skills and `agent.ts` decide what it does, when it wakes up, and which model it runs on.

## Agents or routines?

They solve the same kind of problem in two different ways, and a blueprint can have both.

| | Routine | Agent |
|---|---|---|
| **You write** | A trigger, skills, connectors and a prompt, in Fabrica's format | An Agent SDK project, in the Agent SDK's format |
| **Where it runs** | Any runtime with the triggers it needs: Claude, Cursor, Codex, Copilot, or locally | Cursor's hosting, with the `cursor-agents` runtime |
| **What wakes it up** | Its `trigger:` | Its own channels (GitHub, Slack, webhooks…) and schedules |
| **Updates** | Served live: a merge takes effect on the next run | Built from a commit: a merge takes effect after the next `fabrica apply` |
| **Run history** | Every run, in `fabrica runs` and the dashboard | Not yet: see Cursor's [Deployed Agents](https://cursor.com/dashboard/deployed-agents) |

Reach for an agent when you want code around model judgment (typed tools, deterministic hooks, evals you can run and hillclimb), and for a routine when a prompt and some skills are enough, or when it has to run in several tools.

## In a blueprint

Each agent is a folder under `agents/`, holding one Agent SDK project, exactly as `agent-sdk init` made it:

```
fabrica.yaml
agents/
  pr-reviewer/
    package.json            # depends on @grok/agent-sdk
    package-lock.json
    bot/
      agent.ts
      instructions.md
      tools/inspect_pr.ts
      channels/github.ts
    evals/
      evals.config.ts
```

The folder's name is the agent's name, and follows the same rule as every other item: lowercase letters, digits and single hyphens. Inside the folder, the Agent SDK's own rules apply, and Fabrica doesn't read the TypeScript. `fabrica check` looks only at what a deploy needs:

- `package.json` depends on `@grok/agent-sdk`. One that depends on the SDK's former names, `@cursor/bdk` or `@cursor/july`, is an error that says what to switch to.
- There's a `bot/` folder (or `agent/`, for older projects) with `instructions.md`, `instructions.ts` or an `instructions/` folder.
- There's a lockfile (`package-lock.json`, `npm-shrinkwrap.json`, `pnpm-lock.yaml` or `yarn.lock`), in the agent's folder or a parent of it up to the repo's root (for an npm workspace).
- No `node_modules/` is committed, and there are no symlinks. A `node_modules/` that `npm install` left in your working folder is fine: git doesn't have it, and Fabrica leaves it out.

Run `agent-sdk validate --dir agents/pr-reviewer` yourself for everything else, and the agent's evals in your blueprint's CI.

## Importing agents

Agents come in through [imports](blueprints.md#imports) like every other item, with `only:`, `exclude:`, collisions and overrides as usual. A blueprint that imports another blueprint gets its `agents/`.

A plain repo, one without a `fabrica.yaml`, brings its Agent SDK projects too, so an agent that lives in its own repo needs no changes:

- **A repo (or `path:`) that is one project** comes in named after its `package.json`, without the scope: `@acme/pr-reviewer` is `agents/pr-reviewer`.
- **A folder of projects**, like the Agent SDK's multi-agent layout, brings each subfolder that is a project, named after the folder.

```yaml
imports:
  reviewer:
    git: github.com/acme/pr-reviewer     # the repo is one Agent SDK project
    ref: main
  fleet:
    git: github.com/acme/agents
    ref: v2
    path: agents                         # a folder of projects
    only: [agents/weather]
```

Either way, Cursor builds the agent from where it lives: the import's own repo, at the commit `fabrica.lock` pinned, in its own folder. Nothing is copied.

## In a factory

Add the `cursor-agents` runtime to a cloud factory, and it deploys the blueprint's agents:

```yaml
# factories/acme-bots.yaml
blueprint: { git: github.com/acme/blueprint, ref: stable }
server: https://fabrica.acme.dev
alias: acme
runtimes: [cursor-cloud, cursor-agents]
routines: [pr-review]          # run on cursor-cloud
agents: [pr-reviewer]          # deployed on cursor-agents
targets: [github.com/acme/app, github.com/acme/api]
```

- **`agents:`** lists the agents to deploy, or `["*"]` for all of them. Leave it out, and a factory with `cursor-agents` deploys every agent of its blueprint. It's an error on a factory without a runtime that deploys agents, and on local factories.
- **No routine runs on `cursor-agents`.** A factory of only agents needs no `routines:`, and a routine can't name `cursor-agents` in its `runtimes:`.
- **One deployment per agent, per factory**, watching all of the factory's targets: GitHub repos as `owner/name`, and GitLab and Bitbucket repos by their full names. A target on any other host is an error. The deployment's slug is the factory's tag and the agent's name, like `acme_pr-reviewer`, at most 64 characters, so pick a short `alias:` if they're long. Its identity in state is `acme#agents/pr-reviewer`.
- **The targets don't need the Fabrica line** when the factory deploys only agents, since agents don't read it. They still need it for the factory's routines.
- **`server:` is still required**, like for every cloud factory. Agents don't talk to the Fabrica server yet.

## What apply does

`fabrica apply` deploys each agent through the [`agent-sdk` command line](https://cursor.com/docs/agent-sdk/reference/cli.md), the documented way to deploy:

1. **It copies the agent** (from the folder with its lockfile down) into a scratch folder, and installs its dependencies there from the lockfile, with `npm ci --ignore-scripts` (or pnpm's or yarn's equivalent). `agent-sdk deploy` loads the project on the machine that deploys it, so it needs them.
2. **It reads the secrets the agent declares**, in `hosting.secretNames`, with `agent-sdk info --view manifest`.
3. **It deploys**: `agent-sdk deploy --slug acme_pr-reviewer --repo https://github.com/acme/blueprint --ref <commit> --path agents/pr-reviewer --cursor-events-repo acme/app --cursor-events-repo acme/api`. Cursor builds that commit from git, and apply waits until the deployment runs.
4. **It sets each secret the deployment doesn't have yet**, from apply's own environment, by name: with `LINEAR_TOKEN` set where `fabrica apply` runs, it runs `agent-sdk secret set acme_pr-reviewer LINEAR_TOKEN` with the value on stdin. Secret values never go in a blueprint, a home repo or state.
5. **It keeps the alias token** Cursor shows on a slug's first deploy, which it never shows again, in `~/.cache/fabrica/handoffs/<factory>/<slug>-alias-token`, readable only by you. You need it to call the deployment's [routes](https://cursor.com/docs/agent-sdk/deployment.md#send-work-to-the-deployment).

What apply needs, wherever it runs:

- A **Cursor team with agent hosting** turned on, and a team **service-account key with agent access**, in `CURSOR_API_KEY` (or `CURSOR_SERVICE_ACCOUNT_KEY`, or saved with `fabrica auth login cursor-agents`). A personal Cursor plan can't deploy hosted agents.
- **Node 22.13 or newer, the `agent-sdk` command line** (`npm install --global @grok/agent-sdk`, or point `FABRICA_AGENT_SDK_BIN` at one), and the package manager of the agent's lockfile.
- **A repo Cursor can clone**: the blueprint (or the import that brings the agent) at an `https://` git URL, with the commit pushed. A blueprint at a local path can be checked, but not deployed.

Without the key or the command line, `cursor-agents` uses [handoffs](home-repos.md#handoffs) instead: each one lists the exact commands to deploy, update or delete the agent, ending with `fabrica confirm`.

> [!WARNING]
> Deploying runs the agent's code on the machine that runs `fabrica apply`: `agent-sdk` loads its TypeScript, with your Cursor key in the environment. Install scripts don't run, but treat an agent like a [folder provider](providers.md#folder-providers): review what your blueprint imports before you deploy it.

### When agents are redeployed

A deployment is built from one commit, so it doesn't follow a branch by itself: a merge to the blueprint reaches an agent at the next `fabrica apply` (run it in CI after merges, like the [team setup](getting-started.md) does). But only what the agent is made of counts:

- **Its files changed** (anything in its folder): `plan` shows an update, and apply redeploys it from the new commit.
- **Something else in the blueprint changed**: nothing to do. The agent keeps the commit it was built from.
- **The factory's targets changed**: apply replaces the repos it watches with `agent-sdk deployment update`, without a new build.
- **It moved** (another repo or folder): apply redeploys it.

### Finding deployments again

Fabrica finds its deployments by listing them (`agent-sdk deployment list`): a slug like `acme_pr-reviewer` says which factory and agent it's for. So a deploy that failed, or an apply that crashed before recording one, is adopted by the next apply and deployed again, never duplicated. A deployment deleted by hand is created again.

### Secrets apply doesn't have

An agent deploys without its secrets; it fails only when it needs one. When apply's environment lacks some, apply says so in `~/.cache/fabrica/handoffs/<factory>/cursor-agents-setup.md`, `plan` keeps reminding you, and `fabrica auth` lists every secret each agent declared. Set them with `agent-sdk secret set <slug> <NAME>` (the agent's next task uses them, without a redeploy), or put them in apply's environment, and the agent's next deploy sets them.

## Removing an agent

Taking an agent out of a factory (or the blueprint), removing `cursor-agents`, deleting the factory or running `fabrica destroy` deletes its deployment, with `agent-sdk deployment delete`. **That can't be undone**: it also deletes the deployment's hosted sessions, its KV store (`ctx.host.kv`), its files (`ctx.host.files`) and its eval runs. So `plan` flags every such delete with a warning, and `destroy` asks first. Copy out anything you want to keep before you apply.

## Not yet

- **Agents don't talk to Fabrica.** They don't record runs, load the blueprint's skills and rules, or use Fabrica's memory; they use the Agent SDK's own `skills/`, `instructions.md` and `ctx.host.kv`.
- **Only Cursor's hosting.** There's no runtime for self-hosting agents with `agent-sdk serve` yet.
- **Fabrica doesn't see hand edits.** A deployment someone redeployed by hand from another commit looks in sync, since Cursor doesn't say which commit a deployment runs. `fabrica apply` puts it back only when the agent changes.
