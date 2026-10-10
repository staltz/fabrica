# Example team home repo

A home repo says which blueprint runs on which product repos, in which runtimes. This one belongs to a team, so it only has **cloud** factories.

```
factories/
  pr-review.yaml        review every new PR, on Claude and Cursor, in two repos
  dependencies.yaml     a weekly dependency report, frozen at a tag
.github/workflows/
  fabrica.yml           apply on merge, plan every night
```

Once `fabrica apply` has run, there's also a `state/` folder, with one file per factory. Only Fabrica writes those, and they're committed here, so everyone shares what's installed where.

A few things to notice:

- `pr-review` uses `ref: stable`, a branch, so it **follows** it: a merge to the blueprint's `stable` branch takes effect on the next review, with no apply. `dependencies` uses a tag, so it's **frozen**.
- Params are set once at the top, and `github.com/acme/api` overrides `slack_channel` for itself.
- Both factories point at the same Fabrica server. Each gets its own endpoint and its own token.

The workflow applies after every merge and plans every night. If you'd rather apply by hand, `fabrica plan` and `fabrica apply` work the same from your laptop, and the apply lock keeps you and CI from stepping on each other.

Handoffs (for runtimes without an API, which is all the built-in cloud ones in v0.1) contain the factory's token, so the workflow keeps them as a short-lived build artifact instead of committing them. Someone downloads them, follows them, and runs `fabrica confirm` from a checkout of this repo.

See [docs/home-repos.md](../../docs/home-repos.md) for everything about factories, state and the apply lock.
