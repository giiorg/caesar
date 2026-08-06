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

## Docker runner and egress gateway (Plan 06)

`internal/runs/runner` implements `runs.Executor` for real: every claimed
phase becomes a four-container pipeline on the host Docker daemon
(hand-rolled Engine API client in `internal/platform/dockerapi`, pinned
`v1.43`, no moby dependency):

1. **stage** (`caesar-git` image) — clones the repo at the pinned base
   SHA, sanitizes, verifies, and lays out the workspace. Only container
   that ever sees the forge credential (env only, never argv or disk).
2. **egress gateway** (`caesar egress-proxy`, distroless image) — the
   credential broker + CONNECT proxy. Holds the provider credential and
   only the SHA-256 of the attempt-scoped broker token.
3. **agent** (harness image) — runs the coding agent against
   `/workspace`, the only mount it gets. No credentials; talks to the
   world exclusively through the gateway.
4. **extract** (`caesar-git` image, networkless) — computes the
   canonical patch against the trusted base mirror.

Two networks per attempt: the agent and gateway share a fresh
`internal: true` attempt network (no route out), and the gateway is
additionally attached to the shared `caesar-egress` bridge — it is
dual-homed precisely so that the *only* path from the agent to the
internet is through a process we wrote, which enforces per-provider
operation allowlists (broker) and an exact-hostname, 443-only CONNECT
allowlist that refuses private/loopback/link-local ranges and dials the
vetted IP directly. Every container runs the full hardening profile:
`CapDrop ALL`, `no-new-privileges`, default seccomp, read-only rootfs,
tmpfs `/tmp`, non-root (the runner refuses to start as uid 0),
mem/cpu/pids limits.

The staging contract (`gitops.Stage`, run as `caesar stage` inside the
stage container) has five clauses, in order:

1. **Auth never in the URL** — userinfo in the clone URL fails typed
   before any git runs; the credential travels only via env-config
   `http.extraheader`, which git never persists to disk.
2. **Clone bare + verify the pin** — `git clone --bare` to `base.git`,
   then `cat-file -e <sha>^{commit}`; a missing pin is permanent
   `base_sha_unavailable`.
3. **Materialize** — local clone of `base.git` into `workspace/` and a
   detached checkout at the SHA; the workspace `.git` is
   hostile-by-assumption from this point on.
4. **Sanitize** — reset `origin` to the credential-free URL and assert
   neither git config carries `extraheader`/`askpass`/`credential` keys.
5. **Verify (negative scan)** — no `.git-credentials` anywhere, the
   credential value appears in no clone-metadata file, and the fresh
   clone alone must fit the workspace quota. Any verify miss is
   permanent `staging_verification_failed`: a workspace that fails
   verification is never handed to an agent.

Extraction never executes git *against* the workspace's `.git`: the
agent had two hours alone with it, so its config, hooks, and fsmonitor
settings are attacker-controlled. `gitops.Extract` runs every git call
with `GIT_DIR` pointed at the trusted `base.git` mirror and
`GIT_WORK_TREE` at the workspace, making the hostile `.git` inert data;
gitlinks (embedded repos) are rejected (`embedded_repo`), and the
container itself has no network to exfiltrate through.

Enforcement is control-plane-side, typed, and pinned (config-overridable
via the `runner` block):

| mechanism | limit | on breach |
|---|---|---|
| workspace quota (2s poll) | 2 GiB / 200000 files | agent killed, permanent `workspace_quota_exceeded` |
| log cap (per stream) | 16 MiB | stream truncated, `phase.log_truncated` event |
| log flood kill | 64 MiB | agent killed, permanent `log_flood` |
| free-space watermark | 10 GiB free on `data_root` | claims refused via `runs.Admission` before work starts |

Logs persist under `data_root/logs/<ref>/` and surface as `log_ref` /
`failure_snapshot` artifacts (broker token and provider credential
values scrubbed); everything else under `data_root/attempts/<ref>/` is
torn down on every exit path, with a label-scoped orphan sweep behind it.

