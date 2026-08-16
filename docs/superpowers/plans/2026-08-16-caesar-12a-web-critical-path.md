# Plan 12a — Web Critical Path Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** From a phone, against a real backend holding real data, a person can create a task, watch its run advance without touching the screen, open a waiting gate, read the artifact it examines, approve or request changes with a note, and see the resulting pull request.

**Architecture:** Two waves. **Wave 0** is Go and lands complete before any screen: a paginated `GET /runs` with a list DTO that carries a run's phase shape, artifact payload retrieval, a waiting-gate projection that makes a gate's evidence *findable* (`examined_artifact_id`/`type`, `summary_artifact_id`, `can_decide` + reason), and a versioned payload schema for `plan` and `review_verdict` gated on the workflow document's own `schema_version`. **Wave 1** is the four screens on Plan 11's foundation — one attention stream per tab, invalidate-never-reconstruct cache bridging, TanStack Query over the generated client — plus Playwright's three mobile-critical paths and an operator checkpoint that is a deliverable, not a formality.

**Tech Stack:** Go 1.26 (pgx/sqlc, swaggest openapi-go, stdlib mux) · Node 24.19.0 · React 19.2.8 · TypeScript 5.9.3 · TanStack Router 1.170.29 + Query 5.101.4 · react-hook-form 7.85.0 + Zod 4.4.3 · Tailwind 4.3.3 · Vitest 4.1.10 + Testing Library + MSW 2.15.0 · Playwright 1.62.1.

**Source spec:** `docs/superpowers/specs/2026-08-16-caesar-12a-web-critical-path-design.md`. Read it end to end before Task 1 — §2.3, §2.4, §5 and §10 each contain a decision that is not recoverable from the code.

**Locked design authority:** `docs/brainstorm/2026-08-01-decisions.md` §7 and the eight mockups in `.superpowers/brainstorm/12432-1785604290/content/`. Spec §10 lists the twelve places 12a departs from them. **A code comment citing a decisions section is not the decisions section** — `nav.tsx`'s comment is a wrong paraphrase and Task 8 corrects it.

---

## Global Constraints

Project-wide. They apply to **every** task below and are not re-litigated inside one.

- **Every task ends with `scripts/gate <abs-worktree-path> <sha> <branch>` green**, on a clean tree. A reviewer must not edit the worktree while a gate runs; use an overlay or a second checkout.
- **Mutation is the acceptance criterion.** Every new guard gets a red-then-revert proof, on the new test as well as on production code. A test that cannot be made to fail has not been shown to test anything. Where a step says "prove it", the proof is a recorded command and its output, not an assertion that it would fail.
- **Wave 0 lands complete — `task contracts:gen` run and `task contracts:check` green — before Task 8 begins.** Every screen builds against a generated, typed client.
- **Bounded contexts never import each other**; contexts never import `internal/app`; `internal/platform` imports no business code. `internal/arch` fails CI on a violation. The runs context's SQL never names another context's tables — labels come through ports.
- **Go house rules carry over**: no ORMs, no DI frameworks, `log/slog` only, stdlib `flag` only, stdlib asserts + go-cmp (no testify).
- **`required:"true"` on every DTO field 12a's client reads.** Without it `openapi-typescript` renders the property optional and the screens branch on a case the server cannot produce. Required and nullable are independent: a Go nil slice really marshals to `null`, so a slice stays `["array","null"]` *and* required.
- **Node is 24.19.0 exactly**, supplied by `scripts/with-node`. Every web lane invokes the installed binary **by path** (`./node_modules/.bin/…`). `npx` and `npm exec` reach the public registry and must not appear in a lane.
- **Named exports only. No barrel files. No default exports.** A module with two or more direct importers must live at its group root or at the importers' nearest common ancestor, whichever the clamp in `tools/arch/import-graph.ts` picks. `ui/**` imports nothing from `api`, `realtime`, `auth` or `routes`; `api/**` imports nothing from `routes`, `shell` or `ui`.
- **Non-semantic colours are unrepresentable.** Only the eleven names in `tools/arch/tokens.ts` `SEMANTIC` resolve; `bg-white` and `text-red-500` do not compile to anything.
- **Hand-written `fetch` against `/api` is banned by lint.** All server data goes through the generated client and TanStack Query.
- **The cache bridge invalidates; it never reconstructs.** `setQueryData` from an SSE frame payload is banned. New routing is proved through the injected timer, the way production fires it.
- **ONE STREAM PER TAB.** `useAttentionStream` has exactly one caller (`_app.tsx:63`) and `_app.test.tsx:98` asserts it. No screen in 12a subscribes to `GET /runs/{id}/events`; that endpoint stays the poll-and-resume path.
- **`correlation_id` is rendered on every error surface** (`api-error-panel.tsx`), and field errors route through `problem-to-fields.ts` unchanged.
- **Two empty states per list, always distinct.** "Nothing is waiting on you" is a statement about the inbox; "No projects are shared with you yet" is a statement about grants. Every list added in 12a inherits the rule `ApprovalList` already carries.
- **A stub never fakes success.** Where data does not exist (no verdict, no test outcome, no cost) the surface says so; it never renders an empty block that reads as a summary, or a tick that reads as a pass.
- **URL-as-state** for filters, tabs and the open gate. Nothing that a person would want to link to lives only in React state.

---

## Pre-flight findings

Verified against the tree at `6c144a4` by reading source. Several contradict what a reader of the spec alone would assume.

1. **`workflows.SchemaVersion` is `1` and `Document.Schema` is the JSON key `schema`, not `schema_version`.** `definition.go:22` names the constant, `definition.go:99` names the field, and `ParseDocument` at `:121` rejects anything but the single constant. Spec §2.3 speaks of `schema_version`; the wire key is `schema`. Task 5 keeps the wire key and widens the accepted set to `{1, 2}`.

2. **`DefinitionDoc` — the runs-owned mirror — carries no schema version at all** (`compile.go:44`). `pinnedFromVersion` (`app/runsports.go:44`) drops it on the floor. Nothing downstream of intake can currently learn which document schema a run pinned, which is exactly what §2.3's compatibility rule needs. Task 5 adds `DefinitionDoc.Schema` → `ExecPlan.SchemaVersion` and maps it in the adapter.

3. **`ExaminedInput` returns the first plan-or-patch input in sorted order** (`compile.go:125`), and inputs are sorted `(Origin, Artifact, From)` at `compile.go:186`. There is no sibling for `review_verdict`. Task 1 adds one and it must use the same sort, or the screen and the decision could disagree about which artifact is which.

4. **`validate.go:207` permits a gate to consume ZERO review verdicts** — `verdicts > 1` is the only violation. A patch gate with no review node upstream is a legal workflow, and `collect.go:259` makes declaring artifacts optional besides. "No verdict" is a production state and the surface renders it honestly.

5. **`gates.go:188-191` refuses self-approval BEFORE the branch on decision type at `:204`.** `request_changes` and `reject` are refused identically to `approve`. Hiding only approve ships two controls guaranteed to 403.

6. **`decideGate` requires `GrantApprove` (`api.go:408`) while `GET /approvals` scopes to ANY grant** (`api.go:439-449`), deliberately, and `api_integration_test.go:1256` pins the asymmetry. A view-only principal gets a full inbox of undecidable gates. That is a second, independent 403 class.

7. **`ApprovalResponse` has no policy snapshot** (`dto.go:126`), so `allow_self_approval` is reachable only by fetching each run — the N+1 the endpoint exists to prevent. `can_decide` is computed server-side or not at all.

8. **`phase.` is NOT in `attentionPrefixes`** (`realtime.go:22` — `{"gate.", "publish.", "run."}`). No phase transition reaches a client on the global stream today. `AttentionKindPatterns()` pushes the same list into SQL for the snapshot and replay queries, so the one-line edit changes three things at once and all three are load-bearing.

9. **`cache-bridge.ts` reads nothing off a frame but its kind** (`cache-bridge.ts:68`). It cannot route by `run_id`. The wire already carries one — `StreamFrame.run_id` (`sse-parser.ts:3`) — so the transport needs no change, only the bridge.

10. **`RunResponse` is reused as a reduced list entry** with `phases`, `artifacts` and `publications` empty (`api.go:361`). It carries no task title, no repo or workflow label and no phase summary.

11. **`phase_runs` has no node-type column** (`schema.sql:242`). A phase's node type is only knowable from the pinned definition, so `phase_summary[]`'s `node_type` costs a definition read per distinct `workflow_version_id` on the page — memoized, not per row.

12. **`runs` has no `updated_at`.** It has `created_at`, `started_at`, `finished_at` (`schema.sql:229-231`). Spec §2.1 asks for "created and updated"; Task 4 carries all three real columns instead of synthesising an `updated_at`, and the card renders `finished_at ?? started_at ?? created_at`.

13. **The runs context's SQL never joins another context's tables** — grep `internal/runs/store/queries.sql` for `repos`/`workflows`: no hits. Repo, project and workflow labels therefore arrive through a port, batched by distinct id.

14. **`workflows.Version` carries no workflow name** (`workflows.go:33`). The name needs `GetVersionByID` then `GetWorkflow`. That is two queries, which is why the label port is separate from `DefinitionSource` — `DefinitionByVersionID` is on the orchestrator's hot path and must not grow one.

15. **`fake.Executor` emits `plan:{content}` and `review_verdict:{verdict,notes}`** (`fake/executor.go:176-184`), and it is what the Playwright lane runs on. Neither satisfies §2.3's required sets. `docker/harness/*/entrypoint.sh` appends nothing to node instructions, so **nothing today tells a model what shape to emit**.

16. **`fake.parseDirective` scans instructions line by line for a `caesar-fake:` prefix** and `scripted` uses only the FIRST line (`executor.go:32,141`). Appending an output-shape block to `ExecutionRequest.Instructions` is safe as long as the block contains no line starting with `caesar-fake:`.

17. **`tasks.go:372`'s refusal message says "cancel arrives in Plan 09".** Cancel is row 09's work. `rerunTask` / `Service.Rerun` already ship and already mutate run lifecycle; 12a ships neither control, per §10.3.

18. **`_app` authenticates in its `loader`, not in `beforeLoad`** (`_app.tsx:25`). The guard test's non-vacuousness comes from `router.history.length === 1` — a loader `throw redirect` REPLACES the entry, an error-boundary `navigate` PUSHES one.

19. **`approvals.tsx` already has a loader** (`approvals.tsx:20`). So a loader-bearing route already exists; what §6 asks for is specifically the Tasks route, because Tasks is the one whose loader destroys the property `_app.test.tsx`'s comment currently claims.

20. **`scripts/gate` asserts the resolved command line of every lane character for character.** Any Taskfile change in this plan needs a matching `scripts/gate` edit in the same commit.

21. **`internal/webui/ui.go` needs no per-route registration.** Its SPA fallback serves `index.html` for every non-`/assets` miss, and its contract tests use `/approvals`, `/tasks` and a deep link generically. New client routes cost it nothing.

22. **`web/` has no diff library and none is being added.** Patch decode and file/±line parsing is ~120 lines of pure TypeScript in `patch/`, tested in the `node` vitest project.

23. **`GateDecisionSummary.ExaminedArtifactID` already exists on a DECIDED gate** (`dto.go:156`). What is missing is the same information on a gate that has *not* been decided — which is the only kind the approval surface ever opens.

---

## File Structure

**New — backend:**

- `backend/internal/runs/payload.go` — the `plan` / `review_verdict` payload validator. Pure: a type name, a `map[string]any`, an error. No database, no HTTP.
- `backend/internal/runs/payload_test.go` — the validator's table, including the unknown-extra-key case that must PASS.
- `backend/internal/runs/runlist.go` — `RunSummary`, `PhaseSummary`, `ListRuns` and its cursor.
- `backend/internal/runs/runlist_test.go` — cursor encode/decode round trip and its rejection cases (pure).
- `backend/internal/runs/gateevidence.go` — resolving a gate's examined artifact, its summary verdict and the policy inputs `can_decide` needs.
- `backend/internal/runs/api/payload_dto.go` — `PlanPayload`, `PlanStep`, `ReviewVerdictPayload`, `ReviewFinding`, `TestOutcome`. These exist to be reflected into the OpenAPI document; nothing in the API handler path constructs one.
- `backend/internal/contracts/payload_schema_test.go` — ties `runs.RequiredPayloadKeys` to the generated document's `required` arrays, so the validator and the contract cannot drift.
- `backend/internal/runs/api/runlist_integration_test.go` — grant scoping, ordering, cursor paging, filters, phase summary.
- `backend/internal/runs/api/artifact_integration_test.go` — payload retrieval and its cross-project refusal.
- `backend/internal/runs/api/gateevidence_integration_test.go` — the projection agreeing with what `DecideGate` stamps, the no-verdict gate, both `can_decide` refusals.

**New — web:**

- `web/src/runs/runs-query.ts` — the `['runs', filters]` list query and its filter type.
- `web/src/runs/run-query.ts` — the `['run', runId]` detail query.
- `web/src/runs/phase-dots.tsx` — the phase-dot row, shared by Home and Tasks cards.
- `web/src/runs/run-card.tsx` — the running/recent card, shared by Home and Tasks.
- `web/src/patch/decode-patch.ts` — base64 → bytes → text, and unified-diff parsing into files with ± counts and hunks.
- `web/src/patch/decode-patch.test.ts` — pure, including the non-UTF-8 payload.
- `web/src/gates/decide-gate.ts` — the decision mutation and its query invalidations.
- `web/src/gates/gate-controls.tsx` — approve · request changes · reject, all-or-nothing suppression with its stated reason, the note requirement, the 409 surfaces.
- `web/src/gates/gate-controls.test.tsx` — the decision assertions, reused verbatim by all three hosts.
- `web/src/approvals/approval-surface.tsx` — summary → hard facts → file accordion → sticky actions. Built once, hosted twice.
- `web/src/approvals/artifact-query.ts` — the `['artifact', runId, artifactId]` payload query.
- `web/src/tasks/tasks-query.ts`, `web/src/tasks/task-groups.ts` — the list query and the pure seven-status → four-group mapping.
- `web/src/tasks/new-task-form.tsx` — the FAB's short form on Plan 11's `useAppForm`.
- `web/src/routes/_app/more.tsx` — the More tab: Workflows · Repos · Admin.
- `web/src/routes/_app/runs.index.tsx`, `web/src/routes/_app/runs.$runId.tsx` — the runs list and run detail (replacing `runs.tsx`).
- `web/src/routes/_app/approvals.index.tsx`, `web/src/routes/_app/approvals.$runId.$nodeId.tsx` — the inbox and the full-screen surface (replacing `approvals.tsx`).
- `web/e2e/gate-decision.spec.ts`, `web/e2e/task-timeline.spec.ts` — critical paths 1+3 and 2.

**Modified — backend:**

- `internal/workflows/definition.go` — `SchemaVersion` becomes 2, `MinSchemaVersion` 1, `ParseDocument` branches.
- `internal/runs/compile.go` — `DefinitionDoc.Schema`, `ExecPlan.SchemaVersion`, `SummaryInput`.
- `internal/runs/orchestrator.go` — payload validation before the outcome transaction; the output-shape instruction in `buildRequest`.
- `internal/runs/gates.go` — `WaitingGate` gains the evidence fields.
- `internal/runs/tasks.go` — `Phase` gains the evidence fields on gates; `RunDetail` assembly resolves them.
- `internal/runs/ports.go` — `RunLabelSource`.
- `internal/runs/store/queries.sql` (+ regenerated `queries.sql.go`) — `ListRunsPage`, `ListPhasesForRuns`, the two waiting-gate queries gain `policy_snapshot` and `workflow_version_id`.
- `internal/runs/api/api.go`, `dto.go` — `listRuns`, `getArtifact`, the projection fields, `required:"true"` across everything 12a reads.
- `internal/runs/fake/executor.go` — schema-2-conformant payloads.
- `internal/realtime/realtime.go` — `phase.` joins `attentionPrefixes`.
- `internal/contracts/contracts.go` — `requestOverrides` entries for `listRuns` and `getArtifact`.
- `internal/app/runsports.go` — `Schema` mapped onto the mirror; the label source adapter.
- `internal/app/server.go` — wiring the label source into `runs/api`.
- `internal/app/webe2e_test.go` — a schema-2 workflow with a review node, and the fixture endpoints paths 2 and 3 need.
- `internal/platform/pg/schema.sql` — the `examined_artifact_id` comment correction (§10.2).

**Modified — web:**

- `src/shell/nav.tsx` — five phone tabs plus More; the wrong `decisions §7` paraphrase corrected.
- `src/realtime/cache-bridge.ts` — `phase.` routing by `run_id`.
- `src/routes/_app/index.tsx` — Home.
- `src/routes/_app/tasks.tsx` — Tasks, with a loader.
- `src/routes/_app.test.tsx` — the rehomed guard test.
- `src/approvals/approval-list.tsx` — cards link to the surface.
- `src/test/render-app.tsx` — default handlers for the endpoints every protected route now touches.

---

## Dependencies and order

```
Wave 0 (Go, lands complete; contracts:gen + contracts:check green before Task 8)
  1  SummaryInput + the schema.sql comment
  2  the waiting-gate evidence projection            (needs 1)
  3  artifact payload retrieval
  4  GET /api/v1/runs
  5  document schema 2 + the payload contracts
  6  payload enforcement + the producers             (needs 5)
  7  phase. on the attention stream

Wave 1 (web)
  8  the shell correction                            (touches routing everything builds on)
  9  run queries + the bridge that routes by run_id  (needs 4, 7)
 10  patch decode and diff parsing                   (pure; independent)
 11  gate decision controls                          (needs 2)
 12  the approval surface + its Approvals hosts      (needs 3, 5, 10, 11)
 13  Home                                            (needs 9, 11)
 14  Tasks                                           (needs 9)
 15  the auth-guard test, rehomed                    (needs 14; NOTHING between 14 and 15)
 16  run detail                                      (needs 9, 12)
 17  Playwright — the three mobile-critical paths    (needs 13, 14, 16)
 18  the operator checkpoint                         (needs 17)
```

**Task 15 immediately follows Task 14 and nothing may be scheduled between them.** Spec §8 says the rehoming "lands with or before Tasks"; §6 says the test is "retargeted at the now-loader-bearing Tasks route". Those resolve to adjacency: Task 14 gives Tasks a loader, Task 15 rewrites the guard test against it in the next commit. Between those two commits `_app.test.tsx`'s first test is knowingly weaker than its comment claims, and no third change may hide in that window.

---

# Wave 0 — the backend substrate

Roughly 40% of 12a is Go and none of it is visible from the roadmap row. All seven tasks below land, `task contracts:gen` runs and `task contracts:check` is green before any screen is touched.

---

### Task 1: `SummaryInput` — the other half of a gate's evidence

`ExaminedInput` finds the plan-or-patch a gate decides on. §3.5 orders the approval surface **summary-first**, and that summary comes from a `review_verdict` — a *different* artifact, selected by a different edge. `ExaminedInput` deliberately returns only plan-or-patch (`compile.go:131`), so a client holding the examined id still cannot find the verdict, and "the newest `review_verdict` on the run" is a guess that summarises generation 1's review on a generation-3 gate.

This task adds the resolver and corrects the comment §10.2 names. It touches no wire format; Task 2 puts the result on one.

**Files:**
- Modify: `backend/internal/runs/compile.go` — after `ExaminedInput` (`:125-136`)
- Modify: `backend/internal/runs/compile_test.go`
- Modify: `backend/internal/platform/pg/schema.sql:346-349`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `func (p ExecPlan) SummaryInput(gateNodeID string) (PlanInput, bool)` — Task 2's only caller.

- [ ] **Step 1: Write the failing tests**

Append to `backend/internal/runs/compile_test.go`:

```go
// gateDoc builds a definition whose gate consumes exactly the artifacts named.
// Every producer of a bindable artifact is agentic, so each edge needs a node.
func gateDoc(t *testing.T, artifacts ...string) ExecPlan {
	t.Helper()
	doc := DefinitionDoc{Nodes: []DefNode{{ID: "gate", Kind: KindGate}}}
	for i, a := range artifacts {
		producer := fmt.Sprintf("n%d", i)
		kind := KindPlan
		switch a {
		case ArtifactKindPatch:
			kind = KindImplement
		case ArtifactKindReviewVerdict:
			kind = KindReview
		}
		doc.Nodes = append(doc.Nodes, DefNode{ID: producer, Kind: kind, Harness: "h", Instructions: "go"})
		doc.Edges = append(doc.Edges, DefEdge{From: producer, To: "gate", Artifact: a})
	}
	plan, err := Compile(doc)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return plan
}

func TestSummaryInput(t *testing.T) {
	t.Run("finds the review verdict beside the examined patch", func(t *testing.T) {
		plan := gateDoc(t, ArtifactKindPatch, ArtifactKindReviewVerdict)
		got, ok := plan.SummaryInput("gate")
		if !ok {
			t.Fatal("SummaryInput found nothing on a gate that consumes a verdict")
		}
		if got.Artifact != ArtifactKindReviewVerdict {
			t.Errorf("SummaryInput artifact = %q, want %q", got.Artifact, ArtifactKindReviewVerdict)
		}
		// AND it must not be the examined one. A resolver that returned the
		// first input of any type would satisfy the assertion above on this
		// graph, because the patch sorts first.
		examined, _ := plan.ExaminedInput("gate")
		if got.Origin == examined.Origin {
			t.Errorf("SummaryInput returned the EXAMINED input %+v", got)
		}
	})

	t.Run("a gate with no verdict is not an error", func(t *testing.T) {
		// workflows/validate.go:207 requires exactly one plan-or-patch and
		// permits ZERO verdicts, so this is a legal workflow and the surface
		// renders it honestly rather than failing.
		plan := gateDoc(t, ArtifactKindPatch)
		if _, ok := plan.SummaryInput("gate"); ok {
			t.Error("SummaryInput invented a verdict on a gate that consumes none")
		}
	})

	t.Run("a node that is not a gate has no summary", func(t *testing.T) {
		plan := gateDoc(t, ArtifactKindPatch, ArtifactKindReviewVerdict)
		if _, ok := plan.SummaryInput("n0"); ok {
			t.Error("SummaryInput answered for a non-gate node")
		}
		if _, ok := plan.SummaryInput("nope"); ok {
			t.Error("SummaryInput answered for an unknown node")
		}
	})
}
```

`compile_test.go` may not already import `fmt`; add it.

- [ ] **Step 2: Run them and watch them fail**

Run: `cd backend && go test ./internal/runs/ -run TestSummaryInput -v`
Expected: FAIL — `plan.SummaryInput undefined (type ExecPlan has no field or method SummaryInput)`.

- [ ] **Step 3: Implement it**

In `backend/internal/runs/compile.go`, immediately after `ExaminedInput`:

```go
// SummaryInput is the review_verdict a gate consumes, where its graph has one.
// It is the OTHER half of a gate's evidence: §3.5 orders the approval surface
// summary-first, and the summary is a different artifact from the plan-or-patch
// the gate examines — ExaminedInput deliberately selects only the latter.
//
// At most one can exist. workflows/validate.go:207 makes `verdicts > 1` a
// validation violation and permits ZERO, so this returns a single input and
// never a list.
//
// FALSE MEANS SOMETHING DIFFERENT HERE THAN IT DOES ON ExaminedInput, and the
// two are easy to confuse because the signatures are identical. A gate with no
// examined input is CORRUPTION — DecideGate says so and refuses. A gate with no
// verdict is an ORDINARY workflow, and the caller renders "no summary" rather
// than an empty block pretending to be one.
//
// Inputs are sorted at compile time on (Origin, Artifact, From), so the answer
// is deterministic for the same document however its edges were written.
func (p ExecPlan) SummaryInput(gateNodeID string) (PlanInput, bool) {
	n, ok := p.Nodes[gateNodeID]
	if !ok || n.Kind != KindGate {
		return PlanInput{}, false
	}
	for _, in := range n.Inputs {
		if in.Artifact == ArtifactKindReviewVerdict {
			return in, true
		}
	}
	return PlanInput{}, false
}
```

- [ ] **Step 4: Run them and watch them pass**

Run: `cd backend && go test ./internal/runs/ -run TestSummaryInput -v`
Expected: PASS, all three subtests.

- [ ] **Step 5: Mutation proof — red, then revert**

The guard worth proving is the "not the examined one" assertion, because a resolver that ignored the artifact type would otherwise look correct.

```bash
cd backend
# Mutant: drop the type test.
perl -0pi -e 's/if in\.Artifact == ArtifactKindReviewVerdict \{\n\t\t\treturn in, true/if true {\n\t\t\treturn in, true/' internal/runs/compile.go
go test ./internal/runs/ -run TestSummaryInput
git checkout -- internal/runs/compile.go
```

Expected: the mutant run FAILS with `SummaryInput returned the EXAMINED input` **and** `SummaryInput invented a verdict on a gate that consumes none`. Record both. Then confirm `go test ./internal/runs/ -run TestSummaryInput` is green again after the revert.

- [ ] **Step 6: Correct the schema comment §10.2 names**

`backend/internal/platform/pg/schema.sql`, the `examined_artifact_id` comment currently reads:

```sql
    -- The immutable artifact the human examined. Approval authorizes exactly
    -- this id and nothing newer (spec §3 artifact flow).
```

Replace with:

```sql
    -- A SERVER-SELECTED EVIDENCE BINDING, not a record of human examination.
    -- DecideGate resolves it from the run's pinned definition
    -- (ExecPlan.ExaminedInput) immediately before the stamp, whether or not
    -- anybody looked at it — so it names the artifact the decision was ABOUT.
    -- Approval authorizes exactly this id and nothing newer (spec §3 artifact
    -- flow). The system's real guarantee is that publication waits for a
    -- person to act, which holds wherever that person acts from; see Plan 12a
    -- §10.2 for why no reachable design closes the gap between "evidence
    -- rendered" and "evidence read".
```

- [ ] **Step 7: Gate and commit**

```bash
cd backend && go test ./internal/runs/ ./internal/platform/pg/
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git add backend/internal/runs/compile.go backend/internal/runs/compile_test.go backend/internal/platform/pg/schema.sql
git commit -m "feat(runs): resolve a gate's review verdict, and say what examined_artifact_id really is"
```

---

### Task 2: A waiting gate's evidence, on the wire

§2.2 makes artifact content fetchable. It does not make it **findable**. `ApprovalResponse` carries no artifact id and no artifact type (`dto.go:126`); `ArtifactResponse` carries no gate association in the other direction; the mapping lives in the server's compiled definition and is resolved inside `DecideGate` (`gates.go:193`). A client that guesses shows one artifact and stamps another.

Three things go on the wire together, because a card that offers a decision needs all three before it renders: the **examined** artifact (id and type — a `plan` renders differently from a `patch`), the **summary** verdict (nullable), and **`can_decide`** with a reason. §5's rule is all-or-nothing across the three controls and there are two independent 403 classes; both are answerable only where the grant and the policy snapshot live.

**Files:**
- Create: `backend/internal/runs/gateevidence.go`
- Create: `backend/internal/runs/api/gateevidence_integration_test.go`
- Modify: `backend/internal/runs/gates.go` — `WaitingGate`, `waitingGateFrom`, `ListWaitingGates`
- Modify: `backend/internal/runs/tasks.go` — `Phase`, `GetRunDetail`
- Modify: `backend/internal/runs/store/queries.sql` — `ListWaitingGates`, `ListWaitingGatesByProjects` (+ `sqlc generate`)
- Modify: `backend/internal/runs/api/dto.go`, `backend/internal/runs/api/api.go`
- Regenerate: `backend/api/openapi.json`, `web/src/api/schema.d.ts`

**Interfaces:**
- Consumes: `ExecPlan.SummaryInput` (Task 1), `ExecPlan.ExaminedInput`.
- Produces:
  - Go: `runs.GateEvidence{ExaminedArtifactID uuid.UUID; ExaminedArtifactType string; SummaryArtifactID *uuid.UUID; AllowSelfApproval bool; RequesterID uuid.UUID}`; `WaitingGate.Evidence GateEvidence`; `Phase.Evidence *GateEvidence`.
  - Wire: `ApiGateEvidenceResponse{examined_artifact_id, examined_artifact_type, summary_artifact_id (nullable), can_decide, cannot_decide_reason, wall_deadline}`, present as `ApiApprovalResponse.evidence` (always) and `ApiPhaseResponse.evidence` (only on a gate still awaiting a decision).
  - `cannot_decide_reason` ∈ `{"", "not_approver", "self_approval_forbidden"}`.

- [ ] **Step 1: Widen the two waiting-gate queries**

`backend/internal/runs/store/queries.sql` — both queries gain the two columns the projection needs. Nothing else changes:

```sql
-- name: ListWaitingGates :many
SELECT pr.id, pr.run_id, pr.node_id, pr.attempt, pr.generation, pr.created_at,
       t.id AS task_id, t.project_id, t.title, t.requester_id, t.workflow_version_id,
       r.policy_snapshot
FROM phase_runs pr
JOIN runs r ON r.id = pr.run_id
JOIN tasks t ON t.id = r.task_id
WHERE pr.status = 'waiting_decision'
ORDER BY pr.created_at;

-- name: ListWaitingGatesByProjects :many
SELECT pr.id, pr.run_id, pr.node_id, pr.attempt, pr.generation, pr.created_at,
       t.id AS task_id, t.project_id, t.title, t.requester_id, t.workflow_version_id,
       r.policy_snapshot
FROM phase_runs pr
JOIN runs r ON r.id = pr.run_id
JOIN tasks t ON t.id = r.task_id
WHERE pr.status = 'waiting_decision' AND t.project_id = ANY(sqlc.arg(project_ids)::uuid[])
ORDER BY pr.created_at;
```

