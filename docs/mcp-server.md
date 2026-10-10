# The MCP server

MCP is the **only** way factory content reaches agents. There's no copying files into repos and no git-fetch fallback. One binary, two modes:

| | `fabrica mcp` (local) | `fabrica mcp --http` (remote) |
|---|---|---|
| Used by | Claude Code, Cursor IDE and Codex CLI on your machine | Cloud runtimes: Claude routines, Cursor automations, Codex |
| Transport | stdio | Streamable HTTP |
| Picks the factory from | Your personal home repo's local deployments | One home repo's cloud deployments |
| Run history and memories in | `runs.db` in your personal home repo (`~/.fabrica/runs.db`) | Its own SQLite database (`--db`) |

Each MCP session is served from **one factory commit**, fixed when the session starts. A merge that lands mid-run never changes what that run sees, and `factory_sha` in run history is exact.

## The tools

| Tool | What it does |
|---|---|
| `run_begin(intent, routine?, runtime?, repo?, stub?)` | Starts a run and returns a `run_id`. The intent is a summary of the request in at most 20 words. For a routine run, the stub passes the other four values. Refuses routine runs whose stub is outdated or no longer deployed. |
| `run_report(run_id, summary, status?, links?)` | Records a checkpoint: a summary (at most 20 words), a status (`progress`, the default, `succeeded` or `failed`), and links to PRs, issues or commits. |
| `rules_get()` | Every always-on rule in full, plus an index of the other rules (name, description, globs). It always starts with the "product knowledge only" rule. |
| `rule_load(name)` | One rule. |
| `skills_list()` | Names and descriptions of all skills. Remotely, skills that need their folder on disk are marked unavailable, with the reason. Also lists the skills the factory expects the repo to provide. |
| `skill_load(name)` | A skill's `SKILL.md` and its file list. Locally, also the folder where the skill was unpacked. |
| `skill_file(name, path)` | One text file from a skill. |
| `routine_get(name)` | A routine's full definition: its prompt with params filled in, its skills and connectors, its allowed side effects (and how to do each on this runtime), its memory, and the rules. |
| `memory_save(run_id, content)` | Replaces the memory of the run's routine, for its next runs on the same repo (at most 16 KiB of Markdown; empty forgets everything). Only for routines with `memory: true`, on runtimes where Fabrica keeps the memory, and only from the session that began the run. |

Skills over MCP aren't a standard yet, so Fabrica emulates them with these plain tools. The server's MCP instructions, the stub and the Fabrica line all tell the agent to list and load what's relevant, and how relative paths in a skill work. When native skills over MCP arrive, the emulation can go away.

The server rejects an `intent` or `summary` longer than 20 words and asks for a shorter one.

## The local server

`fabrica apply` registers it once per local tool, at user level, as a server called `fabrica` running `fabrica mcp --runtime <tool> --home <your home repo>`. You don't normally run it by hand.

Each time a session starts, it:

1. **Works out the workspace.** It asks the tool for its MCP roots and uses the first one. Roots are deprecated as of MCP protocol 2026-07-28, so for tools that don't send them it falls back to `CLAUDE_PROJECT_DIR` and then the working directory. `--workspace <folder>` overrides all of that.
2. **Works out the repo** from the workspace's git remote: `origin` if there is one, otherwise the only remote. Several remotes and none called `origin` is ambiguous, so it refuses.
3. **Matches a local deployment** for that repo and runtime. A specific repo beats the wildcard. No match, or more than one, means it serves nothing and says why.
4. **Checks the workspace:** the Fabrica line must be at the top of `AGENTS.md` (and `CLAUDE.md`, if there is one), and no repo skill may share a name with a factory skill (unless the factory expects it).
5. **Loads the factory** at the commit its ref points to right now.

When it refuses, it explains why in its MCP instructions, returns an error from `run_begin` (which records the run as `failed`), and returns the same error from the other tools. With a skill clash, rules still load, but `run_begin` and `skill_load` refuse.

### Skills on disk

On `skill_load`, the local server unpacks the skill's whole folder into a read-only cache outside the workspace, like `~/.cache/fabrica/skills/<hash>/<name>/`. Executable bits are kept, and the folder keeps the skill's name. `skill_load` tells the agent where it is, and the agent runs scripts from there, just like a normally installed skill. Nothing is written into the product repo, and every local runtime can use every skill.

Set `FABRICA_CACHE` to move the cache.

## The remote server

You host it yourself: a container or a small VM. There's no official hosted service, and there won't be.

