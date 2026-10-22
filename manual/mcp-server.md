# The MCP server

MCP is the **only** way blueprint content reaches agents. There's no copying files into repos and no git-fetch fallback. One binary, two modes:

| | `fabrica mcp` (local) | `fabrica mcp --http` (remote) |
|---|---|---|
| Used by | Claude Code, Cursor IDE, Codex CLI and Copilot CLI on your machine | Cloud runtimes: Claude routines, Cursor automations, Codex, Copilot automations |
| Transport | stdio | Streamable HTTP |
| Picks the blueprint from | Your personal home repo's local factories | One home repo's cloud factories |
| Run history and memories in | `runs.db` in your personal home repo (`~/.fabrica/runs.db`) | Its own SQLite database (`--db`) |

Each MCP session is served from **one blueprint commit**, fixed when the session starts. A merge that lands mid-run never changes what that run sees, and `blueprint_sha` in run history is exact.

## The tools

| Tool | What it does |
|---|---|
| `run_begin(intent, routine?, runtime?, repo?, stub?, session?)` | Starts a run and returns a `run_id` and a hint for what to do next. The intent is a summary of the request in at most 20 words. For a routine run, the stub (or, for an [init session](blueprints.md#init-routines), the prompt it was started with) passes `routine`, `runtime`, `repo` and `stub`. Refuses routine runs whose stub is outdated or no longer deployed. `session` is optional and says where the session's transcript lives: a file path, a session ID or a session URL (at most 512 bytes, on one line). A bad one is an error, and the run isn't recorded, so the agent can try again. |
| `run_report(run_id, summary, status?, links?, friction?)` | Records a checkpoint: a summary (at most 20 words), a status (`progress`, the default, `succeeded` or `failed`), and links to PRs, issues or commits. `friction` is optional: up to 5 notes about what slowed the agent down since its last report, each with a kind (`navigation`, `checks`, `standards`, `tools`, `info` or `other`) and a note of at most 20 words. It answers with an acknowledgement and nothing else. Only accepts a run that was begun for this factory (and, on the local server, this repo), from any session; for any other `run_id`, whether it exists or not, it gives the same error. |
| `rules_get()` | Every always-on rule in full, plus an index of the other rules (name, description, globs). It always starts with the "product knowledge only" rule. |
| `rule_load(name)` | One rule. |
| `skills_list()` | Names and descriptions of all skills. Remotely, skills that need their folder on disk are marked unavailable, with the reason. Also lists the skills the blueprint expects the repo to provide. |
| `skill_load(name)` | A skill's `SKILL.md` and its file list. Locally, also the folder where the skill was unpacked. |
| `skill_file(name, path)` | One text file from a skill. |
| `routine_get(name)` | A routine's full definition: its prompt with params filled in, its skills and connectors, its allowed side effects (and how to do each on this runtime), its memory, and the rules. |
| `memory_save(run_id, content)` | Replaces the memory of the run's routine, for its next runs on the same repo (at most 16 KiB of Markdown; empty forgets everything). Only for routines with `memory: true`, on runtimes where Fabrica keeps the memory, and only from the session that began the run. |

Skills over MCP aren't a standard yet, so Fabrica emulates them with these plain tools. The server's MCP instructions, the stub and the Fabrica line all tell the agent to list and load what's relevant, and how relative paths in a skill work. When native skills over MCP arrive, the emulation can go away.

The server rejects an `intent`, `summary` or friction note longer than 20 words and asks for a shorter one.

After `run_begin`, the server also records every skill, rule and routine the session loads (`skill_load`, `rule_load`, `routine_get`, and each always-on rule `rules_get` hands over) as loads of that run. See [Run history](run-history.md).

**No tool returns run history, on purpose.** Agents write run history; only people read it. `run_begin` gives back a `run_id` and a hint, `run_report` an acknowledgement, and neither says anything about earlier runs or checkpoints, not even the agent's own. The one exception is a routine's [memory](blueprints.md#memory), which `routine_get` shows to the routine that saved it. That's a separate feature, not run history.

## The local server

`fabrica apply` registers it once per local tool, at user level, as a server called `fabrica` running `fabrica mcp --runtime <tool> --home <your home repo>`. You don't normally run it by hand.

Each time a session starts, it:

1. **Works out the workspace.** It asks the tool for its MCP roots and uses the first one. Roots are deprecated as of MCP protocol 2026-07-28, so for tools that don't send them it falls back to `CLAUDE_PROJECT_DIR` and then the working directory. `--workspace <folder>` overrides all of that.
2. **Works out the repo** from the workspace's git remote: `origin` if there is one, otherwise the only remote. Several remotes and none called `origin` is ambiguous, so it refuses.
3. **Matches a local factory** for that repo and runtime. A specific repo beats the wildcard. No match, or more than one, means it serves nothing and says why.
4. **Checks the workspace:** the Fabrica line must be at the top of `AGENTS.md` (and `CLAUDE.md`, if there is one), and no repo skill may share a name with a blueprint skill (unless the blueprint expects it).
5. **Loads the blueprint** at the commit its ref points to right now.

When it refuses, it explains why in its MCP instructions, returns an error from `run_begin` (which records the run as `failed`), and returns the same error from the other tools. With a skill clash, rules still load, but `run_begin` and `skill_load` refuse.

### Skills on disk

On `skill_load`, the local server unpacks the skill's whole folder into a read-only cache outside the workspace, like `~/.cache/fabrica/skills/<hash>/<name>/`. Executable bits are kept, and the folder keeps the skill's name. `skill_load` tells the agent where it is, and the agent runs scripts from there, just like a normally installed skill. Nothing is written into the product repo, and every local runtime can use every skill.

Set `FABRICA_CACHE` to move the cache.

## The remote server

You host it yourself: a container or a small VM. There's no official hosted service, and there won't be.

- **It serves exactly what one home repo deploys.** You point it at a home repo, and it serves the blueprint at the ref of each of that repo's cloud factories, and nothing else. Adding a blueprint to the server means merging a factory into the home repo.
- **One endpoint per factory**, at the path of the factory's `server:` URL plus its name: `https://fabrica.acme.dev/pr-review`.
- **It re-fetches regularly** (every minute by default), so merges to followed branches and new factories show up shortly after they land. If a fetch fails, it keeps serving what it had.
- **Routines only run from stubs.** A `run_begin` that names a routine but leaves out its runtime, repo or stub fingerprint is refused, and so is `routine_get` before a routine run has begun.
- **Cloud runtimes only get text-only skills** for now, since the server can't put files on their disk.

### Auth

Each factory has a secret token, computed as `HMAC(server secret, factory name)`. The server checks tokens without storing any, and `fabrica apply`, holding the same secret, computes them and puts them into each runtime's connector.

- The token goes in an `Authorization: Bearer …` header where the runtime allows headers, or in the URL (`…/pr-review/t/<token>`) where it only takes a URL. Each provider's capability matrix says which. Tokens in URLs can leak through logs or screenshots, so headers are used whenever possible.
- A request without the right token gets a `401`. A path that isn't a factory gets a `404`, and so does a factory that was deleted from the home repo.
- To rotate every token, change the server secret and re-apply. `fabrica auth login <server URL>` makes a new one when you press Enter, and `fabrica plan` notices rotated tokens (when it has the secret) and lists the affected stubs as updates.
- `fabrica auth` checks that the secret you have is the server's own, by asking the server: a wrong token gets a `401`, and the right one doesn't. See [Credentials](auth.md).
- Factory tokens only open their own factory's endpoint. Reading run history takes a different token, the admin token, which opens the [admin API](#the-admin-api) and nothing else.

OAuth is planned for later.

### Running the remote server

It needs:

- the server secret, in `FABRICA_SERVER_SECRET` (at least 32 characters). `fabrica auth login <the server's URL>` makes one and saves it on your machine, and `fabrica auth env <the server's URL>` prints it for the server's environment. (`openssl rand -hex 32` makes a good one too.)
- read access to the home repo and the blueprints (Fabrica runs `git`, so give it a credential helper, a deploy key, or a token in the URL),
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
| `--url` | | Only serve factories whose `server:` is this URL. Handy when one home repo points factories at several servers, like staging and production. Without `FABRICA_SERVER_SECRET`, the server uses the secret `fabrica auth login` saved for this URL on its machine. |
| `--listen` | `:8080` | The address to listen on. |
| `--db` | `fabrica-runs.db` | The run-history database. Keep it on a persistent volume. |
| `--refresh` | `1m` | How often to re-fetch the home repo and blueprints. |

`GET /healthz` answers `200` with the home commit being served. It answers `503` before the first successful fetch, and when the latest fetch failed (the server keeps serving what it had, but you'll want to know).

With Docker, using the [Dockerfile](../Dockerfile) in this repo:

```sh
fabrica auth env https://fabrica.acme.dev > fabrica.env    # FABRICA_SERVER_SECRET=…
docker build -t fabrica .
docker run -d --name fabrica \
  --env-file fabrica.env \
  -v fabrica-data:/data \
  -p 8080:8080 \
  fabrica mcp --http --home https://github.com/acme/home.git
rm fabrica.env
```

The image keeps its database and git cache in `/data`.

Put it behind HTTPS (a reverse proxy like Caddy or nginx, or your platform's load balancer) at the URL your factories use as `server:`. The server checks a token on every request, so it works fine behind a proxy that connects over localhost.

The server uses MCP's streamable HTTP transport with sessions, because each session is pinned to one blueprint commit and links its loads to its run. POST responses are JSON, not SSE, so cloud clients that only parse JSON still get `tools/list` after initialize. Clients that ask for the newer, sessionless protocol (2026-07-28) are told which versions it speaks and fall back to one of those.

### The admin API

The remote server also has a small read-only JSON API for people, to read run history. It's on the **operator plane**, the part of Fabrica that people use, and agents never reach it: no MCP tool returns run history, and no factory token opens this.

| Route | What it returns |
|---|---|
| `GET /_admin/runs` | A page of runs, newest first |
| `GET /_admin/runs/<run_id>` | One run in full: the same fields, plus its stub and its timeline of loads, checkpoints and friction notes |

The routes are matched by the last `/_admin/` in the path, so they work under any path prefix: with `server: https://acme.dev/fabrica`, it's `https://acme.dev/fabrica/_admin/runs`. Factory names can't contain an underscore, so this can't clash with a factory's endpoint.

The rules:

- **The token goes in `Authorization: Bearer <admin token>`**, and nowhere else. The `/t/<token>` form that factory endpoints accept isn't allowed here, so the admin token never ends up in a proxy's logs. [`fabrica auth admin-token`](auth.md#the-admin-token) prints it.
- **Anything that isn't the admin token gets a `401`**, with the same message every time. That includes a factory's token, even a valid one.
- **Only `GET`.** Anything else gets a `405`.
- **Every response says `Cache-Control: no-store`.**
- **A bad query value gets a `400`**, with a JSON `{"error": "…"}`. An unknown run gets a `404`.

`GET /_admin/runs` takes these query parameters:

| Parameter | What it does | Default |
|---|---|---|
| `factory` | Only this factory. | all |
| `routine` | Only this routine. `-` means interactive runs, the ones with no routine. | all |
| `repo` | Only this repo, like `github.com/acme/app`. | all |
| `runtime` | Only this runtime. | all |
| `outcome` | Only these outcomes, comma-separated: `succeeded`, `failed`, `in_progress`, `stale`. | all |
| `since` | Only runs that started since then: `7d`, `48h`, or an RFC 3339 time. | `7d` |
| `limit` | How many runs, from 1 to 500. | `100` |
| `before` | A `run_id`: only runs older than it. This is for paging. | none |

```sh
curl -H "Authorization: Bearer $FABRICA_ADMIN_TOKEN" \
  "https://fabrica.acme.dev/_admin/runs?routine=pr-review&outcome=failed,stale&since=14d"
```

```json
{
  "schema_version": 3,
  "runs": [
    {
      "run_id": "run_06gmjeps8jf8zfjjrfmydev50g",
      "started_at": "2026-10-17T09:41:02Z",
      "last_at": "2026-10-17T09:58:40Z",
      "outcome": "succeeded",
      "intent": "Review PR 412 for the billing retry change",
      "routine": "pr-review",
      "runtime": "claude-code-cloud",
      "repo": "github.com/acme/app",
      "factory": "acme",
      "blueprint_sha": "ee8f142…",
      "served_by": "remote",
      "session": "https://claude.ai/code/session_…",
      "last_summary": "Posted review with two blocking comments",
      "checkpoint_count": 3,
      "friction_count": 1,
      "pr_links": ["https://github.com/acme/app/pull/412"]
    }
  ],
  "next": "run_06gm…"
}
```

`schema_version` is the [run-history schema](run-history.md) these fields come from. `next` is the `before` value for the next page, and it's missing on the last page, so to read every run, keep asking with `before` set to the last `next` until there isn't one. Fields that are empty (a run with no `routine`, or no `session`) are left out, except `pr_links`, which is always a list. Times are UTC, to the second.

`GET /_admin/runs/<run_id>` adds `stub` (the stub fingerprint the run claimed, when it claimed one) and a `timeline`, every load, checkpoint and friction note of the run, sorted by time. A friction note has the time of the checkpoint it came with, and comes right after it:

```json
"timeline": [
  {"at": "2026-10-17T09:41:05Z", "type": "load",       "kind": "routine", "name": "pr-review"},
  {"at": "2026-10-17T09:47:30Z", "type": "checkpoint", "author": "agent", "status": "progress", "summary": "Read the diff and the retry tests", "links": []},
  {"at": "2026-10-17T09:47:30Z", "type": "friction",   "kind": "navigation", "note": "couldn't find where retry backoff is configured"}
]
```

### The dashboard

The remote server also serves a dashboard of run history, in your browser, at `/_dashboard`. It's the same dashboard that [`fabrica dashboard`](cli.md#fabrica-dashboard) opens for your local history, and [Run history](run-history.md#the-dashboard) says what's on each page. Like the admin API it's on the operator plane: no factory token opens it, and agents never see it.

It's matched by the last `/_dashboard` in the path, so it works under any path prefix too: with `server: https://acme.dev/fabrica`, it's `https://acme.dev/fabrica/_dashboard`.

- **You log in with the admin token.** Opening `/_dashboard` without the right cookie shows a small page with one password field, "Admin token". Paste what [`fabrica auth admin-token`](auth.md#the-admin-token) prints. A wrong token, and a factory's token too, gets a `401` and the same page again, with the same message.
- **Then a cookie keeps you in.** It's called `fabrica_admin`, and holds the admin token. It's `HttpOnly` and `SameSite=Strict`, so scripts and other sites can't touch it, and it only goes to pages under `<prefix>/_dashboard`. It's `Secure` when the request came over HTTPS, either straight to Fabrica or through a proxy that sends `X-Forwarded-Proto: https`, so make your reverse proxy send that. It's gone when you close the browser, or press "Log out" (a `POST` to `/_dashboard/logout`). Changing the server secret changes the admin token, which logs everyone out.
- **The header doesn't work here.** The admin API takes `Authorization: Bearer`, and the dashboard takes only the cookie. A factory token, in a header, a cookie or the `/t/<token>` form, gets the login page.
- **Everything but logging in and out is a `GET`** that changes nothing, so there's nothing for a cross-site request to abuse.
- **Every page says `Cache-Control: no-store`**, and sends a `Content-Security-Policy` that allows no scripts and nothing from other sites. The pages need no JavaScript and load nothing from the internet.

## Stale stubs

Before a routine run starts, `run_begin` checks that its stub is still current. Both servers check two things, in this order:

1. **The stub is still deployed.** In the latest home repo, the factory being served (remotely, the endpoint's; locally, the one matching the workspace) must still include the stub's routine, repo and runtime.
2. **The stub's shape still matches.** The server works out the routine's shape at the blueprint commit it's about to serve, and compares its fingerprint with the stub's.

An [init session](blueprints.md#init-routines) carries the same four values in the prompt apply started it with, and gets the same two checks, except that "still deployed" means: the factory still runs its init (no `init: false`), the repo is still one of its targets, and the runtime is still one of the init routine's runtimes. Only the remote server serves init sessions, since only cloud factories run them; an init session started on a local runtime, like `claude -p` on your laptop, still connects to the factory's remote server.

If either check fails, the server refuses: `run_begin` returns an error saying which check failed and that the factory needs `fabrica apply`, the run is recorded as `failed`, and `routine_get` won't serve that routine in that session.

Both checks compare the latest home repo with the values inside the stub, never with state. So there's no gap while an apply is running, after one crashes, or while a handoff is pending. Fabrica refuses outdated stubs instead of serving the last compatible version: that's the stricter choice, and switching to holding back later wouldn't break anyone.
