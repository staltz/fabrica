---
name: changelog
description: How to keep a CHANGELOG.md in the Keep a Changelog style, written for the people who use the software rather than the people who wrote it. Covers what belongs in it, how to word entries, how to mark breaking changes and how to cut a release. Use it when a change is something users will notice, when preparing a release, or when asked for release notes.
---

# Changelog

A changelog answers one question for the people who use the software: **"What's different for me in this version?"**

It's not a list of commits. Commit messages are for developers and say why the code changed. Changelog entries are for users and say what they'll notice.

We follow [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). A starting file is in `template.md`, next to this one.

## The shape of the file

The changelog lives in `CHANGELOG.md` at the root of the repo.

- The newest version is at the top.
- At the very top is an `## [Unreleased]` section, for changes that are merged but not released yet.
- Each version has a heading with its number and its release date, in `YYYY-MM-DD` format: `## [1.4.0] - 2026-10-12`. That date format reads the same everywhere in the world.
- Inside a version, entries are grouped under these headings, always in this order, and only when the group has entries:
  - `### Added` for new features.
  - `### Changed` for changes to existing behavior.
  - `### Deprecated` for features that will be removed later.
  - `### Removed` for features that are gone now.
  - `### Fixed` for bug fixes.
  - `### Security` for fixed vulnerabilities.

If the file has version links at the bottom (like `[1.4.0]: https://.../compare/v1.3.2...v1.4.0`), keep them up to date when you add a version.

## What goes in

Include anything a user of the software would notice:

- New features, options and commands.
- Changes in behavior, defaults, output or performance that people will feel.
- Bug fixes for problems users could hit.
- Anything deprecated or removed.
- Security fixes, always.

Leave out what users can't see: refactors, test changes, CI tweaks, internal renames. Dependency updates only go in when they change behavior or fix a security issue.

"Who uses the software" depends on the project. For an app, it's the people using the app. For a library, it's the developers calling it, so a changed function signature does belong in the changelog.

## How to write an entry

- **One line per change**, as a bullet.
- **Write from the user's side.** Say what they can do now, or what's different for them. Not which class you changed.
- **Use plain words.** No internal names, ticket codes or jargon a user wouldn't know.
- **Be specific.** "Improved performance" says nothing. "Search results load about twice as fast on large projects" does.
- **Link to the PR or issue** at the end, so people can find the details.

Before and after:

| Developer-speak | What users need to read |
|---|---|
| Refactored `InvoiceService` to use the new `PdfRenderer` | Invoices download about twice as fast. (#640) |
| Fix NPE in addr parsing | Fixed a crash when saving a profile without a street address. (#812) |
| Bump `left-pad` 1.2 → 1.3 | (Leave it out, unless it changes something users see.) |
| Add `--format` flag | Added `--format json` to `export`, for scripts that read the output. (#655) |

## Breaking changes

When users have to do something to keep things working, make it impossible to miss:

- Put it under **Changed** or **Removed**, and start the line with `**Breaking:**`.
- Say what breaks **and** what to do instead.

```markdown
### Removed

- **Breaking:** Removed the `--legacy` flag. Use `--format v1` instead, which gives the same output. (#701)
```

## When to update it

Add the entry **in the same pull request as the change**, under `## [Unreleased]`. Whoever cuts the release won't remember every change, and you know it best right now.

If a change is too small or too internal for the changelog, that's fine. Not every PR needs an entry.

## Cutting a release

1. **Read the Unreleased section** and tidy it up: merge duplicate entries, fix wording, check the groups.
2. **Pick the version number.** If the project uses [Semantic Versioning](https://semver.org/):
   - Anything breaking means a new **major** version (2.0.0).
   - Anything added, with nothing breaking, means a new **minor** version (1.5.0).
   - Only fixes means a new **patch** version (1.4.1).
3. **Rename** `## [Unreleased]` to the new version and today's date, and add a fresh, empty `## [Unreleased]` above it.
4. **Update the links** at the bottom of the file, if there are any.
5. **Ship it.** If the repo provides its own `deploy` skill, follow it now. If it doesn't, stop here and tell the user the changelog is ready, and what version you picked and why.
