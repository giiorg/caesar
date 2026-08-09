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

The `forge` block is how caesar reaches its code hosts, and it carries
**two** endpoints per forge because they are genuinely different hosts:
`github_base_url` (the API — `https://api.github.com`) and
`github_clone_base_url` (where the repositories live —
`https://github.com`), and the same pair for GitLab. Setting only the API
base is the trap on a self-hosted install: every API call reaches the
private instance while every clone and push still goes to the public
site, which reads as "the run can see the repo but publishes nothing".
`publish_work_dir` is where publication checkouts are materialized — one
fresh subdirectory per publication, removed when it ends — and it
defaults to `/var/lib/caesar/publish`, which a non-root developer cannot
create, so `caesar.dev.json` points it at `/tmp/caesar-publish`.
Every base is validated at load: it must parse, use `https` (or `http`
for a lab), name a host, and carry no userinfo — a token belongs in a
header, never in a URL.

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
Gates add `GET /api/v1/approvals` — the inbox, scoped to the projects
the caller holds *any* grant on (an admin sees every one) — and `POST
/api/v1/runs/{id}/gates/{node}/decision`, which needs `approve` on the
run's project. Seeing a gate is not being allowed to decide it.

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

The production posture is honest, not simulated — and Plan 08 moved half
of it from stub to real. `serve` now wires the real forge adapter and
the real git publisher (`newForgeAdapter` and `gitPublisher` in
`internal/app/serve.go`), so a run pins its base SHA from the forge and
a definition carrying gate or `git-publish` nodes is accepted at intake;
`unavailableForge` and `ExecPlan.HasUnsupportedNodes` are deleted, and
`waiting` is reachable. What stays a stub is the executor:
`runs.NotImplementedExecutor` is still the default, so any phase claimed
with `runner.enabled` false dies `not_implemented` rather than reporting
a result it never produced. Stubs never fake success. The deterministic
fakes in `internal/runs/fake` are what drive the golden-set integration
test, which takes a run end-to-end in CI.

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
(`NotImplementedExecutor` + `AlwaysAdmit`, exactly Plan 05), so the
default deploy refuses to fake a phase result rather than pretending to
run one. Since Plan 08 wired the real forge, that flag is the only thing
left in the way — intake accepts gates and publishes, so flipping one
boolean gives the runner real work, on a host that already has the
Docker daemon, the built images and the writable data root the flag has
always needed — `buildRunner` failing any of those is a boot error, not
a degraded start. In prod, network isolation must not rest on Docker
alone: the host firewall must deny the agent subnets everything except
the gateway (baseline in the Plan 14 deployment runbook).

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

A repository that commits its own `.caesar/` directory is not supported,
and it degrades quietly rather than loudly. Exclude files do not apply to
already-tracked paths, so the base tree's `.caesar` entries survive
`read-tree` and are refreshed by `add -A`; the index drop then removes
them, leaving a written tree that differs from the base tree by exactly
those deletions, while the `diff-tree` pathspec suppresses them from the
patch. Every real change still reaches the patch, so the only phase this
misreports is one that changed nothing else: it reports `changed` with an
empty diff and publishes an empty patch artifact. The visible symptom
lands one phase later — its successor is refused by `lineageInto` for a
patch input carrying no diff, naming the wrong phase. Keep `.caesar/` out
of the repository.

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

## Subscription-first harness drivers (Plan 07)

Spec: `../docs/superpowers/specs/2026-08-05-caesar-subscription-auth-design.md`.
caesar runs the operator's own Claude Code and Codex subscriptions, so the
provider credential enters the agent container rather than staying behind the
gateway. That is a deliberate, user-ruled relaxation of the V1 "no reusable
secret in the agent container" property, scoped to *provider* credentials in
personal mode. Forge credentials are untouched: they still never leave the
stage container.

### The driver contract

`internal/runs/runner/driver.go` is the one place per-harness knowledge lives.
One row per harness kind carries custody derivation, env delivery, the CONNECT
allowlist, the broker profile, the exit→outcome table, and the
sensitive-field/identity extractors for its credential.

**Custody** is derived once from the (harness kind, credential kind) pair and
then carried, never re-guessed:

| harness | credential kind | custody | how the credential reaches the CLI |
|---|---|---|---|
| `claude-code` | `subscription_token` | materialized | `CLAUDE_CODE_OAUTH_TOKEN` in the container env, alongside `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` and `DISABLE_AUTOUPDATER=1` |
| `claude-code` | `model_key` | brokered | `ANTHROPIC_BASE_URL` points at the gateway, `ANTHROPIC_API_KEY` is the attempt-scoped broker token |
| `codex` | `agent_auth_file` | materialized | `auth.json` in a host-backed directory bound at `/caesar-auth`, plus `CODEX_HOME=/caesar-auth` — no secret in the env |
| `codex` | `model_key` | brokered | `OPENAI_BASE_URL` points at the gateway's `/v1`, `OPENAI_API_KEY` is the broker token |

