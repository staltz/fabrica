# Safety checks

Fabrica catches problems as early as it can, and refuses to continue when something is wrong. It never guesses. This page lists every check and where it's caught.

In the table, ❌ is an **error**: `apply` won't touch a deployment until it's fixed. 🔧 is a **stale install** that `apply` repairs, so `plan` lists it as a change, not an error. ⚠️ is a **warning**, which never stops anything.

The code in the last column is what `fabrica plan --json` reports, so scripts can match on it.

| Problem | `lock` / `check` / `plan` | Local server, at runtime | Remote server, at runtime | Code |
|---|---|---|---|---|
| Two imports bring the same item | ❌ error | — | — | `import.collision` |
| An item defined here is also brought by an import (without `exclude:`) | ❌ error, with a hint on how to override | — | — | `import.collision` |
| `only:` or `exclude:` names an item the import doesn't have | ❌ error | — | — | `import.unknown-item` |
| A routine's `skills:` entry doesn't exist | ❌ error | — | — | `routine.missing-skill` |
| An item is taken with `only:` but what it needs isn't | ❌ error, says what to add | — | — | `routine.missing-skill`, `routine.missing-connector` |
| `fabrica.lock` is missing or out of date | ❌ error | ❌ refuses to serve | ❌ refuses to serve | `lock.missing`, `lock.stale` |
| An unknown key, or a value of the wrong type, in any Fabrica file | ❌ error | ❌ refuses to serve | ❌ refuses to serve | `*.invalid` |
| Fabrica line missing from the root `AGENTS.md` (or root `CLAUDE.md`) | ❌ error, for each deployed repo | ❌ refuses to serve | not detectable | `product.fabrica-line` |
| A repo skill has the same name as a factory skill (not in `expects:`) | ❌ error, for each deployed repo | ❌ `run_begin` and `skill_load` refuse | not detectable | `product.skill-clash` |
| Repo rules contain process instructions that conflict with factory rules | can't be checked by code | the agent stops, alerts and reports `failed` | the agent stops, alerts and reports `failed` | — |
| Two specific local deployments for the same repo and runtime | ❌ error | ❌ refuses to serve | — | `home.local-overlap` |
| More than one wildcard local deployment for a runtime | ❌ error | ❌ refuses to serve | — | `home.wildcard-overlap` |
| A broken deployment file in your personal home repo | ❌ error | ❌ refuses to serve (it might have covered this repo) | — | `deployment.invalid` |
| A local deployment in a team home repo | ❌ error | — | — | `deployment.invalid` |
| A cloud deployment without `server:` | ❌ error | — | — | `deployment.invalid` |
| A deployment mixing cloud and local runtimes | ❌ error | — | — | `deployment.invalid` |
| A param that no routine declares, or a required param that isn't set | ❌ error | — | — | `deployment.unknown-param`, `deployment.missing-param` |
| A ref that's both a branch and a tag | ❌ error | ❌ refuses to serve | ❌ refuses to serve | `factory.resolve` |
| A trigger, connector or side effect a runtime doesn't support | ❌ error | — | — | `capability.trigger`, `capability.connector`, `capability.side-effect` |
| A routine's trigger or connectors changed since apply, to ones the runtime supports (stub outdated) | 🔧 update in `plan` | ❌ `run_begin` refuses | ❌ `run_begin` refuses | — |
| A routine, repo or runtime was removed from a deployment, but its stub is still installed (stub no longer deployed) | 🔧 delete in `plan` | ❌ `run_begin` refuses | ❌ `run_begin` refuses | — |
| The server secret was rotated | 🔧 update in `plan` (when it has the secret) | — | ❌ old tokens get `401` | — |
| A cloud routine's `skills:` lists a skill that isn't text-only | ❌ error | — (works locally) | ❌ `skill_load` refuses | `capability.skill` |
| A routine run without a stub on the remote server | — | — | ❌ `run_begin` and `routine_get` refuse | — |
| Another apply holds the deployment's lock | ❌ `apply` stops, names the holder | — | — | `deployment.locked` |
| Two remote resources share one identity | ❌ error in `plan` | — | — | `refresh.duplicate` |
| An intent or summary longer than 20 words | — | ❌ asks for a shorter one | ❌ asks for a shorter one | — |
| Product repo doesn't provide something in `expects:` | ⚠️ reported | — | — | `product.unmet-expects` |
| Prose mentions a skill you forgot to import | ⚠️ warning | — | — | `skill.mentions-missing` |
| A routine's prompt mentions a skill that isn't in its `skills:` | ⚠️ warning | — | — | `routine.unlisted-skill` |
| An import expects something your `expects:` doesn't list | ⚠️ warning | — | — | `expects.uncovered` |
| `evals/` folder exists | ⚠️ warning (reserved) | — | — | `factory.evals` |

## Where each kind of check happens

**Remote runs** can't inspect the workspace, so they rely on `check` and `plan`, which read each deployed repo's default branch. Running `fabrica plan` on a schedule in the home repo's CI catches problems that appear *after* apply: the CI job fails.

**When the local server refuses,** it explains why in its MCP instructions, returns an error from `run_begin`, and records the run as `failed`. Local hooks stay quiet in a repo Fabrica won't serve, so the refusal message comes from one place.

## Things that are deliberately left to people

Some risks are accepted on purpose, and it's worth knowing them:

- **Side effects are an honor system.** A factory author can write anything in a prompt. The real limit is what each connector's credentials allow.
- **A followed branch is a security boundary.** Anyone who can push to a branch that deployments follow changes what every one of those deployments does. Protect those branches.
- **New side effects on a followed branch arrive without a review in the home repo.** New connectors and trigger changes still need a re-apply (the server refuses the routine until then), so those do get reviewed.
- **Changing a routine's trigger or connectors stops it wherever it's followed,** until each home repo re-applies. A refused run is a lost event, like a PR that doesn't get reviewed. That's chosen over running a routine with the wrong trigger or missing connectors.
- **A crashed apply locks its deployment** until someone runs `fabrica unlock`.
- **"Product knowledge only" is enforced by the agent,** so some conflicts will slip through.
