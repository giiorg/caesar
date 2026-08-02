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

## Migrations

Desired state lives in `internal/platform/pg/schema.sql`. Author with
`task db:new -- <name>` (Atlas diff → goose files in
`internal/platform/pg/migrations/`). Runtime: embedded goose under a Postgres
advisory lock at startup, or `caesar migrate up|down|status`.

## Conventions

- Strict JSON config; env overrides only DATABASE_URL (+ CAESAR_MASTER_KEY from Plan 3).
- slog JSON to stdout; correlation id on every request and log line.
- TDD; table-driven tests; behavioral names; stdlib asserts + go-cmp.
- Tool versions pinned in the root Taskfile; `task tools` installs to backend/bin.
- Commits: `type(scope): one short sentence`, no body, no AI attribution.
