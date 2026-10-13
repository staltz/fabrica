# Getting started

This tutorial walks you through Fabrica twice. First you set it up just for yourself, on your laptop, the way a solo developer would. Then you add a cloud PR reviewer for a team, the way a team lead would. Each part takes about ten minutes.

If you haven't installed Fabrica yet, see [Install](../README.md#install) in the README. You'll also need `git`.

A quick refresher on the three kinds of repo, since everything below uses them:

- A **blueprint repo** holds your process: skills, routines, rules and connectors.
- A **home repo** holds factories: which blueprint runs on which product repos (its targets), in which tools.
- A **product repo** is your actual code. Fabrica only asks it for one sentence at the top of its `AGENTS.md`.

## Part 1: Just you, on your laptop

Let's say you want your favorite skills and rules in every repo you open, in both Claude Code and Cursor.

### 1. Write a blueprint

The quickest way is to start from the [starter blueprint](../examples/starter-blueprint/) that comes built into Fabrica:

```sh
fabrica new ~/code/my-blueprint
cd ~/code/my-blueprint
git add . && git commit -m "My blueprint"
```

`fabrica new` writes the blueprint and runs `git init`, but leaves the commit to you. (To write your own blueprint from scratch instead, run `fabrica new --empty ~/code/my-blueprint`.)

Now check it:

```sh
fabrica check
```

```
Blueprint /home/you/code/my-blueprint
  5 skills, 3 routines, 4 rules, 2 connectors, 0 providers
  expects product repos to provide: test, deploy?
✓ No problems found.
```

`fabrica check` is strict on purpose. If you mistype a key, like `alwaysapply` instead of `alwaysApply`, it stops and tells you, instead of quietly ignoring your rule. Try it: break something and run `fabrica check` again.

Push your blueprint somewhere git can reach, like `github.com/you/my-blueprint`. While you're just trying things out, a local path works too.

### 2. Set up your personal home repo

Your personal home repo lives at `~/.fabrica` (set `FABRICA_HOME` to put it somewhere else). It's a normal git repo, so you can push it and clone it on your next laptop.

You don't need to make it: the first `fabrica` command you ran made it for you, with `git init`, an empty `factories/` folder and a `.gitignore` that keeps local run history (`runs.db`) out of git. Nothing is committed yet.

Add a **local factory**: "use my blueprint in every repo, in Claude Code and Cursor".

```yaml
# ~/.fabrica/factories/my-default.yaml
blueprint: { git: github.com/you/my-blueprint, ref: main }
runtimes: [claude-code-local, cursor-local]
targets: ["*"]
```

A few things to notice:

- `ref: main` is a branch, so this factory **follows** it. When you merge a change to `main`, your next session picks it up. No re-apply.
- `targets: ["*"]` means "every repo that no other local factory claims". The quotes matter: a bare `*` means something else in YAML.
- Local factories only live in your personal home repo. A team's home repo can't have them.

### 3. Plan and apply

```sh
fabrica plan
```

```
Factory my-default (local)
  · blueprint github.com/you/my-blueprint@main → 72ef761 (branch main; follows the branch: merges take effect on the next run, no apply needed)
  · targets: ["*"] matches every repo not covered by another local factory; each repo still needs the Fabrica line, which is checked when a session starts
  claude-code-local: api mode, skills unpacked on disk
  cursor-local: api mode, skills unpacked on disk

Local tools on this machine
  claude-code-local (~/.claude.json, ~/.claude/settings.json)
    + add MCP server fabrica
  cursor-local (~/.cursor/mcp.json, ~/.cursor/hooks.json)
    + add MCP server fabrica

Plan: 2 changes, 0 errors. Run `fabrica apply` to make these changes.
```

`plan` never changes anything. It exits with `2` here because there are changes to make. Now apply them:

```sh
fabrica apply
```

That adds one user-level MCP server called `fabrica` to Claude Code and Cursor. It leaves everything else in those config files alone, and it never writes into a product repo.

You only need to run `apply` again when the *installs* change: a new local tool, or a local routine whose trigger changed. Changing which blueprint a repo uses never needs an apply, because `fabrica mcp` works that out fresh every time a session starts.

### 4. Add the Fabrica line to a product repo

Open a product repo and make this the very first line of its root `AGENTS.md`:

```
Before starting, load this repo's working rules from the Fabrica MCP server; if you don't have it, ignore this line.
```

If the repo also has a root `CLAUDE.md`, put the same line at the top of that too (Claude Code ignores `AGENTS.md` whenever a `CLAUDE.md` exists).

Why a sentence in `AGENTS.md`? It's the one file that Codex, Cursor and Claude Code all load on their own. Putting the pointer there makes your blueprint's rules always-on in every tool, without Fabrica writing any files into your repo. And the second half of the sentence means teammates who don't use Fabrica can ignore it.

