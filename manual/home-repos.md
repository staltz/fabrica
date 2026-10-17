# Home repos

A home repo holds **factories**. A factory is a deployment of a blueprint: it says which blueprint runs on which product repos (its **targets**), in which runtimes. A home repo is an ordinary git repo, so it's versioned, durable and works across machines.

```
factories/<name>.yaml             # what to deploy where (you write these)
state/<name>.yaml                 # what's installed, one per cloud factory (only Fabrica writes these)
```

## Personal and team home repos

- Your **personal** home repo is the folder `FABRICA_HOME` points to, or `~/.fabrica` by default. It can hold both local and cloud factories. Your local run history (`runs.db`) lives in it too, so `runs.db*` belongs in its `.gitignore`.

  You never need to make it yourself. Every `fabrica` command, before doing anything else, creates it if it isn't there yet: it runs `git init`, adds an empty `factories/` folder and a `.gitignore` with `runs.db*`, and says so. It never commits. If the folder is a git repo already, say because you cloned your home repo there, Fabrica leaves it exactly as it is, so add `runs.db*` to its `.gitignore` yourself.
- A **team** home repo is any other home repo, owned by a team. In v0.1 it only holds cloud factories: a local factory in one is an error.

Most commands work on one home repo. They pick it like this: the `--home` flag if you give one, otherwise the current folder if it looks like a home repo (it has `factories/` or `state/`), otherwise your personal home repo.

## Cloud factories

A factory says: *this blueprint, these routines, these targets, these runtimes, these params.*

```yaml
# factories/pr-review.yaml
blueprint: { git: github.com/acme/blueprint, ref: stable }
server: https://fabrica.acme.dev
runtimes: [claude-code-cloud, cursor-cloud]
routines: [pr-review]
targets:
  - github.com/acme/app
  - repo: github.com/acme/api
    params: { slack_channel: "#api" }     # per-repo override
params:
  slack_channel: "#eng"
```

