# {{name}}

This is a [Fabrica](https://github.com/staltz/fabrica) blueprint: it writes down how our AI agents build software, once, so the same skills, routines and rules work in every AI coding tool we use.

It's about **process**, how we work. Knowledge about one product, like which test runner or database it uses, belongs in that product's own repo.

## Layout

The folder names are fixed.

```text
fabrica.yaml              the manifest: a description, imports and what product repos should provide
skills/<name>/SKILL.md    the longer how-to, as standard Agent Skills; agents load them when relevant
routines/<name>.md        work that runs by itself when something happens, like a new pull request
rules/<name>.mdc          short instructions that are always on, in Cursor's rule format
connectors/<name>.yaml    what routines use to reach the outside world, like GitHub or Slack
```

## Working on it

Run `fabrica check` after every change. It's strict on purpose: it catches typos in frontmatter, missing skills and connectors, and routines whose prompt mentions a skill they don't list.

Most changes take effect as soon as they're merged: factories that follow a branch pick them up on their next run, with no re-apply. Changing a routine's trigger or connectors is the exception, and needs `fabrica apply` in each home repo that uses it.

To use this blueprint, add a factory for it to a home repo, then run `fabrica plan` and `fabrica apply` there. [Getting started](https://github.com/staltz/fabrica/blob/main/docs/getting-started.md) walks through it, and [Writing a blueprint](https://github.com/staltz/fabrica/blob/main/docs/blueprints.md) has the full format.
