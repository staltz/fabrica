# CLI reference

Fabrica is one binary, `fabrica`. It sits beside git and never wraps it: blueprint and home repos are plain git repos that you edit and review with your usual tools. The CLI only ever writes to git in one place, which is committing and pushing state in a home repo. (`fabrica new` runs `git init` for a new blueprint, but leaves the first commit to you. Likewise, every command first creates your personal home repo if it's missing, without committing; see [Home repos](home-repos.md#personal-and-team-home-repos).)

Every command has `--help`.

## Choosing the home repo

`auth`, `plan`, `apply`, `destroy`, `confirm`, `unlock` and `check` (on a home or product repo) work on one home repo. They pick it like this:

1. the `--home <folder>` flag, if you give one,
2. otherwise the current folder (or the git repo it's in), if it has `factories/` or `state/`,
3. otherwise your personal home repo: `$FABRICA_HOME`, or `~/.fabrica`.

## Environment variables

| Variable | What it's for |
|---|---|
| `FABRICA_HOME` | Where your personal home repo lives. Default `~/.fabrica`. Every command creates it there if it's missing. |
| `FABRICA_CACHE` | Fabrica's cache: git clones, unpacked commits and skills, handoff files, hook markers, and the clones and logs of init sessions started on this machine. Default `$XDG_CACHE_HOME/fabrica`, or `~/.cache/fabrica`. |
| `FABRICA_SERVER_SECRET` | The server secret, for every server. Needed by `fabrica mcp --http`, and by `fabrica apply` for cloud factories (`fabrica destroy` only deletes, so it doesn't need it). `fabrica plan` uses it, when set, to notice rotated tokens. At least 32 characters. Without it, Fabrica uses the secret `fabrica auth login` saved for each factory's server. See [Credentials](auth.md). |
| `FABRICA_ADMIN_TOKEN` | A server's admin token, for people who can read its run history but don't have its server secret. `fabrica runs --server` sends it to the server's admin API. Without it, Fabrica makes the admin token from the server secret, if it has one. [`fabrica auth admin-token`](#fabrica-auth-admin-token-server-url) prints it. See [Credentials](auth.md#the-admin-token). |
| `CURSOR_API_KEY` | A Cursor API key. With it, `plan`, `apply` and `destroy` manage Cursor automations (`cursor-cloud`) through Cursor's API; without it, `cursor-cloud` uses handoffs. `CURSOR_TOKEN`, the name Cursor's Terraform provider uses, works too. Without either, Fabrica uses the key `fabrica auth login` saved. See [Cursor automations through Cursor's API](providers.md#cursor-automations-through-cursors-api). |
| `CURSOR_ENDPOINT` | The address of Cursor's API. Default `https://api2.cursor.sh`. |
| `FABRICA_KEYCHAIN` | Set to `off` to keep the credentials `fabrica auth login` saves in a file only you can read, `$XDG_CONFIG_HOME/fabrica/credentials.json` (or `~/.config/fabrica/`), even where there's a system keychain. |
| `BROWSER` | The program `fabrica auth login` opens pages with. By default it's `open` on macOS, and `xdg-open` on Linux when there's a display. |
| `CURSOR_AGENTS_ENDPOINT` | The address of Cursor's Cloud Agents API, which starts [init sessions](providers.md#init-sessions) on `cursor-cloud`. Default `https://api.cursor.com`. |
| `FABRICA_CLAUDE_BIN` | The `claude` command that starts [init sessions](providers.md#init-sessions) on `claude-code-local`. Default `claude`, from your `PATH`. |
| `FABRICA_BIN` | The path local tools should run as `fabrica`. By default Fabrica uses the `fabrica` on your `PATH` if it's the same program, otherwise its own path. |
| `CI` | When set, `plan`, `apply` and `destroy` skip local factories, since CI has nothing local to set up. |

An environment variable that holds a credential always wins over one `fabrica auth login` saved, even when it's set to nothing: `CURSOR_API_KEY= fabrica plan` shows what happens without your saved key.

Fabrica runs `git` with interactive password prompts switched off, since it often runs where nobody can type one. Set up a credential helper or SSH keys instead; `fabrica auth login git` does it for GitHub, with the GitHub CLI.

## `fabrica new <folder>`

Starts a new blueprint repo. It writes a blueprint into the folder (creating it if needed), and runs `git init` there.

The blueprint is a copy of the [starter blueprint](../examples/starter-blueprint/), built into the binary, so you don't need a copy of Fabrica's repo: five skills, three routines, four rules and two connectors, ready to use and yours to change. With `--empty`, you get a blueprint with no items yet: a `fabrica.yaml` with every key commented out, and a README that explains the layout.

| Flag | What it does |
|---|---|
| `--empty` | Start from an empty blueprint instead of the starter blueprint. |
| `--no-git` | Don't run `git init`. |

New never overwrites a file. If the folder already has any file the blueprint would write, it writes nothing and exits `1`. The one exception is a `README.md`, which it keeps, since a repo made on a git host often starts with one. So `git clone` an empty repo from your git host, then run `fabrica new` on the clone.

When the folder is inside a git repo already, like a subfolder of a monorepo, `new` doesn't run `git init`, and suggests a factory with the matching `path:`. It never commits: look the files over, run `fabrica check`, and commit them yourself.

## `fabrica check [folder]`

Checks a folder for problems. It works out what the folder is:

- **A blueprint** (it has a `fabrica.yaml`): lints every item and import, and warns about likely mistakes.
- **A home repo** (it has `factories/` or `state/`): validates every factory against its blueprint and the runtimes' capability matrices, and checks each deployed product repo's default branch.
- **Anything else is a product repo:** checks the Fabrica line, skill clashes and unmet `expects:`, against the blueprint that serves it.

| Flag | What it does |
|---|---|
| `--blueprint <folder or git[@ref]>` | For a product repo, the blueprint to check against. Without it, Fabrica uses the blueprints of the factories in your home repo that cover this repo. |

Exits `1` when there are errors, `0` otherwise (warnings don't fail it). It works well as a pre-commit hook in a blueprint repo, or as a CI step.

## `fabrica lock [blueprint-folder]`

Pins every import to an exact commit, in `fabrica.lock`. Imports that are already pinned keep their commit. It shows what changed for newly pinned imports, and only writes the lock when the blueprint has no errors with it. Exits `1` on errors.

## `fabrica upgrade [import-alias…]`

Moves imports (all, or the ones you name) to the newest commit of their ref, and shows a diff of every item that was added, changed or removed. It calls out changes to items you override, and new provider code. Then it asks before writing `fabrica.lock`.

| Flag | What it does |
|---|---|
| `--dir <folder>` | The blueprint folder. Default `.`. |
| `--yes`, `-y` | Write the lock without asking. Without a terminal to ask on, upgrade refuses unless you pass this. |
| `--dry-run` | Only show what would change. |

## `fabrica auth`

Shows every credential your home repo's factories need for `apply`, and checks each one with the service it's for: the secret of each Fabrica server (the server says whether it accepts tokens made with it), each runtime API's credentials (like a Cursor API key), and git access to the home repo, the blueprints and the product repos. Exits `1` when something apply needs is missing or refused. [Credentials](auth.md) explains it all.

| Flag | What it does |
|---|---|
| `--no-check` | Only say what's set, without asking servers, runtime APIs or git whether it works. |

`fabrica auth status` does the same.

### `fabrica auth login [credential…]`

Sets up every credential that's missing or refused, or the ones you name: a server's URL, an environment variable (like `CURSOR_API_KEY`), a runtime (like `cursor-cloud`), or `git`. For a server secret, you paste the server's (Fabrica checks it with the server), or press Enter to make a new one. For a runtime credential, you press Enter to open the page where you make one, and paste it (Fabrica checks it with the runtime where it can). For git, it signs you in to GitHub with the GitHub CLI and lets git use that. Credentials are saved in your system's keychain, or else in a file only you can read. What you paste isn't shown. Without a terminal, answers are read from stdin, one per line.

### `fabrica auth logout <credential…>`

Forgets saved credentials: name a server's URL, an environment variable or a runtime. It leaves environment variables alone.

### `fabrica auth env [credential…]`

Prints the credentials the home repo needs (or the ones you name) as `NAME=value` lines, for your server or CI:

```sh
fabrica auth env | gh secret set --env-file - --repo acme/home
fabrica auth env https://fabrica.acme.dev > fabrica.env
```

Credentials that aren't set up are left out, and the ones you named are an error. A server secret comes out as `FABRICA_SERVER_SECRET`, so when factories use several servers with different secrets, name one.

### `fabrica auth admin-token [server-url]`

Prints a server's admin token, which lets people read its run history through the server's [admin API](mcp-server.md#the-admin-api) and nothing else. It's computed from the server secret (`FABRICA_SERVER_SECRET`, or the one `fabrica auth login` saved for that server), so rotating the secret rotates it too. Only the token is printed, so it pipes well:

```sh
fabrica auth admin-token https://fabrica.acme.dev
fabrica auth admin-token | pbcopy
```

Without a URL, it uses the only server your home repo's cloud factories use. With several, it lists them and asks you to name one. Give the token to people who should read run history, and never to agents. They can set it as `FABRICA_ADMIN_TOKEN`. See [Credentials](auth.md#the-admin-token).

## `fabrica plan [factory…]`

Shows what `apply` would do for every factory (or the ones you name), and what's blocking it: creates, updates and deletes with reasons, how each runtime handles each trigger, connector and side effect, each runtime's apply mode, follow versus frozen, pending handoffs, unverifiable resources, and errors. For your personal home repo, it also shows the local tool changes.

It reads the home repo as it is on disk, and never changes anything.

| Flag | What it does |
|---|---|
| `--json` | Print the plan as JSON. |
| `--no-refresh` | Trust state, instead of asking runtime APIs (folder providers with an `api`) what exists. |
| `--destroy` | Show what [`destroy`](#fabrica-destroy-factory) would remove, instead of what `apply` would do. |

| Exit code | Meaning |
|---|---|
| `0` | Everything is in sync. |
| `2` | There are only changes, for `apply` to make. |
| `1` | There are errors. |

## `fabrica apply [factory…]`

Makes runtimes match your home repo. For each cloud factory with changes and no errors, it syncs the home repo, takes the factory's lock, refreshes by identity, re-plans, applies each change (through the runtime's API, or by writing a handoff), and records the results and releases the lock in one commit. On runtimes whose API can't list resources, like Cursor's, it commits state before each create and again once it has the new ID. A create whose answer is lost is not retried; `plan` reports it, and `fabrica confirm` says whether the resource exists. When a runtime's API can't set up everything its resources rely on (Cursor automations' MCP servers), apply writes what to set up by hand next to the handoffs, and lists it at the end. When it creates a factory whose blueprint has an [init routine](blueprints.md#init-routines) (the first time, or again after `fabrica destroy`), it starts the init's session after the stubs, once, on the first of its runtimes that's available (or writes a handoff for it). For your personal home repo, it also writes the local tools' configs.

| Flag | What it does |
|---|---|
| `--handoff-dir <folder>` | Where to write handoff instructions. Default `$FABRICA_CACHE/handoffs`. |

Cloud factories need the secret of their server, and Cursor automations are only managed through Cursor's API with a Cursor API key (otherwise they get handoffs): `fabrica auth login` sets both up, or set `FABRICA_SERVER_SECRET` and `CURSOR_API_KEY`. Apply refuses to run with uncommitted changes in `factories/` or `state/`, or with local commits that aren't pushed yet. Exits `1` if any factory was skipped because of errors, or failed.

## `fabrica destroy [factory…]`

The opposite of `apply`: removes everything your factories (or the ones you name) installed. It leaves the factory files alone, so the next `apply` puts it all back, and runs the factory's [init routine](blueprints.md#init-routines) again, since it recreates the factory. To stop deploying something for good, delete its factory file and `apply` instead.

- **Cloud factories:** it deletes every resource in the factory's state, the same way `apply` deletes a stub that's no longer deployed: through the runtime's API, or with a delete handoff to follow and then `fabrica confirm`. A create handoff that was never confirmed gets a delete handoff too, in case someone followed it anyway. Like `apply`, it takes the factory's lock and records the results in one commit. Until a delete handoff is done, its stub can still run, because the server serves a factory for as long as its file exists.
- **Local tools,** for your personal home repo: it removes the `fabrica` MCP server, connector servers, hooks and scheduled tasks that `apply` installed. When you name local factories and other local factories still use the same tool, only the named ones' hooks and scheduled tasks go. The shared `fabrica` server stays, so it keeps serving their blueprints until you delete their files.

It shows the plan, then asks before changing anything. `fabrica plan --destroy` shows the same plan and changes nothing.

| Flag | What it does |
|---|---|
| `--yes`, `-y` | Destroy without asking. Without a terminal to ask on, destroy refuses unless you pass this. |
| `--handoff-dir <folder>` | Where to write delete handoffs. Default `$FABRICA_CACHE/handoffs`. |

Deleting needs no token, so unlike `apply`, destroy works without a server secret. Like `apply`, it refuses to run with uncommitted changes in `factories/` or `state/`, or with local commits that aren't pushed yet, and exits `1` if any factory was skipped because of errors, or failed.

## `fabrica confirm <handoff-id>`

Closes a pending handoff once someone has followed its instructions. That includes the handoff for a factory's [init session](home-repos.md#init): confirm its ID (like `i_3f2a1c9e04`), with `--remote-id` set to the session's ID or URL, once the session is running.

| Flag | What it does |
|---|---|
| `--remote-id <id>` | The runtime's own ID for the resource you created or updated. Without one, a handoff's resource is tracked as unverifiable. Not allowed for deletes. For an API create whose ID was never recorded, this is how you tell Fabrica the resource does exist. |
| `--absent` | The unfinished API create did not happen (you looked, and it isn't there). The next apply may create it. Not for handoffs, and not together with `--remote-id`. |

Confirm commits and pushes the factory's state, and refuses while an apply holds its lock.

## `fabrica unlock <factory>`

Removes a lock left behind by a crashed apply. Only use it when you're sure that apply is gone. The next apply adopts whatever the crashed one created. A create on a runtime that can't list resources, whose ID was never recorded, is not adopted: `plan` reports it, and apply won't retry it until you confirm whether it exists.

## `fabrica runs`

Reads [run history](run-history.md): the routine firings and editor sessions that agents recorded with `run_begin`. Agents write run history and only people read it, so these commands are for you, and no tool or command of Fabrica's hands history to an agent.

By default it reads `runs.db` in your personal home repo (or in the folder you give with `--home`), which holds the sessions of your local tools. With `--server` it reads a remote Fabrica server instead. Local and remote history are never mixed. If there's no `runs.db` yet, it says so and doesn't make one.

`list` and `export` choose runs with the same filters, and `show` takes one run:

| Flag | What it does |
|---|---|
| `--factory <name>` | Only runs of this factory. |
| `--routine <name>` | Only runs of this routine. `-` means interactive runs, the ones with no routine. |
| `--repo <repo>` | Only runs on this repo, like `github.com/acme/app`. |
| `--runtime <name>` | Only runs on this runtime, like `claude-code-cloud`. |
| `--outcome <outcome>` | Only runs with this outcome: `succeeded`, `failed`, `in_progress` or `stale`. Repeat the flag, or separate them with commas, for several. |
| `--since <when>` | Only runs that started since then: a number of days or hours back, like `7d` or `48h`, or an RFC 3339 time. Default `7d`. |
| `--limit <n>` | The most runs to read. Default `50` for `list` and `200` for `export`. |
| `--server <server>` | Read a remote server instead; see [below](#reading-a-remote-server). |

### `fabrica runs`, `fabrica runs list`

Lists runs, newest first:

```
$ fabrica runs --since 3d
STARTED       OUTCOME    ROUTINE    REPO      INTENT                RUN
Oct 17 09:41  succeeded  pr-review  acme/app  Review PR 412 for t…  run_06gmjeps
Oct 17 08:02  stale      —          acme/app  Fix flaky checkout …  run_06gmj9xq
Oct 16 22:15  failed     autofix    acme/api  Fix lint failure on…  run_06gmh2aa

3 runs from factory acme. Show one with: fabrica runs show <run>
```

Times are in your time zone. `—` means a run with no routine: someone working in their editor. `REPO` leaves out the host. `RUN` is enough of the run's ID to tell it apart from the other runs in the list, and at least 12 characters, so it's what you give to `show`.

Lines are at most 80 characters, so long text is cut with `…`, the intent first. The `FACTORY` column only shows up when the runs come from more than one factory, since there isn't room for it otherwise; when they come from one, the line under the table says which. When there's little room left, the gaps between columns shrink too. Use `--json` for everything, uncut.

| Flag | What it does |
|---|---|
| `--json` | Print the list as JSON, the same as the server's [admin API](mcp-server.md#the-admin-api) answers: `{"schema_version": 3, "runs": [...], "next": "..."}`. `next` is only there when there are more runs than `--limit`. |

When no run matches, it says so and suggests a wider `--since`. That's not an error, and exits `0`.

### `fabrica runs show <run>`

Shows one run: what it was for, then its timeline of everything that happened, in time order.

```
$ fabrica runs show run_06gmjeps
Run run_06gmjeps8jf8zfjjrfmydev50g: succeeded
  Intent     Review PR 412 for the billing retry change
  Factory    acme (served remotely), blueprint ee8f142
  Routine    pr-review on claude-code-cloud, repo github.com/acme/app
  Session    https://claude.ai/code/session_01Abc
  Started    Oct 17 09:41:02, last heard from 09:58:40 (17m later)

09:41:05  loaded routine pr-review
09:41:06  loaded rules product-knowledge-only, commit-messages
09:41:09  loaded skill pr-review
09:47:30  progress   Read the diff and the retry tests
09:52:11  progress   Found the retry backoff settings
09:52:11  friction   navigation: couldn't find where retry backoff is configured
09:58:40  succeeded  Posted review with two blocking comments
          → https://github.com/acme/app/pull/412

See the instructions this run was served:
  git -C <your blueprint clone> show ee8f142:routines/pr-review.md
```

`<run>` is the whole run ID or the start of it, like the `RUN` column shows. If a start fits more than one run, it lists them and exits `1`; if it fits none, it says so. Consecutive loads of the same kind are one line. Fabrica's own checkpoints, like the refusal of an outdated stub, are marked `fabrica`, so they stand out from the agent's. A run that goes past midnight gets a line with the new date. Anything an agent wrote is printed with its line breaks and control characters turned into spaces, so it can't mess with your terminal.

The last lines say how to read the instructions the run was served. The blueprint's commit is recorded with every run, so `git show` in a clone of your blueprint prints the exact routine as the agent saw it. A routine's file is `routines/<name>.md`; an interactive run points at the skills and rules it loaded instead. If the run's factory is in your home repo and its blueprint has a `path:`, the command has it in front. Without that factory, Fabrica can't know, and says to put the blueprint's folder in front if it lives in a subfolder.

| Flag | What it does |
|---|---|
| `--json` | Print the run as JSON, the same as the admin API's `GET /_admin/runs/<run_id>`. |

### `fabrica runs export`

Writes a [retro bundle](run-history.md#the-retro-bundle) for the runs that match: a folder of Markdown and JSON that you hand to a person, or to your own retro skill (Fabrica doesn't ship one).

```
$ fabrica runs export --routine pr-review --since 14d
Exported 23 runs (17 succeeded, 4 failed, 2 stale) to ./fabrica-runs-20261017-1004/
```

| Flag | What it does |
|---|---|
| `--out <folder>` | Where to write the bundle. Default `./fabrica-runs-<YYYYMMDD-HHMM>/`, from the local time. It must not exist, or be empty: Fabrica never writes into a folder that has files in it. |

When no run matches, it says so and writes nothing. When more runs match than `--limit`, the bundle has the newest ones, and it says so, in its README too.

### Reading a remote server

`--server` takes the server's URL, or the name of a cloud factory in your home repo, in which case it's that factory's `server:`. A plain `http://` address only works for `localhost` and `127.0.0.1`, because the admin token goes with every request.

```sh
fabrica runs --server https://fabrica.acme.dev --outcome failed
fabrica runs show run_06gmjeps --server acme
fabrica runs export --server acme --routine pr-review --out ./retro
```

Fabrica reads the server's [admin API](mcp-server.md#the-admin-api) with its admin token, which it finds like this:

1. `FABRICA_ADMIN_TOKEN`, if it's set (even to nothing, which is an error).
2. Otherwise the admin token made from the server secret: `FABRICA_SERVER_SECRET`, or the one `fabrica auth login` saved for that server.

With neither, it says to run `fabrica auth login <server>`, or set `FABRICA_ADMIN_TOKEN`. A token the server refuses gets a message that says where the token came from. See [Credentials](auth.md#the-admin-token).

`list` and `export` keep asking for pages until they have `--limit` runs, and `export` then reads each run in full, four at a time at most. `show` finds a run from the start of its ID by asking the server for the runs around it, so a short start works there too.

## `fabrica dashboard`

Shows your run history as a web page: runs per day, how each routine is doing, what Fabrica turned away, what slowed agents down, and the PRs they touched. It reads `runs.db` in your personal home repo, and serves it on `127.0.0.1` only, so nobody else on your network can reach it.

```
$ fabrica dashboard
Run history from /Users/you/.fabrica/runs.db

Open this link in your browser (it works once):

  http://127.0.0.1:52817/_dashboard?code=9f2c41d07b6e83a5c0d2e1f47a98b3c6

Press Ctrl-C to stop.
```

Open the link. The code in it works once: it trades itself for a cookie, and your browser goes on to the plain address `http://127.0.0.1:52817/_dashboard`, which stays open in that browser until you stop the dashboard. Anyone else who visits without the code gets a page that says to run `fabrica dashboard` again, and so does the link when you use it a second time. To look from another browser, stop it and run it again.

| Flag | What it does |
|---|---|
| `--port <number>` | The port to listen on. Without it, Fabrica picks a free one. |
| `--open` | Open the link in your browser, the way `fabrica auth login` opens pages (`$BROWSER`, `open` on macOS, `xdg-open` on Linux). When it can't, it says so and you open the link yourself. |
| `--home <folder>` | The home repo whose `runs.db` to show. Default: your personal home repo. |

Press Ctrl-C to stop it. If there's no `runs.db` yet, it says so, and doesn't make one: Fabrica starts it the first time an agent calls `run_begin` in a local tool. The dashboard only reads, and changes nothing.

It's the same dashboard that the remote server has at `<server>/_dashboard`, where you log in with the admin token instead. [The MCP server](mcp-server.md#the-dashboard) explains that, and [Run history](run-history.md#the-dashboard) explains what's on each page.

## `fabrica mcp`

Serves a blueprint over MCP.

**Local mode** (without `--http`) serves one session over stdio. Local tools run it; `fabrica apply` registers it as `fabrica mcp --runtime <tool> --home <your home repo>`.

| Flag | What it does |
|---|---|
| `--runtime <name>` | Required. The tool this server runs in: `claude-code-local`, `cursor-local`, `codex-cli` or `copilot-local`. |
| `--workspace <folder>` | Serve this folder, instead of asking the tool for its workspace. |
| `--home <folder>` | Your personal home repo. |

**Remote mode** (`--http`) serves cloud runtimes. See [running the remote server](mcp-server.md#running-the-remote-server). It also has a read-only [admin API](mcp-server.md#the-admin-api) and a [dashboard](mcp-server.md#the-dashboard) for people to read run history, which take the admin token from `fabrica auth admin-token`.

| Flag | Default | What it does |
|---|---|---|
| `--home <git URL or path>` | (required) | The home repo to serve. |
| `--ref <branch>` | the default branch | The home repo branch to serve. |
| `--url <url>` | | Only serve factories whose `server:` is this URL. Without `FABRICA_SERVER_SECRET`, the server uses the secret `fabrica auth login` saved for this URL. |
| `--listen <address>` | `:8080` | Where to listen. |
| `--db <file>` | `fabrica-runs.db` | The run-history database. |
| `--refresh <duration>` | `1m` | How often to re-fetch. |

## `fabrica version`

Prints the version, and the commit it was built from when known. `fabrica --version` and `fabrica -v` do the same.

```
$ fabrica version
fabrica 2.0.0 (b79bb17 2026-10-17)
```

Builds between releases say `dev` instead of a version number.

## `fabrica -u`, `fabrica --update`

Updates Fabrica itself to the latest release on GitHub. It downloads the same files [install.sh](../install.sh) does: the static binary for your OS and CPU, which it checks against the release's `checksums.txt` before putting it in place of the running `fabrica`. When `fabrica` is a symlink, the file it points at gets replaced and the symlink stays.

If you're already on the latest release, it says so and downloads nothing. If the download or the checksum check fails, your current binary is left as it was. When `fabrica` lives in a folder you can't write to, like `/usr/local/bin`, run the update as a user who can (`sudo fabrica -u`). A `fabrica` that came with Fabrica.app (one inside a `.app` bundle, even through a symlink) refuses to update itself: update the app, which brings its own.

Not to be confused with [`fabrica upgrade`](#fabrica-upgrade-import-alias), which moves a blueprint's imports.

## `fabrica hook` (plumbing)

Local tools run this for local routines; you won't need to run it yourself. `fabrica apply` installs hooks like `fabrica hook --runtime claude-code-local --factory oss-work --routine review-before-push --stub 7f3a9c7f3a9c --event git.pre_push`. The command doubles as Fabrica's identifying mark, which is how Fabrica finds its own hooks again.

`fabrica apply` also installs one session pointer hook for each local tool that has a session-start event: `fabrica hook session --runtime claude-code-local`, and the same for `codex-cli` and `copilot-local` (Cursor has no session-start hook). It isn't tied to a routine. When a session starts in a repo one of your local factories covers, it prints one line telling the agent where its own transcript lives, so the agent can pass that as `session` to `run_begin`: the transcript's path if the tool sends one, otherwise `<runtime>:<session id>`, like `copilot-local:<session id>`. If the tool sends neither, it prints nothing. Like the routine hooks, it also prints nothing, and exits `0`, when the folder isn't a git repo, no local factory covers it on that runtime, or the repo doesn't have the Fabrica line. `plan` shows it like any other hook change, applying again leaves it alone, and `destroy` removes it along with the last local factory that uses the tool.
