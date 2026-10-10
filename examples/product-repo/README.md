# Example product repo

This is what a product repo looks like when it's set up for Fabrica. Only a few files matter here (the app's code itself isn't included):

```
AGENTS.md                      the Fabrica line, then product knowledge
CLAUDE.md                      the Fabrica line, then @AGENTS.md to pull in the product knowledge
.agents/skills/test/SKILL.md   how *this* repo runs its tests, for blueprints that expect a `test` skill
```

**The Fabrica line** is the first line of `AGENTS.md` and `CLAUDE.md`. It's the only Fabrica-specific thing in the repo. It tells the agent to load the real working rules from the Fabrica MCP server, and tells everyone else to ignore it.

**Everything below the line in `AGENTS.md` is product knowledge**: what this app is, where things live, its conventions. That's exactly what belongs in a product repo. Process, like "write the test first" or "keep PRs small", belongs in a blueprint instead, so it can be shared across every repo and every tool.

**`CLAUDE.md` is there for Claude Code**, which reads it instead of `AGENTS.md` whenever it exists. So it needs the Fabrica line too. After the line, `@AGENTS.md` makes Claude Code import `AGENTS.md`, so the product knowledge lives in one file instead of two copies that drift apart.

**The `test` skill** answers a question only this repo can answer: how do you run the tests here? A blueprint that lists `test` in its `expects:` (like the [starter blueprint](../starter-blueprint/)) relies on it. Skills the blueprint expects are the one case where a repo skill may share a name with a blueprint skill.

Check this example against the starter blueprint by running this from this folder:

```sh
fabrica check . --blueprint ../starter-blueprint
```

The `.` matters here. Without a folder, `fabrica check` checks the whole git repo you're in, and this example lives inside the Fabrica repo. In a real product repo, `fabrica check` with no folder is what you want.

See [docs/product-repos.md](../../docs/product-repos.md) for the details.
