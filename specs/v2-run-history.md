# Spec: reading run history (Fabrica v2)

**Status:** implemented on the `claude/inspiring-noether-91vxyl` branch (all six parts), ready for review.
**Ships in:** Fabrica v2. The run-history database goes from schema version 2 to **schema version 3**. That schema number is separate from Fabrica's own version.
**Who this is for:** anyone building it. You don't need to know Fabrica well already; this spec explains the parts you touch.

## TL;DR

Fabrica v1 records every run in a SQLite database, but gives you no way to look at it. You need `sqlite3`, or the short "Recent Runs" list in Fabrica.app. This spec adds the reading side:

1. **Schema v3.** Agents can say where their transcript lives and what slowed them down. Fabrica records which rules and routines each run loaded, not just skills, and works out an outcome for every run.
2. **Admin token and admin API.** A read-only JSON API on the remote server. Only people with the admin token can use it.
3. **Session pointer hook.** On local tools, a small hook tells the agent its own session ID or transcript path, so the run can point at its transcript.
4. **`fabrica runs`.** CLI commands to list runs, show one run, and export a **retro bundle**: a folder of Markdown and JSON that you hand to your own retro skill.
5. **Dashboard.** One server-rendered web page with charts and tables, local or on the remote server.
6. **Docs.**

And one rule sits above all six:

> **Agents write run history. Only people read it.**

---

## Words we use

| Word | Meaning |
|---|---|
| **Run** | One agent session that called `run_begin`. Either a routine firing (say, "review this PR") or someone working interactively in their editor. One row in `runs`. |
| **Checkpoint** | One `run_report` call: a status (`progress`, `succeeded`, `failed`), a summary of 20 words or fewer, and links. |
| **Load** | One time a run loaded a skill, a rule or a routine through the MCP server. |
| **Friction** | A short note an agent leaves when something slowed it down: it couldn't find a file, a check was missing, a tool was slow, and so on. New in v3. |
| **Outcome** | What we think happened to a run, worked out from its checkpoints: `succeeded`, `failed`, `in_progress` or `stale`. New in v3. |
| **Session pointer** | A string that says where a run's transcript lives: a file path, a session ID or a session URL. New in v3. |
| **Factory plane** | Everything agents touch: the MCP tools, stubs, hooks, rules, skills and factory tokens. |
| **Operator plane** | Everything people touch: the `fabrica` CLI, the dashboard, Fabrica.app and the admin token. |
| **Operator** | You, or whoever runs Fabrica for a team. |
| **Admin token** | A new secret that unlocks the operator plane on a remote server. It's computed from the server secret (see part 2). |
| **Retro bundle** | A folder made by `fabrica runs export`, meant to be read by a person or by a retro skill. |

---

## The big rule: agents write, people read

Agents in a factory are often running on code they don't fully trust: a PR from a stranger, an issue body, a web page. If they could read run history, a prompt injection could make them:

- read what other routines did, on other repos, in other factories
- dig out PR links, intents and summaries that leak private work
- change their behavior based on how past runs went, in ways nobody reviewed

So in v2:

- **No MCP tool returns run history.** Not on the local server, not on the remote one. No `runs_list`, no `run_get`, nothing like them.
- **Not even an agent's own run.** `run_report` answers with an acknowledgement and nothing else. Agents keep their own context; Fabrica doesn't hand earlier checkpoints back.
- **`run_begin` gives back what it gives back today:** a `run_id` and a `next` hint. No counts, no "this routine failed last time", no hints built from history.
- **Routine memory remains the one exception, as in v1.** A routine with `memory: true` reads the notes *it* saved, through `routine_get`. That's a deliberate, separate feature, not run history.
- **Factory tokens can't reach the admin API or the dashboard.** Only the admin token can.

### The one hole we accept

On your own laptop, `~/.fabrica/runs.db` is a file on disk. A local agent that can run shell commands can `cat` it, or even run `fabrica runs` itself. Fabrica can't stop that without breaking the agent's shell. So the promise is:

> None of **Fabrica's own surfaces** (MCP tools, stubs, hooks, rules, skills, the remote server) give run history to agents.

This goes into `manual/safety-checks.md` under "Things that are deliberately left to people", the same way "side effects are an honor system" does today. Cloud agents never have the file or the admin token, so for them the rule holds completely.

---

## Part 1: Schema v3 and the write path

### What changes, in short

- `runs` gets a `session` column.
- `skill_loads` becomes a more general `loads` table that also covers rules and routines. The old name stays as a **view**, so existing queries keep working.
- A new `friction` table.
- `run_overview` gets five new columns **at the end**: `session`, `last_at`, `outcome`, `friction_count` and `pr_links`. The old columns stay in the same order, because Fabrica.app and people's own scripts read them.
- `run_begin` takes an optional `session`. `run_report` takes an optional `friction` list.
- `run_report` gets stricter about which runs it accepts.

### The migration

Migrations live in `internal/runs/runs.go`. `migrations[i]` takes a database from version `i` to `i+1`, and `PRAGMA user_version` stores the version. Add `schemaV3`, append it to `migrations`, and bump `SchemaVersion` to `3`. Every migration runs inside one transaction, so if anything fails, nothing changes.

