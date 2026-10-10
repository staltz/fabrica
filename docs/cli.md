# CLI reference

Fabrica is one binary, `fabrica`. It sits beside git and never wraps it: factory and home repos are plain git repos that you edit and review with your usual tools. The CLI only ever writes to git in one place, which is committing and pushing state in a home repo.

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
| `FABRICA_DOWNLOAD_URL` | A mirror for `self-update` (and `install.sh`) to download from instead of GitHub releases: a folder with one release's `fabrica-<os>-<arch>` binaries and `checksums.txt`. |
| `CI` | When set, `plan` and `apply` skip local deployments, since CI has nothing local to set up. |

Fabrica runs `git` with interactive password prompts switched off, since it often runs where nobody can type one. Set up a credential helper or SSH keys instead.

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

## `fabrica self-update`

Updates the `fabrica` binary to the latest release. It downloads the same files as `install.sh` (the binary for your OS and CPU, and `checksums.txt`), checks the binary's checksum, and runs it once to make sure it works on your machine. Only then does it replace the old binary, in one atomic rename, at the same path. If anything goes wrong, your `fabrica` stays exactly as it was.

The path matters because `fabrica apply` writes it into your local tools' configs, so they keep working across updates without a re-apply. When `fabrica` is a symlink, the file it points to is replaced and the link stays. Fabrica processes that are already running, like the MCP servers your AI tools started, keep the old version until they restart.

It never downgrades on its own. If you're running something newer than the latest release (like a build from source), it says so and stops.

| Flag | What it does |
|---|---|
| `--version <tag>` | Install this release, like `v0.2.0`, even if it's older than yours. |
| `--check` | Only say whether there's an update. Exits `0` when you're up to date, `2` when there's an update, `1` on errors. |

Set `FABRICA_DOWNLOAD_URL` to update from a mirror instead of GitHub. Then it installs whatever release the mirror has, if its binary is different from yours.

If `fabrica` is installed somewhere you can't write to, like `/usr/local/bin`, run it with `sudo`. In the container image, rebuild the image instead.

Fabrica v0.1.0 doesn't have `self-update`. To update from v0.1.0, run the install script again.

## `fabrica version`

Prints the version.

## `fabrica hook` (plumbing)

Local tools run this for local routines; you won't need to run it yourself. `fabrica apply` installs hooks like `fabrica hook --runtime claude-code --deployment oss-work --routine review-before-push --stub 7f3a9c7f3a9c --event git.pre_push`. The command doubles as Fabrica's identifying mark, which is how Fabrica finds its own hooks again.
