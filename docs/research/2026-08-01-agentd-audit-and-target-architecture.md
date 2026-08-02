# agentd architecture docs — summary for caesar

*Research artifact, 2026-08-01. Produced by a read-only survey agent over `/Users/sofo/Projects/caesar/agentd/docs/`. The full 8-round-hardened executive assessment (`docs/audit/2026-05-27-agentd-platform-assessment/00-executive-assessment.md`) was additionally read in full directly — its P0/P1 findings tables and Slice 0A–4 roadmap are worth re-reading at design time. agentd itself: ~43K LOC Go, SQLite, daemon + CLI + dashboard, live on the user's Proxmox lab; the direct predecessor of caesar.*

**TLDR:** These four docs are one argument in four voices. The external v3.1 doc is an *aspirational* cloud-scale reference (EKS/Temporal/NATS/100 agents); doc 07 mines it and rejects most of its infrastructure as wrong-scale while keeping seven scale-independent patterns; doc 08 then *reverses three of those rejections* once a Kubernetes substrate became free, and lands on the most complete hardened target architecture of the set. The 2026-05-24 direction note is the odd one out — it is the only doc about **product shape** (task intake, adapters, orchestrator boundaries) rather than runtime mechanics, and it is the most directly transferable to caesar. For a greenfield Go+Postgres+React multi-user system, doc 08's §5/§8/§9 and the direction note's §3/§4 are the load-bearing inheritance; everything AWS/Temporal/Forgejo-specific is not.

---

## 1. `08-redlight-fit-target-architecture.md` (739 lines, FINAL, operator-bounded at review round 25)

**What it is.** A finished target-architecture spec for running agentd as a workload on a *sibling* Kubernetes cluster (Hetzner + Talos + Cilium + Flux), reusing an existing infra codebase ("redlight") but deliberately never co-located with it. The most mechanically detailed of the four, and the only one with a migration sequence.

**Key architectural decisions**
- **Per-run Kubernetes Job as the isolation unit.** One Job per agent run, `backoffLimit: 0` (no silent auto-retry), `activeDeadlineSeconds`, `ttlSecondsAfterFinished: 86400`, ephemeral PVC workspace, `readOnlyRootFilesystem`, `runAsNonRoot`, `capabilities: drop ALL`, `automountServiceAccountToken: false`, dedicated tainted nodepool.
- **Run identity is the *phase*, not the workflow.** `${RUN_ID}` = `task-${TASK_ID}` or `wfphase-${WORKFLOW_PHASE_RUN_ID}`; the `attempt` counter lives on the per-run unit. Getting this wrong aliases every phase of a workflow onto one namespace for Job names, secret paths, result keys, and git branches. This was a late (round 24) blocker — worth inheriting for free.
- **Postgres is the dispatch authority.** `SELECT … FOR UPDATE SKIP LOCKED` replaces the in-memory task channel entirely. Workers poll the DB; duplicate dispatch becomes structurally impossible.
- **Durable ownership lease, not advisory locks.** `worker_id` + `leased_until` columns CAS-set with the `pending → running` transition, heartbeated during the run. Explicitly rejects `pg_advisory_xact_lock` because its scope ends at COMMIT while a run spans minutes-to-hours, and because transaction-mode connection pooling forbids session-scoped locks.
- **`reconciling` as an explicit fencing state.** Lease expiry never redispatches directly. Recovery first reconciles against reality: if the Job is terminal and its result validates, *adopt it idempotently* (no attempt++, no re-run); only otherwise kill (foreground-propagation delete, waiting for the Job object to disappear as the pod-termination barrier), inspect the git host for published side effects, then CAS to `pending` with `attempt++`. `running → pending` directly is forbidden as a state-machine invariant enforced by a CHECK/trigger.
- **Transactional outbox** — entity UPDATE + outbox INSERT in one transaction; a separate relay publishes with a dedup message-ID and marks published. Honestly labeled **at-least-once**, not both-or-neither, with consumer idempotency on `outbox.id` as a hard contract.
- **Result bundle over object storage as the authoritative run→control-plane channel** (not the event bus), written by the run via a *presigned single-object PUT URL* the control plane mints. Explicitly downgraded in round 23 from "authenticated" to **self-reported and structurally validated** — the agent can forge its own result, so nothing that matters (tier promotion, publication) may depend on its honesty.
- **Pre-publication patch artifact.** Code-writing runs that must gate before publishing store diff + base SHA + tree hash in the result bundle; on approval the daemon applies it from a *fresh trusted checkout* and only then pushes. This is what makes "gate before publish" survive an ephemeral workspace.

