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
    task backend:test:property     # rapid property tests (pinned seed; RAPID_SEED=0 randomizes)
    task backend:fuzz              # fuzz targets, FUZZTIME each (default 2m)
    task ci                        # exactly what CI runs

Nightly CI runs both the property and fuzz lanes for longer and with a
randomized seed (`RAPID_SEED=0`, extended `RAPID_CHECKS`/`FUZZTIME`); PR CI
runs them pinned and short.

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
drop the old key from the keyset. Only drop the old key once `caesar
admin rekey` has exited 0 — dropping it beforehand strands any
credential not yet re-wrapped.

## API

Routes live under `/api/v1`. Errors are RFC 9457 problem+json
(`type`, `title`, `status`, `detail`, `correlation_id`, `errors[]`).
Each endpoint is one `httpx.Operation` declaration — route and OpenAPI
document come from the same source, so they can't drift. stdlib's own
405 responses (method not allowed on a registered path) are plain text,
not problem+json — an accepted V1 limitation. Resources: identity
(users, sessions, grants), assets (projects, repos, credentials,
harnesses), and workflows (`/api/v1/workflows` + `/versions`) — writes
are admin-only, and the single write door validates every document. The
execution spine adds `/api/v1/tasks` and `/api/v1/runs`: reads need the
`view` grant on the project, creating and re-running a task needs `run`.

## Execution spine (Plan 05)

Tasks, runs, attempt rows, artifacts and run events live in
`internal/runs`. A task pins its repo and workflow version; each run
pins the base SHA and the `PolicySnapshot` in force at intake, carried
as jsonb plus the denormalized budget and deadline columns every
deciding SQL statement predicates on. Status vocabularies and the
transition tables for runs and attempt rows are
`internal/runs/status.go` — the source of truth that the CAS sites, the
property suite, and the schema's CHECK constraints all follow.

PhaseRun *is* the queue; there is no jobs table. Claiming is one
statement: a locking CTE (`FOR UPDATE ... SKIP LOCKED`) takes the oldest
pending attempt whose run is running with its wall deadline unexpired
and its budget unexhausted, and the same statement takes the lease.
Heartbeats extend `leased_until`; a heartbeat that matches no row means
the worker is fenced and must abandon the attempt. Lease expiry never
auto-retries — the janitor moves the row to `reconciling` and asks the
executor's `Inspect` first: a completed attempt with intact outputs is
adopted idempotently, anything else fails transient (`reconcile_lost`).
A retry is then a NEW attempt row inserted in the same transaction that
terminalizes its predecessor, so attempt rows are immutable history;
partial unique indexes keep at most one live run per task and at most
one live attempt per (run, node).

Two clocks bound a run. The wall deadline is a column compared against
the **database** clock (`now()`) inside every deciding statement, so
there is no read-then-decide race. The execution budget is milliseconds
accumulated claim→terminal on the **service** clock, added and checked
in the same transaction that terminalizes the attempt. A worker's
attempt context deadline is whichever of the two comes first. Both
numbers come from the run's pinned snapshot (3 attempts, 2h budget, 24h
wall by default); Plan 09 replaces the evaluation, never the pinning.

Every terminal state change and materialization appends its run events
in the same transaction; the two observational events (`phase.started`,
`phase.reconciling`) are appended in a transaction adjacent to the claim
statement and the reconciling flip. Ids
are allocated from the single `run_event_counter` row locked FOR UPDATE,
so id order is commit order — a sequence would let a tailer's cursor
skip a late commit — and each event also writes one outbox envelope. V1
has no outbox consumer (Plan 10's SSE tailer is the first); the janitor
prunes envelopes older than `orchestrator.outbox_retention`, 7 days by
default. The rest of the queue loop is tuned under the same config key:
`workers`, `poll_interval`, `lease_ttl`, `heartbeat_interval`.

The production posture is honest, not simulated. `serve` wires
`runs.NotImplementedExecutor` and a forge that refuses to resolve a
head, so task intake fails with "forge adapters arrive in Plan 08" (no
run can pin a base SHA), a definition containing gate or `git-publish`
nodes is rejected at intake (`waiting` stays unreachable until Plan 08),
and any phase claimed under production wiring dies `not_implemented`
until Plan 06's Docker runner lands. Stubs never fake success. The
deterministic fakes in `internal/runs/fake` are what drive the
golden-set integration test, which takes a run end-to-end in CI.

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
