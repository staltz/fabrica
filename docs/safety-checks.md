# Safety checks

Fabrica catches problems as early as it can, and refuses to continue when something is wrong. It never guesses. This page lists every check and where it's caught.

In the table, ❌ is an **error**: `apply` won't touch a factory until it's fixed. 🔧 is a **stale install** that `apply` repairs, so `plan` lists it as a change, not an error. ⚠️ is a **warning**, which never stops anything.

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
| A repo skill has the same name as a blueprint skill (not in `expects:`) | ❌ error, for each deployed repo | ❌ `run_begin` and `skill_load` refuse | not detectable | `product.skill-clash` |
| Repo rules contain process instructions that conflict with blueprint rules | can't be checked by code | the agent stops, alerts and reports `failed` | the agent stops, alerts and reports `failed` | — |
| Two specific local factories for the same repo and runtime | ❌ error | ❌ refuses to serve | — | `home.local-overlap` |
| More than one wildcard local factory for a runtime | ❌ error | ❌ refuses to serve | — | `home.wildcard-overlap` |
| A broken factory file in your personal home repo | ❌ error | ❌ refuses to serve (it might have covered this repo) | — | `factory.invalid` |
| A local factory in a team home repo | ❌ error | — | — | `factory.invalid` |
| A cloud factory without `server:` | ❌ error | — | — | `factory.invalid` |
| A factory mixing cloud and local runtimes | ❌ error | — | — | `factory.invalid` |
| A routine's `runtimes` names one the factory doesn't list, or a cloud runtime has no routines | ❌ error | — | — | `factory.invalid` |
| A param that no routine declares, or a required param that isn't set | ❌ error | — | — | `factory.unknown-param`, `factory.missing-param` |
| A ref that's both a branch and a tag | ❌ error | ❌ refuses to serve | ❌ refuses to serve | `blueprint.resolve` |
| A trigger, connector or side effect a runtime doesn't support (including `github.pr_autofix` anywhere it isn't native, and a native side effect on a local hook) | ❌ error | — | — | `capability.trigger`, `capability.connector`, `capability.side-effect` |
| A routine's trigger, connectors, memory setting (on runtimes with their own memory), native side effects or model changed since apply, to ones the runtime supports (stub outdated) | 🔧 update in `plan` | ❌ `run_begin` refuses | ❌ `run_begin` refuses | — |
| A routine, repo or runtime was removed from a factory (or a routine no longer runs on a runtime), but its stub is still installed (stub no longer deployed) | 🔧 delete in `plan` | ❌ `run_begin` refuses | ❌ `run_begin` refuses | — |
| The server secret was rotated | 🔧 update in `plan` (when it has the secret) | — | ❌ old tokens get `401` | — |
| The server secret you have isn't the one the server uses | ❌ `fabrica auth` and `fabrica auth login` (the server refuses its tokens) | — | ❌ tokens get `401` | — |
| A credential a runtime's API needs isn't set | `plan` notes it, and the runtime falls back to handoffs; ❌ error when it has no other apply mode | — | — | `provider.no-mode` |
| The credentials file can be read by other users | ❌ Fabrica won't use it until you `chmod 600` it | — | — | — |
| A cloud routine's `skills:` lists a skill that isn't text-only | ❌ error | — (works locally) | ❌ `skill_load` refuses | `capability.skill` |
| A routine run without a stub on the remote server | — | — | ❌ `run_begin` and `routine_get` refuse | — |
| Another apply holds the factory's lock | ❌ `apply` stops, names the holder | — | — | `factory.locked` |
| Two remote resources share one identity | ❌ error in `plan` | — | — | `refresh.duplicate` |
| A resource was deleted, renamed or had its prompt edited by hand in a runtime that can't list resources (like Cursor) | 🔧 create or update in `plan`, with a note | — | — | — |
| An apply crashes after creating a resource in a runtime that can't list resources, once the ID was recorded | 🔧 nothing to find: that create is already in the home repo | — | — | — |
| An apply loses the answer to a create, or stops, on a runtime that can't list resources, before the ID is recorded | ❌ error: the resource may exist, and apply won't create another until `fabrica confirm` says whether it does | — | — | `refresh.uncertain-create` |
| An intent or summary longer than 20 words | — | ❌ asks for a shorter one | ❌ asks for a shorter one | — |
| Product repo doesn't provide something in `expects:` | ⚠️ reported | — | — | `product.unmet-expects` |
| Prose mentions a skill you forgot to import | ⚠️ warning | — | — | `skill.mentions-missing` |
| A routine's prompt mentions a skill that isn't in its `skills:` | ⚠️ warning | — | — | `routine.unlisted-skill` |
| An import expects something your `expects:` doesn't list | ⚠️ warning | — | — | `expects.uncovered` |
| `evals/` folder exists | ⚠️ warning (reserved) | — | — | `blueprint.evals` |

## Where each kind of check happens

**Remote runs** can't inspect the workspace, so they rely on `check` and `plan`, which read each deployed repo's default branch. Running `fabrica plan` on a schedule in the home repo's CI catches problems that appear *after* apply: the CI job fails.

**When the local server refuses,** it explains why in its MCP instructions, returns an error from `run_begin`, and records the run as `failed`. Local hooks stay quiet in a repo Fabrica won't serve, so the refusal message comes from one place.

## Things that are deliberately left to people

Some risks are accepted on purpose, and it's worth knowing them:

- **Side effects are an honor system.** A blueprint author can write anything in a prompt. The real limit is what each connector's credentials allow.
- **A followed branch is a security boundary.** Anyone who can push to a branch that factories follow changes what every one of those factories does. Protect those branches.
- **New side effects on a followed branch arrive without a review in the home repo.** New connectors and trigger changes still need a re-apply (the server refuses the routine until then), so those do get reviewed.
- **Changing a routine's trigger or connectors stops it wherever it's followed,** until each home repo re-applies. A refused run is a lost event, like a PR that doesn't get reviewed. That's chosen over running a routine with the wrong trigger or missing connectors.
- **A crashed apply locks its factory** until someone runs `fabrica unlock`.
- **"Product knowledge only" is enforced by the agent,** so some conflicts will slip through.