```sql
-- schemaV3: session pointers, all kinds of loads, friction notes and outcomes.

ALTER TABLE runs ADD COLUMN session TEXT;   -- where the transcript lives, if known

CREATE TABLE loads (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id  TEXT NOT NULL REFERENCES runs(run_id),
  at      TEXT NOT NULL,                    -- RFC 3339, UTC
  kind    TEXT NOT NULL CHECK (kind IN ('skill', 'rule', 'routine')),
  name    TEXT NOT NULL
);
CREATE INDEX loads_by_run ON loads(run_id, at);

INSERT INTO loads (run_id, at, kind, name)
  SELECT run_id, at, 'skill', skill FROM skill_loads ORDER BY id;

DROP VIEW run_overview;                     -- it reads skill_loads, so it goes first
DROP TABLE skill_loads;

-- Old queries keep working.
CREATE VIEW skill_loads AS
  SELECT id, run_id, at, name AS skill FROM loads WHERE kind = 'skill';

CREATE TABLE friction (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id  TEXT NOT NULL REFERENCES runs(run_id),
  at      TEXT NOT NULL,                    -- RFC 3339, UTC
  kind    TEXT NOT NULL CHECK (kind IN ('navigation', 'checks', 'standards', 'tools', 'info', 'other')),
  note    TEXT NOT NULL                     -- at most 20 words
);
CREATE INDEX friction_by_run ON friction(run_id);

CREATE VIEW run_overview AS
SELECT
  -- v1 columns, same order as before
  r.run_id, r.started_at, r.intent, r.routine, r.runtime, r.repo,
  r.factory, r.blueprint_sha, r.served_by,
  (SELECT c.status  FROM checkpoints c WHERE c.run_id = r.run_id ORDER BY c.at DESC, c.id DESC LIMIT 1) AS last_status,
  (SELECT c.summary FROM checkpoints c WHERE c.run_id = r.run_id ORDER BY c.at DESC, c.id DESC LIMIT 1) AS last_summary,
  (SELECT COUNT(*)  FROM checkpoints c WHERE c.run_id = r.run_id) AS checkpoint_count,
  (SELECT group_concat(name, ',') FROM (SELECT DISTINCT l.name FROM loads l WHERE l.run_id = r.run_id AND l.kind = 'skill' ORDER BY l.name)) AS skills_loaded,
  -- new in v3
  r.session,
  COALESCE((SELECT MAX(c.at) FROM checkpoints c WHERE c.run_id = r.run_id), r.started_at) AS last_at,
  CASE (SELECT c.status FROM checkpoints c WHERE c.run_id = r.run_id ORDER BY c.at DESC, c.id DESC LIMIT 1)
    WHEN 'succeeded' THEN 'succeeded'
    WHEN 'failed'    THEN 'failed'
    ELSE CASE
      WHEN COALESCE((SELECT MAX(c.at) FROM checkpoints c WHERE c.run_id = r.run_id), r.started_at)
           < strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-6 hours')
      THEN 'stale'
      ELSE 'in_progress'
    END
  END AS outcome,
  (SELECT COUNT(*) FROM friction f WHERE f.run_id = r.run_id) AS friction_count,
  (SELECT json_group_array(DISTINCT l.value)
     FROM checkpoints c, json_each(c.links) l
     WHERE c.run_id = r.run_id
       AND (l.value LIKE '%/pull/%' OR l.value LIKE '%/merge_requests/%')) AS pr_links
FROM runs r;
```

A few things worth knowing:

- **Why `stale`?** Runs have no "end" event. Nobody can reliably say when an agent is done. A run whose latest checkpoint is still `progress` (or that has none) and that hasn't reported anything for **6 hours** is called `stale`. It most likely crashed, ran out of context or was abandoned. The 6 hours is a constant in the SQL; see "Open questions".
- **Outcome follows the *latest* checkpoint**, just like `last_status` does today. If an agent said `succeeded` and then later reported `progress` again, the run is `in_progress` again.
- **Timestamps are text.** They're RFC 3339 strings in UTC, so comparing them as strings works. The fractional seconds make it off by under a second, which doesn't matter here. The v1 docs' example queries already do this.
- **`pr_links`** is a JSON array of every distinct PR or merge request URL across all of a run's checkpoints, or `[]`.
- **Don't change the `runs` and `checkpoints` tables otherwise.** They're a public interface (see `manual/run-history.md`).

Write a migration test that builds a v2 database with a few runs and skill loads, opens it with the new code, and checks that:

- `skill_loads` still returns the same rows
- `run_overview`'s first 13 columns are unchanged
- `user_version` is 3

Also check that a v3 database opened by an older Fabrica still fails with the existing "made by a newer Fabrica" error.

### Go API changes in `internal/runs`

