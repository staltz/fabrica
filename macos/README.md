# Fabrica.app

A small macOS app for Fabrica. It lives in the menu bar (or the Dock, or both), and comes with the `fabrica` CLI inside it.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="../docs/assets/fabrica-app-dark.png">
  <img src="../docs/assets/fabrica-app-light.png" alt="Fabrica.app showing a factory: its blueprint, the tools it runs in, and its repos">
</picture>

- **My Factories.** Edit your personal home repo (`~/.fabrica`, or `FABRICA_HOME`) with forms, never YAML: the blueprint, where it runs, which repos, which routines, params, and init. Turn a factory off without deleting it: the app saves it as `<name>.off`, which Fabrica ignores. Every save is a git commit, because `fabrica apply` refuses uncommitted factory changes and the server only serves what's committed. **Push** sends those commits to your home repo's remote. It only ever fast-forwards: it never merges or rebases.
- **Plan and Apply.** Each factory has a status dot from `fabrica plan` (green in sync, orange changes to apply, red problems), and the problems show up on the factory itself. The toolbar runs `fabrica plan` and `fabrica apply` and shows their output as it's printed.
- **Server.** Starts and stops `fabrica mcp --http` on this Mac, for your cloud factories: pick the `server:` URL to serve (it uses the secret `fabrica auth login` saved for it), and where to listen. It shows the server's health, each factory's endpoint and the output. The tools on your Mac don't need it, since they start their own `fabrica mcp` over stdio. It's for when cloud runtimes should reach a server on this Mac, through a tunnel or a proxy. The server stops when the app quits.
- **Recent Runs.** Fabrica's run history (the `run_overview` view), from this Mac's `runs.db` or from the server's own database. The CLI has no command for it yet.

## Installing

Download [Fabrica-macos.zip](https://github.com/staltz/fabrica/releases/latest/download/Fabrica-macos.zip) from the latest release (each release also has its `.sha256`), unzip it, and move Fabrica.app to Applications. It needs macOS 15 or newer, and runs on Apple silicon and Intel.

A release built without a Developer ID is signed ad hoc, and macOS won't open it the first time. Try to open it, then go to System Settings → Privacy & Security and click **Open Anyway**. Or clear the quarantine flag yourself:

```sh
xattr -dr com.apple.quarantine /Applications/Fabrica.app
```

## The CLI comes with it

On first launch the app links `~/.local/bin/fabrica` (where `install.sh` puts it) to the `fabrica` inside the app, so updating the app updates the CLI. If a separate `fabrica` is there already, the app leaves it alone and offers to swap it for its own (the old one goes to the Trash). If `~/.local/bin` isn't on your PATH, it offers to add it to `~/.zprofile`.

A `fabrica` that came with the app refuses `fabrica -u` and says to update the app instead, since replacing a file inside the app would break its signature.

The app runs `fabrica` and `git` with your login shell's PATH, so they find `git`, `claude`, `gh` and the rest the same way they do in Terminal.

## Building

You'll need Xcode and Go.

```sh
make app                  # dist/Fabrica.app, universal, signed ad hoc
VERSION=v0.1.0 make app   # stamp a release version into both
```

Then move `dist/Fabrica.app` to `/Applications`. For working on the app itself, `swift run` and `swift test` work in this folder; a `swift run` build has no `fabrica` inside, so it uses the one on your PATH.

| File | What it is |
|---|---|
| `Sources/Fabrica/Factory.swift` | A factory file, read with Yams and written in the style of Fabrica's examples. The keys mirror `internal/home/factory.go`. |
| `Sources/Fabrica/HomeRepo.swift` | The factories folder, git, and `fabrica plan --json`. |
| `Sources/Fabrica/Server.swift` | `fabrica mcp --http` as a child process, and its `/healthz`. |
| `Sources/Fabrica/CLI.swift` | The bundled CLI and its link in `~/.local/bin`. |
| `Sources/Fabrica/Runs.swift` | Reads run history straight from SQLite, read-only. |
| `build.sh`, `scripts/make-icon.swift` | Builds the bundle, and draws the icon from the favicon's mark. |

## Releases

Pushing a `v*` tag runs [release.yml](../.github/workflows/release.yml). After the CLI binaries are published, a macOS job tests and builds the app, and adds `Fabrica-macos.zip` and its `.sha256` to the same release. CI builds the app on every push too.

`make app` signs ad hoc. With `CODESIGN_IDENTITY` set to a Developer ID Application identity, it signs with that, using the hardened runtime. The release job does that, and notarizes the app, once the repo has the secrets listed at the top of its `app` job: the certificate as a base64 `.p12` and its password, the identity's name, and an Apple ID, team ID and app-specific password for `notarytool`. Without them, the release still ships, signed ad hoc.