**Tech stack committed to.** Go, PostgreSQL (CloudNativePG) with Atlas for migrations + sqlc + pgx/pgxpool, PgBouncer transaction-mode, NATS JetStream (observation only), Vault + Vault Secrets Operator, Cilium NetworkPolicy with FQDN egress, Kyverno admission policies, Prometheus/Loki/Grafana, self-hosted Langfuse (Postgres + ClickHouse + Redis) for LLM observability, Hetzner Object Storage for backups/artifacts. Rationale is consistently "the substrate is already paid for," plus Postgres's richer CAS primitives.

**Domain/workflow model.** Task and workflow lifecycles with CAS transitions; workflows decompose into a fixed phase graph (7 and 11 phases for the two real workflow types); each phase run is a row with its own attempt fence; approval gates; audit events. Critical asymmetry: **docs-only workflows publish then gate; code-writing workflows must gate before publication.**

**Explicitly warned against / rejected.** AWS/EKS/EFS/S3/Karpenter; Temporal (in-process Go phase graph + Postgres CAS delivers the same benefits); sprint-scale capacity models (operator approvals are the bottleneck, not compute); dedicated high-CPU workers; Go workspaces; GraphQL; per-service `.env` files; a big-bang DDD/hexagonal rewrite concurrent with a migration ("that stack of concurrent breaking changes is how migrations fail" — new modules only). Also rejects Secrets Store CSI in favor of one secret-delivery path. Its own closing note is a warning: 25 adversarial review rounds never converged, and the *last* round still produced three architecture-level blockers.

---

## 2. `[external]-distributed-agent-architecture-v3.1.md` (2568 lines)

**What it is.** A polished third-party reference architecture for a distributed agent platform running ~100 concurrent agents doing sprint-scale autonomous coding, triggered by Jira webhooks, one Kubernetes Job per atomic task. Not agentd's system — a different system at a different scale, deliberately included as an outside opinion.

**Key architectural decisions**
- **The Go binary owns the lifecycle; the AI is a subroutine it calls, not the runtime it runs inside.** Stated as the inversion that makes the system controllable, and the single most quoted line across the other three docs.
- Temporal as the **sole dispatch authority** — its most consequential self-correction (v3.0 → v3.1, "CR-1"): the earlier design had a NATS work-queue competing with Temporal's push dispatch, which it names **split-brain dispatch authority**. The stream was demoted to observability-only.
- Four distinct memory types that must never be conflated: working (LLM context), episodic (per-task, resumable), semantic (pgvector, shared and compounding), procedural (version-controlled config).
- **Three-layer HITL policy engine** evaluated once per run into an *immutable* `HITLPolicy` struct that workflows query at every gate — YAML baseline in git, per-run human override, adaptive layer that **auto-tightens but never auto-loosens** (relaxation requires a human editing the version-controlled baseline). Includes a category **kill switch**: sustained-failure categories get suspended from automation until a human re-enables.
- **Two-timer model**: `execution_timeout` pauses during human waits and resumes with the *remaining* budget; `total_timeout` never pauses. Prevents N approval loops from granting N× compute.
- **Two-tier storage**: fast KV cache in front of Postgres as durable source of truth; cache expiry triggers a DB read, never a data-loss event.
- Failure classification driving *different* retry prompts per failure class (compile/lint/test/git/dependency/security), with **novelty detection** — identical error hash after a retry means escalate rather than burn tokens.
- **Failure snapshot bundle** on terminal failure: exact context, conversation history, git SHA, validation output, sanitized env — written after a configurable redaction pass, plus a CLI that reconstructs the failure environment locally. Framed as an incident-investigation tool: "without this, the team abandons the platform."
- Correlation IDs (`sprint_id`, `task_id`, `workflow_id`) set once and propagated into every log line, span, message header, and Kubernetes label, with a concrete implementation checklist.
- Explicit RPO/RTO per stateful component, with reconstructable components classified as such rather than silently omitted; phased chaos testing; SLO gates between autonomy phases.

**Rejected alternatives, with reasoning worth stealing.** Kafka rejected — "significant overhead for a workload that fits comfortably within [a lighter tool's] capacity… for orchestration signaling, it is overkill." Redis Streams rejected for coordination (kept for caching only). Qdrant deferred behind an explicit measured trigger (p95 vector-search latency > 50ms sustained). It also walks back its own v3.0 "exactly-once" claim to "publisher-side deduplication," noting end-to-end idempotency needs application-level mechanisms at every boundary.

---

## 3. `2026-05-24-agentd-task-intake-platform-direction.md` (355 lines)