A pairing outside this table is refused, and so is an unknown harness kind: a
default would be an open proxy onto somebody's provider account, and an
`auth.json` handed to Claude Code produces a container that cannot authenticate
at all. Both refusals are permanent harness errors raised before any container
exists. The materialized cell is also the only one that reaches the identity
pin, the sensitive-field extractor and the auth mount — a brokered binding
materializes nothing, and its redaction entry is the raw credential bytes.

Under materialized custody the gateway runs in the tokenless open-CONNECT
configuration Plan 06 already built: it holds no provider credential and mints
no broker token, so neither `CAESAR_BROKER_*` nor the CLI-native broker pair is
set, and the FQDN allowlist below is the sole barrier. That pairing is
load-bearing rather than incidental — the gateway rejects any CONNECT without a
valid `Proxy-Authorization` broker token *unless* it runs tokenless, and
unmodified CLI HTTP stacks speak plain `HTTPS_PROXY` CONNECT with no proxy
credentials. Plan 06's invariant survives: an open gateway holds nothing worth
stealing, and `NewGateway` refuses tokenless-with-a-credential. Brokered
attempts keep the token-authenticated gateway and its per-provider operation
allowlist (`/v1/messages`, `/v1/messages/count_tokens`, `/v1/models` for Claude;
`/v1/responses`, `/v1/chat/completions`, `/v1/models` for Codex).

**Per-harness CONNECT allowlists, verified 2026-08-05.** These are added to the
operator's configured `runner.connect_allow` **only under materialized custody**
— a brokered CLI is pointed at the gateway and has no need to reach a provider
host directly, and a raw CONNECT tunnel to one would be an unmetered,
upload-accepting route out for anything the agent finds in its workspace. A
brokered attempt therefore gets the operator's list alone.

| harness | added when materialized | why |
|---|---|---|
| `claude-code` | `api.anthropic.com`, `claude.ai`, `platform.claude.com` | the API, plus OAuth token exchange/refresh |
| `codex` | `chatgpt.com`, `auth.openai.com`, `api.openai.com` | the ChatGPT-auth backend (WebSocket upgrades land there too), token refresh, and apiKey-mode `auth.json` files |

`auth.openai.com` is load-bearing rather than incidental: without it the Codex
CLI cannot refresh mid-run and a long run dies opaquely. Telemetry intakes and
the updaters are deliberately absent and therefore default-denied. **These lists
drift with CLI releases**, and the drift signal is the `phase.egress_denied`
event — a CLI update that starts calling a new host shows up as denial events on
an otherwise healthy run, not as a silent behavior change. Read them before
concluding a harness is broken.

### The credential lease

`agent_auth_file` is a *mutable* credential: the Codex CLI rewrites `auth.json`
when it refreshes, and two runs refreshing independently would invalidate each
other, so the whole aggregate — ciphertext, `secret_version`, lease, and sync
receipts — lives in **assets**, where one transaction can commit all of it.
Acquisition is single-flight and happens after the phase claim and before any
container exists; losing it yields the zero-charge `credential_busy` outcome
rather than a failure. The lease carries a **takeover epoch** rather than just
an owner id, because after heartbeat expiry the original worker and its
reconciler are the same phase run and identity alone cannot fence one from the
other — reconciliation advances the epoch before it touches the container, so a
paused worker can never commit a stale snapshot, and it also cannot delete the
auth directory the reconciler is about to read. Sync-back is a compare-and-swap
against both the epoch and the `secret_version` the lease granted, committed
atomically with an idempotent receipt keyed by attempt, so a commit-then-crash
retry is recognizable as already-applied and an operator's mid-attempt rotation
always wins. The **lease row, looked up by attempt, is the recovery authority**
— reconciliation consults it even when no manifest and no container survive,
because a crash between lease commit and manifest durability must resolve to a
release rather than to a `credential_busy` loop against a lease nobody can
conclude.

Two properties are worth stating plainly because they bound what the protocol
promises. Read-back is **identity-pinned**: the agent and the CLI share the auth
mount under one uid, so caesar cannot tell a CLI refresh from agent-authored
content by shape alone, and a well-formed file whose account identity changed is
quarantined (`credential_quarantined`) with the stored credential preserved.
And **zero-loss rotation is explicitly not guaranteed** (spec §2.7): the CLI
rewrites `auth.json` in place, non-atomically, and a kill landing inside that
write can destroy the only copy of a live token — read-back correctly preserves
the stored credential, but that credential may already be dead. The outcome is
`auth_expired` and the operator re-auth loop, which this design already treats
as routine.

Importing an `auth.json` is an **exclusive custody transfer**. The lease
serializes caesar's runs; it cannot fence a writer caesar cannot see. Prefer a
dedicated `codex login --device-auth` on the caesar box so caesar's copy is the
only live copy. caesar refuses a second active `agent_auth_file` sharing a
refresh-token fingerprint, but an external live copy is undetectable by
construction — it voids single-flight, and the CLI treats refresh-token reuse as
terminal, which surfaces as `auth_expired`.

`subscription_token` needs none of this: it is static, shareable across parallel
runs, and takes no lease.

