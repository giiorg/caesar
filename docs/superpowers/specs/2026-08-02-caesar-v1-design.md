# caesar V1 — design

**Date:** 2026-08-02 · **Status:** all 8 sections approved by the user in the brainstorming sessions of 2026-08-01/02; self-review performed 2026-08-02 (inline fixes applied); codex artifact-review rounds in progress, user review gate next. · **Companion records:** `docs/brainstorm/2026-08-01-decisions.md` (locked decisions, §3/§8 revised 2026-08-02), `docs/research/2026-08-01-*.md` + `2026-08-02-redlight-*.md` (reference corpus).

caesar is a **deterministic orchestrator of agentic workflows**: workflow definitions are validated *data* interpreted by Go; agents only propose work products; Go owns all control flow and performs every side effect. V1 identity: **engine + agentic library** — the first shipped step vocabulary is repo-centric agentic development (plan / implement / review / gate / git-publish). Month-one success: create a task in the UI, pick a workflow, point at a repo, watch phases live, approve gates from a phone, caesar pushes the branch and opens the PR.

---

## 1. System overview

One Go binary — a **modular monolith** control plane: HTTP API, scheduler, orchestrator, SSE streaming, assistant backend, and the embedded React UI. **PostgreSQL is the sole source of truth and the sole dispatch authority.** Agent runs execute in disposable containers via a **runner port** (Docker adapter primary, Kubernetes Jobs adapter secondary). Forges (GitHub + GitLab in V1) and harnesses (Claude Code + Codex CLI in V1) sit behind their own ports. Auth is a port (local accounts in V1, OIDC addable).

The runnable system is exactly three things: **PostgreSQL, the caesar binary, and a container runtime.** Everything else (cloudflared for remote access, Tetragon for runtime observation) is optional garnish.

Internal layout (locked engineering standards): bounded contexts at the top of `internal/` as peers of `platform/` (shared kernel) and `app/` (composition roots); public entry in `cmd/`; hexagonal boundaries enforced mechanically (architecture tests + custom analyzers). Monorepo: `/backend`, `/web`, `/deploy`, `/docs`.

**Evolvability rules (approved).** The monolith stays splittable without redesign by holding two lines, enforced mechanically: (1) **transactions never span bounded contexts**; (2) **events are observation, never dispatch** — the Postgres queue is the only work-dispatch authority, and a future NATS attaches only as an outbox relay for external consumers. Bounded contexts are the extraction units; the claim protocol is already multi-process-safe; contracts are already generated.

