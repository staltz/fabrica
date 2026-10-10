# Run history

Every run, whether it's a routine firing in the cloud or you working in your editor, gets recorded. History can't be filled in after the fact, so Fabrica records from day one, even though tools for browsing it come later.

## Where it's stored

- **Remote:** a SQLite database on the server's persistent volume (`fabrica mcp --http --db …`).
- **Local:** `runs.db` in your personal home repo (`~/.fabrica/runs.db` by default), with the same schema. It never leaves your machine. A home repo that Fabrica created already has `runs.db*` in its `.gitignore`; if you cloned yours, add it.

v0.1 has **no built-in way to read run history**. Open the file with any SQLite tool, like the `sqlite3` command line. That makes the schema below a public interface: later versions will migrate it carefully, and keep the `run_overview` view working.

## How a run gets recorded

1. **At the start**, the agent calls `run_begin`. For routines, the stub tells it to. In interactive sessions, the MCP instructions and the Fabrica line tell it to.
2. **As it works**, the agent calls `run_report` whenever it has progress to record. These are **checkpoints**. There's no "end", because nobody can reliably know when a run is over.
3. **The server links** every `skill_load` in the same MCP session (after `run_begin`) to the run.

When Fabrica refuses a run (an outdated stub, a repo without the Fabrica line, a skill clash), it still records the run, with a `failed` checkpoint written by Fabrica itself that says why.

Fields written by the server (times, factory commit, deployment, skills loaded) are reliable. Fields written by the agent (intent, summaries, statuses, links) are best effort: they depend on the agent calling the tools.

## The schema

The database's `PRAGMA user_version` is the schema version, `1` for this one.

### `runs`

One row per run.

| Column | Written by | What it is |
|---|---|---|
| `run_id` | server | Like `run_06gjb731941x9njfkjzq7vwncc`. IDs sort by creation time. |
| `started_at` | server | RFC 3339, UTC. |
| `intent` | agent | A summary of the original request, at most 20 words, in casual plain English. |
| `routine` | stub | `NULL` for interactive sessions. |
| `runtime` | stub (remote) or server (local) | Like `claude` or `cursor-ide`. |
| `repo` | stub (remote) or server (local) | Like `github.com/acme/app`. The local server works it out from the workspace. |
| `deployment` | server | From the endpoint (remote) or the matched local deployment (local). |
| `factory_sha` | server | The exact factory commit that was served. |
| `stub` | stub | The stub fingerprint the run claimed, if any. |
| `served_by` | server | `local` or `remote`. |

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

### `skill_loads`

One row per `skill_load` during a run: `id`, `run_id`, `at` and `skill`.

### `run_overview`

A view with one row per run and its latest checkpoint: everything in `runs`, plus `last_status`, `last_summary`, `checkpoint_count` and `skills_loaded` (a comma-separated list).

## Handy queries

The last ten runs:

```sql
SELECT started_at, runtime, repo, routine, intent, last_status
FROM run_overview
ORDER BY started_at DESC
LIMIT 10;
```

How often each routine failed in the last four weeks:

```sql
SELECT routine,
       COUNT(*) AS runs,
       SUM(last_status = 'failed') AS failed
FROM run_overview
WHERE routine IS NOT NULL
  AND started_at >= strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-28 days')
GROUP BY routine
ORDER BY failed DESC;
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
SELECT r.started_at, r.deployment, r.routine, r.runtime, r.repo, c.summary
FROM checkpoints c JOIN runs r USING (run_id)
WHERE c.author = 'fabrica'
ORDER BY c.at DESC;
```

Which skills get used the most:

```sql
SELECT skill, COUNT(DISTINCT run_id) AS runs
FROM skill_loads
GROUP BY skill
ORDER BY runs DESC;
```

Every PR a routine touched:

```sql
SELECT r.routine, l.value AS link
FROM checkpoints c
JOIN runs r USING (run_id), json_each(c.links) l
WHERE l.value LIKE '%/pull/%';
```
