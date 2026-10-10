# CLI reference

Fabrica is one binary, `fabrica`. It sits beside git and never wraps it: factory and home repos are plain git repos that you edit and review with your usual tools. The CLI only ever writes to git in one place, which is committing and pushing state in a home repo. (`fabrica new` runs `git init` for a new factory, but leaves the first commit to you.)

Every command has `--help`.

## Choosing the home repo

`plan`, `apply`, `confirm`, `unlock` and `check` (on a home or product repo) work on one home repo. They pick it like this:

1. the `--home <folder>` flag, if you give one,
2. otherwise the current folder (or the git repo it's in), if it has `deployments/` or `state/`,
3. otherwise your personal home repo: `$FABRICA_HOME`, or `~/.fabrica`.

## Environment variables

| Variable | What it's for |
|---|---|
| `FABRICA_HOME` | Where your personal home repo lives. Default `~/.fabrica`. |
| `FABRICA_CACHE` | Fabrica's cache: git clones, unpacked commits and skills, handoff files and hook markers. Default `$XDG_CACHE_HOME/fabrica`, or `~/.cache/fabrica`. |
| `FABRICA_SERVER_SECRET` | The server secret. Needed by `fabrica mcp --http`, and by `fabrica apply` for cloud deployments. `fabrica plan` uses it, when set, to notice rotated tokens. At least 32 characters. |
| `FABRICA_BIN` | The path local tools should run as `fabrica`. By default Fabrica uses the `fabrica` on your `PATH` if it's the same program, otherwise its own path. |
| `CI` | When set, `plan` and `apply` skip local deployments, since CI has nothing local to set up. |

Fabrica runs `git` with interactive password prompts switched off, since it often runs where nobody can type one. Set up a credential helper or SSH keys instead.

## `fabrica new <folder>`

Starts a new factory repo. It writes a factory into the folder (creating it if needed), and runs `git init` there.

The factory is a copy of the [starter factory](../examples/starter-factory/), built into the binary, so you don't need a copy of Fabrica's repo: five skills, three routines, four rules and two connectors, ready to use and yours to change. With `--empty`, you get a factory with no items yet: a `fabrica.yaml` with every key commented out, and a README that explains the layout.

| Flag | What it does |
|---|---|
| `--empty` | Start from an empty factory instead of the starter factory. |
| `--no-git` | Don't run `git init`. |

New never overwrites a file. If the folder already has any file the factory would write, it writes nothing and exits `1`. The one exception is a `README.md`, which it keeps, since a repo made on a git host often starts with one. So `git clone` an empty repo from your git host, then run `fabrica new` on the clone.

When the folder is inside a git repo already, like a subfolder of a monorepo, `new` doesn't run `git init`, and suggests a deployment with the matching `path:`. It never commits: look the files over, run `fabrica check`, and commit them yourself.

## `fabrica check [folder]`

Checks a folder for problems. It works out what the folder is:

- **A factory** (it has a `fabrica.yaml`): lints every item and import, and warns about likely mistakes.
- **A home repo** (it has `deployments/` or `state/`): validates every deployment against its factory and the runtimes' capability matrices, and checks each deployed product repo's default branch.
- **Anything else is a product repo:** checks the Fabrica line, skill clashes and unmet `expects:`, against the factory that serves it.

| Flag | What it does |
|---|---|
| `--factory <folder or git[@ref]>` | For a product repo, the factory to check against. Without it, Fabrica uses the factories of the deployments in your home repo that cover this repo. |

Exits `1` when there are errors, `0` otherwise (warnings don't fail it). It works well as a pre-commit hook in a factory repo, or as a CI step.

## `fabrica lock [factory-folder]`

Pins every import to an exact commit, in `fabrica.lock`. Imports that are already pinned keep their commit. It shows what changed for newly pinned imports, and only writes the lock when the factory has no errors with it. Exits `1` on errors.

## `fabrica upgrade [import-alias…]`

Moves imports (all, or the ones you name) to the newest commit of their ref, and shows a diff of every item that was added, changed or removed. It calls out changes to items you override, and new provider code. Then it asks before writing `fabrica.lock`.

| Flag | What it does |
|---|---|
| `--dir <folder>` | The factory folder. Default `.`. |
| `--yes`, `-y` | Write the lock without asking. Without a terminal to ask on, upgrade refuses unless you pass this. |
| `--dry-run` | Only show what would change. |

## `fabrica plan [deployment…]`

Shows what `apply` would do for every deployment (or the ones you name), and what's blocking it: creates, updates and deletes with reasons, how each runtime handles each trigger, connector and side effect, each runtime's apply mode, follow versus frozen, pending handoffs, unverifiable resources, and errors. For your personal home repo, it also shows the local tool changes.

It reads the home repo as it is on disk, and never changes anything.

| Flag | What it does |
|---|---|
| `--json` | Print the plan as JSON. |
| `--no-refresh` | Trust state, instead of asking runtime APIs (folder providers with an `api`) what exists. |

| Exit code | Meaning |
|---|---|
| `0` | Everything is in sync. |
| `2` | There are only changes, for `apply` to make. |
| `1` | There are errors. |

## `fabrica apply [deployment…]`

Makes runtimes match your home repo. For each cloud deployment with changes and no errors, it syncs the home repo, takes the deployment's lock, refreshes by identity, re-plans, applies each change (through the runtime's API, or by writing a handoff), and records the results and releases the lock in one commit. For your personal home repo, it also writes the local tools' configs.

| Flag | What it does |
|---|---|
| `--handoff-dir <folder>` | Where to write handoff instructions. Default `$FABRICA_CACHE/handoffs`. |

Cloud deployments need `FABRICA_SERVER_SECRET`. Apply refuses to run with uncommitted changes in `deployments/` or `state/`, or with local commits that aren't pushed yet. Exits `1` if any deployment was skipped because of errors, or failed.

## `fabrica confirm <handoff-id>`

Closes a pending handoff once someone has followed its instructions.

| Flag | What it does |
|---|---|
| `--remote-id <id>` | The runtime's own ID for the resource you created or updated. Without one, the resource is tracked as unverifiable. Not allowed for deletes. |

Confirm commits and pushes the deployment's state, and refuses while an apply holds its lock.

## `fabrica unlock <deployment>`

Removes a lock left behind by a crashed apply. Only use it when you're sure that apply is gone. The next apply adopts whatever the crashed one created.

## `fabrica mcp`

Serves a factory over MCP.

**Local mode** (without `--http`) serves one session over stdio. Local tools run it; `fabrica apply` registers it as `fabrica mcp --runtime <tool> --home <your home repo>`.

| Flag | What it does |
|---|---|
| `--runtime <name>` | Required. The tool this server runs in: `claude-code`, `cursor-ide` or `codex-cli`. |
| `--workspace <folder>` | Serve this folder, instead of asking the tool for its workspace. |
| `--home <folder>` | Your personal home repo. |

**Remote mode** (`--http`) serves cloud runtimes. See [running the remote server](mcp-server.md#running-the-remote-server).

| Flag | Default | What it does |
|---|---|---|
| `--home <git URL or path>` | (required) | The home repo to serve. |
| `--ref <branch>` | the default branch | The home repo branch to serve. |
| `--url <url>` | | Only serve deployments whose `server:` is this URL. |
| `--listen <address>` | `:8080` | Where to listen. |
| `--db <file>` | `fabrica-runs.db` | The run-history database. |
| `--refresh <duration>` | `1m` | How often to re-fetch. |

## `fabrica version`

Prints the version, and the commit it was built from when known. `fabrica --version` and `fabrica -v` do the same.

```
$ fabrica version
fabrica 0.1.0 (b79bb17 2026-10-10)
```

Builds between releases say `dev` instead of a version number.

## `fabrica hook` (plumbing)

Local tools run this for local routines; you won't need to run it yourself. `fabrica apply` installs hooks like `fabrica hook --runtime claude-code --deployment oss-work --routine review-before-push --stub 7f3a9c7f3a9c --event git.pre_push`. The command doubles as Fabrica's identifying mark, which is how Fabrica finds its own hooks again.
