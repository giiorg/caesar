# caesar V1 — implementation roadmap (plan of plans)

**Date:** 2026-08-02 · **Spec:** `docs/superpowers/specs/2026-08-02-caesar-v1-design.md` · **Decisions:** `docs/brainstorm/2026-08-01-decisions.md` (LOCKED)

The V1 spec covers one system but many subsystems. Per the writing-plans scope rule, each subsystem gets its own implementation plan that ends in working, testable software. This document is the sequence and the contract between plans.

**Rules.**

1. **Just-in-time authoring.** A plan is written only when its predecessors have landed, so its tasks bind to real code, not predicted code. Only the currently-active plan is guaranteed current.
2. **Never renumber.** New scope appends (16, 17, …); a split plan becomes `NNa`/`NNb`.
3. **Each plan ends green.** Full `task ci` passes at every plan boundary; a plan that leaves the tree red is not done.
4. **Codex review moves to code.** Per the spec's closed review budget, verification of the round-5 spec fixes happens in implementation-stage `code_diff` reviews within each plan's execution, not in further artifact rounds.

## Sequence

| # | Plan | Deliverable when done | Status |
|---|------|----------------------|--------|
| 01 | Foundations | Monorepo + Go skeleton: strict JSON config, slog, pg pool + testcontainers, Atlas/goose migration machinery, `/healthz`, architecture tests + custom analyzers, golangci-lint, gitleaks, Dockerfile, CI composite gate. Binary boots, migrates, CI green. | **done** — `2026-08-02-caesar-01-foundations.md` |
| 02 | API, auth & RBAC | Contract pipeline (Go types → OpenAPI 3.1 → TS client, regen-diff gates), sqlc setup with first schema, problem+json envelope, users/sessions (argon2id, `__Host-` cookie), grant ladder, login rate limiting (params pinned here), `caesar admin bootstrap`, audit entries, clock seam. | **done** — 2026-08-02-caesar-02-api-auth-rbac.md |
| 03 | Assets & credentials | Projects / repos / credentials (AES-256-GCM envelope encryption, `key_id`) / harness registry; credential-use audit. | **done** — 2026-08-03-caesar-03-assets-credentials.md |
| 04 | Workflow definitions | Typed definition document, validator with all §2 invariants (typed artifact bindings, linear patch lineage, gate-dominates-exact-patch, one-gate-per-publish, revision-transition dominance, cycle detection), immutable content-hashed versions, single write door. Property (`rapid`) + fuzz coverage begins. | **done** — 2026-08-03-caesar-04-workflow-definitions.md |
| 05 | Execution spine | Tasks / runs / PhaseRun-as-queue (immutable attempt rows, claim CTE, leases, heartbeats, `reconciling`), commit-ordered RunEvents + counter row, outbox lifecycle, two clocks + transactional deadline predicates, minimal pinned PolicySnapshot, status enums + transition tables for spine entities. Deterministic fake harness + fake forge + local bare repos → **golden-set harness seed**. A run executes end-to-end against fakes. | **done** — 2026-08-03-caesar-05-execution-spine.md |
| 06 | Docker runner & egress gateway | RunSpec, hardened per-attempt containers/networks/volumes, disk quotas + free-space watermark (mechanism pinned here), `caesar egress-proxy` (broker token format + per-provider operation allowlists pinned here; CONNECT allowlist), clone staging contract, hostile-`.git` extraction, validation containers, log caps, reconciliation against real Docker, runner integration suite incl. escape probes. | **done** — 2026-08-04-caesar-06-docker-runner.md |
| 07 | Harness adapters & artifact flow | claude-code + codex adapters (harness image contents + per-harness FQDN allowlists pinned here), filesystem contract, typed outcome mapping, artifact lineage materialization. Subscription-first custody: materialized credentials, single-flight credential leases, delayed retry on cap hits, exact-value redaction with quarantined logs. Real agent produces a patch through the conveyor. | **implementation complete — smoke pending, provisional codex markers** — `2026-08-05-caesar-07-harness-drivers.md` |
| 08 | Gates & publishing | Gate decisions (CAS + deadline predicate), single-use ApprovedPatch, `git-publish` effect state machine + deterministic markers + recovery, forge port + GitHub + GitLab adapters, commit/PR attribution format pinned here, publish status enums, golden-set crash injection around every publish step, **generations / revision rematerialization (moved from 07)**. **Milestone: month-one loop complete via API.** | **implementation complete — live-forge smoke pending** — `2026-08-07-caesar-08-gates-publishing.md` |
| 09 | Policy engine & kill switch | Versioned baselines (exact policy JSON schema pinned here), tighten-only overrides, snapshot evaluation, durable kill switch with two-part fencing (control-plane lock sections + gateway-kill-first), autonomy-tier signals, budget/cost ceilings. | pending |
| 10 | Realtime SSE | Single tailer (LISTEN/NOTIFY + slow-poll), in-memory fan-out, snapshot-then-deltas with watermark splice, `Last-Event-ID` resume, authorization-scoped streams, bounded per-subscriber queues. | pending |
| 11 | Web foundation | Vite + React + TS strict scaffold, constitution enforced by Biome/CI, generated client + TanStack Query + SSE-into-cache wiring, typed form abstraction, OKLCH token theming (1 dark + 1 light), app shell + tabs. | pending |
| 12 | Web features | Home / Tasks / Runs (timeline) / Approvals (approve screen) / Workflows (outline editor, schema-generated step cards) / Repos / Admin; Playwright mobile-critical paths. **Milestone: month-one flow on a phone.** | pending |
| 13 | Assistant | WS frame protocol (origin-checked upgrade), `tool_confirm` proposal binding, provider + budget mechanics pinned here, metering/audit, UI surface (global sheet). | pending |
| 14 | Deployment & ops | `/deploy/compose` prod stack (caesar + postgres + cloudflared + backup sidecar + optional Tetragon), VM recipe + host firewall baseline + sizing/tunnel hostname pinned here, `/metrics`, retention numbers + sweepers wiring, runbooks, `task deploy`. | pending |
| 15 | K8s adapter | Stage/collect Jobs + attempt-scoped PVC + gateway pod, NetworkPolicies (`toFQDNs` + wildcard companions), negative probes, `/deploy/k8s` manifests, kubeconform + kind smoke. | pending |

