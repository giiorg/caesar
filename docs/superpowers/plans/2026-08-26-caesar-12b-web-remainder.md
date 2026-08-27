# Plan 12b — Web Remainder Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** From a fresh browser session, with no curl and no psql: create a project, add a forge credential and a repo bound to it, add a harness credential and register a harness, define a linear workflow in the outline editor, run a task through it, request changes with a note the harness actually receives, approve, open the PR, and afterwards read the decision in Approvals history. When that walk passes on a real deploy — phone for the conveyor half, desktop for the new surfaces — the API stops being the only door and row 12 closes.

**Architecture:** Four waves, fix-first. **Wave 0** is the 12a checkpoint's backlog, backend and web: the gate-note reaches the harness prompt, failure display stops lying, an identical revision is visible, the phone shell's geometry is corrected as a set, event rows and phase cards become links (which requires building the artifact view), the refetch fan collapses to a two-tier invalidation, and packaging/CI close their gaps (git in the runtime image, the runs-list index, go-mod-tidy). **Wave 1** is two new read-only endpoints (`GET /api/v1/audit`, `GET /api/v1/approvals/decisions`), the pulled-in `workflow_id`/`repo_id` filter on `GET /runs`, and the Repos and Admin screens plus the Approvals History tab. **Wave 2** is Workflows: list, version history, version read view with DAG preview, and the linear-first outline editor whose derivation is proven against the real Go validator through a seeded committed corpus. **Wave 3** is desktop: ⌘K and the desktop Playwright project, then the milestone's automated shadow and the operator checkpoint.

**Tech Stack:** Go 1.26 (pgx/sqlc, swaggest openapi-go, stdlib mux, Atlas+goose migrations) · Node 24.19.0 · React 19.2.8 · TypeScript 5.9.3 · TanStack Router 1.170.29 + Query 5.101.4 · react-hook-form 7.85.0 + Zod 4.4.3 · Tailwind 4.3.3 · Vitest 4.1.10 + Testing Library + MSW 2.15.0 · Playwright 1.62.1. **No new dependencies anywhere in this plan** — no cmdk, no drag-and-drop library, no property-testing library; each of those needs is hand-rolled and the task says how.

**Source spec:** `docs/superpowers/specs/2026-08-26-caesar-12b-web-remainder-design.md`. Read it end to end before Task 1 — §2.4, §2.5, §6.3, §10.9 and §10's departure list each contain a decision that is not recoverable from the code.

