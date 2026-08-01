# caesar — V1 decisions record (brainstorming phase)

**Date:** 2026-08-01 · **Status:** LOCKED unless explicitly reopened · **Process:** superpowers:brainstorming, mid-flight (see `2026-08-01-handoff.md`)

Every decision below was made explicitly with the user during the 2026-08-01 brainstorming session. Do not re-litigate silently; if new evidence challenges one, raise it with the user.

---

## 1. Product identity

- **caesar = a deterministic orchestrator of agentic workflows.** Custom platform, built from scratch, learning from (not extending) prior systems.
- **V1 identity: "Engine + agentic library."** Core is a typed workflow engine: definitions are validated *data* (DAG of typed steps, gates first-class), interpreted deterministically by Go. The first shipped step vocabulary is repo-centric agentic development (plan / implement / review / gate / git-publish). agentd's workflow ladder becomes *content*, not code.
- **Determinism means:** Go owns the control flow and performs every side effect; agents only *propose* work products. Every run pins the content hash of the fully resolved definition it executed. (Not claiming bit-identical LLM re-execution.)
- **First use case (month-one success):** UI-driven conveyor — create a task in caesar's UI, pick a workflow, point at a repo, watch phases live, approve gates from a phone, caesar pushes branch + opens PR. The forge is a *target*, not a trigger.
- Explicitly deferred: forge-watch intake (issues/MRs as triggers), React Native app, **multi-org (dropped entirely — single-org, no org entity)**.

## 2. Stack

- **Backend:** Go. Modular-monolith control plane — one binary: HTTP API, scheduler, orchestrator, SSE streaming, assistant backend. Hexagonal/DDD boundaries inside (see §9).
- **Database:** PostgreSQL = **sole source of truth and sole dispatch authority.**
  - Queue: `SELECT … FOR UPDATE SKIP LOCKED` + CAS state transitions (`UPDATE … WHERE status = $expected`) + durable leases (`worker_id`, `leased_until`, heartbeats). Attempt limits enforced in the claim `WHERE`.
  - Events: append-only `run_events` table written in the same tx as state changes; transactional **outbox** table from day one (future NATS relay attaches there; no semantic change).
  - LISTEN/NOTIFY = **wake-up signal only, never the data** (at-most-once, non-durable). Consumers read state first, then listen; slow fallback polls. Lost notification costs latency, never correctness.
- **No NATS, no Redis in caesar's core loop.** Both exist on the target cluster; "available ≠ load-bearing." NATS may later relay the outbox for external consumers. Redis unused.
- **API:** REST + OpenAPI 3.1 on stdlib `net/http` mux. Generated TypeScript client; schema-drift CI gates (port of the bitnet-frontend-admin discipline). Realtime to browser: **SSE** (snapshot-then-deltas, resume via last event id). No GraphQL, no gRPC in V1.
- **Frontend:** React mobile-first web app (React Native later). See §7/§10.
- **Repo:** monorepo — `/backend`, `/web`, `/deploy`, `/docs`.

## 3. Execution & isolation

- **Every agent run executes in a disposable container.** Runner is a **port** with two adapters from day one:
  - **Prod: Kubernetes Jobs** on the user's existing Hetzner cluster (redlight project: Talos, Cilium, Tetragon, CNPG, OpenTofu IaC; NATS/Redis present). Hardened pod spec per agentd doc-08: fresh pod per run, `readOnlyRootFilesystem`, `runAsNonRoot`, drop ALL caps, `automountServiceAccountToken: false`, ephemeral workspace, `activeDeadlineSeconds`, `ttlSecondsAfterFinished`, `backoffLimit: 0`.
  - **Dev: local Docker/Podman** on the Mac. **Hard condition: the dev loop stays cluster-free** — control plane runs locally against local Postgres; no image-push/Flux wait in the inner loop.
- **Egress:** Cilium FQDN allowlist per run — model provider endpoint(s) + target forge only; **no route to caesar's control plane or Postgres**. (Allowlist must cover each harness's own provider endpoint.)
- Env allowlist always (never `os.Environ()` passthrough — a shipped bug in two predecessors). Scoped, short-lived credentials per run. Tetragon for runtime security observation/enforcement on agent pods.
- Cost note: fits in the user's existing $200–300/mo cluster; dedicated tainted node pool for agent Jobs only when volume justifies.

## 4. Harnesses & forges

- **Harnesses V1: Claude Code + Codex CLI**, one-shot per phase, behind a single harness port (container image + credential plumbing + output-parsing adapter each). No warm/persistent agent processes (breaks per-phase isolation; audit verdict).
- **Forges V1: GitHub + GitLab**, behind one forge port. Core git ops (clone/branch/push) forge-agnostic. Design the review/comments primitive per-forge (`ReviewComment{path,line,side,body}`-shaped) with graceful fallback; prefer atomic batch publish where a forge supports it.
- **Pre-publication approval for code:** caesar holds branch/PR until the gate decision; the reviewed artifact is a durable patch (diff + base SHA + tree hash) applied from a fresh trusted checkout on approval, then pushed with an expected-remote-SHA precondition. Never publish-then-gate for code.

