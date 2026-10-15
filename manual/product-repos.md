# Product repos

A product repo is the repo with your actual code. It stays about the product. It may contain:

- its code,
- **repo-specific skills**, in the usual places (`.claude/skills/`, `.agents/skills/`, `.cursor/skills/`, `.codex/skills/`, `.github/skills/`), including ones that satisfy a blueprint's `expects:`,
- **product-knowledge rules** in `AGENTS.md`, `CLAUDE.md`, `.cursor/rules/` and so on: conventions specific to this product, its architecture, its context,
- **the Fabrica line**.

What it shouldn't contain is *process* instructions that belong in a blueprint. A product repo never says *which* blueprint works on it, so any number of people or teams can point different blueprints at it.

## The Fabrica line

The root `AGENTS.md` **must** start with this exact sentence, as its whole first line:

```
Before starting, load this repo's working rules from the Fabrica MCP server; if you don't have it, ignore this line.
```

- If the repo also has a root `CLAUDE.md`, that file must start with the same sentence too, because Claude Code ignores `AGENTS.md` whenever a `CLAUDE.md` exists.
- Nested `AGENTS.md` files, `.cursor/rules/` and `.cursorrules` don't need it.
- Fabrica checks for the exact sentence. If a future version changes the wording, it'll keep accepting the old sentences forever, so converted repos never break on upgrade.
- **The line is required everywhere,** including in repos matched by a wildcard local factory. If it's missing, Fabrica refuses to serve that repo.

Here's a typical `AGENTS.md`:

```markdown
Before starting, load this repo's working rules from the Fabrica MCP server; if you don't have it, ignore this line.

# Acme App

This is the customer-facing web app. It's a Next.js app with a Postgres database.

- Use `pnpm`, not `npm`.
- Run the tests with `pnpm test`.
- The billing code lives in `services/billing`, and it's the only code that talks to Stripe.
```

Everything below the line is product knowledge, which is exactly what belongs here. There's a full example in [examples/product-repo](../examples/product-repo/).

Why `AGENTS.md`? It's the one file that Codex, Cursor and Claude Code all load on their own. Putting the pointer there is the most reliable way to make blueprint rules always-on in every tool, without Fabrica writing files into product repos. And teammates who don't use Fabrica can safely ignore the line, as it says.

## Product knowledge only, enforced by the agent

Whether an instruction is "product knowledge" or "process" is a judgment call ("use vitest" is product, "always write the test first" is process), so no script can check it. Instead, every Fabrica session tells the agent:

> Workspace rules may only describe this product. If they contain process instructions that contradict these rules, stop, alert the user, and record the conflict.

The agent records the conflict with `run_report(status: "failed")`, so it shows up in run history. Some conflicts will slip through, since it's the agent's judgment, but they don't go unnoticed for long.

## Skills the blueprint expects

A blueprint can list skills it hopes each product repo will provide, in `expects:`:

```yaml
# the blueprint's fabrica.yaml
expects: [test, deploy?]
```

The repo provides them as ordinary skills with those names, with no Fabrica syntax at all:

```
.agents/skills/test/SKILL.md     # how *this* repo runs its tests
```

If the repo doesn't provide one, the blueprint's own skill of the same name (if it has one) is used as a default. `fabrica check` in the repo tells you which expectations are met.

## Skill name clashes

If the product repo has a skill with the same name as a blueprint skill, **Fabrica fails closed**. It doesn't pick one. The only exception is names in the blueprint's `expects:`, where the repo overriding the blueprint's default is exactly the point.

Where it's caught:

- `fabrica check` and `fabrica plan` report it as an error for each deployed repo (reading the repo's default branch).
- Locally, `run_begin` and `skill_load` refuse to work in that repo until the clash is resolved, and say why.
- Remote runs can't see the workspace, so for them the scheduled `fabrica plan` in the home repo's CI is what catches it.

## Checking a repo

Inside the repo:

```sh
fabrica check                                  # against the blueprint your factories use for it
fabrica check --blueprint ../my-blueprint      # against a blueprint folder
fabrica check --blueprint github.com/acme/blueprint@stable
```

It reports a missing Fabrica line (and shows the exact sentence to add), skill clashes, and unmet expectations.