**What it is.** A direction note (explicitly "not an implementation spec") recording the platform's intended *product* shape: how work enters the system, where assistants end and the orchestrator begins, and which tools to defer. For caesar this is the most directly reusable of the four.

**The central principle.** *Task intake and assistance are not the orchestrator.* Assistants help shape good work; the orchestrator records durable state, enforces policy, coordinates execution, and preserves the audit trail. The target flow is:

```
human intent → source adapter or intake assistant → normalized task draft
  → operator refinement/approval → authoritative task registration
  → orchestrator selects workflow → agents execute phases → PRs, reviews, gates, audit
```

**Key components**
- **Task Core** — one authoritative component every source of work must route through. It owns task identity, source metadata, target repo/branch, requested outcome, suggested workflow type, policy profile, linked specs/plans/issues/PRs, operator approval state, and audit events. It explicitly does **not** own agent prompting, tool execution, PR mutation, or assistant memory.
- **Source adapters** — issues, dashboard forms, chat intake, CLI/API, future messaging, future scheduled jobs. Adapters normalize and call the core; they never bypass it.
- **Intake assistant** — inspects repos, asks targeted questions, recommends a workflow, and emits a structured draft (repo, intent, suggested workflow, policy profile, outputs, discovered context, open questions). Registration stays explicit and auditable.
- **Workflow registry** — `WorkflowType → phase graph → phase handlers → supported actions → gate behavior → policy profile → PR behavior → resume behavior`. The named pressure point is workflow-specific logic accumulating in one large executor instead of a registry with focused handlers.
- **Vocabulary discipline**: intake assistant / operator assistant / agent harness / orchestrator are four distinct things and the terms are used consistently.
- **State split**: the database holds execution truth (tasks, phase runs, artifacts, gates, review rounds, audit); the repo holds project knowledge and reviewable deliverables (specs, plans, decision notes, runbooks). Neither impersonates the other.
- **Ports and adapters** for every external system: git-host API, git, agent harness, source, policy engine, memory/profile.

**Tech-stack posture.** Go core for state machine, HTTP API, persistence, scheduler, adapters, policy enforcement boundary, workflow registry, dashboard backend — chosen for "transactional correctness, explicit state transitions, and security-sensitive enforcement." Everything else is deliberately deferred: **CUE, OPA/Rego, Goa-AI, Hermes, Temporal all explicitly not yet**. The pattern is consistent and worth copying — *add the interface now, defer the engine*: a policy-engine port with a Go `docs_only` implementation today, so an external policy engine can slot in later against the same typed `PolicyInput`/`PolicyDecision`. Same for Temporal: keep durable state, phase-handler I/O, retry policy, and idempotent side effects separated so a durable-execution engine remains adoptable.

**Explicit "do not"s.** Do not make issues the only source of work. Do not make freeform dashboard forms the long-term intake UX. Do not add code-writing workflows before a scoped write policy exists. Do not let an agent runtime become the authoritative task store. Do not let assistant memory become authoritative platform state. Do not let a second system gain separate authority over task state, gates, policy, or audit. Do not bypass PR-only output and operator gates.

**Capability ladder.** `repo_spec_review` → `repo_pr_review` → `repo_review_fix_cycle` → `repo_plan_authoring` → `plan_scoped_code` policy → `repo_implementation` behind per-repo enablement → intake assistant pilot. Policy profiles: `docs_only`, then `plan_scoped_code`, maybe later `repo_default`.

---

## 4. `07-external-doc-pattern-harvest.md` (146 lines)

**What it is.** A tiered assessment of the external v3.1 doc against agentd's actual scale. Its verdict, and its method, are the valuable part: **"mine the patterns, do not adopt the architecture."**

**The method worth copying.** It applies the external doc's *own* best reasoning — the Kafka rejection — back against every candidate import. Anything that solves multi-node, high-concurrency, or cloud-durability problems the target system doesn't have gets rejected by name so it can't be cargo-culted later. Everything is sorted into four tiers with an explicit mapping to an open finding, so no pattern is adopted without a problem attached.

