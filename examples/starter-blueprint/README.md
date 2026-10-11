# Starter blueprint

This is a small, ready-to-use [Fabrica](https://github.com/staltz/fabrica) blueprint for a team of a few developers. It writes down one sensible way of working with AI coding agents: write the test first, keep changes small, review every pull request, and write commits and changelogs that people can read.

You can use it as it is, copy it and change it, or import the parts you like into your own blueprint.

Everything here is about **process**: how we work. None of it knows anything about your product, like which language, test runner or database you use. That knowledge belongs in your product repos, and the [expects](#what-expects-test-deploy-means) section below explains how the two meet.

## What's inside

```text
fabrica.yaml                    the manifest: a description and what product repos should provide
skills/
  pr-review/                    how to review a change (plus checklist.md)
  test-first/                   red, green, refactor
  commit-messages/              Conventional Commits, and why over what
  debugging/                    reproduce, shrink, hypothesize, bisect, fix
  changelog/                    Keep a Changelog, in words users understand (plus template.md)
routines/
  pr-review.md                  reviews each new pull request (cloud)
  weekly-dependency-check.md    reports outdated and vulnerable dependencies every Monday (cloud)
  review-before-push.md         reviews your own commits before a push (local)
rules/
  tests-first.mdc               always on: a failing test comes before a behavior change
  small-changes.mdc             always on: one focused change at a time
  no-secrets.mdc                always on: never write, print or share secrets
  docs-style.mdc                when editing Markdown: how to write docs
connectors/
  github.yaml                   GitHub, built in, with an MCP fallback for local tools
  slack.yaml                    Slack, built in
```

Here's how the four kinds of item work together:

- **Rules** are always on (or, for `docs-style`, on whenever the agent touches a `.md` file). They're short, so they fit in every session.
- **Skills** hold the longer how-to. The agent loads one when it's relevant, and rules point to them by name, like "the `test-first` skill has the whole workflow".
- **Routines** run by themselves when something happens: a pull request is opened, it's Monday at 9:00 (UTC), or the agent is about to `git push`.
- **Connectors** are what routines use to reach the outside world. Here that's GitHub and Slack.

### The routines

| Routine | Fires on | What it does | What it may change |
|---|---|---|---|
| `pr-review` | a new pull request | Reviews it with the `pr-review` skill, posts one review comment, and sends a one-line summary to Slack. | A PR comment, and a Slack message to `slack_channel` (default `#eng`). |
| `weekly-dependency-check` | Mondays at 09:00 UTC (`0 9 * * 1`) | Looks for outdated and vulnerable dependencies and writes up what it finds. | Opens one issue labeled `issue_label` (default `dependencies`), or comments on it if it's already open. |
| `review-before-push` | the agent running `git push` in your local tool | Reviews the commits you're about to push, and holds the push if it finds a blocker. | Nothing. It only talks to you. |

Slack is optional (`slack?` in the routine), so `pr-review` still works on runtimes without Slack. It skips that step.

### Every skill is text-only

Every file in every skill is Markdown. There are no scripts. That matters because cloud runtimes can only use **text-only** skills in this version of Fabrica: they read skill files one at a time over MCP, and can't run scripts from them. Keeping everything text-only means every skill works everywhere, in the cloud and on your machine. If you add a skill with scripts, it'll still work locally, but `fabrica plan` (and `fabrica check` on a home repo) will stop you from using it in a routine deployed to a cloud runtime.

## What `expects: [test, deploy?]` means

Some things only the product repo knows. "Write a failing test first" is process, so it's in this blueprint. "Run `make test`, and start Postgres first" is product knowledge, so it's not.

The manifest says so:

```yaml
expects:
  - test      # how this repo runs its tests
  - deploy?   # how this repo ships, if it ships at all
```

That's a promise each product repo is asked to keep. The repo provides its own skill named `test`, and optionally one named `deploy`, as ordinary repo skills with no Fabrica syntax at all. For example, in `.agents/skills/test/SKILL.md`:

```markdown
---
name: test
description: How to run this repo's tests. Use it whenever you need to run, add or fix tests.
---

Run every test with `make test`. Run one package with `go test ./internal/billing/...`,
and one test with `go test ./internal/billing -run TestRefund`.

The integration tests need Postgres. Start it first with `docker compose up -d db`.
```

Fabrica also looks for repo skills in `.claude/skills/`, `.cursor/skills/`, `.codex/skills/` and `.github/skills/`, so use whichever folder your repo already has.

Here's where the blueprint uses them:

- `test-first`, the `tests-first` rule and the `review-before-push` routine all load the repo's `test` skill to find out how to run tests.
- `changelog` follows the repo's `deploy` skill as the last step of a release, if there is one. The `pr-review` checklist uses it too, for changes that affect deployment. The `?` makes it optional. A repo without a `deploy` skill works fine: at release time, the agent stops and tells you the release is ready.

`fabrica check` on a product repo tells you which expected skills it's missing. A missing `test` is a warning, and a missing `deploy` is only a note.

## How to use it

There are two ways. Copy it if you want to change a lot. Import it if you want to keep getting updates.

### Option 1: copy it

This blueprint is built into `fabrica`, so one command writes a copy of it into a new repo:

```bash
fabrica new ~/code/my-blueprint
cd ~/code/my-blueprint
git add . && git commit -m "feat: start from the Fabrica starter blueprint"
```

`fabrica new` runs `git init` for you, unless the folder is already inside a git repo (a blueprint can live in a subfolder too). The copy is the version that came with your `fabrica` binary.

Now it's yours. Edit anything you like, then run `fabrica check .` to catch mistakes. You won't get upstream improvements, but you won't get surprises either.

### Option 2: import it

In your own blueprint's `fabrica.yaml`, import it and say which parts you want.

Take everything except one skill, and write your own version of that skill:

```yaml
imports:
  starter:
    git: github.com/staltz/fabrica
    ref: main                     # better: a tag or a commit, so nothing changes under you
    path: examples/starter-blueprint
    exclude: [skills/pr-review]   # we have our own skills/pr-review/SKILL.md

expects:
  - test
  - deploy?
```

Your own `skills/pr-review` then replaces the imported one everywhere, including in the imported `pr-review` and `review-before-push` routines, because they refer to it by name.

Or take only a few parts:

```yaml
imports:
  starter:
    git: github.com/staltz/fabrica
    ref: main
    path: examples/starter-blueprint
    only:
      - skills/test-first
      - skills/commit-messages
      - rules/tests-first
      - rules/no-secrets

expects:
  - test
  - deploy?
```

Then run `fabrica lock`. It pins the import to an exact commit in `fabrica.lock`, so upstream changes only arrive when you run `fabrica upgrade`, which shows you a diff first.

A few things to know about imports:

- **`only:` is literal.** It brings exactly what you list, nothing more. If you take `routines/pr-review`, you also need `skills/pr-review`, `connectors/github` and `connectors/slack`. Don't worry about getting it wrong: `fabrica lock` tells you exactly what's missing.
- **Use `only:` or `exclude:`**, never both on one import.
- **`expects:` isn't inherited.** Your `expects:` list is the whole promise your blueprint makes to product repos, so copy `test` and `deploy?` into it, like the first example does. `fabrica lock` warns you if you forget.
- **You can't rename on import.** Skills mention each other by name in their text, and renaming would quietly break those mentions. To change an item, `exclude:` it and write your own with the same name.

## Try it on your machine

The quickest way to see it working is a **local factory**. It serves this blueprint to Claude Code and Cursor on your laptop, for one product repo, and nothing leaves your machine.

You'll need the `fabrica` binary and a product repo of your own that you can push to. The steps below use `github.com/you/your-app`; change it to yours.

**1. Add a factory to your personal home repo** (`~/.fabrica`, unless you set `FABRICA_HOME`). Create `~/.fabrica/factories/starter.yaml`:

```yaml
blueprint:
  git: github.com/staltz/fabrica
  ref: main
  path: examples/starter-blueprint
runtimes: [claude-code-local, cursor-local]
targets: [github.com/you/your-app]
routines: [review-before-push]
```

To try your own copy instead, point `git:` at it. A local path works too, like `git: /home/you/code/my-blueprint`, but Fabrica reads committed files only, so commit your edits first.

Add `codex-cli` to `runtimes:` if you use Codex CLI too. Only `review-before-push` goes under `routines:`, because it's the only local routine. The other two fire on GitHub events and schedules, so they need a cloud runtime (see below). Skills and rules are served either way.

**2. Check the home repo:**

```bash
fabrica check ~/.fabrica
```

**3. Prepare the product repo.** Make the very first line of its root `AGENTS.md` this exact sentence (and the same for its root `CLAUDE.md`, if it has one):

```text
Before starting, load this repo's working rules from the Fabrica MCP server; if you don't have it, ignore this line.
```

Then add a `test` skill, like the example above, in `.agents/skills/test/SKILL.md`.

**4. Check the product repo** against the blueprint:

```bash
cd ~/code/your-app
fabrica check
```

It should find the Fabrica line, and no missing `test` skill. If you're editing a copy of this blueprint and haven't committed it yet, check against the folder instead: `fabrica check --blueprint ~/code/my-blueprint`.

**5. Install it** in your local tools:

```bash
fabrica apply
```

This adds a `fabrica` MCP server to Claude Code and Cursor, plus a hook that starts `review-before-push` whenever the agent runs `git push`. It never writes into your product repo. (The hook lives in the tool, not in git, so pushes you type in your own terminal aren't held back.)

**6. Try it.** Open the product repo in Claude Code or Cursor and ask "what are your working rules?". The agent should describe the rules from this blueprint, like `tests-first` and `small-changes`. Then make a small change, commit it, and ask the agent to push. It'll review its own commits first, and only push if it finds no blockers.

## Running the cloud routines

`pr-review` and `weekly-dependency-check` run in cloud runtimes, like Claude routines, Cursor automations or Codex. For those you need your own Fabrica server (`fabrica mcp --http`) and a cloud factory in your home repo, for example `factories/review.yaml`:

```yaml
blueprint:
  git: github.com/staltz/fabrica
  ref: main
  path: examples/starter-blueprint
server: https://fabrica.example.com
runtimes: [claude-code-cloud, cursor-cloud]
routines: [pr-review, weekly-dependency-check]
targets:
  - github.com/you/your-app
  - repo: github.com/you/your-api
    params: { slack_channel: "#api" }
params:
  slack_channel: "#eng"
```

Run `fabrica plan` to see what would be set up, then `fabrica apply`. Most cloud runtimes have no API for creating routines yet, so `apply` writes step-by-step instructions for you to click through, and you finish with `fabrica confirm`.

## Changing it

- **Keep it about process.** If a sentence only makes sense for one product, like "we use pnpm" or "the tests need Postgres", it belongs in that product's repo instead.
- **Keep skills text-only**, so cloud routines can use them.
- **Run `fabrica check .`** after every edit. It catches typos in frontmatter, missing skills and connectors, and routines whose prompt mentions a skill they don't list in `skills:`.
- **Say why.** Every rule and skill here explains its reasons. People and agents both follow a rule better when they understand it, and it makes it much easier to tell later whether a rule still makes sense.