### Delayed retry, and why `attempt` can exceed `max_attempts`

Cap windows heal themselves, so runs wait; dead auth needs human hands, so runs
fail fast. `rate_limited` therefore requeues with a durable `not_before` and the
run resumes unattended, while `auth_expired` fails the run immediately with the
credential named in the typed outcome.

Two columns carry it. `phase_runs.not_before` is compared against the
**database** clock inside the claim CTE, exactly like the wall deadline; and
`phase_runs.chargeable_failures` carries the node's spent failure budget forward
across retries. The claim predicate is `chargeable_failures < max_attempts`, not
`attempt <= max_attempts` — waiting outcomes (`rate_limited`, `credential_busy`)
increment nothing.

**The documentation consequence, for readers of the events stream and the API:
`attempt` may legitimately exceed `max_attempts`.** It stays an
always-increasing execution ordinal and the `(run, node, attempt)` identity is
untouched; `max_attempts` bounds *charged failures*, not executions. A run that
crosses several cap windows will show attempt 7 of 3 and be perfectly healthy.

Scheduling is pinned in two regimes (`internal/runs/retry.go`). A
**provider-stated** reset is truth: taken verbatim, floor-clamped, never
jittered and never capped — both of those can only shorten the wait, and waking
early retries into a window we were told is shut. A **fallback** delay is our
own guess and is bounded: exponential on the wait count, ±10% jitter so a shared
cap window does not wake every parked attempt at once, capped.

| outcome | floor | base | cap |
|---|---|---|---|
| `rate_limited` | 2m | 15m | 1h |
| `credential_busy` | 30s | 30s | 10m |

The real ceiling is the run's wall deadline: eligibility that would land past it
terminalizes the run immediately instead of parking a row that can never legally
run.

### Redaction, and the withhold

Placing the credential outside the workspace prevents *accidental* capture by
the diff/extract pipeline and failure snapshots. It is not a structural
guarantee — the agent can read its own credential and could copy it into the
workspace on purpose — and that residual is explicitly accepted. What is
mechanically defended is the accidental half, with two precision rules:

- **What is scanned for** is a pinned per-kind sensitive-field list, not every
  string leaf: the Claude token value, and for `auth.json` its id/access/refresh
  tokens and API key — initial *and* refreshed, since a token the CLI rotates
  mid-run is unknown to caesar until read-back. The account id is deliberately
  excluded: it identifies, it does not authenticate, and redacting it would
  blank the field an operator reads to tell two subscriptions apart. A 16-byte
  minimum-length floor backstops the list, so `auth_mode: "chatgpt"` and
  timestamps cannot blank legitimate patch bytes. The floor governs *extracted*
  fields only — the credential itself and the broker token are secret by
  definition and enter at any length, with a `runner.short_credential` log line
  when one is short enough that it will redact incidental occurrences of itself.
- **Where** is the canonical decoded bytes. Plan 06 stores patch bytes base64
  in artifact payloads, so scanning a serialized payload would never find a raw
  token; the scan runs on the decoded patch before artifact insertion. A hit in
  a patch or a declared artifact fails closed — `secret_in_artifact`, artifact
  not persisted.

Logs stream to a **quarantined** directory for the life of the attempt and are
rescanned after guarded read-back and reconciliation; only the redacted copy is
atomically published as the durable log artifact, and the quarantine is removed
last because it is the recovery copy until publication succeeds. Matches are
replaced with `[caesar:redacted]`.

**The withhold.** If guarded read-back cannot recover the sensitive-field set —
the §2.7 malformed-write window, or a missing manifest — the refreshed values
are unknowable and no complete scan exists. That attempt's quarantined logs and
workspace-derived artifacts are then **permanently withheld** rather than
published unscanned. Withholding suppresses an attempt's logs and artifacts,
never its typed outcome, and the workspace decides which outcome that is: an
attempt that produced a diff fails permanently as `invalid_output`, because the
alternative is a "success" whose patch and logs silently never appear; an
attempt that changed nothing concludes normally with no artifacts. **So a
withheld attempt does not always look like a failure** — a run that finished
cleanly but has no logs is this case, not a missing-logs bug. Either way it
publishes nothing that might carry a token caesar cannot name.

### The images

`task image:harness` builds `caesar-harness-claude:dev` and
`caesar-harness-codex:dev` from `docker/harness/`. It is **operator-only and
never runs in CI** — it pulls the real CLIs from npm, and no CI lane may place a
live subscription call. CI's agent is `caesar-fakecli:dev`, a scripted stand-in
built by `task image:build` that speaks the same filesystem contract. Each
Dockerfile pins its CLI version in an `ARG`, because the exit markers in
`driver.go` are recorded against a specific version and a bump can invalidate
them; bumping one is an operator action followed by the smoke run. The
entrypoints read the **CLI-native** environment only and never translate
`CAESAR_BROKER_*` — see `docker/harness/README.md` for why.

### Credential freshness signals

