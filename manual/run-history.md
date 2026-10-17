# Run history

Every run, whether it's a routine firing in the cloud or you working in your editor, gets recorded. History can't be filled in after the fact, so Fabrica records from day one, even though tools for browsing it come later.

## Where it's stored

- **Remote:** a SQLite database on the server's persistent volume (`fabrica mcp --http --db …`).
- **Local:** `runs.db` in your personal home repo (`~/.fabrica/runs.db` by default), with the same schema. It never leaves your machine. A home repo that Fabrica created already has `runs.db*` in its `.gitignore`; if you cloned yours, add it.

v0.1 has **no built-in way to read run history**. Open the file with any SQLite tool, like the `sqlite3` command line. That makes the schema below a public interface: later versions will migrate it carefully, and keep the `run_overview` view working.

## How a run gets recorded

1. **At the start**, the agent calls `run_begin`. For routines, the stub tells it to. In interactive sessions, the MCP instructions and the Fabrica line tell it to. If the agent knows where its transcript lives, it can pass that as `session`.
2. **As it works**, the agent calls `run_report` whenever it has progress to record. These are **checkpoints**. There's no "end", because nobody can reliably know when a run is over. If something slowed the agent down, it can add **friction** notes to the same call.
3. **The server links** every skill, rule and routine the agent loads in the same MCP session (after `run_begin`) to the run. Those are **loads**.

When Fabrica refuses a run (an outdated stub, a repo without the Fabrica line, a skill clash), it still records the run, with a `failed` checkpoint written by Fabrica itself that says why.

Fields written by the server (times, blueprint commit, factory, loads) are reliable. Fields written by the agent (intent, summaries, statuses, links, friction, the session pointer) are best effort: they depend on the agent calling the tools, and on it knowing the answer. A cloud agent might not know where its transcript is, for example.

## The schema

The database's `PRAGMA user_version` is the schema version, `3` for this one. Version 3 added `runs.session`, `loads` (so rules and routines are recorded, not just skills), `friction` and the new `run_overview` columns. Version 2 added `memories`. Fabrica migrates older databases when it opens them, and a database made by a newer Fabrica than yours is refused.

This schema number has nothing to do with Fabrica's own version.

### `runs`

One row per run.

| Column | Written by | What it is |
|---|---|---|
| `run_id` | server | Like `run_06gjb731941x9njfkjzq7vwncc`. IDs sort by creation time. |
| `started_at` | server | RFC 3339, UTC. |
| `intent` | agent | A summary of the original request, at most 20 words, in casual plain English. |
| `routine` | stub | `NULL` for interactive sessions. |
| `runtime` | stub (remote) or server (local) | Like `claude-code-cloud` or `cursor-local`. |
| `repo` | stub (remote) or server (local) | Like `github.com/acme/app`. The local server works it out from the workspace. |
| `factory` | server | From the endpoint (remote) or the matched local factory (local). |
| `blueprint_sha` | server | The exact blueprint commit that was served. |
| `stub` | stub | The stub fingerprint the run claimed, if any. |
| `served_by` | server | `local` or `remote`. |
| `session` | agent | Where the run's transcript lives, if the agent knew: a transcript file path, a session ID or a session URL. At most 512 bytes, on one line. `NULL` when nobody said. (New in schema 3.) |

### `checkpoints`

One row per `run_report` (and per refusal).

| Column | What it is |
|---|---|
| `id` | An increasing number. |
| `run_id` | The run. |
| `at` | RFC 3339, UTC, set by the server. |
| `author` | `agent`, or `fabrica` for checkpoints Fabrica writes itself (like refusals). |
| `status` | `progress`, `succeeded` or `failed`. |
| `summary` | At most 20 words. |
| `links` | A JSON array of URLs (PRs, issues, commits). |

### `loads`

One row each time a run loaded a skill, a rule or a routine through the MCP server.

| Column | What it is |
|---|---|
| `id` | An increasing number. |
| `run_id` | The run. |
| `at` | RFC 3339, UTC, set by the server. |
| `kind` | `skill`, `rule` or `routine`. |
| `name` | The skill, rule or routine's name. |

What gets recorded, after `run_begin` in the same MCP session:

| Tool | Loads |
|---|---|
| `skill_load` | one `skill` |
| `rule_load` | one `rule` |
| `rules_get` | one `rule` for every always-on rule it hands over, including the built-in `product-knowledge-only` rule that every blueprint gets |
| `routine_get` | one `routine` |

Loads that fail aren't recorded. Neither are loads before `run_begin`, because there's no run to link them to.

### `skill_loads`

A view with the `skill` rows of `loads`: `id`, `run_id`, `at` and `skill`. Before schema 3 this was a table, and the view keeps queries written for it working.

### `friction`

One row per friction note: something an agent says slowed it down. Agents add them to `run_report`, at most 5 per call.

| Column | What it is |
|---|---|
| `id` | An increasing number. |
| `run_id` | The run. |
| `at` | RFC 3339, UTC. The same as its checkpoint's `at`. |
| `kind` | What kind of slowdown it was; see below. |
| `note` | At most 20 words. |

