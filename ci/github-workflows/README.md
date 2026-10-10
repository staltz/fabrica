# GitHub Actions workflows

These are Fabrica's own CI and release workflows:

- `ci.yml` runs the tests (with the race detector), `go vet`, `staticcheck` and `gofmt` on Linux and macOS, and checks that the examples still pass `fabrica check`.
- `release.yml` builds static binaries for every platform when you push a `v*` tag, and publishes them with checksums as a GitHub release, which is where `install.sh` downloads from.

They live here instead of in `.github/workflows/` only because they were written in a session whose GitHub access couldn't push workflow files. To switch them on, move them into place:

```sh
mkdir -p .github/workflows
git mv ci/github-workflows/ci.yml ci/github-workflows/release.yml .github/workflows/
git rm ci/github-workflows/README.md
```
