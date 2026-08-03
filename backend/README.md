# caesar backend

Go control plane. Spec: `../docs/superpowers/specs/2026-08-02-caesar-v1-design.md` · Decisions (LOCKED): `../docs/brainstorm/2026-08-01-decisions.md`.

## Layout law

    cmd/caesar/            thin entry — imports internal/app, nothing else
    internal/app/          composition root: config→logger→pool→migrate→serve wiring, subcommands
    internal/platform/     shared kernel (no business logic): buildinfo, config, httpx, logging, pg, testdb, xerrors
    internal/<context>/    bounded contexts (Plan 2+): peers, never import each other
    internal/arch/         architecture tests — the law, executable
    internal/analyzers/    house-law go/analysis analyzers

Enforced mechanically — `internal/arch` (import graph, deny-list, dead config)
and `internal/analyzers` (env access only in platform/config; stubs return
xerrors.NotImplemented; no tx types in exported context APIs). Violations fail
CI. Fix the code, never the rule.

## Dev loop

    task db:up                     # dev postgres (compose.dev.yaml)
    task backend:run               # serve with caesar.dev.json
    task backend:test              # unit, race
    task backend:test:integration  # real postgres via testcontainers
    task ci                        # exactly what CI runs

## Config

Strict JSON config (`caesar.dev.json` locally); unknown keys are
rejected. Two env vars override file values, for bootstrap secrets
only:

- `DATABASE_URL` — overrides `database.url`.
- `CAESAR_MASTER_KEY` — overrides `security.master_key`: the credential
  master keyset, `key_id:base64-32B[,key_id:base64-32B...]`
  (comma-separated, first entry active — it encrypts new envelopes,
  the rest stay decrypt-only for reads). Generate a key with
  `openssl rand -base64 32`; the dev keyset already lives in
  `caesar.dev.json`. `serve` and `admin rekey` require it; `migrate`
  and `admin bootstrap` do not.

## Migrations

Desired state lives in `internal/platform/pg/schema.sql`. Author with
`task db:new -- <name>` (Atlas diff → goose files in
`internal/platform/pg/migrations/`). Runtime: embedded goose under a Postgres
advisory lock at startup, or `caesar migrate up|down|status`.

## Auth

Passwords hash with argon2id: memory 19456 KiB, time 2, parallelism 1, salt
16 B, key 32 B, stored as a PHC string. Sessions live in a `__Host-` cookie
(`__Host-caesar_session`; `Path=/; Secure; HttpOnly; SameSite=Lax`) with idle
and absolute TTLs from `auth.session_idle_ttl` / `auth.session_absolute_ttl`
(336h / 2160h by default). Login rate limiting is dual-scope: per-account
locks after 5 consecutive failures, backoff `30s × 2^(n−5)` capped at 15
minutes, reset on success; per-IP is a fixed 15-minute window, 30 failures
locks it for 15 minutes. Passwords must be 12–512 characters; no composition
rules. Create the first admin with:

    caesar admin bootstrap -email <email> < password.txt

Rotate the credential master key with `caesar admin rekey -config
<path>`: prepend the new key to `CAESAR_MASTER_KEY` (or
`security.master_key`) ahead of the current one, run the rekey (it
re-wraps every credential envelope under the now-active key), then
drop the old key from the keyset.

## API

Routes live under `/api/v1`. Errors are RFC 9457 problem+json
(`type`, `title`, `status`, `detail`, `correlation_id`, `errors[]`).
Each endpoint is one `httpx.Operation` declaration — route and OpenAPI
document come from the same source, so they can't drift. stdlib's own
405 responses (method not allowed on a registered path) are plain text,
not problem+json — an accepted V1 limitation. Resources: identity
(users, sessions, grants) and assets (projects, repos, credentials,
harnesses).

## Contracts

`task contracts:gen` regenerates the OpenAPI document and the TS client
types from the Go operation table; `task contracts:check` fails on drift.
Generated artifacts are committed and never hand-edited. The TS lane
needs Node ≥ 20.

## sqlc

Edit `internal/platform/pg/schema.sql` and the relevant `queries.sql`,
then `task db:new -- <name>` (migration) and `task db:sqlc` (regenerate
typed Go from the queries).

## Conventions

- Strict JSON config; env overrides only DATABASE_URL and CAESAR_MASTER_KEY.
- slog JSON to stdout; correlation id on every request and log line.
- TDD; table-driven tests; behavioral names; stdlib asserts + go-cmp.
- Tool versions pinned in the root Taskfile; `task tools` installs to backend/bin.
- Commits: `type(scope): one short sentence`, no body, no AI attribution.
