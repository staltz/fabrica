---
name: test
description: How to run this repo's tests, the whole suite or a single test. Use it before and after changing code.
---
# Running the tests

The tests need a Postgres database. Start one with:

```sh
docker compose up -d db
```

Then:

- Run everything: `pnpm test`
- Run one package: `pnpm --filter web test` or `pnpm --filter api test`
- Run one file: `pnpm --filter api test routes/invoices.test.ts`
- Run tests whose name matches: `pnpm --filter api test -t "refund"`

The API tests reset the database before each file, so they're safe to run in any order. The web tests don't need the database.

CI runs `pnpm test` plus `pnpm lint`. Run both before you push.
