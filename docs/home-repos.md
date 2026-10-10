# Home repos

A home repo says which factory runs on which product repos, in which runtimes. It's an ordinary git repo, so it's versioned, durable and works across machines.

```
deployments/<name>.yaml           # what to deploy where (you write these)
state/<name>.yaml                 # what's installed, one per cloud deployment (only Fabrica writes these)
```

## Personal and team home repos

- Your **personal** home repo is the folder `FABRICA_HOME` points to, or `~/.fabrica` by default. It can hold both local and cloud deployments. Your local run history (`runs.db`) lives in it too, so add `runs.db*` to its `.gitignore`.
- A **team** home repo is any other home repo, owned by a team. In v0.1 it only holds cloud deployments: a local deployment in one is an error.

Most commands work on one home repo. They pick it like this: the `--home` flag if you give one, otherwise the current folder if it looks like a home repo (it has `deployments/` or `state/`), otherwise your personal home repo.

## Cloud deployments

A deployment says: *this factory, these routines, these repos, these runtimes, these params.*

```yaml
# deployments/pr-review.yaml
factory: { git: github.com/acme/factory, ref: stable }
server: https://fabrica.acme.dev
runtimes: [claude-code-cloud, cursor]
routines: [pr-review]
repos:
  - github.com/acme/app
  - repo: github.com/acme/api
    params: { slack_channel: "#api" }     # per-repo override
params:
  slack_channel: "#eng"
```

| Key | Required | What it is |
|---|---|---|
| `factory` | yes | `git` and `ref` (and optionally `path`) of the factory, like an import. |
| `server` | for cloud | The URL of your self-hosted Fabrica server. Must be `https` (plain `http` only for localhost). |
| `runtimes` | yes | Where to run: built-in runtimes, or ones from the factory's [folder providers](providers.md#folder-providers). A deployment is either all cloud or all local. |
| `routines` | for cloud | Which of the factory's routines to deploy. |
| `repos` | yes | The product repos. Each is a name, or `{ repo, params }`. |
| `params` | no | Param values for every repo. |

- **The file name is the deployment's ID.** `deployments/pr-review.yaml` is the deployment `pr-review`.
- **It expands to every combination** of routines × repos × runtimes. The example creates 1 × 2 × 2 = 4 remote resources, each tracked separately.
- **The endpoint** is `server` plus the deployment's name: `https://fabrica.acme.dev/pr-review`. Fabrica builds it for you.
- **Repo names** are always written as `host/owner/repo`. Git remotes like `git@github.com:acme/app.git` are turned into that form, in lowercase.
- **Overlap is allowed.** Several deployments, from different home repos and different factories, may target the same repo. Two PR reviewers on one repo is a people problem, not one the tool prevents.

### Params

Params are set once at the top, and individual repos can override them. Every param must be declared by one of the deployment's routines, and every required param must have a value for every repo. Both are errors otherwise, which catches typos.

If two runtimes need different settings, write two deployment files.

### Follow versus frozen

The factory's `ref` decides how the deployment gets updates:

- **A branch** (`main`, `stable`) means **follow**. A merge to that branch takes effect on the next run, with no apply needed.
- **A tag or a commit** means **frozen**. Nothing changes until you edit the deployment.

`fabrica plan` shows which mode each deployment is in. If a name is both a branch and a tag in the factory repo, Fabrica refuses to guess and asks you to use a name that's only one of them.

## Local deployments

A local deployment is the same kind of file, with local runtimes, in your personal home repo:

```yaml
# deployments/my-default.yaml
factory: { git: github.com/me/factory, ref: main }
runtimes: [claude-code-local, cursor-ide]
repos: ["*"]                     # every repo not matched by another local deployment
```

```yaml
# deployments/oss-work.yaml
factory: { git: github.com/me/oss-factory, ref: main }
runtimes: [claude-code-local, cursor-ide]
repos: [github.com/oss/project]
routines: [review-before-push]   # optional local routines
```

What happens:

- **`fabrica apply` on your laptop** installs one user-level `fabrica` MCP server in each local tool, plus a hook for each local routine. It never writes into a product repo. If a local routine needs an MCP connector, that gets registered too, as `fabrica-<connector>`.
- **Choosing a factory needs no apply.** Each time a session starts, `fabrica mcp` reads your local deployments, matches the workspace's git remote, and serves that factory. Change a local deployment, and the next session picks it up.
- **New machine?** Clone your home repo to `~/.fabrica` and run `fabrica apply` once.
- **The wildcard** `repos: ["*"]` (quoted, because a bare `*` means something else in YAML) is only allowed for local runtimes, and must be the only entry. A specific repo always beats the wildcard. There can be at most one wildcard deployment per runtime.
- **Two specific local deployments for the same repo and runtime is an error.** A session must get exactly one factory.
- **If no local deployment matches,** `fabrica mcp` serves nothing and says why.
- **CI ignores local runtimes,** since there's nothing local for it to set up. When `$CI` is set, `plan` and `apply` skip local deployments.

Fabrica finds its own local installs by their identifying marks: MCP servers named `fabrica` or `fabrica-…`, and hooks whose command is `fabrica hook …`. Local deployments don't need a state file.

## State

Each cloud deployment has one state file, `state/<deployment>.yaml`. Only Fabrica writes it, and it's committed to the home repo. It records every remote resource the deployment owns, what was installed in each one, any pending handoffs, and the deployment's lock. Because each deployment has its own file, people applying *different* deployments never conflict.