Run `task sqlc` (or the repo's generate target) and confirm `queries.sql.go` picks up `WorkflowVersionID` and `PolicySnapshot` on both row types.

- [ ] **Step 2: Write the failing integration test**

`backend/internal/runs/api/gateevidence_integration_test.go`:

```go
//go:build integration

package api_test

import (
	"net/http"
	"testing"

	runsapi "github.com/giiorg/caesar/backend/internal/runs/api"
)

// TestWaitingGateCarriesItsEvidence is the anti-guessing test. Every case here
// is one the client cannot answer for itself, which is why the fields exist.
func TestWaitingGateCarriesItsEvidence(t *testing.T) {
	h := newHarness(t)

	t.Run("the examined artifact is the one DecideGate stamps, even when it is not the newest", func(t *testing.T) {
		// THE SHAPE OF THIS FIXTURE IS THE TEST. Stage a run whose gate
		// examines a PLAN produced by an early node, and then let a LATER node
		// produce a review_verdict — so "the newest artifact on the run" and
		// "the artifact this gate examines" are different rows. A projection
		// that guessed would pass on any simpler graph.
		run, gateNode := h.stagePlanGateWithLaterVerdict(t)

		got := h.onlyApproval(t, "approver-tok", run)
		// The oracle is DecideGate's own answer, taken from the decision it
		// records — one function resolving the artifact, so the screen and the
		// stamp cannot drift.
		resp := h.do(t, http.MethodPost, decisionPath(run, gateNode), "approver-tok",
			runsapi.GateDecisionRequest{Decision: "approve"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("approve: %d", resp.StatusCode)
		}
		stamped := h.examinedArtifactOfDecision(t, run, gateNode)
		if got.Evidence.ExaminedArtifactID != stamped {
			t.Errorf("projection examined %s, DecideGate stamped %s",
				got.Evidence.ExaminedArtifactID, stamped)
		}
		if got.Evidence.ExaminedArtifactType != "plan" {
			t.Errorf("examined_artifact_type = %q, want plan", got.Evidence.ExaminedArtifactType)
		}
	})

	t.Run("a gate with no review verdict says so rather than guessing", func(t *testing.T) {
		run, _ := h.stagePatchGateWithoutVerdict(t)
		got := h.onlyApproval(t, "approver-tok", run)
		if got.Evidence.SummaryArtifactID != nil {
			t.Errorf("summary_artifact_id = %v on a gate whose graph has no verdict node", *got.Evidence.SummaryArtifactID)
		}
		// And the KEY is still present. "Absent" and "null" are different
		// answers to a client, and only one of them is a value it can branch on.
		if !h.rawApprovalHasKey(t, "approver-tok", run, "summary_artifact_id") {
			t.Error("summary_artifact_id was omitted; it must be present and null")
		}
	})

	t.Run("a gate WITH a verdict names it, and names this generation's", func(t *testing.T) {
		run, _, gen2Verdict := h.staleVerdictThenRevision(t)
		got := h.onlyApproval(t, "approver-tok", run)
		if got.Evidence.SummaryArtifactID == nil || *got.Evidence.SummaryArtifactID != gen2Verdict {
			t.Errorf("summary_artifact_id = %v, want generation 2's verdict %s",
				got.Evidence.SummaryArtifactID, gen2Verdict)
		}
	})

	t.Run("a view-only member sees the gate and is told they may not decide it", func(t *testing.T) {
		run, node := h.stagePatchGateWithoutVerdict(t)
		got := h.onlyApproval(t, "viewer-tok", run)
		if got.Evidence.CanDecide {
			t.Error("can_decide is true for a principal decideGate refuses")
		}
		if got.Evidence.CannotDecideReason != "not_approver" {
			t.Errorf("cannot_decide_reason = %q, want not_approver", got.Evidence.CannotDecideReason)
		}
		// The projection must agree with the endpoint. A can_decide computed
		// from a different rule is worse than no can_decide at all.
		if resp := h.do(t, http.MethodPost, decisionPath(run, node), "viewer-tok",
			runsapi.GateDecisionRequest{Decision: "approve"}); resp.StatusCode != http.StatusForbidden {
			t.Errorf("decideGate = %d for a caller can_decide called undecidable", resp.StatusCode)
		}
	})

	t.Run("the requester of a no-self-approval run is refused all three, not just approve", func(t *testing.T) {
		run, node := h.stageGateNoSelfApproval(t) // approver-tok is the requester
		got := h.onlyApproval(t, "approver-tok", run)
		if got.Evidence.CanDecide {
			t.Error("can_decide is true for the requester of a run that forbids self-approval")
		}
		if got.Evidence.CannotDecideReason != "self_approval_forbidden" {
			t.Errorf("cannot_decide_reason = %q, want self_approval_forbidden", got.Evidence.CannotDecideReason)
		}
		// gates.go:188 refuses BEFORE branching on decision type, so all three
		// really are refused. Asserting only approve is how a UI ends up
		// shipping two controls guaranteed to 403.
		for _, d := range []string{"approve", "request_changes", "reject"} {
			body := runsapi.GateDecisionRequest{Decision: d, Note: "n"}
			if resp := h.do(t, http.MethodPost, decisionPath(run, node), "approver-tok", body); resp.StatusCode != http.StatusForbidden {
				t.Errorf("decideGate %s = %d, want 403", d, resp.StatusCode)
			}
		}
	})

	t.Run("the same evidence rides the run's own gate phase", func(t *testing.T) {
		run, node := h.stagePatchGateWithoutVerdict(t)
		fromInbox := h.onlyApproval(t, "approver-tok", run).Evidence
		detail := h.getRun(t, "approver-tok", run)
		var fromPhase *runsapi.GateEvidenceResponse
		for _, p := range detail.Phases {
			if p.NodeID == node && p.Evidence != nil {
				fromPhase = p.Evidence
			}
		}
		if fromPhase == nil {
			t.Fatal("the run's waiting gate phase carries no evidence")
		}
		if *fromPhase != fromInbox {
			t.Errorf("phase evidence %+v != inbox evidence %+v", *fromPhase, fromInbox)
		}
	})

	t.Run("a decided or non-gate phase carries none", func(t *testing.T) {
		run, node := h.stagePatchGateWithoutVerdict(t)
		if resp := h.do(t, http.MethodPost, decisionPath(run, node), "approver-tok",
			runsapi.GateDecisionRequest{Decision: "approve"}); resp.StatusCode != http.StatusOK {
			t.Fatalf("approve: %d", resp.StatusCode)
		}
		for _, p := range h.getRun(t, "approver-tok", run).Phases {
			if p.Evidence != nil {
				t.Errorf("phase %s (status %s) carries evidence after the gate was decided", p.NodeID, p.Status)
			}
		}
	})
}
```

The helpers (`stagePlanGateWithLaterVerdict`, `stagePatchGateWithoutVerdict`, `staleVerdictThenRevision`, `stageGateNoSelfApproval`, `onlyApproval`, `getRun`, `rawApprovalHasKey`, `examinedArtifactOfDecision`) go on the existing `harness` in `api_integration_test.go`, built from the same `stageGate`-style primitives already there. Two constraints on them:

- **They stage through the service, never with raw SQL against `phase_runs`.** The existing harness's staging helpers are the template.
- **`stagePlanGateWithLaterVerdict` must produce a graph where the gate's examined origin is genuinely not the newest artifact-bearing node.** `plan → gate(examines plan)` with a `review` node hanging off the same plan and finishing after it. If the fixture cannot produce that ordering, the first subtest is vacuous — check it by printing both artifact ids once during development.

- [ ] **Step 3: Run it and watch it fail**

Run: `cd backend && go test -tags integration ./internal/runs/api/ -run TestWaitingGateCarriesItsEvidence -v`
Expected: FAIL to compile — `got.Evidence undefined`.

- [ ] **Step 4: Resolve the evidence in the runs context**

`backend/internal/runs/gateevidence.go`:

```go
package runs

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// GateEvidence is what a gate's decision surface needs and cannot find for
// itself. Every field here answers a question the client would otherwise have
// to GUESS at, and every guess available to it is wrong in a way the guess
// cannot detect (spec §2.4).
//
// The two artifact ids come from the SAME compiled plan the decision path
// reads, so the artifact the screen renders and the artifact the decision
// records cannot drift.
type GateEvidence struct {
	// ExaminedArtifactID is the plan-or-patch this gate decides on. A ZERO id
	// means the pinned definition binds an artifact the run never produced —
	// the same state DecideGate refuses as corrupt. The surface says the
	// evidence is missing; it must not render an empty diff.
	ExaminedArtifactID   uuid.UUID
	ExaminedArtifactType string
	// SummaryArtifactID is nil for two DIFFERENT reasons and the client needs
	// to distinguish neither: the graph has no verdict node (legal — see
	// validate.go:207), or it has one that declared nothing (legal —
	// collect.go:259 makes declaring optional). Both mean "no summary".
	SummaryArtifactID *uuid.UUID
	// AllowSelfApproval and RequesterID are the two halves of §5's first
	// refusal. They ride the projection because the API layer holds the grant
	// and this layer holds the run; can_decide is computed where both are.
	AllowSelfApproval bool
	RequesterID       uuid.UUID
}

// gateEvidence resolves one gate's evidence against an already-compiled plan.
// The plan is a parameter rather than a lookup so a list can compile once per
// distinct workflow version instead of once per row.
func gateEvidence(ctx context.Context, q *store.Queries, plan ExecPlan,
	runID uuid.UUID, nodeID string, snapshot PolicySnapshot, requesterID uuid.UUID,
) (GateEvidence, error) {
	ev := GateEvidence{AllowSelfApproval: snapshot.AllowSelfApproval, RequesterID: requesterID}

	examined, ok := plan.ExaminedInput(nodeID)
	if !ok {
		// Corruption of a stored definition, exactly as DecideGate reads it.
		return GateEvidence{}, fmt.Errorf("runs: gate %q examines nothing", nodeID)
	}
	ev.ExaminedArtifactType = examined.Artifact
	art, err := q.GetNodeArtifact(ctx, store.GetNodeArtifactParams{
		RunID: runID, NodeID: examined.Origin, Type: examined.Artifact,
	})
	switch {
	case err == nil:
		ev.ExaminedArtifactID = art.ID
	case isNoRows(err):
		// The definition binds it and the run has not got one. Left ZERO on
		// purpose: DecideGate will refuse this gate too, and a projection that
		// substituted some other artifact would show a human one thing and
		// stamp another — the precise failure §2.4 exists to prevent.
	default:
		return GateEvidence{}, fmt.Errorf("runs: examined artifact %s/%s: %w", examined.Origin, examined.Artifact, err)
	}

	if summary, ok := plan.SummaryInput(nodeID); ok {
		row, err := q.GetNodeArtifact(ctx, store.GetNodeArtifactParams{
			RunID: runID, NodeID: summary.Origin, Type: summary.Artifact,
		})
		switch {
		case err == nil:
			// GetNodeArtifact orders by generation then attempt, so a gate on
			// generation 3 gets generation 3's verdict and never generation 1's.
			id := row.ID
			ev.SummaryArtifactID = &id
		case isNoRows(err):
			// The node exists and declared nothing. Not an error.
		default:
			return GateEvidence{}, fmt.Errorf("runs: summary artifact %s/%s: %w", summary.Origin, summary.Artifact, err)
		}
	}
	return ev, nil
}

// isNoRows is spelled out because the two callers above want opposite
// behaviour from it and a bare errors.Is at each site reads as duplication
// rather than as the same decision made twice.
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
```

Add the `errors` and `github.com/jackc/pgx/v5` imports.

`plans` memo — add to `gates.go`'s `ListWaitingGates`, which now resolves evidence per row:

```go
// ListWaitingGates is the approvals inbox. all=true is the admin view; a
// member passes the project ids their grants cover, and an empty list is an
// empty inbox rather than everything.
//
// It resolves each gate's EVIDENCE (spec §2.4), which costs one compiled plan
// per distinct workflow version on the page — memoized, because an inbox is
// usually several gates over one or two workflows — plus one or two artifact
// reads per gate. The alternative is the client fetching each run, which is
// the N+1 this endpoint exists to prevent.
func (s *Service) ListWaitingGates(ctx context.Context, projectIDs []uuid.UUID, all bool) ([]WaitingGate, error) {
	q := store.New(s.pool)
	plans := map[uuid.UUID]ExecPlan{}
	compiled := func(versionID uuid.UUID) (ExecPlan, error) {
		if p, ok := plans[versionID]; ok {
			return p, nil
		}
		def, err := s.defs.DefinitionByVersionID(ctx, versionID)
		if err != nil {
			return ExecPlan{}, err
		}
		p, err := compilePinned(def)
		if err != nil {
			return ExecPlan{}, err
		}
		plans[versionID] = p
		return p, nil
	}
	// ... existing all / by-projects branch, then per row:
	//   snap, err := snapshotFromRow(r.PolicySnapshot)
	//   plan, err := compiled(r.WorkflowVersionID)
	//   ev, err := gateEvidence(ctx, q, plan, r.RunID, r.NodeID, snap, r.RequesterID)
	//   out = append(out, waitingGateFrom(..., ev))
}
```

Collapse the two branches into one row-mapping function so the admin and member paths cannot diverge — today they are two nearly identical loops and this task doubles what each does.

`WaitingGate` gains `Evidence GateEvidence`; `waitingGateFrom` takes it as its last parameter.

- [ ] **Step 5: Put it on the run's gate phases**

`backend/internal/runs/tasks.go` — `Phase` gains:

```go
	// Evidence is present ONLY on a gate attempt still awaiting a decision.
	// A decided gate carries its Decision instead, which already names the
	// examined artifact; a non-gate phase has neither.
	Evidence *GateEvidence
```

In `GetRunDetail`, after the phase rows and decisions are read, resolve evidence for every phase whose status is `waiting_decision`. It compiles the run's plan at most once (all phases of a run share one definition) and does so **only when such a phase exists** — a run with no waiting gate must not pay for a definition read:

```go
	var plan ExecPlan
	planLoaded := false
	for i, p := range phases {
		if p.Status != PhaseWaitingDecision {
			continue
		}
		if !planLoaded {
			task, err := q.GetTask(ctx, run.TaskID)
			if err != nil {
				return RunDetail{}, err
			}
			def, err := s.defs.DefinitionByVersionID(ctx, task.WorkflowVersionID)
			if err != nil {
				return RunDetail{}, err
			}
			if plan, err = compilePinned(def); err != nil {
				return RunDetail{}, err
			}
			// requesterID and the snapshot come off rows already read above.
			planLoaded = true
		}
		ev, err := gateEvidence(ctx, q, plan, run.ID, p.NodeID, run.Snapshot, taskRequesterID)
		if err != nil {
			return RunDetail{}, err
		}
		phases[i].Evidence = &ev
	}
```

- [ ] **Step 6: Project it, and compute `can_decide` where the grants are**

`backend/internal/runs/api/dto.go`:

```go
// GateEvidenceResponse is one gate's decision surface, answered server-side.
// It is the SAME shape on a list row and on a run's gate phase because §3.4 and
// §3.5 are one component with two hosts; a second shape would be a second
// renderer, and the two would drift.
type GateEvidenceResponse struct {
	// ExaminedArtifactID is empty ONLY where the pinned definition binds an
	// artifact the run never produced — a state decideGate also refuses. The
	// surface says the evidence is missing; it does not render an empty diff.
	ExaminedArtifactID string `json:"examined_artifact_id" required:"true"`
	// A plan and a patch render differently and the client must know which
	// before it fetches the payload.
	ExaminedArtifactType string `json:"examined_artifact_type" required:"true"`
	// Required AND nullable. "This gate has no verdict" is a value the client
	// handles, not a key it fails to find.
	SummaryArtifactID *string `json:"summary_artifact_id" required:"true"`
	// CanDecide covers BOTH refusals in §5, which are independent: this
	// endpoint lists gates for any grant while decideGate needs approve, and
	// the run's policy may forbid the requester deciding their own gate.
	// Where it is false the client suppresses ALL THREE controls — gates.go:188
	// refuses before it branches on decision type.
	CanDecide bool `json:"can_decide" required:"true"`
	// One of "", "not_approver", "self_approval_forbidden". Empty exactly when
	// CanDecide is true.
	CannotDecideReason string `json:"cannot_decide_reason" required:"true"`
	// WallDeadline rides along because §5's third refusal is displayable BEFORE
	// it bites: a run past its deadline can no longer be decided (gates.go:176)
	// and the deadline is on the run, so a card can say "this expires in 20
	// minutes" instead of only reporting the 409 afterwards. It is NOT part of
	// CanDecide — that would make the flag a function of the client's clock,
	// and the database clock is the one that decides.
	WallDeadline time.Time `json:"wall_deadline" required:"true"`
}
```

`GateEvidence` carries `WallDeadline time.Time` for it; the two waiting-gate queries already join `runs`, so add `r.wall_deadline` to the `SELECT` in Step 1 alongside `r.policy_snapshot`, and `GetRunDetail` takes it off the run row it has already read.

`ApprovalResponse` gains `Evidence GateEvidenceResponse \`json:"evidence" required:"true"\`` (always present — every row is a waiting gate). `PhaseResponse` gains `Evidence *GateEvidenceResponse \`json:"evidence"\`` (nil off a gate, and off a decided one).

`backend/internal/runs/api/api.go` — the decidability rule, computed once per row from data already in hand:

```go
// decidability answers §5 for one gate. It is SERVER-SIDE because neither half
// is visible from the list: policy_snapshot is on the run (so a client would
// fetch each one — the N+1 this endpoint exists to prevent) and the approve
// grant is not in the response at all.
//
// ORDER MATTERS AND IS DELIBERATE. A caller who is both the requester and
// short of approve gets "not_approver", because that is the one an operator can
// act on: a grant can be raised, and self-approval is a property of a run
// already created. Both are true; the message names the fixable one.
func decidability(level authn.GrantLevel, isAdmin bool, actorID, requesterID uuid.UUID, allowSelf bool) (bool, string) {
	if !isAdmin && !level.AtLeast(authn.GrantApprove) {
		return false, "not_approver"
	}
	if actorID == requesterID && !allowSelf {
		return false, "self_approval_forbidden"
	}
	return true, ""
}
```

In `handleListApprovals`, build `map[uuid.UUID]authn.GrantLevel` from the `GrantsForUser` call the handler **already makes** — no new query — and pass `sess.Role == authn.RoleAdmin`. In `handleGetRun`, call `a.authz.Authorize(ctx, sess.Principal, projectID, authn.GrantApprove)` once for the run and apply the same function; `runScoped` already returns the project id.

**Do not skip the admin branch.** `Authorize` returns true for an admin regardless of grants, and the map built from `GrantsForUser` is empty for one — an admin whose inbox says `not_approver` on every row is the failure this bullet exists to prevent.

- [ ] **Step 7: Run the integration test and confirm it passes**

Run: `cd backend && go test -tags integration ./internal/runs/api/ -run TestWaitingGateCarriesItsEvidence -v`
Expected: PASS, all seven subtests.

Then the existing suite, which asserts `ApprovalResponse` field-by-field at `api_integration_test.go:1256` and will need its literal extended:

Run: `cd backend && go test -tags integration ./internal/runs/api/`
Expected: PASS. Update the `want := runsapi.ApprovalResponse{...}` literal in place; **do not** relax the comparison to a subset — exactness there is what catches a transposed field.

- [ ] **Step 8: Mutation proofs — three, each on a different guard**

```bash
cd backend
# 1. can_decide ignores the grant (the "seeing is deciding" mistake).
#    Expect: "can_decide is true for a principal decideGate refuses".
# 2. Suppress only approve — i.e. decidability returns true for
#    request_changes/reject. Expect the all-three loop to fail.
#    (Mutate the TEST's loop to `[]string{"approve"}` and confirm it then
#    passes with the broken rule: that is the proof the loop is load-bearing.)
# 3. summary resolution falls back to "the newest review_verdict on the run"
#    rather than the compiled input. Expect the generation-2 subtest to fail.
```

Run each, record the failure text, `git checkout --` the file, and confirm green again. Proof 2 is the one the spec singles out as the plausible-looking mistake; do not skip it.

- [ ] **Step 9: Regenerate the contract and confirm the change is additive**

```bash
task contracts:gen
git diff backend/api/openapi.json | grep '^-' | grep -v '^---'
```

Expected: nothing but reformatting context — no removed property, no removed status.

```bash
grep -A 12 'ApiGateEvidenceResponse:' web/src/api/schema.d.ts
```

Expected: `summary_artifact_id: string | null` (present, nullable), `can_decide: boolean`, no `?` on any of the five.

- [ ] **Step 10: Gate and commit**

```bash
task contracts:check
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git add backend web/src/api/schema.d.ts
git commit -m "feat(runs): put a waiting gate's evidence and its decidability on the wire"
```

---

### Task 3: Artifact payload retrieval

**No endpoint returns artifact content today.** `ArtifactResponse` is metadata only. This is the single hardest blocker in 12a: the decision surface exists to show the artifact a gate examines, and that artifact is currently unreachable from the client. Content is cheap to serve — `artifacts.payload` is a `jsonb` column, not a blob on disk.

**Files:**
- Create: `backend/internal/runs/api/artifact_integration_test.go`
- Modify: `backend/internal/runs/tasks.go` — `ArtifactPayload`
- Modify: `backend/internal/runs/api/api.go` — the `getArtifact` operation and handler
- Modify: `backend/internal/runs/api/dto.go` — `ArtifactPayloadResponse`
- Modify: `backend/internal/contracts/contracts.go` — `artifactParams` + `requestOverrides`
- Regenerate: `backend/api/openapi.json`, `web/src/api/schema.d.ts`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `GET /api/v1/runs/{id}/artifacts/{artifact_id}` → `ApiArtifactPayloadResponse{id, run_id, node_id, attempt, generation, type, parent_ids, created_at, payload}`, all required. Task 12's `artifact-query.ts` is the consumer.

- [ ] **Step 1: Write the failing integration test**

`backend/internal/runs/api/artifact_integration_test.go`:

```go
//go:build integration

package api_test

import (
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestArtifactPayloadRetrieval(t *testing.T) {
	h := newHarness(t)
	run, patchID := h.stageRunWithPatchArtifact(t) // project A

	t.Run("a viewer of the run reads the payload", func(t *testing.T) {
		got := h.getArtifact(t, "viewer-tok", run, patchID)
		if got.ID != patchID.String() || got.Type != "patch" {
			t.Fatalf("got %+v", got)
		}
		raw, ok := got.Payload["diff"].(string)
		if !ok {
			t.Fatal("payload has no diff string")
		}
		// THE CONTRACT IS BASE64 AND THIS IS WHERE IT IS PINNED. collect.go
		// encodes because a patch is arbitrary bytes: a file in a legacy
		// encoding diffs as non-UTF-8, and JSON string encoding would replace
		// those bytes with U+FFFD SILENTLY — the mangled patch still applies.
		if _, err := base64.StdEncoding.DecodeString(raw); err != nil {
			t.Errorf("diff is not base64: %v", err)
		}
	})

	t.Run("grant scoping is on the RUN, not on the artifact id", func(t *testing.T) {
		// An artifact id must not be usable to reach across a project boundary.
		resp := h.do(t, http.MethodGet, artifactPath(run, patchID), "approver-b-tok", nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("cross-project artifact read = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("an artifact of a DIFFERENT run is 404, not 200", func(t *testing.T) {
		// The path names a run; addressing another run's artifact through it
		// must not work even when the caller may see both. Without the
		// run_id check the handler is a bare id lookup and the path segment is
		// decoration.
		otherRun, _ := h.stageRunWithPatchArtifact(t)
		resp := h.do(t, http.MethodGet, artifactPath(otherRun, patchID), "approver-tok", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("artifact of another run = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("an unknown artifact id is 404 for everyone", func(t *testing.T) {
		resp := h.do(t, http.MethodGet, artifactPath(run, uuid.New()), "approver-tok", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("unknown artifact = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("a malformed artifact id is 404, never 500", func(t *testing.T) {
		resp := h.do(t, http.MethodGet, "/api/v1/runs/"+run.String()+"/artifacts/not-a-uuid", "approver-tok", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("malformed artifact id = %d, want 404", resp.StatusCode)
		}
	})
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd backend && go test -tags integration ./internal/runs/api/ -run TestArtifactPayloadRetrieval -v`
Expected: FAIL — 404 on every subtest (no route), and a compile error on `h.getArtifact`.

- [ ] **Step 3: The service read**

`backend/internal/runs/tasks.go`:

```go
// ArtifactWithPayload is one artifact including its content. It is separate
// from ArtifactMeta because the run projection deliberately carries metadata
// only: a run with twenty artifacts would otherwise ship every patch on every
// page load.
type ArtifactWithPayload struct {
	ArtifactMeta
	Payload map[string]any
}

// ArtifactPayload reads one artifact of one run.
//
// The RUN ID IS PART OF THE PREDICATE and not merely of the path. Authorization
// upstream is scoped on the run, so an artifact fetched by bare id would let
// one project's run id authorize another project's artifact. Belt and braces:
// the caller checks the run, this checks the artifact belongs to it.
func (s *Service) ArtifactPayload(ctx context.Context, runID, artifactID uuid.UUID) (ArtifactWithPayload, error) {
	row, err := store.New(s.pool).GetArtifactByID(ctx, artifactID)
	if err != nil {
		return ArtifactWithPayload{}, notFound(err, "no such artifact")
	}
	if row.RunID != runID {
		// 404 rather than 403: the caller has been authorized for THIS run, and
		// telling them an id exists elsewhere is an existence oracle.
		return ArtifactWithPayload{}, xerrors.E(xerrors.ClassNotFound, "no such artifact")
	}
	payload, err := unmarshalMap(row.Payload)
	if err != nil {
		return ArtifactWithPayload{}, err
	}
	parents, err := parentIDsFromRow(row.ParentIds)
	if err != nil {
		return ArtifactWithPayload{}, err
	}
	return ArtifactWithPayload{
		ArtifactMeta: ArtifactMeta{
			ID: row.ID, NodeID: row.NodeID, Attempt: row.Attempt, Generation: row.Generation,
			Type: row.Type, ParentIDs: parents, CreatedAt: row.CreatedAt,
		},
		Payload: payload,
	}, nil
}
```

Reuse whatever `GetRunDetail` already uses to decode `parent_ids`; if that logic is inline there, extract it to `parentIDsFromRow` in this task and have both call it.

- [ ] **Step 4: The operation and handler**

`backend/internal/runs/api/api.go`, in `Operations`, after `getRun`:

```go
		{
			Pattern: "GET /api/v1/runs/{id}/artifacts/{artifact_id}", ID: "getArtifact",
			Summary: "Read one artifact of a run, including its payload (view). " +
				"A `patch` payload carries `diff` as base64 by contract — a patch is " +
				"arbitrary bytes and JSON string encoding would replace invalid UTF-8 " +
				"silently. Decode once for display; never round-trip it.",
			Response: ArtifactPayloadResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleGetArtifact),
		},
```

```go
// handleGetArtifact serves one artifact's content. Authorization is on the RUN
// (spec §2.2): an artifact id may not be used to reach across a project
// boundary, so the run is resolved and authorized first and the artifact is
// then required to belong to it.
func (a *API) handleGetArtifact(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	run, _, ok := a.runScoped(w, r, sess, authn.GrantView)
	if !ok {
		return
	}
	artifactID, err := uuid.Parse(r.PathValue("artifact_id"))
	if err != nil {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassNotFound, "no such artifact"))
		return
	}
	art, err := a.svc.ArtifactPayload(r.Context(), run.ID, artifactID)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, artifactPayloadResponse(run.ID, art))
}
```

`dto.go`:

```go
// ArtifactPayloadResponse is one artifact WITH its content. The metadata
// duplicates ArtifactResponse deliberately: this endpoint is reachable without
// having fetched the run, and a client that had to correlate two responses to
// learn an artifact's type would be one join away from rendering a plan as a
// diff.
type ArtifactPayloadResponse struct {
	ID         string         `json:"id" required:"true"`
	RunID      string         `json:"run_id" required:"true"`
	NodeID     string         `json:"node_id" required:"true"`
	Attempt    int32          `json:"attempt" required:"true"`
	Generation int32          `json:"generation" required:"true"`
	Type       string         `json:"type" required:"true"`
	ParentIDs  []string       `json:"parent_ids" required:"true"`
	CreatedAt  time.Time      `json:"created_at" required:"true"`
	Payload    map[string]any `json:"payload" required:"true"`
}
```

- [ ] **Step 5: Register the two-path-parameter request shape**

`backend/internal/contracts/contracts.go` — without this, `requestStructure` **panics** at generation time (it panics loudly by design when a path has `{id}` and no wrapper):

```go
// artifactParams covers GET …/runs/{id}/artifacts/{artifact_id} — two path
// parameters, no body.
type artifactParams struct {
	idParams
	ArtifactID string `path:"artifact_id" description:"artifact id (uuid)"`
}
```

and `"getArtifact": artifactParams{},` in `requestOverrides`.

- [ ] **Step 6: Run the test and confirm it passes**

Run: `cd backend && go test -tags integration ./internal/runs/api/ -run TestArtifactPayloadRetrieval -v`
Expected: PASS, all five subtests.

- [ ] **Step 7: Mutation proof**

```bash
cd backend
# Drop the run_id predicate — the handler becomes a bare id lookup.
perl -0pi -e 's/\tif row\.RunID != runID \{/\tif false {/' internal/runs/tasks.go
go test -tags integration ./internal/runs/api/ -run TestArtifactPayloadRetrieval
git checkout -- internal/runs/tasks.go
```

Expected: FAIL on "an artifact of a DIFFERENT run is 404" — and note that the cross-project subtest still PASSES, because `runScoped` catches it. That asymmetry is the point: the two checks cover different holes and only one test covers each.

- [ ] **Step 8: Regenerate, gate and commit**

```bash
task contracts:gen && task contracts:check
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git add backend web/src/api/schema.d.ts
git commit -m "feat(runs): serve one artifact's payload, grant-scoped on the run"
```

---

### Task 4: `GET /api/v1/runs` — the list, and the DTO that makes it worth building

`/runs` is a committed nav destination with compiler-checked route strings, and nothing lists runs today. A client-side fan-out over every task is an N+1 waterfall on the device least able to afford it.

**The list entry is its own DTO, and that is the half that matters.** `RunResponse` is reused today as a reduced list entry (`api.go:361`) with no task title, no repo or workflow label and no phase summary — so a Home or Tasks card built on it fetches again per row and reintroduces exactly the N+1 this endpoint removes. And the array of phases is not a convenience: locked Home and Tasks both show a **row** of phase dots — done, active, gate, pending — and a scalar "current phase" cannot render a run's shape.

**Files:**
- Create: `backend/internal/runs/runlist.go`, `backend/internal/runs/runlist_test.go`
- Create: `backend/internal/runs/api/runlist_integration_test.go`
- Modify: `backend/internal/runs/store/queries.sql` (+ regenerate)
- Modify: `backend/internal/runs/ports.go` — `RunLabelSource`, `Params.Labels`
- Modify: `backend/internal/runs/api/api.go`, `dto.go`
- Modify: `backend/internal/app/runsports.go`, `backend/internal/app/server.go` — the adapter and its wiring

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - Go: `runs.RunSummary`, `runs.PhaseSummary`, `runs.RunListFilter{Status string; TaskID *uuid.UUID; ProjectIDs []uuid.UUID; All bool; Limit int32; Cursor string}`, `(*Service).ListRuns(ctx, RunListFilter) ([]RunSummary, string, error)`, `runs.RunLabelSource`.
  - Wire: `GET /api/v1/runs?status=&task_id=&cursor=&limit=` → `ApiRunListPageResponse{runs: ApiRunSummaryResponse[], next_cursor: string|null}`.
  - `ApiRunSummaryResponse{id, task_id, task_title, project_id, project_name, repo_slug, workflow_name, status, attempt, generation, requester_id, created_at, started_at, finished_at, phase_summary}`.
  - `ApiPhaseSummaryResponse{node_id, node_type, status: string|null, attempt, generation}`.
  - Task 9's `runs-query.ts` is the consumer.

- [ ] **Step 1: Write the failing pure test for the cursor**

`backend/internal/runs/runlist_test.go`:

```go
package runs

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRunCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 8, 16, 12, 0, 0, 123456789, time.UTC)
	id := uuid.MustParse("018f5f0e-0000-7000-8000-000000000001")

	enc := encodeRunCursor(at, id)
	gotAt, gotID, err := decodeRunCursor(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// NANOSECONDS SURVIVE OR THE CURSOR IS WRONG. uuidv7 ids are minted from a
	// millisecond clock, so two runs created in the same millisecond are
	// separated only by id — and a cursor that truncated the timestamp would
	// re-emit or skip exactly those rows, silently, only under load.
	if !gotAt.Equal(at) {
		t.Errorf("created_at round trip = %v, want %v", gotAt, at)
	}
	if gotID != id {
		t.Errorf("id round trip = %v, want %v", gotID, id)
	}
}

func TestRunCursorRejectsGarbage(t *testing.T) {
	// A cursor is OPAQUE to the client, which means a client will eventually
	// send back something that is not one — a truncated URL, a stale bookmark,
	// a hand-typed value. Every one of these must be a 400 with a field error,
	// never a panic and never a silent page-one.
	for _, bad := range []string{
		"", "!!!", "Zm9v", // valid base64, wrong shape
		"MjAyNi0wOC0xNlQxMjowMDowMFo=",                 // timestamp, no id
		"MjAyNi0wOC0xNlQxMjowMDowMFp8bm90LWEtdXVpZA==", // id is not a uuid
		"bm90LWEtdGltZXxub3QtYS11dWlk",                 // neither half parses
	} {
		if _, _, err := decodeRunCursor(bad); err == nil {
			t.Errorf("decodeRunCursor(%q) accepted garbage", bad)
		}
	}
}

func TestPhaseSummaryOrderFollowsThePlan(t *testing.T) {
	// The dots render the RUN'S SHAPE, so their order is the compiled
	// definition's topological order and never the order rows came back in.
	// Materialization order agrees with it today; a fan-out plus a retry is
	// where they part company, and by then nobody would look here.
	plan := gateDoc(t, ArtifactKindPatch)             // n0 -> gate
	live := map[string]PhaseSummary{                  // deliberately reversed
		"gate": {NodeID: "gate", Status: ptrPhase(PhaseWaitingDecision), Attempt: 1, Generation: 1},
		"n0":   {NodeID: "n0", Status: ptrPhase(PhaseSucceeded), Attempt: 1, Generation: 1},
	}
	got := phaseSummaries(plan, live)
	if len(got) != 2 || got[0].NodeID != "n0" || got[1].NodeID != "gate" {
		t.Fatalf("phase order = %v, want [n0 gate]", got)
	}
	if got[0].NodeType != KindImplement || got[1].NodeType != KindGate {
		t.Errorf("node types = %q/%q, want implement/gate", got[0].NodeType, got[1].NodeType)
	}
}

func TestPhaseSummaryKeepsUnmaterializedNodesNull(t *testing.T) {
	// A node the orchestrator has not reached yet is a DIM DOT, and its status
	// is null rather than "pending": `pending` is a real phase status meaning
	// "materialized and claimable", and conflating the two tells the client a
	// row exists that does not.
	plan := gateDoc(t, ArtifactKindPatch)
	got := phaseSummaries(plan, map[string]PhaseSummary{
		"n0": {NodeID: "n0", Status: ptrPhase(PhaseRunning), Attempt: 1, Generation: 1},
	})
	if len(got) != 2 {
		t.Fatalf("want an entry for every node of the plan, got %d", len(got))
	}
	if got[1].NodeID != "gate" || got[1].Status != nil {
		t.Errorf("unmaterialized gate = %+v, want status nil", got[1])
	}
	if got[1].Attempt != 0 || got[1].Generation != 0 {
		t.Errorf("unmaterialized gate carries attempt/generation %d/%d, want 0/0", got[1].Attempt, got[1].Generation)
	}
}
```

`ptrPhase` is a two-line helper in the test file. `gateDoc` is Task 1's.

- [ ] **Step 2: Run them and watch them fail**

Run: `cd backend && go test ./internal/runs/ -run 'TestRunCursor|TestPhaseSummary' -v`
Expected: FAIL — `undefined: encodeRunCursor`, `undefined: phaseSummaries`.

- [ ] **Step 3: The pure half**

`backend/internal/runs/runlist.go`:

```go
package runs

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// defaultRunPageLimit and maxRunPageLimit bound one page. This endpoint is
// paginated FROM THE OUTSET because it is new: GET /tasks and GET /approvals
// already have the cliff, and there is no reason to build a third.
const (
	defaultRunPageLimit int32 = 50
	maxRunPageLimit     int32 = 200
)

// PhaseSummary is one dot in a run card's phase row.
//
// Status is a POINTER because "the orchestrator has not materialized this node
// yet" is not a phase status. `pending` means a row exists and is claimable;
// null means no row exists at all, and only the client that knows the
// difference can draw a dim dot rather than a queued one.
//
// NodeType rides along because a gate dot is drawn differently from a work dot
// and a client must not infer a node's type from its id string.
type PhaseSummary struct {
	NodeID     string
	NodeType   string
	Status     *PhaseStatus
	Attempt    int32
	Generation int32
}

// RunSummary is a run as a LIST ROW. It is a different projection from
// RunResponse and that is the point: everything a Home or Tasks card renders is
// here, so the card costs no second request (spec §2.1).
type RunSummary struct {
	ID          uuid.UUID
	TaskID      uuid.UUID
	TaskTitle   string
	ProjectID   uuid.UUID
	ProjectName string
	RepoSlug    string
	// WorkflowName is the workflow's current name, resolved through the pinned
	// VERSION. A rename is therefore visible on historical runs, which is the
	// honest behaviour: the workflow is the thing that was renamed.
	WorkflowName string
	Status       RunStatus
	// Attempt is the attempt ordinal of the newest phase row on this run — what
	// a card means by "attempt 2". A run has no attempt of its own; attempts
	// are per node, and the per-node ones are in Phases.
	Attempt     int32
	Generation  int32
	RequesterID uuid.UUID
	// Three real columns. `runs` has no updated_at, and synthesising one would
	// mean choosing which of these the client meant; the card renders
	// finished_at ?? started_at ?? created_at and the choice stays visible.
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	Phases     []PhaseSummary
}

// RunListFilter is one page request. All is the admin view; ProjectIDs is the
// exact grant set otherwise, and an empty one with All false is an empty page
// rather than everything.
type RunListFilter struct {
	All        bool
	ProjectIDs []uuid.UUID
	Status     string
	TaskID     *uuid.UUID
	Limit      int32
	Cursor     string
}

// encodeRunCursor is the opaque page marker. It carries BOTH halves of the sort
// key, so the cursor and the ORDER BY cannot disagree about where a page ended
// — the failure that makes a paginated list silently drop or repeat rows.
//
// RFC3339Nano, because uuidv7 ids come off a millisecond clock: two runs
// created in the same millisecond are separated only by id, and a truncated
// timestamp would put them on the wrong side of the boundary.
func encodeRunCursor(createdAt time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString(
		[]byte(createdAt.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

// decodeRunCursor is deliberately strict. A cursor is opaque to the client, so
// a malformed one is an ordinary thing to receive — from a truncated URL, a
// stale bookmark, a hand-typed value — and every one of them must be a field
// error rather than a panic or a silent page-one.
func decodeRunCursor(s string) (time.Time, uuid.UUID, error) {
	invalid := xerrors.E(xerrors.ClassInvalid, "validation failed").
		WithFields(xerrors.FieldError{Field: "cursor", Message: "not a cursor this endpoint issued"})
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, uuid.Nil, invalid
	}
	at, rest, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, uuid.Nil, invalid
	}
	createdAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, uuid.Nil, invalid
	}
	id, err := uuid.Parse(rest)
	if err != nil {
		return time.Time{}, uuid.Nil, invalid
	}
	return createdAt, id, nil
}

// phaseSummaries renders the run's whole shape: every node of the compiled
// definition, in topological order, whether or not it has a row yet.
//
// EVERY node, not only the materialized ones. The locked cards show pending
// dots ahead of the active one, and a list that grew as the run advanced would
// render a two-node run and a ten-node run identically until the ninth phase.
func phaseSummaries(plan ExecPlan, live map[string]PhaseSummary) []PhaseSummary {
	out := make([]PhaseSummary, 0, len(plan.Order))
	for _, id := range plan.Order {
		entry := PhaseSummary{NodeID: id, NodeType: plan.Nodes[id].Kind}
		if got, ok := live[id]; ok {
			entry.Status, entry.Attempt, entry.Generation = got.Status, got.Attempt, got.Generation
		}
		out = append(out, entry)
	}
	return out
}

func clampRunPageLimit(n int32) int32 {
	switch {
	case n <= 0:
		return defaultRunPageLimit
	case n > maxRunPageLimit:
		return maxRunPageLimit
	default:
		return n
	}
}

func (f RunListFilter) validate() error {
	if f.Status != "" && !RunStatus(f.Status).Valid() {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "status", Message: "not a run status"})
	}
	return nil
}

var _ = fmt.Sprintf // remove once the file has a real fmt use
```

Use `RunStatus`'s existing validity helper if `status.go` already has one; if it does not, add `func (s RunStatus) Valid() bool` there listing the seven values from `schema.sql:216` and delete the placeholder line above.

- [ ] **Step 4: Run them and watch them pass**

Run: `cd backend && go test ./internal/runs/ -run 'TestRunCursor|TestPhaseSummary' -v`
Expected: PASS, all four.

- [ ] **Step 5: The two queries**

Append to `backend/internal/runs/store/queries.sql`:

```sql
-- name: ListRunsPage :many
-- One page of the run list, newest first.
--
-- The keyset predicate is a ROW-VALUE comparison against the same two columns
-- the ORDER BY names, in the same direction. That is what makes the cursor and
-- the sort key incapable of disagreeing: with `created_at < $x OR (created_at =
-- $x AND id < $y)` written out by hand, a later edit to one half silently drops
-- or repeats the rows that share a timestamp — and uuidv7 ids come off a
-- millisecond clock, so runs sharing one are the common case under load.
--
-- all_projects is passed rather than inferred from an empty array: an empty
-- grant set is an EMPTY page, and `= ANY('{}')` gives exactly that, while an
-- admin needs every row. Conflating them is how an ungranted member sees
-- everything.
SELECT sqlc.embed(r), t.title, t.project_id, t.repo_id, t.workflow_version_id, t.requester_id
FROM runs r
JOIN tasks t ON t.id = r.task_id
WHERE (sqlc.arg(all_projects)::boolean OR t.project_id = ANY(sqlc.arg(project_ids)::uuid[]))
  AND (sqlc.narg(status)::text IS NULL OR r.status = sqlc.narg(status)::text)
  AND (sqlc.narg(task_id)::uuid IS NULL OR r.task_id = sqlc.narg(task_id)::uuid)
  AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
       OR (r.created_at, r.id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY r.created_at DESC, r.id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListLatestPhasesForRuns :many
-- The newest attempt row per (run, node) across a whole page — one query, not
-- one per run. Ordered by PROVENANCE (generation, then attempt) for the same
-- reason GetNodeArtifact is: created_at mixes two independent clocks.
SELECT DISTINCT ON (run_id, node_id) run_id, node_id, attempt, generation, status
FROM phase_runs
WHERE run_id = ANY(sqlc.arg(run_ids)::uuid[])
ORDER BY run_id, node_id, generation DESC, attempt DESC;
```

Regenerate and confirm `ListRunsPageParams` carries the nullable arguments as pointers.

- [ ] **Step 6: The label port and its adapter**

`backend/internal/runs/ports.go`:

```go
// RunLabelSource resolves the display labels a run LIST ROW carries. It is
// separate from DefinitionSource and RepoSource on purpose: those are execution
// ports read on the orchestrator's hot path, and a label is neither. Making
// DefinitionByVersionID fetch a workflow name would put a second query on every
// plan load and every gate decision to serve a string only a list renders.
//
// BATCHED BY CONSTRUCTION. The list resolves distinct ids once per page; the
// per-row alternative is the N+1 this endpoint exists to remove, moved one
// layer down where it is harder to see.
//
// A missing id is an ABSENT map entry and never an error: a repo deleted after
// its runs finished must not take out the list that shows them. The caller
// renders the id.
type RunLabelSource interface {
	ProjectNames(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)
	RepoSlugs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)
	// WorkflowNames is keyed by workflow VERSION id, because that is what a
	// task pins; resolving the workflow behind it is the adapter's job.
	WorkflowNames(ctx context.Context, versionIDs []uuid.UUID) (map[uuid.UUID]string, error)
}
```

`Params` gains `Labels RunLabelSource`. Existing `runs.Params{Snapshot: …}` call sites keep compiling; `ListRuns` refuses loudly when it is nil:

```go
	if s.params.Labels == nil {
		return nil, "", fmt.Errorf("runs: the run label source is not wired")
	}
```

**Loud, not degraded.** A list that silently renders ids where names belong looks like a design choice to everyone who sees it and like a bug to nobody.

`backend/internal/app/runsports.go` — the adapter, the only place that sees both vocabularies:

```go
// runsLabelSource adapts assets + workflows to runs.RunLabelSource. The loops
// are over DISTINCT ids the caller already deduplicated, so this is O(distinct)
// round trips per page and not O(rows) — and the port's shape leaves room for a
// real batch query later without touching the runs context.
type runsLabelSource struct {
	assets    *assets.Service
	workflows *workflows.Service
}

func (s runsLabelSource) ProjectNames(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	list, err := s.assets.ListProjectsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(list))
	for _, p := range list {
		out[p.ID] = p.Name
	}
	return out, nil
}

func (s runsLabelSource) RepoSlugs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(ids))
	for _, id := range ids {
		r, err := s.assets.GetRepo(ctx, id)
		if err != nil {
			// A deleted repo is a missing label, not a failed page.
			if xerrors.ClassOf(err) == xerrors.ClassNotFound {
				continue
			}
			return nil, err
		}
		out[id] = r.Slug
	}
	return out, nil
}

func (s runsLabelSource) WorkflowNames(ctx context.Context, versionIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(versionIDs))
	byWorkflow := map[uuid.UUID]string{}
	for _, vid := range versionIDs {
		v, err := s.workflows.GetVersionByID(ctx, vid)
		if err != nil {
			if xerrors.ClassOf(err) == xerrors.ClassNotFound {
				continue
			}
			return nil, err
		}
		name, ok := byWorkflow[v.WorkflowID]
		if !ok {
			wf, err := s.workflows.GetWorkflow(ctx, v.WorkflowID)
			if err != nil {
				if xerrors.ClassOf(err) == xerrors.ClassNotFound {
					continue
				}
				return nil, err
			}
			name = wf.Name
			byWorkflow[v.WorkflowID] = name
		}
		out[vid] = name
	}
	return out, nil
}
```

Wire it in `internal/app/server.go` where `runs.Params` is built.

- [ ] **Step 7: `ListRuns`**

In `runlist.go`. The sequence, and every step of it is one round trip per page rather than per row:

1. `decodeRunCursor` when `f.Cursor != ""`; `f.validate()`; `clampRunPageLimit`.
2. `q.ListRunsPage` with `page_limit = limit + 1` — **fetch one extra row to learn whether a next page exists.** Counting is the alternative and it is a second query over the same predicate.
3. If `len(rows) > limit`, drop the extra and set `next = encodeRunCursor(lastKept.CreatedAt, lastKept.ID)`; otherwise `next = ""`.
4. `q.ListLatestPhasesForRuns` with the page's run ids.
5. Compile once per **distinct** `workflow_version_id` (the same memo shape as Task 2's `ListWaitingGates`), then `phaseSummaries` per run.
6. Resolve labels: three batched calls over the page's distinct project, repo and version ids.
7. Run-level `Attempt` = the maximum `Attempt` among the run's live phase rows; 0 when it has none.

- [ ] **Step 8: Write the failing API integration test**

`backend/internal/runs/api/runlist_integration_test.go` — the four properties nothing else covers:

```go
//go:build integration

package api_test

import (
	"net/http"
	"testing"
)

func TestListRuns(t *testing.T) {
	h := newHarness(t)
	h.stageRunsForListing(t) // 5 in project A, 2 in project B, mixed statuses

	t.Run("scoped exactly as the approvals inbox is", func(t *testing.T) {
		// The same rule, stated once per endpoint and asserted per endpoint:
		// a member sees the projects they hold ANY grant on and no others.
		a := h.listRuns(t, "approver-tok", "")
		for _, r := range a.Runs {
			if r.ProjectID != h.projectID.String() {
				t.Errorf("project-A approver got a run from %s", r.ProjectID)
			}
		}
		if len(h.listRuns(t, "outsider-tok", "").Runs) != 0 {
			t.Error("a member with no grants got runs")
		}
		if len(h.listRuns(t, "admin-tok", "").Runs) != 7 {
			t.Error("an admin does not see every run")
		}
	})

	t.Run("pages without dropping or repeating a row", func(t *testing.T) {
		// THE PROPERTY, and it is not "the page sizes are right". Walk the
		// whole list two rows at a time and assert the union is exactly the
		// unpaginated set, with no duplicates — which is what a cursor that
		// disagreed with the ORDER BY would break, and only on the rows that
		// share a created_at.
		var seen []string
		cursor := ""
		for page := 0; page < 10; page++ {
			got := h.listRunsPaged(t, "admin-tok", cursor, 2)
			for _, r := range got.Runs {
				seen = append(seen, r.ID)
			}
			if got.NextCursor == nil {
				break
			}
			cursor = *got.NextCursor
		}
		assertSameSet(t, seen, h.allRunIDsNewestFirst(t))
		assertNoDuplicates(t, seen)
		// And the ORDER survives paging, which a set comparison cannot see.
		assertEqualSlices(t, seen, h.allRunIDsNewestFirst(t))
	})

	t.Run("a card needs no second request", func(t *testing.T) {
		got := h.listRuns(t, "approver-tok", "")
		r := got.Runs[0]
		if r.TaskTitle == "" || r.RepoSlug == "" || r.WorkflowName == "" || r.ProjectName == "" {
			t.Errorf("row is missing the labels a card renders: %+v", r)
		}
		if len(r.PhaseSummary) == 0 {
			t.Fatal("row carries no phase summary — the dots would need a fetch per run")
		}
		// Every node of the definition, not only the materialized ones.
		if len(r.PhaseSummary) != h.nodeCountOfWorkflow(t) {
			t.Errorf("phase_summary has %d entries, want one per node (%d)",
				len(r.PhaseSummary), h.nodeCountOfWorkflow(t))
		}
		for _, p := range r.PhaseSummary {
			if p.NodeType == "" {
				t.Errorf("phase %s carries no node_type; a client would have to guess from the id", p.NodeID)
			}
		}
	})

	t.Run("filters", func(t *testing.T) {
		waiting := h.listRuns(t, "admin-tok", "?status=waiting")
		for _, r := range waiting.Runs {
			if r.Status != "waiting" {
				t.Errorf("status filter returned %s", r.Status)
			}
		}
		if len(waiting.Runs) == 0 {
			t.Error("the status filter matched nothing; the fixture must stage at least one waiting run or this asserts nothing")
		}
		byTask := h.listRuns(t, "admin-tok", "?task_id="+h.someTaskID(t).String())
		if len(byTask.Runs) == 0 {
			t.Error("the task filter matched nothing")
		}
	})

	t.Run("a bad cursor and a bad status are 400 with a field error, not 500", func(t *testing.T) {
		for _, q := range []string{"?cursor=!!!", "?status=nonsense"} {
			resp := h.do(t, http.MethodGet, "/api/v1/runs"+q, "admin-tok", nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("GET /runs%s = %d, want 400", q, resp.StatusCode)
			}
		}
	})
}
```

- [ ] **Step 9: The operation, handler and DTOs**

`api.go`, in `Operations`, **before** `GET /api/v1/runs/{id}` for readability (the mux does not care):

```go
		{
			Pattern: "GET /api/v1/runs", ID: "listRuns",
			Summary: "List runs on the projects the caller holds any grant on, newest first. " +
				"Cursor-paginated: pass `next_cursor` back as `cursor`; a null `next_cursor` is the last page.",
			Request: ListRunsQuery{}, Response: RunListPageResponse{}, Status: http.StatusOK,
			Errors:  []int{http.StatusUnauthorized},
			Handler: a.authenticated(a.handleListRuns),
		},
```

```go
// ListRunsQuery is the query string, declared as a struct so the parameters
// reach the contract. Every field is `query`-tagged, so the reflector renders
// parameters and no request body.
type ListRunsQuery struct {
	Status string `query:"status" description:"pending | running | waiting | succeeded | failed | superseded | dead"`
	TaskID string `query:"task_id" description:"only this task's runs (uuid)"`
	Cursor string `query:"cursor" description:"opaque; the previous page's next_cursor"`
	Limit  int32  `query:"limit" description:"1-200, default 50"`
}

// RunListPageResponse is one page. next_cursor is required AND nullable: null
// means "this was the last page", which is a value the client branches on, not
// a key it fails to find.
type RunListPageResponse struct {
	Runs       []RunSummaryResponse `json:"runs" required:"true"`
	NextCursor *string              `json:"next_cursor" required:"true"`
}
```

`RunSummaryResponse` and `PhaseSummaryResponse` mirror the Go structs, every field `required:"true"`, with `Status *string` on the phase summary and `StartedAt`/`FinishedAt` as `*time.Time`.

The handler parses the query with the same care the test demands: an unparseable `task_id` is a field error on `task_id`, not a 404 and not a silent ignore.

- [ ] **Step 10: Run everything and confirm it passes**

```bash
cd backend
go test ./internal/runs/
go test -tags integration ./internal/runs/api/ -run TestListRuns -v
```
Expected: PASS.

- [ ] **Step 11: Mutation proofs**

```bash
cd backend
# 1. The cursor loses its id half — keyset on created_at only.
#    Expect: the paging subtest fails on duplicates or on a dropped row.
#    IF IT DOES NOT, the fixture has no two runs sharing a created_at and the
#    subtest is vacuous. Stage two runs in one transaction and re-run.
# 2. phaseSummaries returns only the materialized nodes.
#    Expect: "phase_summary has N entries, want one per node".
# 3. all_projects is inferred from len(project_ids) == 0.
#    Expect: "a member with no grants got runs".
```

Record each failure, revert, confirm green. Proof 1's caveat is the one that matters — check it rather than assuming.

- [ ] **Step 12: Regenerate, gate and commit**

```bash
task contracts:gen && task contracts:check
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git add backend web/src/api/schema.d.ts
git commit -m "feat(runs): list runs with the projection a card actually needs"
```

---

### Task 5: Document schema 2, and a payload contract for `plan` and `review_verdict`

Today a declared artifact's payload is `map[string]any`. The runner checks the type name and scans for secrets; nothing constrains the keys. The approval surface cannot render evidence it has no shape for.

**The compatibility rule is decided, not deferred, and it needs no new column.** A workflow definition document carries its schema version, a `workflow_versions` row is immutable, and a run pins its definition at intake. So: **payload validation is a property of the document schema version.** Schema-1 documents are never validated — exactly today's behaviour, preserved. Payload schemas arrive with schema 2. The prompt and the validator read the same pin, so a node is asked for the shape its own pinned document defines and judged against that same one. In-flight attempts are safe by construction and no drain is needed. Existing workflows opt in by registering a new version, which is already how any graph change happens. Rollback is deploying the old binary: v1 runs are unaffected, and a v2 document is refused by the old `ParseDocument` at registration rather than mis-executed.

This task ships the **contract**. Task 6 ships enforcement and the producers; splitting them is what lets the contract be reviewed against the mockup before anything can fail a run.

**Files:**
- Create: `backend/internal/runs/payload.go`, `backend/internal/runs/payload_test.go`
- Create: `backend/internal/runs/api/payload_dto.go`
- Create: `backend/internal/contracts/payload_schema_test.go`
- Modify: `backend/internal/workflows/definition.go`, `definition_test.go`
- Modify: `backend/internal/runs/compile.go` — `DefinitionDoc.Schema`, `ExecPlan.SchemaVersion`
- Modify: `backend/internal/app/runsports.go` — map the schema onto the mirror
- Regenerate: `backend/api/openapi.json`, `web/src/api/schema.d.ts`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `workflows.SchemaVersion = 2`, `workflows.MinSchemaVersion = 1`, `ParseDocument` accepting both.
  - `runs.DefinitionDoc.Schema int` → `runs.ExecPlan.SchemaVersion int`.
  - `runs.ValidateArtifactPayload(schemaVersion int, artifactType string, payload map[string]any) error` and `runs.RequiredPayloadKeys` (`map[string][]string`).
  - Wire: `ApiPlanPayload`, `ApiPlanStep`, `ApiReviewVerdictPayload`, `ApiReviewFinding`, `ApiTestOutcome`. Task 12 renders them.

- [ ] **Step 1: Write the failing validator table**

`backend/internal/runs/payload_test.go`:

```go
package runs

import "testing"

func TestValidateArtifactPayload(t *testing.T) {
	goodPlan := map[string]any{
		"summary": "Adds session revocation.",
		"steps":   []any{map[string]any{"title": "Add the table", "detail": "with an index"}},
	}
	goodVerdict := map[string]any{
		"verdict":  "approve",
		"findings": []any{},
		"tests":    map[string]any{"status": "passed", "passed": float64(12), "failed": float64(0)},
	}

	t.Run("a schema-1 document is never validated", func(t *testing.T) {
		// EXACTLY TODAY'S BEHAVIOUR, PRESERVED. Every run created before this
		// deploy pinned a v1 definition, and a deploy cannot retroactively
		// change a pin — which is the whole of the compatibility argument and
		// the reason no drain is needed.
		if err := ValidateArtifactPayload(1, ArtifactKindPlan, map[string]any{"content": "anything"}); err != nil {
			t.Errorf("schema 1 rejected a payload: %v", err)
		}
	})

	t.Run("accepts the minimum", func(t *testing.T) {
		if err := ValidateArtifactPayload(2, ArtifactKindPlan, goodPlan); err != nil {
			t.Errorf("plan: %v", err)
		}
		if err := ValidateArtifactPayload(2, ArtifactKindReviewVerdict, goodVerdict); err != nil {
			t.Errorf("review_verdict: %v", err)
		}
	})

	t.Run("UNKNOWN KEYS PASS", func(t *testing.T) {
		// The opposite of the envelope's DisallowUnknownFields, deliberately.
		// The envelope is written by our runner; a payload is written by an LLM
		// harness, and a harness that adds a useful key must not fail a run.
		p := map[string]any{"summary": "s", "steps": []any{}, "confidence": 0.8, "content": "legacy"}
		if err := ValidateArtifactPayload(2, ArtifactKindPlan, p); err != nil {
			t.Errorf("an extra key failed the payload: %v", err)
		}
	})

	t.Run("a missing required key fails", func(t *testing.T) {
		if err := ValidateArtifactPayload(2, ArtifactKindPlan, map[string]any{"summary": "s"}); err == nil {
			t.Error("a plan with no steps was accepted")
		}
		if err := ValidateArtifactPayload(2, ArtifactKindReviewVerdict,
			map[string]any{"verdict": "approve", "findings": []any{}}); err == nil {
			t.Error("a verdict with no tests key was accepted")
		}
	})

	t.Run("tests says NOT REPORTED with an explicit null, and the key is still required", func(t *testing.T) {
		// "Not reported" must be a value, so the hard-facts row can render the
		// fact as unknown rather than inventing a tick. An ABSENT key would be
		// indistinguishable from a harness that forgot.
		ok := map[string]any{"verdict": "approve", "findings": []any{}, "tests": nil}
		if err := ValidateArtifactPayload(2, ArtifactKindReviewVerdict, ok); err != nil {
			t.Errorf("tests: null was rejected: %v", err)
		}
	})

	t.Run("wrong types and out-of-range enums fail", func(t *testing.T) {
		for name, p := range map[string]map[string]any{
			"steps is not an array":    {"summary": "s", "steps": "one, two"},
			"a step is not an object":  {"summary": "s", "steps": []any{"do it"}},
			"a step has no title":      {"summary": "s", "steps": []any{map[string]any{"detail": "d"}}},
			"summary is not a string":  {"summary": 3, "steps": []any{}},
		} {
			if err := ValidateArtifactPayload(2, ArtifactKindPlan, p); err == nil {
				t.Errorf("plan accepted %s", name)
			}
		}
		for name, p := range map[string]map[string]any{
			"verdict is not in the enum": {"verdict": "lgtm", "findings": []any{}, "tests": nil},
			"findings is not an array":   {"verdict": "approve", "findings": "none", "tests": nil},
			"a finding has no severity":  {"verdict": "approve", "findings": []any{map[string]any{"message": "m"}}, "tests": nil},
			"a finding severity is bogus": {"verdict": "approve", "findings": []any{map[string]any{"severity": "spicy", "message": "m"}}, "tests": nil},
			"tests status is bogus":      {"verdict": "approve", "findings": []any{}, "tests": map[string]any{"status": "maybe"}},
		} {
			if err := ValidateArtifactPayload(2, ArtifactKindReviewVerdict, p); err == nil {
				t.Errorf("review_verdict accepted %s", name)
			}
		}
	})

	t.Run("an unconstrained type is not validated", func(t *testing.T) {
		// patch, log_ref, failure_snapshot, publication and gate_note are the
		// control plane's own or extracted, not declared by a harness. This
		// function must not become a second, weaker check on them.
		for _, k := range []string{ArtifactKindPatch, ArtifactKindLogRef, ArtifactKindPublication} {
			if err := ValidateArtifactPayload(2, k, map[string]any{"whatever": true}); err != nil {
				t.Errorf("%s was validated: %v", k, err)
			}
		}
	})
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd backend && go test ./internal/runs/ -run TestValidateArtifactPayload -v`
Expected: FAIL — `undefined: ValidateArtifactPayload`.

- [ ] **Step 3: Write the validator**

`backend/internal/runs/payload.go`. Hand-written, and the reason is stated in the file: this repo has no JSON Schema runtime and adding one to validate two shapes would be a dependency with more surface than the thing it checks. The **contract** is the generated document (Step 5); Step 7's test is what keeps the two from drifting.

```go
package runs

import (
	"fmt"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// payloadSchemaVersion is the document schema version at which artifact
// payloads start being constrained. Below it, nothing is checked — see
// ValidateArtifactPayload.
const payloadSchemaVersion = 2

// RequiredPayloadKeys is the required set per constrained artifact type,
// EXPORTED so internal/contracts can hold it against the generated document's
// own `required` arrays. Two hand-maintained descriptions of one shape is the
// drift this export exists to make impossible.
var RequiredPayloadKeys = map[string][]string{
	ArtifactKindPlan:          {"summary", "steps"},
	ArtifactKindReviewVerdict: {"verdict", "findings", "tests"},
}

var (
	verdictValues  = map[string]bool{"approve": true, "request_changes": true, "reject": true}
	severityValues = map[string]bool{"info": true, "minor": true, "major": true, "blocker": true}
	testStatuses   = map[string]bool{"passed": true, "failed": true, "not_run": true}
)

// ValidateArtifactPayload checks a declared payload against the shape its
// pinned document version promises.
//
// VALIDATION IS A PROPERTY OF THE DOCUMENT SCHEMA VERSION (spec §2.3). A
// schema-1 document is never validated, which is exactly today's behaviour; a
// run pinned its definition at intake and a deploy cannot retroactively change
// that pin, so every in-flight attempt is safe by construction and no drain is
// needed. The prompt the node was given and the validator that judges it read
// the SAME pin — the failure being avoided is a node instructed in one shape
// and judged against another.
//
// UNKNOWN KEYS ARE ALLOWED THROUGH, which is the opposite of the envelope's
// DisallowUnknownFields and is intentional: the envelope is written by our
// runner, payloads are written by an LLM harness, and a harness that adds a
// useful key must not fail a run.
//
// The required sets are deliberately minimal. A schema stricter than a model
// can reliably produce converts model variance into failed runs, and a
// validation failure here is a PERMANENT attempt failure.
func ValidateArtifactPayload(schemaVersion int, artifactType string, payload map[string]any) error {
	if schemaVersion < payloadSchemaVersion {
		return nil
	}
	switch artifactType {
	case ArtifactKindPlan:
		return validatePlanPayload(payload)
	case ArtifactKindReviewVerdict:
		return validateVerdictPayload(payload)
	default:
		// patch is extracted, log_ref and failure_snapshot are the runtime's own
		// observations, publication and gate_note are written by the control
		// plane. None is a harness's to shape.
		return nil
	}
}

func validatePlanPayload(p map[string]any) error {
	if err := requireString(p, "summary"); err != nil {
		return err
	}
	steps, err := requireArray(p, "steps")
	if err != nil {
		return err
	}
	for i, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			return payloadErr("steps[%d] is not an object", i)
		}
		if err := requireString(step, "title"); err != nil {
			return payloadErr("steps[%d]: %v", i, err)
		}
		if err := optionalString(step, "detail"); err != nil {
			return payloadErr("steps[%d]: %v", i, err)
		}
	}
	return nil
}

func validateVerdictPayload(p map[string]any) error {
	verdict, err := requireEnum(p, "verdict", verdictValues)
	if err != nil {
		return err
	}
	_ = verdict
	findings, err := requireArray(p, "findings")
	if err != nil {
		return err
	}
	for i, raw := range findings {
		f, ok := raw.(map[string]any)
		if !ok {
			return payloadErr("findings[%d] is not an object", i)
		}
		if _, err := requireEnum(f, "severity", severityValues); err != nil {
			return payloadErr("findings[%d]: %v", i, err)
		}
		if err := requireString(f, "message"); err != nil {
			return payloadErr("findings[%d]: %v", i, err)
		}
		for _, opt := range []string{"file"} {
			if err := optionalString(f, opt); err != nil {
				return payloadErr("findings[%d]: %v", i, err)
			}
		}
		if err := optionalNumber(f, "line"); err != nil {
			return payloadErr("findings[%d]: %v", i, err)
		}
	}
	// tests is REQUIRED and NULLABLE. Null means "this harness did not report
	// test results", which the hard-facts row renders as unknown; an absent key
	// would be indistinguishable from a harness that forgot the field.
	raw, ok := p["tests"]
	if !ok {
		return payloadErr("tests is required; use null to say tests were not reported")
	}
	if raw == nil {
		return nil
	}
	tests, ok := raw.(map[string]any)
	if !ok {
		return payloadErr("tests is neither an object nor null")
	}
	if _, err := requireEnum(tests, "status", testStatuses); err != nil {
		return payloadErr("tests: %v", err)
	}
	for _, opt := range []string{"passed", "failed"} {
		if err := optionalNumber(tests, opt); err != nil {
			return payloadErr("tests: %v", err)
		}
	}
	return optionalString(tests, "details")
}

func payloadErr(format string, args ...any) error {
	return xerrors.E(xerrors.ClassInvalid, fmt.Sprintf(format, args...))
}
```

`requireString`, `requireArray`, `requireEnum`, `optionalString`, `optionalNumber` are six-line helpers below. **`optionalX` accepts an absent key and rejects a present-but-wrong-typed one** — that asymmetry is what makes "unknown keys pass" different from "anything passes".

- [ ] **Step 4: Run it and watch it pass**

Run: `cd backend && go test ./internal/runs/ -run TestValidateArtifactPayload -v`
Expected: PASS, all seven subtests.

- [ ] **Step 5: The wire shapes**

`backend/internal/runs/api/payload_dto.go`. These types are **reflected into the OpenAPI document and never constructed by a handler** — say so at the top of the file, because an unused-looking struct is the kind of thing a later cleanup deletes:

```go
// Package-level note: the five types below exist to be REFLECTED. No handler
// constructs one — an artifact payload travels as
// ArtifactPayloadResponse.Payload, a map, because the same endpoint serves
// patch and log_ref payloads that have no Go shape here. What these declare is
// the CONTRACT the client renders against and the runner validates to, and
// contracts/payload_schema_test.go is what stops the two descriptions drifting.

// PlanPayload is a `plan` artifact's payload at document schema 2.
type PlanPayload struct {
	Summary string     `json:"summary" required:"true"`
	Steps   []PlanStep `json:"steps" required:"true"`
}

type PlanStep struct {
	Title string `json:"title" required:"true"`
	// Detail is optional. Absent and empty mean the same thing and the renderer
	// treats them the same; there is no third state worth a null here.
	Detail string `json:"detail"`
}

// ReviewVerdictPayload is a `review_verdict` artifact's payload at schema 2.
type ReviewVerdictPayload struct {
	// One of approve | request_changes | reject — the same vocabulary a human
	// gate decision uses, so a reader is not learning two.
	Verdict  string          `json:"verdict" required:"true"`
	Findings []ReviewFinding `json:"findings" required:"true"`
	// Tests is required AND nullable: null is "this harness did not report test
	// results", which the approval surface renders as unknown rather than as a
	// tick. A stub never fakes success.
	Tests *TestOutcome `json:"tests" required:"true"`
}

type ReviewFinding struct {
	// One of info | minor | major | blocker.
	Severity string `json:"severity" required:"true"`
	Message  string `json:"message" required:"true"`
	// File and Line are empty/zero when the finding is not file- or
	// line-scoped, which is ordinary for a design comment.
	File string `json:"file"`
	Line int32  `json:"line"`
}

type TestOutcome struct {
	// One of passed | failed | not_run. `not_run` is a harness saying it did
	// not run tests; a null TestOutcome is a harness saying nothing at all.
	Status  string `json:"status" required:"true"`
	Passed  int32  `json:"passed"`
	Failed  int32  `json:"failed"`
	Details string `json:"details"`
}
```

These reach the document only if something references them. Add them as `altResponses`-free extra schemas by naming them on the `getArtifact` operation's summary **and** registering them explicitly — the simplest mechanism that works with the current generator is a documentation-only operation-free reflection, so instead: extend `ArtifactPayloadResponse` with

```go
	// PlanPayload and ReviewVerdictPayload are never both set and are never
	// populated by the handler — they are here so the generator emits their
	// schemas, which is what the client renders against. `payload` above is the
	// field that carries the bytes.
	//
	// If this ever feels like a trick: it is. The alternative is teaching
	// internal/contracts to reflect free-standing types, which is a generator
	// change for two structs. Revisit it when there is a third.
	PlanSchema   *PlanPayload          `json:"plan_schema,omitempty"`
	VerdictSchema *ReviewVerdictPayload `json:"verdict_schema,omitempty"`
```

**Stop and reconsider before writing that.** If `internal/contracts` can be given a small `extraSchemas []any` list that `Build` reflects through `r.Spec`, do that instead — it is five lines, it names its intent, and it does not put two fields on a response that the server never sets. Prefer it; fall back to the marker fields only if the reflector will not emit an unreferenced type, and record which one you did.

- [ ] **Step 6: Widen `ParseDocument` to schema 2**

`backend/internal/workflows/definition.go`:

```go
// SchemaVersion is the version NEW documents are written as. MinSchemaVersion
// is the oldest this binary still parses.
//
// Schema 2 differs from 1 in exactly one way and it is not in this file: a
// schema-2 document's agentic nodes are instructed in, and their declared
// payloads validated against, the artifact payload schemas (Plan 12a §2.3).
// The DOCUMENT shape is unchanged, which is why this is a version bump and not
// a new type — and why an existing workflow opts in by registering a new
// version rather than being migrated in place.
//
// Rollback is deploying the old binary: v1 runs are unaffected, and a v2
// document is refused HERE at registration rather than mis-executed.
const (
	SchemaVersion    = 2
	MinSchemaVersion = 1
)
```

and in `ParseDocument`:

```go
	if d.Schema < MinSchemaVersion || d.Schema > SchemaVersion {
		return Document{}, xerrors.E(xerrors.ClassInvalid, "unsupported schema version").
			WithFields(xerrors.FieldError{Field: "schema", Message: fmt.Sprintf("must be %d or %d", MinSchemaVersion, SchemaVersion)})
	}
```

`definition_test.go` — the existing "unsupported schema version" case almost certainly uses `2` or `0` as its rejected value. **Check it.** If it uses `2`, change it to `3`; if it asserts the message text, update it. A test that starts passing for a new reason is worse than one that fails.

`Canonical()` is unchanged: `Schema` is already the first field and stays in the hashed bytes, so a v1 and a v2 document with identical nodes hash differently — correct, they are different documents.

- [ ] **Step 7: Carry the version into the runs mirror**

`compile.go`: `DefinitionDoc` gains `Schema int`; `ExecPlan` gains `SchemaVersion int`; `Compile` copies it (`plan := ExecPlan{Order: order, Nodes: nodes, SchemaVersion: doc.Schema}`).

`app/runsports.go`: `mirror := runs.DefinitionDoc{Schema: doc.Schema}`.

**This is the line the whole compatibility rule rests on.** Without it every run looks like schema 0 to the validator, nothing is ever checked, and the feature is silently absent. Pin it: add a case to `runsports`' existing adapter test asserting `pinnedFromVersion` of a schema-2 document yields `Doc.Schema == 2` — and, in the same test, that a schema-1 document yields 1 rather than the constant.

- [ ] **Step 8: Tie the validator to the generated contract**

`backend/internal/contracts/payload_schema_test.go`:

```go
package contracts

import (
	"encoding/json"
	"testing"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// TestPayloadSchemasMatchTheValidator is the anti-drift guard. Two independent
// descriptions of one shape exist by necessity — a Go struct the generator
// reflects, and a hand-written runtime validator — and the failure mode is
// silent: the client renders against one and the runner enforces the other,
// and a run fails for a field the UI never showed.
func TestPayloadSchemasMatchTheValidator(t *testing.T) {
	raw, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Required []string `json:"required"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for artifactType, schemaName := range map[string]string{
		"plan":           "ApiPlanPayload",
		"review_verdict": "ApiReviewVerdictPayload",
	} {
		schema, ok := doc.Components.Schemas[schemaName]
		if !ok {
			t.Fatalf("%s is not in the document — the payload contract is not published", schemaName)
		}
		got := map[string]bool{}
		for _, r := range schema.Required {
			got[r] = true
		}
		for _, want := range runs.RequiredPayloadKeys[artifactType] {
			if !got[want] {
				t.Errorf("%s: the validator requires %q and the contract does not", schemaName, want)
			}
		}
		if len(schema.Required) != len(runs.RequiredPayloadKeys[artifactType]) {
			t.Errorf("%s: contract requires %v, validator requires %v",
				schemaName, schema.Required, runs.RequiredPayloadKeys[artifactType])
		}
	}
}
```

`internal/contracts` is tooling and not a bounded context (`arch.rules` lists it as `nonContext`), so importing `runs` here is legal. Confirm with `task backend:analyze` rather than assuming.

- [ ] **Step 9: Run everything, regenerate, gate and commit**

```bash
cd backend
go test ./internal/runs/ ./internal/workflows/ ./internal/contracts/ ./internal/app/
cd .. && task contracts:gen && task contracts:check
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git add backend web/src/api/schema.d.ts
git commit -m "feat(workflows,runs): document schema 2 and a published payload contract"
```

Mutation proof for this task: change one entry of `RequiredPayloadKeys` and confirm `TestPayloadSchemasMatchTheValidator` fails naming the field; then change one `required:"true"` in `payload_dto.go` and confirm it fails from the other direction. Both directions, or the guard only holds one way.

---

### Task 6: Enforcement, and the producers that must be told first

**Enabling validation breaks today's producers, so this task carries the migration or it carries nothing.** `fake.Executor` emits `plan:{content}` and `review_verdict:{verdict,notes}` — neither satisfies Task 5's required sets, and that executor is what the Playwright lane runs on, so a validator merged without it fails 12a's own acceptance path. The real harnesses are further off: `docker/harness/*/entrypoint.sh` hands the node's stored instructions to the CLI and appends nothing, so **nothing today tells a model what shape to emit** and the schema would be enforced against producers that were never told it exists.

**One deviation from the spec's wording, stated.** §2.3 asks for "a runner-owned output-shape instruction appended for every harness". This task appends it in `Orchestrator.buildRequest` instead — one edit, applying identically to the Docker runner, the fake executor and any future one, and it is the only place that holds both the node and its pinned schema version. Editing three `entrypoint.sh` files would give three copies of one rule, and the fake — the executor the acceptance lane runs on — would get none of them.

**Files:**
- Modify: `backend/internal/runs/payload.go` — `OutputShapeInstruction`
- Modify: `backend/internal/runs/orchestrator.go` — `evaluate`, `buildRequest`
- Modify: `backend/internal/runs/fake/executor.go`
- Create: `backend/internal/runs/payload_enforcement_integration_test.go`
- Modify: `backend/internal/app/webe2e_test.go` — the workflow constant
- Modify: `docker/harness/*/entrypoint.sh` — **no change; a comment only** (see Step 6)

**Interfaces:**
- Consumes: `runs.ValidateArtifactPayload`, `ExecPlan.SchemaVersion` (Task 5).
- Produces: `runs.OutputShapeInstruction(schemaVersion int, nodeKind string) string`; a schema-2 `fake.Executor` payload set; the E2E workflow shape Task 17 asserts against.

- [ ] **Step 1: Write the failing enforcement test**

`backend/internal/runs/payload_enforcement_integration_test.go`:

```go
//go:build integration

package runs_test

import (
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/runs"
)

func TestPayloadEnforcement(t *testing.T) {
	t.Run("a conforming payload is persisted and the run advances", func(t *testing.T) {
		h := newSpineHarness(t, schemaVersion(2))
		run := h.runToCompletion(t)
		if got := h.runStatus(t, run); got != runs.RunSucceeded {
			t.Fatalf("run = %s, want succeeded — the fake's schema-2 payloads must pass their own validator", got)
		}
	})

	t.Run("a payload with extra keys is persisted unchanged", func(t *testing.T) {
		// The extra key must reach the DATABASE, not merely survive validation.
		// A validator that stripped unknown keys would pass every test in
		// payload_test.go and quietly delete whatever a harness added.
		h := newSpineHarness(t, schemaVersion(2))
		h.executor.PlanExtra(map[string]any{"confidence": 0.8})
		run := h.runToCompletion(t)
		payload := h.artifactPayload(t, run, "plan")
		if _, ok := payload["confidence"]; !ok {
			t.Errorf("the extra key was dropped on the way to storage: %v", payload)
		}
	})

	t.Run("a payload missing a required key fails the attempt PERMANENTLY", func(t *testing.T) {
		h := newSpineHarness(t, schemaVersion(2))
		h.executor.PlanPayload(map[string]any{"content": "the old shape"})
		run := h.runToCompletion(t)
		if got := h.runStatus(t, run); got != runs.RunFailed {
			t.Fatalf("run = %s, want failed", got)
		}
		out := h.phaseOutcome(t, run, "plan")
		if out == nil || out.Code != runs.CodeInvalidOutput {
			t.Fatalf("outcome = %+v, want %s", out, runs.CodeInvalidOutput)
		}
		if out.Class != runs.OutcomePermanent {
			t.Errorf("class = %s, want permanent — retrying reaches the same bad shape", out.Class)
		}
		// PERMANENT MEANS ONE ATTEMPT. A transient classification would burn
		// max_attempts against a model that is going to answer identically.
		if n := h.attemptCount(t, run, "plan"); n != 1 {
			t.Errorf("the node was attempted %d times for a permanent outcome", n)
		}
		// And nothing was persisted: a rejected payload must not be readable.
		if h.artifactCount(t, run, "plan") != 0 {
			t.Error("the rejected payload was stored anyway")
		}
	})

	t.Run("a schema-1 run is untouched by any of this", func(t *testing.T) {
		// The compatibility rule, end to end. Same executor, same bad-by-v2
		// payload, a v1 document — and it succeeds, because a v1 run pinned a
		// document that never promised the shape.
		h := newSpineHarness(t, schemaVersion(1))
		h.executor.PlanPayload(map[string]any{"content": "the old shape"})
		run := h.runToCompletion(t)
		if got := h.runStatus(t, run); got != runs.RunSucceeded {
			t.Fatalf("schema-1 run = %s, want succeeded", got)
		}
	})

	t.Run("the node is TOLD the shape, and only at schema 2", func(t *testing.T) {
		h := newSpineHarness(t, schemaVersion(2))
		h.runToCompletion(t)
		got := h.executor.LastInstructions("plan")
		if !strings.Contains(got, `"summary"`) || !strings.Contains(got, `"steps"`) {
			t.Errorf("a schema-2 plan node was not told its output shape:\n%s", got)
		}
		if !strings.HasPrefix(got, h.nodeInstructions("plan")) {
			t.Error("the appended block displaced the node's own instructions instead of following them")
		}

		h1 := newSpineHarness(t, schemaVersion(1))
		h1.runToCompletion(t)
		if h1.executor.LastInstructions("plan") != h1.nodeInstructions("plan") {
			t.Error("a schema-1 node's instructions were modified; v1 behaviour must be byte-identical")
		}
	})
}
```

`newSpineHarness` builds on the existing `internal/runs` integration harness (`testutil_integration_test.go`); `schemaVersion(n)` sets the document's `schema`. `PlanPayload`, `PlanExtra` and `LastInstructions` are three small additions to `fake.Executor` (Step 4).

- [ ] **Step 2: Run it and watch it fail**

Run: `cd backend && go test -tags integration ./internal/runs/ -run TestPayloadEnforcement -v`
Expected: FAIL — the missing-key run succeeds, and `LastInstructions` does not exist.

- [ ] **Step 3: Validate before the outcome transaction**

`backend/internal/runs/orchestrator.go`, at the top of `evaluate`:

```go
func (o *Orchestrator) evaluate(ctx context.Context, workerID string, cp cachedPlan,
	c claimed, fromStatus PhaseStatus, res ExecutionResult, execErr error,
) {
	// THE PAYLOAD CHECK LIVES HERE, before the transaction, and here is the
	// only place it can live once and cover everything.
	//
	// Not in the runner: collect.go builds artifacts inside a process that does
	// not know which document version the run pinned, and the adoption path
	// reaches this function having never run collect at all. Not inside the
	// transaction: turning a success into a failure after finishPhase has
	// already moved the row would mean unwinding a state machine that has one
	// entry point for exactly this reason.
	//
	// Converting the success into an execErr hands it to evaluateFailure, which
	// is the machinery that already knows how a permanent outcome terminalizes
	// a node — so this adds a rule, not a second failure path.
	if execErr == nil {
		execErr = validateResultPayloads(cp.plan.SchemaVersion, res)
	}
	// ... unchanged from here
}
```

```go
// validateResultPayloads refuses an attempt whose declared payloads do not
// match the shape its own pinned document promised (spec §2.3).
//
// PERMANENT, matching how collect.go already treats an undeclarable artifact
// type: retrying reaches the same harness with the same instructions and the
// same answer, so a transient classification would burn the node's whole
// attempt budget to arrive back here.
func validateResultPayloads(schemaVersion int, res ExecutionResult) error {
	for _, a := range res.Artifacts {
		if err := ValidateArtifactPayload(schemaVersion, a.Type, a.Payload); err != nil {
			return OutcomeError{Outcome: Outcome{
				Class: OutcomePermanent, Code: CodeInvalidOutput,
				Message: fmt.Sprintf("declared %s payload does not match the shape this workflow's schema %d promises: %v",
					a.Type, schemaVersion, err),
			}}
		}
	}
	return nil
}
```

- [ ] **Step 4: Tell the node the shape**

`payload.go`:

```go
// OutputShapeInstruction is what a node is told about the payload it must
// declare. It is APPENDED to the node's own instructions, never substituted
// for them.
//
// Empty below the payload schema version, and that emptiness is the whole
// compatibility guarantee at the prompt end: a schema-1 node's instructions are
// byte-identical to what they were before this plan.
//
// Keyed on the NODE KIND rather than on the artifact type, because that is
// what is known when a request is built. A node may still declare an artifact
// of the other kind — collect.go's allowlist permits plan and review_verdict
// from anywhere — and the validator judges whatever arrives, so this text is
// guidance and the validator is the rule.
//
// It must contain no line beginning with `caesar-fake:`: fake.parseDirective
// scans instructions line by line for that prefix, and a stray one would turn
// every schema-2 node in the test suite into a scripted failure.
func OutputShapeInstruction(schemaVersion int, nodeKind string) string {
	if schemaVersion < payloadSchemaVersion {
		return ""
	}
	switch nodeKind {
	case KindPlan:
		return planShapeBlock
	case KindReview:
		return reviewShapeBlock
	default:
		return ""
	}
}

const planShapeBlock = `

--- caesar: required output shape ---
Write your plan to .caesar/out/artifacts.json as one entry:

  [{"type": "plan", "payload": {
      "summary": "one or two sentences on what this changes and why",
      "steps": [{"title": "short imperative", "detail": "optional elaboration"}]
  }}]

"summary" and "steps" are required; "steps" may be empty. Extra keys are kept.
`

const reviewShapeBlock = `

--- caesar: required output shape ---
Write your verdict to .caesar/out/artifacts.json as one entry:

  [{"type": "review_verdict", "payload": {
      "verdict": "approve" | "request_changes" | "reject",
      "findings": [{"severity": "info"|"minor"|"major"|"blocker",
                    "message": "what is wrong",
                    "file": "optional path", "line": 0}],
      "tests": null
  }}]

"verdict", "findings" and "tests" are required; "findings" may be empty. Set
"tests" to null if you did not run tests, or to
{"status": "passed"|"failed"|"not_run", "passed": 0, "failed": 0,
 "details": "optional"} if you did. Extra keys are kept.
`
```

`orchestrator.go`, in `buildRequest`:

```go
	req := ExecutionRequest{
		Ref: ref, Kind: node.Kind, Repo: cp.repo,
		// APPENDED, never substituted. The node's own instructions come first
		// and unchanged; a model that reads only the opening lines still reads
		// the task it was given.
		Instructions: node.Instructions + OutputShapeInstruction(cp.plan.SchemaVersion, node.Kind),
		TaskInputs:   cp.taskInputs, BaseSHA: cp.baseSHA, Deadline: deadline,
	}
```

- [ ] **Step 5: Update the fake, without breaking what asserts against it**

`backend/internal/runs/fake/executor.go` — `scripted` emits schema-2-conformant payloads **that keep their legacy keys**:

```go
	case runs.KindReview:
		return runs.NewArtifact{Type: runs.ArtifactKindReviewVerdict, Payload: map[string]any{
			// `notes` is kept alongside the schema-2 keys, and that is not
			// laziness. Unknown keys pass by contract (spec §2.3), and every
			// pre-12a test that asserts on `notes` stays meaningful — a fake
			// updated by DELETING its old shape would fail a dozen suites for a
			// reason unrelated to what they test.
			"verdict":  "approve",
			"notes":    "fake review",
			"findings": []any{},
			"tests":    map[string]any{"status": "passed", "passed": 1, "failed": 0},
		}}, nil
	default: // plan and anything agentic-shaped
		return runs.NewArtifact{Type: runs.ArtifactKindPlan, Payload: map[string]any{
			"content": "plan: " + firstLine,
			"summary": "plan: " + firstLine,
			"steps":   []any{map[string]any{"title": firstLine}},
		}}, nil
	}
