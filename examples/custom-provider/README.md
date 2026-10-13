# A custom provider

This example shows how a blueprint can teach Fabrica about a runtime it doesn't know, by shipping a **folder provider**. The runtime here is "Acme Runner", a pretend in-house agent runner. Its "API" is a JSON file on your disk, so you can watch exactly what Fabrica does.

```
blueprint/
  fabrica.yaml
  providers/acme-runner/
    provider.yaml      # the capability matrix: plain data
    api                # a tiny Python program that talks to the runtime
    handoff.md         # instructions for when there's no API to call
  routines/triage.md   # a routine that runs on Acme Runner
  skills/triage/SKILL.md
  connectors/github.yaml
```

## The three files of a provider

**`provider.yaml`** is the capability matrix. It says what Acme Runner can do: which triggers it has (`github.issues.opened`, which none of the built-in cloud runtimes support), which connectors, which side effects, and how it can pass the Fabrica token (in a header). Because it's plain data, `fabrica check` and `fabrica plan` read it without running anything. A provider whose `api` needs a key declares it under `credentials:` (there's a commented-out example at the end), and then `fabrica auth login` asks for it and Fabrica hands it to `api`.

**`api`** is a program Fabrica runs during `apply`. It can be written in any language. Fabrica runs `api <op>`, sends one JSON request on stdin, and reads one JSON response from stdout. There are four operations: `list`, `create`, `update` and `delete`. The important rule is that the runtime must store each resource's Fabrica **identity** and give it back from `list`. That's how Fabrica finds what it made, adopts things a crashed apply left behind, and never creates duplicates. Read the comments in `api` to see the whole protocol in about 90 lines.

**`handoff.md`** is a template for step-by-step instructions, used when a provider has no API (or as a fallback, since `apply_modes` lists `handoff` after `api`).

The full reference is in [docs/providers.md](../../docs/providers.md).

## Try it

You'll need `python3` and a product repo you can point at. Push the `blueprint/` folder to a git repo of its own (say `github.com/you/acme-blueprint`). Make sure your product repo has the [Fabrica line](../../docs/product-repos.md#the-fabrica-line) at the top of its `AGENTS.md`. Then, in a home repo:

```yaml
# factories/triage.yaml
blueprint: { git: github.com/you/acme-blueprint, ref: main }
server: https://fabrica.example.com
runtimes: [acme-runner]
routines: [triage]
targets: [github.com/you/your-app]
```

```sh
fabrica auth login https://fabrica.example.com     # press Enter to make a server secret
export ACME_RUNNER_DB=/tmp/acme-runner.json
fabrica plan       # one create, in api mode
fabrica apply      # runs `api create`
cat /tmp/acme-runner.json
fabrica plan       # in sync: `api list` finds the job by its identity
```

Now try breaking things, to see Fabrica fail closed:

- Copy a job inside `/tmp/acme-runner.json` under a second ID. `fabrica plan` now reports two resources sharing one identity, and refuses to pick one.
- Change the routine's trigger to `github.pull_request.opened`. `fabrica plan` shows an update, with the reason.
- Remove the job from the JSON file. `fabrica plan` notices it's gone and wants to create it again.

## Trust

Folder providers run code with your runtime credentials during `apply`. Imports are pinned by commit, and `fabrica upgrade` calls out changed provider code in its diff, so new provider code never arrives unseen. Still, only import providers from people you trust, and read the diff.