```go
type Run struct {
    // …existing fields…
    Session string // where the transcript lives, if the agent said
}

type Friction struct {
    Kind string // navigation, checks, standards, tools, info or other
    Note string // at most 20 words
}

type Checkpoint struct {
    // …existing fields…
    Friction []Friction // written in the same transaction as the checkpoint
}

// Load kinds.
const (
    LoadSkill   = "skill"
    LoadRule    = "rule"
    LoadRoutine = "routine"
)

// Loaded records that a run loaded a skill, rule or routine.
func (d *DB) Loaded(ctx context.Context, runID, kind, name string, at time.Time) error
```

- `Begin` writes `Session`.
- `AddCheckpoint` writes the checkpoint and its friction rows **in one transaction**, so you never get friction without its checkpoint.
- Replace `SkillLoaded` with `Loaded`. It has one caller, `recordSkillLoad` in `internal/mcpserver/tools.go`.
- Add `CheckSession(s string) error`: at most 512 bytes, one line, no control characters. Put it next to `CheckWords` and `CheckLinks`.
- Add `CheckFriction([]Friction) error`: at most 5 items per call, each with a valid kind and a note of 20 words or fewer (reuse `CheckWords`).

The read API (`List`, `Detail`, `Stats`) is in part 4. It lives in the same package, but **only operator-plane code calls it.** See "Keeping reads out of the factory plane" below.

### MCP changes in `internal/mcpserver`

**`run_begin`** gets one more optional argument:

```go
Session string `json:"session,omitempty" jsonschema:"where this session's transcript lives, if you know it: a transcript file path, a session ID or a session URL"`
```

- Run it through `runs.CheckSession`. If it's bad, return an error and **don't** record the run, just like a too-long intent today, so the agent tries again.
- Nothing about the return value changes.

**`run_report`** gets one more optional argument:

```go
Friction []frictionArg `json:"friction,omitempty" jsonschema:"anything that slowed you down since your last report, at most 5: kind is navigation (hard to find code or docs), checks (a lint, type check or test that was missing or broken), standards (a rule that was unclear, wrong or missing), tools (a slow, failing or wasteful tool), info (information you needed but couldn't get) or other; note is at most 20 words"`

type frictionArg struct {
    Kind string `json:"kind"`
    Note string `json:"note"`
}
```

The friction kinds deliberately match the categories in the popular "retro" skill, so a retro can sort friction into them without guessing.

**Recording loads.** Today only `skill_load` is recorded. In v3, record:

| Tool | What to record |
|---|---|
| `skill_load(name)` | one `skill` load (as today) |
| `rule_load(name)` | one `rule` load |
| `rules_get()` | one `rule` load **per always-on rule** it returned |
| `routine_get(name)` | one `routine` load |

As today, loads are only recorded after `run_begin` in the same MCP session. Generalize `recordSkillLoad(ctx, skill)` into `recordLoad(ctx, kind, name)`.

**Tool descriptions.** These get sent to every agent, so keep them short:

- Update `run_begin`'s description to mention `session` in a few words.
- Update `run_report`'s description to mention `friction` in a few words.
- In `Instructions` (`internal/mcpserver/text.go`), add one sentence: when something slows you down, say so in `run_report`'s `friction`.
- Stubs don't change, so nobody has to re-apply.

### Tightening `run_report`

Today `run_report` has two small leaks you should close:

1. **Different errors leak whether a run exists.** "there's no run X" and "run X doesn't belong to this factory" are different messages, so an agent can probe for run IDs in other factories. Run IDs are hard to guess, but there's no reason to leak this.
2. **The local server doesn't check the factory at all.** A local agent could add checkpoints to any run in `runs.db`.

New behavior: `run_report` accepts a `run_id` when **all** of these hold:

- the run exists
- it belongs to this session's factory, both locally and remotely
- locally, it's also for this session's repo

Otherwise it returns **one** message, whatever the reason:

> `run X wasn't begun for this factory and repo; use the run_id run_begin gave you`

Why not "only runs begun in this MCP session"? Because MCP connections drop and come back: a worker restarts, a laptop sleeps. Agents should be able to keep reporting on their run afterwards. `memory_save` stays strict ("only from the session that began the run"), as it is today.

### Keeping reads out of the factory plane

Make this hard to break by accident:

- **Allowlist test.** In `internal/mcpserver/mcpserver_test.go`, assert that the registered tools are *exactly*:

  `skills_list`, `skill_load`, `skill_file`, `rules_get`, `rule_load`, `routine_get`, `memory_save`, `run_begin`, `run_report`

  Anyone adding a tool has to change this test and explain why in review.
- **No-leak test.** Seed a database with runs from another factory and repo, with recognizable intents, summaries, links and friction notes. Then call every tool from a session, with both good and bad arguments. Assert that no response or error message contains any of the seeded text or run IDs.
- **Code rule.** `internal/mcpserver` must not call the new read functions (`List`, `Detail`, `Stats`). Add a test that greps the package's non-test source for those names and fails if it finds them. This is cheap and catches the obvious mistake.

---

## Part 2: Admin token and admin API

### Why a token, and why computed

The remote server already has one secret, the **server secret**. Each factory's token is `HMAC(server secret, factory name)` (see `internal/token`). Cloud runtimes only ever get their factory's token, never the server secret.