| Key | Required | What it is |
|---|---|---|
| `blueprint` | yes | `git` and `ref` (and optionally `path`) of the blueprint, like an import. |
| `server` | for cloud | The URL of your self-hosted Fabrica server. Must be `https` (plain `http` only for localhost). |
| `alias` | no | For cloud factories: a name, like `review`, that runtimes show in front of each routine's name instead of the factory's [tag](concepts.md#identities), a short hash. Lowercase letters, digits and hyphens. No two factories in a home repo may share a tag or alias. |
| `runtimes` | yes | Where to run: built-in runtimes, or ones from the blueprint's [folder providers](providers.md#folder-providers). A factory is either all cloud or all local. |
| `routines` | for cloud | Which of the blueprint's routines to deploy. Each is a name, or `{ routine, runtimes }` to run it on only some of the factory's runtimes. `"*"` stands for all of them, see [below](#all-routines). |
| `targets` | yes | The product repos the factory runs on. Each is a repo name, or `{ repo, params }`. |
| `params` | no | Param values for every repo. |
| `init` | no | `false` never runs the blueprint's [init routine](blueprints.md#init-routines) for this factory. See [Init](#init). |

- **The file name is the factory's ID.** `factories/pr-review.yaml` is the factory `pr-review`.
- **It expands to every combination** of routines × targets × runtimes. The example creates 1 × 2 × 2 = 4 remote resources, each tracked separately. A routine can be narrowed to some of the runtimes, see [below](#routines-on-some-runtimes).
- **The endpoint** is `server` plus the factory's name: `https://fabrica.acme.dev/pr-review`. Fabrica builds it for you.
- **Repo names** are always written as `host/owner/repo`. Git remotes like `git@github.com:acme/app.git` are turned into that form, in lowercase.
- **Overlap is allowed.** Several factories, from different home repos and different blueprints, may target the same repo. Two PR reviewers on one repo is a people problem, not one the tool prevents.

### Routines on some runtimes

By default every routine runs on every runtime of its factory. To run a routine on only some of them, write it as `{ routine, runtimes }`:

```yaml
# factories/acme-app.yaml
blueprint: { git: github.com/acme/blueprint, ref: stable }
server: https://fabrica.acme.dev
runtimes: [claude-code-cloud, cursor-cloud]
routines:
  - pr-review                                              # on both runtimes
  - { routine: release-notes, runtimes: [claude-code-cloud] }
targets: [github.com/acme/app]
```

This creates 3 remote resources: `pr-review` on both runtimes, and `release-notes` only on `claude-code-cloud`. Since `release-notes` never meets `cursor-cloud`, it doesn't matter whether Cursor supports its trigger.

- A routine's `runtimes` must be listed in the factory's own `runtimes:`, which still decide whether the factory is cloud or local.
- In a cloud factory, every runtime must have at least one routine running on it. A runtime with none is an error, since it would install nothing.
- Narrowing a routine to fewer runtimes works like removing a runtime: `plan` shows its stubs on the other runtimes as deletes, and the server refuses their runs straight away.
- Locally, it decides which tools get the routine's hook or scheduled task. Every runtime of a local factory still serves its blueprint over MCP.

### All routines

To deploy every routine of the blueprint, write `"*"` (quoted, like the target wildcard) instead of naming them. It takes `runtimes` too, and any routine you name is an exception to it:

```yaml
# factories/acme-app.yaml
blueprint: { git: github.com/acme/blueprint, ref: stable }
server: https://fabrica.acme.dev
runtimes: [claude-code-cloud, cursor-cloud]
routines:
  - { routine: "*", runtimes: [claude-code-cloud] }        # everything on Claude…
  - { routine: release-notes, runtimes: [cursor-cloud] }   # …but this one on Cursor
  - { routine: review-before-push, runtimes: [] }          # …and this one nowhere
targets: [github.com/acme/app]
```

- **A routine you name follows its own entry,** never the wildcard's. `release-notes` runs only on `cursor-cloud`, not on both.
- **`runtimes: []` leaves a routine out.** Use it for routines that can't run here, like a local routine in a cloud factory. It's only allowed next to `"*"`.
- **`routines: ["*"]`** runs every routine on every runtime of the factory.
- **It follows the blueprint.** A routine added to the blueprint is deployed by the next apply, and one removed from it is deleted, without editing the factory. `plan` shows both. With a frozen `ref`, that only happens when you move the `ref`.
- **The init routine isn't included.** It runs by itself when the factory is created (see [Init](#init)).
- Since `plan` can't know what `"*"` covers until it reads the blueprint, it checks then that every runtime still gets at least one routine (`factory.idle-runtime`).

### Params

Params are set once at the top, and individual targets can override them. Every param must be declared by one of the factory's routines, and every required param must have a value for every target. Both are errors otherwise, which catches typos.

If two runtimes need different settings, write two factory files.

### Init

When the blueprint has an [init routine](blueprints.md#init-routines), the apply that creates the factory starts a session for it, once, after creating the stubs. `plan` shows where it would run and why:

```
  init routine onboard (once, on the first of its runtimes Fabrica can start a session on):
      cursor-cloud: a person starts it, from a handoff (Fabrica can't by itself: CURSOR_API_KEY isn't set)
    → claude-code-local: Fabrica starts it
  Changes:
    + create pr-review @ github.com/acme/app on cursor-cloud (handoff): it isn't installed yet
    ! init   onboard @ github.com/acme/app on claude-code-local (api; Fabrica starts it): the factory is being applied for the first time
```

- **Once per life of the factory.** State records the init, so later applies don't run it again. `fabrica destroy` ends the factory's life: when it removes everything, it forgets the init and records when it destroyed the factory, so the next apply recreates the factory and runs the init again, with a new session. A destroy that failed partway hasn't ended it. A factory that was created before its blueprint got an init routine doesn't run it, until it's destroyed and recreated.
- **Failures are retried.** If the session can't start (the runtime refused, the Fabrica server wasn't ready, or other changes in the same apply failed), the next apply tries again. Every attempt uses the same init ID, and each runtime uses it to make sure there's never a second session: Cursor refuses a second cloud agent with the same ID, and a local session keeps its folder.
- **The Fabrica server has to serve the factory first.** It reads the home repo about once a minute, so before starting a session apply waits (up to 3 minutes) until the server answers for the new factory.
- **A handoff** waits for `fabrica confirm <init ID> --remote-id <the session's ID or URL>`, like any other. Until then, `plan` lists it as waiting, and no apply starts it again.
- **A local session** (on `claude-code-local`) runs in the background, in a fresh clone of the repo under `~/.cache/fabrica/sessions/<factory>/<init ID>/`, with a log file there. Nobody is there to answer permission prompts, so it runs in Claude Code's `auto` permission mode. Resume it with `claude --resume <session ID>` in that clone.
- `init: false` turns it off for the factory: say, for a new factory on repos that are set up already.

### Follow versus frozen

The blueprint's `ref` decides how the factory gets updates:

- **A branch** (`main`, `stable`) means **follow**. A merge to that branch takes effect on the next run, with no apply needed.
- **A tag or a commit** means **frozen**. Nothing changes until you edit the factory.

`fabrica plan` shows which mode each factory is in. If a name is both a branch and a tag in the blueprint repo, Fabrica refuses to guess and asks you to use a name that's only one of them.

## Local factories

A local factory is the same kind of file, with local runtimes, in your personal home repo:

```yaml
# factories/my-default.yaml
blueprint: { git: github.com/me/blueprint, ref: main }
runtimes: [claude-code-local, cursor-local]
targets: ["*"]                   # every repo not matched by another local factory
```

```yaml
# factories/oss-work.yaml
blueprint: { git: github.com/me/oss-blueprint, ref: main }
runtimes: [claude-code-local, cursor-local]
targets: [github.com/oss/project]
routines: [review-before-push]   # optional local routines
```

What happens:

- **`fabrica apply` on your laptop** installs one user-level `fabrica` MCP server in each local tool, plus a hook for each local routine. Tools with a session-start hook (not Cursor) also get a session pointer hook, which tells each session where its own transcript is. It never writes into a product repo. If a local routine needs an MCP connector, that gets registered too, as `fabrica-<connector>`.
- **Choosing a blueprint needs no apply.** Each time a session starts, `fabrica mcp` reads your local factories, matches the workspace's git remote, and serves that blueprint. Change a local factory, and the next session picks it up.
- **New machine?** Clone your home repo to `~/.fabrica` and run `fabrica apply` once. If you've already run `fabrica` there (the installer does), there's an empty home repo in the way, so remove it first with `rm -rf ~/.fabrica`.
- **The wildcard** `targets: ["*"]` (quoted, because a bare `*` means something else in YAML) is only allowed for local runtimes, and must be the only entry. A specific repo always beats the wildcard. There can be at most one wildcard factory per runtime.
- **Two specific local factories for the same repo and runtime is an error.** A session must get exactly one blueprint.
- **If no local factory matches,** `fabrica mcp` serves nothing and says why.
- **CI ignores local runtimes,** since there's nothing local for it to set up. When `$CI` is set, `plan` and `apply` skip local factories.

Fabrica finds its own local installs by their identifying marks: MCP servers named `fabrica` or `fabrica-…`, and hooks whose command is `fabrica hook …`. Local factories don't need a state file.

## State

Each cloud factory has one state file, `state/<factory>.yaml`. Only Fabrica writes it, and it's committed to the home repo. It records every remote resource the factory owns, what was installed in each one, any pending handoffs, and the factory's lock. Because each factory has its own file, people applying *different* factories never conflict.

```yaml
# state/pr-review.yaml
# Written by Fabrica. Don't edit by hand.
lock:                                    # only there while an apply is running
  holder: ci github.com/acme/home/actions/runs/4812
  since: 2026-10-10T12:00:00Z
blueprint:                               # the blueprint the last apply used
  git: github.com/acme/blueprint
  commit: 9c1e4b2a…
resources:
  - id: 4ckr#pr-review@github.com/acme/app
    runtime: claude-code-cloud
    routine: pr-review
    repo: github.com/acme/app
    remote_id: trig_01Hx…                # empty means unverifiable
    mode: handoff
    blueprint_commit: 9c1e4b2a…
    token_hash: 571f1676be88             # notices a rotated server secret
    shape:
      trigger: github.pull_request.opened
      connectors: [github, slack]
      fingerprint: e128f7aa2995
    applied_at: 2026-10-10T12:01:00Z
pending: []                              # handoffs waiting for `fabrica confirm`
destroyed_at: …                          # only after `fabrica destroy`, until the next apply recreates the factory
init:                                    # the factory's init session, once there's been one
  id: i_3f2a1c9e04                       # the same on every attempt
  routine: onboard
  runtime: cursor-cloud
  repo: github.com/acme/app
  mode: api
  status: started                        # starting, started, waiting (for confirm) or failed
  session: bc-6f1c…
  url: https://cursor.com/agents/bc-6f1c…
  blueprint_commit: 9c1e4b2a…
  at: 2026-10-10T12:01:00Z
```

The `token_hash` is a short one-way fingerprint of the token, never the token itself.

## Plan: changes and errors

`fabrica plan` compares what the home repo and its blueprints ask for with what's installed, and sorts everything it finds into two kinds:

- **Changes** are work for `apply`: resources to create, update, delete or adopt, and handoffs to issue. That includes repairing stubs that went stale after a merge. When a routine's trigger, connectors, (on runtimes with their own memory) memory setting, native side effects or (on runtimes that let a stub pick it) model change (to ones the runtime supports), its installed stub is an **update**. A stub whose routine, repo or runtime is no longer in its factory is a **delete**.
- **Errors** are problems `apply` can't fix: a trigger the runtime doesn't support, a product repo without the Fabrica line, duplicate identities, a held lock and so on. `apply` won't touch a factory while it has any. An error in one repo blocks the whole factory, so to unblock the others, fix that repo or take it out of the factory.

`plan` exits with `0` when there's nothing to do, `2` when there are only changes, and `1` when there are errors. Pending handoffs aren't apply's work, so on their own they don't change the exit code, but `plan` lists them.

`plan` reads the home repo as it is on disk, so you can plan your changes before you push them. When it has the server secret (saved by `fabrica auth login`, or in `FABRICA_SERVER_SECRET`), it also notices rotated tokens. Pass `--json` for machine-readable output.

## Apply

### How apply changes a cloud factory

1. **Plan** against the latest home repo. Apply fetches the remote and fast-forwards first, and refuses to run with uncommitted changes in `factories/` or `state/`, or with local commits that aren't pushed: it applies what's on the remote, never what's only on your machine. If the plan finds errors, apply lists them and skips that factory. If nothing needs to change, it stops without committing.
2. **Take the lock.** Commit a `lock:` entry to the state file and push. Git only accepts a push on top of the latest commit, so when two applies race, exactly one push gets through. The other takes its commit back, pulls, sees the lock, and stops with a message naming who holds it. Nothing remote has been touched yet, so losing the race is always safe.
3. **Refresh.** Look up all of this factory's resources in each runtime **by identity** (for runtimes with an API). A resource that exists but is missing from state gets adopted, like one created by an apply that crashed before recording it. A runtime whose API can't list resources (like Cursor's) is asked about each resource state records, by its remote ID, instead.
4. **Re-plan and apply.** If the re-plan finds errors (say, duplicates the refresh turned up), remove the lock and stop without touching any resource. Otherwise carry out each change, and then, on the factory's first apply, start its [init](#init) session (recording in state, before it starts, that it's starting). In a runtime that can't list resources, apply commits the state file (and pushes) before each create, lock still held, and again once the new ID comes back. A crash after the ID is recorded can't lose the resource. A lost answer, or a crash before the ID is recorded, leaves the create marked unfinished: the next plan reports it, and apply will not create another until `fabrica confirm` says whether it exists.
5. **Record and release.** Write the results and remove the lock in one commit, then push. If the push is rejected because of unrelated commits (like another factory's state), rebase and push again. While the lock is held, nobody else writes this file.

### Crashed applies

A crashed apply leaves its lock in place. Fabrica can't tell a crashed apply from a slow one, so it doesn't guess. `plan` and `apply` show who holds the lock and since when, and once you're sure that apply is gone:

```sh
fabrica unlock pr-review
```

The next apply's refresh adopts whatever the crashed one created. On a runtime that can't list resources, a create whose ID was recorded is already in state, so there's nothing to adopt. A create whose ID was never recorded (the answer was lost, or apply stopped mid-call) is an error in `plan`, not a new create: the resource may already exist, and a second one would run alongside it. Look for it, then `fabrica confirm <id> --remote-id <its ID>` if it's there, or `fabrica confirm <id> --absent` if it isn't.

### Handoffs

Some runtimes have no API Fabrica can use (as of v0.1: Claude routines, Codex, Copilot automations, and Cursor automations without a Cursor API key). For those, apply writes a **handoff**: step-by-step instructions that a person, or an AI with computer use, can follow. Fabrica never does computer use itself.

- Handoffs are written to `~/.cache/fabrica/handoffs/<factory>/<id>.md` (or `--handoff-dir`), readable only by you, because they contain the factory's token. Don't commit them.
- Each one ends with the `fabrica confirm` command to run when it's done. Until then, the change is **pending** in state, and a second apply never issues the same handoff twice. If what's wanted changes again before you confirm, apply replaces the pending handoff with a new one.
- `fabrica confirm <id> --remote-id <id-from-the-runtime>` records the result. Without a remote ID, the resource is tracked as **unverifiable**: no drift detection, and updating or deleting it takes a handoff, even on a runtime with an API. `plan` says so.
- Until an update or delete handoff is done, the old stub can still fire, but the server refuses its runs (see [stale stubs](concepts.md#stale-stubs)).
- `confirm` takes the same lock as apply, since it also writes the state file.

### API mode

For runtimes whose provider has an API (folder providers with an `api` program and the credentials it declares, and Cursor automations with a Cursor API key), apply creates, updates and deletes resources itself. Every create comes after the refresh has looked the resource up, so a retry never makes a duplicate. On a runtime that can't list resources, a create whose ID was never learned isn't retried at all until you confirm whether it exists. If the refresh finds two resources with the same identity (say, someone copied one by hand), that's an error, and Fabrica names both rather than picking one to delete.

Some APIs can't set up everything a resource relies on. Cursor automations, for one, only name their MCP servers. Then apply also writes what to set up by hand next to the handoffs (for Cursor, `~/.cache/fabrica/handoffs/<factory>/cursor-cloud-setup.md`), and lists it at the end. See [Cursor automations through Cursor's API](providers.md#cursor-automations-through-cursors-api).

## Destroy

`fabrica destroy` is the opposite of `apply`. It removes everything your factories (or the ones you name) installed, and leaves their files alone, so the next `apply` puts it all back.

For a cloud factory, it plans every resource in state as a delete, exactly as if the factory file were gone, and then carries it out like an apply: it takes the lock, refreshes, deletes each resource through the runtime's API or with a delete handoff, and records the results and releases the lock in one commit. When every delete went through, it also resets the factory's [init](#init), so the next apply, which recreates the factory, runs the init again. For your personal home repo, it also removes what Fabrica installed in your local tools. It shows the plan and asks first; `fabrica plan --destroy` shows the same plan without changing anything.

To stop deploying something for good, delete its factory file and apply instead. The server keeps serving a factory for as long as its file exists, so a destroyed one's stubs still work until their delete handoffs are done.

## Who applies

CI on the home repo is the recommended way: apply on every merge to the main branch, using the CI's own concurrency setting so applies wait in line instead of racing. Individuals can apply too, and the lock keeps that safe.

Run `fabrica plan` on a schedule too. Remote runs can't inspect the workspace, so they rely on `check` and `plan` (which read each deployed repo's default branch) to catch problems that appear after apply, like a repo that dropped its Fabrica line. The scheduled job fails when that happens. It's deliberately `plan` and not `apply`: a new trigger or connector is exactly what someone should review before it's installed.

[examples/home/.github/workflows/fabrica.yml](../examples/home/.github/workflows/fabrica.yml) is a ready-made GitHub Actions workflow that does both.

## Secrets

Secrets never live in blueprint or home repos. The server secret and runtime API keys, like a Cursor API key, come from environment variables (`FABRICA_SERVER_SECRET`, `CURSOR_API_KEY`; CI secrets, say), or else from your keychain, where `fabrica auth login` saves them. `fabrica auth` checks that each one works, and `fabrica auth env | gh secret set --env-file -` copies them to CI. See [Credentials](auth.md). Connector secrets are referenced by name and come from each runtime's own secret store.