```

**`firstLine` is now the first line of instructions that carry an appended block.** Confirm it is still the node's own first line: `strings.Cut(req.Instructions, "\n")` takes everything before the first newline, and the block is appended after, so it is — but a node whose instructions are a single line with no trailing newline now has the block's first characters glued on. Check `planShapeBlock` starts with `\n\n` (it does above) and add a test in `fake`'s own suite asserting `scripted`'s plan content is unchanged when a block is appended.

Add the three test seams the enforcement test uses — `PlanPayload(map[string]any)`, `PlanExtra(map[string]any)`, `LastInstructions(nodeID string) string` — each guarded by the same mutex as the rest of the struct. **`LastInstructions` records what `Execute` received**; do not reconstruct it.

- [ ] **Step 6: Leave the harness entrypoints alone, and say why**

Add one comment to each of `docker/harness/*/entrypoint.sh` above the line that hands instructions to the CLI:

```sh
# The output-shape contract is NOT appended here. It rides on the instructions
# this script receives (runs.OutputShapeInstruction, appended in
# Orchestrator.buildRequest), so every harness — and the fake executor the
# acceptance lane runs on — gets the identical text from one source.
```

No behaviour changes and no image rebuild is needed. Confirm that with `git diff --stat docker/` showing comment-only changes.

- [ ] **Step 7: Move the E2E workflow to schema 2, with a review node**

`backend/internal/app/webe2e_test.go`:

```go
// e2eWorkflow is the smallest definition that reaches a waiting gate AND
// produces the evidence the approval surface renders: an implement node the
// fake answers with a patch, a review node that answers with a schema-2
// verdict, and a gate examining the patch with the verdict beside it.
//
// SCHEMA 2, deliberately. This lane is 12a's acceptance path, so it must run
// with payload validation ON — a lane pinned at schema 1 would prove the
// screens work against payloads nothing checks.
const e2eWorkflow = `{
  "schema": 2,
  "nodes": [
    {"id": "impl", "type": "implement", "harness": "e2e", "instructions": "Write the change."},
    {"id": "review", "type": "review", "harness": "e2e", "instructions": "Review the change."},
    {"id": "gate", "type": "gate", "on_request_changes": {"target": "impl", "max_rounds": 2}}
  ],
  "edges": [
    {"from": "impl", "to": "review", "artifact": "patch"},
    {"from": "impl", "to": "gate", "artifact": "patch"},
    {"from": "review", "to": "gate", "artifact": "review_verdict"}
  ]
}`
```

**Validate this against `internal/workflows/validate.go` before running the lane**, not after: the gate must examine exactly one plan-or-patch and at most one verdict, and `on_request_changes.target` must dominate the gate. If `validateConsumption` refuses this graph, fix the graph here — do not weaken the validator.

Run `task web:e2e` once now (it needs `playwright install --with-deps chromium` and `vite build` first) and confirm the existing approvals spec still passes against the new workflow. It asserts the badge moves; a graph with one more node changes the timing, not the outcome.

- [ ] **Step 8: Run everything and confirm**

```bash
cd backend
go test ./internal/runs/ ./internal/runs/fake/
go test -tags integration ./internal/runs/ -run TestPayloadEnforcement -v
go test -tags integration ./internal/runs/...
```
Expected: PASS. The last command is the one that catches a fake change breaking an unrelated suite.

- [ ] **Step 9: Mutation proofs**

```bash
cd backend
# 1. validateResultPayloads classified transient rather than permanent.
#    Expect: "the node was attempted N times for a permanent outcome".
# 2. The schema-version guard inverted (validate below the version, skip at it).
#    Expect: the schema-1 subtest fails. This is the compatibility rule's own
#    proof and it is the one that would otherwise ship untested.
# 3. OutputShapeInstruction returns its block at every schema version.
#    Expect: "a schema-1 node's instructions were modified".
```

- [ ] **Step 10: Gate and commit**

```bash
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git add backend docker
git commit -m "feat(runs): enforce artifact payload shape at schema 2, and tell the producers first"
```

---

### Task 7: `phase.` on the attention stream

Live phase dots on Home and Tasks, and a live timeline on run detail, need both halves built: a transport that carries phase transitions, and a bridge that routes a frame to the run it belongs to. This is the transport half — one line, three consequences.

**Files:**
- Modify: `backend/internal/realtime/realtime.go:22`
- Modify: `backend/internal/realtime/realtime_test.go`
- Modify: `backend/internal/app/serve_realtime_integration_test.go` (or `realtime_e2e_integration_test.go`) — one end-to-end assertion

**Interfaces:**
- Consumes: nothing.
- Produces: `phase.*` frames on `GET /api/v1/stream`. Task 9's bridge is the consumer.

- [ ] **Step 1: Write the failing tests**

In `internal/realtime/realtime_test.go`:

```go
func TestAttentionFilterCarriesPhaseTransitions(t *testing.T) {
	f := AttentionFilter(Scope{All: true})
	for _, kind := range []string{
		"phase.materialized", "phase.started", "phase.succeeded",
		"phase.retried", "phase.dead", "phase.reconciling",
	} {
		if !f.Matches(Event{Kind: kind}) {
			t.Errorf("the attention stream drops %s; no phase transition would reach a client", kind)
		}
	}
}

func TestAttentionKindPatternsCoverEveryPrefix(t *testing.T) {
	// The SQL patterns and the Go filter must select the same set. Filtering in
	// Go after a LIMIT truncates a page that happened to be all non-attention
	// rows, and a second hand-written prefix list in SQL rots the moment a
	// namespace is added — so this asserts the translation, not the list.
	got := AttentionKindPatterns()
	if len(got) != len(attentionPrefixes) {
		t.Fatalf("patterns = %v, prefixes = %v", got, attentionPrefixes)
	}
	for i, p := range attentionPrefixes {
		if got[i] != p+"%" {
			t.Errorf("pattern %d = %q, want %q", i, got[i], p+"%")
		}
	}
}
```

And, in the app-level realtime integration suite, one assertion that a phase transition produced by the **real orchestrator** arrives on a **live global stream** — not a hand-appended event. The existing suite has the machinery; extend its gate-arrival case to also observe an earlier `phase.` frame on the same connection.

- [ ] **Step 2: Run them and watch them fail**

Run: `cd backend && go test ./internal/realtime/ -run TestAttentionFilter -v`
Expected: FAIL — every `phase.` kind is dropped.

- [ ] **Step 3: The one line**

```go
// attentionPrefixes are the event-kind namespaces the global stream carries.
// Plan 08 deliberately gave gates and publications their own namespaces (see
// runs/events.go) so this filter is a prefix test and never an enumeration
// that rots as kinds are added.
//
// `phase.` joined in Plan 12a because live phase dots and a live run timeline
// need transitions, and the alternative — a per-run stream on run detail —
// would open a second connection against the one-stream-per-tab invariant
// (_app.tsx:63) or replace the global one and silently kill the cross-run
// approvals badge on the very screen where a decision is about to be made.
//
// IT WIDENS WHAT EVERY CONNECTED CLIENT RECEIVES, and by a lot: a run in flight
// emits far more phase transitions than gate or publish events, and
// `phase.log_truncated` / `phase.egress_denied` are capped per attempt rather
// than rare. The client's debounce in cache-bridge.ts is what keeps that from
// becoming a refetch storm — one refetch per window, not one per frame — and
// that debounce is now load-bearing rather than an optimization.
var attentionPrefixes = []string{"gate.", "publish.", "run.", "phase."}
```

- [ ] **Step 4: Run them and confirm they pass**

```bash
cd backend
go test ./internal/realtime/
go test -tags integration ./internal/realtime/... ./internal/app/ -run Realtime
```
Expected: PASS. The integration run matters: `AttentionKindPatterns` feeds the snapshot and replay queries, so this line changes what a resuming client is re-sent, and the existing resume tests are what prove it did not change what a client *skips*.

- [ ] **Step 5: Mutation proof**

```bash
cd backend
perl -0pi -e 's/"run\.", "phase\."/"run."/' internal/realtime/realtime.go
go test ./internal/realtime/ -run TestAttentionFilter
git checkout -- internal/realtime/realtime.go
```

Expected: FAIL naming six dropped kinds. Then confirm green.

- [ ] **Step 6: Gate and commit**

```bash
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git add backend
git commit -m "feat(realtime): carry phase transitions on the attention stream"
```

- [ ] **Step 7: Close wave 0**

```bash
task contracts:gen && task contracts:check
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git status --porcelain   # must be empty
```

Then read `web/src/api/schema.d.ts` and confirm by eye that every type wave 1 consumes is present and non-optional where it should be: `ApiRunListPageResponse`, `ApiRunSummaryResponse`, `ApiPhaseSummaryResponse`, `ApiGateEvidenceResponse`, `ApiArtifactPayloadResponse`, `ApiPlanPayload`, `ApiReviewVerdictPayload`. **Wave 1 does not begin until this list checks out** — a screen built against a moving contract is the thing wave 0 exists to prevent.

---

# Wave 1 — the flow

---

### Task 8: The shell, corrected to five phone tabs

Locked shell A is **five** phone tabs — Home · Runs · Approvals (badged) · Tasks · **More** — plus a desktop sidebar. Plan 11 shipped **seven equal tabs**, and the cause is visible in `nav.tsx`'s own comment: it cites "the V1 page map (decisions §7)" — the page map is indeed seven *pages*, but the shell decision was five *tabs* plus More. Page map and tab bar were conflated, and **a code comment citing a decisions section is not the decisions section**.

12a corrects it because nothing later would. Left alone, the phone navigation stays off-design permanently.

**Files:**
- Modify: `web/src/shell/nav.tsx`
- Modify: `web/src/shell/app-shell.test.tsx`
- Create: `web/src/routes/_app/more.tsx`
- Regenerate: `web/src/routeTree.gen.ts`

**Interfaces:**
- Consumes: nothing.
- Produces: the `/more` route string (compiler-checked); `PHONE_TABS` and `SECONDARY` exported from `nav.tsx` so `more.tsx` renders the same list the sidebar does.

- [ ] **Step 1: Write the failing tests**

In `web/src/shell/app-shell.test.tsx`:

```tsx
describe('the phone tab bar', () => {
  it('has exactly the five locked tabs, in order', () => {
    render(<AppShell me={me}>x</AppShell>)
    const tabs = within(screen.getByTestId('phone-tabs')).getAllByRole('link')
    expect(tabs.map((t) => t.textContent?.replace(/\d+$/, ''))).toEqual([
      'Home',
      'Runs',
      'Approvals',
      'Tasks',
      'More',
    ])
  })

  it('does not put a configuration surface in the thumb zone', () => {
    // The failure this replaces was not "a wrong number of tabs" — it was
    // Workflows, Repos and Admin competing for the same five thumb positions
    // as the four screens a person uses every day.
    render(<AppShell me={me}>x</AppShell>)
    const bar = within(screen.getByTestId('phone-tabs'))
    for (const label of ['Workflows', 'Repos', 'Admin']) {
      expect(bar.queryByRole('link', { name: label })).toBeNull()
    }
  })

  it('keeps one navigation landmark', () => {
    // _app.test.tsx and both e2e specs find the shell with
    // getByRole('navigation'). A second <nav> for the desktop rail would make
    // every one of those queries ambiguous — and jsdom applies no media
    // queries, so both would be present regardless of viewport.
    render(<AppShell me={me}>x</AppShell>)
    expect(screen.getAllByRole('navigation')).toHaveLength(1)
  })

  it('renders the badge only when there is something to act on', () => {
    const { rerender } = render(
      <AppShell me={me} approvalCount={0}>
        x
      </AppShell>,
    )
    expect(screen.queryByTestId('approvals-badge')).toBeNull()
    rerender(
      <AppShell me={me} approvalCount={3}>
        x
      </AppShell>,
    )
    expect(screen.getByTestId('approvals-badge')).toHaveTextContent('3')
  })
})

describe('the desktop rail', () => {
  it('carries all seven pages, because a sidebar has room for them', () => {
    render(<AppShell me={me}>x</AppShell>)
    const rail = within(screen.getByTestId('desktop-rail'))
    for (const label of ['Home', 'Tasks', 'Runs', 'Approvals', 'Workflows', 'Repos', 'Admin']) {
      expect(rail.getByRole('link', { name: new RegExp(`^${label}`) })).toBeInTheDocument()
    }
  })
})
```

And in a new `web/src/routes/_app/more.test.tsx`, or appended to the shell test file if the route is trivial to render:

```tsx
it('/more reaches every page the tab bar dropped', async () => {
  renderApp('/more')
  for (const label of ['Workflows', 'Repos', 'Admin']) {
    expect(await screen.findByRole('link', { name: new RegExp(`^${label}`) })).toBeInTheDocument()
  }
})
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd web && ../scripts/with-node ./node_modules/.bin/vitest run src/shell`
Expected: FAIL — no `phone-tabs` testid, seven links in the bar, no `/more` route.

- [ ] **Step 3: Rewrite `nav.tsx`**

```tsx
import { Link } from '@tanstack/react-router'

/**
 * THE TAB BAR AND THE PAGE MAP ARE DIFFERENT LISTS, and conflating them is the
 * mistake Plan 11 shipped. decisions §7 has TWO bullets: a **page map** of
 * seven pages, and a **shell decision** of five phone tabs — Home · Runs ·
 * Approvals (badged) · Tasks · More — plus a desktop sidebar. This file's
 * previous comment cited the first as authority for the second.
 *
 * EVERY `to` BELOW IS CHECKED BY THE COMPILER, via the `Register` augmentation
 * in router.ts. If a typo in one of these strings compiles, the augmentation is
 * gone and this project's route typing is decorative — that is the subject of
 * `web:typecheck`'s route-inference mutant, and this is still the largest body
 * of literal route strings in the tree.
 */