```yaml
# state/pr-review.yaml
# Written by Fabrica. Don't edit by hand.
lock:                                    # only there while an apply is running
  holder: ci github.com/acme/home/actions/runs/4812
  since: 2026-10-10T12:00:00Z
factory:                                 # the factory the last apply used
  git: github.com/acme/factory
  commit: 9c1e4b2a…
resources:
  - id: fabrica.acme.dev/pr-review#pr-review@github.com/acme/app
    runtime: claude-code-cloud
    routine: pr-review
    repo: github.com/acme/app
    remote_id: trig_01Hx…                # empty means unverifiable
    mode: handoff
    factory_commit: 9c1e4b2a…
    token_hash: 571f1676be88             # notices a rotated server secret
    shape:
      trigger: github.pull_request.opened
      connectors: [github, slack]
      fingerprint: e128f7aa2995
    applied_at: 2026-10-10T12:01:00Z
pending: []                              # handoffs waiting for `fabrica confirm`
```

The `token_hash` is a short one-way fingerprint of the token, never the token itself.

## Plan: changes and errors

`fabrica plan` compares what the home repo and its factories ask for with what's installed, and sorts everything it finds into two kinds:

- **Changes** are work for `apply`: resources to create, update, delete or adopt, and handoffs to issue. That includes repairing stubs that went stale after a merge. When a routine's trigger or connectors change (to ones the runtime supports), its installed stub is an **update**. A stub whose routine, repo or runtime is no longer in its deployment is a **delete**.
- **Errors** are problems `apply` can't fix: a trigger the runtime doesn't support, a product repo without the Fabrica line, duplicate identities, a held lock and so on. `apply` won't touch a deployment while it has any. An error in one repo blocks the whole deployment, so to unblock the others, fix that repo or take it out of the deployment.

`plan` exits with `0` when there's nothing to do, `2` when there are only changes, and `1` when there are errors. Pending handoffs aren't apply's work, so on their own they don't change the exit code, but `plan` lists them.

`plan` reads the home repo as it is on disk, so you can plan your changes before you push them. When the server secret is set (`FABRICA_SERVER_SECRET`), it also notices rotated tokens. Pass `--json` for machine-readable output.

## Apply

### How apply changes a cloud deployment

1. **Plan** against the latest home repo. Apply fetches the remote and fast-forwards first, and refuses to run with uncommitted changes in `deployments/` or `state/`, or with local commits that aren't pushed: it applies what's on the remote, never what's only on your machine. If the plan finds errors, apply lists them and skips that deployment. If nothing needs to change, it stops without committing.
2. **Take the lock.** Commit a `lock:` entry to the state file and push. Git only accepts a push on top of the latest commit, so when two applies race, exactly one push gets through. The other takes its commit back, pulls, sees the lock, and stops with a message naming who holds it. Nothing remote has been touched yet, so losing the race is always safe.
3. **Refresh.** Look up all of this deployment's resources in each runtime **by identity** (for runtimes with an API). A resource that exists but is missing from state gets adopted, like one created by an apply that crashed before recording it.
4. **Re-plan and apply.** If the re-plan finds errors (say, duplicates the refresh turned up), remove the lock and stop without touching any resource. Otherwise carry out each change.
5. **Record and release.** Write the results and remove the lock in one commit, then push. If the push is rejected because of unrelated commits (like another deployment's state), rebase and push again. While the lock is held, nobody else writes this file.

### Crashed applies

A crashed apply leaves its lock in place. Fabrica can't tell a crashed apply from a slow one, so it doesn't guess. `plan` and `apply` show who holds the lock and since when, and once you're sure that apply is gone:

```sh
fabrica unlock pr-review
```

The next apply's refresh adopts whatever the crashed one created.

### Handoffs

Some runtimes have no API Fabrica can use (as of v0.1, that's all three built-in cloud runtimes). For those, apply writes a **handoff**: step-by-step instructions that a person, or an AI with computer use, can follow. Fabrica never does computer use itself.

- Handoffs are written to `~/.cache/fabrica/handoffs/<deployment>/<id>.md` (or `--handoff-dir`), readable only by you, because they contain the deployment's token. Don't commit them.
- Each one ends with the `fabrica confirm` command to run when it's done. Until then, the change is **pending** in state, and a second apply never issues the same handoff twice. If what's wanted changes again before you confirm, apply replaces the pending handoff with a new one.
- `fabrica confirm <id> --remote-id <id-from-the-runtime>` records the result. Without a remote ID, the resource is tracked as **unverifiable**: no drift detection and no automatic deletion. `plan` says so.
- Until an update or delete handoff is done, the old stub can still fire, but the server refuses its runs (see [stale stubs](concepts.md#stale-stubs)).
- `confirm` takes the same lock as apply, since it also writes the state file.

### API mode

For runtimes whose provider has an API (folder providers with an `api` program), apply creates, updates and deletes resources itself. Every create comes after the refresh has looked the resource up by identity, so a retry never makes a duplicate. If the refresh finds two resources with the same identity (say, someone copied one by hand), that's an error, and Fabrica names both rather than picking one to delete.

## Who applies

CI on the home repo is the recommended way: apply on every merge to the main branch, using the CI's own concurrency setting so applies wait in line instead of racing. Individuals can apply too, and the lock keeps that safe.

Run `fabrica plan` on a schedule too. Remote runs can't inspect the workspace, so they rely on `check` and `plan` (which read each deployed repo's default branch) to catch problems that appear after apply, like a repo that dropped its Fabrica line. The scheduled job fails when that happens. It's deliberately `plan` and not `apply`: a new trigger or connector is exactly what someone should review before it's installed.

[examples/home/.github/workflows/fabrica.yml](../examples/home/.github/workflows/fabrica.yml) is a ready-made GitHub Actions workflow that does both.

## Secrets

Secrets never live in factory or home repos. The server secret comes from `FABRICA_SERVER_SECRET` (a CI secret, say). Connector secrets are referenced by name and come from each runtime's own secret store.