**Locked design authority:** `docs/brainstorm/2026-08-01-decisions.md` §7 and the eight mockups in `.superpowers/brainstorm/12432-1785604290/content/` (`04-workflow-editor` is 12b's own; the locked variant is **B — typed outline + live DAG preview**). Spec §10 names all eleven departures. Nothing else may be dropped silently.

**Scope decisions already made (sofo, 2026-08-26):** the `workflow_id`/`repo_id` runs-list filter is **pulled in** (spec §10.7/§10.8's plan-time call — it un-defers the workflow-detail run history and the repo recent-activity surfaces); the pending-run "Replace" control (§10.11) is **deferred to row 09** to ride with cancel. §3.2's sibling decisions endpoint and §10.9's hand-authored forms stand as specced.

---

## Global Constraints

Project-wide. They apply to **every** task below and are not re-litigated inside one.

- **Every task's `git add` must cover every file its own prose touches.** Check the last line of a task against its first before running the gate. This is not bookkeeping: `scripts/gate:99` refuses a dirty tree outright, so one unstaged helper edit ends the task at its final step — and three separate tasks in this plan shipped that defect through two review rounds (see Round 3), because a file named in "Files:" and absent from the commit reads as complete to everyone who is not the shell.
- **Every task ends with `scripts/gate <abs-worktree-path> <sha> <branch>` green, on a clean tree.** A reviewer must not edit the worktree while a gate runs; use an overlay or a second checkout. The gate asserts each lane's RESOLVED command character for character — **any Taskfile.yml change in this plan needs a matching `scripts/gate` edit in the same commit** (Tasks 3, 4, and 29 change lanes).
- **Mutation is the acceptance criterion.** Every new guard gets a red-then-revert proof, on the new test as well as on production code. Where a step says "prove it", the proof is a recorded command and its output.
- **Verification is the full gate, never a hand-picked lane list.** `task backend:ci` + `task contracts:check` + `task web:ci` at minimum; the gate script runs more.
- **Each wave lands complete before the next begins**; wave 1's endpoints land with `task contracts:gen` run and `contracts:check` green before their screens are touched. Merging the branch early at a wave boundary stays open (one plan, one branch: `caesar-12b-web-remainder`).
- **Bounded contexts never import each other**; contexts never import `internal/app`; `internal/platform` imports no business code. `internal/arch` fails CI on a violation — but note what it checks: **Go imports**, not SQL. The SQL rule is a convention this plan states and this plan must therefore keep consistent: the runs context's SQL names another context's tables only where an entry below licenses it, and every such entry names its reason.
  - `users`, for the actor's email — precedented by `ListGateDecisionsForRun` and reused by Task 14.
  - `workflow_versions`, for Task 15's `workflow_id` filter. Licensed here, in the constraint, rather than argued inside the task: the alternative is a cross-context read port resolving a workflow id to its version ids before the runs query runs, which buys a second round-trip and a new seam to keep the SQL text clean while the two queries still race. sqlc generates against one schema, the join is read-only, and spec §10.7 names this filter as the whole cost of the deferred surface. **The seam that actually matters stays intact — no Go import crosses, so `internal/arch` still holds the real line.**
  - Nothing else. A third exception is a decision, not a convenience: add it here with its reason or use a port.
- **Go house rules**: no ORMs, no DI frameworks, `log/slog` only, stdlib `flag` only, stdlib asserts + go-cmp.
- **`required:"true"` on every DTO field a 12b screen reads** — with the $ref exception: NEVER mark a pointer-to-named-struct required (a `$ref` property gets no null branch; required would claim non-nullability). Scalars, slices and maps are marked freely — required and nullable are independent. Pattern for every DTO change: Go field → `task contracts:gen` → commit `openapi.json` AND `schema.d.ts` in the same commit → `contracts:check` green.
- **Any new `{id}`-bearing endpoint with a request body needs a `requestOverrides` wrapper in `internal/contracts/contracts.go` or the generator panics.** Any new API constructor argument touches `operationTables()`'s nil-dependency construction in the same file.
- **Deciding predicates read the DB clock** (`DBNow` / `now()` in SQL), never the app clock — a deadline comparison with `s.clock.Now()` on one side is a constraint violation, not a style choice.
- **Migrations are authored, never hand-written**: edit `internal/platform/pg/schema.sql`, then `task db:new -- <name>`, then `task db:sqlc`, then `contracts:check`.
- **Node is 24.19.0 exactly**, via `scripts/with-node`. Web lanes invoke installed binaries **by path** (`./node_modules/.bin/…`); `npx`/`npm exec` never appear. **`npm ci` only; never delete `package-lock.json`.**
- **Named exports only. No barrel files. No default exports.** A module with 2+ direct importers lives at its group root (`tools/arch/import-graph.ts` clamps). `ui/**` imports nothing from `api`/`realtime`/`auth`/`routes`; `api/**` imports nothing from `routes`/`shell`/`ui`.
- **Only the eleven semantic colour names in `tools/arch/tokens.ts` resolve**; arbitrary-value utility classes (`pb-(--x)`) fail `web:arch`. New layout tokens are `@theme` entries consumed as NAMED utilities.
- **Hand-written `fetch` against `/api` is banned by lint**; all server data goes through the generated client and TanStack Query.
- **The cache bridge invalidates; it never reconstructs.** No `setQueryData` from frame payloads. Every bridge seam is proved through injected timers, the way production fires it — a test-only `flush()` is the recorded historical failure, not an option.
- **ONE STREAM PER TAB**; `useAttentionStream` keeps exactly one caller (`_app.tsx`).
- **One `<nav>` landmark.** jsdom applies no media queries; a second `<nav>` breaks eleven role queries across unit and e2e suites. New shell chrome stays inside the existing landmark.
- **`correlation_id` on every error surface**; field errors route through `problem-to-fields.ts` unchanged (flat names — the workflows validator's `nodes[i].field` paths are handled by Task 24's own mapper, not by changing the shared one).
- **Two empty states per list, always distinct; a stub never fakes success; URL-as-state** for filters, tabs, and anything a person would link to.
- **MSW `onUnhandledRequest: 'error'`**: every new endpoint a component test reaches needs a handler in `src/test/render-app.tsx` defaults or the test's own overrides.
- **Editor documents are `"schema": 2`** (the current `workflows.SchemaVersion`); edges are **fully explicit** — the engine never infers one.
- **The milestone walk takes TWO credentials** — a repo binds `forge_token`; a harness binds `subscription_token`/`model_key`/`agent_auth_file` — disjoint kinds, one credential can never serve both. Every fixture, e2e spec, and checkpoint script honours that order: project → forge credential → repo → harness credential → harness → workflow → task.

---

## Pre-flight findings

Verified against `main` at `83aadbc` by reading source, running the real validator over candidate documents, and **measuring the live client in a browser** (finding 1). Several contradict what a reader of the spec alone would assume. The five plan-time verifications the spec names (§2.2, §2.3, §2.5 ×2, §2.6) are findings 1–5.

1. **The `/auth/me` drumbeat's initiator is the 60s refresh floor — measured, not guessed** (spec §2.5's named verification). Method: the `TestWebE2E` world was held open (same boot, steps 1–6, testcontainer Postgres — the deploy untouched) and real Chromium driven against it, every `/api/*` request timestamped. Result: `/auth/me` fired at t=60.6s, 120.6s, 180.6s — exactly the `refetchInterval: 60_000` in `AUTH_SCOPED_QUERY_OPTIONS` (`query-client.ts:39-41`) — through 90s of idle AND 60s with a run in flight; stream bursts added ZERO `me` requests. Each full document load adds one (~700ms after load). Code corroborates: nothing invalidates `['auth','me']` but `login.tsx`; `useMe` has zero consumers; the bridge never touches it. The one initiator with a true drumbeat SHAPE is `sse-connection.ts`'s `probeMe` under reconnect backoff (500ms→30s) — it fires only when the stream flaps, which the healthy bench never did but the checkpoint's phone-over-tunnel deploy plausibly did. **The fix is therefore: touch nothing about `me`'s config** (the floor is the deliberate grant-narrowing cap, row 10a relaxes it); Task 12's test pins a per-endpoint ceiling of ZERO `me` requests under a phase burst; Task 34's checkpoint re-measures the interval SHAPE on the real deploy (flat 60s = floor; exponential-flattening-at-30s = a flapping stream, which is a deploy/tunnel finding, not a client bug).
2. **The phase-tier throttle is 5 seconds** (spec §2.5's second named verification). Chosen to equal the client-wide `staleTime: 5_000` (`query-client.ts:9`): the throttled tier then never serves data older than the staleness the design already accepts, and 5s comfortably satisfies the locked "live phase dots" bound against the 60s floor. It is a named constant (`PHASE_TIER_THROTTLE_MS = 5_000`) and Task 12's test asserts the value.
3. **§2.2 needs NO DTO change — but PHASE STATUS is the primary discriminator, not the counter.** `PhaseResponse` already carries `Outcome *OutcomeResponse` (`class`, `code`, `message`, `retry_at`) plus `Status`, `Attempt` and `MaxAttempts` (`runs/api/dto.go:174-200`). Three engine facts complete the derivation, and the FIRST was missing from this plan's first draft (round 1's blocker):
   - (a) **`failed` means a successor exists; `dead` means the node is over.** `evaluateFailure` terminalizes the attempt row to `PhaseFailed` on the retry branch and inserts attempt N+1 in the same transaction — its comment states the invariant outright, "failed rows always have successors (plan decision 3)" (`orchestrator.go:1573-1592`) — and to `PhaseDead` on the terminal branch, where nothing follows (`orchestrator.go:1616-1622`). So `status` alone answers "will another attempt come?", which is the only question a status line is asked.
   - (b) **An exhausted phase keeps its TRANSIENT outcome.** `finishPhase(…, PhaseDead, …, rawOut)` writes the original outcome; only the RUN-level outcome is rewritten to `attempts_exhausted` (`orchestrator.go:1652-1656`). A derivation keyed on outcome class alone therefore renders `attempt 3/3` — the exact lying counter §2.2 exists to kill — on the single most common exhaustion path, and renders "resumable" for a dead attempt whose ordinal outran the cap through earlier uncharged waits. Both were live bugs in this plan's first-draft formatter.
   - (c) The attempt ordinal outruns `max_attempts` ON PURPOSE — the claim predicate bounds `chargeable_failures`, not the ordinal (`store/queries.sql:128-141`), and only uncharged waits (`rate_limited`, `credential_busy`, `publish_not_started`, `publish_resumable` — `outcome.go:88-97`) can push it past. So `attempt > max_attempts` is an honest "resumable — not chargeable" signal **only on a non-terminal row**, with the reason and `retry_at` on the previous attempt's phase row (also in `run.phases`).
   - And: `auth_expired` is a PHASE outcome, class permanent — the phase goes `dead` and the RUN goes `failed`, never run-status `dead` (`dead` ⇔ `wall_deadline_exceeded`, its sole producer `sweepExpiredRuns`). The status line reads the phase's outcome, not the run's.
4. **§2.3 needs NO contract change either.** No artifact hash exists anywhere (the `artifacts` table stores raw `payload jsonb`; `ListArtifactsForRun` is deliberately metadata-only, and computing a digest there would read every payload per run-detail request). Instead: the gate screen already fetches the examined patch payload through the immutable `['artifact', runId, id]` query (`staleTime: Infinity`); the previous generation's examined artifact is findable in run detail's `artifacts` array (same `node_id`, type `patch`, `generation N-1`, newest attempt), one more immutable fetch — and byte-equality of the two payloads' `diff` strings IS the "one hash comparison". Zero backend change; the extra fetch happens only when a previous generation exists.
5. **§2.6's deadline machinery, verified**: `runs.wall_deadline` is written once at insert and never updated; `DecideGate` refuses on it with the DB clock (`q.DBNow`, `gates.go:171-181`) — grant first (handler), then gate liveness, then **deadline**, then self-approval, then examined artifact. So `deadline_expired` slots **SECOND** in `decidability()`'s deliberate order, between `not_approver` and `self_approval_forbidden`. `WallDeadline` already rides the evidence projection (`gateevidence.go:41-45` — put there in 12a precisely so a card could display expiry before it bites); the only missing ingredient is a `now`, which must come from the DB alongside the read (an app-clock comparison against a DB-stamped deadline is forbidden by `DBNow`'s own contract). One caveat Task 5 must check in-flight: whether the waiting-gates queries exclude expired runs — if they do, the reason is computable but only ever visible on run detail's gate phase (whose evidence rides `PhaseResponse.Evidence`), and the task's tests must cover that path rather than the inbox.
6. **`gate_decisions` carries `generation` but NOT `attempt`** (`schema.sql:340-364`). Attempt-precise links (§2.4/§3.2) need a join through `phase_run_id` to `phase_runs`; the existing `ListGateDecisionsForRun` does not do it. The table's only useful index is `(run_id, node_id)`; a global newest-first read needs the new `(acted_at DESC, id DESC)` composite (Task 14 adds both query and index in one migration).
7. **The audit read path does not exist at all.** `audit_entries` has ONE query (`ListAuditEntriesByAction`) and every caller is a test. No service, no HTTP surface, no pagination. Its only index is `(occurred_at)`; `occurred_at` is not unique (writers stamp it from the injected `Recorder` clock; one transaction can emit several rows) but `id` is a monotonic bigint identity — the composite cursor is `(occurred_at DESC, id DESC)` and Task 13 adds the matching index.
8. **`DeleteRepo` is the one delete in `assets` without the `fkViolation` branch** (`repos.go:214-227` vs `projects.go:149-166`, `credentials.go:319-335`; `UpdateRepo` in the same file uses the helper). Three `ON DELETE RESTRICT` FKs make it a 500 today: `tasks.repo_id`, `approved_patches.repo_id`, `publish_intents.repo_id`. `fkViolation` returns the violated CONSTRAINT NAME, so the message can name tasks vs publish records accurately.
9. **The credential fingerprint is populated ONLY for `agent_auth_file`** — it is `hex(sha256(refresh_token))` feeding a partial unique index; every other kind stores NULL (`credentials.go:103-114`). It is absent from all four metadata queries' column lists, from `assets.Credential`, and from `CredentialResponse` — a three-layer read-path change (SQL → domain → DTO), with the DTO field `*string` + `required:"true"` (required-but-nullable is the honest shape) and an explicit "n/a" rendering for the other three kinds.
10. **The `required:"true"` markup is deliberately partial today** and wave-1/2 screens read unmarked DTOs: every `Credential*`, `Harness*`, `WorkflowVersion*`, `UserListResponse`, `GrantListResponse`. The tasks that build those screens carry the markup change (Tasks 17, 24) — each is a `schema.d.ts` diff and rides `contracts:check`.
11. **`createWorkflowVersion`'s idempotent 200 replay is NOT in the contract** (`httpx.Operation.Status` holds one value; only 201 and 400 are declared, but posting unchanged content returns the existing version with 200 — `workflows/api/api.go:225-229`). The editor's save path must handle a status the generated types do not predict, and treat it as success-with-existing-version.
12. **Validation failures arrive as RFC 9457 field errors whose `field` values are the `Violation.Path` strings** — `nodes[2].harness`, POSITIONAL indexing (`workflows.go:214-219` → `httpx/problem.go`). The editor must keep its card order identical to the posted node order to map a rejection back onto a card. Parse failures (bad JSON, unknown field, wrong schema) carry NO field errors — detail `"invalid definition document"` — and cannot blame a card.
13. **Edges are fully explicit and the engine NEVER infers one.** A gate must have exactly one explicitly authored incoming plan-or-patch edge or the document is REJECTED (`validate.go:207-210`); `graph.examined()` merely selects it. "Nearest preceding producer" (spec §6.3) is the EDITOR's derivation rule, run client-side to synthesize edges — including the review-bypass edge — before save. Verified by running the real `Validate` over candidate documents: `impl→review→gate` with only the verdict edge is INVALID ("a gate examines exactly one plan or patch"); adding the explicit `impl→gate (patch)` bypass makes it VALID. Gates can also examine PLANS (a `plan→gate (plan)` document validates), which the ordinary conveyor never exercises but the DAG preview must render.
14. **The `harness` field is NOT validated against the registry at save time** (`workflows` is pure; resolution happens at task creation — `runs/tasks.go:206-225`, `node %q references unknown harness %q`). A freehand harness name saves happily and explodes later. The editor uses a picker fed by `GET /api/v1/harnesses` — admin-only, which is acceptable because `createWorkflowVersion` is admin-only too; a MEMBER opening a version read view gets harness NAMES from the document, not registry resolution.
15. **The version hash is computed from the PARSED struct** — `Canonical()` is compact `json.Marshal` in struct field order, `Hash()` is sha256 hex (`definition.go:147-161`) — so the editor need not canonicalize anything; whitespace and key order are irrelevant. `UNIQUE (workflow_id, content_hash)` + the FOR UPDATE lock give contiguous version numbers and content idempotency.
16. **The parked runs-list index migration exists NOWHERE as SQL** — only as a roadmap/spec line; no stated shape anywhere. This plan specifies it: `(created_at DESC, id DESC)` on `runs`, mirroring `ListRunsPage`'s keyset `ORDER BY` exactly. The pulled-in filters do not change it: `workflow_id`/`repo_id`/`project_id` all live on (or join through) `tasks`, so the runs-side ordering index is filter-independent.
17. **No go-mod-tidy lane exists anywhere** (Taskfile, CI, scripts/gate). Task 3 adds it from scratch — and edits `scripts/gate` in the same commit (constraint above).
18. **`image-serves-ui` already executes the built image** — by digest, against a `postgres:18-alpine` service (17 lacks `uuidv7()`), with four measured assertions and a `/tmp` publish workdir because distroless-nonroot cannot mkdir under `/var/lib`. The git assertion (Task 4) EXTENDS this proven job. The runtime-image fix itself has a proven shape in the same Dockerfile: the `git` stage (alpine:3.22 + `apk add git` + the caesar binary, USER 65532) — which is exactly what the only working deploy (`caesar-local` = `caesar-git:dev`) already runs. Copying git INTO distroless is a musl-vs-debian libc mismatch that would have to be measured; promoting the proven shape is the plan.
19. **The phone tab bar is NOT fixed and nothing needs clearance today** — it is a flex SIBLING of `<main>` (`app-shell.tsx:45-48`). Making it fixed forces FOUR simultaneous edits: `<main>` bottom clearance, the FAB's offset (`bottom-20`, a local component in `tasks.tsx:174-209` — NOT global chrome), the approval surface's `sticky bottom-0` decision bar, and a stacking order. Task 10 does all four; splitting them ships an overlap bug between commits. There is NO spacing/layout token layer (`tokens.css` is colours, fonts, three radii) — the tab-bar-height token must be a `@theme` entry consumed as a named utility; `pb-(--x)` fails `web:arch`.
20. **`validateSearch` cannot DROP unknown keys** — measured and recorded in `runs.$runId.tsx:25-40`: ancestors contribute the whole raw search object and the route's validated result merges OVER it, so every declared search param is a type the runtime is free to violate. Task 9's `generation`/`attempt` params re-narrow at every read site.
21. **The run-events prefix comment will become FALSE when Task 12 lands.** `run-events-query.ts:18-25` says "the prefix is the whole mechanism" (and cites a bridge line that has already drifted, :92→:100); the spec's exact-invalidation decision replaces that mechanism with explicit tier membership. Rewriting the comment is a step of Task 12, not something to notice later. Likewise `decide-gate.ts`'s `onSettled` invalidates the SAME four keys as the bridge's `fire()` — the twin moves in the same commit.
22. **The three parked geometry assertions** are the 12a ledger's Task-16 finding ("nothing proves the desktop rail paints left… three geometry assertions across the plan wait on this decision"). Operationalized in Task 32: (a) the rail paints LEFT of `<main>` at the desktop viewport (`md:order-first` actually works); (b) run detail's split view is two-pane — rail and `phase-detail` side by side; (c) the phone tab bar is hidden at desktop (the inverse of the phone project's `desktop-rail`-hidden assertion, which itself becomes per-project).
23. **The e2e harness exports FIVE helpers, not four** — `runState(request, runId)` is the backend oracle (`{generation, status}` straight from the database) and the file's own "four control-server calls" comment is stale. New specs assert generation/status claims through `runState`, and Task 33 fixes the comment in passing.
24. **`renderApp`'s default handlers answer `runs: null` / `events: null` / `tasks: null`** — nulls, not empty arrays, deliberately (a Go nil slice marshals to null); new tests keep that convention so every `?? []` branch stays live.
25. **The `walk` workflow's v2 workaround lives in the DEPLOY's database, not the repo.** Retiring it (spec §2.1) is not a code change: the closing checkpoint authors a fresh workflow through the editor with PLAIN instructions and witnesses the note honored without the workaround (Task 34).

---

## File Structure

**New — backend:**

- `backend/internal/platform/pg/migrations/<ts>_runs_list_index.sql` — `(created_at DESC, id DESC)` on `runs` (Task 2, via `task db:new`).
- `backend/internal/platform/pg/migrations/<ts>_audit_read_index.sql` — `(occurred_at DESC, id DESC)` on `audit_entries` (Task 13).
- `backend/internal/platform/pg/migrations/<ts>_gate_decisions_history_index.sql` — `(acted_at DESC, id DESC)` on `gate_decisions` (Task 14).
- `backend/internal/platform/audit/read.go` + `read_test.go` — the paginated read (`ListAuditPage`, cursor codec). Platform-owned; imports no business code.
- `backend/internal/runs/decisionhistory.go` + `decisionhistory_test.go` — `ListDecisions` + its `(acted_at, id)` cursor codec (pure half).
- `backend/internal/runs/api/decisionhistory_integration_test.go` — grant scoping, ordering, equal-timestamp page boundary.
- `backend/internal/identity/api/audit_integration_test.go` — admin-only refusal, ordering, equal-timestamp boundary.
- `backend/internal/workflows/descriptor.go` + `descriptor_test.go` — the §10.9 declarative descriptor and its prove-against-`Validate` table (Task 29).
- `backend/internal/workflows/cmd/descriptorgen/main.go` — emits `backend/api/workflow-descriptor.json` (rides `contracts:gen`).
- `backend/internal/workflows/editorcorpus_test.go` — runs `Validate` over every committed corpus document (Task 28).
- `backend/internal/workflows/testdata/editor_corpus/*.json` — the seeded corpus the web generator emits (generated, committed, regen-diff-gated).
- `backend/api/workflow-descriptor.json` — generated, committed (Task 29).

**Modified — backend:**

- `internal/runs/orchestrator.go` — the gate-note injection in `buildRequest` (Task 1).
- `internal/runs/buildrequest_test.go` — the injection's assertions on assembled `Instructions`.
- `internal/runs/api/api.go`, `dto.go` — `deadline_expired` in `decidability` (+ its `now` threading), the `listDecisions` operation, `ListRunsQuery.WorkflowID/RepoID`.
- `internal/runs/gates.go` / `gateevidence.go` — DBNow rides the waiting-gates read (Task 5).
- `internal/runs/runlist.go` + `store/queries.sql` (+ regenerated) — the two new filter predicates + the workflow_versions join (Task 15); `ListDecisionsPage` + `DBNow` on the evidence read (Tasks 14, 5).
- `internal/assets/repos.go` — `DeleteRepo` gains the `fkViolation` branch (Task 16).
- `internal/assets/store/queries.sql`, `assets.go`, `api/dto.go` — fingerprint through all three layers; `required:"true"` on `Credential*`/`Harness*` (Task 17).
- `internal/identity/api/api.go` — the `listAudit` operation (admin), constructor gains the audit reader → `contracts.go` + `serve.go` wiring (Task 13).
- `internal/identity/api/dto.go` — `required:"true"` on `UserListResponse`, `ProjectGrantResponse`, `GrantListResponse` (Task 17).
- `internal/workflows/api/dto.go` — `required:"true"` on the `WorkflowVersion*` family (Task 24).
- `internal/contracts/contracts.go` — nil-constructor updates; no new requestOverrides needed (both new endpoints are GET-with-query).
- `internal/app/serve.go` — wiring for the audit reader.
- `Dockerfile` — the final runtime stage becomes the proven alpine+git shape (Task 4).
- `.github/workflows/ci.yml` — the in-image `git version` + publish assertion in `image-serves-ui`; the tidy lane (Tasks 3, 4).
- `Taskfile.yml` + `scripts/gate` — `backend:tidy:check` (Task 3); corpus/descriptor gen wiring (Tasks 28, 29). Same-commit edits, always.

**New — web:**

- `src/runs/phase-status.ts` + `.test.ts` — the §2.2 status-line derivation (pure).
- `src/runs/artifact-kind-views.tsx` + `.test.tsx` — the per-kind artifact renderers (Task 8).
- `src/routes/_app/runs.$runId.artifacts.$artifactId.tsx` (+ test) — the artifact view route.
- `src/gates/identical-revision.ts` + `.test.ts` — previous-generation lookup + byte-equality (pure); banner rendering in `approval-surface.tsx`.
- `src/runs/retry-run.ts` — the rerun mutation; header button in `runs.$runId.tsx` (Task 11).
- `src/realtime/cache-bridge.ts` — REWRITTEN two-tier (Task 12; same path, new internals).
- `src/audit/audit-query.ts`, `src/decisions/decisions-query.ts` — cursor-paged infinite queries (wave 1).
- `src/approvals/decision-history.tsx` (+ test) — the History tab (Task 18).
- `src/projects/*` — project list/detail, project + repo forms, grants section (Tasks 19, 20).
- `src/admin/*` — the four Admin sections + placeholder card (Tasks 21–23).
- `src/routes/_app/repos.index.tsx`, `repos.$projectId.tsx`, `admin.tsx` (rebuilt), `workflows.index.tsx`, `workflows.$workflowId.tsx`, `workflows.$workflowId.versions.$version.tsx`, `workflows.$workflowId.edit.tsx` — the screens.
- `src/workflows/derive.ts` + `.test.ts` — cards→edges derivation, round-trip chain check, grammar (Task 25; pure, the property test's subject).
- `src/workflows/dag-preview.tsx` + `.test.tsx` — dependency-free SVG layout (Task 26).
- `src/workflows/editor-*.tsx` + tests — card list, typed card forms, registry (Task 27).
- `src/workflows/drafts.ts` — localStorage drafts (per-workflow key).
- `src/workflows/descriptor-coverage.test.ts` — card registry ⇄ `workflow-descriptor.json` (Task 29).
- `tools/gen-editor-corpus.mts` — the seeded corpus emitter (runs under `node --experimental-strip-types`, Task 28).
- `src/shell/command-palette.tsx` + test (Task 31) — ⌘K.
- `e2e/milestone-walk.spec.ts` — the milestone's automated shadow (Task 33).
- `e2e/desktop-geometry.spec.ts` — the desktop project's shell/geometry/split-view/⌘K assertions (Task 32).

**Modified — web:**

- `src/shell/app-shell.tsx`, `nav.tsx`, `src/styles/tokens.css`, `app.css` — the layout set + wordmark link (Task 10).
- `src/routes/_app/tasks.tsx` — FAB offset + restyle (Task 10).
- `src/approvals/approval-surface.tsx` — decision-bar offset (Task 10); identical-revision banner (Task 7).
- `src/routes/_app/runs.$runId.tsx` — status lines (Task 6), artifact links + `PhaseArtifacts` comment rewrite (Task 8), event/phase links + search schema (Task 9), Retry (Task 11).
- `src/runs/phase-spine.ts` — the targeted-attempt bypass (Task 9).
- `src/runs/runs-query.ts` — filters gain `workflow_id`/`repo_id` (second key element).
- `src/gates/decide-gate.ts` + `src/runs/run-events-query.ts` — the bridge twins (Task 12).
- `src/routes/_app/approvals.index.tsx` — the History tab host (Task 18).
- `src/routes/_app/more.tsx` — links unchanged; stubs it points at become real.
- `playwright.config.ts` — the desktop project (Task 32); `e2e/approvals.spec.ts` — per-project geometry (Tasks 10, 32).
- `src/test/render-app.tsx` — default handlers for every new endpoint.

---

## Dependencies and order

```
Wave 0 — conveyor truth
  1  gate-note injection (buildRequest)                 [backend; the top product fix]
  2  runs-list index migration                          [independent]
  3  go-mod-tidy lane (+ scripts/gate)                  [independent]
  4  git in the runtime image + in-image assertion      [independent]
  5  deadline_expired in cannot_decide_reason           [backend + web render]
  6  honest failure display                             [web; pure derivation]
  7  identical-revision warning                         [web]
  8  artifact view route + per-kind renderers           [web]
  9  linkified rows + attempt-precise gate links        [needs 8]
 10  the tab-bar layout set + wordmark + FAB            [web; ONE task, four edits]
 11  sticky-header Retry on failed runs                 [web]
 12  the refetch fan collapses                          [web; bridge rewrite + twins]

Wave 1 — endpoints, Repos, Admin        (13–15 land, contracts green, before 18–23)
 13  GET /api/v1/audit (+ index)                        [backend]
 14  GET /api/v1/approvals/decisions (+ index)          [backend]
 15  workflow_id / repo_id filters on GET /runs         [backend; pulled in]
 16  DeleteRepo RESTRICT classification                 [backend]
 17  credential fingerprint + wave-1 required markup    [backend]
 18  Approvals History tab                              [needs 14, 9]
 19  Repos: projects list + detail + repo forms         [needs 16, 17]
 20  project grants + repo recent activity              [needs 19, 15]
 21  Admin: Users + the row-09 placeholder              [needs 17]
 22  Admin: Harnesses + Credentials                     [needs 17]
 23  Admin: Audit                                       [needs 13]

Wave 2 — Workflows
 24  list + two-write New + detail + version read view  [+ WorkflowVersion* markup]
 25  the derivation engine (pure TS)                    [independent of 24]
 26  DAG preview SVG                                    [needs 25]
 27  the outline editor                                 [needs 24, 25, 26]
 28  property test + seeded corpus + Go corpus lane     [needs 25, 27]
 29  the §10.9 descriptor gate                          [needs 27]
 30* workflow-detail run history                        [needs 15, 24]  (*task numbered 30a in text order below)

Wave 3 — desktop + closure
 31  ⌘K palette                                         [independent]
 32  desktop Playwright project + geometry assertions   [needs 10, 27; and 31 for the ⌘K check]
 33  the milestone shadow spec                          [needs everything web]
 34  the operator checkpoint                            [needs 33; sofo at a phone and a desk]
```

Wave 0's backend tasks (1–5) land before its web tasks need them only in one place: Task 5's reason rendering. Everything else in wave 0 is independent and may be executed in the listed order. **Task 12 goes LAST in wave 0** deliberately: every earlier web task adds queries/screens the refetch test then counts.

---

# Wave 0 — conveyor truth

Everything the checkpoint proved the system does *right* but shows wrong — or silently drops — is fixed before any new screen is built.

---

### Task 1: The gate-note reaches the harness prompt

The 12a checkpoint's best finding (ledger finding 5, proven live with a byte-identical generation-2 patch): `openRevision` seeds a request-changes note as a `gate_note` artifact and the runner stages it at `.caesar/in/1-gate_note.json`, but the harness prompt is the node's instructions verbatim — generation 2 never learns the file exists. The seam is `buildRequest` (`internal/runs/orchestrator.go:931`), the ONE place every harness's instructions are assembled; its own comment already blesses the append-never-substitute pattern for `OutputShapeInstruction`.

**Files:**
- Modify: `backend/internal/runs/orchestrator.go` (buildRequest, ~line 931)
- Test: `backend/internal/runs/revisions_integration_test.go` (exists — extend). **NOT `buildrequest_test.go`.** That file holds harness-binding cases and the pure `TestCollapsePatchInputs`, and it has no pool or seed fixture because it cannot have one: `buildRequest` builds `store.New(o.svc.pool)` and reads the seeds it must inject through `ListSeedArtifacts` against real Postgres (`orchestrator.go:948-971`). A unit test there does not compile, let alone fail for the right reason. The revisions suite already owns exactly the seam this needs — `bindingRecorder` wrapping the executor and `waitForRequest(t, rec, runID, nodeID, attempt)` returning the real `runs.ExecutionRequest` (`revisions_integration_test.go:321-330`) — which is the assembled-`Instructions` assertion §2.1 asks for, made against the request a harness would actually receive.

**Interfaces:**
- Consumes: the seed loop already in `buildRequest` (`ListSeedArtifacts`, attempt-0 rows); the `gate_note` payload shape `{note, decision, gate_node_id, examined_artifact_id, acted_by, acted_at, generation}` (`revisions.go:262-270`); `maxGateNoteLen` = 8 KiB (`gates.go:40`) bounds growth upstream.
- Produces: `ExecutionRequest.Instructions` ending, when a gate-note seed exists, with the marked requested-changes section below. Every harness (Docker runner, fake executor) receives it identically; the staged file stays — it is the artifact contract.

- [ ] **Step 1: Write the failing test**

In `revisions_integration_test.go`, following the file's own fixture shape — `gatedRunWithExecutor(t, gatedConveyorDoc(), rec)` mints the run, `DecideGate` with `DecisionRequestChanges` writes the note, `waitForRun(t, svc, run.ID, runs.RunWaiting)` settles the generation, and `waitForRequest` returns the request the successor attempt was built with. `TestRequestChanges_ASecondRoundSeesOnlyItsOwnNote` (`:611-640`) is the closest existing case; read it before writing:

```go
// §2.1's acceptance: the note reaches the PROMPT, not only the staging dir.
// Asserted on the assembled Instructions of the request a harness actually
// receives — the artifact-contract half (the .caesar/in file) has its own
// tests in gatenote_runner_test and is untouched here.
func TestRequestChanges_TheNoteReachesTheAssembledInstructions(t *testing.T) {
	rec := &bindingRecorder{inner: fake.NewExecutor()}
	f, svc, run := gatedRunWithExecutor(t, gatedConveyorDoc(), rec)

	const note = "Use three sentences, mention the Rubicon."
	if _, err := svc.DecideGate(context.Background(), principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: note,
	}); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, svc, run.ID, runs.RunWaiting)

	// Attempt 1 is generation 1's; the revision's target runs as attempt 2.
	first := waitForRequest(t, rec, run.ID, "impl", 1)
	if strings.Contains(first.Instructions, "Requested changes") {
		t.Fatalf("generation 1 grew a requested-changes section:\n%s", first.Instructions)
	}

	req := waitForRequest(t, rec, run.ID, "impl", 2)
	// The node's own instructions still OPEN the prompt — appended, never
	// substituted. Compare against the document's own text rather than a
	// literal: gatedConveyorDoc() owns it, and a hard-coded prefix here goes
	// stale the first time that fixture is edited.
	if !strings.HasPrefix(req.Instructions, implInstructionsOf(gatedConveyorDoc())) {
		t.Fatalf("instructions no longer open with the node's own text:\n%s", req.Instructions)
	}
	want := "\n\n## Requested changes (a reviewer rejected the previous attempt)\n\n" + note + "\n"
	if !strings.Contains(req.Instructions, want) {
		t.Fatalf("assembled instructions missing the requested-changes section:\n%s", req.Instructions)
	}
}
```

The attempt-1 assertion is the negative control and it is free here — the same recorder already holds it, so no second fixture is needed to prove a generation-1 attempt grows nothing. Add the small `implInstructionsOf` helper beside the file's other fixture readers if one does not exist.

Two constraints on the marker text, stated here because the test pins them: it must contain **no line beginning `caesar-fake:`** (`fake.parseDirective` scans instructions line-by-line for that prefix — 12a pre-flight 16), and the note is inserted VERBATIM (no escaping) because it is length-bounded and operator-authored.

- [ ] **Step 2: Run it and watch it fail**

Run: `cd backend && go test -tags integration ./internal/runs/ -run TestRequestChanges_TheNoteReachesTheAssembledInstructions -count=1 -v`
Expected: FAIL on the generation-2 assertion — the section is absent — while the attempt-1 negative control passes. (Integration tags and a testcontainer Postgres: this suite is not the unit lane, and `buildRequest` reads its seeds from the database.)

- [ ] **Step 3: Implement the injection**

In `buildRequest`, inside the existing seed loop (or immediately after it), extract the note and append it. The injection point is AFTER the `Instructions:` field is first assembled and BEFORE `collapsePatchInputs` (which must stay the last statement — its comment says why):

```go
	for _, row := range seeds {
		payload, err := unmarshalMap(row.Payload)
		if err != nil {
			return ExecutionRequest{}, err
		}
		req.Inputs = append(req.Inputs, ArtifactInput{ID: row.ID, Type: row.Type, Payload: payload})
		// The revision loop's only new input reaches the PROMPT, not just the
		// staging dir: the note is appended — never substituted, same rule as
		// OutputShapeInstruction above — in a marked section, so the model
		// reads its original task first and the reviewer's words after.
		// The staged .caesar/in file stays; it is the artifact contract.
		if row.Type == ArtifactKindGateNote {
			if note, ok := payload["note"].(string); ok && note != "" {
				req.Instructions += "\n\n## Requested changes (a reviewer rejected the previous attempt)\n\n" + note + "\n"
			}
		}
	}
```

- [ ] **Step 4: Run the tests**

Run: `cd backend && go test -tags integration ./internal/runs/ -run TestRequestChanges -count=1 -v`
Expected: PASS, including every pre-existing revisions case — `ASecondRoundSeesOnlyItsOwnNote` in particular, which now also proves the injected text is the CURRENT round's note and not a concatenation of both.

- [ ] **Step 5: Red-then-revert proof**

Delete the four injection lines (the `if row.Type == ArtifactKindGateNote` block), run Step 4 again, record the FAIL output, restore. This is the acceptance proof the spec names (§2.1): the test red-dens on exactly the deleted behaviour. Also run the revision acceptance lane end-to-end: `go test -tags integration ./internal/runs/ -run Revision -count=1` — the existing `gatenote_runner_test` staging assertions stay green (the artifact-contract half is untouched).

- [ ] **Step 6: Commit**

```bash
git add backend/internal/runs/orchestrator.go backend/internal/runs/revisions_integration_test.go
git commit -m "runs: append a seeded gate note to the harness prompt in buildRequest"
```

---

### Task 2: The runs-list index migration

Parked by the 12a final review with no stated shape (pre-flight 16). The shape follows `ListRunsPage`'s keyset exactly: `ORDER BY r.created_at DESC, r.id DESC` with a row-value cursor predicate on the same pair. The pulled-in filters (Task 15) all ride the `tasks` join and do not change this runs-side index.

**Files:**
- Modify: `backend/internal/platform/pg/schema.sql`
- Create (generated): `backend/internal/platform/pg/migrations/<ts>_runs_list_index.sql`

- [ ] **Step 1: Add the index to the desired-state schema**

In `schema.sql`, with the other `runs` indexes (~line 233):

```sql
-- Serves ListRunsPage's keyset scan: ORDER BY created_at DESC, id DESC with a
-- row-value cursor on the same pair. status and task_id are selective runs-side
-- equality filters and workflow/repo ride the tasks join; none of them earn a
-- slot in a keyset index.
CREATE INDEX runs_created_id_idx ON runs (created_at DESC, id DESC);
```

- [ ] **Step 2: Author the migration**

Run: `cd backend && task db:new -- runs_list_index`
Expected: a new goose file whose Up is `CREATE INDEX "runs_created_id_idx" ON "public"."runs" ("created_at" DESC, "id" DESC);` with the matching auto-generated Down, and an updated `atlas.sum`.

- [ ] **Step 3: Prove it applies and is used**

Run: `task backend:test:integration` (the migration runner applies the chain from scratch in testcontainers — a broken migration fails here). Then prove the planner uses it, in psql against a dev database seeded with a few runs (`task db:up`, `task backend:run` once, or reuse an integration-test container):

```sql
EXPLAIN SELECT r.id FROM runs r ORDER BY r.created_at DESC, r.id DESC LIMIT 50;
-- Expected: Index Scan (or Index Only Scan) using runs_created_id_idx
```

Record the plan output in the commit message body if it surprised you; otherwise proceed.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/platform/pg/schema.sql backend/internal/platform/pg/migrations/
git commit -m "pg: index runs (created_at DESC, id DESC) for the list keyset"
```

---

### Task 3: The go-mod-tidy lane

No tidy check exists anywhere (pre-flight 17). Go 1.26's `go mod tidy -diff` prints the diff and exits non-zero on drift without writing — exactly a CI lane's shape. **Verify that flag against the installed toolchain first** (the pinned-surface rule): `cd backend && go mod tidy -diff; echo $?` — expect exit 0 and no output on today's tidy module. If the flag is absent in the installed Go, fall back to the copy-compare shape (`cp go.mod go.sum` to temp, run tidy, `git diff --exit-code go.mod go.sum`, restore) and record which shape shipped.

**Files:**
- Modify: `Taskfile.yml`, `.github/workflows/ci.yml`, `scripts/gate` (same commit — the gate asserts resolved commands)

- [ ] **Step 1: Add the task**

```yaml
  backend:tidy:check:
    desc: "Fail if go.mod/go.sum drift from go mod tidy"
    dir: backend
    cmds:
      - go mod tidy -diff
```

- [ ] **Step 2: Wire it into backend:ci and the gate**

Add `- task: backend:tidy:check` to `backend:ci`'s cmds (after `fmt:check`, before `vet`). Then update `scripts/gate`'s expected-lane list and resolved-command assertions to include the new lane — read the script's assertion block first; it asserts character-for-character.

- [ ] **Step 3: Prove the lane can fail**

Add a junk require to `backend/go.mod` (`require example.com/nonexistent v1.0.0` is wrong-shaped — instead add a real-but-unused module: `cd backend && go get github.com/google/renameio@latest`), run `task backend:tidy:check`, expect FAIL (diff printed, non-zero). Revert (`git checkout backend/go.mod backend/go.sum`), run again, expect PASS. Record both outputs.

- [ ] **Step 4: Run the full gate and commit**

`task backend:ci` green, then:

```bash
git add Taskfile.yml scripts/gate .github/workflows/ci.yml
git commit -m "ci: a go-mod-tidy drift lane in backend:ci and the gate"
```

---

### Task 4: git in the runtime image, and CI executes a publish from inside it

The published artifact cannot publish: `git-publish` shells out to `git` in-process and the shipped distroless image has none. The fix promotes the Dockerfile's PROVEN shape — the `git` stage (alpine:3.22 + git + caesar, USER 65532), which is what the only working deploy (`caesar-local` = `caesar-git:dev`) already runs — to be the final runtime stage. Copying git into distroless is a musl-vs-glibc mismatch that would have to be measured; there is no reason to when the working shape exists in the same file (pre-flight 18).

**Files:**
- Modify: `Dockerfile` (the final stage, lines ~74-94)
- Modify: `.github/workflows/ci.yml` (`image-serves-ui`)

- [ ] **Step 1: Restructure the final stage**

Replace the distroless final stage with the git-stage shape, and keep the OLD `git` stage's target name working (Taskfile's `image:build` builds `--target git` as `caesar-git:dev`):

```dockerfile
# The runtime image. alpine + a real git rather than distroless: git-publish
# shells out to git IN-PROCESS, and a runtime that cannot publish is the
# deploy's first blocking finding (12a checkpoint). This is the same shape the
# `git` target has always built — promoted to final, so a bare `docker build .`
# now produces an image that can publish.
FROM alpine:3.22
RUN apk add --no-cache git
COPY --from=build /out/caesar /usr/local/bin/caesar
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/caesar"]
CMD ["serve", "-config", "/etc/caesar/config.json"]
```

**POSITION MATTERS AND IS THE TRAP HERE.** Docker's implicit final stage is the LAST one in the file, and today's order is `build` → `git` (:74) → `fakecli` (:85) → `distroless` (:91). "Drop the old separate stage and label this one `AS git`" is ambiguous, and the literal reading — delete :91 and leave the git stage where it is — makes **`fakecli` the final stage**, so a bare `docker build .` ships the scripted fake harness as the control plane. The instruction is therefore precise: **delete the distroless stage and move the git stage to the END of the file**, keeping its `AS git` label so `image:build --target git` still resolves. `fakecli` stays where it is, before it.

Two more edits that must ride this commit:
- **The git stage's own doc comment becomes FALSE** (`Dockerfile:71-73`: "It is not the last stage, so a bare `docker build .` still produces the control-plane runtime image"). Rewrite it to say what is now true — this IS the runtime image, and why (git-publish shells out in-process; distroless has no git and no `sh`). The plan rewrites two other comments the moment their mechanism changes (Tasks 8, 12); this is the third and it is the same rule.
- `CMD ["serve", "-config", "/etc/caesar/config.json"]` moves onto it — the current git stage has an ENTRYPOINT but no CMD, because it was never the thing that boots.

Verify `task image:build` still produces all three tags, and that `docker build .` with no `--target` produces an image whose `git version` answers.

- [ ] **Step 2: Extend image-serves-ui with the in-image assertions**

In the job, after the four existing assertions (the container is already running as `candidate`):

```bash
# 5) The image can run git at all — the runtime regression this plan fixes.
docker exec candidate git version || fail "no git in the runtime image"

# 6) A publish-shaped git flow works INSIDE the image: init a bare repo, clone,
#    commit, push. UNDER CAESAR'S OWN GIT ENVIRONMENT, not a convenient one —
#    gitops.go:84-91 constructs every git invocation with HOME set to the work
#    dir, GIT_CONFIG_GLOBAL=/dev/null, GIT_CONFIG_NOSYSTEM=1,
#    GIT_TERMINAL_PROMPT=0 and the author/committer identity in the env. An
#    assertion using `git -c user.email=…` instead would prove a shape caesar
#    never executes, and the interesting failures here (no passwd entry for uid
#    65532, no HOME, a config git refuses to read) live precisely in that env.
docker exec candidate sh -c '
  set -e
  export HOME=/tmp/pub-home
  export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0
  export GIT_AUTHOR_NAME=caesar GIT_AUTHOR_EMAIL=caesar@invalid
  export GIT_COMMITTER_NAME=caesar GIT_COMMITTER_EMAIL=caesar@invalid
  mkdir -p "$HOME"
  git init --bare /tmp/pub-remote.git
  git clone /tmp/pub-remote.git /tmp/pub-work
  cd /tmp/pub-work
  git commit --allow-empty -m probe
  git push origin HEAD:refs/heads/probe
' || fail "in-image git publish flow failed under caesar's git env"
```

Two things about this snippet, both learned the hard way in this very job (its own comments record the lessons): it routes through the existing **`fail()` helper** rather than `echo; exit 1`, so a red run carries the container logs with it like every other assertion here; and it runs under the step's existing `set -euo pipefail`, where `cmd || fail …` is the shape that actually fails the job — a bare failing command inside a pipeline, or one negated with `!`, does not. `alpine` has `sh`; distroless did not — this assertion is only writable because of Step 1.

- [ ] **Step 3: Prove the assertions fire**

Build the OLD distroless shape locally (`git stash` the Dockerfile change, `docker build -t caesar:probe .`, unstash), run assertion 5 against it, record the failure. Then build the new shape and run both assertions green. `task image:build && task backend:test:runner` must also stay green (the runner lane consumes `caesar-git:dev`).

- [ ] **Step 4: Commit**

```bash
git add Dockerfile .github/workflows/ci.yml
git commit -m "image: the runtime can publish — alpine+git final stage, and CI proves it from inside the image"
```

---

### Task 5: `cannot_decide_reason` learns `deadline_expired`

`decidability` (`internal/runs/api/api.go:495-528`) returns `not_approver` / `self_approval_forbidden` / `evidence_missing` in decideGate's own order. The deadline refusal sits SECOND in that order (pre-flight 5), and the comparison must be DB-clock-to-DB-clock: the evidence read returns `DBNow` alongside its rows; `s.clock.Now()` on one side of a `wall_deadline` comparison is forbidden by `DBNow`'s own contract.

**Files:**
- Modify: `backend/internal/runs/gates.go` (the waiting-gates read path), `gateevidence.go` (if the evidence resolver is the right carrier), `api/api.go` (`decidability` + both call paths), `api/dto.go` (the `CannotDecideReason` doc comment)
- Modify: `backend/internal/runs/api/gateevidence_integration_test.go`
- Modify (web): `src/gates/gate-controls.tsx` (the suppression-reason copy), its testkit

**Interfaces:**
- Consumes: `GateEvidence.WallDeadline` (already in the projection); `q.DBNow(ctx)`.
- Produces: `decidability(canApprove bool, actorID, requesterID uuid.UUID, allowSelf bool, haveExamined bool, wallDeadline, now time.Time) (bool, string)` — two new trailing params; the string value `"deadline_expired"`.

- [ ] **Step 1: Check the waiting-gates predicate FIRST**

Read `ListWaitingGates` / `ListWaitingGatesByProjects` (`store/queries.sql:663+`): do they exclude runs whose `wall_deadline <= now()`? Record the answer in the commit message. If they exclude, the reason is only ever OBSERVABLE on run detail's gate phase (whose `Evidence` rides `PhaseResponse`) — the integration test then drives that path; the inbox path gets a test asserting the expired gate is absent (which is itself the honest inbox behaviour). If they do NOT exclude, both paths get the reason. Do not guess; read.

- [ ] **Step 2: Write the failing tests**

In `gateevidence_integration_test.go`, following the file's fixture pattern (a waiting gate on a real run):

```go
func TestDecidabilityDeadlineExpired(t *testing.T) {
	// Fixture: a waiting gate whose run's wall_deadline is in the past.
	// runs.wall_deadline is written once at insert (gates.go:381's comment),
	// so mint the run with a deadline barely in the future and let the
	// fixture's DBNow pass it — or insert with a past deadline through the
	// service's own creation path if it permits one. Whichever the fixture
	// allows, the DECIDING clock is the DB's.
	ev := waitingGateEvidencePastDeadline(t)
	canDecide, reason := decidabilityUnderTest(t, ev /* caller holds approve, is not requester */)
	if canDecide || reason != "deadline_expired" {
		t.Fatalf("got (%v, %q), want (false, deadline_expired)", canDecide, reason)
	}
}

func TestDecidabilityOrderDeadlineBeforeSelfApproval(t *testing.T) {
	// A requester past the deadline gets deadline_expired only if they can
	// approve at all: not_approver still wins (decideGate's grant check is
	// first). A self-approving approver past the deadline gets
	// deadline_expired, NOT self_approval_forbidden — decideGate checks the
	// deadline at gates.go:176, before self-approval at :186.
	ev := waitingGateEvidencePastDeadline(t)
	_, reason := decidabilityAsSelfApprover(t, ev)
	if reason != "deadline_expired" {
		t.Fatalf("order violated: got %q, want deadline_expired before self_approval_forbidden", reason)
	}
}
```

- [ ] **Step 3: Run them, watch them fail** — `go test -tags integration ./internal/runs/api/ -run TestDecidability -count=1`. Expected: FAIL (reason is `self_approval_forbidden` / decidability has no deadline arm).

- [ ] **Step 4: Implement**

Thread a `now` from the read: the service call that resolves evidence executes `q.DBNow(ctx)` once per request (not per row) and hands it to the DTO layer with the rows. Then:

```go
func decidability(canApprove bool, actorID, requesterID uuid.UUID, allowSelf, haveExamined bool,
	wallDeadline, now time.Time,
) (bool, string) {
	if !canApprove {
		return false, "not_approver"
	}
	// SECOND, matching decideGate: it refuses on the deadline (gates.go:176)
	// before self-approval (:186) and before resolving the examined artifact
	// (:192) — so this names the refusal the caller would ACTUALLY get.
	// Both sides of the comparison are DB timestamps: `now` is q.DBNow from
	// the same read that carried wall_deadline; the app clock never touches a
	// deciding predicate (DBNow's contract, store/queries.sql:114-119).
	if !wallDeadline.After(now) {
		return false, "deadline_expired"
	}
	if actorID == requesterID && !allowSelf {
		return false, "self_approval_forbidden"
	}
	if !haveExamined {
		return false, "evidence_missing"
	}
	return true, ""
}
```

Update `dto.go`'s `CannotDecideReason` doc comment to enumerate all four values in order. Both `decidability` call sites (`gateViewer` in dto.go and any run-detail path) receive the pair.

- [ ] **Step 5: The web half** — `gate-controls.tsx` maps the new reason to copy: "This run's wall deadline has passed, so its gates can no longer be decided." — which is the sentence the file's existing client-clock check ALREADY renders, so the two paths share one `DEADLINE_PASSED` constant rather than carrying two near-identical wordings for one fact (corrected in place after execution: this step originally dictated a second, slightly different sentence, and the reviewer's consolidation was upheld by the owner). Rendered through the same all-or-nothing suppression the other reasons use. Add the case to the decision testkit. Run `cd web && ../scripts/with-node ./node_modules/.bin/vitest run src/gates`.

- [ ] **Step 6: Full check + mutation proof + commit**

Revert the deadline arm, watch Step 3's tests fail, restore. `task backend:ci && task contracts:check && task web:ci` (the DTO change is doc-only — confirm `contracts:gen` produces NO schema diff, since `cannot_decide_reason` was already a required string; if it does diff, commit the regenerated pair).

```bash
git add backend/internal/runs/ web/src/gates/
git commit -m "runs: cannot_decide_reason names an expired wall deadline, second in decideGate's own order"
```

---

### Task 6: Honest failure display

One rule fixes three findings: **a phase card's status line derives from the phase's STATUS first and its outcome class second — never from a bare counter** (spec §2.2). No DTO change (pre-flight 3). The derivation is a pure function; `runs.$runId.tsx`'s `PhaseSummary` (one component, used by rail and timeline — keep it one) renders its result.

**The ordering matters and is the whole task.** `dead` is terminal — nothing follows that row — while `failed` always has a successor attempt (pre-flight 3a). A formatter that consults only the outcome class renders `attempt 3/3` for an exhausted node (whose phase outcome stays *transient*; only the run's is rewritten to `attempts_exhausted`) and "resumable" for a dead row whose ordinal outran the cap. Status is therefore checked BEFORE the counter, and the counter is reachable only from a row that can still advance.

**Files:**
- Create: `web/src/runs/phase-status.ts`, `web/src/runs/phase-status.test.ts`
- Modify: `web/src/routes/_app/runs.$runId.tsx` (`PhaseSummary`, ~line 165)

**Interfaces:**
- Consumes: `Phase` (the generated `ApiPhaseResponse`): `status`, `attempt`, `max_attempts`, `outcome?: {class, code, message, retry_at?}`. Read the status vocabulary from the generated type, not from this plan — `PhaseStatus`'s eight constants live in `runs/status.go:52-66` (`pending`, `running`, `waiting_decision`, `reconciling`, `succeeded`, `failed`, `dead`, `superseded`).
- Produces: `phaseStatusLine(phase: Phase, priorAttempt: Phase | null): string` — `priorAttempt` is the same node's previous attempt row (for the resumable reason), findable in `run.phases`.

- [ ] **Step 1: Write the failing tests**

```ts
import { describe, expect, it } from 'vitest'
import { phaseStatusLine } from './phase-status'

const base = { node_id: 'impl', generation: 1, status: 'running', attempt: 1, max_attempts: 3 }

describe('phaseStatusLine', () => {
  it('renders the counter while transient retry budget remains', () => {
    expect(phaseStatusLine({ ...base, attempt: 2 }, null)).toBe('attempt 2/3')
  })

  // THE ROUND-1 BLOCKER, pinned. An exhausted node's phase row keeps its
  // TRANSIENT outcome (only the RUN's becomes attempts_exhausted —
  // orchestrator.go:1652), and its status is `dead` because nothing follows it
  // (`failed` rows always have successors — orchestrator.go:1573). A formatter
  // that reads the outcome class first prints "attempt 3/3" here, which is
  // exactly the lying counter this task exists to remove.
  it('never prints a counter on a dead row, even with a transient outcome at the cap', () => {
    const p = {
      ...base, status: 'dead', attempt: 3,
      outcome: { class: 'transient', code: 'harness_error', message: 'exit 1' },
    }
    const line = phaseStatusLine(p, null)
    expect(line).not.toMatch(/attempt \d/)
    expect(line).not.toContain('resumable')
    expect(line).toContain('harness_error')  // the cause survives; the promise of a retry does not
  })

  // Same row shape, ordinal PAST the cap (earlier uncharged waits pushed it
  // there). "resumable" is a promise of a successor; a dead row has none.
  it('never says resumable on a dead row whose ordinal outran the cap', () => {
    const p = {
      ...base, status: 'dead', attempt: 5,
      outcome: { class: 'transient', code: 'rate_limited', message: 'cap window' },
    }
    const line = phaseStatusLine(p, null)
    expect(line).not.toContain('resumable')
    expect(line).not.toContain('5/3')
  })

  // `failed` is the mirror case and proves the discriminator is STATUS, not
  // just "is there an outcome": same class, same code, opposite reading.
  it('a failed row with the same outcome still reads as a retry in progress', () => {
    const p = {
      ...base, status: 'failed', attempt: 2,
      outcome: { class: 'transient', code: 'harness_error', message: 'exit 1' },
    }
    expect(phaseStatusLine(p, null)).toBe('attempt 2/3')
  })

  it('names the terminal code on a permanent outcome, never a counter', () => {
    const p = {
      ...base,
      status: 'dead',
      outcome: { class: 'permanent', code: 'auth_expired', message: 'credential claude-sub: token expired' },
    }
    const line = phaseStatusLine(p, null)
    expect(line).toContain('auth_expired')
    expect(line).not.toMatch(/attempt \d/) // "1/3" on a permanent failure promises retries that will never come
  })

  it('says resumable — not chargeable past the cap, with the wait reason from the prior attempt', () => {
    // The prior row is `failed`, not `dead`: it HAS a successor (this is the
    // retry branch's invariant, orchestrator.go:1573). A `dead` prior would
    // mean the node ended and there is no attempt 4 to describe.
    const prior = {
      ...base, attempt: 3, status: 'failed',
      outcome: { class: 'transient', code: 'rate_limited', message: 'cap window', retry_at: '2026-08-26T12:00:00Z' },
    }
    const p = { ...base, attempt: 4, status: 'pending' }
    const line = phaseStatusLine(p, prior)
    expect(line).toContain('resumable')
    expect(line).toContain('not chargeable')
    expect(line).toContain('rate_limited')
    expect(line).not.toContain('4/3')
  })

  // The two statuses a three-branch function silently mishandles. A gate has
  // no retry clock at all (status.go:55-60) and a succeeded phase has no
  // outcome (orchestrator.go:1099) — both fall through to "attempt 1/3"
  // unless the switch is exhaustive.
  it('a gate awaiting a human says so, never a counter', () => {
    expect(phaseStatusLine({ ...base, status: 'waiting_decision', attempt: 1 }, null)).toBe('awaiting a decision')
  })

  it('a succeeded phase says succeeded, not a counter', () => {
    // No outcome at all on this path — the success writer passes nil.
    expect(phaseStatusLine({ ...base, status: 'succeeded', attempt: 1, outcome: undefined }, null)).toBe('succeeded')
  })

  it('every status in the generated vocabulary produces a line', () => {
    // Exhaustiveness, asserted rather than assumed: a status added to the Go
    // side and not to the switch must fail here, not render a wrong counter.
    for (const status of ['pending', 'running', 'waiting_decision', 'reconciling',
                          'succeeded', 'failed', 'dead', 'superseded']) {
      expect(phaseStatusLine({ ...base, status }, null)).toBeTruthy()
    }
  })

  it('policy_violation renders its code like permanent (final is final)', () => {
    const p = { ...base, status: 'dead', outcome: { class: 'policy_violation', code: 'secret_in_artifact', message: 'x' } }
    expect(phaseStatusLine(p, null)).toContain('secret_in_artifact')
  })
})
```

- [ ] **Step 2: Run, watch fail** — `cd web && ../scripts/with-node ./node_modules/.bin/vitest run src/runs/phase-status`

- [ ] **Step 3: Implement**

```ts
import type { Phase } from './run-query'

/**
 * §2.2's one rule: the status line derives from what will HAPPEN NEXT, not
 * from a bare counter. The engine facts this encodes (verified, pre-flight 3):
 *
 *  - STATUS FIRST. `dead` means nothing follows this row; `failed` means a
 *    successor attempt was inserted in the same transaction
 *    (orchestrator.go:1573 — "failed rows always have successors"). Only a row
 *    that can still advance may show a counter or the word "resumable", both
 *    of which are promises about a next attempt.
 *  - An exhausted node keeps its TRANSIENT phase outcome; only the RUN's is
 *    rewritten to attempts_exhausted (orchestrator.go:1652). So class alone
 *    cannot tell exhausted from mid-retry, and a class-first formatter printed
 *    "attempt 3/3" on the commonest failure there is.
 *  - attempt outruns max_attempts ONLY via uncharged waits, so on a LIVE row
 *    `attempt > max_attempts` means "resumable — not chargeable".
 *  - auth_expired and every other permanent code lives on the PHASE outcome
 *    (the run merely goes `failed`), so this reads the phase's outcome, never
 *    the run's.
 */
export function phaseStatusLine(phase: Phase, priorAttempt: Phase | null): string {
  const out = phase.outcome
  // EXHAUSTIVE over all eight statuses (status.go:52-66). A default arm that
  // falls through to the counter is how the counter reappears where it lies:
  // a gate awaiting a human has no worker, no lease and no retry clock
  // (status.go:55-60 says so), and a succeeded phase has no outcome at all
  // (the success path writes finishPhase(…, PhaseSucceeded, …, nil) —
  // orchestrator.go:1099), so both would render "attempt 1/3" under a
  // three-branch function. Every arm below is pinned by its own test.
  switch (phase.status) {
    case 'succeeded':
      return 'succeeded'
    case 'waiting_decision':
      // The only status whose next event is a PERSON. Never a counter.
      return 'awaiting a decision'
    case 'superseded':
      return 'superseded by a newer generation'
    case 'dead':
      // Over. Report the cause and never a counter: there is no next attempt
      // to count toward. `attempts_exhausted` is a RUN outcome and
      // deliberately not reachable here — the phase carries the outcome that
      // actually ended it.
      if (!out) return 'ended without an outcome'
      return out.class === 'transient' ? `${out.code} — no attempts remain` : out.code
    case 'pending':
    case 'running':
    case 'reconciling':
    case 'failed':
      break // a row that can still advance — the counter is honest here
  }
  if (out && out.class !== 'transient') {
    // Permanent or policy_violation on a live row: the code IS the status.
    return out.code
  }
  if (phase.attempt > phase.max_attempts) {
    const reason = priorAttempt?.outcome?.code
    return reason ? `resumable — not chargeable (${reason})` : 'resumable — not chargeable'
  }
  return `attempt ${phase.attempt}/${phase.max_attempts}`
}
```

- [ ] **Step 4: Wire it into `PhaseSummary`** — replace the literal `attempt {phase.attempt}/{phase.max_attempts}` span with `{phaseStatusLine(phase, priorAttemptOf(run, phase))}` where `priorAttemptOf` scans `run.phases` for the same `node_id` with `attempt === phase.attempt - 1` (add it beside `newestPhaseOf` in `phase-spine.ts`). The route test gets one assertion per branch (permanent code shown; no counter on permanent; resumable line) using the existing fixture pattern.

- [ ] **Step 5: Run the suites, prove the mutants** — `vitest run src/runs src/routes`. FIVE mutations, each recorded, each must redden a DIFFERENT test: (i) change `out.class !== 'transient'` to `=== 'permanent'` — the policy_violation case fails; (ii) delete the `case 'dead'` arm — the two dead-row cases fail (if they stay green the arm is not load-bearing and the fixtures are wrong); (iii) move `'failed'` into the `dead` arm — the "failed row still reads as a retry in progress" case fails; (iv) delete the `waiting_decision` arm — the gate case fails; (v) delete the `succeeded` arm — the succeeded case fails. Restore after each.

- [ ] **Step 6: Commit**

```bash
git add web/src/runs/phase-status.ts web/src/runs/phase-status.test.ts web/src/runs/phase-spine.ts web/src/routes/_app/runs.\$runId.tsx web/src/routes/_app/runs.\$runId.test.tsx
git commit -m "web: phase status lines derive from outcome class — auth_expired gets a face, resumable stops reading as a bug"
```

---

### Task 7: An identical revision is visible

Finding 6: request-changes can silently loop an operator into approving work that ignored their review. Byte-equality of the examined patch's `diff` against the previous generation's IS the hash comparison (pre-flight 4) — zero contract change.

**Files:**
- Create: `web/src/gates/identical-revision.ts`, `web/src/gates/identical-revision.test.ts`
- Modify: `web/src/approvals/approval-surface.tsx`

**Interfaces:**
- Consumes: run detail's `artifacts` array (`ArtifactResponse[]`: `id`, `node_id`, `type`, `attempt`, `generation`); the immutable `artifactQueryOptions(runId, artifactId)`; the current evidence's `examined_artifact_id`.
- Produces: `previousExaminedArtifactId(artifacts, examined): string | null` (pure — the newest `patch` artifact of the SAME node with `generation === examined.generation - 1`); a banner in the approval surface when the two payloads' `diff` strings are equal.

**PATCH GATES ONLY, and the guard is not defensive.** A gate may examine a PLAN — the validator accepts `plan→gate (plan)` and pre-flight 13 confirms it by running the real `Validate`. A plan payload has no `diff` field, so a comparison written as `current.payload.diff === prior.payload.diff` reads `undefined === undefined` and is **true for every plan revision ever**, banner and all, telling the operator their reviewed changes were ignored when they were not. So: `previousExaminedArtifactId` requires `examined.type === 'patch'` and returns null otherwise, and the surface compares only when both `diff` values are strings. Byte-equality of plan CONTENT is a different feature with a different payload shape; it is not in this task and the code says so.

- [ ] **Step 1: Write the failing tests**

```ts
import { describe, expect, it } from 'vitest'
import { previousExaminedArtifactId } from './identical-revision'

const art = (id: string, gen: number, attempt: number, type = 'patch', node = 'impl') =>
  ({ id, node_id: node, type, generation: gen, attempt, parent_ids: [], created_at: '' })

describe('previousExaminedArtifactId', () => {
  it('finds the newest prior-generation patch of the same node', () => {
    const arts = [art('a', 1, 1), art('b', 1, 2), art('c', 2, 3)]
    expect(previousExaminedArtifactId(arts, art('c', 2, 3))).toBe('b') // newest attempt of gen 1
  })
  it('returns null on generation 1 (nothing to compare)', () => {
    expect(previousExaminedArtifactId([art('a', 1, 1)], art('a', 1, 1))).toBeNull()
  })
  it('ignores other nodes and other types', () => {
    const arts = [art('x', 1, 1, 'plan'), art('y', 1, 1, 'patch', 'other'), art('c', 2, 2)]
    expect(previousExaminedArtifactId(arts, art('c', 2, 2))).toBeNull()
  })

  // A gate CAN examine a plan (validate.go accepts plan→gate; pre-flight 13
  // proved it against the real validator). Plan payloads carry no `diff`, so
  // without this guard the surface compares undefined to undefined, finds them
  // equal, and accuses the operator's revision of changing nothing.
  it('refuses to answer for a plan-examining gate', () => {
    const arts = [art('p1', 1, 1, 'plan'), art('p2', 2, 1, 'plan')]
    expect(previousExaminedArtifactId(arts, art('p2', 2, 1, 'plan'))).toBeNull()
  })
})
```

And three surface tests in the approvals suite: render with MSW handlers where BOTH artifact payloads carry the same base64 `diff` and assert the banner (`getByText(/byte-identical/i)`) appears; a second case with differing diffs asserting it does not; and **a plan-examining gate at generation 2** whose two plan payloads have no `diff` field at all — the banner must NOT appear, which is the case a naive equality passes and a person acts on.

- [ ] **Step 2: Run, watch fail.**

- [ ] **Step 3: Implement the pure half**

```ts
type Artifact = { id: string; node_id: string; type: string; generation: number; attempt: number }

export function previousExaminedArtifactId(artifacts: readonly Artifact[], examined: Artifact): string | null {
  // Patch gates only. A plan-examining gate's payloads have no `diff`, and a
  // caller comparing two undefineds would find every plan revision identical.
  if (examined.type !== 'patch') return null
  if (examined.generation <= 1) return null
  let best: Artifact | null = null
  for (const a of artifacts) {
    if (a.node_id !== examined.node_id || a.type !== examined.type) continue
    if (a.generation !== examined.generation - 1) continue
    if (best === null || a.attempt > best.attempt) best = a
  }
  return best?.id ?? null
}
```

- [ ] **Step 4: The surface half** — in `approval-surface.tsx`, where the examined artifact is already fetched: compute the prior id from the run's artifacts (the route already ensures `runQueryOptions`); when non-null, mount a second `useQuery(artifactQueryOptions(runId, priorId))` (immutable, `staleTime: Infinity` — cost is one fetch, only on revision gates); when both payloads are present and **both `diff` values are strings** and `current.payload.diff === prior.payload.diff` (compare the base64 strings — decode nothing; the `typeof` guard is what stops two absent fields from comparing equal), render prominently ABOVE the accordion:

```tsx
<div role="status" className="border-warning text-text bg-surface mb-3 border px-3 py-2 text-sm">
  This revision is byte-identical to what was already reviewed — the previous
  request-changes note was not acted on.
</div>
```

- [ ] **Step 5: Suites + mutants** — `vitest run src/gates src/approvals`. Two mutations: (i) compare `diff.length` instead of `diff` — the differing-diff case must still pass and the identical case must too, so ALSO add a same-length-different-bytes fixture that catches it; (ii) drop the `examined.type !== 'patch'` guard AND the `typeof` string check — the plan-gate case must go red. Prove both, restore.

- [ ] **Step 6: Commit**

```bash
git add web/src/gates/identical-revision.ts web/src/gates/identical-revision.test.ts web/src/approvals/
git commit -m "web: a byte-identical revision is named on the gate screen"
```

---

### Task 8: The artifact view — a route this plan BUILDS, then links

`PhaseArtifacts`' own comment says it: "NOTHING HERE LINKS ANYWHERE… 12a adds no route that renders one" (`runs.$runId.tsx:337-373`). 12b adds `/runs/$runId/artifacts/$artifactId`, linked per artifact **row** (a phase can hold several artifacts — the phase card is not the link), with a per-kind rendering contract rather than one generic JSON dump. The seven kinds (`runs/compile.go`): `patch` → the diff renderer the approval surface already has; `plan` / `review_verdict` / `gate_note` → their typed payload views; `publication` → the PR reference; `log_ref` / `failure_snapshot` → metadata-only presentations (their payloads are not displayable content and are not fetched as such).

**Files:**
- Create: `web/src/routes/_app/runs.$runId.artifacts.$artifactId.tsx`, `web/src/runs/artifact-kind-views.tsx`, `web/src/runs/artifact-kind-views.test.tsx`
- Modify: `web/src/routes/_app/runs.$runId.tsx` (`PhaseArtifacts` rows become links; ITS COMMENT IS REWRITTEN — pre-flight/spec both demand the same-commit rewrite)
- Regenerate: `web/src/routeTree.gen.ts`

**Interfaces:**
- Consumes: `GET /runs/{id}/artifacts/{artifact_id}` (`ArtifactPayloadResponse` — all nine fields required; `patch.diff` is base64 by contract, decode once, never round-trip); `decodePatch` from `src/patch/`; the plan/verdict readers in `src/approvals/artifact-query.ts` (`readPatchDiff`/`readPlan`/`readVerdict` — reuse, do not duplicate).
- Produces: the route string `/runs/$runId/artifacts/$artifactId` (compiler-checked) that Task 9's links target; `ArtifactKindView(props: { artifact: ArtifactPayload })`.

- [ ] **Step 1: Write the failing renderer tests**

In `artifact-kind-views.test.tsx` — one fixture per contract-bearing kind, minimum the spec's three (patch, plan, one metadata-only):

```tsx
const payloadOf = (type: string, payload: Record<string, unknown>) => ({
  id: 'a1', run_id: 'r1', node_id: 'impl', attempt: 1, generation: 1,
  type, parent_ids: [], created_at: '2026-08-26T10:00:00Z', payload,
})

describe('ArtifactKindView', () => {
  it('renders a patch through the diff renderer, not as JSON', () => {
    // one-file one-line diff, base64 — reuse the decode-patch test fixture bytes
    render(<ArtifactKindView artifact={payloadOf('patch', { diff: PATCH_B64 })} />)
    expect(screen.getByText('ROME.md')).toBeInTheDocument()          // a file row
    expect(screen.queryByText(/"diff":/)).toBeNull()                  // no raw JSON
  })
  it('renders a plan as its typed steps', () => {
    render(<ArtifactKindView artifact={payloadOf('plan', { content: 'March on Rome', steps: [] })} />)
    expect(screen.getByText('March on Rome')).toBeInTheDocument()
  })
  it('renders log_ref as metadata only and fetches no log bytes', () => {
    render(<ArtifactKindView artifact={payloadOf('log_ref', { path: '/var/log/x' })} />)
    expect(screen.getByText('/var/log/x')).toBeInTheDocument()
    expect(screen.getByText(/not.*displayable|on the runner/i)).toBeInTheDocument() // honest, not empty
  })
  it('renders a gate_note with its note and decision context', () => {
    render(<ArtifactKindView artifact={payloadOf('gate_note', { note: 'Three sentences.', decision: 'request_changes' })} />)
    expect(screen.getByText('Three sentences.')).toBeInTheDocument()
  })
  it('renders publication as the PR reference, linked out', () => {
    render(<ArtifactKindView artifact={payloadOf('publication', { pr_url: 'https://github.com/x/y/pull/2' })} />)
    expect(screen.getByRole('link')).toHaveAttribute('href', 'https://github.com/x/y/pull/2')
  })
})
```

Before writing the publication/plan fixtures, READ the real payload shapes: `runs/publish.go` for what a `publication` artifact stores (the field may be `url`/`pr`/`ref`, not `pr_url` — use the code's key, not this plan's guess), and the 12a §2.3 schemas (`runs/payload.go`, `payload_dto.go`) for plan/review_verdict. Correct the fixtures to match; that correction is expected, not a deviation.

- [ ] **Step 2: Run, watch fail.**

- [ ] **Step 3: Implement `ArtifactKindView`** — a switch over `artifact.type`, each arm a small component: patch → `decodePatch` + the approval surface's file accordion pieces (import the shared components; if any is local to `approval-surface.tsx`, PROMOTE it to `src/patch/` or `src/ui/` per the 2+-importer clamp rather than copy); plan/review_verdict → the existing typed readers' presentation; gate_note → note + decision + acted_by; publication → an external `<a>`; log_ref/failure_snapshot → a definition list of the payload's scalar fields plus the honest sentence ("The content lives on the runner's disk and is not served — §10.4's deferral."). Unknown type → the raw type name + "no renderer for this kind" (a stub never fakes success).

- [ ] **Step 4: The route** — new file, following `runs.$runId.tsx`'s shape:

```tsx
export const Route = createFileRoute('/_app/runs/$runId/artifacts/$artifactId')({
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(artifactQueryOptions(params.runId, params.artifactId)),
  component: ArtifactView,
})
```

`ArtifactView` renders a header (kind badge, `node_id`, `generation`/`attempt`, `created_at`, a back-link to `/runs/$runId?phase=<node_id>`) and `ArtifactKindView`. Run `../scripts/with-node npm run routes:gen` (routeTree regen) and `task web:routes:check`.

- [ ] **Step 5: Link the rows + rewrite the comment** — in `PhaseArtifacts`, each row becomes a `<Link to="/runs/$runId/artifacts/$artifactId" params={…}>`; the "NOTHING HERE LINKS ANYWHERE" comment is REPLACED with one stating the per-row link and why the phase card is not the link (several artifacts per phase). Route test: a run fixture with a patch artifact → clicking the row lands on the artifact view (assert via the rendered file row).

- [ ] **Step 6: Suites + gate + commit**

```bash
git add web/src/runs/ web/src/routes/_app/ web/src/routeTree.gen.ts
git commit -m "web: the artifact view route, per-kind renderers, and artifact rows that finally link"
```

---

### Task 9: Event rows and phase cards that name a destination become links — attempt-precise

Three link classes (spec §2.4): `publish.pr_opened` → the PR URL; `gate.decided` → the gate decision; phases with artifacts → Task 8's route. The gate link must be **attempt-precise**: `?phase=<node>` alone opens the NEWEST gate under that node (the spine collapses per node), not the decision the event records. `gates.go` stamps `attempt` + `generation` on `gate.decided`; the search schema gains the pair.

**Files:**
- Modify: `web/src/routes/_app/runs.$runId.tsx` (search schema, event rows, phase-detail resolution)
- Modify: `web/src/runs/phase-spine.ts` (+ its test)

**Interfaces:**
- Consumes: event payloads (`gate.decided`: `node_id`, `generation`, `attempt`, `decision`; `publish.pr_opened`: the PR URL — read `runs/publish.go`/`events.go` for the exact key, as in Task 8); pre-flight 20 (search params must re-narrow at read sites).
- Produces: the search shape `{ phase?: string; generation?: number; attempt?: number }`; `phaseForSelector(run, nodeId, generation?, attempt?): Phase | null` in `phase-spine.ts` — Task 18's history rows link with the same triple.

- [ ] **Step 1: Failing spine test**

```ts
it('resolves a targeted generation/attempt, bypassing the newest-per-node collapse', () => {
  const run = withPhases([
    phase('gate', { generation: 1, attempt: 1, status: 'dead' }),      // decided: request_changes
    phase('gate', { generation: 2, attempt: 2, status: 'succeeded' }), // decided: approve
  ])
  expect(phaseForSelector(run, 'gate', 1, 1)?.attempt).toBe(1)
  expect(phaseForSelector(run, 'gate', 2, 2)?.attempt).toBe(2)
  expect(phaseForSelector(run, 'gate', undefined, undefined)?.attempt).toBe(2) // no selector → newest
  expect(phaseForSelector(run, 'gate', 9, 9)).toBeNull()                        // miss → null, render honest absence
})
```

- [ ] **Step 2: Run, watch fail. Implement:**

```ts
export function phaseForSelector(run: Run, nodeId: string, generation?: number, attempt?: number): Phase | null {
  if (generation === undefined || attempt === undefined) return newestPhaseOf(run, nodeId)
  for (const p of run.phases ?? []) {
    if (p.node_id === nodeId && p.generation === generation && p.attempt === attempt) return p
  }
  return null
}
```

- [ ] **Step 3: The search schema** — extend `searchSchema` with `generation: z.coerce.number().int().positive().optional(), attempt: z.coerce.number().int().positive().optional()` (`coerce` because URL values are strings) — and RE-NARROW at every read site (`typeof x === 'number' && Number.isFinite(x)`) per pre-flight 20; the schema is a type the runtime can violate. The phase-detail pane resolves through `phaseForSelector`; a null resolution renders "This attempt is not in the run's phase list." rather than falling back silently to newest — a silent fallback would re-create the bug the selector exists to fix.

- [ ] **Step 4: The links** — in the event log: `gate.decided` rows wrap in `<Link to="/runs/$runId" search={{ phase: p.node_id, generation: p.generation, attempt: p.attempt }}>`; `publish.pr_opened` rows render an external `<a href={payload.<url key>}>`; artifact-bearing payload rows link per Task 8. Phase cards with artifacts already link via Task 8's rows.

- [ ] **Step 5: The two-decisions test (the spec's own acceptance shape)** — a route test whose fixture run carries: gen-1 gate decided `request_changes`, gen-2 gate decided `approve`, and both `gate.decided` events. Click each event row; assert the pane shows THAT attempt's decision (the gen-1 pane renders the request-changes decision, not the approve). This fails against a `?phase=`-only implementation — prove it by temporarily dropping `generation`/`attempt` from the link, watching it red, restoring.

- [ ] **Step 6: Commit**

```bash
git add web/src/runs/phase-spine.ts web/src/runs/phase-spine.test.ts web/src/routes/_app/
git commit -m "web: event rows link to their destinations, and a gate decision links to ITS attempt"
```

---

### Task 10: The phone tab bar becomes actually fixed — as a set, plus the wordmark and the FAB

Pre-flight 19 is this task's contract: pinning the tabs alone overlays the last screenful of every page AND the gate decision controls. Four edits, one task: the token + fixed bar, `<main>` clearance, the FAB, the decision bar. Plus the wordmark→Home link (finding 15) and the FAB restyle (finding 16). One `<nav>` landmark throughout.

**Files:**
- Modify: `web/src/styles/tokens.css` (the `@theme` token), `web/src/styles/app.css`
- Modify: `web/src/shell/app-shell.tsx`, `web/src/shell/nav.tsx`, `web/src/shell/app-shell.test.tsx`
- Modify: `web/src/routes/_app/tasks.tsx` (FAB), `web/src/approvals/approval-surface.tsx` (decision bar)
- Modify: `web/e2e/approvals.spec.ts` (the geometry assertion grows the pinned-bar checks)

**Interfaces:**
- Produces: the `@theme` entry `--spacing-tab-bar: 4rem;` — consumed ONLY as named utilities (`h-tab-bar`, `pb-tab-bar`, `bottom-tab-bar`); Tailwind 4 derives them from the `--spacing-*` namespace. Arbitrary-value spellings fail `web:arch` (pre-flight 19).

- [ ] **Step 1: Write the failing shell tests**

In `app-shell.test.tsx` (structure assertions — geometry is Step 5's e2e):

```tsx
it('pins the phone tab bar and reserves its height on main', () => {
  render(<AppShell me={me}>x</AppShell>)
  const bar = screen.getByTestId('phone-tabs')
  expect(bar.className).toMatch(/\bfixed\b/)
  expect(bar.className).toMatch(/\bbottom-0\b/)
  // The reservation is the other half of the fix: pinning alone overlays the
  // last screenful of every page (finding 14's real shape).
  expect(screen.getByRole('main').className).toMatch(/\bpb-tab-bar\b/)
})

it('the wordmark links Home', () => {
  render(<AppShell me={me}>x</AppShell>)
  expect(screen.getByRole('link', { name: 'caesar' })).toHaveAttribute('href', '/')
})

it('keeps one navigation landmark', () => {
  render(<AppShell me={me}>x</AppShell>)
  expect(screen.getAllByRole('navigation')).toHaveLength(1)
})
```

- [ ] **Step 2: Run, watch fail.**

- [ ] **Step 3: Implement the set**

`tokens.css` — inside the existing `@theme` block:

```css
  /* The phone tab bar's reserved height. A SPACING token so Tailwind derives
     h-tab-bar / pb-tab-bar / bottom-tab-bar as named utilities — web:arch
     rejects arbitrary-value spellings.

     THE BODY PADDING DOES NOT COVER A FIXED BAR. app.css:12-18 puts
     env(safe-area-inset-*) on `body`, but a `position: fixed; bottom: 0`
     element is laid out against the VIEWPORT (the initial containing block),
     not against body's padding box — so the bar would sit under the iPhone
     home indicator no matter what body does. An earlier draft of this plan
     asserted the opposite as fact; it is wrong, and reasoning is not how this
     gets settled (see "emulation is not the device").

     THE TOKEN IS THE TOTAL, and the 4rem is what survives INSIDE it. Preflight
     makes everything border-box, so the bar's own bottom padding eats its
     height rather than adding to it: a 4rem token plus a 34px inset would
     leave a 30px tab, under the 44px floor — and the floor rule in app.css
     does not cover these anchors, nor would any CI viewport reveal it.
     Every consumer — main's clearance, the FAB, the decision bar — offsets by
     this same total, or each under-clears by exactly the indicator. */
  --spacing-tab-bar: calc(4rem + env(safe-area-inset-bottom));
```

**The safe-area spelling has one legal shape, not a choice.** `web:arch` rejects arbitrary-value utilities (`pb-(--x)`, `pb-[env(...)]`), so the inset cannot be written inline; and the token's value must be the TOTAL:

```css
  --spacing-tab-bar: calc(4rem + env(safe-area-inset-bottom));
```

The bar takes `h-tab-bar` **plus** an internal `padding-bottom` of the inset, so its content row keeps the full 4rem while the element occupies the total; `<main>`, the FAB and the decision bar all offset by the same total token.

An earlier draft also offered a second shape — keep `--spacing-tab-bar` at `4rem` and add the inset as padding on the bar — **and that one is broken.** Tailwind's preflight sets `box-sizing: border-box` globally (`tailwindcss/preflight.css:12`), so padding lands INSIDE the 4rem rather than adding to it: on a 34px iPhone inset the tab content gets 30px, under the locked 44px minimum. Nor does the global floor catch it — `app.css:21-26` matches `button`, `a[role="button"]` and `[role="tab"]`, while the tabs are plain `<Link>` anchors with none of those (`nav.tsx:77-88`). And the Playwright projects run at zero inset, so nothing in CI would ever show it; the first report would be a thumb missing a tab on a real phone.

**Verify the token compiles to a real value before building on it** — `env()` inside a `@theme` entry is exactly the kind of pinned-surface claim this repo has lost four times by reasoning. Run `task web:build`, grep the emitted CSS for the `h-tab-bar`/`pb-tab-bar` rules, and confirm the `calc()` survived rather than being dropped or resolved to `4rem`. If Tailwind refuses `env()` in a theme value, fall back to a plain `app.css` rule that sets BOTH the height and the padding on the bar's own class, keeping the content row at 4rem — and record which shape shipped.

`nav.tsx` — the phone-tabs div gains `fixed inset-x-0 bottom-0 z-20 h-tab-bar` (keeping `border-border bg-surface flex border-t md:hidden`) **plus its own `padding-bottom` of the inset** — `h-tab-bar` is the TOTAL (content row + indicator), and under border-box the padding carves the indicator strip out of that total, leaving the tabs their full 4rem. Without the padding the tabs paint under the home indicator; without the inset in the token they lose 34px of touch target. Confirm on a real device at Step 6 — emulation runs at a zero inset and will show neither. `app-shell.tsx` — `<main className="flex-1 p-4 pb-tab-bar md:pb-4">`; the wordmark `<span>` becomes `<Link to="/" className="font-mono text-sm">caesar</Link>` (inside the header, NOT a second nav). `tasks.tsx` — the FAB's `bottom-20` becomes `bottom-tab-bar` plus a small gap: `className="… fixed right-4 bottom-tab-bar mb-4 … md:bottom-6 md:mb-0"`, and the restyle (finding 16's taste verdict): drop the full-round `size-14 rounded-full` pill for `rounded-xl px-5 py-3` keeping `bg-accent text-accent-contrast` — final taste call is recorded at the checkpoint (Task 34). `approval-surface.tsx` — the decision bar becomes `sticky bottom-tab-bar z-10 md:bottom-0` (it must clear the fixed bar on phone; on desktop the bar is hidden). Stacking order: tab bar `z-20`, decision bar `z-10`, FAB `z-10` — the walk's most important buttons are never under chrome.

- [ ] **Step 4: Run the unit suites** — `vitest run src/shell src/routes src/approvals`; jsdom asserts structure only.

- [ ] **Step 5: The Playwright geometry assertion (phone project)**

Append to `e2e/approvals.spec.ts`'s geometry block — this is the ONLY place these classes are falsifiable (its own comment says so):

```ts
test('the pinned bar never covers the decision controls or the end of a page', async ({ page, request }) => {
  const runId = await openGate(request)
  await signIn(page)
  await page.getByTestId('gate-card').filter({ has: page.locator(`a[href*="${runId}"]`) }).click()
  const bar = page.getByTestId('phone-tabs')
  const approve = page.getByRole('button', { name: 'Approve' })
  await expect(bar).toBeVisible()
  await expect(approve).toBeVisible()
  const [barBox, btnBox] = [await bar.boundingBox(), await approve.boundingBox()]
  // The decision controls sit fully ABOVE the pinned bar — overlap is the bug class.
  expect(btnBox!.y + btnBox!.height).toBeLessThanOrEqual(barBox!.y + 1)

  // THE MAIN-PADDING HALF NEEDS ITS OWN SUBJECT. The Approve button lives in a
  // `sticky bottom-…` bar inside the approval surface (approval-surface.tsx:82)
  // with its OWN offset — it clears the tab bar whether or not <main> reserves
  // anything, so measuring it proves nothing about `pb-tab-bar` (round 1's
  // blocker: the prescribed mutation below stayed green). The subject is an
  // ordinary, non-sticky, LAST element of the scrolled document.
  await page.keyboard.press('End')
  const tail = page.getByTestId('page-end')   // add this testid to the last block of the gate screen
  const tailBox = await tail.boundingBox()
  expect(tailBox!.y + tailBox!.height).toBeLessThanOrEqual(barBox!.y + 1)
  await approve.click() // clickable proves not covered — Playwright refuses covered targets
})
```

Run `task web:e2e`. **Three independent mutation proofs, each recorded:** (i) remove `pb-tab-bar` from `<main>` alone → the `page-end` assertion fails and the Approve assertion stays green (that asymmetry IS the point — if `page-end` also stays green the sentinel is not at the end of the scroll and the fixture is wrong); (ii) remove the decision bar's `bottom-tab-bar` alone → the Approve overlap half fails; (iii) remove `fixed` from the bar → both halves pass vacuously, so also assert the bar's `y` is unchanged after `End` (a pinned bar does not scroll away). Restore after each.

- [ ] **Step 6: Commit**

```bash
git add web/src/styles/ web/src/shell/ web/src/routes/_app/tasks.tsx web/src/approvals/approval-surface.tsx web/e2e/approvals.spec.ts
git commit -m "web: the tab bar pins as a layout set — height token, main clearance, FAB and decision bar above it, wordmark home"
```

---

### Task 11: Sticky-header Retry on failed runs only

Sofo's checkpoint ruling, verbatim: "Retry on failed runs only." Wired to the existing `rerunTask` (`POST /api/v1/tasks/{id}/runs`, no body, 201 → full `RunResponse`). Rejected runs end `failed`, so ONE status rule covers both. On a `failed` run the service takes the terminal fall-through (a plain new run); the refusal string for live runs ("run is in flight; cancel arrives in Plan 09") is unreachable from a failed-only button — but the 409 surface still renders it if racing state changes it.

**Files:**
- Create: `web/src/runs/retry-run.ts`
- Modify: `web/src/routes/_app/runs.$runId.tsx` (RunHeader), its test

**Interfaces:**
- Consumes: `run.task_id` (on the DTO), `run.status`; `api.POST('/api/v1/tasks/{id}/runs')`.
- Produces: `useRetryRun(taskId)` returning the mutation; on success navigate to `/runs/$runId` of the NEW run (`response.id`) and invalidate `['runs']`, `['tasks']` (the bridge also hears `run.created`, but a navigation should not wait on a stream frame).

- [ ] **Step 1: Failing route test** — three cases: header shows `Retry` on a `failed` run fixture; no Retry on `running`/`waiting`/`succeeded` fixtures (iterate); clicking Retry POSTs (MSW spy) and navigates to the new run id.

- [ ] **Step 2: Run, watch fail. Implement:**

```ts
export function useRetryRun(taskId: string) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  return useMutation({
    mutationFn: async () => unwrap(await api.POST('/api/v1/tasks/{id}/runs', { params: { path: { id: taskId } } })),
    onSuccess: (run) => {
      void qc.invalidateQueries({ queryKey: ['runs'] })
      void qc.invalidateQueries({ queryKey: ['tasks'] })
      void navigate({ to: '/runs/$runId', params: { runId: run.id } })
    },
  })
}
```

Header: `{run.status === 'failed' && <button onClick={() => retry.mutate()} disabled={retry.isPending} className="…">Retry</button>}` in the sticky `RunHeader`, with the mutation's `ApiError` rendered through the existing error-surface pattern (correlation_id shown) — a 409 ("another re-run of this task is already pending") is a normal answer, not a crash.

- [ ] **Step 3: Suites, mutation** (widen the guard to `!== 'running'` — the succeeded/waiting cases must fail), restore, commit.

```bash
git add web/src/runs/retry-run.ts web/src/routes/_app/
git commit -m "web: sticky-header Retry on failed runs, riding rerunTask"
```

---

### Task 12: The refetch fan collapses — two tiers, and the twins move together

The bridge's existing pooling is NOT the gap (spec §2.5): the fan is what every window fires *unconditionally*. Two-tier rewrite: a window containing any non-`phase.*` kind invalidates the collections at once, as today (`ATTENTION_KINDS` is NOT the predicate — it omits `run.created`/`run.superseded` and its own doc scopes it to the inbox); a phase-only window schedules a trailing collection refresh on `PHASE_TIER_THROTTLE_MS = 5_000` (pre-flight 2). The run-DTO invalidation goes **exact**; the events key `['run', id, 'events']` joins the throttled tier and flushes immediately on any non-phase window. `decide-gate.ts` (the fan's twin) and `run-events-query.ts`'s now-false comment move in the same commit (pre-flight 21).

**Files:**
- Modify: `web/src/realtime/cache-bridge.ts` (rewrite), `web/src/realtime/cache-bridge.test.ts`
- Modify: `web/src/gates/decide-gate.ts`, `web/src/runs/run-events-query.ts` (comment)
- Create: `web/src/realtime/refetch-fan.test.tsx` (the HTTP-counting acceptance)
- **Modify: `web/src/test/render-app.tsx`** — the observer seam the acceptance test needs (see Step 4). It does not exist today: `renderApp(path, handlers)` renders `RouterProvider` and nothing else (`render-app.tsx:119, 165-170`), so there is no way to mount a `['tasks']` observer alongside a route that does not read it. Listed here rather than discovered mid-task, because the gate refuses a dirty tree and an unstaged helper edit is how this task fails at its last step.

**Interfaces:**
- Consumes: `EventFrame` (kind + run_id); `setTimeoutImpl` — the throttle tier gets its OWN injected timer through the same option (one seam, two uses), NOT a test-only flush (the recorded historical failure).
- Produces: same export `createCacheBridge(qc, opts)`; new exported const `PHASE_TIER_THROTTLE_MS = 5_000`.

- [ ] **Step 1: Rewrite the bridge**

The snippet below shows the new INTERNALS only. `createCacheBridge`'s existing doc comment (`cache-bridge.ts:37-57`) carries two recorded lessons that survive this rewrite unchanged and must not be dropped with the code they sit above: why there is no test-only `flush()` (the plan-11 failure where deleting the scheduling arm left all five tests green and the shipped bridge inert), and why there is no `close()`. KEEP both paragraphs; extend the comment with the tier split. A rewrite that silently sheds them is the recorded "comments are written from intent" failure — and the next reader re-invents `flush()`.

```ts
/** How long frames are pooled before one invalidation goes out. */
const DEFAULT_DEBOUNCE_MS = 250
/**
 * The phase tier's trailing-refresh throttle. 5s = the client-wide staleTime
 * (query-client.ts): the throttled tier never serves data older than the
 * staleness the design already accepts, and phase dots advance well inside
 * the locked "live dots" bound. Asserted by value in the tests.
 */
export const PHASE_TIER_THROTTLE_MS = 5_000

export function createCacheBridge(
  qc: QueryClient,
  opts?: { debounceMs?: number; throttleMs?: number; setTimeoutImpl?: typeof setTimeout },
) {
  const debounceMs = opts?.debounceMs ?? DEFAULT_DEBOUNCE_MS
  const throttleMs = opts?.throttleMs ?? PHASE_TIER_THROTTLE_MS
  const later = opts?.setTimeoutImpl ?? setTimeout
  let scheduled = false
  let inboxDirty = false
  /** Did the open window see any non-phase.* kind? That — not ATTENTION_KINDS,
   *  whose own doc scopes it to the inbox and which omits run.created and
   *  run.superseded — is the collection-immediate predicate. */
  let lifecycleDirty = false
  let throttlePending = false
  const dirtyRuns = new Set<string>()
  /** Runs whose phase frames arrived during a throttle hold; refreshed when it fires. */
  const throttledRuns = new Set<string>()

  function fireCollections(runs: readonly string[]) {
    void qc.invalidateQueries({ queryKey: ['approvals'] }) // guarded by caller where needed
  }

  function fire() {
    scheduled = false // BEFORE any invalidation — the deaf-after-first-burst guard, now needed twice
    const runs = [...dirtyRuns]
    const inbox = inboxDirty
    const lifecycle = lifecycleDirty
    dirtyRuns.clear()
    inboxDirty = false
    lifecycleDirty = false

    // The run DTO stays per-window — but EXACT: the events log no longer rides
    // the prefix (it joins the throttled tier below), so a chatty phase stream
    // costs one run-DTO refetch per window and NOTHING else by side effect.
    for (const runId of runs) void qc.invalidateQueries({ queryKey: ['run', runId], exact: true })

    if (lifecycle) {
      // Any non-phase kind: collections and event logs refetch NOW — terminal
      // events stay instant — and a pending throttle hold is absorbed.
      if (inbox) void qc.invalidateQueries({ queryKey: ['approvals'] })
      void qc.invalidateQueries({ queryKey: ['runs'] })
      void qc.invalidateQueries({ queryKey: ['tasks'] })
      for (const runId of runs) void qc.invalidateQueries({ queryKey: ['run', runId, 'events'] })
      for (const runId of throttledRuns) void qc.invalidateQueries({ queryKey: ['run', runId, 'events'] })
      throttledRuns.clear()
      return
    }
    // Phase-only window: the collections render phase progress (RunCard and the
    // Tasks rows draw PhaseDots off phase_summary; Home's live dots are locked
    // design), so DEFERRING to the 60s floor would be a regression dressed as
    // tuning — instead one trailing refresh per throttle interval.
    for (const runId of runs) throttledRuns.add(runId)
    if (throttlePending) return
    throttlePending = true
    later(() => {
      throttlePending = false // BEFORE the invalidations — same deafness guard
      const held = [...throttledRuns]
      throttledRuns.clear()
      // ABSORBED: a lifecycle window that fired while this timer was in flight
      // already refetched the collections and drained throttledRuns, and the
      // timer cannot be cancelled through the setTimeoutImpl seam. Returning on
      // an empty hold is what makes absorption total — without it the orphaned
      // timer lands a second, pointless ['runs'] + ['tasks'] refetch a few
      // seconds after the lifecycle event, which is precisely the drumbeat this
      // task exists to remove (round 1's blocker). The guard is on the HELD SET
      // rather than on a cancellation flag because the set is the honest
      // question: "is there anything left to refresh?"
      if (held.length === 0) return
      void qc.invalidateQueries({ queryKey: ['runs'] })
      void qc.invalidateQueries({ queryKey: ['tasks'] })
      for (const runId of held) void qc.invalidateQueries({ queryKey: ['run', runId, 'events'] })
    }, throttleMs)
  }

  return {
    onFrame(f: EventFrame): void {
      const runId = f.frame.run_id
      if (typeof runId !== 'string' || runId === '') return
      dirtyRuns.add(runId)
      if (!f.frame.kind.startsWith('phase.')) lifecycleDirty = true
      if (ATTENTION_KINDS.has(f.frame.kind)) inboxDirty = true
      if (scheduled) return
      scheduled = true
      later(fire, debounceMs)
    },
  }
}
```

(Keep `ATTENTION_KINDS` exactly as-is — it still governs the inbox alone; update its doc comment's last paragraph to name the new tier split. Delete the unused `fireCollections` stub above — it is shown here only to flag that `approvals` stays inbox-guarded.)

- [ ] **Step 2: Rewrite the bridge tests** — the existing 12 adapt (the full-fan exact `toEqual` becomes the LIFECYCLE window's expectation, with `{exact: true}` visible on the run key — assert via the spy's second argument, not just the key); new cases: (a) a pure-`phase.*` window invalidates ONLY `['run', id]` exact, and after firing the throttle timer, `['runs']` + `['tasks']` + `['run', id, 'events']`; (b) two phase windows inside one throttle hold produce ONE trailing refresh (the drumbeat collapse, asserted by count); (c) a `run.superseded` window refetches collections immediately, never on the throttle; (d) **total absorption**: a lifecycle window arrives during a throttle hold, then the ORIGINAL throttle timer is fired manually — it must produce ZERO further invalidations of `['runs']`, `['tasks']` or the events key (the timer cannot be cancelled through the `setTimeoutImpl` seam, so the empty-hold return is the only thing standing between a lifecycle event and a spurious collection refetch five seconds later; assert on the spy's total call count before and after firing it, not merely on the events key); (d2) **absorption does not go deaf**: a NEW phase frame arriving after that lifecycle window, but before the orphaned timer fires, still gets its own trailing refresh (the orphan timer drains the newly-held run — or a fresh one is armed — either way the run's events refetch exactly once); (e) the throttle timer's ms is `PHASE_TIER_THROTTLE_MS` and equals 5000; (f) BOTH deafness guards: after a fired window AND after a fired throttle, the next frame arms a new one (the recorded failure mode, now with two flags).

- [ ] **Step 3: The twins** — `decide-gate.ts`'s `onSettled` gains `exact: true` on `['run', runId]` and adds `['run', runId, 'events']` explicitly (a decision must refresh the log it just changed — the prefix no longer does it); `run-events-query.ts`'s doc comment is REWRITTEN: the key's mechanism is now explicit tier membership in the bridge and the decide mutation, and the stale `:92` citation goes. `approvals.test.tsx` still holds the key spellings together — extend it to the events key spelling if it does not already.

- [ ] **Step 4: The HTTP-counting acceptance test** (`refetch-fan.test.tsx`) — the spec's shape: real HTTP requests counted through MSW under an event burst, with per-endpoint ceilings, driven through the injected timers (`scriptedStream` + the bridge's `setTimeoutImpl`):

**EVERY MEASURED ENDPOINT NEEDS A MOUNTED OBSERVER, or the ceiling is vacuous.** This is round 1's third blocker and it kills the obvious shape: `renderApp('/')` mounts `['approvals']` and `['runs']` and NOTHING ELSE — Home's loader and component read exactly those two (`routes/_app/index.tsx:52-55, 70-71`). Invalidating a query with no observers refetches nothing, so `GET /tasks` and `GET /runs/r1/events` would sit at **0** under any bridge, upper bounds would pass, and mutation (i) below could not fire. The route under test is therefore **`renderApp('/runs/r1')`** — run detail observes the run DTO and the events log — with the Tasks and Home collections mounted alongside by pre-warming their queries through a `useQuery` harness component rendered in the same tree (**the helper does NOT accept children today and this task adds the seam**: `renderApp` takes `(path, handlers)` and renders `RouterProvider` alone (`render-app.tsx:119, 165-170`). Give it an optional third argument — `renderApp(path, handlers, { observe: [tasksQueryOptions(), runsQueryOptions()] })` — that mounts a tiny component calling `useQuery` on each option INSIDE the same `QueryClientProvider`, so the observers are real ones the cache serves, not a hand-rolled subscription that behaves differently. Do not move the assertions to a route that cannot see them, and do not fake an observer: an invalidation reaches a query only if something is watching it, which is the whole property under test.)

**TWO scenarios, because absorption and accumulation give different counts and one scenario cannot assert both.** Both share the same setup: `renderApp('/runs/r1')` with observers mounted for `['runs']`, `['tasks']`, `['approvals']` and `['run','r1','events']`; let every initial fetch settle; THEN zero the MSW counters — the baseline loads are not what is being measured. Counts are EXACT, never ceilings: an upper bound alone is satisfied by zero, which is the failure mode these scenarios exist to catch.

```
Scenario A — the drumbeat collapses (throttle fires with work held).
40 phase.* frames for r1 across 10 windows inside one throttle interval; run
the throttle timer; THEN one run.succeeded window and its debounce.
  GET /api/v1/runs           == 2   (the trailing throttle refresh, then the lifecycle)
  GET /api/v1/tasks          == 2
  GET /api/v1/approvals      == 1   (the lifecycle window only — run.succeeded is an attention kind)
  GET /api/v1/runs/r1/events == 2   (throttle + lifecycle; the old prefix behaviour measured ~10)
  GET /api/v1/runs/r1        == 11  (one exact per window — the per-window run DTO tier)
  GET /api/v1/auth/me        == 0   (pre-flight 1: bursts add zero me traffic)
This is the LIVENESS half too: the runs-list was genuinely refetched inside the
throttle (its phase_summary would advance a dot), which is why the 2 is an
equality and not a ≤.

Scenario B — absorption (lifecycle arrives BEFORE the throttle fires).
The same 10 phase windows, then one run.succeeded window and its debounce, and
only THEN the still-pending throttle timer.
  GET /api/v1/runs           == 1   (the lifecycle only — it drained the held set)
  GET /api/v1/tasks          == 1
  GET /api/v1/runs/r1/events == 1
  GET /api/v1/runs/r1        == 11
Then fire the orphaned throttle timer and assert EVERY counter is unchanged.
That last step is the whole point: the timer cannot be cancelled through the
setTimeoutImpl seam, so the empty-hold return is the only thing standing between
a lifecycle event and a spurious collection refetch five seconds later.
```

The ordering half rides Scenario B: the lifecycle window's collection refetch has already landed at a point where no throttle timer has run.

Asserting on invalidation calls alone would pass against today's bridge and prove nothing — the counters are HTTP-level (MSW resolver call counts). Mutation proofs, each recorded, and **each must fail on its own** (run them one at a time; a mutation that leaves both scenarios green means its endpoint has no observer and the fixture, not the bridge, is wrong): (i) revert the events key to prefix-riding (drop it from both tiers, rely on `['run', id]` non-exact) → A's events count moves; (ii) route phase frames to the immediate tier → A's runs/tasks counts blow past 2; (iii) drop the throttle's trailing refresh → A falls to 1 across the board and its liveness half fails; (iv) drop `exact: true` from the run-DTO invalidation → A's events count rises (the prefix starts carrying it again); (v) **delete the `held.length === 0` return** → B's final step fails and A stays green, which is the asymmetry that proves the guard is load-bearing rather than decorative.

- [ ] **Step 5: Suites + gate + commit**

```bash
git add web/src/realtime/ web/src/gates/decide-gate.ts web/src/runs/run-events-query.ts web/src/approvals/approvals.test.tsx web/src/test/render-app.tsx
git commit -m "web: two-tier cache bridge — phase bursts cost one trailing refresh per 5s, lifecycle stays instant, the twins move with it"
```

---

# Wave 1 — two read endpoints, the runs filter, Repos, Admin

All three backend endpoints (13–15) land, `task contracts:gen` run and `contracts:check` green, before their screens (18–23) begin. No new write paths anywhere in this wave (Task 16 classifies an existing delete's error; it writes nothing new).

---

### Task 13: `GET /api/v1/audit`

Admin-only, newest-first, cursor-paginated from day one — `audit_entries` is the one table where "unpaginated is fine at month-one volume" is already false. Minimal V1: cursor + limit, no filters until a use appears. The read path is from scratch (pre-flight 7). The read query and cursor live in `internal/platform/audit` (platform imports no business code); the HTTP operation lives in `internal/identity/api` beside the other admin surfaces — which changes `identityapi.New`'s signature and therefore touches `contracts.go` and `serve.go` in the same commit.

**Files:**
- Modify: `backend/internal/platform/pg/schema.sql` + new migration `<ts>_audit_read_index.sql`
- Modify: `backend/internal/platform/audit/queries.sql` (+ regenerated store)
- Create: `backend/internal/platform/audit/read.go`, `read_test.go`
- Modify: `backend/internal/identity/api/api.go`, `dto.go`; `backend/internal/contracts/contracts.go`; `backend/internal/app/serve.go`
- Create: `backend/internal/identity/api/audit_integration_test.go`

**Interfaces:**
- Produces: `audit.Reader` with `ListPage(ctx, db DBTX, cursor string, limit int32) (Page, error)`; `Page{Entries []AuditEntry, NextCursor string}`; the operation `listAudit`, `GET /api/v1/audit`, response `AuditPageResponse{Entries []AuditEntryResponse required, NextCursor *string required}` — `AuditEntryResponse{ID int64, OccurredAt time.Time, ActorUserID *string, Action, SubjectKind, SubjectID, CorrelationID string, Detail map[string]any}` — every field `required:"true"` EXCEPT none ($ref rule does not bite: `ActorUserID` is `*string`, required-but-nullable is the honest shape; `Detail` is a map — required, renders `["object","null"]`).

- [ ] **Step 1: The index migration** — in `schema.sql`, under the existing audit index:

```sql
-- Serves the admin read: newest-first keyset on (occurred_at DESC, id DESC).
-- occurred_at is NOT unique (writers stamp it from the injected clock; one
-- transaction can emit several entries — the login sweep), so the id
-- tiebreaker is load-bearing, not defensive.
CREATE INDEX audit_entries_read_idx ON audit_entries (occurred_at DESC, id DESC);
```

`task db:new -- audit_read_index`, then `task db:sqlc`.

- [ ] **Step 2: The query** — in `platform/audit/queries.sql`:

```sql
-- name: ListAuditPage :many
-- Newest-first keyset. The row-value comparison matches the index exactly;
-- cursor nulls mean "first page".
SELECT id, occurred_at, actor_user_id, action, subject_kind, subject_id, correlation_id, detail
FROM audit_entries
WHERE (sqlc.narg(cursor_occurred_at)::timestamptz IS NULL
       OR (occurred_at, id) < (sqlc.narg(cursor_occurred_at)::timestamptz, sqlc.narg(cursor_id)::bigint))
ORDER BY occurred_at DESC, id DESC
LIMIT sqlc.arg(page_limit);
```

- [ ] **Step 3: Failing pure tests, then the reader** — `read_test.go` covers the cursor codec round-trip and rejections (mirror `runlist.go:91-100`'s shape — base64.RawURLEncoding of `RFC3339Nano + "|" + strconv.FormatInt(id, 10)`; malformed → `xerrors.ClassInvalid` field error on `cursor`), and clamp (default 50, max 200). `read.go` requests `limit+1` to detect the next page (the runlist idiom), encodes `NextCursor` from the last returned row.

- [ ] **Step 4: The operation** — `identityapi.New` gains an `audit *audit.Reader` (plus the pool it reads through — pass the pool the service already holds, or a `DBTX`; follow what `New` already receives and keep the layering: the API calls `reader.ListPage(ctx, pool, …)`). Update `contracts.go:230-241`'s nil-construction and `serve.go`'s wiring in the same commit. The table entry:

```go
{
	Pattern: "GET /api/v1/audit", ID: "listAudit",
	Summary: "Global audit entries, newest first (admin). Cursor-paginated: pass next_cursor back as cursor.",
	Request: ListAuditQuery{}, Response: AuditPageResponse{}, Status: http.StatusOK,
	Errors:  []int{http.StatusUnauthorized, http.StatusForbidden},
	Handler: a.adminOnly(a.handleListAudit),
}
```

`ListAuditQuery{Cursor string `query:"cursor"`; Limit int32 `query:"limit"`}` — query-tagged, no body, so NO requestOverrides wrapper is needed.

- [ ] **Step 5: Integration tests** — `audit_integration_test.go` (testcontainers, the identity api test harness pattern): (a) member → 403 "admin role required"; (b) seed 3 entries via `Recorder.Record` with DISTINCT times → newest first; (c) **equal-timestamp page boundary**: seed 4 entries where the middle two share `occurred_at` exactly (drive the Recorder's fake clock), page with limit 2, follow `next_cursor`, assert all 4 arrive exactly once in order; (d) empty table → empty page, null cursor; (e) exact-boundary cursor (limit equals remaining rows) → next page empty, cursor null.

- [ ] **Step 6: Contracts + gate + commit** — `task contracts:gen` (openapi.json + schema.d.ts diff committed together), `contracts:check`, full gate.

```bash
git add backend/ web/src/api/schema.d.ts
git commit -m "audit: a paginated admin read — (occurred_at DESC, id DESC) keyset, index in the same change"
```

---

### Task 14: Decision history — `GET /api/v1/approvals/decisions`

A sibling operation; the waiting arm changes by zero bytes. Cross-run decision rows, newest-first on `(acted_at DESC, id DESC)`, same grant scoping as the waiting arm (any grant scopes projects; admin sees all). The row joins `phase_runs` for the attempt (pre-flight 6) and `users` for the actor (precedented), and `runs`→`tasks` for task/project identifiers and scoping.

**Files:**
- Modify: `backend/internal/platform/pg/schema.sql` + migration `<ts>_gate_decisions_history_index.sql`
- Modify: `backend/internal/runs/store/queries.sql` (+ regenerated)
- Create: `backend/internal/runs/decisionhistory.go`, `decisionhistory_test.go`
- Modify: `backend/internal/runs/api/api.go`, `dto.go`
- Create: `backend/internal/runs/api/decisionhistory_integration_test.go`

**Interfaces:**
- Produces: `GET /api/v1/approvals/decisions`, ID `listDecisions`, `DecisionPageResponse{Decisions []DecisionResponse required, NextCursor *string required}`; `DecisionResponse` — all `required:"true"`, scalars throughout: `ID, RunID, TaskID, TaskTitle, NodeID string; Generation, Attempt int32; Decision, Note string; ActedByEmail string; ActedAt time.Time; ExaminedArtifactID string`. Task 18 links rows with `(RunID, NodeID, Generation, Attempt)` — Task 9's selector triple.

- [ ] **Step 1: Index** — in `schema.sql` beside `gate_decisions_run_node_idx`:

```sql
-- The global newest-first history read. (run_id, node_id) cannot serve it.
CREATE INDEX gate_decisions_acted_idx ON gate_decisions (acted_at DESC, id DESC);
```

`task db:new -- gate_decisions_history_index`.

- [ ] **Step 2: The query**

```sql
-- name: ListDecisionsPage :many
-- Cross-run decision history, newest first. Joins: phase_runs recovers the
-- attempt (the decision row deliberately keys phase_run_id and stores only
-- generation); users for the actor (the ListGateDecisionsForRun precedent);
-- tasks for title + the project scoping the waiting arm uses.
SELECT gd.id, gd.run_id, gd.node_id, gd.generation, pr.attempt,
       gd.decision, gd.note, gd.examined_artifact_id, gd.acted_at,
       u.email AS acted_by_email,
       t.id AS task_id, t.title AS task_title, t.project_id
FROM gate_decisions gd
JOIN phase_runs pr ON pr.id = gd.phase_run_id
JOIN users u ON u.id = gd.acted_by
JOIN runs r ON r.id = gd.run_id
JOIN tasks t ON t.id = r.task_id
WHERE (sqlc.arg(all_projects)::boolean OR t.project_id = ANY(sqlc.arg(project_ids)::uuid[]))
  AND (sqlc.narg(cursor_acted_at)::timestamptz IS NULL
       OR (gd.acted_at, gd.id) < (sqlc.narg(cursor_acted_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY gd.acted_at DESC, gd.id DESC
LIMIT sqlc.arg(page_limit);
```

- [ ] **Step 3: Pure half + tests** — `decisionhistory.go`: the `(acted_at, id-uuid)` cursor codec (identical shape to `encodeRunCursor` — RFC3339Nano + "|" + uuid), clamp, limit+1 next-page detection, `DecisionFilter{All bool; ProjectIDs []uuid.UUID; Limit int32; Cursor string}` with the non-nil ProjectIDs normalization (`x = ANY(NULL)` is the recorded bug shape). Table-driven codec tests first, red then green.

- [ ] **Step 4: The operation** — in runs/api's table, beside the approvals entry:

```go
{
	Pattern: "GET /api/v1/approvals/decisions", ID: "listDecisions",
	Summary: "Decision history across runs, newest first (any grant scopes; admin sees all). Cursor-paginated.",
	Request: ListDecisionsQuery{}, Response: DecisionPageResponse{}, Status: http.StatusOK,
	Errors:  []int{http.StatusUnauthorized},
	Handler: a.authenticated(a.handleListDecisions),
}
```

**Route-order caveat, checked not assumed:** the waiting arm registers `GET /api/v1/approvals`. Go 1.22+ mux prefers the more specific literal pattern, so `/approvals/decisions` never shadows it — CONFIRM with an integration assertion that both operations answer (one test hitting each). The handler mirrors `handleListRuns`' grant resolution verbatim (admin → All; else GrantsForUser with the unconditional ids build).

- [ ] **Step 5: Integration tests** — (a) two decided gates across two projects, member granted one project sees ONLY theirs, admin sees both, newest first; (b) **equal-`acted_at` page boundary**: two decisions in one transaction-stamped instant straddling a limit-1 boundary — both arrive exactly once via the cursor (acted_at is `DBNow`, frozen per transaction — decide two gates of one run in the same test transaction shape, or stamp via the store directly through `DecideGate`'s real path with a held clock; if the real path cannot produce equal stamps, insert the second row through `InsertGateDecision` with the first's `acted_at` — the STORE is the subject here, and the test says so in a comment); (c) a request_changes → approve pair on one node: two rows, distinct `(generation, attempt)`, both present — the attempt-precision this endpoint exists to serve; (d) empty page shape.

- [ ] **Step 6: Contracts + gate + commit**

```bash
git add backend/ web/src/api/schema.d.ts
git commit -m "runs: GET /approvals/decisions — cross-run history with attempt recovered through phase_runs, index in the same change"
```

---

### Task 15: `workflow_id` and `repo_id` filters on `GET /runs`

The pulled-in call. Repo: no new join (`tasks` is already joined). Workflow: one subquery against `workflow_versions` — **licensed by name in this plan's bounded-context constraint** (see Global Constraints; the licence lives there rather than here so a reader of the rule sees every exception it has, and so this task cannot be read as quietly overriding it).

**Files:**
- Modify: `backend/internal/runs/store/queries.sql` (`ListRunsPage`), `runlist.go`, `api/api.go` (`runListFilter`), `api/dto.go` (`ListRunsQuery`)
- Modify: `backend/internal/runs/runlist_test.go`, `api/runlist_integration_test.go`
- Modify (web): `src/runs/runs-query.ts` (filters type)

- [ ] **Step 1: Failing integration cases** — in `runlist_integration_test.go`'s table: two tasks on different repos + different workflows, runs on each; `repo_id=X` returns only X's runs; `workflow_id=W` returns only W's (W being the WORKFLOW id, not the version id — a task pinned to version 2 of W still matches W); both filters compose with `status`; a malformed uuid in either param is a field error (`{workflow_id, "must be a uuid"}` — the task_id precedent).

- [ ] **Step 2: SQL** — `ListRunsPage` gains, after the task_id predicate:

```sql
    AND (sqlc.narg(repo_id)::uuid IS NULL OR t.repo_id = sqlc.narg(repo_id)::uuid)
    AND (sqlc.narg(workflow_id)::uuid IS NULL
         OR t.workflow_version_id IN (SELECT wv.id FROM workflow_versions wv WHERE wv.workflow_id = sqlc.narg(workflow_id)::uuid))
```

(A subquery rather than a join keeps the FROM clause untouched and the planner free; either is fine — pick one and keep the cursor predicate last.)

- [ ] **Step 3: Plumb** — `RunListFilter` gains `RepoID, WorkflowID *uuid.UUID`; `runListFilter` parses both with the task_id error pattern; `ListRunsQuery` gains

```go
	WorkflowID string `query:"workflow_id" description:"only runs of tasks pinned to any version of this workflow (uuid)"`
	RepoID     string `query:"repo_id" description:"only runs of tasks on this repo (uuid)"`
```

- [ ] **Step 4: Web filter type** — `runs-query.ts`'s filter object gains the two optional ids; they ride the SECOND key element (`['runs', filters]`) so the bridge's prefix invalidation still covers every variant (the key's own recorded design).

- [ ] **Step 5: Contracts + gate + commit**

```bash
git add backend/ web/src/api/schema.d.ts web/src/runs/runs-query.ts
git commit -m "runs: workflow_id/repo_id filters on the list — the two locked surfaces' missing read"
```

---

### Task 16: A repo delete refusal becomes readable

`DeleteRepo` is the one delete in `assets` that skips `fkViolation` (pre-flight 8); an in-use repo currently 500s. The constraint name picks the message: the FK is from **tasks** (or the publish tables) — "this repository is used by existing tasks", not "has runs".

**Files:**
- Modify: `backend/internal/assets/repos.go`
- Modify: `backend/internal/assets/repos_integration_test.go` (service), `backend/internal/assets/api/api_integration_test.go` (handler → 409)

- [ ] **Step 1: Failing service test** — create project→credential→repo→(a task referencing it, inserted through the runs service or a direct task insert if the assets test harness lacks intake — say which in a comment); `DeleteRepo` → assert `xerrors.ClassOf(err) == ClassConflict` and the message names tasks. A second case exercising a publish-table constraint if the fixture can mint an `approved_patches` row cheaply; if it cannot, the constraint-name switch still gets unit coverage by calling the classifier with a synthetic `pgconn.PgError`.

- [ ] **Step 2: Implement** — in `DeleteRepo`, mirroring `DeleteProject`'s order (fk check BEFORE the n==0 check):

```go
		n, err := store.New(tx).DeleteRepo(ctx, id)
		if constraint, fk := fkViolation(err); fk {
			// The RESTRICT constraints name what actually holds the repo:
			// tasks.repo_id, approved_patches.repo_id, publish_intents.repo_id.
			switch {
			case strings.Contains(constraint, "tasks"):
				return xerrors.E(xerrors.ClassConflict, "this repository is used by existing tasks")
			default:
				return xerrors.E(xerrors.ClassConflict, "this repository has publish records; it cannot be deleted")
			}
		}
		if err != nil {
			return fmt.Errorf("assets: delete repo: %w", err)
		}
```

- [ ] **Step 3: Handler test** — the api integration suite asserts the 409 problem body (title + detail) for the tasks case. Red-then-revert: remove the fk branch, the service test must fail with the wrapped-error shape, restore.

- [ ] **Step 4: Gate + commit**

```bash
git add backend/internal/assets/
git commit -m "assets: DeleteRepo classifies RESTRICT refusals — a used repo answers 409 naming its tasks"
```

---

### Task 17: The credential fingerprint gets a face, and wave 1's DTOs get their marks

**Four**-layer read-path change (pre-flight 9 undercounted it by one — see the files list) plus the `required:"true"` sweep for every DTO wave-1 screens read (pre-flight 10): `Credential*`, `Harness*`, `UserListResponse`, `ProjectGrantResponse`, `GrantListResponse`. Fingerprint's real shape: populated ONLY for `agent_auth_file` — the DTO field is `*string` + `required:"true"` (required-but-nullable), the UI renders an explicit "n/a" for the other three kinds, and the tests cover all four.

**Files:**
- Modify: `backend/internal/assets/store/queries.sql` (four column lists), `assets.go` (domain struct), **`credentials.go`**, `api/dto.go`
- Modify: `backend/internal/identity/api/dto.go`
- Modify: `backend/internal/assets/api/api_integration_test.go`

**`credentials.go` is the layer the first draft missed, and missing it is silent.** sqlc emits a distinct row type per query, so the package hand-rolls one intermediate — `credentialRow` — and `newCredential` maps it to `assets.Credential` (`credentials.go:32-48`). All four read paths build that intermediate (`:175, :195, :212, :277`). Add the column to the queries and the field to the domain struct only, and the value is read from Postgres, dropped on the floor at `newCredential`, and every API fingerprint is `null` — **including `agent_auth_file`'s**, so this task's own integration test fails and the obvious diagnosis (the SQL, the DTO) is the wrong one twice before anyone looks at the mapper.

- [ ] **Step 1: Failing integration test** — create one credential per kind (`forge_token`, `subscription_token`, `model_key` — and `agent_auth_file` with a valid auth-file secret; steal the fixture from `credentials_test.go`'s fingerprint cases); `GET /credentials` asserts: the auth-file row's `fingerprint` is a 64-hex string, the other three are JSON `null` (present, null — not absent). **Assert it on all four read paths, not just the list** — create's response, get, list, and the rotate response each build their own `credentialRow`, so a mapper updated in three places out of four passes the list test and lies on one screen.

- [ ] **Step 2: The four layers** — add `fingerprint` to the four queries' column lists (`CreateCredential RETURNING`, `ListCredentials`, `GetCredential`, `UpdateCredentialSecret RETURNING`); `task db:sqlc`; `assets.Credential` gains `Fingerprint *string`; **`credentialRow` gains `fingerprint *string`, `newCredential` assigns it, and all four `credentialRow{…}` literals pass it through** — the step the value is otherwise dropped at; `CredentialResponse` gains

```go
	// Fingerprint is populated ONLY for agent_auth_file (it feeds the partial
	// unique custody index; every other kind stores NULL) — required AND
	// nullable, the honest pair.
	Fingerprint *string `json:"fingerprint" required:"true"`
```

- [ ] **Step 3: The markup sweep** — `required:"true"` on every field of: `CredentialResponse` (all of it, `LastUsedAt`/`LastAuthExpiredAt` as `*time.Time` required-nullable — drop their `omitempty`, which contradicts required), `CredentialListResponse`, `HarnessResponse`/`HarnessListResponse`, `UserListResponse.Users`, `ProjectGrantResponse`, `GrantListResponse.Grants`. NO pointer-to-named-struct exists among them (scalars, slices, *string, *time.Time only) — the $ref rule does not bite; say so in the commit message.

- [ ] **Step 4: Contracts + gate + commit** — `contracts:gen`; the schema.d.ts diff will be broad (properties leaving optionality); commit the pair together.

```bash
git add backend/ web/src/api/schema.d.ts
git commit -m "assets/identity: fingerprint on the credential read path (agent_auth_file only, null elsewhere) + required marks for wave-1 DTOs"
```

---

### Task 18: The Approvals History tab

The waiting list stays; beneath it a History tab — newest-first decision rows, load-more on the cursor, each row linking into its run **attempt-precisely** (Task 9's triple). Closes 12a §10.7. URL-as-state: the tab is a search param.

**Files:**
- Create: `web/src/decisions/decisions-query.ts`, `web/src/approvals/decision-history.tsx` (+ test)
- Modify: `web/src/routes/_app/approvals.index.tsx`, `web/src/test/render-app.tsx` (default handler)

**Interfaces:**
- Consumes: `GET /api/v1/approvals/decisions` (generated types from Task 14); `useInfiniteQuery`.
- Produces: `decisionsInfiniteQueryOptions` — key `['decisions']`, `initialPageParam: undefined`, `getNextPageParam: (last) => last.next_cursor ?? undefined`, spreading `AUTH_SCOPED_QUERY_OPTIONS`.

- [ ] **Step 1: Failing route tests** — (a) `renderApp('/approvals?tab=history')` renders decision rows (MSW: two rows, one request_changes with a note, one approve) newest first, each row showing decision, note, actor email, acted_at, task title; (b) each row is a link whose href carries `?phase=<node>&generation=<g>&attempt=<a>`; (c) load-more: first page's `next_cursor` non-null → a "Load more" button; clicking fetches page 2 (MSW asserts the cursor param arrived) and appends; (d) the two tabs are links driving `?tab=` (URL-as-state), waiting is the default; (e) two distinct empty states: history-empty says "No decisions have been made yet." — a different sentence from the waiting list's.

- [ ] **Step 2: Implement** — the tab bar inside the Approvals screen is two `<Link search>`s styled as tabs (`[role="tab"]` gets the 44px floor from app.css); `DecisionHistory` maps pages→rows; the row's link:

```tsx
<Link to="/runs/$runId" params={{ runId: d.run_id }}
  search={{ phase: d.node_id, generation: d.generation, attempt: d.attempt }}>
```

Badge and stream behaviour unchanged — `['decisions']` is deliberately NOT in the bridge's fan (history moves only when a human decides; the decide mutation's own `onSettled` adds `['decisions']` to its invalidation list — extend Task 12's twin note).

- [ ] **Step 3: Suites, mutation** (drop `generation` from the link → test (b) fails), **commit**

```bash
git add web/src/decisions/ web/src/approvals/ web/src/routes/_app/approvals.index.tsx web/src/gates/decide-gate.ts web/src/test/render-app.tsx
git commit -m "web: Approvals grows its History tab — cursor-paged decisions, attempt-precise links into runs"
```

---

### Task 19: Repos — projects are the organizing unit

The `repos.tsx` stub becomes: a card list of projects, each opening a project detail holding its repos (grants join in Task 20). Create/edit through `useAppForm`: project (name, description); repo (forge kind chosen at create and IMMUTABLE after — `UpdateRepoRequest` has no forge field, so the edit form renders it read-only; slug, default branch, credential picker filtered to `forge_token`). Card-first phone, table projection desktop. Deletes surface Task 16's 409 distinctly from an outage.

**Files:**
- Create: `web/src/projects/projects-query.ts` (list + detail + repos queries), `project-form.tsx`, `repo-form.tsx`, `project-cards.tsx` (+ tests)
- Create: `web/src/routes/_app/repos.index.tsx`, `web/src/routes/_app/repos.$projectId.tsx` (+ tests)
- Delete: `web/src/routes/_app/repos.tsx` (the stub)
- Modify: `web/src/test/render-app.tsx`

**Interfaces:**
- Consumes: `listProjects`/`createProject`/`updateProject`/`deleteProject`, `listProjectRepos`/`createRepo`/`getRepo`/`updateRepo`/`deleteRepo`, `listCredentials` (admin — the credential picker; a manage-grant member without admin gets the picker's empty state naming the constraint, never a silent empty select: render "Credentials are listed by admins only — ask an admin to attach one" when the query 403s), all typed from Task 17's regenerated client.
- Produces: route strings `/repos` and `/repos/$projectId`; `projectReposQueryOptions(projectId)` reuses the existing `['project-repos', projectId]` key (the new-task form already owns that spelling — import from its module or promote the query to `src/projects/` and point both at it; the 2+-importer clamp decides the location).

- [ ] **Step 1: Failing route tests** — (a) `/repos` renders a project card per row (name, description, repo count if cheap — repo count needs per-project fetches; SKIP it, render name/description only: no invented data); empty state "No projects yet — create the first."; (b) card opens `/repos/$projectId`; (c) detail lists repos (slug, forge badge, default branch) with its own distinct empty state; (d) "New project" opens the form (vaul drawer on phone — the FAB pattern — or inline section; use the drawer, precedent exists), submits `{name, description}`, invalidates `['projects']`; a server field error (duplicate name → the backend's conflict) lands on the name field via `applyProblemToForm` when field-shaped and as the form banner otherwise — assert the banner path with a 409 problem fixture; (e) "New repo" form: forge select (github|gitlab), slug, default branch, credential select listing ONLY `forge_token`-kind credentials (MSW fixture has two kinds; assert the model_key one is absent); (f) edit repo: forge rendered as static text, not a control; (g) delete repo on a 409 renders the server's detail sentence in the error surface with correlation_id, visually distinct from the generic-outage copy (assert the detail text appears).

- [ ] **Step 2: Implement queries + forms** — `projectsQueryOptions` (`['projects']` — the new-task form's spelling, shared now; move it per the clamp), `projectQueryOptions(id)`, forms on `useAppForm` with zod schemas mirroring the request DTOs (`z.object({ name: z.string().min(1), description: z.string() })`; repo: `forge: z.enum(['github','gitlab'])`, `slug: z.string().min(1)`, `default_branch: z.string().min(1)`, `credential_id: z.string().min(1)`). Mutations invalidate `['projects']` / `['project-repos', projectId]`.

- [ ] **Step 3: The two projections** — phone-first cards; `md:` renders the repo list as a table (`<table>` with the semantic tokens; the card list hides at `md:` and vice versa INSIDE one component tree — no duplicated data fetching). Desktop's truth is only falsifiable in Task 32's project; unit tests assert both structures exist.

- [ ] **Step 4: Suites + routes regen + commit**

```bash
git add web/src/projects/ web/src/routes/_app/ web/src/routeTree.gen.ts web/src/test/render-app.tsx
git commit -m "web: Repos is real — project cards, project detail, typed repo forms, readable delete refusals"
```

---

### Task 20: Project grants, and recent activity per repo

Grants live on the project detail (the API is project-nested; a grant is a property of a project). Recent activity per repo rides Task 15's filter — the locked page-map surface §10.8 deferred onto it.

**Files:**
- Create: `web/src/projects/grants-section.tsx` (+ test), `web/src/projects/repo-activity.tsx` (+ test)
- Modify: `web/src/routes/_app/repos.$projectId.tsx`

**Interfaces:**
- Consumes: `listGrants`/`setGrant`/`deleteGrant` (`GET|PUT /projects/{id}/grants`, `DELETE /projects/{id}/grants/{user_id}`); `listUsers` (ADMIN-ONLY — the grant form's user picker; on 403 the picker degrades to a uuid text input with help text, honest not silent); `runsQueryOptions({ repo_id })` from Task 15.

- [ ] **Step 1: Failing tests** — (a) grants section lists `{email, level}` rows with a remove control and a set-grant form (user picker + level select `view|run|approve|manage`; READ the grant level vocabulary from the generated `SetGrantRequest`/`GrantResponse` types and use ITS values, not this plan's guess — correct in place if they differ); (b) setting a grant PUTs and invalidates the grants query; removing DELETEs; (c) per-repo activity: each repo row (or its expansion) renders the last few runs via `['runs', { repo_id }]` — assert the filter param reached MSW and rows render as **`RunCard`s exactly as they are today** (phase dots + the run's status); empty state "No runs have touched this repo."

**NOT Task 6's status line — the list contract cannot carry it.** `PhaseSummaryResponse` is one dot: `node_id`, `node_type`, `status`, `attempt`, `generation` (`runs/api/dto.go:142-155`). It has no `max_attempts` and no `outcome`, both of which `phaseStatusLine` requires, so the formatter cannot run on a runs-list row at all. Task 6's line lives on run DETAIL, where `PhaseResponse` carries the outcome. Widening the list DTO to feed it would put an outcome object on every dot of every row of every list — a contract change with a real payload cost, for a surface nobody asked for. If a later row wants it, that is a DTO task with its own `contracts:gen`; it is not a parenthetical in a UI task.
- [ ] **Step 2: Implement** — `grantsQueryOptions(projectId)` keyed `['grants', projectId]` + AUTH_SCOPED; activity reuses `RunCard` (it is THE card, per its own comment).
- [ ] **Step 3: Suites + commit**

```bash
git add web/src/projects/ web/src/routes/_app/repos.\$projectId.tsx
git commit -m "web: project grants on the project detail, and per-repo recent activity on the pulled-in filter"
```

---

### Task 21: Admin — Users, and the honest placeholder

The `admin.tsx` stub becomes the four-section screen; this task ships the frame, the Users section, and the visible row-09 placeholder ("Policy baselines and the kill switch arrive with row 09" — a named card, not silence). Sections are URL-as-state (`?section=users|harnesses|credentials|audit`, default users).

**Files:**
- Create: `web/src/admin/users-section.tsx` (+ test), `web/src/admin/admin-nav.ts`
- Modify: `web/src/routes/_app/admin.tsx` (stub → screen, validateSearch for `section` with read-site re-narrowing)

**Interfaces:**
- Consumes: `listUsers`/`createUser`/`updateUser` (list/create/role-edit — ALL EXIST; pre-flight: there is NO password-reset endpoint and this screen must not promise one).

- [ ] **Step 1: Failing tests** — (a) member hitting `/admin` sees the 403 problem surface (the route renders; the queries 403 — assert the error panel with correlation_id, not a blank); (b) admin sees four section tabs + the placeholder card naming row 09; (c) Users: rows `{email, role, active}`; create form (email, password, role select) → POST, list invalidated; role edit (member↔admin) via PATCH `{role}`; deactivate via PATCH `{active: false}`; (d) the API's own-row guard surfaces: PATCHing yourself answers 403 "cannot change own role or active flag" — rendered as the form/row error, asserted; (e) NO password-reset control exists (assert absence — `queryByText(/password/i)` null in the row actions; the create form's password field is the one exception).
- [ ] **Step 2: Implement** — `usersQueryOptions` `['users']` + AUTH_SCOPED; forms on `useAppForm`; the section tabs are `<Link search>`s.
- [ ] **Step 3: Suites + commit**

```bash
git add web/src/admin/ web/src/routes/_app/admin.tsx web/src/routeTree.gen.ts web/src/test/render-app.tsx
git commit -m "web: Admin frame + Users — list, create, role edit, and a placeholder that names row 09"
```

---

### Task 22: Admin — Harnesses and Credentials

The registry and the credential surface the expired-token finding was invisible without. Credentials list shows kind, **fingerprint** (Task 17 — explicit "n/a — only agent auth files carry one" for the three NULL kinds), `secret_version`, `last_used_at`, and **`last_auth_expired_at`** — the column that finally gets a face; when set, the row states it plainly ("auth expired <time> — rotate the secret").

**Files:**
- Create: `web/src/admin/harnesses-section.tsx`, `web/src/admin/credentials-section.tsx` (+ tests)
- Modify: `web/src/routes/_app/admin.tsx`

**Interfaces:**
- Consumes: `registerHarness`/`listHarnesses`/`updateHarness`/`deleteHarness`; `createCredential`/`listCredentials`/`rotateCredentialSecret` (`PUT /credentials/{id}/secret`)/`deleteCredential`. READ the request DTOs from the generated types for exact field names (harness: name, kind `claude-code|codex`, image ref, provider endpoint, credential binding, defaults JSON; credential: name, kind, secret) and follow them.

- [ ] **Step 1: Failing tests** — (a) harness rows show name/kind/image/credential; register form's credential picker filters by `harnessCredentialKinds` (claude-code → subscription_token|model_key; codex → agent_auth_file|model_key) — fixture holds all four kinds, assert the picker narrows per selected harness kind (the backend enforces it too; the picker prevents the round-trip); defaults JSON is a textarea validated as JSON client-side before submit; (b) credential rows: all four kinds in the fixture — the agent_auth_file row shows its 64-hex fingerprint, the other three show "n/a"; a row with `last_auth_expired_at` set renders the expiry sentence with the timestamp; (c) rotate: a per-row control opening a secret textarea → PUT to `/credentials/{id}/secret`, list invalidated; (d) delete on a 409 ("credential is referenced by a repo or harness") renders the server's sentence; (e) the secret value is NEVER rendered anywhere (assert absence after create/rotate).
- [ ] **Step 2: Implement** — `harnessesQueryOptions` `['harnesses']`, `credentialsQueryOptions` `['credentials']`, both AUTH_SCOPED; forms on `useAppForm`.
- [ ] **Step 3: Suites + commit**

```bash
git add web/src/admin/ web/src/routes/_app/admin.tsx
git commit -m "web: Admin harnesses + credentials — the registry, fingerprints with an honest n/a, and last_auth_expired_at gets its face"
```

---

### Task 23: Admin — Audit

The §3.1 endpoint's face: newest-first, load-more on the cursor.

**Files:**
- Create: `web/src/audit/audit-query.ts`, `web/src/admin/audit-section.tsx` (+ test)
- Modify: `web/src/routes/_app/admin.tsx`, `web/src/test/render-app.tsx`

- [ ] **Step 1: Failing tests** — rows render `occurred_at`, `action`, `subject_kind/subject_id`, actor (or "system" when `actor_user_id` is null — the login sweep writes system rows; assert both renderings), and `correlation_id`; `detail` renders as a collapsed `<details>` JSON block (this one IS a JSON surface — audit detail is genuinely shapeless); load-more follows `next_cursor` (MSW asserts the cursor arrived); empty state "The audit log is empty."
- [ ] **Step 2: Implement** — `auditInfiniteQueryOptions` keyed `['audit']`, AUTH_SCOPED, `getNextPageParam` off `next_cursor`.
- [ ] **Step 3: Suites + commit**

```bash
git add web/src/audit/ web/src/admin/ web/src/routes/_app/admin.tsx web/src/test/render-app.tsx
git commit -m "web: Admin audit — the table written since Plan 02 becomes readable"
```

---

# Wave 2 — Workflows

The locked design is variant B: a typed outline of step cards with a live read-only DAG preview above. The engine's ground truth (pre-flight 13): edges are FULLY EXPLICIT, the editor synthesizes every one — including the review-bypass edge — and "chain-shaped" is defined by ROUND-TRIP (derive(nodes) reproduces the stored edge set), not by graph shape, because an editor-produced document is not edge-linear.

---

### Task 24: Workflows list, the two-write New flow, detail, and the version read view

Creation is TWO writes: `POST /workflows` (metadata) then the editor on the new id for the initial version. A workflow with no versions is a legal row (`latest_version: 0`) — the list shows "no versions yet" and opening it resumes the initial-version editor; a failed first save strands nothing and duplicates nothing.

**Files:**
- Modify: `backend/internal/workflows/api/dto.go` — `required:"true"` on **every** field of `WorkflowVersionResponse`, `Document` INCLUDED, plus the Meta/List family. The first draft exempted `Document` on the grounds that "the list projection omits it, so required would claim presence the meta rows don't have" — **that is false and the tree says so**: the list uses a SEPARATE type, `WorkflowVersionMetaResponse` (`dto.go:54-58`), which has no `Document` field at all, while `versionResponse` unconditionally assigns the document on the full type (`dto.go:70-76`). There is no shape in which a `WorkflowVersionResponse` lacks its document. So: drop `Document`'s `omitempty` (it contradicts required, the same pairing Task 17 fixes on `LastUsedAt`), mark it required, and leave the meta type document-free. Without this the editor and the version read view — Tasks 24, 26, 27, the whole of wave 2 — read the one field they exist to render through an optional type, which is exactly what the global `required:"true"` constraint is for.

  Two things to CHECK at task time rather than assume, both cheap: (a) what schema swaggest emits for a `json.RawMessage` field — it is a `[]byte` and may reflect as a string or as an untyped object; run `task contracts:gen` and read the `openapi.json` diff before building the editor on the generated type, and if the emitted type is unusable, say so in the commit message and give the field an explicit schema rather than silently reverting the mark; (b) that `required` on it does not trip the `$ref` rule — it will not, since `json.RawMessage` is not a pointer-to-named-struct, but the rule is worth naming in the field comment so the next reader does not re-litigate it.
- Create: `web/src/workflows/workflows-queries.ts` (list/detail/versions/version), `workflow-form.tsx` (+ tests)
- Create: `web/src/routes/_app/workflows.index.tsx`, `workflows.$workflowId.tsx`, `workflows.$workflowId.versions.$version.tsx` (+ tests)
- Delete: `web/src/routes/_app/workflows.tsx` (stub)

**Interfaces:**
- Consumes: all eight workflows operations (Task fact sheet: `createWorkflow` 201, `listWorkflows`, `getWorkflow`, `updateWorkflow` PATCH, `deleteWorkflow`, `createWorkflowVersion` 201/**200-replay** (pre-flight 11), `listWorkflowVersions` newest-first, `getWorkflowVersion`).
- Produces: `workflowsQueryOptions` — REUSES the `['workflows']` key the new-task form already owns (promote per the clamp); `workflowQueryOptions(id)` `['workflow', id]`; `workflowVersionsQueryOptions(id)` `['workflow', id, 'versions']`; `workflowVersionQueryOptions(id, v)` `['workflow', id, 'version', v]` — versions are immutable: `staleTime: Infinity`, no AUTH_SCOPED (the artifact-query precedent, stated in a comment).

- [ ] **Step 1: Failing route tests** — (a) list: card per workflow (name, description, `v{latest_version}`); a `latest_version: 0` row renders "no versions yet — finish setup"; empty state "No workflows are defined yet."; (b) **New workflow**: a form (name, description) POSTs metadata, then NAVIGATES to `/workflows/$workflowId/edit` (Task 27's route — the editor in initial-version mode); assert the navigation target; a 409 on a duplicate name lands on the name field or banner per the mapper; (c) opening a no-versions workflow's card resumes the same editor route; (d) detail: PATCH-editable name/description (small `useAppForm`), version history newest-first, each row `v{n}` + `content_hash` (truncated, full on title attr) + created_at, linking to the version view; (e) version view fetches the document and renders the DAG preview region + outline region (both are Task 26/27's components — this task mounts placeholders asserting the SLOTS exist, replaced in those tasks; the test asserts the document reached the page by asserting a node id renders).
- [ ] **Step 2: Implement.** The two-write flow's failure honesty: if the metadata POST succeeds and navigation happens, there is nothing else to do — the "no versions yet" list state IS the recovery path; write that sentence as a comment on the mutation.
- [ ] **Step 3: Contracts (backend markup) + suites + commit**

```bash
git add backend/internal/workflows/api/dto.go backend/api/openapi.json web/src/api/schema.d.ts web/src/workflows/ web/src/routes/_app/ web/src/routeTree.gen.ts
git commit -m "web: Workflows list/detail/version views + the two-write New flow; version DTOs get their marks"
```

---

### Task 25: The derivation engine — pure TS, the property test's subject

Cards → edges, per-type consumption rules over the WHOLE ordered sequence (adjacency is provably wrong: in `implement → review → gate → git-publish` the gate examines the implement's patch PAST the review — the bypass edge — while also consuming the review's verdict). Plus: the round-trip chain-shape check, the V1 grammar, and node-id derivation. No React, no IO — this module is what the corpus generator and the editor both import.

**Files:**
- Create: `web/src/workflows/derive.ts`, `web/src/workflows/derive.test.ts`

**Interfaces:**
- Produces:

```ts
export type CardType = 'plan' | 'implement' | 'review' | 'gate' | 'git-publish'
export type Card = {
  id: string            // ^[a-z][a-z0-9_-]{0,63}$ — the validator's exact pattern
  type: CardType
  harness?: string      // agentic cards only
  instructions?: string // agentic cards only
  revision?: { target: string; maxRounds: number } // gate cards only, target = a preceding plan/implement id
}
// The wire shapes, exactly definition.go's. `edges` is OPTIONAL because
// toDocument omits the key entirely when the set is empty (the canonical
// absent form — Document.Edges carries `omitempty`); declaring it required
// makes that emit fail to type-check.
export type Doc = { schema: 2; nodes: DocNode[]; edges?: DocEdge[] }
export function deriveEdges(cards: readonly Card[]): DocEdge[]
export function toDocument(cards: readonly Card[]): Doc
export function isChainShaped(doc: { nodes: DocNode[]; edges?: DocEdge[] }): boolean  // round-trip equality
/** COMPLETE-document legality: this sequence is saveable as-is. */
export function grammarViolation(cards: readonly Card[]): string | null
/**
 * PREFIX legality: this sequence is a valid partial state on the way to some
 * complete one. `[]`, `[implement]` and `[implement, review]` are all legal
 * prefixes and NONE of them is a legal complete document — an editor lives in
 * this state for its entire authoring session, so a reducer invariant written
 * against grammarViolation would refuse every ordinary construction step
 * (round 1's blocker). This is the reducer's invariant; grammarViolation is
 * SAVE's precondition. `null` = completable.
 */
export function prefixViolation(cards: readonly Card[]): string | null
export function legalTypesAt(cards: readonly Card[], index: number): CardType[]
export function nextCardId(cards: readonly Card[], type: CardType): string  // plan-1, impl-1, …
```

- [ ] **Step 1: Write the failing tests.** The anchor case is the engine's own canonical bytes (fact sheet, verified against the real validator):

```ts
it('derives the canonical conveyor edge set — bypass edge included', () => {
  const cards: Card[] = [
    { id: 'im', type: 'implement', harness: 'cc', instructions: 'y' },
    { id: 'rev', type: 'review', harness: 'cc', instructions: 'r' },
    { id: 'g', type: 'gate', revision: { target: 'im', maxRounds: 2 } },
    { id: 'pub', type: 'git-publish' },
  ]
  expect(new Set(deriveEdges(cards).map((e) => `${e.from}→${e.to}:${e.artifact}`))).toEqual(
    new Set(['im→rev:patch', 'rev→g:review_verdict', 'im→g:patch', 'g→pub:patch']),
  )
})
```

Further cases, each one derivation rule: plan feeds the FIRST agentic card (`plan→impl-1:plan`); each implement takes the previous implement's patch (`impl-1→impl-2:patch` on an implement+ chain); review takes the nearest preceding patch; a gate with no review consumes no verdict; git-publish takes the gate's pass-through patch (`g→pub:patch` — from the GATE, which is how the validator's pass-through works); `toDocument` emits `{schema: 2}` with nodes in card order and NO `edges` key when empty (the parse discipline hashes `[]` and absent identically, but emit the canonical absent form); round-trip: `isChainShaped(toDocument(cards))` is true for every grammar-legal sequence, false for the canonical conveyor with one edge REMOVED, one ADDED, or one artifact retyped (set equality on `(from,to,artifact)` triples — order-insensitive); grammar: `plan? · implement+ · review? · gate · git-publish` refusals — a leading review ("review needs a patch before it"), two gates, a missing publish, a missing implement — each `grammarViolation` returns a human sentence naming the card; **and the prefix/complete split gets its own table**: `prefixViolation([])`, `prefixViolation([implement])` and `prefixViolation([implement, review])` are all `null` while `grammarViolation` on each is non-null (nothing is saveable yet), whereas a leading review and a second gate are non-null under BOTH (no completion rescues them) — the pair of tables is what stops the two notions from silently collapsing back into one; `legalTypesAt` at each position of a partial sequence offers exactly the types that keep a legal completion reachable (its answer and `prefixViolation` are the same judgement asked two ways — assert they agree: appending any type `legalTypesAt` offers must leave `prefixViolation` null, and appending any type it withholds must not); `nextCardId` yields `impl-2` after `impl-1`, skips a user-renamed collision, and always matches the validator's charset.

- [ ] **Step 2: Run, watch fail. Implement.** Derivation walks the sequence tracking `lastPatchProducer`, `pendingPlan`, `reviewOf(lastPatch)`; the rules are the spec §6.3 sentences transcribed — write each rule's sentence as the comment above its branch, citing `validate.go`'s consumption table as the thing each edge must satisfy. Grammar is a tiny state machine over positions; V1 pins EXACTLY ONE gate and ONE publish (the family `genConveyor` draws from), and `revision.target` legality = a PRECEDING plan/implement id (review is never offered — the bypass edge means it can never dominate the gate; cite `validate_test.go:249-255` in the comment).

- [ ] **Step 3: Suites + a hand-run against the real validator** — write the canonical case's `toDocument` output to a temp file and POST it through `createWorkflowVersion` against a dev server ONCE (`task backend:run` + curl), recording the 201. This is the pinned-surface rule applied to our own emit shape: prove one real acceptance before building three tasks on top. (The systematic proof is Task 28's corpus; this is the smoke.)

- [ ] **Step 4: Commit**

```bash
git add web/src/workflows/derive.ts web/src/workflows/derive.test.ts
git commit -m "web: the editor's derivation engine — explicit edges incl. the bypass, round-trip chain check, V1 grammar"
```

---

### Task 26: The DAG preview

A dependency-free SVG layout rendering ANY document — including branching ones authored via the API — nodes badged by type in the mockup's colour language (agent / gate / git; the eleven semantic tokens only). Used by the version read view now, the editor live in Task 27 (locked variant B puts the live preview IN the editor).

**Files:**
- Create: `web/src/workflows/dag-preview.tsx`, `dag-layout.ts`, `dag-preview.test.tsx`, `dag-layout.test.ts`

**Interfaces:**
- Consumes: `{ nodes: {id, type}[], edges?: {from, to, artifact}[] }` — the document's shape, NOT the Card type (it renders non-editor documents too).
- Produces: `layoutDag(doc): { columns: string[][]; edges: LaidEdge[] }` (pure — longest-path layering: a node's column = 1 + max(column of edge sources), sources sorted stably by document order; no crossing minimization, V1 documents are narrow); `<DagPreview doc={…} />` rendering `<svg>` with a node pill per id (fill by `data-node-type` — the phase-dots convention: agentic → accent family, gate → warning square, git-publish → success) and a line per edge. Overflow-x scrolls inside the component; the page never scrolls horizontally.

- [ ] **Step 1: Failing layout tests** — the canonical conveyor lays out as columns `[[im],[rev],[g],[pub]]` with the bypass edge spanning im→g; a branching fixture (two implements consuming one plan — API-authored shape) puts both in column 2; a single-node doc renders one column; edges reference laid coordinates (each LaidEdge carries from/to column+row indexes).
- [ ] **Step 2: Failing render tests** — one `[data-node-type]` pill per node; `<svg>` has `role="img"` and an `aria-label` naming the workflow shape ("4 steps: implement, review, gate, git-publish"); a document with zero edges renders pills and no lines.
- [ ] **Step 3: Implement** (layout ~40 lines; the SVG maps columns to x, rows to y, fixed pill size, `<line>` per edge with a marker). No graph library — the spec says so and the pin lane enforces the no-new-deps stance.
- [ ] **Step 4: Mount it** in `workflows.$workflowId.versions.$version.tsx` (replacing Task 24's slot); route test asserts pills render for the fixture document.
- [ ] **Step 5: Commit**

```bash
git add web/src/workflows/dag-*.* web/src/routes/_app/workflows.\$workflowId.versions.\$version.tsx
git commit -m "web: the DAG preview — dependency-free layered SVG, renders any document"
```

---

### Task 27: The outline editor

Ordered typed cards: add step → pick type (grammar-filtered menu) → fill the type's fields; drag to reorder by the grip (refused with the reason on the card when it breaks the grammar); node ids auto-derive, editable for taste; gate cards carry the revision loop (max rounds 1–10; targets = preceding plan/implement only); agentic cards get a harness picker (from the registry — pre-flight 14: names are unvalidated at save, the picker is the guard) and an instructions textarea (8192-byte limit mirrored inline); the live DAG preview above, updating from the derived edges as cards change; drafts in localStorage per workflow. A new version starts from scratch or prefilled from a chain-shaped version; non-chain documents open read-only with "authored outside the editor".

**Files:**
- Create: `web/src/workflows/editor-state.ts` (+ test) — the reducer (the property test's other subject)
- Create: `web/src/workflows/editor-cards.tsx`, `card-registry.ts` (+ tests), `web/src/workflows/drafts.ts` (+ test)
- Create: `web/src/workflows/outline-view.tsx` (+ test) — the READ-ONLY typed outline, shared by the editor's card list and the version read view
- Create: `web/src/routes/_app/workflows.$workflowId.edit.tsx` (+ test)
- **Modify: `web/src/routes/_app/workflows.$workflowId.versions.$version.tsx`** — Task 24 mounted TWO placeholder slots there (DAG and outline) and Task 26 replaced only the DAG. This task replaces the other one; without it the version read view ships a permanent placeholder and §6.1's read view is never finished. The read-only outline is the same renderer the editor uses with its controls off (one component, a `readOnly` prop — the 2+-importer clamp puts it at the group root), so a foreign document and a chain-shaped one both render, and the editor's "authored outside the editor" read-only mode gets its body from the same place rather than a second one.

**Interfaces:**
- Consumes: Task 25's whole surface; `harnessesQueryOptions` (Task 22); `workflowVersionQueryOptions` for prefill.
- Produces: `editorReducer(state, action)` with actions `add(type, at)`, `remove(id)`, `move(from, to)`, `retarget(gateId, target)`, `setField(id, field, value)`, `rename(id, newId)` — every transition either applies or returns the SAME state plus a refusal string bound to the offending card. **Every transition is judged by `prefixViolation`, `move` included** — `add` consults `legalTypesAt` (its positional form of the same question) and `move` re-checks `prefixViolation` on the reordered sequence. Never `grammarViolation`: an editor mid-construction has no complete document, so judging a reorder by complete-document legality refuses ordinary reorders on an incomplete outline — the exact failure the prefix/complete split exists to remove, and one that survived the split's first draft inside this very sentence. `grammarViolation` appears in ONE place in the whole editor: Save. `CARD_REGISTRY: Record<CardType, CardSpec>` — the per-type field spec Task 29's coverage test walks.

- [ ] **Step 1: Failing reducer tests** — add offers/accepts only legal types per position (a review before any implement is refused with its reason); move that would put publish before the gate is refused, state unchanged, reason present; retarget to a non-preceding node refused; rename validates the charset + uniqueness (the validator's exact pattern and its 64-byte cap); remove of the implement a gate's revision targets clears/retargets the revision with a visible note (decide: clear + note — never silently keep a dangling target); **every applied transition leaves a legal PREFIX** — `prefixViolation(state.cards) === null` after every accepted op, including the empty start and every partial sequence on the way up (an invariant test iterating a scripted sequence). It is deliberately NOT `grammarViolation`: an editor is incomplete for its whole authoring session, and holding the reducer to complete-document legality would refuse the first card ever added. `grammarViolation` is checked in ONE place — save (Task 28), which is where incompleteness actually matters — and the editor surfaces it as a disabled Save with the reason, not as a refused keystroke.
- [ ] **Step 2: Failing component tests** — (a) the add-step menu at each position lists `legalTypesAt`'s answer exactly; (b) a gate card's target select lists preceding plan/implement ids only (fixture with review present — review absent from the options); (c) instructions over 8192 bytes shows the inline byte-count error BEFORE save (the validator's wording: bytes, not characters); (d) the preview region re-renders on card edits (add an implement → a new pill appears — live derivation is pure, no network); (e) drafts: edits persist to `localStorage` key `caesar.workflow-draft.<workflowId>` (the theme key's naming convention) debounced; reload (remount) restores; a successful save CLEARS the draft; (f) prefill: opening `/edit?from=<version>` with a chain-shaped fixture populates cards; with a NON-chain fixture the editor refuses into read-only mode showing "authored outside the editor" and the DAG preview (assert no card controls render); (g) drag reorder by the grip: fire pointer/drag events on the grip to move card 2 above card 1 and assert the order changed — hand-rolled HTML5 drag (draggable on the grip only) or pointer-based reorder; NO new dependency; keyboard fallback buttons (↑/↓ on each card) exist and are the thing the unit test drives if jsdom's drag events prove unfaithful — the e2e (Task 28's spec) drives the real drag.
- [ ] **Step 3: Implement.** The registry drives card rendering:

```ts
/**
 * Every field carries its WIRE name — the JSON key on the engine's Node
 * (definition.go:92-98) — because the editor's label and the engine's key
 * differ for the gate: the card says "revision", the document says
 * "on_request_changes". Task 29's coverage test joins on `wire`, so a registry
 * that dropped it would fail on a naming difference and invite someone to
 * rename one side, losing the mapping the save path needs.
 */
export const CARD_REGISTRY = {
  plan:      { fields: [{ name: 'harness', wire: 'harness' }, { name: 'instructions', wire: 'instructions' }] },
  implement: { fields: [{ name: 'harness', wire: 'harness' }, { name: 'instructions', wire: 'instructions' }] },
  review:    { fields: [{ name: 'harness', wire: 'harness' }, { name: 'instructions', wire: 'instructions' }] },
  gate:      { fields: [{ name: 'revision', wire: 'on_request_changes' }] }, // optional; maxRounds 1-10
  'git-publish': { fields: [] },
} as const satisfies Record<CardType, { fields: readonly { name: string; wire: string }[] }>
```

(Task 29 proves this table against the ENGINE — do not decorate it beyond what the descriptor will carry.) Editor state lives in a `useReducer`; the route hosts `DagPreview(toDocument(cards))` above the card list. The forms are hand-authored typed inputs (spec §10.9's ruling) — reuse `field-frame.tsx`'s pieces, not `useAppForm` (this is not a flat form; RHF's `useFieldArray` has no precedent here and the reducer IS the state owner — say so in a comment).

- [ ] **Step 4: Retire the version read view's outline placeholder** — mount `OutlineView` (readOnly) beneath the DAG preview in `workflows.$workflowId.versions.$version.tsx`, replacing Task 24's slot. Route tests: a chain-shaped fixture renders one row per node with its type badge and its typed fields; a BRANCHING fixture (two implements off one plan — API-authored, not editor-producible) still renders every node in document order rather than blanking, since this view renders any document the API will return. Assert no editable control is present in either.

- [ ] **Step 5: Suites + commit**

```bash
git add web/src/workflows/ web/src/routes/_app/workflows.\$workflowId.edit.tsx web/src/routes/_app/workflows.\$workflowId.versions.\$version.tsx web/src/routeTree.gen.ts
git commit -m "web: the outline editor — grammar-structural cards, live preview, drafts, read-only for foreign documents"
```

---

### Task 28: Save, the property test, and the seeded corpus the Go validator judges

Two halves. (1) Save: POST through `createWorkflowVersion`; 201 AND the undeclared 200-replay both mean success (pre-flight 11); a 400's field errors map POSITIONALLY back onto cards (`nodes[2].harness` → card index 2 — pre-flight 12; parse-shaped rejections with no fields land on the form banner). (2) The acceptance bar (spec §6.3): random editor operation sequences within the grammar derive documents that parse and validate SERVER-side — subject: the TS reducer + derivation; verdict: the real Go `Validate`, through a seeded committed corpus riding the regen-diff gate. `genConveyor` stays what it is.

**Files:**
- Modify: `web/src/routes/_app/workflows.$workflowId.edit.tsx` (save), `web/src/workflows/editor-state.ts` (error binding)
- Create: `web/src/workflows/editor-property.test.ts`, `web/src/workflows/op-generator.ts`
- Create: `web/tools/gen-editor-corpus.mts` (imports `op-generator`, **`editor-state`** and `derive` — the reducer is in the pipeline, not beside it)
- Modify: `backend/internal/workflows/definition.go` — add `NodeTypes() []NodeType` and make `Valid()` range over it, so the corpus's coverage assertion and Task 29's descriptor read ONE vocabulary
- Create: `backend/internal/workflows/editorcorpus_test.go`; generated `backend/internal/workflows/testdata/editor_corpus/*.json` + `manifest.json`
- Modify: `Taskfile.yml` (`contracts:gen` grows the corpus step; `contracts:check`'s diff paths grow the corpus dir) + `scripts/gate` (same commit)

**Interfaces:**
- Produces: `generateOps(seed: number, count: number): Action[]` — a hand-rolled mulberry32 PRNG (NO fast-check; a new dependency needs its own install-and-verify cycle and pins — a 30-line generator over our own Action vocabulary does not); `SEEDS = [1, 2, …, 24] as const` — each seed emits one corpus document.

- [ ] **Step 1: Save + error mapping, test-first** — (a) save POSTs `toDocument(cards)`; on 201 navigate to the new version's view and clear the draft; on **200** do the same (MSW case with a 200 body — the replay); (b) a 400 fixture with `errors: [{field: "nodes[1].instructions", message: "required on agentic nodes"}]` binds the message to card index 1 (assert the card shows it) — the mapper parses `nodes[<i>].<field>` with a regex and falls back to the banner for `document`-field and detail-only rejections (both asserted).
- [ ] **Step 2: The property test** — `editor-property.test.ts`: for each of ~200 seeded runs (fast in CI — pure), `generateOps` drives the reducer from empty. **Invariant after every step: `prefixViolation(cards) === null`, or the op was refused-with-reason and the state is byte-identical to before.** (Not `grammarViolation` — the generator starts from `[]` and the first append can never produce a complete document, so a complete-legality invariant refuses the entire run. Round 1's blocker.) At sequence end, force-complete to a legal document (append the missing gate/publish through the reducer itself), assert `grammarViolation(cards) === null` — the completion actually completed — then assert `isChainShaped(toDocument(cards))` and local shape facts (exactly one gate, one publish, every edge's artifact matches the producer table). Deterministic: same seeds, same documents, no `Date.now`, no `Math.random` — mulberry32 only. One further invariant worth its line: a refused op must leave the state untouched, which is what makes "refused-with-reason" an escape hatch rather than a hole.
- [ ] **Step 3: The corpus — an EXACT output set, not an accumulating directory**

`gen-editor-corpus.mts` (run via `scripts/with-node node --experimental-strip-types`, the web:arch precedent) emits `SEEDS.length` documents as canonical JSON to `backend/internal/workflows/testdata/editor_corpus/seed-<n>.json`, byte-stable (stable key order comes free — `toDocument` builds objects in wire order; `JSON.stringify` with no spacing).

**It must REPLAY through the production `editorReducer`, not just call `derive`.** This task's own header names the subject as "the TS reducer + derivation", and the acceptance bar is that what the EDITOR produces validates server-side — but a generator importing only `op-generator` and `derive` never executes a line of the reducer, so a reducer mutation leaves every corpus document byte-identical and the Go lane green while the editor ships something else. The pipeline is therefore: `generateOps(seed, n)` → fold through `editorReducer` from the empty state (refusals are skipped, exactly as the UI would) → force-complete → `toDocument(state.cards)`. `derive` is then reached the way production reaches it, through the state the reducer actually built.

**The generator RECONCILES the directory**: before writing, it reads the existing `*.json` entries and deletes any that are not in this run's output set. Without that, dropping a seed from `SEEDS` leaves its stale-but-still-valid file behind forever — generation never touches it, the tree stays clean, `contracts:check` is green, and the Go judge happily validates a document no current rule produces (round 1's second blocker). It also emits `manifest.json` — the sorted filename list plus each file's sha256 — which is what makes the exact set assertable from the Go side without re-running Node.

Taskfile: a `contracts:editorcorpus` step inside `contracts:gen` (after `contracts:ts`), and the corpus dir added to `contracts:check`'s `git diff --exit-code` path list AND its porcelain assertion; `scripts/gate` updated in the same commit.
- [ ] **Step 4: The Go judge** — `editorcorpus_test.go` (no build tag — it must run in plain `backend:test`):

```go
func TestEditorCorpusValidates(t *testing.T) {
	// The manifest is the EXACT expected set. Globbing alone accepts an orphan
	// file left behind by a removed seed — still valid JSON, validated
	// forever, produced by no rule the editor still has.
	var man struct {
		Files map[string]string `json:"files"` // name -> sha256
	}
	raw, err := os.ReadFile("testdata/editor_corpus/manifest.json")
	if err != nil {
		t.Fatalf("editor corpus manifest missing: %v", err)
	}
	if err := json.Unmarshal(raw, &man); err != nil { t.Fatal(err) }
	if len(man.Files) == 0 {
		t.Fatal("editor corpus manifest is empty")
	}

	files, err := filepath.Glob("testdata/editor_corpus/seed-*.json")
	if err != nil { t.Fatal(err) }
	if len(files) != len(man.Files) {
		t.Fatalf("corpus has %d files, manifest declares %d — an orphan or a missing document",
			len(files), len(man.Files))
	}

	shapes := map[string]bool{}
	for _, f := range files {
		name := filepath.Base(f)
		raw, err := os.ReadFile(f)
		if err != nil { t.Fatal(err) }
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != man.Files[name] {
			t.Errorf("%s: sha256 %s, manifest says %s", name, got, man.Files[name])
			continue
		}
		doc, err := ParseDocument(raw)
		if err != nil { t.Fatalf("%s: parse: %v", name, err) }
		if v := Validate(doc); len(v) != 0 {
			t.Errorf("%s: %d violations, first: %s %s", name, len(v), v[0].Path, v[0].Message)
		}
		shapes[Hash(doc)] = true
	}

	// DIVERSITY. 24 seeds that all force-complete to the same minimal document
	// satisfy every assertion above while exercising one code path. The bar is
	// stated as a number so a generator that degenerates is RED, and every card
	// type must appear somewhere in the corpus.
	if len(shapes) < 8 {
		t.Errorf("corpus collapsed to %d distinct documents across %d seeds", len(shapes), len(files))
	}
	// ...plus: assert each NodeType appears in at least one corpus document,
	// iterating definition.go's OWN vocabulary. There is no slice today — the
	// five types are loose constants (definition.go:46-50) and `Valid()` is a
	// switch over them (:53) — so THIS task adds `func NodeTypes() []NodeType`
	// beside them and rewrites `Valid()` to range over it, which is the only
	// way "a new node type reddens the corpus" is true rather than hoped for.
	// NOT Descriptor(): that lives in Task 29,
	// which runs AFTER this one, and a Go test cannot reference a file the
	// declared task order has not created yet. Task 29 then consumes the same
	// vocabulary rather than minting a second copy.
}
```

Two vacuousness guards, both load-bearing: the manifest count catches an orphan or a missing document, and the diversity floor catches a generator that degenerates to one shape.

- [ ] **Step 5: The mutation proofs, all four recorded**

(i) Flip a TS derivation rule (bypass edge drawn from review instead of implement): regen → either the corpus DIFFS (`contracts:check` red) or, committed, the Go test REDDENS ("a gate examines exactly one plan or patch") — record which.

(ii) **Remove a seed from `SEEDS`** and regenerate. Expect: the reconciler deletes that seed's file and rewrites `manifest.json`, so `contracts:check`'s diff goes red on the deletion — and if the reconciler were absent, the orphan would survive with a clean tree and the Go test would still pass. Run it BOTH ways (with the reconciler, then with the reconciling lines commented out) and record the difference; that contrast is the proof the reconciler is load-bearing.

(iii) **Hand-write an orphan** `seed-999.json` containing a valid document, do not regenerate, run `task backend:test` → the manifest count assertion is red. (Note what this replaces: the earlier draft prescribed "delete one corpus file without regenerating → `contracts:check` red", which is **backwards** — `contracts:check` runs `contracts:gen` FIRST (`Taskfile.yml:259-264`), so a deleted generated file is restored byte-for-byte and the check is GREEN. Direct deletion of a current artifact is self-healing by design and is not a mutation this gate can catch; the orphan is.)

(iv) Weaken the grammar (allow two gates) → the property test's own invariant red.

(v) **Mutate the REDUCER** — make `move` a no-op that returns state unchanged — and regenerate. The corpus must DIFF (`contracts:check` red). If it does not, the generator is not replaying through `editorReducer` and Step 3's whole claim is false; this is the proof that the reducer is genuinely the corpus's subject rather than a module the pipeline happens to sit beside.

Restore all.
- [ ] **Step 6: The editor e2e** — one Playwright spec (mobile project) against the real backend: from `/workflows`, New workflow → metadata → the editor → add implement/review/gate/publish, fill fields (harness from the seed via `seedLabels`), drag one reorder, save → land on v1's version view with the DAG rendered; assert via the API (`request.get('/api/v1/workflows/...')`) that `latest_version` is 1. This is the "authors and saves against the real backend" bar; the webe2e seed already registers a harness.
- [ ] **Step 7: Full gate + commit**

```bash
git add web/ backend/internal/workflows/ Taskfile.yml scripts/gate
git commit -m "web+workflows: save with positional error binding; the seeded corpus makes the Go validator judge the TS editor"
```

---

### Task 29: The §10.9 descriptor — proved against the engine, covering the editor

Locked outline B's "fields generated from the engine's schema" ships as hand-authored forms + an exhaustive drift gate. The node-type VOCABULARY is emitted mechanically from `definition.go`; per-type applicability/limits/requiredness are NOT mechanically derivable (they live in `validate.go`'s branches), so every declared row is PROVED: a Go test builds a document violating the row and asserts `Validate` rejects it, and a conforming one passes. The web test asserts the card registry covers the descriptor exactly.

**Files:**
- Create: `backend/internal/workflows/descriptor.go`, `descriptor_test.go`, `cmd/descriptorgen/main.go`
- Create (generated): `backend/api/workflow-descriptor.json`
- Create: `web/src/workflows/descriptor-coverage.test.ts`
- Modify: `Taskfile.yml` (`contracts:gen` runs descriptorgen; `contracts:check` diff paths — `backend/api` glob may already cover it, VERIFY: the diff path list names `backend/api/openapi.json` specifically, so ADD the descriptor file) + `scripts/gate`

**Interfaces:**
- Produces: in Go —

```go
// FieldRule is one PROVEN statement about what Validate accepts. The vocabulary
// (which types exist, which are agentic, WHICH FIELDS A NODE HAS) is emitted
// from definition.go; the rules below are DECLARED — and descriptor_test.go
// convicts every one against Validate, which is what keeps this file honest
// (§10.9).
type FieldRule struct {
	NodeType    NodeType `json:"node_type"`
	// WireField is the JSON name on definition.go's Node — harness,
	// instructions, on_request_changes. It is the join key against the
	// mechanically enumerated vocabulary, so it must be the WIRE name and never
	// an editor-side label: the editor calls the gate's field "revision", and
	// CardSpec carries that mapping (below) rather than the descriptor
	// pretending the engine has a field by that name.
	WireField   string   `json:"wire_field"`
	Requirement string   `json:"requirement"`  // required | forbidden | optional
	MaxBytes    int      `json:"max_bytes,omitempty"`
	// Numeric bounds, for rules Validate expresses as an integer range rather
	// than a byte cap — on_request_changes.max_rounds is 1..10
	// (validate.go:18, :172-174). The first draft of this struct could not
	// express that at all while the prose claimed it did (round 1).
	MinInt      *int     `json:"min_int,omitempty"`
	MaxInt      *int     `json:"max_int,omitempty"`
	SubField    string   `json:"sub_field,omitempty"` // e.g. "max_rounds" under on_request_changes
}
func Descriptor() []FieldRule
```

Rows: harness+instructions required on plan/implement/review (max 100 / 8192 bytes), forbidden on gate/git-publish; on_request_changes optional on gate with `max_rounds` bounded 1–10, forbidden elsewhere. The JSON adds `{"node_types": [...], "agentic": [...], "node_fields": [...], "id_pattern": "^[a-z][a-z0-9_-]{0,63}$", "rules": [...]}`.

The node-type vocabulary comes from `NodeTypes()`, which **Task 28 adds** to `definition.go` — this task consumes it rather than minting a second list, so the corpus's coverage assertion and the descriptor can never disagree about which types exist.

**`node_fields` is what makes the gate detect an ADDED engine field.** Every assertion in the first draft iterated descriptor rows, so a new field or rule in `validate.go` with no descriptor row left every test green — the claim "a Go-side rule change breaks CI until descriptor AND editor both learn it" was not established by anything (round 1). The fix is a second, opposite-direction pass: `descriptorgen` reflects over `definition.Node`'s struct tags and emits the JSON field names (`id`, `type`, `harness`, `instructions`, `on_request_changes`), and `descriptor_test.go` asserts that **every** non-structural field (everything but `id` and `type`) has a `FieldRule` row for **every** `NodeType`. Add a field to `Node` and the descriptor is incomplete before anyone writes a rule for it.

- [ ] **Step 1: The proof table, test-first — in BOTH directions**

*Rows → engine (soundness).* `descriptor_test.go` iterates `Descriptor()`: for each `required` row, a minimal legal document MINUS that field must yield a violation whose Path names it and a conforming one zero violations on that path; for each `forbidden` row, a document WITH the field must be rejected; for each MaxBytes, the boundary (exactly max passes, max+1 fails — the validator counts BYTES; use a multibyte rune in one case); **for each MinInt/MaxInt, the four boundary documents** (min−1 rejected, min accepted, max accepted, max+1 rejected) with the violation Path naming the sub-field (`nodes[i].on_request_changes.max_rounds`). Build the minimal documents with a helper deriving from the canonical conveyor.

*Engine → rows (completeness).* A second test walks the emitted `node_fields` × `node_types` grid and fails on any cell with no `FieldRule`. This is the direction that catches an ADDED engine field, and it is the one the first draft lacked.

RED first, twice: declare one deliberately wrong row (instructions optional on review) and watch the soundness test convict it; then delete one true row and watch the completeness test convict the hole. Fix both — the recorded proof that the harness catches a lying descriptor AND an incomplete one.
- [ ] **Step 2: The generator** — `descriptorgen` marshals `Descriptor()` + the emitted vocabulary to `backend/api/workflow-descriptor.json` (stable field order, trailing newline); wire into `contracts:gen` + check paths + gate.
- [ ] **Step 3: The web coverage test** — `descriptor-coverage.test.ts` imports the JSON (vitest resolves JSON imports) and `CARD_REGISTRY` + the grammar's type list: every descriptor node_type has a card spec; every `required` field row appears in that card's fields; every `forbidden` field is ABSENT; the id pattern string equals the one `derive.ts` validates with (export the regex SOURCE from derive.ts and compare strings); **and the `max_rounds` bounds the gate card's input enforces equal the descriptor's MinInt/MaxInt** (Task 27 hard-codes 1–10 — this is where that copy gets held to the engine).

  The join is on the WIRE name, and `CardSpec` must carry it: the registry's gate field is called `revision` while the engine's is `on_request_changes`, so `CARD_REGISTRY` gains a per-field `wire` key and the test compares `wire` values, not labels. Without that the coverage test fails on a naming difference and an implementer "fixes" it by renaming one side — losing the mapping the editor actually needs.
- [ ] **Step 4: Mutation, both directions** — (i) add a fictitious `timeout` field row to `Descriptor()` → the Go soundness test red (no violation to convict) AND the web coverage red (no card field); (ii) add a real field to `definition.Node` (`Timeout int` with `json:"timeout,omitempty"`) WITHOUT a descriptor row → the Go completeness test red. Restore both. (ii) is the one that proves the claimed direction "a Go-side change breaks CI"; without it that sentence is an assertion about a test nobody wrote. Full gate + commit.

```bash
git add backend/ web/src/workflows/descriptor-coverage.test.ts Taskfile.yml scripts/gate backend/api/workflow-descriptor.json
git commit -m "workflows: the descriptor gate — every row convicted against Validate, the card registry held to it"
```

---

### Task 30a: Run history on the workflow detail

The §10.7 surface, on Task 15's filter: the workflow detail page grows its run history (newest first, `RunCard` rows, load-more unnecessary — one page at month-one volume, the residual stays named).

**Files:**
- Modify: `web/src/routes/_app/workflows.$workflowId.tsx` (+ test)

- [ ] **Step 1: Failing test** — the detail renders a "Runs" section fed by `['runs', { workflow_id }]` (MSW asserts the param); rows are `RunCard`s; empty state "No runs have used this workflow."
- [ ] **Step 2: Implement + suites + commit**

```bash
git add web/src/routes/_app/workflows.\$workflowId.tsx
git commit -m "web: run history per workflow definition — the locked page-map surface, un-deferred"
```

---

# Wave 3 — desktop, the shadow, the checkpoint

---

### Task 31: ⌘K

Scoped to what a single operator actually jumps to: the seven pages, "New task", and recent tasks/runs pulled from the QUERY CACHE — client-side matching only, no new endpoint, no cmdk (hand-rolled dialog; CSP is `script-src 'self'` and the pin lane guards the dependency list).

**Files:**
- Create: `web/src/shell/command-palette.tsx`, `command-palette.test.tsx`, `palette-entries.ts` (+ test)
- Modify: `web/src/shell/app-shell.tsx` (mount + the ⌘K listener)
- Modify: `web/src/routes/_app/tasks.tsx` — the new-task drawer moves from local `useState` to the `?new=1` search param (see Step 2), plus `validateSearch` and its read-site re-narrowing

**Interfaces:**
- Consumes: `PHONE_TABS` + `SECONDARY` (nav.tsx's exported lists — the seven pages come from the SOURCE, not a copy); `queryClient.getQueryData(['tasks'])` and `getQueryData(['runs', {}])` — read-only cache peeks (never fetches: an empty cache lists no recents, honestly).
- Produces: `paletteEntries(qc, query): Entry[]` (pure given the cache reads) — `Entry{label, kind: 'page'|'new-task'|'task'|'run', to, params?}`; the dialog opens on `metaKey+k` (and `ctrlKey+k`), arrows + Enter navigate, Escape closes, focus is trapped and restored.

- [ ] **Step 1: Failing tests** — (a) `paletteEntries` with a warm cache: typing `app` matches "Approvals" (substring, case-insensitive); typing a task title substring surfaces that task; a cold cache yields pages + New task only; (b) the dialog: `cmd+k` opens (fire the keydown on window), input is focused; ArrowDown+Enter navigates to the highlighted entry (assert via the router's location in renderApp); Escape closes and focus returns to the previously-focused element; (c) `role="dialog"` with `aria-label`, entries in a `role="listbox"` — the accessibility contract the desktop e2e drives later.
- [ ] **Step 2: Implement.** Overlay + panel styled with the semantic tokens; the listener lives in AppShell's effect (one listener, window-level, removed on unmount).

**"New task" must actually create a task, not merely land near the button.** The spec names it as one of the palette's three entry classes (§7), and an entry that navigates to `/tasks` and stops is a command that does not perform its command. The drawer is currently local state (`useState` in `NewTaskFab`, `tasks.tsx:175`), so the palette cannot reach it — the fix is the plan's own URL-as-state constraint applied one more time: `/tasks` gains a `new` search param, `NewTaskFab` derives `open` from it and writes it on open/close, and the palette entry navigates to `/tasks?new=1`. That also makes the drawer linkable and survives a reload, which the local-state version does not. Re-narrow the param at the read site (pre-flight 20). Assert it: selecting "New task" from the palette closes the palette AND opens the drawer, from a route other than `/tasks`.
- [ ] **Step 3: Suites + commit**

```bash
# tasks.tsx and its tests MUST be in this commit — Step 2 moves the drawer to a
# search param, and the gate refuses a dirty tree outright (scripts/gate:99).
git add web/src/shell/ web/src/routes/_app/tasks.tsx web/src/routes/_app/tasks.test.tsx
git commit -m "web: a hand-rolled command palette — pages, New task, and cache-fed recents"
```

Also re-run `task web:e2e` before committing: any existing spec that opens the new-task drawer by clicking the FAB still passes (the trigger is unchanged), but a spec that asserted the URL does not change when it opens will now fail, and that failure is correct — fix the spec, not the feature.

---

### Task 32: The desktop Playwright project, and the geometry debts paid

12a parked it deliberately ("would double the plan's slowest lane for a viewport Plan 12 owns"). It arrives: the same critical-path BEHAVIOR at a desktop viewport, the three waiting geometry assertions (pre-flight 22), and the split-view + ⌘K checks. "Same specs" cannot mean unchanged files: `e2e/approvals.spec.ts` asserts `desktop-rail` hidden, which MUST fail at 1280px — shell/geometry assertions become per-project, the shared critical path stays one set of specs.

**Files:**
- Modify: `web/playwright.config.ts`
- Modify: `web/e2e/approvals.spec.ts` (geometry becomes viewport-aware)
- Create: `web/e2e/desktop-geometry.spec.ts`

- [ ] **Step 1: The project**

```ts
projects: [
  { name: 'mobile', use: { ...devices['Pixel 7'] } },
  // Desktop owns: the three geometry assertions 12a parked (ledger, Task 16
  // note), split view, ⌘K. The lane cost is real and accepted — this plan is
  // the one that owns the viewport.
  { name: 'desktop', use: { ...devices['Desktop Chrome'] } },
],
```

`workers: 1` stays (one server, one database, quiesce is global — now across BOTH projects; Playwright runs projects serially under one worker, which is exactly what the shared inbox needs).

- [ ] **Step 2: Per-project geometry** — in `approvals.spec.ts`, the three-engine block branches on `test.info().project.name`: the phone arm keeps its three assertions VERBATIM (each engine keeps its measured mutant); the desktop arm asserts the inverse — rail visible, `phone-tabs` hidden (`toBeHidden` preceded by a `toHaveCount(1)` on the same testid: the bounding-box engine passes on a missing node, so the count guard is load-bearing — the block's own lesson applied in reverse).
- [ ] **Step 3: The debts** — `desktop-geometry.spec.ts`, desktop project only (`test.skip(({}, testInfo) => testInfo.project.name !== 'desktop')`):

```ts
test('the rail paints LEFT of main', async ({ page }) => {
  await signIn(page)
  const [rail, main] = [await page.getByTestId('desktop-rail').boundingBox(), await page.getByRole('main').boundingBox()]
  expect(rail!.x + rail!.width).toBeLessThanOrEqual(main!.x + 1) // md:order-first, finally falsifiable
})

test('run detail is two-pane at desktop', async ({ page, request }) => {
  const runId = await openGate(request)
  await signIn(page)
  await page.goto(`/runs/${runId}`)
  const [rail, detail] = [await page.getByTestId('phase-rail').boundingBox(), await page.getByTestId('phase-detail').boundingBox()]
  expect(rail!.x + rail!.width).toBeLessThanOrEqual(detail!.x + 1) // side by side, not stacked
})

test('⌘K opens, matches, navigates', async ({ page }) => {
  await signIn(page)
  await page.keyboard.press('ControlOrMeta+KeyK')
  await page.getByRole('dialog').getByRole('textbox').fill('workfl')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/workflows/)
})
```

(`phase-rail` needs its testid if it lacks one — add it in the same commit; check the component first.) Confirm the critical-path specs pass unchanged on desktop; where one asserts phone-only chrome, give it the same per-project branch rather than skipping the spec.

- [ ] **Step 4: Mutation** — remove `md:order-first` from nav.tsx → rail-left fails; restore. Run `task web:e2e` (both projects, one lane).
- [ ] **Step 5: Commit**

```bash
git add web/playwright.config.ts web/e2e/
git commit -m "e2e: the desktop project — 12a's three parked geometry assertions, split view, ⌘K"
```

---

### Task 33: The milestone's automated shadow

One Playwright spec walking the §1 sentence end-to-end against the fake harness and fake forge, in the DEPENDENCY-TRUE order with TWO credentials — and through the two clauses every existing spec stops short of: the PR link renders, and the decision appears in Approvals History. `gate-decision.spec.ts` ends at badge clearance; this spec exists because those last clauses were exactly the unshadowed ones. "UI is the only door" becomes CI-checked continuously, not walked once.

**Files:**
- Create: `web/e2e/milestone-walk.spec.ts`
- Modify: `web/e2e/harness.ts` (the stale "four control-server calls" comment → five; pre-flight 23)
- **Modify: `backend/internal/app/webe2e_test.go` AND `backend/internal/app/serve.go`** — the shadow creates its OWN assets through the UI and is the first e2e spec to reach `git-publish`, which the harness cannot currently serve. See Step 1; this is the task's real cost and it is not optional.

- [ ] **Step 1: The harness gap — THREE registrations, not one, and a composition-root change**

Read `webe2e_test.go`'s `e2eFixtures` and `serve.go`'s `serveOptions` before writing anything. The obvious plan — a `register-remote` fixture calling `executor.Remote(id, bare)` — is **necessary and nowhere near sufficient**, and the shortfall is silent: the spec would reach the gate, approve, and then fail at publish with an error about a URL nobody recognizes.

What the tree actually holds:

- `serveOptions` substitutes exactly two things, the executor and the listener (`serve.go:112-120`). Its own comment scopes the substitution deliberately: the agent CONTAINER is replaced, "everything below that substitution is production code."
- So publication runs the PRODUCTION `newForgeAdapter` and `gitPublisher` (`serve.go:185-212`). The publisher clones from the forge endpoints — which, in this harness, point at a stub that answers `{"sha": …}` to **every** request whatever the path (`webe2e_test.go:131-134`). It is a `ResolveHead` stand-in and nothing more; there is no repository behind it to clone and no PR endpoint to open.
- The doubles that would work exist and each needs its OWN registration: `fake.NewPublisher(remotes)` with `Publisher.Remote(repoID, bare)` (`fake/publisher.go:49-56`) and `fake.NewForge(heads)` with `Forge.Remote(repoID, bare)` (`fake/forge.go:90, 119`). Two maps, two registrations, plus the head SHA.

So this step is: **extend `serveOptions` with `publisher` and `forge` slots** alongside `executor`, following the executor slot's existing shape and extending its doc comment to say why the substitution boundary moved (the same reasoning applies — a fake forge is the network stand-in, everything below it stays production). Wire the served service and the control-side service in `webe2e_test.go` to ONE shared fake forge and ONE shared fake publisher — the control side currently builds its own `gitPublisher` (`webe2e_test.go:528`) and two instances would register remotes into different maps, which fails exactly the way "the API host and the clone host drift apart" is described at `serve.go:180`. Then `POST /fixture/register-remote {repo_id}` registers the new repo with **all three** — executor, publisher, forge (with its head SHA) — in one call, following `e2eFixtures`' style, raw-SQL-free.

Prove the wiring before writing the walk: assert the fake forge is the one serving by having the fixture return the registered head and checking a publish-shaped request reaches it. Without the executor registration the implement falls back to the synthetic patch and the gate screen shows nothing (the recorded 12a lesson at the top of that file); without the other two the walk dies at its last beat, which is the beat this whole spec exists to cover.
- [ ] **Step 2: The spec** — mobile project (the conveyor is phone-first; the desktop pass is the human checkpoint's job), `beforeEach` quiesce, generous per-beat timeouts under the existing 120s budget:

```
signIn →
Repos: New project (walk-<runid suffix>) → project detail →
Admin › Credentials: create forge_token cred → Repos: New repo bound to it →
  control: register-remote(repo id read from the UI or the API via request) →
Admin › Credentials: create subscription_token cred → Admin › Harnesses: register harness (claude-code, the fake's image ref, that cred) →
Workflows: New workflow → metadata → editor: implement+review+gate(revision→implement, 2 rounds)+git-publish → save →
Tasks: FAB → new task (project/repo/workflow selects → the ones just created) →
run detail: gate goes waiting (data-state wait) → open gate → REQUEST CHANGES with a note →
  generation 2 arrives (runState oracle: generation 2) → gate again → APPROVE →
run detail: the publish.pr_opened event row renders AS A LINK with an href (the fake forge's real ref) →
Approvals › History: the two decisions just made are the top rows — the request_changes row
  carries the note text and links attempt-precisely (click → the gen-1 gate's decision pane).
```

Every generation/status claim goes through `runState` (the backend oracle), not the DOM alone. Assert the PR link's href is non-empty and starts with the fake forge's URL shape — clicking external links is out of scope.

- [ ] **Step 3: Prove the spec can fail** — run it once with the History beat pointed at a decision that does not exist (temporary wrong filter) → red; restore. Then `task web:e2e` fully green, twice (flake check — the lane retries zero times by config, so a flaky shadow is a bug now, not later).
- [ ] **Step 4: Commit**

```bash
# serve.go too — Step 1 extends serveOptions, and the gate refuses a dirty tree.
git add web/e2e/ backend/internal/app/
git commit -m "e2e: the milestone walk, shadowed — two credentials, the editor, the PR link, and History, continuously"
```

---

### Task 34: The operator checkpoint

**Wave 3 ends at an operator assessment on a real deploy, and the assessment is a deliverable.** This task is sofo's, at a phone and then at a desk. Everything before it is preparation for it. Row 12b flips ONLY on this record.

**Files:**
- Modify: `docs/superpowers/ledgers/2026-08-26-caesar-12b-web-remainder.md` (create as the wave record)
- Modify: `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md` — row 12b

- [ ] **Step 1: Deploy the tested digest** — the Plan 11 release path: build once, `image-serves-ui` tests THAT digest (now including the in-image git assertions), promote THAT digest. Record it in the ledger. Note: the runtime image changed shape in Task 4 (alpine+git) — the deploy replaces `caesar-local`'s hand-built `caesar-git:dev` with the promoted image, which is the FIRST promoted image that can publish; say so in the ledger.
- [ ] **Step 2: The walk, phone first** — from a fresh browser session, no curl, no psql, in the dependency-true order: create a project → forge credential → repo (bound) → harness credential → harness → **define the workflow in the editor with PLAIN instructions** (this retires the `walk` v2 workaround — the definition must NOT name `.caesar/in` paths) → task → watch the run → request changes with a note → **witness generation 2 honor the note without the workaround** (§9's first named witness) → approve → open the PR → **read both decisions in Approvals History** (§9's second). Time each beat.
- [ ] **Step 3: The desktop pass** — a deliberate pass over the new surfaces at a desk: Repos, Admin (all four sections), Workflows (list → detail → version → editor), split view, ⌘K. Taste verdicts to the ledger, including the two 12a deferred to "once it's built": the FAB (Task 10 restyled it; verdict now), and the texty-ness/linking texture (finding 17's "polish once correctness and comprehensiveness are achieved" — this plan's exit is that bar).
- [ ] **Step 4: Re-measure the two instruments** — (a) the network panel during a run in flight: the per-endpoint drumbeat is gone (collections at most once per 5s under phase bursts; `/auth/me` flat at 60s — if it shows the exponential-flattening-at-30s shape instead, that is the STREAM flapping through the tunnel: record it as a deploy finding with its own row, not a client bug — pre-flight 1); (b) `attempt n/cap` vs the new status lines on a real failure if one occurs (an expired token is likely enough on the subscription clock — `rotate-creds.sh` is the refresh if it blocks the walk).
- [ ] **Step 5: Record the row** — flip 12b ONLY if steps 1–4 completed on the real deploy. Name explicitly in the ledger what 12b did NOT close: run cancel / policy / kill switch (row 09), live agent output (design pass, unowned row — slotting it is sofo's call, §10.10), multi-arch + TLS deploy shape (row 14), `GET /tasks` + waiting-arm pagination (named residual), branch editing (later projections), every assistant surface (row 13), row 08's both-forges live smoke (untouched, an operator event).
- [ ] **Step 6: Correct the documentation against what was built** — `web/README.md` gains the screens and the two things a next reader would get wrong (the two-tier bridge; the editor's corpus gate). **Then re-read this plan against the tree and correct it in place**; write execution's divergences into the ledger as a numbered list. Every plan in this repo has ended with execution finding things review did not — plan-08's pre-flight lesson says scan for them BEFORE executing too.

---

## Self-review

Run against the spec with fresh eyes after the plan was complete. Corrections were made inline; the notable ones are recorded rather than smoothed over.

### Spec coverage

| Spec | Where |
|---|---|
| §1 scope, the milestone sentence, two credentials, wave table | header; Global Constraints; Tasks 33, 34 |
| §1 scope decisions 1–4 | Task 4 (git stays); Tasks 21–23 (Admin's four sections); Tasks 25–29 (linear-first); Task 34 (walk = the bar) |
| §2.1 gate-note injection, workaround retired, test on assembled Instructions | Task 1; Task 34 step 2 |
| §2.2 outcome-class status lines (+ its plan-time verification) | pre-flight 3; Task 6 |
| §2.3 identical revision (+ verification) | pre-flight 4; Task 7 |
| §2.4 tab bar set / Retry / links + artifact view + attempt-precise / wordmark / FAB | Tasks 10, 11, 8, 9 |
| §2.5 two-tier refetch fan (+ both verifications) | pre-flight 1, 2; Task 12 |
| §2.6 git-in-image + in-image assertion / index migration / deadline expiry / go-mod-tidy | Tasks 4, 2, 5, 3 |
| §3.1 GET /audit | Task 13 |
| §3.2 GET /approvals/decisions + History tab | Tasks 14, 18 |
| §4 Repos: projects-as-unit, forms, RESTRICT classification, forge immutable | Tasks 16, 19, 20 |
| §5 Admin: users/harnesses/credentials(+fingerprint)/audit + placeholder | Tasks 17, 21, 22, 23 |
| §6.1 list/detail/version, DAG preview, two-write create, no-versions resume | Tasks 24, 26, 27 (the read view's outline) |
| §6.2 new-version-never-edit, typed cards, pickers, ids, drag, live preview | Task 27 |
| §6.3 derivation, round-trip chain-shape, grammar, property test via corpus | Tasks 25, 28 |
| §6.4 save via the one door, client-only drafts | Tasks 28, 27 |
| §7 ⌘K + desktop Playwright project | Tasks 31, 32 |
| §8 testing bar (endpoints, injection, editor, fan, shadow, house rules) | Tasks 13, 14, 1, 25–29, 12, 33; Global Constraints |
| §9 the closing checkpoint's two witnesses | Task 34 steps 2, 5 |
| §10.1–10.11 departures | 10.1/10.2/10.5 recorded (row 13 / kanban absent); 10.3 Task 21's placeholder; 10.4 unchanged (Task 24 keeps `tests` rendering); 10.6 Task 31; 10.7 Tasks 15+30a; 10.8 Tasks 15+20 + row 09 note; 10.9 Tasks 27+29; 10.10 Task 34 step 5; 10.11 deferred to row 09 (scope decision) |
| §11 residuals | Task 34 step 5 |

### Corrections the review forced

1. **Task 12's first draft asserted invalidation calls.** The spec's own warning ("would pass against today's bridge and prove nothing") forced the HTTP-counter shape with per-endpoint ceilings and the liveness half; the bridge tests keep the call-level assertions only for tier ROUTING, never as the acceptance.
2. **The §2.3 verification's two named outcomes were both wrong and the plan says so** (pre-flight 4): no hash is client-visible AND no DTO field is added — the immutable payloads the surface already fetches make byte-equality of `diff` strictly cheaper than new contract surface. The spec's goal (the gate screen names an identical revision) is met; the mechanism it guessed at is not used.
3. **Task 14's first draft keyed the attempt off `gate_decisions` directly** — the table doesn't store one (pre-flight 6); the `phase_runs` join was forced by the schema, and the equal-`acted_at` test had to move to the store layer because `DBNow` freezes per transaction, making equal stamps hard to mint through the full path honestly.
4. **Task 27's first draft reached for `useFieldArray`** — no precedent in the tree, and the reducer owns ordering/refusal logic RHF cannot express; the hand-authored card forms follow §10.9's own ruling.

### Round 1 (codex artifact review, 2026-08-26)

Eleven findings, every one validated against the tree before acceptance; none rejected. Six were blockers and three of those were **tests that could not fail**, which is this repo's recurring failure mode rather than a coincidence. Recorded because the shape of each mistake is more useful than the fix:

1. **Task 6 read the outcome class where it had to read the STATUS.** An exhausted phase keeps its *transient* outcome — only the RUN's is rewritten to `attempts_exhausted` (`orchestrator.go:1652`) — and its status is `dead`, while a retrying attempt is `failed` and always has a successor (`orchestrator.go:1573`, its own comment). So the first-draft formatter printed `attempt 3/3` on the commonest failure there is: the precise lying counter §2.2 exists to remove, reintroduced by the fix for it. The discriminator was sitting in the DTO the whole time.
2. **Task 12's HTTP ceilings measured endpoints the test route never observes.** `renderApp('/')` mounts `['approvals']` and `['runs']` only, so `GET /tasks` and the events log would have counted **0** under any bridge — upper bounds all green, and the events-key mutation proof unable to fire. Ceilings are now exact counts on a route that observes every measured key. An upper bound is satisfied by zero; that is worth remembering the next time one is written.
3. **Task 28's deletion mutation was backwards.** `contracts:check` runs `contracts:gen` FIRST, so a deleted generated file is restored byte-for-byte and the check goes **green** — the prescribed proof asserted the opposite. The real hole it was reaching for is the orphan: drop a seed and its stale-but-valid document survives forever, validated by the Go judge, invisible to the diff. Fixed with a reconciling generator, a manifest, and a diversity floor.
4. **Task 12's throttle timer outlived its purpose.** A lifecycle window drained the held set without cancelling the timer, which then landed a second `['runs']`/`['tasks']` refetch seconds later — the drumbeat the task exists to remove, surviving inside its own fix.
5. **Task 10's mutation proof was disconnected from its assertion.** The measured button lives in a `sticky` bar with its own offset, so removing `<main>`'s padding could not redden it. And the safe-area reasoning was simply wrong: `body` padding does not reach a `position: fixed` element, so the bar would have sat under the home indicator with the plan asserting it would not.
6. **Task 28's reducer invariant made the editor unbuildable.** `grammarViolation` judges COMPLETE documents; the reducer was held to it from an empty start, so the first card ever added violated the invariant. Split into `prefixViolation` (the reducer's) and `grammarViolation` (save's).
7. Three majors were false claims about the tree that a reader would have believed: `Document` was called optional because "the list omits it" (the list uses a different type entirely); Task 15's cross-context join contradicted this plan's own SQL rule; and Task 29's descriptor could not express the integer bound its prose claimed, nor detect an engine field added without a row — so "a Go-side rule change breaks CI" was not established by any test.
8. Two gaps: the version read view's outline placeholder was mounted by Task 24 and never replaced by anyone, and ⌘K's "New task" navigated without opening anything.

Two findings were the author's own, from reading the Dockerfile against Task 4: the final-stage instruction was ambiguous in a way whose literal reading ships the **fake harness** as the runtime image, and the git stage's doc comment ("it is not the last stage") becomes false the moment the task lands.

### Round 2 (codex artifact review, 2026-08-26)

Eight findings, all validated, none rejected — and **three of the four blockers were defects round 1's own fixes introduced.** That is the round's real lesson and it is why the protocol treats a reviewer's fix as review input rather than as a closed question.

1. **Round 1's absorption guard made round 1's own counts wrong.** Adding `if (held.length === 0) return` to the throttle is correct — and it means a lifecycle event arriving before the timer produces ONE collection refetch, not two. The scenario still asserted two, so the prescribed test could not pass against the prescribed bridge. Split into two scenarios: throttle-then-lifecycle (2) and lifecycle-then-orphaned-timer (1, then zero more).
2. **Round 1's `prefixViolation` split left `move` behind.** The invariant was rewritten; the sentence three lines down still told `move` to re-check `grammarViolation`, which refuses ordinary reorders on an incomplete outline — the exact failure the split was for, surviving inside the fix for it.
3. **Round 1's node-type coverage assertion referenced `Descriptor()`, which Task 29 creates.** Task 28 runs first; a Go test cannot cite a file the declared order has not written. Now both read a `NodeTypes()` accessor that Task 28 adds to `definition.go`.
4. **Task 31's commit command was never updated** when the drawer moved to a search param, leaving `tasks.tsx` dirty — and `scripts/gate:99` refuses a dirty tree outright, so the task could not reach its own gate.
5. **Task 33 could not publish.** The plan proposed one `executor.Remote` registration; the harness needs three. `serveOptions` substitutes only the executor and the listener (`serve.go:112`), so publication runs the production forge adapter and git publisher against a stub that answers `{"sha": …}` to every request (`webe2e_test.go:131`). The fakes exist and each needs its own remote registered. This is a composition-root change, and the spec's last beat — the beat it exists for — is what would have failed.
6. **Task 28's corpus never executed the reducer** it named as its subject: the generator imported `op-generator` and `derive` only, so a reducer mutation left every document byte-identical and the Go lane green. It now replays through `editorReducer`, with a reducer mutation as the proof.
7. **Task 6 was still lying, just for different rows.** Round 1 fixed `dead`; `waiting_decision` and `succeeded` still fell through to the counter. A gate awaiting a human has no worker, no lease and no retry clock (`status.go:55-60`) and a succeeded phase has no outcome at all (`orchestrator.go:1099`). The function is now exhaustive over all eight statuses, with an exhaustiveness test.
8. **Task 7 would have accused every plan revision of changing nothing.** Gates may examine plans (pre-flight 13, proven against the real validator); plan payloads have no `diff`; `undefined === undefined` is true. The banner would have fired on every plan-gate revision — telling an operator their reviewed changes were ignored when they were not, which is worse than the silence the task set out to fix.

Also: `Doc.edges` was declared required while `toDocument` omits the key when empty (the emit would not type-check), and Task 20 asked `RunCard` for Task 6's status line using a list DTO that carries neither `max_attempts` nor `outcome`.

### Round 3 (codex artifact review, 2026-08-26)

Five findings, all validated, none rejected. The round turned up a **recurring class rather than five unrelated bugs**: a task names a file it must change and then does not stage it, so it cannot reach the clean-tree gate its own Global Constraints demand. Round 2 caught one instance (Task 31); round 3 caught three more, two of them in round 2's own fixes. The lesson is mechanical and worth stating as a rule for execution: **the last line of every task must be checked against the first — every file the task's prose touches appears in its `git add`.**

1. **Task 1's test could not compile.** The plan said `buildrequest_test.go` "exists — extend", and it does exist, but it holds harness-binding cases and the pure `TestCollapsePatchInputs` — no pool, no seed fixture, and no way to acquire one: `buildRequest` builds `store.New(o.svc.pool)` and reads seeds through `ListSeedArtifacts` against real Postgres (`orchestrator.go:948-971`). The named helpers were invented. The acceptance moves to `revisions_integration_test.go`, whose `bindingRecorder` + `waitForRequest` seam returns the real `ExecutionRequest` — which is what §2.1 actually asks to assert on. This is the plan's **first** task and its top product fix.
2. **Task 12 needed a `render-app` seam it neither described nor staged.** `renderApp(path, handlers)` renders `RouterProvider` alone (`render-app.tsx:119, 165-170`); there is no children slot, so the `['tasks']` observer round 1 relied on cannot exist. The task now defines the `observe` option and stages the helper.
3. **Task 33 listed `serve.go` and staged only `web/e2e/` and `webe2e_test.go`** — round 2's own fix, one line short.
4. **Task 17 was a four-layer change described as three.** sqlc emits a distinct row type per query, so the package hand-rolls a `credentialRow` intermediate that `newCredential` maps (`credentials.go:32-48`), built at all four read sites. Add the column and the domain field without it and the value is read from Postgres and dropped at the mapper — every fingerprint null, `agent_auth_file` included, and the task's own test red for a reason two layers away from where anyone would look.
5. **Task 10's safe-area fix offered a broken option.** Round 2 left two spellings open; one keeps the token at `4rem` and adds the inset as bar padding, which under preflight's global `box-sizing: border-box` carves the inset OUT of the 4rem — a 30px tab on a 34px-inset phone, beneath the locked 44px floor. The floor rule does not cover these anchors (`app.css:21-26` matches `button`/`a[role="button"]`/`[role="tab"]`; the tabs are plain `<Link>`s), and every CI viewport runs at a zero inset, so nothing automated would ever have shown it. The token is now the total and the choice is gone.

### Risks carried knowingly

- **Task 12 rewrites the one module every screen leans on.** Mitigations: the tier predicate is a one-line boolean over the kind string; both deafness flags carry the recorded reset-before-invalidate ordering; the HTTP-ceiling test pins behaviour end-to-end; the twins (`decide-gate`, the events comment) move in the same commit.
- **The corpus gate (Task 28) couples web tooling into backend testdata.** Accepted deliberately: the coupling IS the feature (a TS derivation change must redden a Go lane), and it rides the same regen-diff discipline as every generated artifact. The cost is a `contracts:gen` step that needs Node — which `contracts:ts` already imposed. The residual risk after round 1 is narrower and named: the regen-diff discipline catches *changed* and *orphaned* documents (reconciler + manifest), but a corpus is only as good as its generator, so the diversity floor and the node-type coverage assertion are what stand between "24 documents" and "one document 24 times". Both are in the Go judge, where they redden the cheap lane.
- **The runtime image loses distroless (Task 4).** The git stage's alpine shape is what the working deploy has run since 12a's checkpoint; the alternative (git into distroless) is a measured-libc-mismatch project this plan declines. Row 14 (deploy story) may revisit.
- **Playwright's lane roughly doubles (Task 32)** — the recorded price 12a declined to pay and this plan owns, per the spec. `workers: 1` keeps the shared-inbox semantics.
- **`GET /tasks` and the waiting arm stay unpaginated** — named residual, unchanged.
- **The editor's drag-reorder in jsdom is unfaithful by nature**; the keyboard fallback is the unit-tested path and the real drag is e2e-driven (Tasks 27, 28). If the e2e drag flakes, the fallback buttons are the shipped affordance and the mockup's grip stays as enhancement — record the outcome at the checkpoint.

---

## Execution handoff

Plan complete and saved to `docs/superpowers/plans/2026-08-26-caesar-12b-web-remainder.md`. Two execution options:

**1. Subagent-Driven (recommended)** — a fresh subagent per task, review between tasks. Give every subagent an explicit dissent licence (on Plan 10, fifteen controller- and reviewer-authored items were wrong and the implementer was right every time); name the return channel in every brief; no scratchpad repo copies; the gate needs a clean tree and no reviewer may work the worktree while one runs.

**2. Inline Execution** — superpowers:executing-plans, batch execution with checkpoints.

Either way: **wave boundaries are hard** (0 → 1 → 2 → 3); wave 1's endpoints land contracts-green before their screens; Task 12 closes wave 0 LAST; and the checkpoint (Task 34) is sofo's, on a real deploy, with the ledger as its deliverable.