export const PHONE_TABS = [
  { to: '/', label: 'Home' },
  { to: '/runs', label: 'Runs' },
  { to: '/approvals', label: 'Approvals' },
  { to: '/tasks', label: 'Tasks' },
  { to: '/more', label: 'More' },
] as const

/**
 * The pages More holds, and the ones the desktop rail adds to the five above.
 * Exported so `/more` and the rail render ONE list: two copies would drift the
 * first time a page is added, and the phone would be the one that lost it.
 */
export const SECONDARY = [
  { to: '/workflows', label: 'Workflows' },
  { to: '/repos', label: 'Repos' },
  { to: '/admin', label: 'Admin' },
] as const

const linkClass = 'text-text-muted flex items-center gap-1 px-3 py-2 text-sm'

function Badge(props: { count: number }) {
  /* The count renders ONLY when there is something to act on. A badge reading
     "0" is a call to action for nothing. */
  if (props.count <= 0) return null
  return (
    <span
      data-testid="approvals-badge"
      className="bg-accent text-accent-contrast rounded-sm px-1 font-mono text-xs"
    >
      {props.count}
    </span>
  )
}

export function Nav(props: { approvalCount: number }) {
  return (
    /* ONE landmark. The two presentations are CSS, not two <nav> elements:
       jsdom applies no media queries, so a second nav would be present in every
       component test and would make getByRole('navigation') ambiguous in
       _app.test.tsx and in both Playwright specs. */
    <nav aria-label="Sections">
      <div
        data-testid="phone-tabs"
        className="border-border bg-surface flex border-t md:hidden"
      >
        {PHONE_TABS.map((item) => (
          <Link
            key={item.to}
            to={item.to}
            className={`${linkClass} flex-1 justify-center`}
            activeProps={{ className: 'text-accent' }}
          >
            {item.label}
            {item.to === '/approvals' && <Badge count={props.approvalCount} />}
          </Link>
        ))}
      </div>
      <div data-testid="desktop-rail" className="hidden md:flex md:flex-col">
        {[...PHONE_TABS.slice(0, 4), ...SECONDARY].map((item) => (
          <Link key={item.to} to={item.to} className={linkClass} activeProps={{ className: 'text-accent' }}>
            {item.label}
            {item.to === '/approvals' && <Badge count={props.approvalCount} />}
          </Link>
        ))}
      </div>
    </nav>
  )
}
```

`PHONE_TABS.slice(0, 4)` and not a third literal: the rail is the four real tabs plus the secondary pages, and `More` is a phone affordance with nothing behind it on desktop.

The shell's layout changes with it — `app-shell.tsx` puts the rail beside `<main>` at `md:` and the bar below it on phone. Keep the DOM order (header, indicator, main, nav) so the tab bar stays last in the tab sequence on a phone; use `md:flex-row` on the wrapper and `md:order-first` on the nav for the desktop arrangement.

- [ ] **Step 4: Add `/more`**

`web/src/routes/_app/more.tsx`:

```tsx
import { createFileRoute, Link } from '@tanstack/react-router'
import { SECONDARY } from '../../shell/nav'