There is no proactive token-probing cron: the CLIs are the source of truth for
whether auth works, and a probe would spend cap window to ask. Instead the
credentials view carries passive freshness — `provided_at` (so a ~1-year
`subscription_token`'s age is visible before it dies mid-run), `last_used_at`,
`last_auth_expired_at`, and the monotonic `secret_version` that a Codex refresh
advances. The second signal is the runs themselves: any run failing
`auth_expired` names the credential in its typed outcome, visible in run detail
and the event stream.

Secrets are size-bounded per kind at the door: 4 KiB for single tokens, 64 KiB
for `agent_auth_file`, which must also parse as JSON and yield a refresh-token
fingerprint — a file that cannot is unusable, and refusing it at creation beats
discovering it at materialization time.

### Spike: the Codex CLI's own command sandbox (spec §2.5) — REJECTED

Spec §2.5 accepts one residual under materialized custody: agent-executed code
can read the credential the CLI legitimately holds. It asks whether the Codex
CLI's native sandbox could deny model-executed commands access to `/caesar-auth`
inside our container. Probed 2026-08-06 against `caesar-harness-codex:dev`
(Codex CLI 0.146.1) on Docker 29.6.2, kernel 6.12.76-linuxkit. **Verdict:
reject — the §2.5 residual stands as user-accepted.** Not because the CLI cannot
express the denial, but because the mechanism that enforces it cannot start
under the runner's container posture.

What the probes showed:

1. The confinement is **expressible**. Named permission profiles are configured
   as a path-keyed map, and a `"none"` (or `"deny"`) entry on the auth mount
   works: with `"/" = "read"`, `"/workspace" = "write"`,
   `"/caesar-auth" = "none"`, `codex sandbox -P <profile> -- cat
   /caesar-auth/auth.json` returned `Permission denied`. Writes outside the
   granted roots were refused too.
2. The enforcement backend is **vendored bubblewrap, not Landlock** — the binary
   ships `codex-resources/bwrap` and every failure came out of it. Under the
   runner's exact posture (`--cap-drop ALL`, `no-new-privileges=true`,
   read-only rootfs, tmpfs `/tmp`, non-root) it dies immediately: `bwrap: No
   permissions to create a new namespace`. The kernel is not the constraint
   (`/proc/sys/user/max_user_namespaces` = 31738); Docker's **default seccomp
   profile** is. Re-running the identical container with
   `--security-opt seccomp=unconfined` as the only change made bwrap work — that
   isolates the cause exactly. `--cap-add SYS_ADMIN` is not an alternative:
   bwrap then gets past the namespace and dies at `pivot_root`.
3. The **legacy Landlock backend is not a way around it.** Codex carries a
   `use_legacy_landlock` feature flag, and Landlock would need no user namespace
   and would run under our posture — but every
   `codex --enable use_legacy_landlock sandbox -P <profile>` invocation was
   refused with *"permission profiles requiring direct runtime enforcement are
   incompatible with --use-legacy-landlock"*, with and without a deny entry. In
   0.146.1 the profile path is bwrap-only.

So adopting this would cost the agent container its default seccomp profile —
the broadest syscall-level defense we have, on the one container that runs
hostile code — to buy a defense against a credential the same process tree holds
anyway. That is a worse trade than the residual, so it is rejected.

Two caveats, recorded so a re-run is cheap. The blocker is Docker's seccomp
policy, **not** a macOS/Docker-Desktop artefact, so the Linux deploy target is
expected to behave identically; worth one confirming run there anyway. And the
spike probed the sandbox through `codex sandbox`, the CLI's own manual entry
point for the same machinery — that `codex exec` wraps model-executed commands
through this identical backend is inferred from the shared implementation, not
observed, since observing it needs a real subscription (i.e. the smoke run
below). If the residual ever needs closing, the concrete follow-up is a custom
seccomp profile for the agent container only — Docker's default plus the
`clone`/`unshare` `CLONE_NEWUSER` allowance — which preserves everything else.
That is a line item, not adopted.

To reproduce, in the runner's posture (drop `seccomp=unconfined` to see the
failure; keep it to see the denial that the failure hides):

    docker run --rm --read-only --tmpfs /tmp:rw,size=268435456 \
      --cap-drop ALL --security-opt no-new-privileges=true --network none \
      --security-opt seccomp=unconfined \
      -u "$(id -u):$(id -g)" -v "$PWD/auth:/caesar-auth" -v "$PWD/ws:/workspace" \
      -e CODEX_HOME=/tmp/ch -e HOME=/tmp \
      --entrypoint sh caesar-harness-codex:dev -c '
        mkdir -p /tmp/ch
        printf "[permissions.p.filesystem]\n\"/\" = \"read\"\n\"/workspace\" = \"write\"\n\"/caesar-auth\" = \"none\"\n" > /tmp/ch/config.toml
        codex sandbox -P p -C /workspace -- /usr/bin/cat /caesar-auth/auth.json'

### Manual smoke checklist (operator-run, real subscriptions)

