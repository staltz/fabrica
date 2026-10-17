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

## Reading it

Run history is for people to read, not agents. From your terminal, `fabrica runs` does it:

- `fabrica runs` lists the latest runs, newest first.
- `fabrica runs show <run>` shows one run in full: what it loaded, what it reported and what slowed it down.
- `fabrica runs export` writes a [retro bundle](#the-retro-bundle), a folder of Markdown and JSON for you or your own retro skill.

```
$ fabrica runs --since 3d
STARTED       OUTCOME    ROUTINE    REPO      INTENT                RUN
Oct 17 09:41  succeeded  pr-review  acme/app  Review PR 412 for t…  run_06gmjeps
Oct 17 08:02  stale      —          acme/app  Fix flaky checkout …  run_06gmj9xq
Oct 16 22:15  failed     autofix    acme/api  Fix lint failure on…  run_06gmh2aa

3 runs from factory acme. Show one with: fabrica runs show <run>
```

It reads the `runs.db` of your local sessions. To read a remote server's history, add `--server` with the server's URL or the name of one of its factories, and have its admin token ready (`FABRICA_ADMIN_TOKEN`, or the server secret that `fabrica auth login` saved). Local and remote history are never merged: you read one or the other. Filters, flags and the rest are in the [CLI reference](cli.md#fabrica-runs).

Two things to keep in mind while reading:

- **Outcomes are worked out, not reported.** A run's outcome follows its latest checkpoint, and a run that went quiet for 6 hours is `stale`. See [How `outcome` works](#how-outcome-works).
- **Agents wrote most of what you read.** Intents, summaries, statuses, friction notes and session pointers are best effort. Times, blueprint commits, factories and loads are Fabrica's own, so those you can trust.

## The retro bundle

`fabrica runs export` writes a retro bundle: a folder you hand to a person, or to your own retro skill. Fabrica doesn't ship a retro skill; the bundle is what it provides. The format is a public interface, like the [schema](#the-schema): Fabrica adds to it carefully, and `schema_version` in `runs.json` is the [schema version](#the-schema) the runs come from.

```
fabrica-runs-20261017-1004/
  README.md        what this is, the filters used, totals, and how to read it
  runs.json        {"schema_version": 3, "filters": {…}, "runs": [<run in full>, …]}
  runs/
    run_06gmjeps8jf8zfjjrfmydev50g.md   one run, the same as `fabrica runs show`
    …
```

Fabrica only writes into a folder that isn't there yet, or is empty. The default name comes from the local time you ran it.

### `README.md`

Start here. It's Markdown, and it always has these parts, in this order:

1. **What this is.** One paragraph: when and where it was made, which factory, which server (or your own machine's history), which time window and filters, how many runs, and whether more runs matched than the bundle holds (`--limit`).
2. **Totals**, worked out from the runs in the bundle:
   - runs by outcome;
   - runs by routine, with how many succeeded, failed and went stale, and a success rate. The rate is the runs that succeeded out of those that succeeded or failed, so stale and in-progress runs aren't in it. Interactive runs are one row of their own;
   - friction notes by kind;
   - the most-loaded skills and rules, 10 of each at most, by how many runs loaded them;
   - refusals by reason: the runs Fabrica refused itself, from its own failed checkpoints.
3. **The runs**, grouped by outcome: failed first, then stale, succeeded and in progress. Each line links to the run's file. Failed and stale ones also say the last thing they reported.
4. **Where to look next.** Pointers to what's outside the bundle:
   - **Instructions the runs were served.** Every blueprint commit in the bundle, with the `git show <commit>:<path>` command for each routine it served. Run it in a clone of your blueprint repo to read the exact routine an agent got.
   - **Pull requests.** Every PR (or merge request) the runs linked, with the runs that touched it. The review comments on them are often the best sign of what went wrong.
   - **Transcripts.** The session pointers that are files on this machine, so a tool can read them. Pointers that are URLs or session IDs aren't listed here; they're in each run's file.
5. **Fields you can't fully trust.** Intents, summaries, statuses, friction notes, links and session pointers are written by agents on a best-effort basis. Times, blueprint commits, factories and loads are written by Fabrica.

### `runs.json`

The same runs as JSON, for programs:

```json
{
  "schema_version": 3,
  "filters": {"routine": "pr-review", "since": "2026-10-03T07:04:00Z", "limit": 200},
  "runs": [ … ]
}
```

- **`filters`** is what the bundle was asked for. `factory`, `routine`, `repo` and `runtime` are there only when you used them, `outcomes` is a list that's there when you gave `--outcome`, and `since` and `limit` are always there. `since` is the moment `--since` meant when it ran, as an RFC 3339 time in UTC. The server and the time it was made are only in the README.
- **`runs`** are newest first, and each one is exactly what the [admin API](mcp-server.md#the-admin-api) answers for `GET /_admin/runs/<run_id>`: the run's fields, its `stub`, and its `timeline` of loads, checkpoints and friction notes. It's always a list, even when it's empty.

### `runs/<run_id>.md`

One file per run, with the same content as `fabrica runs show`, as Markdown: the run's fields, a timeline (consecutive loads of one kind are a line, and Fabrica's own checkpoints are marked `fabrica`), and the `git show` commands for what it was served. Dates include the year (`2026-10-17 09:41:02`), because the file will be read some other day. Times are in the time zone of the machine that made the bundle, and the file says which.

### What isn't in it

**Transcripts.** They can be huge and hold anything, so the bundle only points at them. If a run's session pointer is a file on your machine, the README lists it.

Text from agents is cleaned when it's written to the Markdown files: line breaks and control characters become spaces, so a summary can't add headings or lists to the file, or send commands to your terminal. `runs.json` keeps the text as the agent wrote it.

### Working from a bundle

A retro skill (or you) can start with `README.md`: the failed and stale runs come first, then the friction totals. From there:

- Read the friction notes in the runs' files. Their kinds (`navigation`, `checks`, `standards`, `tools`, `info`, `other`) are the usual retro categories.
- For failed and stale runs, read the transcript when the README lists one.
- Read the review comments on the linked PRs.
- Use the `git show` commands to read what the runs were actually served. A rule that every run loads but that never matters in any transcript might be a no-op.
- Fix what you find in the blueprint (a skill, rule or routine change) or in the product repo (AGENTS.md, a lint rule, a test).

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
