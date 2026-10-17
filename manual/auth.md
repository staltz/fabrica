# Credentials

`fabrica apply` deploys your factories into other services, so it needs access to them. This page explains what it needs, how `fabrica auth` sets it all up in a minute, and where your credentials are kept.

The short version:

```sh
fabrica auth          # what does apply need, and does each credential work?
fabrica auth login    # set up whatever is missing
```

## What apply needs

| What | Who needs it | Where it comes from |
|---|---|---|
| **The secret of each Fabrica server** | Cloud factories. Apply makes each factory's token with it (`HMAC(secret, factory name)`), and the server checks every token with the same secret. | Fabrica makes one for a new server, or you paste the one your server already uses. |
| **Runtime API credentials** | Runtimes Fabrica drives through an API: Cursor automations (`CURSOR_API_KEY`), and folder providers that declare [`credentials:`](#for-provider-authors). Without them, a runtime falls back to handoffs if it can, and otherwise can't be applied. | The page `fabrica auth login` opens for you. |
| **Git access** | Every factory. Apply reads blueprints and (for cloud factories) product repos, and pushes state to the home repo. | Whatever your git already uses: a credential helper, an SSH key, or the GitHub CLI. Fabrica keeps no git credentials. |

Local factories, the ones on your laptop, only need git access to their blueprint. Runtimes that use handoffs (Claude routines, Codex and Copilot automations, as of October 2026) need nothing at all: you follow the handoff yourself.

## `fabrica auth`

`fabrica auth` (or `fabrica auth status`) reads your home repo, works out every credential its factories need, and checks each one:

```
$ fabrica auth
Home repo /home/you/acme-home (team home repo)

Server secrets
  ✗ https://fabrica.acme.dev: the server secret, for factory pr-review
    not set, so apply can't make the factories' tokens

Runtime credentials
  · CURSOR_API_KEY: a Cursor API key, for cursor-cloud (factory pr-review)
    not set, so cursor-cloud uses handoffs instead of its API

Git access
  ✓ github.com/acme/home: the home repo, where apply records state
  ✓ github.com/acme/app: target of pr-review
  ✓ github.com/acme/blueprint: blueprint of pr-review

✗ 1 problem. Run `fabrica auth login` to fix what Fabrica can.
```

✓ means it's set up and works. ✗ means apply would fail: it's missing, refused, or can't be read. · is an optional credential that isn't set, and ! is one Fabrica couldn't check just now (say, the server is down). It exits `1` when there's a ✗, so it works as a CI step too.

It doesn't just look for credentials, it asks each service whether they work:

- **A server secret:** Fabrica asks the server whether it accepts tokens made with the secret, without ever knowing the server's own. For one of the factories it serves, it first sends a token that can't be right, which a Fabrica server refuses with a `401`, and then the token made with your secret. Both are plain GET requests that change nothing on the server, and neither follows redirects. So a typo, or another server's secret, shows up here, instead of as a `401` from every routine after you apply. A brand-new factory can't be checked until the server serves it, about a minute after it's merged.
- **A Cursor API key:** Fabrica exchanges it for a session token, the first thing apply does with it.
- **A folder provider's credential:** Fabrica has no way to check it, since only the provider's `api` program knows how to use it.
- **Git access:** `git ls-remote` on each repo, which reads nothing but its HEAD.

`--no-check` skips all of that, and only says what's set.

## `fabrica auth login`

`fabrica auth login` walks you through everything that's missing or refused, and skips what already works:

```
$ fabrica auth login
Setting up credentials for home repo /home/you/acme-home.
Fabrica saves them in your macOS Keychain.

https://fabrica.acme.dev: the server secret, for factory pr-review
  Your Fabrica server and `fabrica apply` share this secret: apply makes each factory's token with it, and the server checks every token with it.
  The server is running factory pr-review, so it has a secret already.
  Paste the secret your server uses, or press Enter to make a new one:
  ✓ Yes: the server accepts it (checked with factory pr-review).
  ✓ Saved in your macOS Keychain.

CURSOR_API_KEY: a Cursor API key, for cursor-cloud (factory pr-review)
  Without it, cursor-cloud uses handoffs instead of its API.
  Set it up now? [Y/n]
  Paste it, or press Enter to open https://cursor.com/dashboard/api and make one there:
  Opened it in your browser.
  Create a new API key, call it fabrica, and copy it.
  Then paste it here (or press Enter to skip):
  ✓ Cursor accepted it.
  ✓ Saved in your macOS Keychain.

Git access
  ✓ git can read all 3 repos apply uses.

To give the home repo's GitHub Actions the same credentials:
  fabrica auth env | gh secret set --env-file - --repo acme/home
Run `fabrica auth` to check everything, and `fabrica plan` to see what apply would do.
```

What you paste isn't shown on screen. Here's what it does for each kind of credential:

- **A server secret.** Paste the secret your server uses, and Fabrica checks it with the server before saving it. Setting up a new server? Press Enter, and Fabrica makes a new secret (64 random hex characters, like `openssl rand -hex 32`). Then [give your server the same secret](#giving-your-server-and-ci-the-same-credentials). If the server is already running, Fabrica asks before making a new one, because that rotates every factory's token.
- **A runtime credential.** Press Enter, and Fabrica opens the page where you make one, and says what to click. Paste the key, and Fabrica checks it with the runtime before saving it, so a bad paste is caught right there. As of October 2026, Cursor offers no way for a program to make an API key for you (its API has no sign-in flow), so this is the shortest path there is.
- **Git.** When git can't read a repo on github.com, Fabrica offers to sign you in with the [GitHub CLI](https://cli.github.com): `gh auth login --web` signs you in in your browser, and `gh auth setup-git` lets git use that sign-in. Fabrica never sees the GitHub token: `gh` keeps it in your keychain and hands it to git. Without `gh`, Fabrica says how to install it. For other hosts, set up a credential helper or an SSH key.

Name credentials to set up only those, or to replace ones that work: a server's URL, an environment variable, a runtime (all its credentials), or `git`.

```sh
fabrica auth login https://fabrica.acme.dev
fabrica auth login cursor-cloud
fabrica auth login git
```

Without a terminal, login reads answers from stdin, one per line, so scripts can pipe a credential in:

```sh
echo "$KEY" | fabrica auth login CURSOR_API_KEY
```

## Where credentials are kept

Fabrica looks for each credential in two places, in this order:

1. **Its environment variable:** `FABRICA_SERVER_SECRET`, `CURSOR_API_KEY` (or `CURSOR_TOKEN`), or whatever a folder provider declares. When it's set, even to nothing, it wins, and Fabrica looks nowhere else. So CI works exactly as before, and `CURSOR_API_KEY= fabrica plan` shows what happens without your saved key. `FABRICA_SERVER_SECRET` counts for every server.
2. **Where `fabrica auth login` saved it:**
   - **macOS:** your login keychain, through `/usr/bin/security`. Keychain Access shows each one as "Fabrica: <name>", under service `fabrica`.
   - **Linux:** the Secret Service (GNOME Keyring, KWallet), through `secret-tool` (in the `libsecret-tools` package, or your distribution's equivalent), when there's a desktop session to talk to.
   - **Anywhere else** (servers, containers, SSH sessions): `~/.config/fabrica/credentials.json` (or `$XDG_CONFIG_HOME/fabrica/`), a file only you can read, in a folder only you can open. Fabrica refuses to read it if anyone else can, the way SSH treats a private key. Set `FABRICA_KEYCHAIN=off` to use this file even where there's a keychain.

A few things hold everywhere:

- **Each server has its own secret.** Secrets are saved per server URL, so setting up one server never changes another's tokens. A factory only gets the secret of the server its `server:` names, so a factory pointed at some other server gets no secret to send there.
- **Secrets never go on a command line**, where other users on the machine could see them: keychain tools get them on stdin.
- **Secrets are never printed**, except by `fabrica auth env`, which exists to print them.
- **Secrets never go in a git repo.** The credentials file lives in your config folder, never in your home repo.

To be clear about what a keychain protects: it keeps credentials encrypted on disk and locked while you're logged out. It doesn't hide them from programs you run yourself, which can ask the same keychain tool for them. That's true of every command-line tool's keychain entries, `gh`'s included.

## Giving your server and CI the same credentials

`fabrica auth env` prints credentials as `NAME=value` lines, the format of a `.env` file. That's what Docker's `--env-file`, `gh secret set --env-file` and most hosting platforms read, so you can pipe them straight where they go:

```sh
# Your home repo's GitHub Actions secrets
fabrica auth env | gh secret set --env-file - --repo acme/home

# Your Fabrica server's environment
fabrica auth env https://fabrica.acme.dev > fabrica.env
docker run -d --name fabrica --env-file fabrica.env -v fabrica-data:/data -p 8080:8080 \
  fabrica mcp --http --home https://github.com/acme/home.git
rm fabrica.env
```

Without names, it prints every credential the home repo needs that's set up, and leaves out the rest. A server secret comes out as `FABRICA_SERVER_SECRET`, which holds one secret, so when your factories use several servers with different secrets, name the one you want.

On a machine where you ran `fabrica auth login`, the server can also read its secret itself: `fabrica mcp --http --url https://fabrica.acme.dev` uses the secret saved for that URL when `FABRICA_SERVER_SECRET` isn't set.

## The admin token

Run history is for people, not agents, so a remote server has one more token, which only people use: the **admin token**. It opens the server's [admin API](mcp-server.md#the-admin-api), where run history can be read, and nothing else. It's not a factory token, and no factory token opens the admin API. It works the same way a factory's token does, computed from the server secret:

```
admin token = "fab_admin_" + base64url(HMAC-SHA256(server secret, "_admin"))
```

Factory names can't contain an underscore, so no factory can be called `_admin`, and its token can never be the same. Because it's computed, there's nothing new to store or rotate: the server checks it with the secret it already has.

Get it with `fabrica auth admin-token`:

```sh
fabrica auth admin-token https://fabrica.acme.dev
```

Fabrica finds the server secret the way `fabrica auth` does: `FABRICA_SERVER_SECRET`, or else the one `fabrica auth login` saved for that server. Then it prints the admin token, and only that, so it's easy to pipe where it goes. Without a URL it uses the only server your home repo's cloud factories use; with several, it asks you to name one.

**Who to give it to.** People who should read run history: you, your teammates, whoever runs your retros. They don't need the server secret. Someone with only the admin token can read run history, but can't make factory tokens, can't change anything on the server, and can't connect to a factory as an agent. They set it in `FABRICA_ADMIN_TOKEN`, which `fabrica runs --server` reads, or send it as `Authorization: Bearer <token>` to the admin API.

**Who never gets it.** Agents. Cloud runtimes only ever get their own factory's token, and the admin token must stay out of stubs, connectors, prompts and anything else a cloud agent can read. That's what keeps a prompt injection from reading what other routines did.

**Rotating it.** The admin token comes from the server secret, so changing the server secret changes it too. Everyone who had the old admin token gets a `401` until you give them the new one.

## `fabrica auth logout`

`fabrica auth logout` forgets saved credentials. Name a server's URL, an environment variable or a runtime:

```sh
fabrica auth logout cursor-cloud
```

It leaves your environment variables alone, and says so when one is still set. Fabrica keeps no git credentials, so to sign git out of GitHub, use `gh auth logout`.

## For provider authors

A [folder provider](providers.md#folder-providers) whose `api` program needs credentials can declare them in its `provider.yaml`. Then `fabrica auth` lists them, `fabrica auth login` asks for them (opening `url`, if you give one), and Fabrica hands the saved ones to your `api` program as environment variables:

```yaml
credentials:
  ACME_API_KEY:
    description: an Acme API key                   # a few words: what it is
    url: https://acme.dev/settings/api-keys        # optional: the page where you make one
    instructions: Click New key, and copy it.      # optional: what to do on that page
    aliases: [ACME_TOKEN]                          # optional: other variables that work too
```

A runtime only uses `api` mode when every credential it declares is set. Otherwise it falls back to its next apply mode, like `handoff`, and `plan` says why. With no mode to fall back to, `plan` reports an error that names the missing credential.

## Why it works this way

Some choices here are deliberate, and worth explaining:

- **No OAuth app of Fabrica's own.** As of October 2026, none of the runtimes' automation APIs offers a sign-in flow: Claude routines, Codex and Copilot automations have no API for them at all, and Cursor's takes API keys made in its dashboard. GitHub does have one, and the GitHub CLI already does it well, so Fabrica uses `gh` rather than holding GitHub tokens itself. When a runtime ships a sign-in flow, `fabrica auth login` is where it goes.
- **No hosted login service.** Fabrica has no hosted service, and won't. Everything here runs on your machine.
- **A shared server secret, rather than per-token storage.** The server checks tokens without storing any, which keeps it simple to run. `fabrica auth` takes the sting out of sharing it: it makes the secret, checks it against the live server, and hands it to the server and CI in one pipe each.
- **Environment variables always win.** CI, containers and scripts already pass credentials that way, and an explicit variable is the clearest way to say "use this one, now".