**This is the plan's acceptance evidence, not an appendix.** CI proves the
mechanics against fakes and can never place a live subscription call, so a real
token is the only place real CLI behavior is ever observed. Until this runs,
roadmap row 07 stays annotated *implementation complete — smoke pending,
provisional codex markers*.

Build the images first: `task image:harness`.

1. **Claude Code, materialized.** `claude setup-token` on any machine (the flow
   prints the token; the CLI saves it nowhere) → create a `subscription_token`
   credential → register a `claude-code` harness on `caesar-harness-claude:dev`
   → run a small real task → a patch lands. Already pre-verified under the
   runner's exact container posture: Claude Code v2.1.223 starts with `HOME=/tmp`
   and creates `~/.claude`, `~/.claude.json` and `~/claude-501` on the tmpfs.
   What only this run can prove is the credentialed half — that the CLI gets
   past `Please run /login`, completes a turn, and that its `HOME` footprint over
   a full attempt stays inside the 256 MiB tmpfs. Watch for `ENOSPC` on `/tmp`
   as the signature of the latter failing; the tmpfs is RAM and counts against
   the 4 GiB container memory limit, so `HOME` is ~6% of the agent's budget.
   If a cap window is hit: observe `rate_limited`, the parked `not_before`, and
   the unattended resume.
2. **Codex, materialized.** `codex login --device-auth` **on the caesar box** —
   this makes caesar's copy the only live copy, which is the point — → paste the
   `auth.json` contents as an `agent_auth_file` credential → run a real task →
   verify the patch **and** that `secret_version` bumped if the CLI refreshed
   mid-run. Same start-and-write check as above: `$CODEX_HOME` is the writable
   bind, but anything else the CLI keeps under `$HOME` lands on the tmpfs.
3. **Auth death.** Revoke the Codex session from another device → run → the
   attempt fails fast as `auth_expired` naming the credential, and
   `last_auth_expired_at` appears in the credentials view.
4. **Record the real marker lines** observed for cap-hit and auth-death on both
   CLIs, then update the `driver.go` exit tables and the test fixtures to match.
   Commit that as its own change.

Item 4 is the one with teeth, because the tables are not equally trustworthy:

- **The `codex` rows are INVENTED** (`driver.go:205-239`). No recorded corpus
  for that CLI exists; Task 13's fakecli fixture speaks these exact markers,
  which proves the mapping engine and says nothing about the CLI. If the real
  CLI speaks differently, cap hits charge as `harness_error` instead of parking,
  and auth death stops failing fast.
- **The `claude-code` rows are recorded** — the cap rows against v2.1.222, and
  the auth row's first alternative verbatim against the pinned v2.1.223: started
  uncredentialed the CLI prints `Not logged in · Please run /login` (one line,
  `·` as U+00B7, no escape sequences, exit 1). The bare `Please run /login` and
  bare `Invalid API key` alternatives that row used to carry are **gone**: the
  tail these patterns run over is the agent's own stdout, so a phrase an agent
  types in passing would stamp a live credential dead via `MarkAuthExpired`.
  Every alternative now has to open a line AND complete with the slash command
  only the CLI can offer.

That narrowing is deliberately not the smoke run's to settle. A smoke run
measures **recall** — does the CLI print this? — and the auth rows' danger is
**precision**: a false match is permanent, kills a live run, and writes to the
operator's credential row, and no observation of a CLI can tell you what a model
will print into the same stdout. Item 4 may therefore *widen* these rows onto
recorded lines; it should not loosen them onto bare fragments. The same applies
to the epoch capture, which is guarded on the right (`epochGuard`) so a
millisecond timestamp fails the row and falls through to the plain cap rule
rather than parking a run past its wall deadline.

One consequence of the auth row worth knowing before anyone reads a smoke
transcript: because the CLI prints the marker whenever it has no usable token,
**a claude attempt whose token is absent, empty or rejected classifies as
`auth_expired`, not `harness_error`.** Defensible — there is genuinely no
working auth — but it means a mis-pasted token and a dead one produce the same
typed outcome, so check the credential before concluding the subscription
expired.

## Gates and publishing (Plan 08)

A run reaches a human gate and stops. The human approves, requests
changes, or rejects; an approval mints a single-use authorization that
exactly one `git-publish` node consumes to push a branch and open a pull
request on GitHub or GitLab, crash-safe at every step.

### The gate lifecycle

**A gate is a phase that is never claimed.** Gate rows are inserted
directly as `waiting_decision`, and the claim CTE predicates on
`pending` — that single branch is the whole reason a human's thinking
time never burns a lease or the execution budget, which accrues only
from `claimed_at` to evaluation. The wall clock does **not** stop: the
run's deadline is a column compared against the database clock inside
every deciding statement, so a gate nobody answers dies of the deadline
like anything else.

The run parks to `waiting` once nothing else on the graph is runnable.
That is read back from the database (`RunIsWaitingOnAGate`), never
inferred from what the pass just inserted, and `anyRunnablePhase` counts
`pending`, `running` and `reconciling` rows — so a gate in one branch of
a parallel graph leaves the run `running` while its sibling executes,
and the run parks when the sibling finishes. `syncWaiting` moves the run
in both directions so the two callers cannot drift.