/**
 * The fifth tab. It is a real page and not a menu overlay: a bottom sheet here
 * would be one more thing to dismiss on the way to a page a person visits
 * rarely, and it could not be linked to.
 *
 * It renders `SECONDARY` rather than its own list, so a page added to the app
 * cannot reach the desktop rail and miss the phone.
 */
export const Route = createFileRoute('/_app/more')({ component: More })

function More() {
  return (
    <ul className="flex flex-col gap-2">
      {SECONDARY.map((item) => (
        <li key={item.to}>
          <Link
            to={item.to}
            className="border-border bg-surface text-text block rounded-md border p-3"
          >
            {item.label}
          </Link>
        </li>
      ))}
    </ul>
  )
}
```

- [ ] **Step 5: Regenerate the route tree and run the lanes**

```bash
cd web
../scripts/with-node ./node_modules/.bin/tsr generate
../scripts/with-node ./node_modules/.bin/vitest run
../scripts/with-node ./node_modules/.bin/tsc --noEmit
../scripts/with-node node tools/arch/main.ts
```
Expected: PASS. `web:arch` matters here — `nav.tsx` now has two importers (`app-shell.tsx` and `routes/_app/more.tsx`), and the consumer-count rule clamps to `shell/`, which is where it already lives.

- [ ] **Step 6: Mutation proof**

```bash
cd web
# Put Admin back in the tab bar.
# Expect: "does not put a configuration surface in the thumb zone" fails, and
# the ordered-tabs test fails naming six entries.
```

Also delete `data-testid="desktop-rail"`'s block and confirm the rail test fails — a rail that renders nothing is the plausible outcome of a Tailwind typo and no other test would see it.

- [ ] **Step 7: Gate and commit**

```bash
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git add web
git commit -m "fix(web): five phone tabs and a desktop rail, as decisions §7 actually says"
```

---

### Task 9: Run queries, and the bridge that routes a frame to its run

Task 7 put phase transitions on the wire. `cache-bridge.ts` invalidates exactly `['approvals']` and reads nothing off a frame but its kind — it **cannot** route by `run_id` because it never looks at one. The wire already carries one (`StreamFrame.run_id`), so this is a bridge change and not a transport change.

`cache-bridge.ts` records its own house rule and it governs here: a test-only `flush()` left the shipped scheduling arm unasserted, and deleting that arm kept all five plan tests green. **New routing is proved through the injected timer, the way production fires it** — and the proof asserts an **intermediate phase transition**, not gate arrival or run completion, which Plan 11 already covers and which would pass with phase routing entirely absent.

**Files:**
- Create: `web/src/runs/runs-query.ts`, `web/src/runs/run-query.ts`
- Modify: `web/src/realtime/cache-bridge.ts`, `web/src/realtime/cache-bridge.test.ts`
- Modify: `web/src/test/render-app.tsx` — a default `/api/v1/runs` handler

**Interfaces:**
- Consumes: `ApiRunListPageResponse`, `ApiRunResponse` (wave 0).
- Produces:
  - `runsQueryOptions(filters?: RunFilters)` on key `['runs', filters]`, returning `ApiRunSummaryResponse[]`.
  - `runQueryOptions(runId: string)` on key `['run', runId]`, returning `ApiRunResponse`.
  - `createCacheBridge(qc, opts)` unchanged in signature.
  - Tasks 13, 14 and 16 are the consumers.

- [ ] **Step 1: Write the failing bridge tests**

In `web/src/realtime/cache-bridge.test.ts`:

```ts
/** A frame as the wire really shapes it. run_id is the field the bridge could
 *  not previously see. */
const frame = (kind: string, runId: string): EventFrame => ({
  kind: 'event',
  id: 1,
  event: kind,
  frame: { id: 1, run_id: runId, kind, payload: {}, occurred_at: '' },
})

describe('routing a frame to its run', () => {
  it('an INTERMEDIATE phase transition invalidates that run and the list', () => {
    // phase.started, deliberately. Gate arrival and run completion are Plan
    // 11's cases and both are in ATTENTION_KINDS already — a test written on
    // either would pass with phase routing entirely absent, which is the whole
    // reason this file's house rule exists.
    const { qc, calls, tick } = harness()
    const bridge = createCacheBridge(qc, { setTimeoutImpl: tick.impl })
    bridge.onFrame(frame('phase.started', 'r1'))
    expect(calls).toEqual([]) // nothing leaves the module before the window closes
    tick.fire()
    expect(calls).toContainEqual(['run', 'r1'])
    expect(calls).toContainEqual(['runs'])
  })

  it('does not invalidate a DIFFERENT run', () => {
    const { qc, calls, tick } = harness()
    const bridge = createCacheBridge(qc, { setTimeoutImpl: tick.impl })
    bridge.onFrame(frame('phase.succeeded', 'r1'))
    tick.fire()
    expect(calls).not.toContainEqual(['run', 'r2'])
  })

  it('coalesces a burst across runs into ONE window and loses neither run', () => {
    // The debounce is load-bearing now rather than an optimization: a run in
    // flight emits far more phase transitions than gate events. What must not
    // happen is the window keeping only the first run it saw.
    const { qc, calls, tick } = harness()
    const bridge = createCacheBridge(qc, { setTimeoutImpl: tick.impl })
    bridge.onFrame(frame('phase.started', 'r1'))
    bridge.onFrame(frame('phase.started', 'r2'))
    bridge.onFrame(frame('phase.succeeded', 'r1'))
    expect(tick.armed).toBe(1)
    tick.fire()
    expect(calls).toContainEqual(['run', 'r1'])
    expect(calls).toContainEqual(['run', 'r2'])
  })

  it('arms a NEW window after one fires', () => {
    const { qc, calls, tick } = harness()
    const bridge = createCacheBridge(qc, { setTimeoutImpl: tick.impl })
    bridge.onFrame(frame('phase.started', 'r1'))
    tick.fire()
    bridge.onFrame(frame('phase.started', 'r2'))
    tick.fire()
    expect(calls).toContainEqual(['run', 'r2'])
  })

  it('a phase frame does NOT touch the approvals inbox', () => {
    // GET /approvals reads phase_runs.status and never run_events, but the
    // inbox only changes when a gate opens or closes. Invalidating it on every
    // phase tick would turn a chatty run into a refetch loop on the one query
    // rendered on every page.
    const { qc, calls, tick } = harness()
    const bridge = createCacheBridge(qc, { setTimeoutImpl: tick.impl })
    bridge.onFrame(frame('phase.started', 'r1'))
    tick.fire()
    expect(calls).not.toContainEqual(['approvals'])
  })

  it('a gate frame still moves the inbox — Plan 11 behaviour, unchanged', () => {
    const { qc, calls, tick } = harness()
    const bridge = createCacheBridge(qc, { setTimeoutImpl: tick.impl })
    bridge.onFrame(frame('gate.waiting', 'r1'))
    tick.fire()
    expect(calls).toContainEqual(['approvals'])
    expect(calls).toContainEqual(['run', 'r1'])
  })

  it('a frame with no run_id is ignored rather than crashing', () => {
    // Not defensive padding: the contract types run_id as a string, and a
    // frame that somehow arrives without one must not throw inside onEvent —
    // sse-connection.ts catches a consumer throw and would read it as a dead
    // socket.
    const { qc, tick } = harness()
    const bridge = createCacheBridge(qc, { setTimeoutImpl: tick.impl })
    const bad = frame('phase.started', 'r1')
    // @ts-expect-error deliberately violating the contract
    bad.frame.run_id = undefined
    expect(() => bridge.onFrame(bad)).not.toThrow()
    tick.fire()
  })
})
```

`harness()` returns a `QueryClient` stub recording `invalidateQueries` keys, plus a manual timer with `impl`, `armed` and `fire()`. Follow whatever the existing file already does for its timer; do not introduce a second style.

- [ ] **Step 2: Run them and watch them fail**

Run: `cd web && ../scripts/with-node ./node_modules/.bin/vitest run src/realtime/cache-bridge.test.ts`
Expected: FAIL — every `['run', …]` expectation, and the phase frames produce no invalidation at all.

- [ ] **Step 3: Extend the bridge**

```ts
/**
 * The kinds that move the approvals INBOX. Deliberately narrower than what the
 * stream now carries: `phase.` joined the attention prefixes in Plan 12a, and
 * the inbox does not change when a phase ticks. A kind added here that the
 * server emits in bulk turns a chatty run into a refetch loop on the one query
 * rendered on every page.
 */
export const ATTENTION_KINDS: ReadonlySet<string> = new Set([
  'gate.waiting',
  'gate.decided',
  'run.started',
  'run.succeeded',
  'run.failed',
  'run.dead',
])

export function createCacheBridge(
  qc: QueryClient,
  opts?: { debounceMs?: number; setTimeoutImpl?: typeof setTimeout },
) {
  const debounceMs = opts?.debounceMs ?? DEFAULT_DEBOUNCE_MS
  const later = opts?.setTimeoutImpl ?? setTimeout
  let scheduled = false
  let inboxDirty = false
  /** The runs named by frames in the open window. A SET, not the last id: a
   *  run in flight interleaves with every other run in flight, and keeping one
   *  would drop the others silently and only under concurrency. */
  const dirtyRuns = new Set<string>()

  function fire() {
    // Reset BEFORE invalidating, so the next frame arms a new window. Left set,
    // the bridge goes permanently deaf after its first burst while every
    // single-burst test still passes.
    scheduled = false
    const runs = [...dirtyRuns]
    const inbox = inboxDirty
    dirtyRuns.clear()
    inboxDirty = false

    if (inbox) void qc.invalidateQueries({ queryKey: ['approvals'] })
    // The LIST, prefix-matched across every filter variant: ['runs', {...}]
    // keys all start with 'runs', and a run that changed could enter or leave
    // any of them. This is also why the list query key puts its filters in a
    // second element rather than in the string.
    void qc.invalidateQueries({ queryKey: ['runs'] })
    for (const runId of runs) void qc.invalidateQueries({ queryKey: ['run', runId] })
  }

  return {
    onFrame(f: EventFrame): void {
      const runId = f.frame.run_id
      if (typeof runId !== 'string' || runId === '') return
      dirtyRuns.add(runId)
      if (ATTENTION_KINDS.has(f.frame.kind)) inboxDirty = true
      if (scheduled) return
      scheduled = true
      later(fire, debounceMs)
    },
  }
}
```

**Note what changed and what did not.** Every frame now arms a window, where before only an attention-kind frame did; the inbox is invalidated only when an attention kind was seen. The bridge still invalidates and never reconstructs, and it still has no `close()` for the reason already recorded there.

- [ ] **Step 4: The query modules**

`web/src/runs/runs-query.ts`:

```ts
import { queryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'
import type { components } from '../api/schema'

export type RunSummary = components['schemas']['ApiRunSummaryResponse']

/** The filters that are URL state on Runs and Tasks. */
export type RunFilters = { status?: string; taskId?: string }

/**
 * THE KEY IS ['runs', filters] AND THE SHAPE IS LOAD-BEARING. The cache bridge
 * invalidates the prefix `['runs']`, which matches every filter variant — so a
 * phase transition refreshes the list a user is actually looking at whatever
 * they have filtered to. Encoding filters into the first element instead
 * (`['runs:waiting']`) would make the prefix match nothing.
 *
 * ONE PAGE, deliberately. 12a's screens show the newest runs; infinite scroll
 * is not in the locked designs and `next_cursor` is carried so the next plan
 * has it without a contract change.
 */
export function runsQueryOptions(filters: RunFilters = {}) {
  return queryOptions({
    queryKey: ['runs', filters],
    queryFn: async () => {
      const res = await unwrap(
        await api.GET('/api/v1/runs', {
          params: { query: { status: filters.status, task_id: filters.taskId } },
        }),
      )
      // Required AND nullable, like every list in this contract: a Go nil slice
      // marshals to null, and reading .length off it throws on exactly the
      // empty case every new user starts in.
      return res.runs ?? []
    },
    ...AUTH_SCOPED_QUERY_OPTIONS,
  })
}
```

`web/src/runs/run-query.ts` is the same shape on `['run', runId]` against `GET /api/v1/runs/{id}`.

- [ ] **Step 5: Give the test harness a default handler**

`web/src/test/render-app.tsx` — every protected route now renders under a shell that may fetch runs, and `onUnhandledRequest: 'error'` turns a missing handler into a failure far from its cause:

```tsx
    http.get('/api/v1/runs', () => HttpResponse.json({ runs: [], next_cursor: null })),
```

placed **behind** the caller's handlers, like the three already there.

- [ ] **Step 6: Run them and confirm they pass**

Run: `cd web && ../scripts/with-node ./node_modules/.bin/vitest run`
Expected: PASS, including `_app.test.tsx`'s one-stream test — the bridge change must not alter connection behaviour.

- [ ] **Step 7: Mutation proofs**

```bash
cd web
# 1. dirtyRuns becomes a single `let lastRun` instead of a Set.
#    Expect: "coalesces a burst across runs" fails naming r1 or r2.
# 2. onFrame gates on ATTENTION_KINDS again before adding to dirtyRuns.
#    Expect: the intermediate-phase-transition test fails. THIS IS THE ONE THE
#    HOUSE RULE IS ABOUT — record its output verbatim.
# 3. inboxDirty is set unconditionally.
#    Expect: "a phase frame does NOT touch the approvals inbox" fails.
```

- [ ] **Step 8: Gate and commit**

```bash
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git add web
git commit -m "feat(web): route stream frames to the run they belong to"
```

---

### Task 10: Patch decode and diff parsing

`patch` payloads carry `diff` as **base64 by contract**, and `collect.go` documents why: a patch is arbitrary bytes, a file in a legacy encoding diffs as non-UTF-8, and JSON string encoding would replace those bytes with U+FFFD *silently* — the mangled patch still applies. The client decodes once for display and never round-trips it.

This is pure, it runs in the `node` vitest project, and it is the only new module in 12a with no React in it.

**Files:**
- Create: `web/src/patch/decode-patch.ts`, `web/src/patch/decode-patch.test.ts`

**Interfaces:**
- Consumes: nothing.
- Produces: `decodePatch(diffBase64: string): ParsedPatch`, `ParsedPatch{files: DiffFile[]; added: number; removed: number; lossy: boolean}`, `DiffFile{path, oldPath, added, removed, hunks, binary}`, `DiffHunk{header, lines}`, `DiffLine{kind: 'context'|'add'|'del', text}`. Task 12 renders them.

- [ ] **Step 1: Write the failing tests**

`web/src/patch/decode-patch.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { decodePatch } from './decode-patch'

const b64 = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes))
const b64text = (s: string) => b64(new TextEncoder().encode(s))