Sequence is dependency-ordered but not rigid: 10 (SSE) can start any time after 05; 14 can begin once 08 exists; 13 and 15 are last-movers. 09's kill-switch fencing needs 06.

**Two notes on row 07.** *Generations / revision rematerialization moved to Plan 08*: it needs gate decisions to exist before it can be built, and every other 07 deliverable landed without it. And 07 does **not** flip to done on green CI. Per its own gate, the plan's acceptance evidence is the manual smoke checklist in `backend/README.md` — run once by the operator against real subscriptions, because no CI lane may ever place a live subscription call. Until that runs and its evidence is committed, the codex exit-marker rows in `internal/runs/runner/driver.go` remain **invented** rather than recorded: the CI fixture speaks them, which proves the mapping engine and nothing about the CLI. If the real CLI speaks differently, cap hits charge as `harness_error` instead of parking and auth death stops failing fast. The row closes on that single item; it never closes silently.

Those are the *miss* failures, and they are the mild ones. The two the smoke run structurally **cannot** close are the false-match failures, because it observes what a CLI prints and never what a model prints into the same stdout: an auth row that matches the agent's own prose is **permanent** and writes `last_auth_expired_at` onto a live, working credential, and an epoch the capture mis-reads (a millisecond timestamp read as seconds) yields a provider-stated reset that bypasses the retry cap by design and therefore **terminalizes a run that was owed a park**. Both rows were narrowed for precision *before* the run rather than after it.

**One note on row 08.** It does not flip to done on green CI either, and for
the same reason row 07 does not: its acceptance evidence is an operator event.
Every forge assertion in the plan is against `httptest` servers and local bare
repositories, and **no CI lane has ever made a live GitHub or GitLab call** —
so the two adapters' response shapes and error mappings are invented in
fixtures exactly as the codex exit markers were. If a forge answers
differently, `repo_unavailable` can present as a `forge_unavailable` retry loop
against a vanished repository with a pushed branch orphaned behind it, or
`auth_expired` can stop failing fast. The row closes on the **manual forge
smoke checklist** in `backend/README.md` — one throwaway repo per forge, one
task through a gate to an approve, then the branch, the four `caesar-*`
trailers, the committer identity and the pull request's base verified by eye,
plus the credential-dead halt observed at the publication's recovery probe.
One arm is out of a manual run's reach: `repo_unavailable` fires only once an
intent is already `pushed`, so it stays a named residual on the plan rather
than holding this row open. The row's *Milestone: month-one loop
complete via API* claim rides on that same evidence: nothing here is verified
until the checklist runs and its result is committed.

## Where the spec's "left to the implementation plan" items land

| Item | Plan |
|------|------|
| golangci-lint config detail · Postgres major pin (18) · Go version (1.26) | 01 |
| Login rate-limit parameters · sqlc machinery (needs first schema) · injected clock seam (first consumer: session expiry) | 02 |
| Full status enums + transition tables | 05 (spine), 08 (gates/publish), 02 (sessions) |
| Broker token format + per-provider operation allowlists · per-attempt disk-quota mechanism + free-space watermark | 06 (values revisited in 14) |
| Harness image contents + per-harness FQDN allowlists (telemetry-off defaults) | 07 |
| Commit/PR attribution format for requester identity | 08 |
| Exact policy JSON schema | 09 (minimal snapshot fields already pinned in 05) |
| Assistant model/provider + budget mechanics | 13 |
| Exact VM sizing/OS + tunnel hostname · retention numbers (logs, snapshots, backups, sessions) · `/metrics` endpoint (no metrics without call sites) | 14 |