`GET /api/v1/approvals` is the inbox — unbounded by design at V1, since
the waiting set is the size of a human's attention, not of the run
history. `POST /api/v1/runs/{id}/gates/{node}/decision` is the door: it
takes `{"decision": "approve" | "request_changes" | "reject", "note":
"…"}` and needs the `approve` grant on the project resolved **from the
run**. `request_changes` requires a note — it is the revision's only new
input. A decision is recorded once per gate *attempt* row
(`gate_decisions.phase_run_id UNIQUE`), so a rematerialized gate is
decidable again while the same row is never decided twice. Self-approval
is a policy-snapshot field (`allow_self_approval`, default true, snapshot
schema 2 with a v1 upgrade branch); a run whose snapshot forbids it
refuses the requester's own decision.

### What an approval authorizes

Approving mints one `approved_patches` row — the ApprovedPatch (spec
§2), scoped to the single publish node its gate dominates. It pins the
examined artifact id, the base SHA, the tree hash, **the repository as
approved**, the target branch, and the generation.

"The repository as approved" is three columns, not one: `repo_id` plus
`forge_kind` and `repo_slug`. `UpdateRepo` can change a repo's slug,
default branch and credential mid-run, so `repo_id` alone is not an
identity — an id that still matches proves nothing about *where* the
patch would land. Pinning the coordinates makes a rename a refusal
instead of a patch delivered to a repository nobody approved. The target
branch is the repo's default branch at approval time, and it is pinned
for the same reason.

Two indexes carry the single-use guarantee, and they do different jobs:

- `publish_intents.authorization_id uuid NOT NULL UNIQUE` — single use in
  its strongest form. A second consumption of the same authorization is a
  constraint violation, not a lost CAS; Postgres is the authority.
- `approved_patches_live_key ON approved_patches (run_id, publish_node_id)
  WHERE status = 'valid'` — at most one *live* authorization per publish
  node per run, which is what makes "invalidate the old one, mint the new
  one" impossible to get half-done.

Every pinned field is revalidated at consumption, before any forge call.
A repo id, forge kind, slug, default branch, base SHA or authorizing gate
that no longer agrees fails permanently as `authorization_stale`. A newer
generation invalidates a live authorization outright.

### Revisions

`request_changes` opens a bounded revision generation. One transaction
bumps the run's generation counter, invalidates every live authorization
on the run, supersedes every phase row in the target→gate slice —
`succeeded` rows included, because readiness is each node's newest
attempt row, so a slice node whose previous-generation row succeeded
would otherwise stay readable as done and the gate would re-decide
against the previous generation's verdict — seeds the target with the
note as a `gate_note` artifact plus a carry-forward copy of the examined
artifact (payload copied, original as parent, history append-only), and
rematerializes the target under the new generation.

`max_rounds` is per **gate**, but the generation counter and the
authorization invalidation are per **run**: a revision at one gate
advances the whole run's generation and invalidates every live
authorization on it.

Two refusals answer `request_changes` with 409, and both leave approve
and reject available:

- **Rounds spent.** Exhausting `max_rounds` refuses the decision rather
  than killing the run — ending a run on the Nth click would destroy
  work, and a waiting gate is bounded by the wall deadline anyway. It is
  a runtime conflict, not a validator rule.
- **Already publishing.** A revision may not rewind a run that has begun
  *any* publication — including one still `pending`, whose intent row
  commits before the first forge call and whose authorization is already
  consumed. Nothing in a transaction can un-push a commit, so refusing is
  the honest answer.

### The publish state machine

`pending → pushed → published`, with `halted` as the terminal
manual-reconciliation state. There is **one intent per `(run, publish
node)`**, resumed across every later attempt and never re-minted — a
second intent would mean a second branch — and the intent's `attempt`
column is provenance, not identity.

Each effect's receipt commits before the next call, so a retry always
distinguishes "never happened" from "succeeded, receipt lost":
`RecordPushReceipt` is `pending → pushed`, `RecordPRReceipt` is
`pushed → published`, and `published` is deliberately **not** haltable —
a publication with both receipts is done, and no late arrival may
overwrite it with a call for human help. Status only moves forward, row
counts are checked, and a driver that loses the CAS reloads and adopts
the winner's row rather than failing the run.

While an intent is `pending` or `pushed`, every error leaving the publish
path is transient and **uncharged**. That is an invariant, not a
tendency: no sweep revisits a failed run, so one charged failure here
would make an orphaned branch permanent.

The marker is deterministic — `caesar/<approved-artifact-id>`, from a
uuidv7 unique per node per generation, so recovery reconstructs it from
the authorization row alone and a new generation necessarily gets a new
branch. Human readability lives in the PR title, not the ref.

**Ownership is proved by rebuilding the commit, never by observing a
sha.** Recovery reconstructs the commit it would push and compares shas,
which works only because both dates are pinned (below). An observed
remote head is evidence, never a lease value: passing one to
`--force-with-lease` would convert "somebody else's branch" into "a
branch I may replace", so creating a marker uses an atomic *create-only*
lease (`--force-with-lease=refs/heads/<branch>:`, empty expected value)
— a plain push fast-forwards a branch that appeared between the probe
and the push and reports success.

