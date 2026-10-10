Before starting, load this repo's working rules from the Fabrica MCP server; if you don't have it, ignore this line.

# Acme App

This is Acme's customer-facing web app: a Next.js front end and a small Node API, backed by Postgres.

## Getting around

- `web/` is the Next.js app. Pages live in `web/app/`, shared components in `web/components/`.
- `api/` is the API server. Each resource has its own folder in `api/routes/`.
- `services/billing/` is the only code that talks to Stripe. Keep it that way.
- Database migrations live in `api/migrations/`, one file per change, and are never edited after they're merged.

## Conventions

- Use `pnpm`, not `npm` or `yarn`. The lockfile is `pnpm-lock.yaml`.
- TypeScript is in strict mode everywhere. Don't add `any` without a comment explaining why.
- Money is always stored in cents, as an integer.
- Dates go over the wire as ISO 8601 strings in UTC.