Fabrica checks for this exact sentence. If it's missing, Fabrica refuses to serve that repo, and says why, instead of quietly half-working.

Want to check a repo before opening it? Run this inside it:

```sh
fabrica check
```

It finds the factory that covers the repo and checks the Fabrica line, skill name clashes, and skills the blueprint expects the repo to provide.

### 5. Use it

Open the repo in Claude Code or Cursor and ask for something. Behind the scenes, the agent:

1. calls `run_begin` with a short summary of what you asked for,
2. calls `rules_get` and follows your rules,
3. calls `skills_list` and loads what it needs with `skill_load` (locally, Fabrica unpacks each skill's folder into `~/.cache/fabrica/skills/`, so its scripts can run),
4. reports progress with `run_report`.

Every session is recorded in `~/.fabrica/runs.db`. Have a look:

```sh
sqlite3 ~/.fabrica/runs.db "SELECT started_at, repo, intent, last_status FROM run_overview ORDER BY started_at DESC LIMIT 10"
```

### 6. Add a local routine (optional)

Routines are things an agent does automatically when something happens. The starter blueprint has one for your laptop, `review-before-push`, which reviews your diff before you push. Add it to a factory:

```yaml
# ~/.fabrica/factories/my-default.yaml
blueprint: { git: github.com/you/my-blueprint, ref: main }
runtimes: [claude-code-local, cursor-local]
targets: ["*"]
routines: [review-before-push]
```

Run `fabrica plan` (it now wants to add a hook) and `fabrica apply`. Now when the agent tries to `git push`, the hook holds the push back once and tells the agent to run the routine first. After the review, the push goes through.

## Part 2: A team, with a cloud PR reviewer

Now let's say you lead a team. You want every new pull request in `github.com/acme/app` reviewed automatically, by Claude routines and Cursor automations, following the same process.

### 1. A blueprint with a PR-review routine

The starter blueprint already has a `pr-review` routine. Here's the gist of it:

```markdown
<!-- routines/pr-review.md -->
---
trigger: { on: github.pull_request.opened }
skills: [pr-review]
connectors: [github, slack?]
side_effects:
  - github.pr_comment
  - slack.notify: { channel: "{{params.slack_channel}}" }
params:
  slack_channel: { required: false, default: "#eng" }
---
Review the pull request that was just opened, using the `pr-review` skill. …
```

The routine says *what* should happen, not how any one tool does it. Each runtime's **provider** translates that. For example, `slack?` means Slack is optional: on a runtime without Slack, the routine still runs and just skips the Slack message.

Put the blueprint in a repo your team can reach, like `github.com/acme/blueprint`. Many teams keep a `stable` branch for production and use `main` for trying things out.

### 2. Run a Fabrica server

Cloud runtimes can't run `fabrica mcp` on your laptop, so they talk to a Fabrica server you host. It's one small container. You need:

- a long random **server secret**, for example from `openssl rand -hex 32`,
- read access to your home repo and your blueprints,
- a persistent folder for the run-history database.

```sh
docker build -t fabrica .          # from a checkout of this repo
docker run -d --name fabrica \
  -e FABRICA_SERVER_SECRET=… \
  -v fabrica-data:/data \
  -p 8080:8080 \
  fabrica mcp --http --home https://github.com/acme/home.git
```

Put it behind HTTPS at a URL like `https://fabrica.acme.dev`. The details, including private repos, are in [The MCP server](mcp-server.md#running-the-remote-server).

### 3. A team home repo with a cloud factory

Make a home repo for the team, like `github.com/acme/home`, and add a **cloud factory**:

```yaml
# factories/pr-review.yaml
blueprint: { git: github.com/acme/blueprint, ref: stable }
server: https://fabrica.acme.dev
runtimes: [claude-code-cloud, cursor-cloud]
routines: [pr-review]
targets: [github.com/acme/app]
params:
  slack_channel: "#eng"
```

The file name is the factory's ID (`pr-review`). Its endpoint on your server is `https://fabrica.acme.dev/pr-review`, and Fabrica works that out for you.

This factory expands into one stub per routine × target × runtime: here, 1 × 1 × 2 = 2.

Make sure `github.com/acme/app` has the Fabrica line at the top of its `AGENTS.md` on its default branch, then commit and push the home repo.

### 4. Plan

```sh
cd home && fabrica plan
```

```
Factory pr-review (cloud)
  endpoint https://fabrica.acme.dev/pr-review
  · blueprint github.com/acme/blueprint@stable → 72ef761 (branch stable; follows the branch: merges take effect on the next run, no apply needed)
  · cursor-cloud uses handoff mode because CURSOR_API_KEY isn't set; set it, and Fabrica manages cursor-cloud through its API instead
  claude-code-cloud: handoff mode, text-only skills, token in the url
    routine pr-review: trigger github.pull_request.opened [native] · connectors github [native], slack [native] · side effects github.pr_comment [emulated], slack.notify [emulated] · stub e128f7aa2995
  cursor-cloud: handoff mode, text-only skills, token in the header
    routine pr-review: trigger github.pull_request.opened [native] · connectors github [native], slack [native] · side effects github.pr_comment [emulated], slack.notify [emulated] · stub f1b3f4b57a0a
  Changes:
    + create pr-review @ github.com/acme/app on claude-code-cloud (handoff): it isn't installed yet
    + create pr-review @ github.com/acme/app on cursor-cloud (handoff): it isn't installed yet

Plan: 2 changes, 0 errors. Run `fabrica apply` to make these changes.
```

The plan shows how each runtime handles each part of the routine. **Native** means the runtime has it built in, **emulated** means Fabrica turns it into prompt instructions plus the connector that carries it out, and **unsupported** would be an error.

It also reads `github.com/acme/app`'s default branch to check the Fabrica line and look for skill name clashes. Cloud runs can't look at the workspace themselves, so this is where those problems get caught.

### 5. Apply, then follow the handoffs

`apply` needs the same server secret as the server, so it can compute each factory's token:

```sh
export FABRICA_SERVER_SECRET=…
fabrica apply
```

```
Factory pr-review: took the lock.
  ✓ + create pr-review @ github.com/acme/app on claude-code-cloud (handoff): it isn't installed yet (handoff written to ~/.cache/fabrica/handoffs/pr-review/h_19345343ec.md)
  ✓ + create pr-review @ github.com/acme/app on cursor-cloud (handoff): it isn't installed yet (handoff written to ~/.cache/fabrica/handoffs/pr-review/h_0eabba279a.md)
Factory pr-review: recorded the results and released the lock.
```

Here's what happened:

1. Apply took the factory's **lock**, by committing a `lock:` entry to `state/pr-review.yaml` and pushing it. Git only accepts a push on top of the latest commit, so if two applies race, exactly one wins, and the other stops without touching anything.
2. Claude routines have no API for creating triggered automations yet, and Cursor's API needs a Cursor API key, which isn't set here. So instead of calling an API, Fabrica wrote a **handoff** for each: step-by-step instructions that you, or an AI with computer use, can follow.
3. It recorded the handoffs as **pending** in the state file and released the lock, in one commit.

Open a handoff. It tells you exactly what to set up: the routine's name (its Fabrica identity), repo, trigger, connectors (including the Fabrica server with this factory's token), and the stub prompt to paste in. The prompt is tiny. It just tells the agent to call `run_begin` and then load the real routine with `routine_get`.

The handoff files contain the factory's token, so they're written outside the home repo, readable only by you.

When you've set it up, tell Fabrica, passing the ID the runtime gave it:

```sh
fabrica confirm h_19345343ec --remote-id trig_01Hx…
```

If you can't find an ID, leave `--remote-id` out. Fabrica then tracks the resource as **unverifiable**, and `plan` says so.

Cursor can skip most of this. Set `CURSOR_API_KEY` to a Cursor API key (as a CI secret, if apply runs in CI), and `cursor-cloud` switches to **api** mode: apply creates, updates and deletes the Cursor automation itself, with nothing to confirm. The one thing Cursor's API can't do is set up the automation's MCP servers, so apply writes down which ones to add to Cursor's MCP settings (here, `fabrica-pr-review`, with this factory's token), once. See [Cursor automations through Cursor's API](providers.md#cursor-automations-through-cursors-api).

### 6. Improving the process is a merge

Now say someone improves the `pr-review` skill and merges that to `stable`. The next PR review uses the new version, with no apply and no handoff, because the stub only says "load your instructions from Fabrica".

What *does* need an apply is a change to the routine's **shape**: its trigger or its connectors (and, on runtimes with their own memory feature, its `memory:` setting). Those are baked into the runtime. If someone changes the trigger, the Fabrica server notices on the next run that the stub's fingerprint doesn't match anymore, and **refuses to run it**, with an error that says the factory needs `fabrica apply`. Meanwhile `fabrica plan` lists the stub as an update. That's deliberate: a new trigger or connector is exactly the kind of thing someone should review before it's installed.

### 7. Put it in CI

Two jobs work well in the home repo's CI:

- **On every merge to `main`:** `fabrica apply`, so factory changes roll out after review. Use your CI's concurrency setting so applies wait in line.
- **Every night:** `fabrica plan`. It fails (exits non-zero) on errors and on stubs that went stale, like a product repo that lost its Fabrica line, or a trigger change nobody applied yet.

There's a ready-made GitHub Actions workflow in [examples/home/.github/workflows/fabrica.yml](../examples/home/.github/workflows/fabrica.yml).

## Where to next

- [Writing a blueprint](blueprints.md) covers the blueprint format in detail, including imports.
- [Home repos](home-repos.md) covers factories, state and the apply lock.
- [Safety checks](safety-checks.md) lists every way Fabrica fails closed.