A pull request on the marker is adopted only on an exact three-way
match: it must carry both a base and a head, its base must equal the
branch the approval named, and its head must equal the sha we recorded.
A candidate missing either field, two candidates matching, or a
candidate that can be neither confirmed nor ruled out all halt. Absent
evidence is never permission, and a head branch alone is not a unique
description of a publication.

Five outcome codes classify a forge answer, and three of them **halt**:

| code | class | why |
|---|---|---|
| `push_rejected` | transient | the expected-remote-SHA precondition failed — the remote moved, or the network blinked |
| `forge_unavailable` | transient | the API could not be reached, or refused transiently (a 403 that carries rate-limit signals is throttling, not "forbidden") |
| `publish_conflict` | permanent, **halts** | a marker exists in a state we cannot prove is ours |
| `auth_expired` | permanent, **halts** | the forge rejected the repository's credential; somebody must issue a new token |
| `repo_unavailable` | permanent, **halts** | the forge will not show the repository at all — deleted, renamed, or no longer visible to this token |

`haltsPublication` in `internal/runs/publish.go` is the single authority
on that set. The three halt for one reason: each leaves something of ours
half-done on the forge that no retry can finish, so the intent becomes
`halted` and a human reconciles. Everything else is an ordinary transient,
or a permanent the run is entitled to die on because nothing of ours is
left behind it. Recovery never guesses.

**Where a halting answer arrives bounds which ones you will ever see.**
A publication's *first* forge call is the recovery probe: `pushPublication`
asks `BranchHead` before it pushes anything, and routes the answer through
`maybeHalt`, so a credential the forge rejects halts there — one call
before the push it guards, with a `halt_reason` for whoever must reissue
the token. But the platform clients deliberately fold a 404 on that probe
into "no branch" rather than an error, because absence is recovery's
*answer* and not its failure. A repository that has vanished is therefore
not distinguished at the probe: the publication proceeds to the clone,
which cannot tell a deleted repository from a network fault and types
itself transient `forge_unavailable`. `repo_unavailable` is reached on
`OpenPullRequest` and `FindPullRequests` instead — once an intent is
already `pushed`, which is the window where halting matters most and the
one hardest to stage deliberately.

### Attribution

Attribution is verbatim and pinned. **Author = the requester** —
`GIT_AUTHOR_NAME` is the email's local part, `GIT_AUTHOR_EMAIL` the
address. **Committer = `caesar <caesar@invalid>`.** Git's own
author/committer split says "this human's intent, executed by this
machine" natively.

`GIT_AUTHOR_DATE` and `GIT_COMMITTER_DATE` are one pinned RFC3339 stamp
— the intent's `created_at`, never the wall clock. That is a correctness
requirement, not tidiness: a commit's sha hashes its identities
*including their timestamps*, and rebuilding that sha is how recovery
proves a receiptless branch is ours. An unpinned date makes every
rebuild a different commit, the adopt comparison misses, and a
crash-after-push force-pushes a duplicate over what is already on the
remote.

The commit message carries four trailers, in this order:

    caesar-run: <run id>
    caesar-node: <publish node id>
    caesar-artifact: <approved artifact id>
    caesar-approved-by: <approver's email address>

`caesar-approved-by` is the **approver**; the author is the requester,
and the message is where the approver is recorded. These are provenance,
not attribution. **No `Co-authored-by` machine trailer is emitted**,
deliberately — `gitops/publish_test.go` asserts none appears. The attempt
ordinal is deliberately absent: it differs between attempts of one
publication, and every input here must not.

Both `author_email` and `commit_message` are **snapshotted on the intent
and never re-rendered**. Both are hashed into the commit sha and both
interpolate a mutable `users` row, so a re-render between two attempts
would rebuild a different commit, miss the adopt comparison, and turn a
crash-after-push into a permanent `publish_conflict` on a branch that is
genuinely ours.

### Operating a halted publication

**caesar will never resolve this itself, and that is deliberate.** The
run is already `failed` with the halting code (`publish_conflict`,
`auth_expired` or `repo_unavailable`); `HaltPublishIntent` is terminal by
design, and no later attempt reopens a halted intent — anything else
would mean a machine guessing about a ref it could not prove was ours.