- **It serves exactly what one home repo deploys.** You point it at a home repo, and it serves the factory at the ref of each of that repo's cloud deployments, and nothing else. Adding a factory to the server means merging a deployment into the home repo.
- **One endpoint per deployment**, at the path of the deployment's `server:` URL plus its name: `https://fabrica.acme.dev/pr-review`.
- **It re-fetches regularly** (every minute by default), so merges to followed branches and new deployments show up shortly after they land. If a fetch fails, it keeps serving what it had.
- **Routines only run from stubs.** A `run_begin` that names a routine but leaves out its runtime, repo or stub fingerprint is refused, and so is `routine_get` before a routine run has begun.
- **Cloud runtimes only get text-only skills** in v0.1, since the server can't put files on their disk.

### Auth

Each deployment has a secret token, computed as `HMAC(server secret, deployment name)`. The server checks tokens without storing any, and `fabrica apply`, holding the same secret, computes them and puts them into each runtime's connector.

- The token goes in an `Authorization: Bearer …` header where the runtime allows headers, or in the URL (`…/pr-review/t/<token>`) where it only takes a URL. Each provider's capability matrix says which. Tokens in URLs can leak through logs or screenshots, so headers are used whenever possible.
- A request without the right token gets a `401`. A path that isn't a deployment gets a `404`, and so does a deployment that was deleted from the home repo.
- To rotate every token, change the server secret and re-apply. `fabrica plan` notices rotated tokens (when it has the secret) and lists the affected stubs as updates.

OAuth is planned for later.

### Running the remote server

It needs:

- the server secret, in `FABRICA_SERVER_SECRET` (at least 32 characters; `openssl rand -hex 32` makes a good one),
- read access to the home repo and the factories (Fabrica runs `git`, so give it a credential helper, a deploy key, or a token in the URL),
- a **persistent volume** for its run-history database, which also holds routine memories.

```sh
fabrica mcp --http \
  --home https://github.com/acme/home.git \
  --db /data/runs.db \
  --listen :8080
```

| Flag | Default | What it does |
|---|---|---|
| `--home` | (required) | The home repo: a git URL, or a path to a git repo. |
| `--ref` | the default branch | The home repo branch to serve. |
| `--url` | | Only serve deployments whose `server:` is this URL. Handy when one home repo points deployments at several servers, like staging and production. |
| `--listen` | `:8080` | The address to listen on. |
| `--db` | `fabrica-runs.db` | The run-history database. Keep it on a persistent volume. |
| `--refresh` | `1m` | How often to re-fetch the home repo and factories. |

`GET /healthz` answers `200` with the home commit being served. It answers `503` before the first successful fetch, and when the latest fetch failed (the server keeps serving what it had, but you'll want to know).

With Docker, using the [Dockerfile](../Dockerfile) in this repo:

```sh
docker build -t fabrica .
docker run -d --name fabrica \
  -e FABRICA_SERVER_SECRET="$(cat secret.txt)" \
  -v fabrica-data:/data \
  -p 8080:8080 \
  fabrica mcp --http --home https://github.com/acme/home.git
```

The image keeps its database and git cache in `/data`.

Put it behind HTTPS (a reverse proxy like Caddy or nginx, or your platform's load balancer) at the URL your deployments use as `server:`. The server checks a token on every request, so it works fine behind a proxy that connects over localhost.

The server uses MCP's streamable HTTP transport with sessions, because each session is pinned to one factory commit and links its skill loads to its run. Clients that ask for the newer, sessionless protocol (2026-07-28) are told which versions it speaks and fall back to one of those.

## Stale stubs

Before a routine run starts, `run_begin` checks that its stub is still current. Both servers check two things, in this order:

1. **The stub is still deployed.** In the latest home repo, the deployment being served (remotely, the endpoint's; locally, the one matching the workspace) must still include the stub's routine, repo and runtime.
2. **The stub's shape still matches.** The server works out the routine's shape at the factory commit it's about to serve, and compares its fingerprint with the stub's.

If either check fails, the server refuses: `run_begin` returns an error saying which check failed and that the deployment needs `fabrica apply`, the run is recorded as `failed`, and `routine_get` won't serve that routine in that session.

Both checks compare the latest home repo with the values inside the stub, never with state. So there's no gap while an apply is running, after one crashes, or while a handoff is pending. v0.1 refuses outdated stubs instead of serving the last compatible version: that's the stricter choice, and switching to holding back later wouldn't break anyone.