const TWO_FILES = `diff --git a/src/auth.ts b/src/auth.ts
index 111..222 100644
--- a/src/auth.ts
+++ b/src/auth.ts
@@ -1,4 +1,5 @@
 import { db } from './db'
-export function login() {}
+export function login(): void {}
+export function logout(): void {}
 // tail
diff --git a/README.md b/README.md
--- a/README.md
+++ b/README.md
@@ -1 +1 @@
-old
+new
`

describe('decodePatch', () => {
  it('splits files and counts lines the way the hard-facts row reads them', () => {
    const got = decodePatch(b64text(TWO_FILES))
    expect(got.files.map((f) => f.path)).toEqual(['src/auth.ts', 'README.md'])
    expect(got.files[0]).toMatchObject({ added: 2, removed: 1 })
    expect(got.files[1]).toMatchObject({ added: 1, removed: 1 })
    // The row shows totals; a per-file sum computed at render time is a second
    // implementation of the same arithmetic.
    expect(got).toMatchObject({ added: 3, removed: 2 })
  })

  it('does not count the ---/+++ headers as removed and added lines', () => {
    // The obvious bug in every hand-rolled diff parser, and it is invisible on
    // a one-file patch: two files means the wrong parser reports +5/-4.
    const got = decodePatch(b64text(TWO_FILES))
    expect(got.added).toBe(3)
    expect(got.removed).toBe(2)
  })

  it('keeps hunk lines classified for rendering', () => {
    const [file] = decodePatch(b64text(TWO_FILES)).files
    expect(file.hunks).toHaveLength(1)
    expect(file.hunks[0].header).toBe('@@ -1,4 +1,5 @@')
    expect(file.hunks[0].lines.map((l) => l.kind)).toEqual([
      'context',
      'del',
      'add',
      'add',
      'context',
    ])
    // The leading marker is stripped: a renderer that colours by `kind` must
    // not also print the character it coloured for.
    expect(file.hunks[0].lines[1].text).toBe("export function login() {}")
  })

  it('survives a NON-UTF-8 payload and says that it was lossy', () => {
    // THE CASE THE BASE64 CONTRACT EXISTS FOR (collect.go). A latin-1 é (0xE9)
    // is not valid UTF-8; the bytes reach the client intact precisely because
    // they were never put in a JSON string, and the client must still render
    // something rather than throwing — while saying the text it shows is not
    // exactly the bytes.
    const bytes = new Uint8Array([
      ...new TextEncoder().encode('--- a/legacy.txt\n+++ b/legacy.txt\n@@ -1 +1 @@\n-caf'),
      0xe9,
      ...new TextEncoder().encode('\n+cafe\n'),
    ])
    const got = decodePatch(b64(bytes))
    expect(got.lossy).toBe(true)
    expect(got.files.map((f) => f.path)).toEqual(['legacy.txt'])
    expect(got).toMatchObject({ added: 1, removed: 1 })
  })

  it('a clean UTF-8 patch is not marked lossy', () => {
    // Without this, `lossy: true` hardcoded would pass the test above and the
    // warning would be on every diff in the product.
    expect(decodePatch(b64text(TWO_FILES)).lossy).toBe(false)
  })

  it('marks a binary file rather than pretending it has hunks', () => {
    const p = `diff --git a/logo.png b/logo.png
Binary files a/logo.png and b/logo.png differ
`
    const [file] = decodePatch(b64text(p)).files
    expect(file).toMatchObject({ path: 'logo.png', binary: true, added: 0, removed: 0 })
  })

  it('an empty or unparseable payload yields an empty patch, never a throw', () => {
    // The surface renders this as "no changes to show". A throw here would
    // take out the whole approval screen for a payload the server accepted.
    expect(decodePatch('')).toMatchObject({ files: [], added: 0, removed: 0 })
    expect(() => decodePatch('!!!not base64!!!')).not.toThrow()
  })
})
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd web && ../scripts/with-node ./node_modules/.bin/vitest run src/patch`
Expected: FAIL — `Cannot find module './decode-patch'`.

Note the file lands in the `node` project only if `vitest.config.ts`'s `node` project includes it; today that project's `include` is `tools/**/*.test.ts`. **Check which project picks this file up.** It has no DOM dependency, so either is correct — but it must run in exactly one, and if it lands in `dom` by default, say so in a comment rather than widening the `node` project's globs for one file.

- [ ] **Step 3: Implement it**

`web/src/patch/decode-patch.ts`:

```ts
export type DiffLine = { kind: 'context' | 'add' | 'del'; text: string }
export type DiffHunk = { header: string; lines: DiffLine[] }
export type DiffFile = {
  path: string
  oldPath: string
  added: number
  removed: number
  hunks: DiffHunk[]
  binary: boolean
}
export type ParsedPatch = {
  files: DiffFile[]
  added: number
  removed: number
  /** True when the bytes were not valid UTF-8 and the rendered text therefore
   *  contains replacement characters. The surface says so; it does not quietly
   *  show a corrupted diff as if it were the file. */
  lossy: boolean
}

const EMPTY: ParsedPatch = { files: [], added: 0, removed: 0, lossy: false }

/**
 * base64 → bytes → text → files.
 *
 * THE BASE64 IS NOT AN ENCODING CHOICE, IT IS THE CONTRACT (collect.go): a
 * patch is arbitrary bytes, a file in a legacy encoding diffs as non-UTF-8, and
 * JSON string encoding would replace those bytes with U+FFFD silently — the
 * mangled patch still applies. Decoded ONCE here, for display, and never
 * re-encoded: nothing in the client round-trips a patch.
 *
 * The parser is unified-diff-shaped and deliberately small. It exists to answer
 * three questions the approval surface asks — which files, how many ± lines,
 * and what do the hunks say — and it never has to APPLY anything, which is what
 * a real diff library would be for.
 */
export function decodePatch(diffBase64: string): ParsedPatch {
  if (diffBase64 === '') return EMPTY
  let bytes: Uint8Array
  try {
    const bin = atob(diffBase64)
    bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0))
  } catch {
    // A payload the server accepted whose diff is not base64 is a producer bug,
    // and the surface's job is to say there is nothing to show — not to take
    // the screen down with it.
    return EMPTY
  }
  // Two decodes: one to LEARN whether the bytes are valid UTF-8, one to get
  // text regardless. `fatal: true` throws on the first invalid sequence, which
  // is the only way to distinguish "the patch contains a literal U+FFFD" from
  // "we substituted one".
  let lossy = false
  try {
    new TextDecoder('utf-8', { fatal: true }).decode(bytes)
  } catch {
    lossy = true
  }
  const text = new TextDecoder('utf-8').decode(bytes)
  return { ...parseUnifiedDiff(text), lossy }
}
```

`parseUnifiedDiff` is a line loop with four states, and the two traps are worth naming in comments:

- **`---` and `+++` are headers, not lines**, so they are recognised before the `+`/`-` classification and never counted. The one-file case hides the bug; the fixture has two files for exactly that reason.
- **A hunk begins at `@@` and ends at the next `@@`, `diff --git`, or `---`**, so a file whose last hunk runs to the end of the buffer still closes.
- `diff --git a/x b/x` supplies the path when present; `+++ b/x` supplies it otherwise (the non-UTF-8 fixture has no `diff --git` line for exactly this reason). `+++ /dev/null` means a deletion — keep `oldPath` as the path in that case.
- A `Binary files … differ` line sets `binary` and closes the file with zero counts.

- [ ] **Step 4: Run them and confirm they pass**

Run: `cd web && ../scripts/with-node ./node_modules/.bin/vitest run src/patch`
Expected: PASS, all seven.

- [ ] **Step 5: Mutation proofs**

```bash
cd web
# 1. Count '---' and '+++' as del/add.
#    Expect: "does not count the ---/+++ headers" fails with +5/-4.
# 2. Hardcode lossy: false.
#    Expect: the non-UTF-8 test fails.
# 3. Hardcode lossy: true.
#    Expect: "a clean UTF-8 patch is not marked lossy" fails. Both directions,
#    or the flag is only proved to be settable.
```

- [ ] **Step 6: Gate and commit**

```bash
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git add web
git commit -m "feat(web): decode a patch payload and parse its files"
```

---

### Task 11: The decision controls — built once, all-or-nothing

Three surfaces offer a decision: Home's gate card, the run-detail gate phase, and Approvals. §5's rule is what a card is likeliest to get wrong, because **suppressing only approve looks obviously right and 403s on the other two** — `gates.go:188` runs the requester check *before* the branch on decision type at `:204`. And self-approval is not the only refusal: `decideGate` requires `GrantApprove` while `GET /approvals` lists gates for any grant, deliberately, with an integration test pinning it.

So this is one component with one test file, and the three hosts import it.

**Files:**
- Create: `web/src/gates/decide-gate.ts`, `web/src/gates/gate-controls.tsx`, `web/src/gates/gate-controls.test.tsx`, `web/src/gates/decision-rules.testkit.ts`

**Interfaces:**
- Consumes: `ApiGateEvidenceResponse` (Task 2), `useAppForm` / `problem-to-fields` (Plan 11).
- Produces:
  - `useDecideGate(runId: string)` → a mutation taking `{nodeId, decision: 'approve'|'request_changes'|'reject', note?: string}`.
  - `<GateControls runId nodeId evidence onDecided? />`.
  - `expectDecisionRules(render: (evidence: GateEvidence) => void): Promise<void>` from `decision-rules.testkit.ts`.
  - Tasks 12, 13 and 16 are the hosts.

- [ ] **Step 1: Write the failing tests**

`web/src/gates/gate-controls.test.tsx`. The three suppression assertions are re-run **verbatim from each host** in Tasks 12, 13 and 16, so they live in `web/src/gates/decision-rules.testkit.ts` and every host imports them from there.

**`.testkit.ts`, not `.test.tsx`, and the extension is load-bearing.** Importing a `*.test.tsx` file from another test file registers its `describe` blocks a second time, so every shared assertion would run once per host under the wrong file's name and a failure would name the wrong subject. A `.testkit.ts` module is not collected by vitest and is not counted as a consumer by `tools/arch/import-graph.ts` (its `NOT_A_CONSUMER` pattern covers `.test`/`.spec`, and nothing in production imports this), so it has zero consumers by the arch checker's reckoning and sits wherever it is put — `gates/`, beside its subject.

The suite below is `gate-controls.test.tsx`; `expectDecisionRules` is the extracted trio marked ★:

```tsx
const decidable = {
  examined_artifact_id: 'a1',
  examined_artifact_type: 'patch',
  summary_artifact_id: null,
  can_decide: true,
  cannot_decide_reason: '',
  wall_deadline: '2099-01-01T00:00:00Z',
}

