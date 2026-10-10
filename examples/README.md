# Examples

Each folder here is a small, working example of one part of Fabrica. They're all checked by Fabrica's own tests, so they stay correct as Fabrica changes.

| Folder | What it shows |
|---|---|
| [starter-factory](starter-factory/) | A real, ready-to-use factory: five skills, three routines (two cloud, one local), four rules and two connectors. Copy it (`fabrica new` writes a copy for you), or import parts of it. |
| [home](home/) | A team home repo with two cloud deployments (one following a branch, one frozen at a tag) and a GitHub Actions workflow that applies on merge and plans every night. |
| [personal-home](personal-home/) | A personal home repo (`~/.fabrica`) with a wildcard local deployment and a specific one with a local routine. |
| [product-repo](product-repo/) | What a product repo needs: the Fabrica line at the top of `AGENTS.md` and `CLAUDE.md`, product knowledge below it, and its own `test` skill. |
| [custom-provider](custom-provider/) | A factory that ships its own provider for a runtime Fabrica doesn't know, with a working `api` program in Python. |

New to Fabrica? Start with [docs/getting-started.md](../docs/getting-started.md), which uses these examples.
