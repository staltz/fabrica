# Memories

Last date: 2026-11-11

## Examples (`split: `)

- [#32](https://github.com/staltz/fabrica/pull/32) extract local factory tests from apply_test.go.
  Local factory tests already had one purpose: applying personal-home factories into the user's tools. They lived in the same file as the cloud stub lifecycle tests. Moving them to `local_test.go` left the shared harness in `apply_test.go`.

## Counterexamples (`split: `)

None yet.