describe('gate controls', () => {
  it('offers all three when the caller may decide', async () => {
    renderControls({ evidence: decidable })
    for (const name of [/^approve$/i, /request changes/i, /^reject$/i]) {
      expect(await screen.findByRole('button', { name })).toBeEnabled()
    }
  })

  // ★ extracted into decision-rules.testkit.ts, with the two below it
  it('suppresses ALL THREE when the caller is short of approve', async () => {
    renderControls({
      evidence: { ...decidable, can_decide: false, cannot_decide_reason: 'not_approver' },
    })
    // Named individually rather than counted: `queryAllByRole('button')` would
    // pass against a surface that rendered three DISABLED buttons, which is
    // the design this rule rejects — a disabled control still says "this is
    // yours to do, later".
    expect(screen.queryByRole('button', { name: /^approve$/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /request changes/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /^reject$/i })).toBeNull()
    expect(await screen.findByText(/approve grant/i)).toBeInTheDocument()
  })

  it('suppresses ALL THREE for a requester whose run forbids self-approval', async () => {
    // THE PLAUSIBLE-LOOKING MISTAKE. gates.go:188 refuses before it branches on
    // decision type, so hiding only approve ships two controls guaranteed to
    // 403 — and on a single-operator system the operator IS always the
    // requester, which makes this the normal path rather than an edge case.
    renderControls({
      evidence: { ...decidable, can_decide: false, cannot_decide_reason: 'self_approval_forbidden' },
    })
    expect(screen.queryByRole('button', { name: /^approve$/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /request changes/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /^reject$/i })).toBeNull()
    expect(await screen.findByText(/your own gate/i)).toBeInTheDocument()
  })

  it('states WHICH refusal it is', async () => {
    // Two refusals with one message is a message that tells nobody what to do:
    // one is fixed by a grant, the other by a policy on a run already created.
    renderControls({ evidence: { ...decidable, can_decide: false, cannot_decide_reason: 'not_approver' } })
    expect(screen.queryByText(/your own gate/i)).toBeNull()
  })

  it('approve posts the decision and nothing else', async () => {
    const posted: unknown[] = []
    renderControls({ evidence: decidable, onPost: (b) => posted.push(b) })
    await userEvent.click(await screen.findByRole('button', { name: /^approve$/i }))
    await waitFor(() => expect(posted).toEqual([{ decision: 'approve' }]))
  })

  it('request changes REQUIRES a note before it will post', async () => {
    // Not a UI nicety: gates.go feeds the note to the revision target as a
    // context file, and a revision with no note re-runs the same work with the
    // same information and reaches the same patch. The note IS the new input.
    const posted: unknown[] = []
    renderControls({ evidence: decidable, onPost: (b) => posted.push(b) })
    await userEvent.click(await screen.findByRole('button', { name: /request changes/i }))
    const send = await screen.findByRole('button', { name: /send/i })
    await userEvent.click(send)
    expect(posted).toEqual([])
    expect(await screen.findByRole('alert')).toHaveTextContent(/note/i)

    await userEvent.type(screen.getByLabelText(/note/i), 'use the existing helper')
    await userEvent.click(send)
    await waitFor(() =>
      expect(posted).toEqual([{ decision: 'request_changes', note: 'use the existing helper' }]),
    )
  })

  it('reject asks for confirmation before it ends a run', async () => {
    // Reject is terminal — DecisionReject finishes the run failed. It is the
    // one control in the thumb zone whose mis-tap cannot be undone.
    const posted: unknown[] = []
    renderControls({ evidence: decidable, onPost: (b) => posted.push(b) })
    await userEvent.click(await screen.findByRole('button', { name: /^reject$/i }))
    expect(posted).toEqual([])
    await userEvent.click(await screen.findByRole('button', { name: /reject this run/i }))
    await waitFor(() => expect(posted).toHaveLength(1))
  })

  it('a 409 shows the decision that WON, not just a failure', async () => {
    // ApiGateDecisionSummary carries acted_by_email, decision, note and
    // acted_at, so the screen can say what happened. "Something went wrong" on
    // a gate somebody else just approved is the least useful true statement
    // available.
    renderControls({ evidence: decidable, reply: conflict(), refetched: decidedByAlice() })
    await userEvent.click(await screen.findByRole('button', { name: /^approve$/i }))
    expect(await screen.findByText(/alice@example.com/)).toBeInTheDocument()
    expect(await screen.findByText(/already/i)).toBeInTheDocument()
  })

  it('says when the wall deadline has already passed, before anything is posted', async () => {
    renderControls({ evidence: { ...decidable, wall_deadline: '2020-01-01T00:00:00Z' } })
    expect(screen.queryByRole('button', { name: /^approve$/i })).toBeNull()
    expect(await screen.findByText(/deadline/i)).toBeInTheDocument()
  })

  it('renders the correlation id when the server 500s', async () => {
    renderControls({ evidence: decidable, reply: serverError('cid-77') })
    await userEvent.click(await screen.findByRole('button', { name: /^approve$/i }))
    expect(await screen.findByText('cid-77')).toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd web && ../scripts/with-node ./node_modules/.bin/vitest run src/gates`
Expected: FAIL — module not found.

- [ ] **Step 3: The mutation**

`web/src/gates/decide-gate.ts`:

```ts
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'

export type Decision = 'approve' | 'request_changes' | 'reject'

/**
 * One gate decision.
 *
 * WHAT IT INVALIDATES IS THE WHOLE OF ITS CACHE CONTRACT, and it is deliberately
 * the same set the stream's bridge would invalidate: the inbox (this gate left
 * it), the run list (the run's status changed) and this run. Writing the new
 * state into the cache instead would be the client reconstructing an
 * orchestrator transition it did not perform — approve resumes the run,
 * request_changes opens a generation and rewinds a slice, and neither is
 * derivable from the response.
 *
 * The frames arriving from the stream a moment later are then idempotent with
 * this, which is why both paths invalidate rather than one of them patching.
 */
export function useDecideGate(runId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (v: { nodeId: string; decision: Decision; note?: string }) =>
      unwrap(
        await api.POST('/api/v1/runs/{id}/gates/{node}/decision', {
          params: { path: { id: runId, node: v.nodeId } },
          body: { decision: v.decision, note: v.note },
        }),
      ),
    onSettled: () => {
      // ON SETTLED, not onSuccess. A 409 means somebody else's decision won,
      // and the screen showing the stale "waiting" state is exactly the case
      // that needs the refetch most.
      void qc.invalidateQueries({ queryKey: ['approvals'] })
      void qc.invalidateQueries({ queryKey: ['runs'] })
      void qc.invalidateQueries({ queryKey: ['run', runId] })
    },
  })
}
```

- [ ] **Step 4: The component**

`web/src/gates/gate-controls.tsx`. The shape, with the rules that must not be softened:

```tsx
/**
 * Approve · request changes · reject, and the rule that they are ALL THREE or
 * NONE.
 *
 * `gates.go:188` returns ClassForbidden on the requester check BEFORE it
 * branches on decision type at `:204`, so request_changes and reject are
 * refused identically to approve. Suppressing only approve is the intuitive
 * design and it ships two controls guaranteed to 403.
 *
 * SUPPRESSED, NOT DISABLED. A greyed-out button says "this is yours to do,
 * later"; these are refusals with a reason, and the reason is what a person can
 * act on.
 *
 * The evidence still renders around this component wherever it is hosted —
 * reading is a view grant and is unaffected.
 */
export function GateControls(props: {
  runId: string
  nodeId: string
  evidence: GateEvidence
  onDecided?: () => void
}) {
```

Behaviour, in order:

1. **Deadline first.** `new Date(evidence.wall_deadline) <= new Date()` → render the deadline notice and no controls. It is checked before `can_decide` because a passed deadline refuses every caller, including one who could otherwise decide.
2. **`can_decide === false`** → render `REASONS[cannot_decide_reason]` and no controls:
   ```ts
   const REASONS: Record<string, string> = {
     not_approver:
       'You can see this gate but not decide it — deciding needs the approve grant on this project.',
     self_approval_forbidden:
       'You requested this run, and its policy does not let a requester decide their own gate.',
   }
   ```
   An unrecognised reason falls back to a plain "You cannot decide this gate." — a new reason added server-side must degrade to something true, not to nothing.
3. **Otherwise** the three buttons, sticky in the thumb zone at the host's discretion (the host supplies the positioning class; the component supplies the controls).
4. **Request changes** opens a note field built on `useAppForm` with a Zod schema of `z.object({ note: z.string().min(1, 'A note is required — it is the revision's only new input').max(8192, 'At most 8192 bytes') })`. 8192 is `maxGateNoteLen` (`gates.go:40`); state that in a comment so the number is traceable rather than magic.
5. **Reject** requires a second, explicitly-labelled confirmation.
6. **Errors**: 409 → the refetched run's gate phase `decision` block, rendered as "*decision* by *acted_by_email* — *note*"; anything else → `ApiErrorPanel`, which carries the correlation id.

- [ ] **Step 5: Run them and confirm they pass**

Run: `cd web && ../scripts/with-node ./node_modules/.bin/vitest run src/gates`
Expected: PASS, all ten.

- [ ] **Step 6: Mutation proofs**

```bash
cd web
# 1. Suppress only approve (render the other two when can_decide is false).
#    Expect: BOTH suppression tests fail, each naming two live buttons.
# 2. Render the three DISABLED instead of absent.
#    Expect: the same two tests fail. If they pass, they were counting buttons
#    rather than naming them — fix the tests, not the component.
# 3. Drop the note's min(1).
#    Expect: "request changes REQUIRES a note" fails on the first click.
# 4. One shared refusal message for both reasons.
#    Expect: "states WHICH refusal it is" fails.
```

- [ ] **Step 7: Gate and commit**

```bash
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git add web
git commit -m "feat(web): the gate decision controls, all three or none"
```

---

### Task 12: The approval surface, and its full-screen host

Locked approval A is **summary-first**: agent summary, then a hard-facts row, then a file-by-file accordion expanding into real diffs, then sticky actions in the thumb zone — approve · request changes · reject, all three.

§3.4 and §3.5 are **one component with two hosts**. Locked §7 decides twice that a gate is decidable where its evidence is shown: once in run detail A (inline at the gate) and once in approval A (the summary-first screen). Building it once is what keeps them from drifting apart. This task builds it and hosts it full-screen under Approvals; Task 16 hosts it inside the run-detail gate phase.

**The hard-facts row ships as files · ±lines · tests · duration. Cost is absent because it does not exist** — there is no cost, usage, token or price field anywhere in the API *or* the schema; only `execution_used_ms`, which is time, not money (§10.1). The per-file "explain" spark is row 13 (§10.6) and the surface ships without it.

**Files:**
- Create: `web/src/approvals/artifact-query.ts`, `web/src/approvals/approval-surface.tsx`, `web/src/approvals/approval-surface.test.tsx`
- Create: `web/src/routes/_app/approvals.index.tsx`, `web/src/routes/_app/approvals.$runId.$nodeId.tsx`
- Delete: `web/src/routes/_app/approvals.tsx` (its content moves to `approvals.index.tsx`)
- Modify: `web/src/approvals/approval-list.tsx` — cards link to the surface
- Modify: `web/src/routes/_app/approvals.test.tsx`

**Interfaces:**
- Consumes: `decodePatch` (Task 10), `GateControls` + `expectDecisionRules` (Task 11), `ApiArtifactPayloadResponse` (Task 3), `ApiGateEvidenceResponse` (Task 2), `ApiPlanPayload` / `ApiReviewVerdictPayload` (Task 5).
- Produces: `<ApprovalSurface runId nodeId evidence executionUsedMs />`; `artifactQueryOptions(runId, artifactId)` on key `['artifact', runId, artifactId]`; the `/approvals/$runId/$nodeId` route string. Task 16 hosts the component.

- [ ] **Step 1: Write the failing tests**

`web/src/approvals/approval-surface.test.tsx`:

```tsx
describe('the approval surface', () => {
  it('leads with the agent summary', async () => {
    renderSurface({ verdict: { verdict: 'approve', findings: [], tests: null }, patch: TWO_FILES_B64 })
    const summary = await screen.findByTestId('agent-summary')
    // Summary-FIRST is the locked decision, so its position is asserted, not
    // merely its presence: a surface that renders the diff first and the
    // summary below it is a different screen (mockup B) that was not chosen.
    expect(summary.compareDocumentPosition(screen.getByTestId('hard-facts'))).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    )
  })

  it('renders NO summary block at all when the gate has no verdict', async () => {
    // validate.go:207 permits a gate to consume ZERO review verdicts, so this
    // is a legal workflow and a production state. An empty block pretending to
    // be a summary is the failure: a stub never fakes success.
    renderSurface({ evidence: { ...decidable, summary_artifact_id: null }, patch: TWO_FILES_B64 })
    await screen.findByTestId('hard-facts')
    expect(screen.queryByTestId('agent-summary')).toBeNull()
    // And the hard facts it CAN still compute are there.
    expect(screen.getByTestId('hard-facts')).toHaveTextContent('2 files')
  })

  it('computes the hard facts from the decoded patch', async () => {
    renderSurface({ patch: TWO_FILES_B64, executionUsedMs: 62_000 })
    const row = await screen.findByTestId('hard-facts')
    expect(row).toHaveTextContent('2 files')
    expect(row).toHaveTextContent('+3')
    expect(row).toHaveTextContent('−2')
    expect(row).toHaveTextContent(/1m 2s/)
  })

  it('says tests are UNKNOWN rather than showing a tick', async () => {
    renderSurface({ verdict: { verdict: 'approve', findings: [], tests: null }, patch: TWO_FILES_B64 })
    const row = await screen.findByTestId('hard-facts')
    expect(row).toHaveTextContent(/tests .*not reported/i)
    expect(row).not.toHaveTextContent('✓')
  })

  it('shows a real test outcome when one is reported', async () => {
    renderSurface({
      verdict: { verdict: 'approve', findings: [], tests: { status: 'failed', passed: 9, failed: 2 } },
      patch: TWO_FILES_B64,
    })
    expect(await screen.findByTestId('hard-facts')).toHaveTextContent(/9 passed, 2 failed/)
  })

  it('never shows a cost', async () => {
    // §10.1: there is no cost, usage, token or price field in the API or the
    // schema. The locked mockup has one; restoring it needs usage accounting
    // that does not exist, and a fabricated number here would be worse than
    // its absence.
    renderSurface({ patch: TWO_FILES_B64 })
    await screen.findByTestId('hard-facts')
    expect(screen.queryByText(/\$/)).toBeNull()
  })

  it('is an accordion: files listed, diffs on demand', async () => {
    renderSurface({ patch: TWO_FILES_B64 })
    expect(await screen.findByRole('button', { name: /src\/auth\.ts/ })).toBeInTheDocument()
    expect(screen.queryByText(/export function logout/)).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: /src\/auth\.ts/ }))
    expect(await screen.findByText(/export function logout/)).toBeInTheDocument()
  })

  it('renders a PLAN gate as prose, not as a diff', async () => {
    // examined_artifact_type is on the wire precisely so this decision is made
    // before the payload is fetched. A plan rendered through the diff parser
    // is an empty file list and a person told there is nothing to review.
    renderSurface({
      evidence: { ...decidable, examined_artifact_type: 'plan' },
      plan: { summary: 'Adds revocation.', steps: [{ title: 'Add the table' }] },
    })
    expect(await screen.findByText('Add the table')).toBeInTheDocument()
    expect(screen.queryByTestId('file-accordion')).toBeNull()
  })

  it('says so when the examined artifact is missing rather than showing an empty diff', async () => {
    // A zero examined_artifact_id means the pinned definition binds an
    // artifact the run never produced — the state decideGate also refuses.
    renderSurface({ evidence: { ...decidable, examined_artifact_id: '' } })
    expect(await screen.findByText(/evidence.*not available/i)).toBeInTheDocument()
  })

  it('warns when the patch bytes were not valid UTF-8', async () => {
    renderSurface({ patch: LATIN1_B64 })
    expect(await screen.findByText(/not valid UTF-8|may not render exactly/i)).toBeInTheDocument()
  })

  it('carries the decision rules of every host', async () => {
    // The SAME assertions Task 11 exports, run against this host. §7 asks for
    // them on all three surfaces because §5's all-or-nothing rule is what a
    // card is likeliest to get wrong, and a host-local reimplementation is how
    // one host quietly gets a weaker version.
    await expectDecisionRules((evidence) => renderSurface({ evidence, patch: TWO_FILES_B64 }))
  })
})
```

Plus, in `web/src/routes/_app/approvals.test.tsx`:

```tsx
it('keeps the two empty states distinct', async () => {
  // Plan 11's rule, inherited by every list 12a adds. Collapsing them tells a
  // user with no access that they are all caught up.
  renderApp('/approvals', [http.get('/api/v1/approvals', () => HttpResponse.json({ approvals: [] }))])
  expect(await screen.findByText('Nothing is waiting on you.')).toBeInTheDocument()
})

it('a card opens the surface at a linkable URL', async () => {
  const router = renderApp('/approvals', [oneWaitingGate()])
  await userEvent.click(await screen.findByRole('link', { name: /gate on A/ }))
  expect(router.state.location.pathname).toBe('/approvals/run-1/gate')
  expect(await screen.findByTestId('hard-facts')).toBeInTheDocument()
})
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd web && ../scripts/with-node ./node_modules/.bin/vitest run src/approvals src/routes/_app/approvals.test.tsx`
Expected: FAIL — modules and routes missing.

- [ ] **Step 3: The artifact query**

`web/src/approvals/artifact-query.ts`:

```ts
/**
 * One artifact's payload, keyed by ['artifact', runId, artifactId].
 *
 * ARTIFACTS ARE IMMUTABLE, so this is the one query in 12a that does NOT take
 * AUTH_SCOPED_QUERY_OPTIONS' refresh interval: an artifact row is written once
 * and never updated, and re-polling a patch every sixty seconds would be
 * bandwidth spent to learn it is unchanged. A revision produces a NEW artifact
 * with a new id, which is a new cache entry.
 *
 * The AUTHORIZATION on it can still narrow, and that is covered where it
 * matters: the list and the run both carry the refresh floor, so a gate a user
 * may no longer see leaves the screen within the stated interval.
 */
export function artifactQueryOptions(runId: string, artifactId: string) {
  return queryOptions({
    queryKey: ['artifact', runId, artifactId],
    queryFn: async () =>
      unwrap(
        await api.GET('/api/v1/runs/{id}/artifacts/{artifact_id}', {
          params: { path: { id: runId, artifact_id: artifactId } },
        }),
      ),
    staleTime: Number.POSITIVE_INFINITY,
    enabled: artifactId !== '',
  })
}
```

- [ ] **Step 4: The surface**

`web/src/approvals/approval-surface.tsx`, in the locked order and nothing else:

1. **Agent summary** — from the `review_verdict` payload at `evidence.summary_artifact_id`. **Rendered only when that id is non-null and the fetch produced a payload.** No id → no block. The verdict's `verdict` value and its `findings` render with it; a `request_changes` verdict from the agent is information the human deciding wants first.
2. **Hard facts** — `files · ±lines · tests · duration`. Files and ± come from `decodePatch`; `tests` from the verdict's `tests` (null → "not reported"); duration from `execution_used_ms`, formatted. No cost.
3. **The examined artifact**, branched on `evidence.examined_artifact_type`:
   - `patch` → the file accordion. Each row is a `<button>` with the path and its ±counts; expanding renders the hunks with `kind`-driven classes. A `lossy` patch carries a one-line warning above the accordion.
   - `plan` → the plan payload as prose: summary, then the steps as an ordered list.
   - Neither, or `examined_artifact_id === ''` → "The evidence for this gate is not available." and no accordion.
4. **Sticky actions** — `<GateControls>` in a `sticky bottom-0` container, which is the thumb zone the locked design asks for. The component supplies the controls; this host supplies the position.

**The summary and the examined artifact are two different fetches and they fail independently.** A verdict that 404s must not take the diff down, and vice versa: render each block from its own query state, and let the surface degrade to whichever half it has.

- [ ] **Step 5: Split the Approvals route**

`approvals.tsx` becomes `approvals.index.tsx` (unchanged content, plus cards that link) and a new `approvals.$runId.$nodeId.tsx`:

```tsx
/**
 * The approval surface, full-screen. A URL, not a modal: this is the screen a
 * person is sent to by a notification, and a gate under review is exactly the
 * thing they will want to send to somebody else.
 *
 * It loads the RUN rather than re-reading the inbox, because the run carries
 * `execution_used_ms` for the hard-facts row and the gate phase carries the
 * same evidence the inbox does — so a deep link works with no inbox in the
 * cache at all.
 */
export const Route = createFileRoute('/_app/approvals/$runId/$nodeId')({
  loader: ({ context, params }) => context.queryClient.ensureQueryData(runQueryOptions(params.runId)),
  component: ApprovalScreen,
})
```

`ApprovalScreen` finds the phase whose `node_id` matches and whose `evidence` is non-null. **A gate that is no longer waiting has no evidence**, and that is the 409-after-the-fact case: render the phase's `decision` block — "approved by alice@example.com" — rather than an empty surface.

Regenerate the route tree: `../scripts/with-node ./node_modules/.bin/tsr generate`.

- [ ] **Step 6: Run everything and confirm**

```bash
cd web
../scripts/with-node ./node_modules/.bin/vitest run
../scripts/with-node ./node_modules/.bin/tsc --noEmit
../scripts/with-node node tools/arch/main.ts
```
Expected: PASS. `web:arch` is the one to watch: `approval-surface.tsx` is imported by `routes/_app/approvals.$runId.$nodeId.tsx` and (in Task 16) by `routes/_app/runs.$runId.tsx`, whose common ancestor is `routes/_app` — the clamp then requires it at `approvals/`, which is where it is.

- [ ] **Step 7: Mutation proofs**

```bash
cd web
# 1. Render an empty summary block when summary_artifact_id is null.
#    Expect: "renders NO summary block at all" fails.
# 2. Render "tests ✓" when tests is null.
#    Expect: "says tests are UNKNOWN" fails on both assertions.
# 3. Ignore examined_artifact_type and always render the accordion.
#    Expect: "renders a PLAN gate as prose" fails.
# 4. Move the hard-facts row above the summary.
#    Expect: "leads with the agent summary" fails. Without this the locked
#    ordering is a comment.
```

- [ ] **Step 8: Gate and commit**

```bash
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git add web
git commit -m "feat(web): the approval surface, summary-first, built once"
```

---

### Task 13: Home

Locked Home A is **state-first**. In order: a status line, "Needs you" gate cards, "Running" cards with live phase dots, recent activity.

**Gate cards carry inline approve · request changes · reject, as locked.** §10.2 records a deviation this document proposed and withdrew: navigating to a screen is not examining what is on it either, `examined_artifact_id` is a *server-selected evidence binding* and not a record of human examination, and no reachable design closes the gap. Whether the affordance gets used is a taste question, and §1 builds the place to answer it — the wave-1 operator checkpoint. **Ship the locked affordance; find out.**

The assistant-generated résumé and the persistent ask-bar are row 13 (§10.6). Until then **the status line is computed client-side from counts already on screen — a plain count line, not a fabricated summary.**

**Files:**
- Create: `web/src/runs/phase-dots.tsx`, `web/src/runs/run-card.tsx`
- Modify: `web/src/routes/_app/index.tsx`
- Create: `web/src/routes/_app/index.test.tsx`

**Interfaces:**
- Consumes: `runsQueryOptions` (Task 9), `approvalsQueryOptions` (Plan 11), `GateControls` + `expectDecisionRules` (Task 11).
- Produces: `<PhaseDots phases={ApiPhaseSummaryResponse[]} />` and `<RunCard run={ApiRunSummaryResponse} />`, both reused by Task 14.

- [ ] **Step 1: Write the failing tests**

`web/src/routes/_app/index.test.tsx`:

```tsx
describe('Home', () => {
  it('is state-first: status line, needs you, running, recent — in that order', async () => {
    renderApp('/', [inbox(2), runs(mixed())])
    const order = (await screen.findAllByRole('heading')).map((h) => h.textContent)
    expect(order).toEqual(['Needs you', 'Running', 'Recent'])
  })

  it('states the counts and nothing it cannot know', async () => {
    // Locked Home A opens with an assistant-generated situation résumé. The
    // assistant is row 13 and unbuilt, so this is a COUNT LINE — computed from
    // what is already on screen. A stub never fakes success, and a plausible
    // one-line narrative is the most convincing fake available here.
    renderApp('/', [inbox(1), runs(mixed())])
    const line = await screen.findByTestId('status-line')
    expect(line).toHaveTextContent(/2 running/)
    expect(line).toHaveTextContent(/1 waiting on you/)
  })

  it('draws a dot per node, typed, with the gate distinguishable', async () => {
    // A row of dots, not one dot. A scalar "current phase" cannot render the
    // run's shape, which is why phase_summary is an array on the wire.
    renderApp('/', [inbox(0), runs([runWith(['implement:succeeded', 'review:running', 'gate:null'])])])
    const dots = within(await screen.findByTestId('phase-dots')).getAllByRole('listitem')
    expect(dots).toHaveLength(3)
    expect(dots[0]).toHaveAttribute('data-state', 'succeeded')
    expect(dots[1]).toHaveAttribute('data-state', 'running')
    // Not materialized: a DIM dot, not a queued one. `pending` means a row
    // exists and is claimable; null means no row exists.
    expect(dots[2]).toHaveAttribute('data-state', 'not-started')
    expect(dots[2]).toHaveAttribute('data-node-type', 'gate')
  })

  it('gate cards carry the three controls inline, as locked', async () => {
    renderApp('/', [inbox(1), runs(mixed())])
    const card = await screen.findByTestId('gate-card')
    for (const name of [/^approve$/i, /request changes/i, /^reject$/i]) {
      expect(within(card).getByRole('button', { name })).toBeInTheDocument()
    }
  })

  it('and the card body opens the run AT THAT GATE, already expanded', async () => {
    // "The detail page is a choice, not a toll." Locked §3.2 says tapping the
    // body opens the run's EXPANDED GATE PHASE — not the top of a timeline the
    // person then has to find the gate in, which on a phone is a scroll and a
    // second tap for the screen they were already looking at.
    //
    // The expansion is URL state (`?phase=<node_id>`), per the constitution: a
    // gate under review is exactly the thing a person links to somebody else,
    // and React state would make that link land on a collapsed timeline.
    const router = renderApp('/', [inbox(1), runs(mixed())])
    await userEvent.click(await screen.findByRole('link', { name: /gate on A/ }))
    expect(router.state.location.pathname).toMatch(/^\/runs\//)
    expect(router.state.location.search).toMatchObject({ phase: 'gate' })
  })

  it('carries the decision rules of every host', async () => {
    await expectDecisionRules((evidence) => renderApp('/', [inbox(1, evidence), runs(mixed())]))
  })

  it('says whether a gate is a plan or a patch before offering a decision', async () => {
    // A card that offers a decision should say what is being decided. This is
    // why examined_artifact_type is on the inbox row and not only on the run.
    renderApp('/', [inbox(1, { ...decidable, examined_artifact_type: 'plan' }), runs(mixed())])
    expect(await screen.findByTestId('gate-card')).toHaveTextContent(/plan/i)
  })

  it('has two distinct empty states', async () => {
    renderApp('/', [inbox(0), runs([])])
    expect(await screen.findByText(/nothing is waiting on you/i)).toBeInTheDocument()
    expect(screen.getByText(/no runs yet/i)).toBeInTheDocument()
  })

  it('redraws when a phase transition arrives, with no user action', async () => {
    // The end-to-end claim of Task 9, asserted on the rendered DOM rather than
    // on a request count: a refetch is the MECHANISM, and forbidding it would
    // assert against the design.
    const stream = scriptedStream()
    let phase = 'running'
    renderApp('/', [inbox(0), runs(() => [runWith([`implement:${phase}`])]), stream.handler])
    await screen.findByTestId('phase-dots')
    phase = 'succeeded'
    stream.emit({ kind: 'phase.succeeded', run_id: 'run-1' })
    await waitFor(() =>
      expect(within(screen.getByTestId('phase-dots')).getAllByRole('listitem')[0]).toHaveAttribute(
        'data-state',
        'succeeded',
      ),
    )
  })
})
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd web && ../scripts/with-node ./node_modules/.bin/vitest run src/routes/_app/index.test.tsx`
Expected: FAIL — Home is still `StubPanel`.

- [ ] **Step 3: `PhaseDots`**

`web/src/runs/phase-dots.tsx`:

```tsx
/**
 * The run's shape as a row of dots — done, active, gate, pending — which is
 * what locked Home and Tasks both show. A scalar "current phase" cannot render
 * this, and that is why `phase_summary` is an array on the wire.
 *
 * `data-state` and `data-node-type` are on the DOM because they are the only
 * way a test can assert what a person SEES here: the whole content of this
 * component is colour and shape, and a test that read class names would be
 * asserting Tailwind's spelling rather than the design.
 *
 * A gate dot is drawn differently from a work dot, and the type comes off the
 * wire rather than being inferred from the node id — a client that guessed
 * "the node called gate is the gate" would be wrong on the first workflow that
 * names one `approval`.
 */
export function PhaseDots(props: { phases: PhaseSummary[] }) {
  return (
    <ul data-testid="phase-dots" className="flex items-center gap-1">
      {props.phases.map((p) => (
        <li
          key={p.node_id}
          data-state={p.status ?? 'not-started'}
          data-node-type={p.node_type}
          title={`${p.node_id} — ${p.status ?? 'not started'}`}
          className={dotClass(p)}
        />
      ))}
    </ul>
  )
}
```

`dotClass` maps `(status, node_type)` onto semantic tokens only: succeeded → `bg-success`, running → `bg-accent` with a ring, `waiting_decision` → `bg-warning`, failed/dead → `bg-danger`, null → `bg-border`. No Tailwind hue names — `web:arch` rejects them.

- [ ] **Step 4: `RunCard` and Home**

`RunCard` renders title, repo/workflow labels, `PhaseDots`, and the relevant timestamp (`finished_at ?? started_at ?? created_at`), wrapped in a `<Link to="/runs/$runId">`.

`web/src/routes/_app/index.tsx`:

```tsx
export const Route = createFileRoute('/_app/')({
  loader: async ({ context }) => {
    // BOTH, in parallel. Home's three sections come from two queries and
    // nothing else; a card that had to fetch per row is the N+1 GET /runs
    // exists to remove.
    await Promise.all([
      context.queryClient.ensureQueryData(approvalsQueryOptions),
      context.queryClient.ensureQueryData(runsQueryOptions()),
    ])
  },
  component: Home,
})
```

Sections:

- **Status line** — `<p data-testid="status-line">` with `{running} running · {waiting} waiting on you`, both counted from the two lists already in hand. When both are zero it says so plainly rather than rendering an empty bar.
- **Needs you** — the approvals list as gate cards. Each card: task title (a link to the run), the node and what kind of artifact it examines, waiting-since, and `<GateControls>` with `evidence` straight off the row.
- **Running** — runs whose status is `running`, `pending` or `waiting`, as `RunCard`s.
- **Recent** — the rest of the list, capped at ten, newest first. **`log()` nothing and hide nothing**: if the cap drops rows, the section says "showing the 10 most recent" — a silent truncation reads as "this is everything".

- [ ] **Step 5: Run them and confirm they pass**

Run: `cd web && ../scripts/with-node ./node_modules/.bin/vitest run`
Expected: PASS. The full suite, because `index.tsx` losing its `StubPanel` may be asserted elsewhere.

- [ ] **Step 6: Mutation proofs**

```bash
cd web
# 1. Render only the first phase as a dot.
#    Expect: "draws a dot per node" fails on the length.
# 2. Map a null status onto 'pending'.
#    Expect: the not-started assertion fails. This is the distinction the
#    contract went out of its way to make nullable; without the test it is free
#    to collapse.
# 3. Hide only approve on the gate card.
#    Expect: expectDecisionRules fails from THIS host — which is the point of
#    re-running it here rather than trusting Task 11.
```

- [ ] **Step 7: Gate and commit**

```bash
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git add web
git commit -m "feat(web): Home — state-first, with live dots and inline gate decisions"
```

---

### Task 14: Tasks — attention-grouped, and a FAB that creates one

Locked Tasks B is an attention-grouped live list: **Needs you · In flight · Up next · Recently done**. The groups are projections of run state, not a stored field.

**The grouping needs its mapping written down, because there is no task lifecycle to lean on.** `tasks` has no status column; `TaskResponse.Status` *is* the latest run's status, one of seven. Two of the four groups are not obvious:

- **Up next** is `pending` — a run created at intake the orchestrator has not started. On a single-operator system that is a state measured in seconds, so this group is **usually empty**, and its empty state is the one on screen most of the time. It gets designed, not defaulted.
- **`superseded`** is a run replaced by a re-run. It belongs in *Recently done* or nowhere; what it must not do is fall through the switch and vanish, taking its task off the list.

The mockup's caption has tasks flowing `draft → ready → running → in review → done`. Three of those five have no representation in the schema (§10.8) and 12a builds none of them.

**Files:**
- Create: `web/src/tasks/tasks-query.ts`, `web/src/tasks/task-groups.ts`, `web/src/tasks/task-groups.test.ts`, `web/src/tasks/new-task-form.tsx`
- Modify: `web/src/routes/_app/tasks.tsx`
- Create: `web/src/routes/_app/tasks.test.tsx`

**Interfaces:**
- Consumes: `runsQueryOptions` (Task 9), `PhaseDots` (Task 13), `useAppForm` (Plan 11).
- Produces: `groupTasks(tasks: ApiTaskResponse[]): Record<TaskGroup, ApiTaskResponse[]>` with `TaskGroup = 'needs-you' | 'in-flight' | 'up-next' | 'recently-done'`. Task 17's second spec drives the form.

- [ ] **Step 1: Write the failing pure test for the mapping**

`web/src/tasks/task-groups.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { ALL_RUN_STATUSES, groupTasks, type TaskGroup } from './task-groups'

const t = (status: string) => ({ id: status, status }) as never

describe('groupTasks', () => {
  it('places every one of the seven run statuses', () => {
    // THE WHOLE POINT. There is no task lifecycle to lean on, so this mapping
    // IS the design — and a status that falls through the switch does not land
    // in a wrong group, it takes its task off the screen entirely.
    const want: Record<string, TaskGroup> = {
      waiting: 'needs-you',
      running: 'in-flight',
      pending: 'up-next',
      succeeded: 'recently-done',
      failed: 'recently-done',
      dead: 'recently-done',
      superseded: 'recently-done',
    }
    expect(Object.keys(want).sort()).toEqual([...ALL_RUN_STATUSES].sort())
    for (const [status, group] of Object.entries(want)) {
      const got = groupTasks([t(status)])
      expect(got[group]).toHaveLength(1)
      // And in exactly one group.
      expect(Object.values(got).flat()).toHaveLength(1)
    }
  })

  it('never drops a task, whatever the server sends', async () => {
    // A status added server-side must degrade to VISIBLE. Disappearing from
    // the list is the one behaviour a person cannot diagnose: there is nothing
    // on screen to be wrong about.
    const got = groupTasks([t('a_status_from_the_future')])
    expect(Object.values(got).flat()).toHaveLength(1)
  })

  it('keeps ALL_RUN_STATUSES honest against the contract', () => {
    // Hand-maintained lists rot. This one mirrors schema.sql's CHECK
    // constraint, and the assertion above is only as good as it is.
    expect([...ALL_RUN_STATUSES]).toEqual([
      'pending',
      'running',
      'waiting',
      'succeeded',
      'failed',
      'superseded',
      'dead',
    ])
  })
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd web && ../scripts/with-node ./node_modules/.bin/vitest run src/tasks`
Expected: FAIL — module not found.

- [ ] **Step 3: Write the mapping**

`web/src/tasks/task-groups.ts`:

```ts
/**
 * The seven run statuses, mirroring schema.sql's CHECK constraint on
 * runs.status. Named here so the mapping below can be asserted EXHAUSTIVE
 * rather than merely correct on the cases someone thought of.
 */
export const ALL_RUN_STATUSES = [
  'pending',
  'running',
  'waiting',
  'succeeded',
  'failed',
  'superseded',
  'dead',
] as const

export type TaskGroup = 'needs-you' | 'in-flight' | 'up-next' | 'recently-done'

/**
 * Seven run statuses onto four locked groups.
 *
 * `TaskResponse.Status` IS the latest run's status (dto.go:190) — `tasks` has
 * no status column at all, and a task is born already running because
 * createTask makes a run. So the groups are projections of run state and the
 * mockup's `draft → ready → in review` lifecycle has nothing behind it (§10.8).
 *
 * TWO ENTRIES ARE NOT OBVIOUS AND BOTH ARE DELIBERATE:
 *
 *   `pending` is UP NEXT — a run intake created that the orchestrator has not
 *   started. On a single-operator system that lasts seconds, so this group is
 *   usually empty and its empty state is what is on screen most of the time.
 *
 *   `superseded` is RECENTLY DONE — a run replaced by a re-run. The tempting
 *   alternative is to skip it, and skipping takes the whole TASK off the list,
 *   because a task's status is its newest run's and a re-run supersedes the
 *   previous one at the moment the new one is created.
 *
 * The default arm is not defensive padding: a status added server-side must
 * degrade to VISIBLE, because a task that vanishes leaves nothing on screen to
 * be wrong about.
 */
export function groupTasks(tasks: Task[]): Record<TaskGroup, Task[]> {
  const out: Record<TaskGroup, Task[]> = {
    'needs-you': [],
    'in-flight': [],
    'up-next': [],
    'recently-done': [],
  }
  for (const task of tasks) {
    switch (task.status) {
      case 'waiting':
        out['needs-you'].push(task)
        break
      case 'running':
        out['in-flight'].push(task)
        break
      case 'pending':
        out['up-next'].push(task)
        break
      default:
        out['recently-done'].push(task)
    }
  }
  return out
}
```

- [ ] **Step 4: Write the failing screen test**

`web/src/routes/_app/tasks.test.tsx`:

```tsx
describe('Tasks', () => {
  it('renders the four locked groups in order', async () => {
    renderApp('/tasks', [tasks(everyStatus()), runs(mixed())])
    const order = (await screen.findAllByRole('heading')).map((h) => h.textContent)
    expect(order).toEqual(['Needs you', 'In flight', 'Up next', 'Recently done'])
  })

  it('designs the empty state that is on screen most of the time', async () => {
    // "Up next" is `pending` and pending lasts seconds here. Its empty state is
    // not an edge case; it is the default appearance of a quarter of this
    // screen.
    renderApp('/tasks', [tasks([]), runs([])])
    expect(await screen.findByText(/nothing queued/i)).toBeInTheDocument()
  })

  it('keeps the two list empty states distinct', async () => {
    renderApp('/tasks', [tasks([]), runs([]), noGrants()])
    expect(await screen.findByText(/no projects are shared with you yet/i)).toBeInTheDocument()
  })

  it('shows live dots for a task whose latest run is on the runs page', async () => {
    renderApp('/tasks', [tasks([taskWithRun('run-1')]), runs([runWith(['implement:running'])])])
    expect(await screen.findByTestId('phase-dots')).toBeInTheDocument()
  })

  it('shows the task WITHOUT dots when its latest run is not on the page', async () => {
    // GET /tasks is unpaginated and GET /runs is one page, so an old task's run
    // can be absent. The task must still be listed and must not get invented
    // dots — a row of grey dots on a succeeded run reads as "never started".
    renderApp('/tasks', [tasks([taskWithRun('run-999')]), runs([])])
    expect(await screen.findByText(/an older task/i)).toBeInTheDocument()
    expect(screen.queryByTestId('phase-dots')).toBeNull()
  })

  it('the FAB creates a task and lands on its run', async () => {
    const router = renderApp('/tasks', [tasks([]), runs([]), projects(), repos(), workflows(), createTaskOk()])
    await userEvent.click(await screen.findByRole('button', { name: /new task/i }))
    await userEvent.selectOptions(await screen.findByLabelText(/project/i), 'p1')
    await userEvent.selectOptions(await screen.findByLabelText(/repo/i), 'repo-1')
    await userEvent.selectOptions(await screen.findByLabelText(/workflow/i), 'wf-1')
    await userEvent.type(screen.getByLabelText(/title/i), 'Add revocation')
    await userEvent.type(screen.getByLabelText(/instructions/i), 'Add a session revocation table.')
    await userEvent.click(screen.getByRole('button', { name: /create/i }))
    await waitFor(() => expect(router.state.location.pathname).toMatch(/^\/runs\//))
  })

  it('routes a server field error back onto its field', async () => {
    // Plan 11's problem-to-fields, unchanged. createTask really answers 404
    // when the forge cannot see the repo and 429 when it is throttling, and
    // both must land somewhere a person reads.
    renderApp('/tasks', [tasks([]), runs([]), projects(), repos(), workflows(), createTaskFieldError('title')])
    await openFormAndSubmit()
    expect(await screen.findByRole('alert')).toHaveTextContent(/title/i)
  })

  it('surfaces a forge failure as itself', async () => {
    renderApp('/tasks', [tasks([]), runs([]), projects(), repos(), workflows(), createTaskForge404()])
    await openFormAndSubmit()
    expect(await screen.findByText(/repository|branch/i)).toBeInTheDocument()
  })
})
```

- [ ] **Step 5: Build the screen and the form**

`tasks.tsx` gains a loader over `tasksQueryOptions` **and** `runsQueryOptions()`, groups with `groupTasks`, and renders four sections. Each row shows the title, the labels, and `PhaseDots` **only when the task's `latest_run_id` is present in the runs page** — `runs.find(r => r.id === task.latest_run_id)`, and nothing when it is not.

`new-task-form.tsx` is the FAB's short form on `useAppForm`: project → repo (a cascade: `GET /projects`, then `GET /projects/{id}/repos`), workflow (`GET /workflows`, submitting `workflow_version: workflow.latest_version`), title, instructions. Instructions go into `inputs` — `CreateTaskRequest` has no instructions field, and `inputs` is the task-level map the definition's nodes read.

**Assistant-drafted tasks are row 13 (§10.6).** The short form is the locked design's own stated alternative, not a substitute invented here — say so in the file.

The FAB itself is a `<button>` positioned above the tab bar with a 44px touch target, opening a bottom sheet (`vaul`, already a dependency, and the locked mobile-first decision names bottom sheets as the default modal).

Add a default `/api/v1/tasks` handler to `web/src/test/render-app.tsx`, behind the caller's handlers like the four already there — `onUnhandledRequest: 'error'` otherwise turns a missing handler into a failure far from its cause, and every protected route now renders a shell that may fetch tasks.

**This step gives `/tasks` a loader**, which is what Task 15 exists to answer. `tasks.tsx`'s comment forbidding a loader while Plan 12 is unbuilt is now false — delete it and replace it with a pointer to the rehomed test:

```tsx
/**
 * This route HAD no loader, deliberately, and `_app.test.tsx` navigated here
 * because of it: a route that issues no request is the case a redirect-on-401
 * design cannot catch. Plan 12a gives it one, and the guard test is rehomed
 * onto this route in the next commit rather than being allowed to quietly
 * assert less. See `_app.test.tsx`'s first case.
 */
```

- [ ] **Step 6: Run everything and confirm**

```bash
cd web
../scripts/with-node ./node_modules/.bin/tsr generate
../scripts/with-node ./node_modules/.bin/vitest run
```
Expected: PASS — including `_app.test.tsx`'s first test, which still passes and now proves something different. **That is the whole reason Task 15 is next and nothing may come between.**

- [ ] **Step 7: Mutation proofs**

```bash
cd web
# 1. Delete the `superseded` case (let it fall through to nothing).
#    Expect: "places every one of the seven run statuses" fails. Then check the
#    SCREEN test still passes — it does, which is why the pure exhaustive test
#    is the one carrying this rule.
# 2. Render dots for a task whose run is missing, from an empty array.
#    Expect: "shows the task WITHOUT dots" fails.
```

- [ ] **Step 8: Gate and commit**

```bash
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git add web
git commit -m "feat(web): Tasks — four attention groups over seven run statuses, and a FAB"
```

---

### Task 15: The auth-guard test, rehomed

`_app.test.tsx` navigates to `/tasks` **because that route issues no request** — it proves the 401 redirect fires in the case no server rejection can produce. Task 14 gave Tasks a loader and destroyed that property. After 12a, plausibly no loader-free route remains.

**Inventing a route to keep the test green would be a seam with no production caller** — a mistake this project has already paid for once, where a test-only affordance made all five of its own tests vacuous.

**But "assert the guard directly" is not the fix either.** `_app` authenticates in its **`loader`**, not in `beforeLoad` (`_app.tsx:25`), and a direct callback assertion would drop the router-and-history behaviour the current test actually proves: that exactly one redirect fires and `/login` **replaces** the entry rather than stacking on it.

So the rehoming keeps a **router-level** unauthorized-navigation test, retargeted at the now-loader-bearing Tasks route. **The property being preserved is the guard's observable behaviour under the router, not the absence of a request.** This is its own task with its own mutation proof.

**Files:**
- Modify: `web/src/routes/_app.test.tsx`

**Interfaces:**
- Consumes: the Tasks loader (Task 14).
- Produces: nothing other tasks consume.

- [ ] **Step 1: Rewrite the first test**

```tsx
it('sends an unauthenticated visit to a loader-bearing route to /login, exactly once', async () => {
  // THIS TEST CHANGED ITS MEANING IN PLAN 12a AND THE CHANGE IS THE POINT.
  //
  // It used to navigate to /tasks BECAUSE that route issued no request: a stub
  // makes nothing for the server to reject, which is the one case a
  // redirect-on-401 design cannot catch. Task 14 gave /tasks a loader, and
  // after 12a plausibly no loader-free route remains.
  //
  // The two fixes that were rejected, and why:
  //
  //   Invent a loader-free route to keep the old property. That is a seam with
  //   no production caller — a mistake this project has already paid for, where
  //   a test-only affordance made all five of its own tests vacuous.
  //
  //   Assert the guard callback directly. `_app` authenticates in its LOADER,
  //   not in beforeLoad, and a callback assertion drops the router-and-history
  //   behaviour this test actually proves.
  //
  // What is preserved is the guard's OBSERVABLE BEHAVIOUR UNDER THE ROUTER:
  // exactly one redirect, and /login REPLACES the entry rather than stacking
  // on it. What is given up is the no-request case, knowingly.
  let taskRequests = 0
  const router = renderApp('/tasks', [
    http.get('/api/v1/auth/me', noSession),
    http.get('/api/v1/tasks', () => {
      taskRequests += 1
      return noSession()
    }),
  ])
  expect(await screen.findByRole('button', { name: /sign in/i })).toBeInTheDocument()

  // ONE entry. A loader `throw redirect()` during the initial load REPLACES it;
  // a navigate() from a rendered error boundary PUSHES one, leaving /tasks
  // behind /login in the back stack — so Back returns the visitor to a route
  // they cannot see and re-runs the whole failed load.
  //
  // Without this line the test is VACUOUS: neuter _app's ensureQueryData and it
  // still passes, because Tasks' own loader now 401s into AppErrorBoundary,
  // which navigates to the same place. The visible outcome is identical; the
  // history is not.
  expect(router.history.length).toBe(1)

  // AND THE CHILD LOADER'S OWN FAILURE ADDS NO SECOND REDIRECT. This is the
  // assertion the rehoming buys and the old test could not make, because the
  // old target had no loader to fail. Two redirects would be invisible to a
  // person and would double every unauthenticated load in the product.
  expect(router.state.location.pathname).toBe('/login')
  expect(taskRequests).toBeLessThanOrEqual(1)
})
```

Update the file's leading comment, which currently explains the old target's significance.

- [ ] **Step 2: Run it and confirm it passes**

Run: `cd web && ../scripts/with-node ./node_modules/.bin/vitest run src/routes/_app.test.tsx`
Expected: PASS.

- [ ] **Step 3: The mutation proof — this is the task's deliverable**

```bash
cd web
# 1. Neuter the layout guard: make _app's loader swallow the 401 instead of
#    redirecting.
#    Expect: FAIL on `router.history.length` — 2, not 1. If it PASSES, the
#    rehomed test is vacuous in exactly the way its predecessor was measured
#    to be, and this task is not done.
# 2. Make the layout redirect with navigate() instead of throw redirect().
#    Expect: FAIL on the same line.
# 3. Add a second redirect from the error boundary on the same load.
#    Expect: FAIL on the pathname/one-request assertion.
```

Record all three. Proof 1 is the one that was measured to be missing before `router.history.length` was added, and the rehoming is what puts it at risk again.

- [ ] **Step 4: Gate and commit**

```bash
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git add web
git commit -m "test(web): rehome the auth guard onto a loader-bearing route"
```

---

### Task 16: Run detail — the timeline is the spine

Locked run detail A: **the vertical phase timeline is the spine**, and the run reads top to bottom as its phase graph. **Each phase expands in place** — artifacts and verdicts when done, and at a gate, the examined artifact followed by the decision controls.

**A gate phase expands into the approval surface of §3.5 — the same component, not a second implementation of it.** Locked §7 decides twice that a gate is decidable where its evidence is shown: once here inline, once on the summary-first screen. Those are one surface with two hosts, and building it once is what keeps them from drifting.

**A publish phase expands into its publication**, which is where "see the PR" is delivered. `PublicationResponse` already carries everything: a four-value status, branch and target branch, base and head SHA, `pr_number` and `pr_url`, and — the one that matters most — `halt_reason`. **A publication that halted is the case the operator most needs to see and the easiest to render as a blank space**, so `halted` gets a stated appearance rather than falling out of a switch.

**The phase spine is typed; the event log is not.** `ApiRunEventResponse` is `{id, kind, occurred_at, payload}` with **no `node_id` column** — the node lives inside the untyped payload. Events therefore render as a flat chronological log, not grouped under their phase. Grouping would mean depending on a field `contracts:check` cannot police.

Cancel and retry are §10.3 and 12a ships neither. Live agent output is §10.4: **no log-serving route exists**, logs are quarantined and redacted specifically because secrets reach them, and serving them is a safety decision that deserves its own design pass. A live *phase-status* spine is buildable and this task builds it; the terminal box is not.

**Files:**
- Create: `web/src/routes/_app/runs.$runId.tsx`, `web/src/routes/_app/runs.$runId.test.tsx`
- Rename: `web/src/routes/_app/runs.tsx` → `web/src/routes/_app/runs.index.tsx` (and give it the real list)
- Create: `web/src/runs/run-events-query.ts`, `web/src/runs/publication-panel.tsx`

**Interfaces:**
- Consumes: `runQueryOptions` (Task 9), `ApprovalSurface` (Task 12), `PhaseDots` / `RunCard` (Task 13), `expectDecisionRules` (Task 11).
- Produces: the `/runs/$runId` route string; nothing later consumes it.

- [ ] **Step 1: Write the failing tests**

`web/src/routes/_app/runs.$runId.test.tsx`:

```tsx
describe('run detail', () => {
  it('reads top to bottom as the phase graph', async () => {
    renderApp('/runs/run-1', [run(threePhases())])
    const spine = within(await screen.findByTestId('phase-timeline')).getAllByRole('listitem')
    expect(spine.map((p) => p.getAttribute('data-node-id'))).toEqual(['impl', 'review', 'gate'])
  })

  it('expands a finished phase in place, showing its artifacts', async () => {
    renderApp('/runs/run-1', [run(threePhases())])
    await userEvent.click(await screen.findByRole('button', { name: /impl/ }))
    expect(await screen.findByText(/patch/)).toBeInTheDocument()
    // IN PLACE. A phase that navigated somewhere would break the locked spine:
    // the timeline is the screen, not an index into other screens.
    expect(screen.getByTestId('phase-timeline')).toBeInTheDocument()
  })

  it('expands a WAITING GATE into the approval surface — the same component', async () => {
    renderApp('/runs/run-1', [run(threePhases()), artifact(patchPayload())])
    await userEvent.click(await screen.findByRole('button', { name: /gate/ }))
    // The surface's own testids, asserted from THIS host: if run detail grew a
    // second, simpler gate renderer, these would be absent and the two
    // surfaces would have drifted exactly as §3.4 warns.
    expect(await screen.findByTestId('hard-facts')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: /^approve$/i })).toBeInTheDocument()
  })

  it('carries the decision rules of every host', async () => {
    await expectDecisionRules((evidence) =>
      renderApp('/runs/run-1', [run(threePhases({ evidence })), artifact(patchPayload())]),
    )
  })

  it('shows a DECIDED gate as what happened, not as a decidable one', async () => {
    renderApp('/runs/run-1', [run(decidedGate())])
    await userEvent.click(await screen.findByRole('button', { name: /gate/ }))
    expect(await screen.findByText(/alice@example.com/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^approve$/i })).toBeNull()
  })

  it('expands a publish phase into the PR', async () => {
    renderApp('/runs/run-1', [run(published({ pr_number: 42, pr_url: 'https://forge/pr/42' }))])
    await userEvent.click(await screen.findByRole('button', { name: /publish/ }))
    const link = await screen.findByRole('link', { name: /#42/ })
    expect(link).toHaveAttribute('href', 'https://forge/pr/42')
  })

  it('gives a HALTED publication a stated appearance', async () => {
    // The case the operator most needs to see and the easiest to render as a
    // blank space: `halted` is one of four statuses and the only one with no
    // pr_url to hang the UI on.
    renderApp('/runs/run-1', [
      run(published({ status: 'halted', halt_reason: 'base moved under the approved patch' })),
    ])
    await userEvent.click(await screen.findByRole('button', { name: /publish/ }))
    expect(await screen.findByText(/halted/i)).toBeInTheDocument()
    expect(await screen.findByText(/base moved under the approved patch/)).toBeInTheDocument()
  })

  it('renders events as a flat chronological log', async () => {
    // ApiRunEventResponse has no node_id — the node is inside the untyped
    // payload. Grouping events under their phase would depend on a field
    // contracts:check cannot police, and would silently mis-place every event
    // whose payload shape changed.
    renderApp('/runs/run-1', [run(threePhases()), events(threeEvents())])
    const log = within(await screen.findByTestId('event-log')).getAllByRole('listitem')
    expect(log).toHaveLength(3)
    expect(log[0]).toHaveTextContent('run.created')
  })

  it('has a sticky header with workflow, repo and attempt — and no cancel or retry', async () => {
    // §10.3: cancel does not exist and tasks.go:372 assigns it to Plan 09;
    // whole-task retry DOES exist (rerunTask) and is deferred anyway, so the
    // checkpoint can decide what this header should offer as a whole.
    renderApp('/runs/run-1', [run(threePhases())])
    const header = await screen.findByTestId('run-header')
    expect(header).toHaveTextContent('e2e')
    expect(header).toHaveTextContent('caesar/e2e')
    expect(header).toHaveTextContent(/attempt 1/i)
    expect(within(header).queryByRole('button', { name: /cancel|retry/i })).toBeNull()
  })

  it('advances live when a phase transition arrives, with no user action', async () => {
    const stream = scriptedStream()
    let phases = threePhases()
    renderApp('/runs/run-1', [run(() => phases), stream.handler])
    await screen.findByTestId('phase-timeline')
    phases = threePhases({ review: 'succeeded' })
    stream.emit({ kind: 'phase.succeeded', run_id: 'run-1' })
    await waitFor(() =>
      expect(screen.getByTestId('phase-review')).toHaveAttribute('data-state', 'succeeded'),
    )
  })

  it('shows no live agent output, and does not pretend there is any', async () => {
    // §10.4. No log-serving route exists; logs live on the runner's disk behind
    // a log_ref artifact and the event vocabulary is lifecycle-only. An empty
    // terminal box would be the most convincing lie on this screen.
    renderApp('/runs/run-1', [run(threePhases())])
    await userEvent.click(await screen.findByRole('button', { name: /review/ }))
    expect(screen.queryByTestId('agent-output')).toBeNull()
  })
})
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd web && ../scripts/with-node ./node_modules/.bin/vitest run src/routes/_app/runs`
Expected: FAIL — `/runs/$runId` does not exist.

- [ ] **Step 3: Split the runs routes**

`runs.tsx` → `runs.index.tsx`, replacing `StubPanel` with the real list: `runsQueryOptions(filters)` where `filters` is **URL state** (`?status=`), rendered as `RunCard`s. Its two empty states are the inherited pair.

`runs.$runId.tsx`:

```tsx
export const Route = createFileRoute('/_app/runs/$runId')({
  loader: ({ context, params }) => context.queryClient.ensureQueryData(runQueryOptions(params.runId)),
  component: RunDetail,
})
```

The screen, top to bottom:

1. **Sticky header** — workflow name, repo slug, `attempt N` off the newest phase. No controls (§10.3).
2. **`<ol data-testid="phase-timeline">`** — one `<li data-node-id data-state>` per phase, in the order `ApiRunResponse.phases` arrives (`ListPhasesForRun` orders by `created_at, node_id, attempt`, which is materialization order and is the run's own reading of its graph). Each is a disclosure `<button>` expanding **in place**.

   **Which phase is expanded is URL state**, read from a `?phase=<node_id>` search param with a `validateSearch` on the route. Home's gate cards link here with it set (Task 13), so tapping a card body lands on the gate already open rather than at the top of a timeline the person has to scroll. Toggling a phase writes the param, so the state a person is looking at is the state they can send to somebody else — the constitution's URL-as-state rule, and the reason this is not `useState`.

   Add a test for it:

   ```tsx
   it('opens at the phase the URL names', async () => {
     renderApp('/runs/run-1?phase=gate', [run(threePhases()), artifact(patchPayload())])
     expect(await screen.findByTestId('hard-facts')).toBeVisible()
   })

   it('ignores a phase param naming a node this run does not have', async () => {
     // A stale link must land on the timeline, not on a blank screen or a
     // crash: node ids come from a workflow version that may since have been
     // superseded by one that renamed the node.
     renderApp('/runs/run-1?phase=nonexistent', [run(threePhases())])
     expect(await screen.findByTestId('phase-timeline')).toBeVisible()
   })
   ```
3. **Expanded content, by phase kind**:
   - a gate with `evidence` → `<ApprovalSurface>`, the same component Task 12 built.
   - a gate with `decision` → the decision block: decision, `acted_by_email`, `note`, `acted_at`.
   - a publish node → `<PublicationPanel>`.
   - anything else → its artifacts, by type and id, each linking to the artifact payload where one is renderable.
4. **`<ul data-testid="event-log">`** — the flat chronological log from `GET /runs/{id}/events`, kind and time. Its query key is `['run', runId, 'events']`, which the bridge's `['run', runId]` invalidation **prefix-matches** — so the log follows the stream with no second rule.

`publication-panel.tsx` switches on all four statuses explicitly, with `halted` rendering `halt_reason` prominently. Write the switch exhaustively over `'pending' | 'pushed' | 'published' | 'halted'` and let TypeScript's exhaustiveness check hold it — a fifth status added server-side must be a compile error here, not a blank panel.

- [ ] **Step 4: Run everything and confirm**

```bash
cd web
../scripts/with-node ./node_modules/.bin/tsr generate
../scripts/with-node ./node_modules/.bin/vitest run
../scripts/with-node ./node_modules/.bin/tsc --noEmit
../scripts/with-node node tools/arch/main.ts
```
Expected: PASS.

- [ ] **Step 5: Mutation proofs**

```bash
cd web
# 1. Replace <ApprovalSurface> with a local three-button block.
#    Expect: "expands a WAITING GATE into the approval surface" fails on
#    hard-facts. This is the drift §3.4 exists to prevent and nothing else
#    would catch it.
# 2. Drop the `halted` arm from the publication switch.
#    Expect: a tsc error (exhaustiveness) AND the halted test fails. If only
#    the test fails, the switch is not exhaustive — fix it.
# 3. Group events under their phase by reading payload.node_id.
#    Expect: "renders events as a flat chronological log" fails on the count.
```

- [ ] **Step 6: Gate and commit**

```bash
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git add web
git commit -m "feat(web): run detail — the timeline spine, the gate surface inline, and the PR"
```

---

### Task 17: Playwright — the three mobile-critical paths

Playwright extends Plan 11's existing rig: Postgres up, migrations applied, admin bootstrapped, the Vite build embedded into a `-tags embedui` binary, specs run against it, `runs.Executor` faked and **nothing else**. Plan 11 built that lane precisely so 12a could add specs to something that already works.

**Paths 1 and 3 are the operator interface for row 08's smoke — not a substitute for it.** The checklist in `backend/README.md` is forge-side and Playwright can observe none of it: a throwaway repo on **each** forge (GitHub *and* GitLab, because the two adapters' response shapes and error mappings are invented until a live call proves them); a branch named `caesar/<approved-artifact-id>` whose head is the reported `head_sha`; the commit's author being the requester and its committer being `caesar <caesar@invalid>`; all four `caesar-*` trailers and **no** `Co-authored-by`; a PR whose base is the default branch and whose number and URL match the publication projection; and the two broken-credential cases at the publish step, in the stated order. **Row 08 closes when every checklist item is recorded against both forges**, which stays an operator event. What 12a changes is that the checklist's operator half — reach the gate, one revision round, approve — is walked on a phone instead of with `curl`.

**Files:**
- Create: `web/e2e/gate-decision.spec.ts`, `web/e2e/task-timeline.spec.ts`
- Modify: `web/e2e/approvals.spec.ts` — it navigates through a shell that now has five tabs
- Modify: `backend/internal/app/webe2e_test.go` — the control server's fixture set

**Interfaces:**
- Consumes: every screen.
- Produces: the acceptance evidence for wave 1's checkpoint.

- [ ] **Step 1: Extend the control server, without loosening its rule**

`webe2e_test.go`'s control server gains two endpoints beside `/fixture/open-gate`. **Raw SQL insertion of a gate or phase row stays banned** — `appendEvents` allocates the ids the resume path depends on, and a hand-written INSERT desynchronises the counter and leaves later resume behaviour broken in a way no assertion in this lane would catch.

- `GET /fixture/seed` → the project, repo and workflow ids the create-task form needs, so the spec can assert on names rather than hunting for them.
- `GET /fixture/run/{id}` → the run's phases and generation as JSON, read through `runs.Service`. **This is the ORACLE for path 3**: "a new generation opened" is a backend fact, and asserting it only from the DOM would let a screen that re-rendered the same generation pass.

- [ ] **Step 2: Path 1 — the gate, end to end on a phone**

`web/e2e/gate-decision.spec.ts`:

```ts
test('a gate opened by the backend reaches Home, opens to its evidence, and approving clears the badge', async ({
  page,
  request,
}) => {
  await signIn(page)

  // BEAT 1: nothing waiting.
  await expect(page.getByTestId('approvals-badge')).toHaveCount(0)

  // BEAT 2: the BACKEND TRANSACTION opens the gate. A spec that skipped this
  // and appended an event instead would invalidate the query, trigger the
  // refetch, and get the identical list back — the badge would not move and the
  // spec would prove nothing while looking like it passed.
  expect((await request.post(`${CONTROL}/fixture/open-gate`)).status()).toBe(202)

  // BEAT 3: it arrives with NO USER ACTION. No click, no reload, no navigation
  // between beat 2 and this assertion.
  await expect(page.getByTestId('approvals-badge')).toHaveText('1', { timeout: 30_000 })
  const card = page.getByTestId('gate-card')
  await expect(card).toBeVisible()

  // BEAT 4: the card opens to its EXAMINED ARTIFACT. This is the §2.2 + §2.4
  // pair working end to end — the id came off the list row, the payload came
  // off the new endpoint, and the diff is the one the gate examines.
  await card.getByRole('link').first().click()
  await expect(page.getByTestId('hard-facts')).toBeVisible()
  await page.getByRole('button', { name: /CAESAR-impl\.md/ }).click()
  await expect(page.getByText(/^\+/).first()).toBeVisible()

  // BEAT 5: approve, and the badge clears with no user action.
  await page.getByRole('button', { name: /^approve$/i }).click()
  await expect(page.getByTestId('approvals-badge')).toHaveCount(0, { timeout: 30_000 })
})
```

- [ ] **Step 3: Path 2 — create a task and watch its run advance**

`web/e2e/task-timeline.spec.ts`:

```ts
test('creating a task produces a run whose timeline advances live', async ({ page, request }) => {
  await signIn(page)
  const seed = await (await request.get(`${CONTROL}/fixture/seed`)).json()

  await page.getByRole('link', { name: 'Tasks' }).click()
  await page.getByRole('button', { name: /new task/i }).click()
  await page.getByLabel(/project/i).selectOption({ label: seed.project })
  await page.getByLabel(/repo/i).selectOption({ label: seed.repo })
  await page.getByLabel(/workflow/i).selectOption({ label: seed.workflow })
  await page.getByLabel(/title/i).fill('Live timeline')
  await page.getByLabel(/instructions/i).fill('Write the change.')
  await page.getByRole('button', { name: /create/i }).click()

  // Landing on the run is the form's own contract, and it is what makes the
  // next assertion about a LIVE timeline rather than about a fresh page load.
  await expect(page.getByTestId('phase-timeline')).toBeVisible()

  // ADVANCES WITHOUT A RELOAD. The dot the assertion watches starts not-started
  // and must reach succeeded on this same DOM — asserted on the state
  // attribute, because the whole content of a dot is colour.
  await expect(page.getByTestId('phase-impl')).toHaveAttribute('data-state', 'succeeded', {
    timeout: 60_000,
  })
  await expect(page.getByTestId('phase-gate')).toHaveAttribute('data-state', 'waiting_decision', {
    timeout: 60_000,
  })
})
```

- [ ] **Step 4: Path 3 — request changes opens a new generation**

Appended to `gate-decision.spec.ts` as its own `test`:

```ts
test('request_changes with a note opens a new generation, visible on the timeline', async ({
  page,
  request,
}) => {
  await signIn(page)
  expect((await request.post(`${CONTROL}/fixture/open-gate`)).status()).toBe(202)
  await page.getByTestId('approvals-badge').waitFor()
  await page.getByTestId('gate-card').getByRole('link').first().click()

  await page.getByRole('button', { name: /request changes/i }).click()
  // The note is REQUIRED and the requirement is part of the path: gates.go
  // feeds it to the revision target as a context file, and a revision with no
  // note re-runs the same work and reaches the same patch.
  await page.getByRole('button', { name: /send/i }).click()
  await expect(page.getByRole('alert')).toContainText(/note/i)
  await page.getByLabel(/note/i).fill('Use the existing helper instead.')
  await page.getByRole('button', { name: /send/i }).click()

  // The DOM says a new round opened...
  await expect(page.getByTestId('run-header')).toContainText(/generation 2/i, { timeout: 60_000 })
  // ...and the BACKEND agrees. Asserting only the DOM would pass against a
  // screen that re-rendered generation 1 with a new label.
  const runId = new URL(page.url()).pathname.split('/').pop()
  const state = await (await request.get(`${CONTROL}/fixture/run/${runId}`)).json()
  expect(state.generation).toBe(2)
})
```

- [ ] **Step 5: Run the lane**

```bash
cd web
./node_modules/.bin/playwright install --with-deps chromium   # Linux; macOS drops --with-deps
../scripts/with-node ./node_modules/.bin/vite build
cd .. && task web:e2e
```

Expected: PASS, three specs plus Plan 11's. **Run it with `-v` at least once**: `go test` hides a passing subprocess's output, and the first green run says nothing about whether Playwright ran at all.

```bash
cd backend && go test -tags 'webe2e integration' -v -run TestWebE2E ./internal/app/ 2>&1 | grep -E 'passed|failed|✓'
```

- [ ] **Step 6: Prove the specs can fail**

Each of the three, individually — a Playwright spec is the easiest kind of test to write vacuously:

```bash
# Path 1: make the bridge ignore gate.waiting.
#   Expect: the badge never reaches '1' and the spec times out at beat 3.
# Path 2: make PhaseDots render every dot as not-started.
#   Expect: the data-state assertion times out.
# Path 3: make request_changes post without the note.
#   Expect: the 400 surfaces and generation stays 1 — the backend oracle is
#   what catches it, and that is why it is there.
```

Record the timeout message for each. A spec that passes with its subject broken is worse than no spec.

- [ ] **Step 7: Gate and commit**

```bash
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" "$(git branch --show-current)"
git add web backend
git commit -m "test(web): the three mobile-critical paths, against the real binary"
```

---

### Task 18: The operator checkpoint

**Wave 1 ends at an operator assessment, and the assessment is a deliverable, not a formality: the build must be deployed and reachable from the operator's own phone against their own data.** A checkpoint conducted against mocked data cannot produce the taste feedback it exists for.

This task is sofo's, at a keyboard and then at a phone. Everything before it is preparation for it.

**Files:**
- Modify: `docs/superpowers/ledgers/2026-08-16-caesar-12a-web-critical-path.md` (create it as the wave record if it does not exist yet)
- Modify: `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md` — row 12a

- [ ] **Step 1: Deploy the tested digest**

Use the release path Plan 11 built and measured: build once, test **that** digest with `image-serves-ui`, promote **that** digest. Do not build a second image for the deploy.

```bash
gh run list --branch <branch> --limit 5
# read the publish job log for the digest that image-serves-ui tested
```

Record the digest in the ledger. `gh api user/packages/...` needs a `read:packages` scope the local token lacks — read the publish job log rather than trying to list GHCR versions.

- [ ] **Step 2: Reach it from the phone**

Against real data, on the real device. Then walk, in order, and time each:

1. Create a task from the FAB.
2. Watch its run advance without touching the screen.
3. Open the waiting gate from Home.
4. Read the examined artifact.
5. Request changes with a note; confirm the new generation appears.
6. Approve the next gate.
7. Open the PR from the publish phase.

- [ ] **Step 3: Answer the questions the checkpoint exists for**

Write the answers into the ledger. Each of these is a decision the plan deliberately deferred to evidence:

- **Does Home's inline approve get used, or is the run opened first every time?** (§10.2, §11.) It ships because it is locked and because this is the right instrument for the question. If the answer is "always opened first", the card's buttons come off **with evidence behind the removal**.
- **What should the sticky header offer?** (§10.3, §11.) Whole-task retry is a call, not a feature — `rerunTask` ships today. It stayed out so the checkpoint could decide the header as a whole; if the answer is "obviously retry", the cost of adding it was never the reason to wait.
- **Is `phase.` on the global stream too chatty in practice?** (§11.) A run in flight emits far more phase transitions than gate or publish events, and the bridge's debounce is what keeps that from becoming a refetch storm. Watch the network panel during step 2 and write down what you saw, not what you expect.
- **Do the empty states read as empty or as broken?** Particularly "Up next", which is empty most of the time by construction.
- **What is missing that the mockups promised?** §10's twelve entries are the known list. Anything else found here is a thirteenth, and it belongs in 12b's spec.

- [ ] **Step 4: Record the row**

Flip roadmap row 12a to done **only** if steps 1–3 completed on a real device against real data. If the deploy could not be reached from a phone, the row is not done and the ledger says which step blocked it.

Note in the ledger, explicitly, what 12a did **not** close:

- **Row 08 does not close here.** Paths 1 and 3 are the interface the checklist is walked on, not the event. Row 08 closes when every item of `backend/README.md`'s checklist is recorded against **both** forges.
- **Row 12b is unstarted**: Workflows outline editor, Repos, Admin, the Approvals history endpoint (§10.7), the task-board lifecycle problem (§10.8), and desktop's ⌘K and run-detail split view (§10.9).
- **Plan 11's one open operator item is still open** and unrelated: proving the `__Host-` cookie survives the Vite dev proxy, per the six steps in `web/vite.config.ts`.
- **Cancel is row 09's** and live agent output needs its own design pass (§10.4).

---

- [ ] **Step 5: Correct the documentation against what was actually built**

`web/README.md` gains the four screens, the `/more` tab, and the two things a next reader will otherwise get wrong: that `phase.` is now on the global attention stream (and what that costs), and that document schema 2 is what turns payload validation on.

**Then re-read this plan against the tree and correct it in place.** Every plan in this repo has ended with execution finding things review did not — Plan 11's ledger records thirty-five. Write them into the ledger as a numbered list, and fix the plan's claims where they turned out false; a plan left describing what was intended is the secondary source the next session will trust over the code. [[verify-handoffs-against-git]] is the failure this step exists to avoid.

---

## Self-review

Run against the spec with fresh eyes after the plan was complete. Three corrections were made inline and are recorded here rather than smoothed over.

### Spec coverage

| Spec | Where |
|---|---|
| §1 scope, two waves, done-means | wave headers; Task 18 |
| §1 deferred: cancel/retry, live logs | Task 16 (both stated and not built); Task 18 step 3 |
| §2.1 `GET /runs`, `RunSummary`, `phase_summary[]`, cursor, filters, cache topology | Task 4; Task 9 (the invalidation half) |
| §2.2 artifact payload, base64 contract, run-scoped grants | Task 3 |
| §2.3 payload schema, the producer migration, the compatibility rule | Task 5 (contract); Task 6 (enforcement + producers) |
| §2.4 examined artifact, summary verdict, `can_decide` + reason; the `schema.sql` comment | Task 2; Task 1 step 6 |
| §3.1 shell — five phone tabs | Task 8 |
| §3.2 Home | Task 13 |
| §3.3 Tasks, the seven→four mapping, the FAB | Task 14 |
| §3.4 run detail, publication, flat event log | Task 16 |
| §3.5 approval surface, hard facts, accordion, note requirement | Task 12 |
| §4 `phase.` transport; `run_id` routing; one stream | Task 7; Task 9 |
| §5 both 403 classes, both 409s, field errors | Task 11 |
| §6 the rehomed auth guard | Task 15 |
| §7 the four test levels and the three Playwright paths | Tasks 1–16 (per task); Task 17 |
| §8 dependencies and order | the order block |
| §9 what 12a does not touch | Task 18 step 4 |
| §10.1–§10.9 deviations | 10.1 Task 12 · 10.2 Tasks 1+13 · 10.3 Task 16 · 10.4 Task 16 · 10.5 Task 8 · 10.6 Tasks 12+13+14 · 10.7 Task 18 · 10.8 Task 14 · 10.9 Task 8 |
| §11 residuals | Task 18 steps 3–4 |

### Corrections the review forced

1. **`wall_deadline` was unreachable from Home.** §5 says a passed deadline is displayable *before* expiry because the deadline is on the run — but `ApprovalResponse` carries no run fields, and Home's cards are built from it. Fixed by putting `wall_deadline` on `GateEvidenceResponse` in Task 2, where the query already joins `runs`. Without it, Task 11's "says when the wall deadline has already passed" test had no data source and the deadline would only ever have surfaced as a 409.

2. **Home's card was going to open the wrong thing.** §3.2 says tapping the body opens the run's *expanded gate phase*; the first draft of Task 13 asserted only that it reached `/runs/…`, which on a phone is the top of a timeline and a scroll away from the screen the person was already looking at. Fixed with `?phase=<node_id>` as URL state in Tasks 13 and 16 — which the constitution wanted anyway, and which makes a gate under review linkable.

3. **The shared decision assertions were going to run three times under the wrong names.** §7 requires them on all three surfaces; the first draft exported them from `gate-controls.test.tsx`, and importing one test file from another re-registers its `describe` blocks so each host would have run the whole suite. Fixed with `decision-rules.testkit.ts`, which vitest does not collect.

### Risks carried knowingly

- **Task 2 is the largest single task in the plan** — a store change, a service resolution, two DTO surfaces and a decidability rule. It was not split because every half is unusable alone: the projection with no `can_decide` ships a screen that 403s, and `can_decide` with no evidence ships a decision surface with nothing to decide about. Its seven subtests and three mutation proofs are the compensation.
- **`phase.` on the global stream widens what every connected client receives**, and `phase.log_truncated` / `phase.egress_denied` are capped per attempt rather than rare. The bridge's debounce is now load-bearing rather than an optimization. Task 18 step 3 measures it rather than assuming.
- **The payload schema is a live risk to runs, not just to screens.** A schema stricter than a model reliably produces converts model variance into failed runs. Mitigations: the required sets are minimal, unknown keys pass, and the whole thing is off below document schema 2 — so the blast radius on deploy day is zero and grows only as workflows opt in.
- **Wave 0's fixtures are where this plan can go vacuous.** Three tasks depend on a fixture being *shaped* a particular way — a gate whose examined origin is not the newest artifact (Task 2), two runs sharing a `created_at` (Task 4), a two-file patch (Task 10) — and each would pass trivially on a simpler fixture. Each of those steps says to check the fixture rather than trust it; that instruction is the task, not a note on it.
- **`GET /tasks` and `GET /approvals` remain unpaginated.** Correct at month-one volume, a cliff later; named so the next person meets it deliberately.

---

## Execution handoff

Plan complete and saved to `docs/superpowers/plans/2026-08-16-caesar-12a-web-critical-path.md`. Two execution options:

**1. Subagent-Driven (recommended)** — a fresh subagent per task, review between tasks, fast iteration. Give every subagent an explicit dissent licence: on Plan 10 fifteen controller- and reviewer-authored items were wrong and the implementer was right every time.

**2. Inline Execution** — batch execution in-session with checkpoints.

Either way: **Task 15 immediately follows Task 14 with nothing between**, and **wave 0 closes with `contracts:check` green before Task 8 begins**.