- **Tier A (adopt now, scale-independent):** correlation-ID propagation; failure-snapshot bundle *with redaction and owner-only storage* (the bundle contains verbatim prompts and instructions, so it is itself a sensitive asset); explicit RPO/RTO per stateful component including "no backup, by design" classifications; SLO-gate governance.
- **Tier B (adopt the concept, build it light):** policy-as-evaluated-immutable-struct that *drives* gates (registries that validate but don't drive are the anti-pattern); one swappable executor port; per-run containment with scoped credentials; the two-timer model; transactional outbox; **single dispatch authority**; secret-scan before publish.
- **Tier C (defer):** compounding organizational memory; per-language validation taxonomy; layered context taxonomy; novelty detection.
- **Tier D (reject as wrong-scale, named explicitly):** AWS/EKS/Karpenter/EFS, Temporal, NATS JetStream, two-tier cache/store split, PgBouncer, the 100-agent capacity model, the full Grafana/Loki/Tempo/Langfuse stack, Chaos Mesh, the Jira-sprint trigger model. *(Note: doc 08 later reversed several Tier-D rejections once the k8s substrate became free — the rejections were scale- and cost-conditional, never principled. The same applies to caesar now that it targets the redlight cluster.)*

**Three corrections it makes to the external doc — each a real design lesson**
1. **Executor port shape.** Adopt the single-port principle, reject the `Plan()`/`Step()` interface: that shape assumes a warm ReAct loop against a raw LLM API, which accumulates cross-phase context and *breaks the per-phase isolation that is load-bearing for the security model*. Keep a one-shot `Run(input) → results` port.
2. **But split the verdict** — one good pattern hides inside the rejected interface: the external doc has the agent *propose* a diff and Go *validate and apply* it, enforcing file scope, command allowlist, and deletion authorization **before** the working tree mutates. The alternative (agent edits freely, policy checked afterward on the committed diff) is the gap. Realize pre-mutation enforcement through tool allowlists, permission modes, and a filesystem sandbox — not by rewriting the agent into a propose-only loop.
3. **Egress.** The external doc's NetworkPolicy lets agent jobs reach the *entire* infrastructure namespace, which contains the control plane; it bounds a compromised agent with credential scoping, not network denial. That is judged insufficient — the agent must be unable to *reach* the control plane at all, with an allowlist covering **each executor's own provider endpoint** (an Anthropic-only allowlist would either break a second executor or leave it unsandboxed).

**Other sharp calls.** Secret scanning is defense-in-depth for a code-writing surface, **never a gate on one** — a non-secret malicious diff passes it untouched. SLO gates must be **absolute and class-segmented**, not rate-over-N: at low volume a rate gate false-passes after a few easy runs or false-blocks on one unrelated rejection, and *docs-only outcomes must never unlock a code-writing tier*. Novelty detection presupposes a deterministic retry loop and actively misfires on a linear graph with two by-design independent review passes. And: **if infrastructure is available, spend it on per-run isolation, not throughput.**

---

## Cross-doc synthesis — the ten ideas caesar should inherit

1. **The orchestrator is not an agent.** Deterministic Go owns lifecycle, state, and policy; the model is a subroutine it calls. All four docs converge on this independently — treat it as settled, not as a decision to re-open.
2. **One dispatch authority, and make it Postgres.** `SELECT … FOR UPDATE SKIP LOCKED` + CAS `UPDATE … WHERE status = $expected RETURNING` + a durable `worker_id`/`leased_until` lease with heartbeats. No in-memory work channel, no second producer path, no advisory locks. This gives multi-worker safety, crash recovery, and connection-pool compatibility in one primitive.
3. **Events are observation, never a work queue.** If caesar adds NATS or Redis Streams, inscribe the invariant now: correctness must not depend on delivery. Pair it with a transactional outbox and be honest that the boundary is at-least-once.
4. **`reconciling` as a first-class state, and reconcile-before-retry.** An expired lease means the *worker* died, not necessarily the *run*. Adopt a completed result idempotently; only otherwise fence — stop the run, wait for actual termination, inspect the git host for already-published side effects, then re-attempt with an incremented attempt number. Forbid `running → pending` structurally.
5. **Get run identity right on day one.** The unit of execution is the *phase*, not the workflow. Every run-scoped name — job, secret, artifact key, event subject, branch ref — keys off `(run_id, attempt)`. Branch names must be attempt-scoped; add an expected-remote-SHA precondition on push so a zombie process can't interleave.
6. **Policy as an evaluated, immutable struct that *drives* gates** — not registries that validate but don't drive, and not a profile hardcoded at the call site. Layer it (baseline in version control → per-run override → adaptive), and make the asymmetry a rule: **the system may tighten itself autonomously; loosening always requires a human editing the versioned baseline.** Include a durable per-repo/per-category kill switch. For caesar's multi-user RBAC this is the natural seam — *who* may loosen what is an authorization question the policy struct should answer.
7. **Pre-mutation enforcement, deterministic validation, then gate before publish.** The failure mode to design out is "edit freely, check afterward, validate never." For any code-writing surface: constrain writes before the tree mutates; run the repo's own build/lint/test (or require a green CI status) before publishing; secret-scan as defense-in-depth only; and for code-writing specifically, **approve before publication** — which requires the pre-publication patch artifact (diff + base SHA + tree hash) persisted durably, then applied from a fresh trusted checkout on approval.
8. **Isolation is for blast radius, not throughput.** One disposable execution unit per run, scoped short-lived credentials per run, and egress denied to caesar's own control plane with an allowlist covering *every* executor's provider endpoint. Known residual: if the agent process and the runner code share a UID, the agent can read the runner's credentials — the fix is a credential broker at a different UID (or never handing the run a write capability at all); keep it a known deferred item rather than a surprise.
9. **Observability as domain design, not instrumentation.** A correlation struct (`workflow_id`, `run_id`, `phase`, `attempt`) set at every entry point, carried in context, and emitted into every log line, span, outbound header, and job label. Plus a redacted failure-snapshot bundle written by the *control plane* (not the run) on terminal failure, with retention. Both are cheap early and near-impossible to retrofit.
10. **Earn autonomy through absolute, class-segmented gates with control-plane-owned inputs.** Minimum absolute counts per risk class, zero containment violations, explicit attribution — never rate-over-N, and never let a lower-risk class unlock a higher-risk one. The metrics that feed a gate must be computed by the orchestrator from its own observations, never copied from anything the agent reported about itself.

**Plus, for caesar specifically:** the direction note's **adapter discipline** is the highest-leverage structural idea for the multi-forge goal — every external system behind a port; every source of work normalized into one task core that nothing bypasses; database holds execution truth, repo holds reviewable deliverables. And the gap: **none of these docs designs multi-user RBAC** — doc 08's identity work is service-to-service, and its only human-facing decision is eliminating server-side sessions in favor of signed cookies plus a small revocation table. Caesar's actor model, roles, and per-repo authorization are net-new design.

---

## Contradictions and tensions between the docs

- **Executor interface (direct conflict).** External §6.2 specifies `Plan()`/`Step()` — a warm ReAct loop. Doc 07 rejects this shape outright (warm loop accumulates cross-phase context, breaking per-phase isolation). Doc 08 sides with 07 (one-shot per phase). The two imply different security models, not just different code. *(caesar decided: one-shot per phase.)*
- **Tier D is unstable.** Doc 07 rejects Postgres-scale infra, NATS, PgBouncer, and the Loki/Langfuse stack as over-engineering; doc 08 reverses three of those rejections weeks later once the substrate became free. The rejections were **scale- and cost-conditional, never principled**.
- **Sequencing conflict.** The direction note says do not introduce NATS/Temporal/OPA/CUE "before the Go core boundaries are explicit"; doc 08 adopts NATS, Vault, and a policy baseline in external storage. Reconcilable (boundaries first, engines later) but the docs never reconcile it.
- **Adaptive policy mechanics (direct conflict).** External §16 gates phase transitions on merge-rate / rollback-rate over N sprints. Docs 07 and 08 reject rate mechanics for low-volume systems and replace them with absolute, class-segmented counts.
- **Novelty detection.** External applies it to all failure types; doc 07 shows it actively misfires on a linear phase graph with two by-design independent review passes. Only valid on a genuine deterministic retry loop.
- **Egress posture.** External allows agent jobs to reach the whole infrastructure namespace, relying on credential scoping. Docs 07 and 08 deny control-plane egress outright — a deliberate strengthening.
- **Secret scanning's role.** External treats gitleaks as a commit-blocking gate; doc 07 demotes it to defense-in-depth.
- **Doc 08 versus itself.** Its §8.1 result channel was described as authoritative and authenticated, then corrected late to *self-reported and structurally validated only*. The corrected reading is right: never let a self-reported artifact move a gate or trigger a publication.
- **Doc 08's own status caveat.** 25 adversarial review rounds never converged; the final round still produced three architecture-level blockers, and blockers got *deeper* over time (authoritative result channel, pre-publication durability, run-identity collision, default-deny ingress). Treat it as a strong pattern catalogue and a taxonomy of mistake-classes — not a validated blueprint.

**Original file paths:**
- `/Users/sofo/Projects/caesar/agentd/docs/audit/2026-05-27-agentd-platform-assessment/08-redlight-fit-target-architecture.md`
- `/Users/sofo/Projects/caesar/agentd/docs/[external]-distributed-agent-architecture-v3.1.md`
- `/Users/sofo/Projects/caesar/agentd/docs/superpowers/specs/2026-05-24-agentd-task-intake-platform-direction.md`
- `/Users/sofo/Projects/caesar/agentd/docs/audit/2026-05-27-agentd-platform-assessment/07-external-doc-pattern-harvest.md`