## 5. AuthN / AuthZ

- **Local accounts:** email + password (argon2id), hardened session cookies + server-side revocation table, admin-invite-only registration. Auth boundary is a port (OIDC addable later). TOTP/passkeys later.
- **RBAC in Postgres, single-org.** Roles + per-project/repo grants (exact role set to be fixed in the spec; ladder direction: read / comment / write / approve / admin).
- **Requester principal end-to-end:** every run records the initiating human; flows into audit rows, run records, commits/PRs. Agent authority = a projection of the requesting human's authority — structural, not behavioral.
- Approvals route by policy (metadata-derived routing pattern), not manual assignment.

## 6. Policy & safety invariants (inherited from predecessor audits — treat as requirements)

1. The orchestrator is not an agent; the model is a subroutine.
2. Postgres is the single dispatch authority; no second producer path.
3. Events are observation, never a work queue; consumer idempotency on outbox id; at-least-once honesty.
4. `reconciling` is a first-class state: reconcile-before-retry (adopt completed results idempotently; kill-then-inspect otherwise; `running → pending` forbidden structurally).
5. Run identity = the **phase attempt** (`run_id`, `attempt`): Job names, secret paths, artifact keys, event subjects, branch refs all attempt-scoped.
6. Policy = evaluated **immutable struct** that drives gates; layered (versioned baseline → per-run human override, non-sticky → adaptive **tighten-only**); durable per-repo/per-category kill switch; loosening always requires a human editing the versioned baseline.
7. Pre-mutation write constraints; deterministic validation (repo's own build/lint/test) before publish; secret-scan is defense-in-depth, never the gate.
8. Isolation spends on blast radius, not throughput.
9. Correlation IDs (`workflow_id`, `run_id`, `phase`, `attempt`) in every log line, span, header, job label. Redacted failure-snapshot bundles written by the control plane, with retention.
10. Autonomy gates: absolute, class-segmented counts, computed by the orchestrator from its own observations (never from agent self-reports); docs-level success never unlocks code-writing tiers.
11. Two clocks: execution budget pauses during human waits; wall-clock total never pauses.
12. Durable-before-acknowledge intake; two-level dedup (delivery key vs semantic key); typed outcome errors with unclassified → permanent; supersede-before-work with `superseded` as its own terminal state.
13. No dead config (a field lands in the same commit as its first read); no metrics without call sites; stubs must never report success.
14. Replay/debug path must not depend on the observability stack. Evaluation harness (golden set) early.
15. Definitions: validation on the write path (the API that persists is the only door); no entities nothing reads; the DAG has real edges + cycle detection; schema/runtime drift is the #1 bug class to design out (single source of truth: Go types).

## 7. UI decisions (all locked with mockups; files in `.superpowers/brainstorm/12432-1785604290/content/`)

- **Page map V1:** Home · Tasks · Runs · Approvals · Workflows · Repos · Admin (users/roles, harness config, policy baselines, global audit).
- **Shell (A):** phone = bottom tabs Home · Runs · Approvals (badged) · Tasks · More; desktop = full sidebar + top bar with ⌘K.
- **Home (A):** state-first — one-line assistant-generated situation résumé; "Needs you" gate cards with inline approve/reject; Running cards with live phase dots; recent activity; persistent assistant ask-bar docked above tabs.
- **Run detail (A):** vertical phase timeline as the spine; each phase expands in place (live output stream while active; artifacts/verdicts when done; approve/reject inline at gates); sticky header with cancel/retry; desktop = timeline left rail + detail right.
- **Workflow editor (B):** typed outline of step cards (fields generated from the engine's schema) + live read-only DAG preview; drag to reorder. Canonical artifact is always the schema-validated definition document; outline/canvas/code-view/assistant are all projections. Code view (schema-aware YAML/JSON) and visual canvas come later as additional projections.
- **Tasks (B):** attention-grouped live list — Needs you · In flight (live phase dots) · Up next · Recently done. Statuses move automatically via runs. Board and timeline views are later projections of the same data. New task = FAB → assistant drafts it or a short form.
- **Approval (A):** summary-first — agent summary (3 lines), hard-facts row (files · ±lines · tests · cost), file-by-file accordion expanding to real diffs, per-file assistant "explain" spark, sticky approve / request-changes (note feeds next phase) / reject in the thumb zone. Guided chunk-by-chunk review (C) is a later layer on top, never the only path.
- **Assistant = global surface** (not a tab): bottom sheet/drawer reachable from every page including phone. Understands caesar's API/state; gives situation résumés; drafts tasks and workflow definitions conversationally; explains runs/diffs. Server-driven UI blocks + tool-call confirmation cards; typed frame protocol over its own socket (pattern: bitnet-frontend-admin's ai-assistant subsystem — the realtime reference).
- **Visual style: Terminal** (user's explicit taste; Termius as reference vibe). Monospace for data/logs/IDs/statuses; body text phone-legible; 44px touch targets; squarish corners; console-honest density.
- **Theming day one by construction:** semantic OKLCH tokens (CSS variables), `data-theme` on root, system-preference default; ship exactly 1 dark (flagship) + 1 light; more themes = new token file, never component edits.
- **Mobile-first structurally:** bottom sheets as default modal, safe-area insets, web app manifest, mobile viewport in Playwright CI. Card-first lists with desktop table projections. Self-hosted fonts.

## 8. Deployment

- **Prod:** control plane as a Deployment on the existing redlight cluster; Postgres via CNPG; agent runs as Jobs; ingress + secrets via the cluster's existing conventions (**bind specifics against the NEWER redlight repo** — the local copy at `/Users/sofo/Projects/bitnet/redlight` is stale; user will provide the current one).
- CI → registry → GitOps deploy (cluster conventions); nightly DB backups (CNPG/PITR per cluster setup).
- **Dev:** same binary locally + local Postgres + Docker adapter. `task run`-grade simplicity.

## 9. Backend engineering standards (from the user's reference repos)

- Hexagonal + DDD, **enforced mechanically** (architecture-as-test AST checks and/or custom `go/analysis` analyzers, like bitnet-backend-go / arb-lab).
- Layout: bounded contexts at top of `internal/` as peers of `platform/` (shared kernel, no business logic) and `app/` (composition roots) — arb-lab's newer convention (no `boundary/` segment). Public entry in `cmd/`.
- Stdlib-first with an explicit dependency deny-list: `log/slog` only; `net/http` ServeMux; no ORMs; no DI frameworks (manual composition roots + decorator chains); pgx/v5 + sqlc where useful; migrations tooling per spec (Atlas authoring + goose runtime is the house pattern).
- Injected clock seam; default-deny semantics everywhere (unknown config keys rejected; unclassified errors safe-direction); money/cost never float (decimal).
- Testing: table-driven; property tests (`rapid`, pinned seed + nightly randomized); fuzz with committed corpora; testcontainers integration (real Postgres); behavioral test names; architecture tests; mutation testing later. TDD workflow.
- Lint: golangci-lint broad config with justified disables, plus custom analyzers where a rule is house law. CI gate = composite check (build, vet, fmt, lint, race tests, govulncheck, drift checks).
- Commits: `<type>(<scope>): <description>`, one short sentence, no body, **no AI attribution ever** (user's global rule).
- Open style splits to settle in spec/plan: testify vs stdlib-only asserts; Taskfile vs Makefile; config file shape (env vs strict JSON/YAML); error model (AppError envelope vs two-axis Class/Effect — likely both: two-axis internally, envelope at API).

## 10. Frontend engineering standards (from bitnet-frontend-admin survey)

- React + Vite + TypeScript strict; Tailwind v4 CSS-first (no JS config; tokens in CSS); shadcn-style primitives on Base UI; lucide icons.
- TanStack Router (**file-based**, not a central routes file); TanStack Query for server state over the generated REST client; Zustand for the few client stores; URL-as-state for tables/filters (Zod `validateSearch` with `.catch`).
- Forms: react-hook-form + Zod with a **shared typed form abstraction built first** (the 72-hand-wired-forms lesson).
- Architecture constitution as law from day one: route-centric colocation, no barrel files, named exports, one component per file, no cross-group imports, consumer-count file placement; CLAUDE.md + standards docs + ADRs.
- Quality: Biome (Ultracite) not ESLint/Prettier; Vitest + Testing Library + MSW at network level; Playwright incl. mobile viewport project; codegen/schema drift gates in CI.
- Realtime: SSE client with snapshot-then-deltas; assistant on its own typed-frame socket. No polling-as-realtime.

## 11. Reference corpus (read these before re-deriving anything)

Research summaries live in `docs/research/` (written 2026-08-01):

- `2026-08-01-agentd-audit-and-target-architecture.md` — agentd (direct predecessor) audit synthesis + doc-08 target architecture + direction note + pattern harvest. **The richest source.** Original docs: `/Users/sofo/Projects/caesar/agentd/docs/`.
- `2026-08-01-bitnet-neo.md` — closest working ancestor (webhook→queue→lease→agent→post-back); 10 patterns to copy, 5 to avoid.
- `2026-08-01-ai-augmented-and-vision-docs.md` — Temporal implementation lessons (drop Temporal, keep the seam + discipline) + the product-vision corpus (roles, HITL policy engine, metadata-derived routing).
- `2026-08-01-forge.md` — typed-definitions verdict (Go types as source of truth; CUE deferred; skill contract worth stealing).
- `2026-08-01-go-style-references.md` — bitnet-backend-go + arb-lab style synthesis.
- `2026-08-01-frontend-admin-ui.md` — UI stack survey; keep/upgrade lists.

Selected reference repos (user-confirmed): `bitnet/bitnet-neo`, `bitnet/ai-augmented`, `bitnet/ai-augmented-development-platform` (+v2), `forge/repos/forge` + `forge/docs`, `bitnet/repos/bitnet/bitnet-frontend-admin`, `bitnet/repos/bitnet/bitnet-backend-go`, `misc/arb-lab`. Global taste file: `/Users/sofo/Projects/_CLAUDE.md`.