The admin token works the same way:

```
admin token = "fab_admin_" + base64url(HMAC-SHA256(server secret, "_admin"))
```

- **No new secret to store or rotate.** Rotating the server secret rotates the admin token too.
- **It can't collide with a factory token.** Factory names must match `^[a-z0-9]+(-[a-z0-9]+)*$` (`internal/names`), so no factory can be called `_admin`.
- **You can hand it out on its own.** Someone with only the admin token can *read* history, but can't mint factory tokens.

Add to `internal/token`:

```go
// Admin is the admin token of a server: it unlocks the admin API and the
// dashboard, and nothing else.
func Admin(secret string) string

// ValidAdmin checks a token against the admin token, in constant time.
func ValidAdmin(secret, tok string) bool
```

### Getting the admin token

Add `fabrica auth admin-token [server-url]`:

- It finds the server secret the same way `auth` already does (`credentials.ServerSecret`), and prints the admin token.
- With no URL, it uses the only server in the home repo, or asks you to name one if there are several.

The CLI's `--server` flag (part 4) also accepts the token through a new `FABRICA_ADMIN_TOKEN` environment variable. That's for people who have the admin token but not the server secret.

### Routes on the remote server

The remote server's `ServeHTTP` (`internal/mcpserver/remote.go`) routes `/healthz` and factory endpoints today. Add the routes below.

Match them by the **last** `/_admin/` or `/_dashboard` segment in the path, so they work under any path prefix (for example `server: https://acme.dev/fabrica`). This can't collide with factory endpoints, because factory names never contain `_`.

| Route | What it returns |
|---|---|
| `GET …/_admin/runs` | A page of runs, newest first (JSON) |
| `GET …/_admin/runs/<run_id>` | One run in full (JSON) |
| `…/_dashboard…` | The dashboard (part 5) |

Rules for `/_admin/*`:

- **The token goes in `Authorization: Bearer <admin token>`.** The `/t/<token>` URL form that factory endpoints accept is **not** allowed here, so the admin token never ends up in proxy logs.
- **Anything that isn't the admin token gets `401`.** That includes valid factory tokens. Don't distinguish "valid factory token" from "garbage"; the error message is the same.
- **Only `GET`.** Anything else gets `405`.
- **Every response sends `Cache-Control: no-store`.**
- **Bad query values get `400`,** with a JSON `{"error": "…"}` like `httpError` already sends.

Query parameters for `GET …/_admin/runs`:

| Parameter | Meaning | Default |
|---|---|---|
| `factory` | only this factory | all |
| `routine` | only this routine; `-` means interactive runs (no routine) | all |
| `repo` | only this repo, like `github.com/acme/app` | all |
| `runtime` | only this runtime | all |
| `outcome` | only these outcomes, comma-separated | all |
| `since` | `7d`, `48h`, or an RFC 3339 time | `7d` |
| `limit` | 1–500 | `100` |
| `before` | a `run_id`; return runs older than it (for paging) | none |

The JSON shapes come from the read API in part 4. One shape is shared by the API, `fabrica runs --json` and the export bundle, so define it once in `internal/runs` with JSON tags.

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

`next` is the `before` value for the next page, or missing on the last page.

`GET …/_admin/runs/<run_id>` returns the same fields, plus:

```json
{
  "stub": "…",
  "timeline": [
    {"at": "…", "type": "load",       "kind": "routine", "name": "pr-review"},
    {"at": "…", "type": "load",       "kind": "rule",    "name": "product-knowledge-only"},
    {"at": "…", "type": "load",       "kind": "skill",   "name": "pr-review"},
    {"at": "…", "type": "checkpoint", "author": "agent", "status": "progress", "summary": "…", "links": []},
    {"at": "…", "type": "friction",   "kind": "navigation", "note": "…"},
    {"at": "…", "type": "checkpoint", "author": "agent", "status": "succeeded", "summary": "…", "links": ["…"]}
  ]
}
```

The timeline is every load, checkpoint and friction note for the run, sorted by time. Friction rows have the same `at` as their checkpoint, and come right after it.

### Tests for part 2

- With the admin token, every route returns `200`.
- With each factory's token, every route returns `401`, whether the token is in a header or in a `/t/` path.
- With no token or a wrong one, every route returns `401`.
- `/t/<admin token>` doesn't work.
- `POST` returns `405`.
- The routes work with a path prefix.
- Paging with `before` and `next` walks every run exactly once.

---

## Part 3: The session pointer hook

### The problem

A retro is much better with the transcript: you can see *where* the agent went wrong, not just that it did. But the agent usually doesn't know where its own transcript is stored. The local tools *do* know, and they send it to hooks:

- **Claude Code and Codex CLI** send `session_id`, and `transcript_path` when they have it.
- **Copilot CLI** sends `sessionId`.
- **Cursor** sends `conversation_id`, but it has no session-start hook, so we can't use it here. See below.

`internal/hook/hook.go` already parses most of these fields (`input` struct). Add `TranscriptPath string \`json:"transcript_path"\``.

### What to build

A new kind of hook entry, the **session hook**:

```
fabrica hook session --runtime claude-code-local
```

**Installing it.** `fabrica apply` installs one session hook per local runtime that:

- is used by at least one local factory in your personal home repo, **and**
- has a session-start event: `claude-code-local`, `codex-cli` and `copilot-local`.

Event mapping already exists (`claudeEvent`, `copilotEvent` in `internal/localtools/hookcmd.go`). Use the `SessionStart` / `sessionStart` events.

- **`plan` shows it** like any other hook change.
- **`destroy` of the last local factory using a runtime removes it.**
- **`IsFabricaEntry`, `HookCommand` and `ParseHookCommand` learn the new form,** so apply recognizes and updates its own entry and never touches anyone else's.

**What it does when it fires:**

1. Do the same quiet checks as routine hooks. Is this a git repo? Is there a matching local factory for this runtime? Does it have the Fabrica line? If any check fails, print nothing and exit 0. (Hooks never get in your way because of Fabrica's own problems.)
2. Work out the pointer:
   - `transcript_path`, if the tool sent one
   - otherwise `<runtime>:<session id>`, like `copilot-local:3f2a…`
   - otherwise print nothing
3. Print one line of extra context for the agent, in the format each tool expects. Reuse `claudeContext("SessionStart", …)` for Claude Code and Codex, and `{"additionalContext": …}` for Copilot CLI:

   > `Fabrica: when you call run_begin, pass session: "<pointer>".`

Claude Code also fires `SessionStart` when a session resumes or is compacted. That's fine: the pointer is the same.

**Is this against the big rule?** No. The hook tells an agent about *its own* session, which it's already in. It doesn't read run history.

### Cursor and cloud runtimes

- **Cursor** has no session-start hook (see the trigger table in `manual/providers.md`), so Cursor runs only have a pointer when the agent passes one on its own.
- **Cloud runtimes** get no hook. Some agents know their own session URL (Claude Code on the web puts it in the system prompt, for example), and the `run_begin` description invites them to pass it.
- So `session` is **best effort**, like `intent` and `summary`. The docs must say so.

### Tests for part 3

- `apply` installs exactly one session hook per eligible runtime, and none for `cursor-local`.
- Re-applying changes nothing.
- `destroy` removes the hook when the runtime is no longer used.
- Given stdin from each tool (copy real payloads into test fixtures), the hook prints the right pointer, in the right output format.
- In a folder that's not a git repo, or has no matching factory, or no Fabrica line, it prints nothing.

---

## Part 4: `fabrica runs`

### The read API (`internal/runs`)

Operator-plane code (the CLI, the admin API and the dashboard) uses these. The factory plane must not (see part 1).

```go
type Filter struct {
    Factory, Routine, Repo, Runtime string // Routine "-" means interactive runs
    Outcomes []string
    Since    time.Time
    Before   string // run_id, for paging
    Limit    int
}

type Summary struct { /* the fields of one run in the JSON above */ }
type Detail  struct { Summary; Stub string; Timeline []Event }
type Event   struct { /* one entry of the timeline */ }

func (d *DB) List(ctx context.Context, f Filter) (runs []Summary, next string, err error)
func (d *DB) Detail(ctx context.Context, runID string) (*Detail, error)
func (d *DB) Stats(ctx context.Context, f Filter) (*Stats, error) // for the dashboard, see part 5
```

- Build the SQL with `?` placeholders, never by pasting strings together.
- Read from `run_overview` for `List`, and from `checkpoints`, `loads` and `friction` for `Detail`.
- Sort newest first by `run_id`. IDs sort by creation time.

### Commands

Add a `runs` command to `internal/cli` (register it in `root.go` next to the others) with three subcommands. All of them read `runs.db` in your home repo by default (`paths` already knows where it is), or a remote server with `--server`.

#### `fabrica runs` (same as `fabrica runs list`)

```
$ fabrica runs --since 3d
STARTED           OUTCOME      FACTORY  ROUTINE    REPO        INTENT                                  RUN
Oct 17 09:41      succeeded    acme     pr-review  acme/app    Review PR 412 for the billing retry…    run_06gmjeps…
Oct 17 08:02      stale        acme     —          acme/app    Fix flaky checkout test                 run_06gmj9xq…
Oct 16 22:15      failed       acme     autofix    acme/api    Fix lint failure on main                run_06gmh2aa…

3 runs. Show one with: fabrica runs show <run>
```

- **Filter flags:**
  - `--factory`, `--routine` (`-` for interactive), `--repo`, `--runtime`
  - `--outcome` (repeatable)
  - `--since` (default `7d`), `--limit` (default `50`)
- **Output flags:**
  - `--json`: the same JSON as the admin API, `{"schema_version": 3, "runs": […], "next": …}`
- **Display:**
  - Times are shown in your local time zone.
  - Long text is cut to fit an 80-column terminal, except with `--json`.
  - `REPO` drops the host (`acme/app`).
  - `RUN` shows a unique prefix of the ID, at least 12 characters.
- **Empty result:** say so plainly and suggest widening `--since`.

#### `fabrica runs show <run>`

`<run>` can be a full run ID or a unique prefix.

```
$ fabrica runs show run_06gmjeps
Run run_06gmjeps8jf8zfjjrfmydev50g: succeeded
  Intent     Review PR 412 for the billing retry change
  Factory    acme (served remotely), blueprint ee8f142
  Routine    pr-review on claude-code-cloud, repo github.com/acme/app
  Session    https://claude.ai/code/session_…
  Started    Oct 17 09:41:02, last heard from 09:58:40 (17m later)

09:41:05  loaded routine pr-review
09:41:06  loaded rules product-knowledge-only, commit-messages
09:41:09  loaded skill pr-review
09:47:30  progress   Read the diff and the retry tests
09:52:11  friction   navigation: couldn't find where retry backoff is configured
09:58:40  succeeded  Posted review with two blocking comments
          → https://github.com/acme/app/pull/412

See the instructions this run was served:
  git -C <your blueprint clone> show ee8f142:routines/pr-review.md
```

- **Merge consecutive loads of the same kind into one line,** as above.
- **Fabrica's own checkpoints (refusals) show as `fabrica`,** so they stand out from agent checkpoints.
- **`--json` prints the detail JSON.**

#### `fabrica runs export`

This writes a **retro bundle**: a folder you hand to a person, or to your own retro skill. Fabrica doesn't ship a retro skill; you keep yours wherever you keep your skills.

```
$ fabrica runs export --routine pr-review --since 14d
Exported 23 runs (17 succeeded, 4 failed, 2 stale) to ./fabrica-runs-20261017-1004/
```

- **Filters:** the same as `list`. `--limit` defaults to `200` here.
- **`--out <dir>`:** where to write. The default is `./fabrica-runs-<YYYYMMDD-HHMM>/`. Refuse to write into a folder that exists and isn't empty.

**The bundle format** is a public interface, like the schema, so document it in `manual/run-history.md`:

```
fabrica-runs-20261017-1004/
  README.md        what this is, the filters used, totals, and how to read it
  runs.json        {"schema_version": 3, "filters": {…}, "runs": [<detail>, …]}
  runs/
    run_06gmjeps8jf8zfjjrfmydev50g.md   one run, same content as `fabrica runs show`
    …
```

`README.md` should contain:

1. One paragraph saying what the bundle is, which factory, server and time window it came from, and when it was made.
2. **Totals:**
   - runs per outcome
   - runs per routine (with success rate)
   - friction notes per kind
   - the most-loaded skills and rules
   - refusals by reason
3. **The list of runs,** grouped by outcome (failed, then stale, then succeeded, then in progress). Each line links to its `runs/<id>.md`.
4. **"Where to look next":**
   - the `git show <sha>:<path>` hint for served instructions, listing each distinct `blueprint_sha` in the bundle
   - the PR links, whose review comments are often the best signal of what went wrong
   - the session pointers that point at files that exist on this machine, for transcripts
5. **A short "Fields you can't fully trust" note:** intents, summaries, statuses, friction and session pointers are written by agents, on a best-effort basis. Times, blueprint commits and loads are written by Fabrica.

Transcripts aren't copied into the bundle. They can be huge and contain anything. The bundle only *points* at them.

### Reading a remote server: `--server`

- `--server` takes a server URL, or a factory name from your home repo, in which case its `server:` is used.
- **Token lookup:** first `FABRICA_ADMIN_TOKEN`, then the admin token computed from the saved server secret (`credentials.ServerSecret`).
- If neither is there, say exactly what to do: "run `fabrica auth login <url>`, or set FABRICA_ADMIN_TOKEN".
- `list` follows `next` until it has `--limit` runs. `export` does too, then fetches each run's detail. Fetch at most 4 at a time, so a big export doesn't hammer the server.
- **Local and remote histories are never merged** in v2. You read one or the other.

### Tests for part 4

- `list`, `show` and `export` against a seeded local database, compared against golden files. That covers the filters, the `-` routine, unique prefixes (and ambiguous ones, which must error) and the empty result.
- `--json` output matches the admin API's JSON for the same data.
- `--server` against an in-process remote server: works with the admin token, fails clearly with no token.
- `export` refuses a non-empty `--out`.

---

## Part 5: The dashboard

### Where it runs

| Where | How to open it | Who can see it |
|---|---|---|
| **Locally** | `fabrica dashboard` serves `~/.fabrica/runs.db` on `127.0.0.1` | whoever has the URL it prints (below) |
| **Remote server** | `https://<server>/_dashboard` | whoever has the admin token |

**Locally:**

- `fabrica dashboard` picks a free port, or takes one from `--port`.
- It binds to `127.0.0.1` only.
- It makes a random one-time code and prints `http://127.0.0.1:<port>/_dashboard?code=<code>`.
  - Visiting that URL trades the code for a cookie and redirects to the clean URL.
  - Requests without the cookie get a page that says to run `fabrica dashboard` again.
- `--open` opens the URL in your browser.
- Ctrl-C stops it.

**Remotely:**

- `/_dashboard` without a valid cookie shows a small page with one password field: "Admin token".
- Submitting it (`POST /_dashboard/login`) checks the token with `token.ValidAdmin` and sets a cookie:
  - name `fabrica_admin`, value the admin token
  - `HttpOnly`, `SameSite=Strict`, `Path=<prefix>/_dashboard`
  - `Secure` when the request came over HTTPS (check `r.TLS` or `X-Forwarded-Proto: https`)
- `POST /_dashboard/logout` clears it.
- Every other dashboard request is a `GET` and changes nothing, so there's nothing for CSRF to abuse.

### How it's built

- **A new package, `internal/dashboard`,** that exports one `http.Handler`. Both the local command and the remote server use it, so the pages are the same in both places.

  ```go
  func New(db *runs.DB, opts Options) http.Handler
  ```

- **Server-rendered `html/template` pages,** embedded with `embed`. No JavaScript framework, no build step and nothing loaded from the internet, so the binary stays self-contained.
- **Charts are inline SVG made in Go.** Add a tiny helper for stacked bars and lines. Every chart also shows its numbers in a table, or as `<title>` tooltips, so it works without a mouse and for screen readers.
- **CSS is inline.** It follows the system's light or dark mode (`prefers-color-scheme`).
- **Every page sends:**
  - `Cache-Control: no-store`
  - `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; img-src data:; form-action 'self'`
- **The data comes from `runs.Stats`** (below) plus `List` and `Detail`.

### Pages

Each page has filters at the top, as plain links or a form using `GET` query parameters, so every view is bookmarkable:

- factory
- time window: 7, 30 or 90 days (default 30)
- routine runs, interactive runs, or both

**1. Overview (`/_dashboard`)**

- **Tiles:**
  - total runs
  - success rate (succeeded ÷ finished, where finished = succeeded + failed)
  - failed
  - stale
  - PRs touched
  - refusals
- **Chart: runs per day, stacked by outcome.**
- **Routines table:** one row per routine with:
  - runs and success rate
  - failed and stale counts
  - its most common friction kind
  - when it last ran

  Each routine links to its page.
- **Refusals:** grouped by factory, routine and runtime, with Fabrica's reason. When the reason is a stale stub, show the fix: "run `fabrica apply` for factory acme".
- **Friction:** counts per kind, plus the 10 most recent notes, each linking to its run.
- **PRs:** the most recent PR links, with routine, outcome and run.

**2. Routine (`/_dashboard/routines/<factory>/<routine>`)**

- **Chart: failure rate per day,** where failure rate = (failed + stale) ÷ runs that day. **Mark each blueprint change on it:**
  - Draw a vertical line on the day a new `blueprint_sha` was **first served** to this routine.
  - Label it with the short SHA.
  - When the server's git cache has the commit, add its subject as a tooltip. Use the same `gitx` cache the server already reads blueprints with; skip the subject if the lookup fails.
  - This answers "did my change to the skill help?"
- **Runs table** for this routine, newest first, linking to run pages.
- **Friction notes for this routine,** grouped by kind.
- **Loads:** how often each skill and rule was loaded by this routine's runs.

**3. Run (`/_dashboard/runs/<run_id>`)**

The same content as `fabrica runs show`, as HTML: the header, then the timeline. PR links are clickable. The session pointer is shown as text; it's clickable only if it's an `https://` URL.

**Empty states.** Every table and chart says something useful when there's no data. For example: "No runs in the last 30 days. Runs show up here once agents call run_begin."

### `runs.Stats`

```go
type Stats struct {
    Totals    map[string]int        // per outcome
    PerDay    []DayCounts           // date, plus counts per outcome
    Routines  []RoutineStats        // factory, routine, runs, per outcome, top friction kind, last run
    Friction  map[string]int        // per kind
    Recent    []FrictionNote        // the latest notes, with their run
    Loads     []LoadCount           // kind, name, runs
    Refusals  []RefusalGroup        // factory, routine, runtime, summary, count, latest
    PRs       []PRLink              // url, run, routine, outcome, at
    Blueprint []BlueprintFirstSeen  // factory, routine, sha, first served at
}
```

- **Keep each piece a single SQL query** over `run_overview` and the tables, filtered by the same `Filter`.
- **90 days is the longest window,** so these stay fast on a database with tens of thousands of runs. Add a benchmark test with 50,000 runs and make sure the overview renders in under a second.

### Fabrica.app

Fabrica.app reads `run_overview` read-only, and its columns don't change, so it keeps working with no changes. Showing `outcome` instead of `last_status`, and opening the local dashboard from the menu, are nice follow-ups but **not part of this spec**.

### Tests for part 5

- **Handler tests with a seeded database:**
  - every page returns `200` and contains the expected numbers
  - the empty database renders the empty states
  - unknown routines and runs return `404`
- **Auth:**
  - Remote: without the cookie you get the login page. A wrong token on login gets a `401` and the login page again. A factory token on login is refused. The cookie has the right flags.
  - Local: without the code or cookie you get the "run `fabrica dashboard` again" page.
- **Headers:** the CSP and `no-store` headers are on every response.
- **No outside requests:** a test asserts the HTML contains no `http://` or `https://` URLs except links from run data.

---

## Part 6: Docs

Update these, in plain English like the rest of the manual:

- **`manual/run-history.md`** (most of the work):
  - Replace "v0.1 has no built-in way to read run history" with the new ways: `fabrica runs`, the dashboard, the admin API and Fabrica.app.
  - Add **"Who can read it"**: the factory plane / operator plane split, and the local-disk caveat.
  - Document schema v3: `session`, `loads`, the `skill_loads` view, `friction`, and the new `run_overview` columns. Explain how `outcome` and `stale` work.
  - Document the retro bundle format.
  - Update the "Handy queries" to use `loads` and `outcome`.
- **`manual/mcp-server.md`:**
  - the new `session` and `friction` arguments in the tools table
  - the stricter `run_report`
  - the admin routes and `/_dashboard`
  - a sentence that no tool returns run history, on purpose
- **`manual/cli.md`:** `fabrica runs` (list, show, export), `fabrica dashboard`, `fabrica auth admin-token`, and `fabrica hook session` under plumbing.
- **`manual/auth.md`:** the admin token — how it's computed, how to get it, who to give it to, and that rotating the server secret rotates it.
- **`manual/safety-checks.md`:**
  - Add the run-history rule to "Where each kind of check happens".
  - Add the local-disk caveat to "Things that are deliberately left to people".
- **`manual/providers.md`:** which local runtimes get the session pointer hook.
- **`README.md`:** remove the "There's no command to read run history yet" limitation, and add a line about the dashboard.
- **`internal/runs/runs.go` package comment:** it says v0.1 has no way to read history; update it.
- **While you're in these files:** the manual still says "v0.1" in places where it means the current release. Change the ones you touch to match the real version.

---

## Build order

Each step is its own PR, and each one leaves `main` working.

1. **Schema v3 and the write path** (part 1). This must go first: history can't be filled in after the fact, so the sooner agents send `session` and `friction`, the more data the later steps have.
2. **Admin token and admin API** (part 2), including the read API from part 4, which it needs.
3. **The session pointer hook** (part 3). It's independent of 2 and can be built in parallel.
4. **`fabrica runs`** (part 4).
5. **The dashboard** (part 5).
6. **Docs** (part 6). Each earlier PR updates the docs for what *it* adds; this last PR does the README, the overall run-history rewrite, and checks that everything fits together.

Before each PR, run `make build test lint`, as for any change.

## Not in this spec

- **Agents reading run history,** in any form, ever. That's the big rule, not a "later".
- **Token, cost and duration tracking.** No runtime reports tokens or cost to Fabrica today.
- **Merging local and remote history** into one view.
- **Copying or uploading transcripts.** Fabrica only stores the pointer.
- **Pruning old runs,** retention settings.
- **Alerts and notifications,** like "post to Slack when a routine fails three times".
- **Multiple users and roles on the dashboard.** There's one admin token per server.
- **A retro skill.** Bring your own; the bundle is what Fabrica provides.

## Open questions (with the default we'll use if nobody objects)

1. **How long before a run counts as `stale`?** Default: **6 hours**, hard-coded in the view. A long interactive session that stays quiet for 6 hours shows as `stale` and flips back to `in_progress` when it reports again. That's harmless.
2. **How many friction notes per `run_report`?** Default: **5**. Agents rarely have more than one or two, and a cap stops a runaway loop from filling the table.
3. **Dashboard default time window?** Default: **30 days**, with 7 and 90 as the other choices.

---

## Appendix: using a retro bundle with your own retro skill

This is guidance for whoever writes the retro skill, not something Fabrica ships. A retro skill (like the popular one that sorts findings into navigation, automated checks, coding standards, AGENTS.md, tool economy, no-ops and information access) can work from a bundle like this:

1. **Start with `README.md`.** It already lists the failed and stale runs first, and the friction totals.
2. **Read the friction notes.** Their kinds map straight onto retro categories:

   | Friction kind | Retro category |
   |---|---|
   | `navigation` | Navigation |
   | `checks` | Automated checks |
   | `standards` | Coding standards |
   | `tools` | Tool economy |
   | `info` | Information access |
   | `other` | read and decide |

3. **For failed and stale runs, read the transcript** when the session pointer is a file on this machine. Look for the point where the run went wrong.
4. **Read the PR review comments** for linked PRs, with `gh` or the GitHub tools. Corrections from people are the strongest signal.
5. **Check what the agent was actually told.** Use the `git show <sha>:<path>` hints to read the exact routine, skills and rules each run was served. Compare the loads: a rule that's served to every run, but never matters in any transcript, is a candidate for the "no-ops" category.
6. **Sort each finding by where the fix goes:**
   - **Blueprint fix:** a skill, rule or routine change. It's a PR to the blueprint repo, and it reaches every factory that uses the blueprint on their next run.
   - **Product fix:** AGENTS.md, a lint rule, a test, or CI in the product repo.
7. **After the fix ships, check the routine's page on the dashboard.** The blueprint commit shows up as a marker on the failure-rate chart, so you can see whether things got better.