The kinds are `navigation` (hard to find code or docs), `checks` (a lint, type check or test that was missing or broken), `standards` (a rule that was unclear, wrong or missing), `tools` (a slow, failing or wasteful tool), `info` (information the agent needed but couldn't get) and `other`.

### `memories`

One row per factory, routine and repo: the notes a routine with `memory: true` keeps for its own next runs, on runtimes where Fabrica keeps them (see [Memory](blueprints.md#memory)).

| Column | What it is |
|---|---|
| `factory`, `routine`, `repo` | Whose memory it is. Together they're the primary key. |
| `content` | Markdown written by the agent with `memory_save`, at most 16 KiB. |
| `updated_at` | RFC 3339, UTC, set by the server. |
| `updated_by` | The run that saved it. |

Only the latest version is kept. Saving an empty memory deletes the row.

### `run_overview`

A view with one row per run, its latest checkpoint and a few counts. Fabrica.app and your own scripts can read it, so its first 13 columns never change. New columns only ever go at the end.

| Column | What it is |
|---|---|
| `run_id`, `started_at`, `intent`, `routine`, `runtime`, `repo`, `factory`, `blueprint_sha`, `served_by` | The same as in `runs`. |
| `last_status` | The status of the latest checkpoint, or `NULL` if there is none. |
| `last_summary` | The summary of the latest checkpoint. |
| `checkpoint_count` | How many checkpoints the run has. |
| `skills_loaded` | The distinct skills it loaded, as a comma-separated list. Rules and routines aren't in it; look in `loads`. |
| `session` | The same as in `runs`. (New in schema 3.) |
| `last_at` | When the run last reported, or `started_at` if it never did. |
| `outcome` | What we think happened: `succeeded`, `failed`, `in_progress` or `stale`. See below. |
| `friction_count` | How many friction notes the run has. |
| `pr_links` | A JSON array of every distinct PR or merge request link across the run's checkpoints, like `["https://github.com/acme/app/pull/412"]`, or `[]`. |

#### How `outcome` works

Runs have no "end" event, so Fabrica works out the outcome from the checkpoints:

- If the **latest** checkpoint says `succeeded` or `failed`, that's the outcome. If an agent says `succeeded` and then reports `progress` again later, the run is `in_progress` again.
- Otherwise the run is `in_progress`, unless it hasn't reported anything for **6 hours**. Then it's `stale`: it most likely crashed, ran out of context or was abandoned. That includes runs that never reported at all, measured from when they started.

The 6 hours are written into the view. A long interactive session that stays quiet for 6 hours shows up as `stale`, and flips back to `in_progress` when it reports again. That's harmless.

Because the view looks at the clock, a run's outcome can change without anything being written to the database.

## Handy queries

The last ten runs:

```sql
SELECT started_at, runtime, repo, routine, intent, outcome
FROM run_overview
ORDER BY started_at DESC
LIMIT 10;
```

How often each routine failed or went stale in the last four weeks:

```sql
SELECT routine,
       COUNT(*) AS runs,
       SUM(outcome = 'failed') AS failed,
       SUM(outcome = 'stale') AS stale
FROM run_overview
WHERE routine IS NOT NULL
  AND started_at >= strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-28 days')
GROUP BY routine
ORDER BY failed DESC;
```

Runs that went quiet without finishing, with where to find their transcript:

```sql
SELECT started_at, last_at, routine, repo, intent, session
FROM run_overview
WHERE outcome = 'stale'
ORDER BY started_at DESC;
```

Why runs failed:

```sql
SELECT r.started_at, r.repo, c.author, c.summary
FROM checkpoints c JOIN runs r USING (run_id)
WHERE c.status = 'failed'
ORDER BY c.at DESC;
```

Runs Fabrica refused (usually stale stubs that need `fabrica apply`):

```sql
SELECT r.started_at, r.factory, r.routine, r.runtime, r.repo, c.summary
FROM checkpoints c JOIN runs r USING (run_id)
WHERE c.author = 'fabrica'
ORDER BY c.at DESC;
```

Which skills, rules and routines get loaded the most:

```sql
SELECT kind, name, COUNT(DISTINCT run_id) AS runs
FROM loads
GROUP BY kind, name
ORDER BY runs DESC;
```

What slowed agents down, newest first:

```sql
SELECT r.started_at, r.routine, f.kind, f.note
FROM friction f JOIN runs r USING (run_id)
ORDER BY f.at DESC;
```

How much of each kind of friction there is:

```sql
SELECT kind, COUNT(*) AS notes
FROM friction
GROUP BY kind
ORDER BY notes DESC;
```

What each routine remembers:

```sql
SELECT factory, routine, repo, updated_at, content
FROM memories
ORDER BY updated_at DESC;
```

Every PR (or merge request) a run touched, and how that run went:

```sql
SELECT o.routine, o.outcome, l.value AS pr
FROM run_overview o, json_each(o.pr_links) l;
```
