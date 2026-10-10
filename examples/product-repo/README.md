# Example product repo

This is what a product repo looks like when it's set up for Fabrica. Only a few files matter here (the app's code itself isn't included):

```
AGENTS.md                      the Fabrica line, then product knowledge
CLAUDE.md                      the same, because Claude Code reads CLAUDE.md instead of AGENTS.md when it exists
.agents/skills/test/SKILL.md   how *this* repo runs its tests, for factories that expect a `test` skill
```

**The Fabrica line** is the first line of `AGENTS.md` and `CLAUDE.md`. It's the only Fabrica-specific thing in the repo. It tells the agent to load the real working rules from the Fabrica MCP server, and tells everyone else to ignore it.

**Everything below the line is product knowledge**: what this app is, where things live, its conventions. That's exactly what belongs in a product repo. Process, like "write the test first" or "keep PRs small", belongs in a factory instead, so it can be shared across every repo and every tool.

**The `test` skill** answers a question only this repo can answer: how do you run the tests here? A factory that lists `test` in its `expects:` (like the [starter factory](../starter-factory/)) relies on it. Skills the factory expects are the one case where a repo skill may share a name with a factory skill.

Check a repo like this one with:

```sh
fabrica check --factory ../starter-factory
```

See [docs/product-repos.md](../../docs/product-repos.md) for the details.
