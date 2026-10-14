# Architecture

This page is for people working on Fabrica itself. It explains how the code is organized, the decisions behind it, and how the tests work.

## The packages

Everything lives under `internal/`, so nothing outside this module depends on it, and we're free to change it. The one exception is `examples`, whose only Go file builds the starter blueprint into the binary for `fabrica new` (Go can only embed files at or below a package's folder). Roughly from the bottom up:

| Package | What it does |
|---|---|
| `diag` | Diagnostics: errors, warnings and notes with a stable code, a location, a message and a hint. Almost every check returns a `diag.List` instead of stopping at the first problem, so you see everything at once. |
| `yamlx` | Strict YAML (unknown keys are errors, with friendly messages) and frontmatter splitting. |
| `names` | Item names (`pr-review`), item refs (`skills/pr-review`) and optional names (`slack?`). |
| `repourl` | Turns every way of writing a repo's address into `host/owner/repo`, and back into a URL git can clone. |
| `paths` | Where Fabrica keeps things: the personal home repo, the cache and local run history. |
| `fabline` | The Fabrica line, and the list of every sentence ever accepted. |
| `gitx` | A thin wrapper around the `git` binary: a cache of bare clones, unpacked commit trees, ref resolution, and the commit and push operations the apply lock is built on. |
| `provider` | Capability matrices, the built-in providers (embedded YAML and handoff templates), folder providers, the `api` protocol client, the client for Cursor's Automations API, and handoff rendering. |
| `blueprint` | The blueprint format: manifest, lock, every item kind, import resolution (blueprints and plain repos), filters, collisions, overrides, expects, lint warnings, and `lock`/`upgrade`. |
| `stub` | A routine checked against one runtime: its shape, fingerprint, identity and stub prompt. |
| `product` | Inspects product repos (on disk or a remote's default branch): the Fabrica line, repo skills, clashes and expects. |
| `home` | Home repos: factories, local matching, state files, and creating your personal home repo when it's missing. |
| `token` | Factory tokens: `HMAC(secret, factory name)`, and making new server secrets. |
| `credentials` | Finding credentials (the environment first, then where `fabrica auth login` saved them) and saving them: the macOS Keychain through `security`, the Secret Service through `secret-tool`, or a file only you can read. |
| `localtools` | Editors for Claude Code, Cursor and Codex config files, which only touch Fabrica's own entries. |
| `plan` | Compares what's wanted with what's installed, and sorts it into changes and errors. |
| `auth` | Works out the credentials and git access a home repo needs for `fabrica auth`, and checks each with its service: a server by the tokens it accepts, a runtime by its provider's verifier, git with `ls-remote`. |
| `scaffold` | Writes new blueprints for `fabrica new`: the starter blueprint (from `examples`) or an empty one, never overwriting a file. |
| `apply` | Carries out plans, for `apply` and `destroy`: the git lock, API calls, handoffs, `confirm` and `unlock`. |
| `runs` | The run-history database. |
| `mcpserver` | The MCP server: the eight tools, the local (stdio) and remote (HTTP) modes, stale-stub checks and skill unpacking. |
| `hook` | What `fabrica hook` does when a local tool fires a hook. |
| `selfupdate` | What `fabrica -u` does: installs the latest GitHub release over the running binary, checked against the release's checksums. |
| `cli` | The cobra commands. Thin: they parse flags, call the packages above and print. |
| `testkit` | Test helpers: a fake git host, home repos with a shared remote, the toy runtime, and a ready-made world. |

The arrows only point down: `blueprint` never imports `plan`, `plan` never imports `mcpserver`, and so on. `cmd/fabrica` is just `main`.

## Decisions worth knowing

**We run the real `git`.** Fabrica sits beside git, so instead of a Go git library we shell out to `git`. Users' credential helpers, SSH keys and `url.<base>.insteadOf` rewrites just work, and tests use that same `insteadOf` trick to point `https://github.com/…` at temp folders, with no test-only code paths in Fabrica.

**Credentials come from the environment, then the keychain.** A credential's environment variable always wins, even when it's set to nothing, so CI and scripts behave exactly as they say. Otherwise Fabrica reads what `fabrica auth login` saved, and like with git, it runs the system's own keychain tool (`security`, `secret-tool`) rather than linking a keychain library, so the binary stays static. Secrets reach those tools on stdin, never on a command line. Server secrets are saved per server URL, so a factory only ever gets the secret of the server it names.

**Commits are content-addressed caches.** A commit's content never changes, so each commit is unpacked once into `$FABRICA_CACHE/trees/<commit>/` and reused forever. Skills are unpacked into `skills/<content-hash>/<name>/`. Blueprints are loaded from those trees, and imports are memoized by URL, commit and path.

**Strict by default, collected not thrown.** Fabrica's own files are decoded with unknown keys as errors. Problems are collected into `diag.List`s with stable codes (like `import.collision`), so `check` can show everything at once and tests can match on codes instead of wording. A broken item carries its problems in `ItemInfo.Problems`, and they only become errors if the item survives everyone's `only:`/`exclude:` filters.

**The apply lock is a git compare-and-swap.** Git only accepts a push on top of the latest commit. Taking the lock is "commit a `lock:` entry, push"; losing the race means the push is rejected, so we take our commit back (`reset --keep`, which never touches other work) and look again. No lock server needed.

**Shapes, not content, live in runtimes.** A stub bakes in its shape (trigger, connectors and their settings, the runtime's memory setting where it has its own, the runtime's own features for its native side effects, the model where the runtime lets it pick one, server address). Everything else is served live. The shape's fingerprint is a hash of its canonical JSON, and both the planner and the MCP server compute it the same way (`stub.Evaluate`), which is what makes stale-stub detection work.

**Sessions pin a commit.** The MCP server uses stateful sessions: each session resolves its snapshot (factory and blueprint at a commit) once and keeps it. Stale-stub checks, on the other hand, always look at the *latest* home repo.

**Local configs are edited surgically.** `localtools` parses JSON into order-preserving objects and edits TOML as text, so everything that isn't Fabrica's comes out byte-for-byte the same. Writes are atomic, keep file permissions, leave a `.fabrica.bak` next to the file, and are skipped when nothing would change.

## Tests

`go test ./...` runs everything. Most tests are real: they create git repos in temp folders, run real `git`, start real MCP servers and talk to them with the MCP SDK's client.

- **`testkit.NewHost`** isolates git (a temp `GIT_CONFIG_GLOBAL`) and rewrites `https://` to a temp `hosts/` folder. `host.Repo("github.com/acme/app")` makes a repo Fabrica can fetch as `https://github.com/acme/app.git`. `host.Home(…)` makes a home repo with a bare remote and a tracking clone, for testing the apply lock.
- **The toy runtime** is a pretend cloud runtime whose "API" is a folder of JSON files. Its folder provider's `api` script runs the test binary itself: each test package's `TestMain` calls `testkit.MaybeRunToyProvider()`, which turns the binary into the provider when it's started that way.
- **`testkit.NewWorld`** builds a complete setup: a blueprint (with the toy provider), two product repos with the Fabrica line, a home repo and the toy runtime.
- **The CLI's end-to-end tests** run `cli.Main` in-process, and for stdio MCP they run the test binary as `fabrica` (`FABRICA_TEST_AS_CLI=1`). `TestSuccessTest` replays the v0.1 success test from the design doc, from `plan` all the way to run history.
- **Credentials stay out of your keychain.** Test binaries call `testkit.IsolateCredentials` in `TestMain`, which turns the system keychain off and points the credentials file at a temporary folder, and `testkit.FreshCredentials` gives one test its own. Keychain backends are tested against small fake `security` and `secret-tool` scripts.

Also run `go vet ./...` and `staticcheck ./...` (configured in `staticcheck.conf`). CI runs all three.

## Common changes

**Supporting a new trigger on a built-in runtime:** add it to the runtime's `internal/provider/builtin/<name>/provider.yaml`. For a local runtime, also map it to a hook event in `internal/localtools/hookcmd.go` and teach `internal/hook` how to respond to it.

**Giving a cloud provider an API:** when a runtime ships an API, implement `provider.RuntimeAPI` (create, update, delete) against the same `provider.Resource` the folder-provider protocol uses, plus `provider.Lister` if the API can list resources and read their identity back (`find_by_identity: true`), or else `provider.Getter` if it can read one back by its ID (`find_by_remote_id: true`). Register the constructor in `builtinAPIs`, declare its credentials under `credentials:` in its `provider.yaml` (so the runtime falls back to handoffs without them, and `fabrica auth login` can set them up), read them with `credentials.Lookup`, and add `api` to the front of its `apply_modes`. If the API can tell a good key from a bad one, add a verifier to `verifiers`, so `fabrica auth` can check it. The planner and apply already handle `api` mode, refresh, adoption and the checkpoints around creates. If the API can't set up everything its resources rely on, implement `provider.ManualSetup` too. `cursor.go` is the example, and `testkit.NewFakeCursor` shows how to test one with a fake server.

**Changing the run-history schema:** bump `runs.SchemaVersion`, add a migration from the previous version in `runs.migrate`, keep `run_overview` compatible, and update `docs/run-history.md`. The schema is a public interface.

**Changing the Fabrica line:** add the new sentence to the front of `fabline.accepted` and keep every old one. Converted repos must never break on upgrade.