Run the suite (build tag `runnerintegration`, its own CI lane):

    task image:build && task backend:test:runner

It needs a real Docker daemon and both images; it skips only when the
images are missing, never when the daemon is absent. The e2e test
bind-mounts `t.TempDir()`, so it expects a Linux daemon (or a Docker
Desktop whose file sharing covers the OS temp dir).

The production posture stays honest: `runner.enabled` defaults false
(`NotImplementedExecutor` + `AlwaysAdmit`, exactly Plan 05), and task
intake is refused until Plan 08's forge adapters land, so an enabled
runner is unreachable today — the flag exists so config, wiring, and
validation are real now and the deploy stack flips one boolean. In prod,
network isolation must not rest on Docker alone: the host firewall must
deny the agent subnets everything except the gateway (baseline in the
Plan 14 deployment runbook).

## Harness filesystem contract (Plan 07)

`/workspace` is the agent's only mount and its whole world. Everything a
phase was handed, and everything it hands back, moves through one
reserved directory inside it — `.caesar/`, laid out by the stage
container before the agent starts:

    /workspace/                     the repo, checked out detached at base_sha
      .caesar/in/instructions.md    the node's instructions, verbatim (may be empty)
      .caesar/in/inputs.json        {"task_inputs": {...}, "inputs": [{"id","type","name"}]}
      .caesar/in/<idx>-<type>.json  one file per non-patch input artifact, payload verbatim
      .caesar/out/                  created empty; the harness's side of the contract
      .caesar/out/artifacts.json    optional: [{"type","payload"}, ...]

`in/` is always complete: `instructions.md` and `inputs.json` exist even
for a phase with no lineage at all (`task_inputs` an object, `inputs` an
array — never `null`), so a harness entry script can read them
unconditionally. `<idx>` is the input's position in the node's declared
input list, so the filenames are stable and collision-free; `name` in the
manifest points at the file, and is absent for a patch input, which
arrives as tree content rather than as a file.

`out/artifacts.json` is the declarable allowlist from Plan 06: `plan`,
`review_verdict` and other work products. A harness may not declare a
`patch` (that is extracted from the workspace), nor `log_ref` /
`failure_snapshot` (those are the runtime's own observations).

**`.caesar/` is reserved, and invisible to the extracted patch.** It is
the control plane's work, not the phase's, so it must never read as a
change the agent made. Two independent mechanisms enforce that, either
sufficient alone:

1. A `/.caesar/` line written into `base.git/info/exclude` at stage time.
   Extraction indexes the workspace with `GIT_DIR` pointed at that
   trusted mirror, which lives *outside* the agent's bind mount — so this
   is the authoritative one: the agent cannot re-include itself. (The
   same line goes into `workspace/.git/info/exclude` for agent-side
   `git status` hygiene. That copy is the agent's own file and carries no
   security weight.)
2. Extraction drops `.caesar` from its throwaway index and passes
   `-- . :(exclude).caesar` to `diff-tree`, so both the tree hash (and
   therefore `changed`) and the patch bytes stay honest even if the
   exclude ever failed.

A repository that commits its own `.caesar/` directory is not supported:
staging shadows it and extraction hides it.

Lineage is bounded at stage time; a breach is a permanent
`staging_failed` naming the bound (the same oversize input would breach
it again on every retry):

| input | cap |
|---|---|
| input patch (decoded bytes) | 4 MiB |
| `instructions.md` | 256 KiB |
| each context file | 1 MiB |

**The cumulative-patch contract.** Extraction always diffs from the
run's base SHA, never from where the phase started, so every patch
artifact already contains its ancestors' changes and the newest one
subsumes the whole linear chain. Staging therefore applies **exactly one**
patch: applying an ancestor chain would apply everything they share a
second time. A node declaring two patch inputs is refused before any
container runs (permanent `staging_failed`); multi-patch composition is a
generations/merge shape that lands with Plan 08. The patch travels as
base64 (`payload["diff"]`) end to end — a patch is arbitrary bytes, and
round-tripping it through a JSON string would silently replace invalid
UTF-8 while leaving a diff that still applies.

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
