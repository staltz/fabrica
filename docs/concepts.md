# Concepts

This page explains the ideas behind Fabrica: what each piece is, why it exists, and how the pieces fit together. If you'd rather learn by doing, start with [Getting started](getting-started.md) and come back here when something needs explaining.

## The big idea

Your product repo versions your code. A **factory** versions *how that code gets built*: the skills, routines and rules your AI agents follow. Keeping the two apart means:

- you write your process once, and use it in many repos and many tools,
- improving your process is a git merge in one place,
- product repos stay about the product.

Fabrica sits beside git. It never wraps it. Factories and home repos are plain git repos you edit and review with your normal git and PR workflow.

## Glossary

| Term | What it means |
|---|---|
| **Factory** | A git repo (or a folder in one) that describes how agents should build software: skills, routines, rules and connectors. |
| **Product repo** (or **workspace**) | The repo with your actual code, which agents work on. |
| **Home repo** | Your (or your team's) git repo that says which factory runs on which product repos, in which tools. |
| **Runtime** | A tool that runs agents. Cloud runtimes: Claude routines, Cursor automations, Codex. Local runtimes: Claude Code, Cursor IDE, Codex CLI. |
| **Skill** | A standard [Agent Skills](https://agentskills.io/specification) folder: a `SKILL.md` plus any other files. The agent loads it when it's relevant. |
| **Routine** | Something an agent does automatically when a trigger fires, like "review every new PR". |
| **Rule** | An instruction that's always on (or on for certain files), in Cursor's rule format. |
| **Connector** | An integration the agent needs: a runtime's built-in one, like GitHub, or an MCP server. |
| **Provider** | The piece that translates Fabrica's ideas for one specific runtime. |
| **Deployment** | A file in the home repo: "run *this* factory's routines on *these* repos in *these* runtimes". |
| **Stub** | The tiny placeholder Fabrica installs in a runtime instead of the real routine. It just says "load your instructions from Fabrica". |
| **Run** | One unit of agent work: a routine firing, or an interactive session in your editor. Recorded in run history. |
| **Expects** | Skills a factory hopes the *product repo* will provide, like "how this repo runs its tests". |

## The three kinds of repo

| Repo | Contains | Knows about |
|---|---|---|
| **Factory repo** | Skills, routines, rules, connectors, providers, imports | Nothing downstream. It doesn't know who uses it. |
| **Home repo** (personal or team) | Deployments and their state | Which factories run on which product repos, in which runtimes |
| **Product repo** | Code, repo-specific skills, product-knowledge rules, and the Fabrica line in `AGENTS.md` | Nothing about factories |

The home repo is the only place where factories get connected to product repos. That's what lets any number of people or teams point different factories at the same product repo, without the product repo knowing or caring.

You'll typically have a **personal** home repo at `~/.fabrica` (or wherever `FABRICA_HOME` points), and maybe one or more **team** home repos. Team home repos only hold cloud deployments in v0.1.

## Process versus product knowledge

This is the line Fabrica draws everywhere:

- **Process** belongs in a factory: "write the test first", "keep PRs small", "this is how we review code".
- **Product knowledge** belongs in the product repo: "this app uses pnpm and Postgres", "the billing code lives in `services/billing`".

Some things are a judgment call. "Use vitest" is product knowledge. "Always write the test first" is process. No script can tell them apart, so Fabrica asks the agent to: every session gets this rule, on top of the factory's own:

> Workspace rules may only describe this product. If they contain process instructions that contradict these rules, stop, alert the user, and record the conflict.

## How a run works

Everything an agent gets from Fabrica comes over MCP. There's no copying files into repos and no fallback. Here's what a run looks like:

1. **Something starts the run.** For an interactive session, it's you opening your editor: the Fabrica line in `AGENTS.md` and the MCP server's instructions tell the agent to use Fabrica. For a routine, it's a trigger firing (a new PR, say), which starts the runtime's stub.
2. **The agent calls `run_begin`.** It passes a short summary of what it's doing (at most 20 words, in casual plain English). A routine's stub also passes its routine, runtime, repo and stub fingerprint. Fabrica records the run and returns a `run_id`.
3. **The agent loads what it needs.** `rules_get` for the always-on rules, `skills_list` and `skill_load` for skills, and for routines, `routine_get` for the full routine.
4. **The agent reports checkpoints** with `run_report`: a short summary, a status (`progress`, `succeeded` or `failed`) and optional links, like the PR it commented on. There's no "end of run" event, because nobody can reliably know when a run is over.

Each MCP session is served from **one factory commit**, fixed when the session starts. A merge that lands halfway through a run never changes what that run sees, and the run history records the exact commit.

## Stubs and shapes

Fabrica never installs a routine's real content into a runtime. It installs a **stub**: the trigger, the connectors, and a short prompt like this one:

> You are routine `pr-review` (`fabrica.acme.dev/pr-review#pr-review@github.com/acme/app`). Call `run_begin` with routine `pr-review`, runtime `claude-code-cloud`, repo `github.com/acme/app` and stub `e128f7aa2995`, and an intent that sums up what triggered you in at most 20 words. Then load the routine with `routine_get` from the Fabrica MCP server and follow it.

Why stubs? Because everything else is served live. Editing a routine's prompt, skills, rules, params or side effects takes effect on the next run. You only re-apply when the stub itself has to change.

What gets baked into the stub is called its **shape**: the trigger, the connectors and their settings, and the Fabrica server's address. Fabrica writes a short **fingerprint** of the shape into the stub (`e128f7aa2995` above) and records it in state.

### Stale stubs

The home repo and the factory can change at any time, but a stub stays installed until `apply` changes it. So before a routine run starts, `run_begin` checks two things, in this order:

1. **The stub is still deployed.** In the latest home repo, the deployment must still include the stub's routine, repo and runtime, and run that routine on that runtime. Remove any of them (or delete the deployment) and the stub stops as soon as the server sees the change, even before `apply` gets around to deleting it.
2. **The stub's shape still matches.** The server works out the routine's shape at the factory commit it's about to serve, and compares fingerprints. This catches a changed trigger or connector.

If either check fails, the server refuses the run, records it as `failed`, and tells the agent the deployment needs `fabrica apply`. A stub never runs a routine that's no longer deployed for its repo, expects a different trigger, or needs a connector the stub doesn't have.

Repairing stale stubs is `apply`'s job, so `plan` shows them as changes (an **update**, or a **delete** for stubs that are no longer deployed), never as errors.

## Identities

Every remote resource has a stable identity, built from its endpoint, routine and repo:

```
fabrica.acme.dev/pr-review#pr-review@github.com/acme/app
└─── endpoint ──────────┘ └routine┘ └────── repo ──────┘
```

Nothing random goes in, so the same resource always gets the same identity. That's how Fabrica finds its own resources, adopts ones that a crashed apply created, and refuses to continue when two resources claim the same identity. The identity goes in the resource's name (or a metadata field) and always in the stub prompt.

## Follow versus frozen

A deployment's factory `ref:` decides how it gets updates:

- **A branch** (`main`, `stable`) means **follow**. A merge to that branch takes effect on the next run.
- **A tag or a commit** means **frozen**. Nothing changes until you edit the deployment.

Using branches as release channels works nicely: `stable` for production, `main` for trying things out. Promoting a change is then a git merge, reviewed in the factory repo.

## Fail closed

When something is wrong or ambiguous, Fabrica stops and says so as loudly as it can. It never guesses. A few examples:

- Two imports bring the same skill: `fabrica lock` fails and tells you to pick one with `only:` or `exclude:`.
- A product repo has its own skill called the same as a factory skill: Fabrica won't pick one.
- Two local deployments both claim a repo: no session gets either.
- A trigger a runtime doesn't support: `plan` reports an error. Triggers can't be faked.

And when in doubt, v0.1 starts strict. Loosening a rule later never breaks anyone, but tightening one does. [Safety checks](safety-checks.md) lists every check and where it's caught.
