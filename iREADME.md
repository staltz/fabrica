# Fabrica

Fabrica lets you write down *how your AI agents build software* (skills, routines and rules) **once**, in a git repo, and run that same setup on many AI coding tools: Claude, Cursor, Codex, GitHub Copilot and more.

Agents fetch your setup live from a small Fabrica server, so improving your process is just a git merge. Every agent run gets recorded, so you can see what happened.

> **Status:** v0.1, early days. The file formats and commands described here work and are tested, but expect rough edges. See [What v0.1 can't do yet](#what-v01-cant-do-yet).

## Why

If your team uses AI coding agents, your "how we work" knowledge probably ends up scattered all over the place:

- The same skills get copied into every repo's `.claude/skills/` folder.
- `AGENTS.md` files mix "how this app works" with "how we write code in general".
- A PR-review bot gets set up by hand in three different tools, and the three slowly drift apart.
- Nobody knows what the agents actually did last week.

So when you improve your process, you have to update it everywhere. And when you switch tools, you start over.

Fabrica treats your agent setup as its own versioned thing, called a **blueprint**, kept apart from the software it builds. Your product repo versions your code. Your blueprint repo versions *how that code gets built*. Deploy a blueprint to some product repos (its **targets**) in some tools, and that deployment is a **factory**.

## How it works

There are three kinds of repo:

| Repo | What's in it |
|---|---|
| **Blueprint repo** | Skills, routines, rules and connectors: your process. It doesn't know who uses it. |
| **Home repo** | Factories: "run *this* blueprint on *these* targets in *these* tools". Targets are product repos. This is the only place blueprints and product repos meet. |
| **Product repo** | Your code. The only Fabrica thing in it is one sentence at the top of its `AGENTS.md`. |

Then:

1. You write a blueprint.
2. You deploy it as a factory in your home repo: which product repos it targets, in which tools.
3. You run `fabrica apply`.

From then on, agents in those tools load your blueprint live over MCP. Fabrica never copies blueprint content into your tools or your product repos. For a routine like "review every new PR", it installs a tiny **stub** that basically says "load your instructions from Fabrica". So when you change a routine's instructions, skills or rules and merge that to git, the next run picks it up. No re-deploy needed.

## Install

Fabrica is a single static binary.

```sh
curl -fsSL https://raw.githubusercontent.com/staltz/fabrica/main/install.sh | bash
```

That puts `fabrica` in `~/.local/bin` (set `FABRICA_INSTALL_DIR` to change it). You also need `git`, since Fabrica runs it to read blueprints and to commit state. Later, `fabrica -u` updates it to the latest release.

Prefer building from source? You'll need Go 1.26 or newer:

```sh
go install github.com/staltz/fabrica/cmd/fabrica@latest
```

## Try it in five minutes (solo, on your laptop)

This sets up your own blueprint for every repo you work on, in Claude Code and Cursor.

**1. Make a blueprint.** Start from the [starter blueprint](examples/starter-blueprint/), which comes built into `fabrica`:

```sh
fabrica new ~/code/my-blueprint
cd ~/code/my-blueprint
fabrica check        # should say: No problems found.
git add . && git commit -m "My blueprint"
```

Push it somewhere git can reach, like `github.com/you/my-blueprint`.

**2. Add a factory to your personal home repo** at `~/.fabrica`. Fabrica created it the first time you ran it (a git repo with an empty `factories/` folder), so just add a file:

```sh
cat > ~/.fabrica/factories/my-default.yaml <<'EOF'
blueprint: { git: github.com/you/my-blueprint, ref: main }
runtimes: [claude-code-local, cursor-local]
targets: ["*"]               # every repo not matched by another local factory
EOF
```

**3. Apply it.** This adds a user-level `fabrica` MCP server to Claude Code and Cursor. It never touches your product repos.

```sh
fabrica plan     # see what it will do
fabrica apply
```

**4. Add the Fabrica line to a product repo.** Make this the very first line of its root `AGENTS.md` (and of its root `CLAUDE.md`, if it has one):

```
Before starting, load this repo's working rules from the Fabrica MCP server; if you don't have it, ignore this line.
```

**5. Open the repo in Claude Code or Cursor.** The agent calls `run_begin`, loads your rules with `rules_get`, and loads skills as it needs them. Every session gets recorded in `~/.fabrica/runs.db`.

The full walkthrough, including a team setup with a cloud PR reviewer, is in [docs/getting-started.md](docs/getting-started.md).

## Commands

| Command | What it does |
|---|---|
| `fabrica new <folder>` | Starts a new blueprint repo, from the starter blueprint or (with `--empty`) from scratch. |
| `fabrica check [folder]` | Lints a blueprint, validates a home repo's factories, or checks a product repo. It works out which one the folder is. |
| `fabrica lock` | Pins a blueprint's imports to exact commits, in `fabrica.lock`. |
| `fabrica upgrade [alias…]` | Moves imports to newer commits, showing a diff first. |
| `fabrica plan` | Shows what `apply` would do and what's blocking it. Exits `0` when in sync, `2` with changes, `1` with errors. |
| `fabrica apply` | Installs and updates stubs in runtimes, and sets up your local tools. |
| `fabrica destroy [factory…]` | The opposite of `apply`: removes everything it installed, and leaves your factory files alone. Asks first. |
| `fabrica confirm <id>` | Closes a handoff once someone has followed its instructions. |
| `fabrica unlock <factory>` | Removes a lock left behind by a crashed apply. |
| `fabrica mcp [--http]` | Serves a blueprint over MCP: locally over stdio, or remotely over HTTP. |
| `fabrica -u` | Updates Fabrica itself to the latest release. |

Every command has `--help`. The full reference is in [docs/cli.md](docs/cli.md).

## Documentation

- [Getting started](docs/getting-started.md): a hands-on tutorial, solo and team.
- [Concepts](docs/concepts.md): blueprints, home repos, stubs, shapes and runs, explained.
- [Writing a blueprint](docs/blueprints.md): the blueprint format in full (skills, routines, rules, connectors, imports, expects).
- [Home repos](docs/home-repos.md): factories, state, the apply lock, handoffs and CI.
- [Product repos](docs/product-repos.md): the Fabrica line, repo skills and what stays out of product repos.
- [The MCP server](docs/mcp-server.md): the tools agents use, and how to run the remote server.
- [Providers](docs/providers.md): what each runtime supports, and how to write your own provider.
- [Run history](docs/run-history.md): the SQLite schema and some handy queries.
- [Safety checks](docs/safety-checks.md): every way Fabrica fails closed, and where it catches each problem.
- [CLI reference](docs/cli.md): every command and flag.
- [Architecture](docs/architecture.md): how the code is organized, for contributors.

Examples live in [examples/](examples/): a [starter blueprint](examples/starter-blueprint/), a [home repo](examples/home/), a [product repo](examples/product-repo/) and a [custom provider](examples/custom-provider/).

## What v0.1 can't do yet

Fabrica is honest about its limits. Here's what v0.1 can't do yet:

- **Most built-in cloud providers use handoffs.** As of October 2026, Claude routines, Codex and Copilot automations have no API for creating triggered automations. So for those, `fabrica apply` writes step-by-step instructions (a **handoff**) for you, or an AI with computer use, to follow, and `fabrica confirm` records the result. Cursor automations are the exception: with `CURSOR_API_KEY` set, Fabrica manages them through Cursor's API (the one its Terraform provider uses), apart from a one-time setup of their MCP servers. If you write your own provider with an API, Fabrica drives it directly. See [docs/providers.md](docs/providers.md).
- **Cloud routines can only use text-only skills.** A skill with scripts or binary files needs its folder on disk, which only works locally for now.
- **There's no command to read run history yet.** Open the SQLite file with any SQLite tool.
- **No hosted service, ever.** You run the remote server yourself.

The design doc, [DESIGN.md](DESIGN.md), has the full list of what's planned for later.

## Contributing

You need Go 1.26 or newer and `git`. `go test ./...` runs everything, including end-to-end tests that build little git worlds in temp folders, and `make lint` runs `go vet`, `staticcheck` and `gofmt`. [docs/architecture.md](docs/architecture.md) explains how the code is organized and how the tests work.

## License

MIT. See [LICENSE](LICENSE).