1. Read `halt_reason` on the run's publication projection (`GET
   /api/v1/runs/{id}`), and the `publish.halted` event in the run's
   event stream. Between them they name the branch, the target branch,
   and why the publication stopped.
2. Inspect that branch on the forge, and any pull request opened from it.
3. Choose one of two answers:
   - **Re-run the task.** A new run mints a new patch artifact and
     therefore a new `caesar/<artifact-id>` marker, so the abandoned ref
     never blocks it (`publish_intents_marker_key` is per `(repo,
     branch)`). Deleting the abandoned ref is hygiene, not a
     precondition.
   - **Accept what is already on the forge** and close the task by hand.

There is **no endpoint that marks a halted publication resolved**. That
is not an oversight: it belongs with Plan 09's operator surface, and
until it exists the halt is reconciled outside caesar.

### Configuring the forge

The `forge` block is documented under [Config](#config) above — both
endpoints per forge, the self-hosted trap, `publish_work_dir` and its
default, and the base-URL validation rules. Two operational notes belong
here rather than there: `publish_work_dir` must be **writable by the
caesar process**, and `serve` creates it at boot rather than lazily,
because discovering an unwritable work dir at the first publication is
discovering it at the worst possible moment.

### Manual forge smoke checklist (operator-run, real GitHub/GitLab)

**This is row 08's acceptance evidence, not an appendix.** Every forge
assertion in this plan is against `httptest` servers and local bare
repositories, and no CI lane may make a live forge call — so the GitHub
and GitLab response shapes and error mappings are *invented* until this
runs. Until it does, roadmap row 08 stays annotated *implementation
complete — live-forge smoke pending*.

Use a throwaway repository on each forge; a publication pushes a branch
and opens a real pull request.

1. **Setup, per forge.** Create a throwaway repo with one commit on its
   default branch. Mint a real token for it — caesar uses that one token
   for everything the port does (read the default branch's head, read a
   branch head, create a pull request / merge request, list the requests
   on a head branch) **and** for the HTTPS clone and push, so it needs
   both repository write and API access. Store it as a `forge_token`
   credential (the only kind a repo accepts), register the repo on a
   project with that credential, and register a workflow version whose
   graph ends in a gate followed by the `git-publish` node it dominates.
2. **Run to the gate.** Create a task and let it reach the gate. Intake
   is the first live forge call — it resolves the default branch's head
   to pin `base_sha` — so a token problem shows up here rather than at
   publish time. Then `GET /api/v1/approvals` must list the gate, and
   the run status must be `waiting` (the graph is linear, so nothing
   else is runnable).
3. **One revision round.** `request_changes` with a note, confirm the
   response reports the **new** generation, and confirm the target node
   rematerializes and re-runs.
4. **Approve.** Confirm the response reports the decided generation and
   that the run resumes.
5. **Verify the publication on the forge**, which is the part only this
   run can prove:
   - a branch named `caesar/<approved-artifact-id>` exists, and its head
     is the `head_sha` the run reports;
   - the commit's **author** is the requester's address and its
     **committer** is `caesar <caesar@invalid>`;
   - the message carries all four `caesar-*` trailers and **no**
     `Co-authored-by` line;
   - a pull request exists whose **base is the repo's default branch**
     (not merely "some open PR on that head") and whose number and URL
     match `pr_number` / `pr_url` on the run's publication projection.
6. **The two halting answers, at the publish step.** `publishError`
   (`internal/app/runsports.go`) turns `forge.ErrUnauthorized` into
   `auth_expired` and `forge.ErrNotFound` into `repo_unavailable`; which
   HTTP answer each forge actually gives is the invented half, and this
   is the risk row 08 is open for.

   **The ordering is what makes the step work, so follow it exactly.**
   Both conditions are also visible at *intake*, which resolves the head
   before it writes anything — and intake runs a **different** mapping,
   `intakeError` in the same file, which answers 404 / 400 / 429 as
   problem+json rather than producing an outcome code. Break either
   condition before creating the task and the task-creation call fails,
   no run exists, and the publish-step mapping is never reached. So
   break it *after* the gate instead:

   - **Credential dead.** Take a fresh task to the gate with a
     **working** token (step 2, stopping short of the approve in step 4),
     then revoke or expire the token on the forge, then approve. The publication is what meets the
     dead credential: the decision and the intent's creation are both
     database-only transactions, and the intent pins `credential_id`, so
     the first thing to present the token afterwards is the push itself,
     presenting the revoked one.
   - **Repository gone.** Same ordering, another fresh task — reach the
     gate, then rename or delete the repo without updating its caesar
     registration, then approve. The intent pins `repo_slug`, so the publication addresses
     the name the approval used.

   For each, record where it fails — task creation, or the run — and with
   which code. Neither may retry to the wall deadline, and a failure
   reached after an intent exists must leave `halt_reason` on the run's
   publication projection and a `publish.halted` event.

   Optionally record the **intake** behaviour of the same two conditions
   as a separate case (break the condition first, then create the task).
   It is worth writing down, but it does not stand in for the above:
   `intakeError` and `publishError` are two different tables, and only
   the second one leaves a branch on the forge when it is wrong.
7. **Record what the forges actually said.** If either forge's status
   codes or error bodies map differently than `runsports.go` assumes,
   fix the mapping and its `httptest` fixtures and commit that as its own
   change — then row 08 closes.

Step 6 is the one with teeth. A miss there is not cosmetic: a
`repo_unavailable` that presents as `forge_unavailable` retries a
vanished repository until the wall deadline with a pushed branch sitting
orphaned behind it, and an `auth_expired` that presents as transient does
the same.

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