Explicitly deferred from V1: forge-watch intake (webhooks as triggers), React Native app, multi-org (dropped entirely — single-org, no Organization entity), Forgejo forge adapter (post-V1 candidate; the team's own repos live on `git.swap.io`), helm chart, PAT/service tokens, TOTP/passkeys, mutation testing.

## 2. Domain model

**Principles.** Go types are the single source of truth — schema, API DTOs, and definition documents derive from them (drift is the #1 designed-out bug class). Single-org: no Organization entity. Every state mutation appends its events in the same transaction. **Execution identity is the phase attempt** — `(run_id, phase, attempt)` scopes container names, secret names, artifact keys, event payloads, and branch refs.

**Identity & access** — **User** (email, argon2id hash, active flag; admin-invite only) · **Session** (server-side row: token hash, expiry, revocation) · **Role/Grant** (global role + per-project grants; ladder in §6).

**Assets** — **Project** (groups repos, policies, grants) · **Repo** (forge kind + slug/URL + default branch + credential ref) · **Credential** (encrypted at rest, referenced by id, never embedded in definitions; every use audited) · **Harness** (admin-registered: kind `claude-code`/`codex`, image ref, provider endpoint, a Credential reference for the model API key, defaults).

**Definitions** — **WorkflowDefinition**: a typed, schema-validated document — a DAG with real edges, cycle detection, gates as first-class nodes. Immutable versions, content-hashed; the persisting API is the only write door. UI outline, future canvas/code views, and the assistant are all projections of this document.

**Execution spine** — **Task**: the unit of user intent (repo + workflow version + inputs + requester principal); status derived from its runs (1:N — re-runs and supersedes). **Run**: one execution of a pinned definition hash with a pinned **PolicySnapshot**; carries two clocks (execution budget pauses during human waits; wall clock never); terminal states include `superseded` (supersede-before-work) and `dead`. **PhaseRun**: one attempt of one DAG node — **the queue is this table**: claim via `FOR UPDATE SKIP LOCKED`, CAS transitions, durable lease (`worker_id`, `leased_until`, heartbeats), attempt limits enforced in the claim `WHERE`; `reconciling` is a first-class state; `running → pending` is structurally impossible; outcome errors are typed, unclassified → permanent. **Gate**: a phase whose completion is a decision — routed by policy metadata, records decision + note (the note feeds the next phase), `acted_by`/`acted_at`; for code, the gate holds the patch artifact until approval (never publish-then-gate).

**Work products & safety** — **Artifact**: attempt-scoped durable outputs — `patch` (diff + base SHA + tree hash), `plan`, `review_verdict`, `log_ref`, `failure_snapshot` · **Policy**: versioned baseline per repo/category + per-run tighten-only override + durable kill-switch rows; evaluated into the immutable snapshot the run pins · **AuditEntry**: append-only who-did-what (auth, approvals, config changes, credential use, run lifecycle).

**Observation** — **RunEvent**: append-only, written in the same tx as the state change; SSE serves snapshot-then-deltas with `Last-Event-ID` resume from it · **Outbox**: same-tx envelope table from day one; nothing consumes it in V1 (a future NATS relay attaches with no semantic change).

**Deliberately absent:** Organization; a separate jobs/queue table (PhaseRun *is* the queue); NATS subjects; warm agent sessions; webhook-intake entities (WebhookSubscription / durable InboundDelivery / trigger rules — deferred with forge-watch; invariant #12 already dictates their future behavior).

## 3. Execution & isolation

**Claiming.** The orchestrator materializes ready PhaseRuns from the pinned DAG; an in-process worker pool claims them with a single `UPDATE … WHERE status='pending' AND attempt < max_attempts … FOR UPDATE SKIP LOCKED`, taking a durable lease it heartbeats. Lease expiry never auto-retries — it enters `reconciling`.

**RunSpec.** From the claim, the orchestrator builds an immutable RunSpec: digest-pinned harness image, the phase's resolved config, a **constructed env allowlist** (never inherited — `os.Environ()` passthrough is banned by analyzer), workspace spec, egress allowlist (harness provider endpoints + nothing else), resource limits, and a deadline derived from the run's remaining execution budget. Identity everywhere: `caesar-<run>-<phase>-<attempt>`.

**Docker adapter (primary — prod and dev).** Per attempt: a fresh **internal** Docker network (no route anywhere) + a fresh workspace volume + the agent container, hardened: non-root, read-only root FS (writable workspace + tmpfs `/tmp`), all caps dropped, `no-new-privileges`, default seccomp, pids/memory/CPU limits. Egress happens only via a per-run **egress proxy** on that internal network — **the caesar binary itself in a subcommand mode (`caesar egress-proxy`)**: a tiny stdlib CONNECT proxy enforcing the hostname allowlist, denying and *logging* everything else as run events. Because the agent has no direct route out, this is default-deny even if a tool ignores `HTTPS_PROXY`; filtering by CONNECT hostname avoids DNS-pinning gymnastics. Zero new dependencies, full observability.

**Isolation properties.** (1) **Forge credentials never enter agent containers**: the control plane clones the repo at a pinned SHA into the workspace *before* the container starts and extracts the diff *after* it exits; publish happens later from a fresh trusted checkout. The only secret an agent container ever holds is its per-attempt model API credential. (2) **No route to caesar or Postgres** — structurally, via the internal network.

**K8s adapter (secondary).** Same RunSpec rendered as a hardened Job (agentd doc-08 spec: `backoffLimit: 0`, `activeDeadlineSeconds`, TTL, non-root, read-only root FS, drop ALL caps, `automountServiceAccountToken: false`); egress via Cilium `toFQDNs` (patterns harvested from redlight — exact names + wildcard companions) or the same egress-proxy sidecar for identical cross-adapter behavior.

**Harness port.** One adapter per harness (Claude Code, Codex CLI), one-shot per phase: a filesystem contract — prompt/context mounted in, declared outputs written to `workspace/.caesar/out/` — plus output parsing into typed results and exit-condition mapping into typed outcomes (transient / permanent / policy-violation; unclassified → permanent). No warm/persistent agent processes.

**Collection & completion.** The runner streams container output into `run_events` (and a log artifact); on exit the orchestrator validates work products: the patch must apply cleanly against the base SHA, and where policy requires it the repo's own build/lint/test runs in a **disposable validation container** with the same hardening as agent runs — repo code is untrusted and never executes in the control-plane process — its egress allowlist extended only by policy-listed toolchain endpoints (package registries). It then persists Artifacts and CAS-transitions the PhaseRun.

**Reconciling.** On lease expiry or restart: look up the attempt-named container/Job in the runtime. Completed with intact outputs → adopt idempotently; anything else → kill-then-inspect; a new attempt starts only after reconciliation concludes.

**Budgets.** Two clocks enforced from the orchestrator's own observations (runtime state + parsed provider usage), never agent self-reports: execution budget pauses at gates; wall clock never pauses; breach → kill + typed outcome. Abnormal ends produce a redacted failure-snapshot artifact (logs tail, container inspect, workspace listing — secrets scrubbed) with retention.

## 4. API & realtime

**Shape.** One versioned REST surface — `/api/v1/…` — on the stdlib `net/http` ServeMux (method + wildcard patterns). Resources mirror the domain: `tasks`, `runs`, `approvals`, `workflows` + immutable `versions`, `projects`, `repos`, `credentials`, `policies`, `users`/`sessions`, admin config. No GraphQL, no gRPC. The binary also serves the embedded UI and the realtime endpoints (SSE streams + assistant WebSocket), all same-origin.

**Contract discipline.** Go types → generated OpenAPI 3.1 document → generated TypeScript client. CI carries regenerate-and-diff gates on both artifacts. Definition validation lives on the write path only: the endpoint persisting a WorkflowDefinition version is the single door (schema + DAG edge/cycle checks).

**Errors.** Two-axis internally (class × retry effect); at the API boundary a single stable envelope in RFC 9457 `application/problem+json` form: `type`, `title`, `status`, `detail`, `correlation_id`, typed `errors[]` for field validation. Unclassified internal errors surface as permanent.

**AuthN/Z middleware.** Session cookie → server-side session row → user + grants resolved once per request; the **requester principal rides the request context into every domain write**. CSRF: same-origin app + `SameSite=Lax` + Origin/custom-header check on unsafe methods. Deciding mutations are CAS end-to-end (approve = `… WHERE status='waiting_decision'`; concurrent second decision → `409` with winning state). V1 exposes cookie sessions only — PATs/service tokens deferred.

**SSE.** Two stream kinds, both snapshot-then-deltas over `run_events`: per-run (`GET /api/v1/runs/{id}/events`) and a global attention stream (`GET /api/v1/stream`) for badges/cards/lists. On connect: one `snapshot` event, then deltas; `Last-Event-ID` resumes the tail, falling back to re-snapshot when too old. Server side: a single tailer goroutine per process follows `run_events` (woken by `LISTEN/NOTIFY`, slow-poll fallback) and fans out in-memory — no per-connection DB polling. Handlers flush headers immediately and heartbeat every ~20s (tunnel obligations). Process-local fan-out is a stated monolith assumption.

**Assistant socket.** Its own WebSocket at `/api/v1/assistant/ws` speaking a versioned, discriminated-union frame protocol (`user_msg`, `assistant_delta`, `ui_block`, `tool_proposal`, `tool_confirm`, `tool_result`, `error`, heartbeats). Hard rules: the assistant *proposes* mutations as tool-call cards and executes only after an explicit `tool_confirm` frame; every assistant-driven mutation goes through the same domain services, permission checks, and audit as REST, with the human as requester principal. Assistant LLM usage is metered and audited per user.

**Correlation.** Every request gets/propagates a correlation id, echoed in responses and stamped — with `workflow_id`/`run_id`/`phase`/`attempt` where applicable — into every log line and span.

## 5. UI architecture

*(The §7 UI decisions in the decisions record — page map Home/Tasks/Runs/Approvals/Workflows/Repos/Admin, shell, run timeline, workflow outline editor, approval screen, assistant surface, terminal aesthetic — are locked with mockups; this section is the architecture that delivers them.)*

**Stack.** React + Vite + TypeScript strict; Tailwind v4 CSS-first; shadcn-style primitives over Base UI; lucide icons; TanStack Router **file-based** + TanStack Query; Zustand only for genuinely client-side stores; Biome (Ultracite).

**Constitution as law from day one:** route-centric colocation, no barrel files, named exports, one component per file, no cross-group imports, consumer-count file placement — enforced by lint + CI, documented in `/web` standards docs + ADRs.

**Server state.** All server data flows through TanStack Query over the **generated** client — hand-written fetches banned. Realtime is not polling: SSE deltas write into the Query cache (`setQueryData`), Query owns fetch/cache/refetch-on-reconnect. URL-as-state for every table/filter/tab (Zod `validateSearch` with `.catch`).

**Realtime client.** One SSE abstraction: snapshot-then-deltas handling, `Last-Event-ID` resume, exponential backoff, heartbeat watchdog surfacing staleness honestly ("reconnecting", never silently stale). Attention stream mounts app-wide; per-run stream on run detail. Assistant WS codec layer typed from the same generated contract.

**Forms.** The shared typed form abstraction is built **before any feature form**: react-hook-form + Zod + generated request types, one field-component registry, uniform error rendering, submit orchestration mapping problem-envelope field errors onto fields automatically. Flagship consumer: the workflow editor's step cards, whose fields are *generated from the engine's step schemas* — new step types appear without UI code.

**Theming & mobile-first, by construction.** Semantic OKLCH tokens as CSS variables; `data-theme` on root; system default; exactly one dark (flagship) + one light shipped — a new theme is a token file, never a component edit. Bottom sheets as default modal on phone, safe-area insets, 44px targets, web app manifest (installable; no offline promises in V1), self-hosted fonts, monospace for data/logs/IDs/statuses. Card-first lists with desktop table projections.

**Serving & future split.** The `/web` build embeds into the Go binary (`embed.FS`, SPA fallback) — same-origin by construction, no CORS/nginx. Dev loop: Vite HMR proxying to the local backend. The UI can graduate to its own deployment cheaply (static hosting + API base URL + cookie-mode config); kept cheap by a configurable client base URL and sessions behind the auth port.

**Testing.** Vitest + Testing Library; MSW at network level against the generated client; Playwright with a mobile-viewport project covering phone-critical paths (gate approve, live timeline, task creation).

## 6. Security & RBAC

**Authentication.** Local accounts: email + argon2id (pinned params), admin-invite-only. Server-side sessions via `__Host-` cookie (`HttpOnly`, `Secure`, `SameSite=Lax`); rotation on login/privilege change; idle + absolute expiries; instant revocation. Login rate limiting (per-account + per-IP) with lockout backoff, audited. Auth boundary is a port (OIDC later). Cloudflare Access may sit in front as an optional outer gate; caesar's auth remains authoritative.

**RBAC (V1 model, approved).** Global roles `admin` / `member`. Per-project inclusive grant ladder: **`view` ⊂ `run` ⊂ `approve` ⊂ `manage`** — one grant row per (user, project); checks are `grant >= level`. Self-approval is a **policy field**: `allow_self_approval` defaults true in the shipped baseline (solo-friendly), teams flip it in the versioned baseline; self-approvals are marked distinctly in audit.

**Requester principal.** Every task records the initiating human; launch requires `run` on the project — agent authority is a projection of human authority, enforced structurally at intake. Gates route to `approve` holders via policy metadata. The requester's identity flows into audit, run records, and published commits/PRs (exact attribution format pinned in the plan).

**Policy engine.** Versioned baselines per repo/category (categories = the step-class taxonomy — one vocabulary for gate requirements and kill switches); per-run overrides tighten-only and non-sticky; durable kill switch per repo/category; loosening only by editing the versioned baseline. V1 fields: gate requirements per step class (code-publish gates non-negotiable), `allow_self_approval`, attempt limits, execution-budget and cost ceilings, autonomy-tier thresholds — class-segmented success counts, computed by the orchestrator from its own observations, that let the baseline relax gate requirements for lower-risk step classes as tiers are earned (code-publish gates never auto-relax; docs-level success never unlocks code tiers). Evaluation produces the run's pinned PolicySnapshot.

**Credentials at rest.** Envelope encryption per row: AES-256-GCM under a master key from env/file (`key_id` recorded for rotation; KMS/age pluggable behind the same port later). Decrypted material only in memory at use time; every use audited. Agent containers receive only the per-attempt model credential; forge tokens never leave the control plane.

**Redaction & defense-in-depth.** Central redactor scrubs known live secret values from logs, run events, and failure snapshots (snapshots also scrub by pattern). Pre-publish secret scanning is defense-in-depth — the gate is deterministic validation + human approval, never the scanner.

**Assistant authority.** None of its own: mutations require human `tool_confirm`, execute through the same domain services/permissions, audited as the human with an assistant marker. Repo content, diffs, and logs it summarizes are **untrusted data** — they can never trigger tool execution.

**Web hardening.** Strict same-origin CSP (no inline scripts), standard security headers, HSTS behind the tunnel, Origin-check middleware on unsafe methods.

## 7. Testing & CI

**Resolved splits (approved):** stdlib asserts + `go-cmp` (no testify) · **Taskfile** (CI and local runs share the same `task` targets) · strict JSON config (see §8) · errors two-axis internally / problem-envelope at API (§4).

**Backend pyramid.**
- **Unit:** table-driven, behavioral names, TDD workflow.
- **Property (`rapid`):** DAG resolution/cycle detection, CAS state-machine legality, policy merge monotonicity (tighten-only), budget arithmetic. Pinned seed in PR CI; nightly randomized.
- **Fuzz (committed corpora):** definition parser/validator, patch application, SSE/assistant frame codecs.
- **Integration (testcontainers, real Postgres):** claim/lease/heartbeat under concurrency, attempt fencing, reconciliation scenarios (adopt vs kill-then-inspect), same-tx event+outbox atomicity, migration up-down. **Runner integration against real Docker:** hardening flags applied, egress proxy allow/deny/log paths, workspace lifecycle, output collection.
- **Architecture tests + custom `go/analysis`:** context-boundary imports, dependency deny-list, "transactions never span contexts", no `os.Environ()` in runner paths, no dead config (every field has a read site), stubs return typed `NotImplemented` — never fake success.
- **Golden-set evaluation harness, early:** deterministic fake harness adapter (scripted outputs) + local bare-repo remotes + fake forge adapter, driving full conveyor runs — task → phases → gate → approve → publish-with-expected-SHA — asserting artifacts, events, audit. Doubles as the replay/debug path, independent of the observability stack.
- **Real-harness contract tests:** recorded CLI output fixtures; creds-gated live smoke nightly/manual, never PR-blocking.

**Frontend.** Vitest + TL; MSW at network level; Playwright incl. mobile-viewport project; Biome as the single lint/format gate.

**CI (GitHub Actions, composite required check, all via `task`):** backend (build, fmt, vet, golangci-lint, race tests, property-pinned, integration, architecture tests, `govulncheck`) · frontend (biome, typecheck, vitest, build, Playwright) · **contracts** (OpenAPI regen-diff, TS client regen-diff, sqlc regen-diff, migration up-down) · secrets (`gitleaks`, pinned). On `main`: build + push digest-pinned image to ghcr (cosign later). Nightly: randomized property seeds + live harness smoke. Mutation testing explicitly later.

## 8. Deployment & operations

**The stack.** One `compose.yaml` in `/deploy/compose`: **caesar** (ghcr image, digest-pinned) · **postgres** (pinned major, single instance) · **cloudflared** (tunnel token → `caesar.<zone>`) · **backup** sidecar (nightly `pg_dump` → local dir + optional `rclone` to a Hetzner bucket, N-day retention; meets the relaxed durability bar — PITR is k8s/CNPG-mode content) · optional **Tetragon standalone** (observe-only policies adapted from redlight's four shapes). Agent containers are not compose services — the runner creates/destroys them per attempt on per-run internal networks. Artifacts: structured work products in Postgres; bulky logs + snapshots on a disk volume with a retention sweeper.

**Target.** The user's dedicated Hetzner box (96 CPU / 250 GiB, Proxmox; Superset LXC neighbor): caesar in one Linux VM (~2/3 of the box). VM recipe documented in `/deploy`; Proxmox is not caesar's concern. No new hardware, no separate infra repo.

**Trust boundary (approved).** V1 mounts the Docker socket into caesar's container; **the VM is the security boundary, not caesar's container** — caesar is already the trusted control plane holding every secret. Agent containers never see socket, secrets, or a route to caesar. Socket-proxy filtering and rootless runtimes are documented hardening options.

**Config.** One **strict JSON config file** (stdlib `DisallowUnknownFields` — unknown keys rejected) + a small fixed set of env overrides for **bootstrap secrets only** (`DATABASE_URL`, `CAESAR_MASTER_KEY`). Model-provider API keys are not env config — they live as encrypted Credential rows referenced by Harness registrations (§2), injected per attempt. Secrets on the box: chmod-600 `.env`; SOPS+age documented as the optional upgrade.

**Networking.** Cloudflared is outbound-only — zero listening ports beyond SSH; UI reachable exclusively through the tunnel (+ optional Cloudflare Access). `/metrics` and `/healthz` not tunnel-exposed.

**Upgrades.** CI pushes digest-pinned images on `main`; deploy = `task deploy` (SSH: `compose pull && up -d`). Migrations: Atlas-authored, goose-embedded, run at startup under a Postgres advisory lock; down-migrations committed; rollback = previous image + documented down path.

**Observability.** `slog` JSON to stdout; Prometheus `/metrics` for future scrape; correlation IDs everywhere. Alerting V1: `/healthz` + dead-man ping to hc-ping/ntfy. No Grafana stack requirement — dependency count stays at three.

**k8s mode (secondary).** `/deploy/k8s`: minimal raw manifests exercising the k8s adapter (Deployment/Service, NetworkPolicies + egress patterns from the redlight harvest, CNPG-or-external-Postgres notes). CI: kubeconform static validation; kind smoke nightly/optional. Helm post-V1.

**Runbooks in `/deploy`:** Proxmox VM recipe, restore-from-backup drill, upgrade + rollback, credential/master-key rotation, session mass-revocation, kill-switch usage.

---

## Safety invariants (requirements, inherited — full text in decisions §6)

1 orchestrator-not-agent · 2 Postgres single dispatch authority · 3 events-are-observation, consumer idempotency, at-least-once honesty · 4 reconcile-before-retry · 5 attempt-scoped identity · 6 policy = evaluated immutable struct, tighten-only overrides, kill switch · 7 pre-mutation constraints + deterministic validation before publish · 8 isolation spends on blast radius · 9 correlation IDs everywhere + redacted failure snapshots · 10 orchestrator-computed autonomy gates · 11 two clocks · 12 durable-before-acknowledge intake + two-level dedup + typed outcomes + supersede-before-work · 13 no dead config, no metrics without call sites, stubs never fake success · 14 replay path independent of observability, golden set early · 15 write-path validation, real DAG edges, Go types as single source of truth.

## Left to the implementation plan (deliberate)

Commit/PR attribution format for requester identity · assistant model/provider + budget mechanics · harness image contents (build from scratch vs adapt) + per-harness FQDN allowlists (incl. telemetry-off defaults) · exact VM sizing/OS + tunnel hostname · golangci-lint config detail · exact policy JSON schema · full status enums + transition tables per entity (the invariants in §2/§6 constrain them) · login rate-limit parameters · retention numbers (logs, snapshots, backups, sessions).
