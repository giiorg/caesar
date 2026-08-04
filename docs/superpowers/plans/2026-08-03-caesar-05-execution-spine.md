# caesar Plan 05 — Execution Spine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The execution spine of the domain (spec §2): Tasks, Runs, and PhaseRun-as-queue (immutable attempt rows, claim CTE, durable leases, heartbeats, `reconciling`), commit-ordered RunEvents with the transactional counter row, the outbox lifecycle, two clocks with transactional deadline predicates, the minimal pinned PolicySnapshot, and status enums + transition tables for every spine entity — proven end-to-end by the golden-set seed: a run executes task → plan → implement → review → succeeded against a deterministic fake harness, a fake forge, and a local bare repo.

**Architecture:** `internal/runs` is the fourth bounded context (peer of `identity`, `assets`, `workflows`). Its pure core is the status vocabulary with transition tables, the typed outcome model, the minimal PolicySnapshot with budget arithmetic, and the execution-plan compiler (a runs-owned mirror of the definition document, compiled into topological order + per-node input bindings with origins resolved through pass-through gates). Around that core: a sqlc store over the new spine tables, the intake service (tasks, runs, supersede-before-work), the queue (single-statement claim CTE feeding a CAS `UPDATE`, leases, heartbeats, attempt fencing by row identity), the orchestrator (in-process worker pool + sweepers + reconciler), and HTTP handlers. Everything the spine needs from its peers arrives through five runs-owned ports — `DefinitionSource`, `RepoSource`, `HarnessSource`, `Forge`, `Executor` — wired only in `app`. No real containers in this plan: `runs.NotImplementedExecutor` is wired in production serve (a claimed phase dies fast with a typed permanent outcome), and the deterministic fakes in `internal/runs/fake` drive every integration test and the golden-set seed.

**Tech Stack:** Everything from Plans 01–04. **No new Go dependencies.** The bare-repo fixture shells out to the `git` binary (present in dev and CI; used only from tests via the `fake` package).

## Decisions this plan bakes in

Flag these to sofo if any look wrong; they are interpretations where the spec left latitude:

1. **One bounded context for the whole spine.** Task, Run, PhaseRun, Artifact, RunEvent, and the outbox are one aggregate cluster with one transaction boundary (`internal/runs`); splitting tasks from runs would force cross-context transactions, which the arch law bans. PhaseRun **is** the queue — no separate jobs table (spec §2 "deliberately absent").
2. **Intake rejects gates and publishing until Plan 08.** A definition containing a `gate` or `git-publish` node is rejected at task creation with a typed validation error ("gates and publishing arrive in Plan 08"). Stubs never fake success (decisions §6.13): refusing loudly at the door beats letting a run strand mid-DAG. The run/phase status enums still declare the `waiting` states Plan 08 will need — transition tables are data, and appending decision states later is additive. The compiler itself handles gates generically (pass-through origins) so Plan 08 extends, not rewrites.
3. **`failed` attempt rows always have a successor.** An attempt that fails retryably is CAS'd to `failed` in the **same transaction** that inserts the next attempt's `pending` row (spec §2); a failure with no retry (permanent, policy, exhausted) is `dead`. Consequence: the **latest** attempt row is never `failed`, node state derivation stays trivial, and `running → pending` is unrepresentable.
4. **Task status is not stored.** It is the status of the task's newest run, projected at read time (spec §2: "status derived from its runs"). Re-run (`POST /tasks/{id}/runs`) supersedes only a `pending` current run — supersede-before-work exactly (invariant 12); re-running a `running`/`waiting` run is a 409 (cancel arrives with the kill switch, Plan 09). Re-runs re-resolve the head SHA and re-evaluate the snapshot.
5. **Minimal PolicySnapshot** = `{schema: 1, max_attempts, execution_budget_ms, wall_clock_ms}`, evaluated from injected `runs.Params` (app wires `runs.DefaultSnapshot()`; versioned baselines arrive in Plan 09). The snapshot is pinned as jsonb on the run **and** denormalized into `execution_budget_ms` / `wall_deadline` columns so every deciding SQL statement carries the deadline predicate against the **database clock** (spec §3 Budgets). `max_attempts` is copied onto each attempt row at insertion so the claim `WHERE` can re-guard without a join into jsonb.
6. **Commit-ordered event ids via one counter row** (`run_event_counter`, single row, `SELECT … FOR UPDATE` in the mutation tx — spec §2 accepts the serialization cost). One outbox envelope row per event, same tx. Outbox lifecycle = retention sweep (7 days pinned); no consumer in V1.
7. **Fakes are first-class non-test packages** (`internal/runs/fake`): a scripted deterministic executor (directives embedded in node instructions), a map-backed forge, and a bare-repo fixture (exec `git`). Production serve wires `runs.NotImplementedExecutor` until Plan 06 — honest typed failure, never fake success. The golden-set seed and the replay path stay independent of observability (invariant 14).
8. **Artifact lineage `parent_ids` is a jsonb array of uuid strings** (sqlc's `uuid[]` mapping friction isn't worth a child table); the patch payload shape is pinned `{"diff", "base_sha", "tree_hash"}`. `log_ref`/`failure_snapshot` are declared in the type CHECK (runtime outputs, spec §2) but first produced in Plan 06.
9. **Harness references resolve at intake** (Plan 04 decision 5 completes): each agentic node's `harness` name must resolve against the registry via new `assets.GetHarnessByName`; an unknown name is a field-level validation error on task creation. The resolved ids ride into every ExecutionRequest.
10. **RBAC:** create task / re-run = `run` grant on the repo's project (spec §6: launch requires `run`); every read = `view` (403 `insufficient project grant` on direct gets, grant-filtered lists — the assets pattern verbatim). No admin-only spine endpoints.
11. **Events are run-scoped only** (`run.*` / `phase.*` kinds); task creation is audited, not evented — `run.created` carries the task context. Orchestrator-originated events stamp the attempt identity string as their correlation id.
12. **`project_id` is denormalized onto `tasks` and `run_events`** — streams are authorization-scoped by project (spec §4); Plan 10's tailer filters `run_events` by grant without joins.

## Global Constraints

Everything in Plans 01–04's Global Constraints still applies verbatim (module path `github.com/giiorg/caesar/backend`, layout law + arch tests, dependency deny-list, strict JSON config, stdlib asserts + go-cmp, slog JSON, TDD + table-driven + behavioral names, Taskfile parity, generated code never hand-edited, property tag mechanics, commit style — one short sentence, **no AI attribution of any kind**). New pins for this plan:

- **Execution identity** (spec §2): `AttemptRef{RunID, NodeID, Attempt}`; its `String()` is `caesar-<run>-<node>-<attempt>` and names containers (Plan 06), secret scopes, artifact keys, event correlation.

- **Run status enum + transitions** (terminal: `succeeded`, `failed`, `superseded`, `dead`):

  | from | to | trigger |
  |---|---|---|
  | `pending` | `running` | orchestrator startup tick (roots materialize in the same tx) |
  | `pending` | `superseded` | re-run intake (supersede-before-work) |
  | `pending` | `dead` | wall-deadline sweeper |
  | `running` | `waiting` | *declared for Plan 08* (all frontier phases await decisions) |
  | `waiting` | `running` | *declared for Plan 08* (decision recorded) |
  | `running` | `succeeded` | every node's latest attempt succeeded |
  | `running` | `failed` | a node died (permanent/exhausted) or budget breached |
  | `running` | `dead` | wall-deadline sweeper |
  | `waiting` | `dead` | wall-deadline sweeper (*declared, reachable in Plan 08*) |

  `failed` = work was tried and definitively failed (node dead, budget exhausted). `dead` = terminated without conclusion (wall clock, later: kill switch). Nothing transitions **into** `pending`.

- **PhaseRun (attempt row) status enum + transitions** (terminal: `succeeded`, `failed`, `dead`, `superseded`):

  | from | to | trigger |
  |---|---|---|
  | `pending` | `running` | claim CTE (lease taken) |
  | `pending` | `superseded` | run terminalized |
  | `running` | `succeeded` | worker terminal tx |
  | `running` | `failed` | retryable outcome — **successor `pending` row inserted in the same tx** |
  | `running` | `dead` | permanent / policy / attempts exhausted |
  | `running` | `reconciling` | lease-expiry sweeper |
  | `running` | `superseded` | run terminalized (stale worker fenced by row status) |
  | `reconciling` | `succeeded` | reconciler adopts a completed execution idempotently |
  | `reconciling` | `failed` | kill-then-inspect concluded retryable — successor same tx |
  | `reconciling` | `dead` | permanent / exhausted |
  | `reconciling` | `superseded` | run terminalized |

  Structural invariants: `running → pending` does not exist; at most one non-terminal row per `(run, node)` (partial unique index); one row per `(run, node, attempt)` (unique index); a `failed` row always has a successor row; stale workers fence by row identity — attempt N's CAS carries `WHERE id = $attemptRowID AND status = 'running' AND worker_id = $me` and can never touch attempt N+1's row.

- **Typed outcome model** (spec §2: outcome errors are typed; unclassified → permanent):

  | class | retry? | pinned codes |
  |---|---|---|
  | `transient` | yes (until attempts exhaust) | `harness_error`, `timeout`, `reconcile_lost` |
  | `permanent` | never | `not_implemented`, `internal_error`, `invalid_output` |
  | `policy_violation` | never | *(codes arrive with Plan 09)* |

  `context.DeadlineExceeded` from an executor classifies `transient`/`timeout`. Run-level terminal outcomes reuse the struct with codes `attempts_exhausted`, `budget_exhausted`, `wall_deadline_exceeded`.

- **Minimal PolicySnapshot defaults** (pinned; injected via `runs.Params`, overridable only in tests until Plan 09): `max_attempts` **3** (bounds 1–10), `execution_budget_ms` **2h**, `wall_clock_ms` **24h**. Two clocks (invariant 11): the wall deadline (`created_at + wall_clock`) never pauses; execution usage accumulates only over attempt execution time (claim → terminal), so gate waits in Plan 08 cost nothing by construction.

- **Transactional deadline predicates** (spec §3 Budgets): the claim statement's `WHERE` carries `r.status = 'running' AND r.wall_deadline > now() AND r.execution_used_ms < r.execution_budget_ms` evaluated by the **database clock**; the sweepers are backstops, never the mechanism that loses a race.

- **RunEvent vocabulary** (kind → payload keys; every payload also implies the row columns `run_id`, `project_id`, `occurred_at`, `correlation_id`):

  | kind | payload |
  |---|---|
  | `run.created` | `task_id`, `workflow_version_id`, `definition_hash`, `base_sha` |
  | `run.started` | — |
  | `run.superseded` | `by_run_id` |
  | `run.succeeded` | — |
  | `run.failed` | `outcome` |
  | `run.dead` | `outcome` |
  | `phase.materialized` | `node_id`, `attempt` |
  | `phase.started` | `node_id`, `attempt`, `worker_id` |
  | `phase.reconciling` | `node_id`, `attempt` |
  | `phase.retried` | `node_id`, `attempt`, `next_attempt`, `outcome` |
  | `phase.succeeded` | `node_id`, `attempt`, `artifact_ids` |
  | `phase.dead` | `node_id`, `attempt`, `outcome` |

  Event ids come from the counter row; ordering by id **is** commit order. One outbox row per event, same tx; outbox retention 7 days.

- **Config additions** (all fields read in serve — no dead config):

  ```json
  "orchestrator": {
    "workers": 4,
    "poll_interval": "500ms",
    "lease_ttl": "30s",
    "heartbeat_interval": "10s",
    "outbox_retention": "168h"
  }
  ```

  Validation: `workers` 1–64, all durations positive, `heartbeat_interval < lease_ttl`.

- **Audit vocabulary bump:** `task.created` (actor = requester), `run.created` (requester), `run.superseded` (requester), `run.finished` (actor **NULL** — system; detail carries final status + outcome).

- **API surface** (all under the Plan 02 middleware; problem envelope on every non-2xx):

  | operation | pattern | authz | success |
  |---|---|---|---|
  | `createTask` | `POST /api/v1/tasks` | `run` on the repo's project | 201 |
  | `listTasks` | `GET /api/v1/tasks` | authenticated; grant-filtered | 200 |
  | `getTask` | `GET /api/v1/tasks/{id}` | `view` | 200 |
  | `rerunTask` | `POST /api/v1/tasks/{id}/runs` | `run` | 201 (409 while running/waiting) |
  | `listTaskRuns` | `GET /api/v1/tasks/{id}/runs` | `view` | 200 |
  | `getRun` | `GET /api/v1/runs/{id}` | `view` | 200 (phases + artifact meta inline) |
  | `listRunEvents` | `GET /api/v1/runs/{id}/events?after=N` | `view` | 200 (ascending by id, page cap 500) |

- **Intake validation pins:** `title` 1–200 bytes · `inputs` optional JSON object ≤ 8 KiB (stored verbatim, passed to every ExecutionRequest) · `workflow_version` ≥ 1 · repo, workflow version, and every node harness must resolve · definitions containing `gate`/`git-publish` rejected (decision 2).

- All `go` / `task` commands run from the repo root unless the step says otherwise. Integration tests: `cd backend && go test -race -tags integration -count=1 ./...` (Docker required). Property lane: `task backend:test:property`.

---
### Task 1: Status vocabulary, transition tables, typed outcomes

The pure heart of the spine: run/phase status enums, their transition tables as data, and the typed outcome model with default-deny classification. Everything later CAS-checks against these tables.

**Files:**
- Create: `backend/internal/runs/status.go`
- Create: `backend/internal/runs/outcome.go`
- Create: `backend/internal/runs/status_test.go`
- Create: `backend/internal/runs/outcome_test.go`
- Create: `backend/internal/runs/status_property_test.go` (build tag `property`)

**Interfaces:**
- Consumes: `platform/xerrors` only.
- Produces (later tasks rely on these exact names):
  - `runs.RunStatus` (`RunPending, RunRunning, RunWaiting, RunSucceeded, RunFailed, RunSuperseded, RunDead`; methods `Valid()`, `Terminal()`, `CanTransition(to RunStatus) bool`)
  - `runs.PhaseStatus` (`PhasePending, PhaseRunning, PhaseReconciling, PhaseSucceeded, PhaseFailed, PhaseDead, PhaseSuperseded`; same three methods)
  - `runs.OutcomeClass` (`OutcomeTransient, OutcomePermanent, OutcomePolicyViolation`)
  - `runs.Outcome{Class OutcomeClass; Code, Message string}` with `Retryable() bool` and `Map() map[string]any`
  - `runs.OutcomeError{Outcome}` (implements `error`) and `runs.ClassifyOutcome(err error) Outcome`
  - Code constants: `CodeHarnessError, CodeTimeout, CodeReconcileLost, CodeNotImplemented, CodeInternalError, CodeInvalidOutput, CodeAttemptsExhausted, CodeBudgetExhausted, CodeWallDeadlineExceeded, CodeFenced, CodeSuperseded`

- [ ] **Step 1: Write the failing tests**

`backend/internal/runs/status_test.go`:

```go
package runs_test

import (
	"testing"

	"github.com/giiorg/caesar/backend/internal/runs"
)

func TestRunStatusTransitions(t *testing.T) {
	allowed := []struct{ from, to runs.RunStatus }{
		{runs.RunPending, runs.RunRunning},
		{runs.RunPending, runs.RunSuperseded},
		{runs.RunPending, runs.RunDead},
		{runs.RunRunning, runs.RunWaiting},
		{runs.RunWaiting, runs.RunRunning},
		{runs.RunRunning, runs.RunSucceeded},
		{runs.RunRunning, runs.RunFailed},
		{runs.RunRunning, runs.RunDead},
		{runs.RunWaiting, runs.RunDead},
	}
	for _, tc := range allowed {
		if !tc.from.CanTransition(tc.to) {
			t.Errorf("%s -> %s should be allowed", tc.from, tc.to)
		}
	}
	denied := []struct{ from, to runs.RunStatus }{
		{runs.RunRunning, runs.RunPending},
		{runs.RunSucceeded, runs.RunRunning},
		{runs.RunDead, runs.RunRunning},
		{runs.RunSuperseded, runs.RunRunning},
		{runs.RunPending, runs.RunSucceeded},
		{runs.RunPending, runs.RunWaiting},
	}
	for _, tc := range denied {
		if tc.from.CanTransition(tc.to) {
			t.Errorf("%s -> %s must be denied", tc.from, tc.to)
		}
	}
}

func TestRunStatusTerminality(t *testing.T) {
	terminal := []runs.RunStatus{runs.RunSucceeded, runs.RunFailed, runs.RunSuperseded, runs.RunDead}
	live := []runs.RunStatus{runs.RunPending, runs.RunRunning, runs.RunWaiting}
	for _, s := range terminal {
		if !s.Terminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
	for _, s := range live {
		if s.Terminal() {
			t.Errorf("%s should not be terminal", s)
		}
	}
}

func TestPhaseStatusTransitions(t *testing.T) {
	allowed := []struct{ from, to runs.PhaseStatus }{
		{runs.PhasePending, runs.PhaseRunning},
		{runs.PhasePending, runs.PhaseSuperseded},
		{runs.PhaseRunning, runs.PhaseSucceeded},
		{runs.PhaseRunning, runs.PhaseFailed},
		{runs.PhaseRunning, runs.PhaseDead},
		{runs.PhaseRunning, runs.PhaseReconciling},
		{runs.PhaseRunning, runs.PhaseSuperseded},
		{runs.PhaseReconciling, runs.PhaseSucceeded},
		{runs.PhaseReconciling, runs.PhaseFailed},
		{runs.PhaseReconciling, runs.PhaseDead},
		{runs.PhaseReconciling, runs.PhaseSuperseded},
	}
	for _, tc := range allowed {
		if !tc.from.CanTransition(tc.to) {
			t.Errorf("%s -> %s should be allowed", tc.from, tc.to)
		}
	}
	// The two structurally-forbidden moves the spec calls out by name.
	if runs.PhaseRunning.CanTransition(runs.PhasePending) {
		t.Error("running -> pending must be structurally impossible")
	}
	if runs.PhaseReconciling.CanTransition(runs.PhasePending) {
		t.Error("reconciling -> pending must be structurally impossible")
	}
}

func TestStatusValidity(t *testing.T) {
	if runs.RunStatus("bogus").Valid() || runs.PhaseStatus("bogus").Valid() {
		t.Error("unknown statuses must be invalid")
	}
	if !runs.RunWaiting.Valid() || !runs.PhaseReconciling.Valid() {
		t.Error("declared statuses must be valid")
	}
}
```

`backend/internal/runs/outcome_test.go`:

```go
package runs_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/giiorg/caesar/backend/internal/runs"
)

func TestClassifyOutcome(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want runs.Outcome
	}{
		{
			name: "typed outcome error passes through",
			err:  runs.OutcomeError{Outcome: runs.Outcome{Class: runs.OutcomeTransient, Code: runs.CodeHarnessError, Message: "boom"}},
			want: runs.Outcome{Class: runs.OutcomeTransient, Code: runs.CodeHarnessError, Message: "boom"},
		},
		{
			name: "wrapped outcome error passes through",
			err:  fmt.Errorf("execute: %w", runs.OutcomeError{Outcome: runs.Outcome{Class: runs.OutcomePermanent, Code: runs.CodeNotImplemented, Message: "no runner"}}),
			want: runs.Outcome{Class: runs.OutcomePermanent, Code: runs.CodeNotImplemented, Message: "no runner"},
		},
		{
			name: "context deadline is a transient timeout",
			err:  fmt.Errorf("execute: %w", context.DeadlineExceeded),
			want: runs.Outcome{Class: runs.OutcomeTransient, Code: runs.CodeTimeout, Message: "attempt deadline exceeded"},
		},
		{
			name: "unclassified is permanent (default-deny)",
			err:  errors.New("some surprise"),
			want: runs.Outcome{Class: runs.OutcomePermanent, Code: runs.CodeInternalError, Message: "some surprise"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runs.ClassifyOutcome(tc.err); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestOutcomeRetryable(t *testing.T) {
	if !(runs.Outcome{Class: runs.OutcomeTransient}).Retryable() {
		t.Error("transient must be retryable")
	}
	if (runs.Outcome{Class: runs.OutcomePermanent}).Retryable() || (runs.Outcome{Class: runs.OutcomePolicyViolation}).Retryable() {
		t.Error("permanent and policy_violation must not be retryable")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/runs/`
Expected: FAIL — package does not exist / undefined identifiers.

- [ ] **Step 3: Implement the vocabulary**

`backend/internal/runs/status.go`:

```go
// Package runs is the execution-spine bounded context (spec §2): Tasks,
// Runs, PhaseRun-as-queue with immutable attempt rows, artifacts,
// commit-ordered run events, and the outbox. Postgres is the sole
// dispatch authority; everything the spine needs from peer contexts
// arrives through the ports in ports.go, wired only in app.
package runs

// RunStatus is a run's lifecycle state. Transitions live in
// runTransitions; nothing ever transitions into pending, and waiting is
// declared for Plan 08's gate decisions.
type RunStatus string

const (
	RunPending    RunStatus = "pending"
	RunRunning    RunStatus = "running"
	RunWaiting    RunStatus = "waiting"
	RunSucceeded  RunStatus = "succeeded"
	RunFailed     RunStatus = "failed"
	RunSuperseded RunStatus = "superseded"
	RunDead       RunStatus = "dead"
)

// runTransitions is the run transition table (plan header). Data, not
// code: CAS sites and the property suite both read it.
var runTransitions = map[RunStatus][]RunStatus{
	RunPending: {RunRunning, RunSuperseded, RunDead},
	RunRunning: {RunWaiting, RunSucceeded, RunFailed, RunDead},
	RunWaiting: {RunRunning, RunDead},
}

func (s RunStatus) Valid() bool {
	switch s {
	case RunPending, RunRunning, RunWaiting, RunSucceeded, RunFailed, RunSuperseded, RunDead:
		return true
	}
	return false
}

func (s RunStatus) Terminal() bool {
	return s == RunSucceeded || s == RunFailed || s == RunSuperseded || s == RunDead
}

func (s RunStatus) CanTransition(to RunStatus) bool { return transitionIn(runTransitions[s], to) }

// PhaseStatus is one attempt row's state. A failed row always has a
// successor attempt row inserted in the same transaction (plan decision
// 3); running -> pending is unrepresentable.
type PhaseStatus string

const (
	PhasePending     PhaseStatus = "pending"
	PhaseRunning     PhaseStatus = "running"
	PhaseReconciling PhaseStatus = "reconciling"
	PhaseSucceeded   PhaseStatus = "succeeded"
	PhaseFailed      PhaseStatus = "failed"
	PhaseDead        PhaseStatus = "dead"
	PhaseSuperseded  PhaseStatus = "superseded"
)

var phaseTransitions = map[PhaseStatus][]PhaseStatus{
	PhasePending:     {PhaseRunning, PhaseSuperseded},
	PhaseRunning:     {PhaseSucceeded, PhaseFailed, PhaseDead, PhaseReconciling, PhaseSuperseded},
	PhaseReconciling: {PhaseSucceeded, PhaseFailed, PhaseDead, PhaseSuperseded},
}

func (s PhaseStatus) Valid() bool {
	switch s {
	case PhasePending, PhaseRunning, PhaseReconciling, PhaseSucceeded, PhaseFailed, PhaseDead, PhaseSuperseded:
		return true
	}
	return false
}

func (s PhaseStatus) Terminal() bool {
	return s == PhaseSucceeded || s == PhaseFailed || s == PhaseDead || s == PhaseSuperseded
}

func (s PhaseStatus) CanTransition(to PhaseStatus) bool {
	return transitionIn(phaseTransitions[s], to)
}

func transitionIn[T comparable](list []T, to T) bool {
	for _, v := range list {
		if v == to {
			return true
		}
	}
	return false
}
```

`backend/internal/runs/outcome.go`:

```go
package runs

import (
	"context"
	"errors"
)

// OutcomeClass is the typed outcome taxonomy (spec §2): transient
// retries until attempts exhaust; everything else is final. Unclassified
// errors are permanent — default-deny.
type OutcomeClass string

const (
	OutcomeTransient       OutcomeClass = "transient"
	OutcomePermanent       OutcomeClass = "permanent"
	OutcomePolicyViolation OutcomeClass = "policy_violation"
)

// Pinned outcome codes (plan header). Attempt-level codes come from
// classification; run-level codes are stamped by the orchestrator.
const (
	CodeHarnessError         = "harness_error"
	CodeTimeout              = "timeout"
	CodeReconcileLost        = "reconcile_lost"
	CodeNotImplemented       = "not_implemented"
	CodeInternalError        = "internal_error"
	CodeInvalidOutput        = "invalid_output"
	CodeAttemptsExhausted    = "attempts_exhausted"
	CodeBudgetExhausted      = "budget_exhausted"
	CodeWallDeadlineExceeded = "wall_deadline_exceeded"
	CodeFenced               = "fenced"
	CodeSuperseded           = "superseded"
)

// Outcome is a typed terminal result for an attempt or a run.
type Outcome struct {
	Class   OutcomeClass `json:"class"`
	Code    string       `json:"code"`
	Message string       `json:"message"`
}

func (o Outcome) Retryable() bool { return o.Class == OutcomeTransient }

// Map renders the outcome for event payloads and jsonb columns.
func (o Outcome) Map() map[string]any {
	return map[string]any{"class": string(o.Class), "code": o.Code, "message": o.Message}
}

// OutcomeError carries a typed outcome across the Executor port boundary.
type OutcomeError struct{ Outcome Outcome }

func (e OutcomeError) Error() string { return e.Outcome.Code + ": " + e.Outcome.Message }

// ClassifyOutcome maps an executor error to a typed outcome: typed
// errors pass through, a context deadline is a transient timeout, and
// anything unclassified is permanent (spec §2 default-deny).
func ClassifyOutcome(err error) Outcome {
	var oe OutcomeError
	if errors.As(err, &oe) {
		return oe.Outcome
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Outcome{Class: OutcomeTransient, Code: CodeTimeout, Message: "attempt deadline exceeded"}
	}
	return Outcome{Class: OutcomePermanent, Code: CodeInternalError, Message: err.Error()}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/runs/`
Expected: PASS

- [ ] **Step 5: Write the transition-legality property suite**

`backend/internal/runs/status_property_test.go`:

```go
//go:build property

package runs_test

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

var allRunStatuses = []runs.RunStatus{
	runs.RunPending, runs.RunRunning, runs.RunWaiting,
	runs.RunSucceeded, runs.RunFailed, runs.RunSuperseded, runs.RunDead,
}

var allPhaseStatuses = []runs.PhaseStatus{
	runs.PhasePending, runs.PhaseRunning, runs.PhaseReconciling,
	runs.PhaseSucceeded, runs.PhaseFailed, runs.PhaseDead, runs.PhaseSuperseded,
}

// Terminal states admit no transition at all, for both entities.
func TestPropertyTerminalStatesAreSinks(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		from := rapid.SampledFrom(allRunStatuses).Draw(t, "from")
		to := rapid.SampledFrom(allRunStatuses).Draw(t, "to")
		if from.Terminal() && from.CanTransition(to) {
			t.Fatalf("terminal run status %s transitions to %s", from, to)
		}
		pf := rapid.SampledFrom(allPhaseStatuses).Draw(t, "pf")
		pt := rapid.SampledFrom(allPhaseStatuses).Draw(t, "pt")
		if pf.Terminal() && pf.CanTransition(pt) {
			t.Fatalf("terminal phase status %s transitions to %s", pf, pt)
		}
	})
}

// No random walk through the declared transitions ever re-enters
// pending, and self-transitions never exist (CAS legality, spec §7).
func TestPropertyWalksNeverResurrectPending(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cur := runs.PhasePending
		steps := rapid.IntRange(1, 20).Draw(t, "steps")
		for i := 0; i < steps; i++ {
			next := rapid.SampledFrom(allPhaseStatuses).Draw(t, "next")
			if !cur.CanTransition(next) {
				continue
			}
			if next == runs.PhasePending {
				t.Fatalf("%s -> pending is representable", cur)
			}
			if next == cur {
				t.Fatalf("self-transition %s -> %s", cur, next)
			}
			cur = next
		}
	})
}
```

- [ ] **Step 6: Run the property suite (pinned seed) and the full checks**

Run: `task backend:test:property && task backend:ci`
Expected: PASS (arch tests see the new context; no lint findings)

- [ ] **Step 7: Commit**

```bash
git add backend/internal/runs
git commit -m "feat(backend): execution-spine status vocabulary and typed outcomes"
```

### Task 2: Minimal PolicySnapshot and budget arithmetic

The pinned snapshot struct every run carries (spec §2), its defaults, and the pure budget arithmetic the orchestrator and the property suite share (spec §7: "budget arithmetic" is a named rapid target).

**Files:**
- Create: `backend/internal/runs/policy.go`
- Create: `backend/internal/runs/policy_test.go`
- Create: `backend/internal/runs/policy_property_test.go` (build tag `property`)

**Interfaces:**
- Consumes: stdlib only.
- Produces:
  - `runs.PolicySnapshot{SchemaVersion int; MaxAttempts int32; ExecutionBudgetMS int64; WallClockMS int64}` (json tags `schema`, `max_attempts`, `execution_budget_ms`, `wall_clock_ms`)
  - `runs.DefaultSnapshot() PolicySnapshot` — `{1, 3, 2h, 24h}`
  - `(PolicySnapshot).Validate() error`
  - `runs.RemainingBudgetMS(budgetMS, usedMS int64) int64` (clamped at 0)
  - `runs.AddUsageMS(usedMS, deltaMS int64) int64` (negative deltas ignored; saturates instead of overflowing)

- [ ] **Step 1: Write the failing tests**

`backend/internal/runs/policy_test.go`:

```go
package runs_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/giiorg/caesar/backend/internal/runs"
)

func TestDefaultSnapshotIsPinned(t *testing.T) {
	want := runs.PolicySnapshot{SchemaVersion: 1, MaxAttempts: 3, ExecutionBudgetMS: 2 * 60 * 60 * 1000, WallClockMS: 24 * 60 * 60 * 1000}
	if diff := cmp.Diff(want, runs.DefaultSnapshot()); diff != "" {
		t.Errorf("default snapshot mismatch (-want +got):\n%s", diff)
	}
	if err := runs.DefaultSnapshot().Validate(); err != nil {
		t.Errorf("default snapshot must validate: %v", err)
	}
}

func TestSnapshotJSONShape(t *testing.T) {
	raw, err := json.Marshal(runs.DefaultSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema":1,"max_attempts":3,"execution_budget_ms":7200000,"wall_clock_ms":86400000}`
	if string(raw) != want {
		t.Errorf("snapshot json = %s, want %s", raw, want)
	}
}

func TestSnapshotValidation(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*runs.PolicySnapshot)
	}{
		{"zero attempts", func(s *runs.PolicySnapshot) { s.MaxAttempts = 0 }},
		{"attempts over cap", func(s *runs.PolicySnapshot) { s.MaxAttempts = 11 }},
		{"zero budget", func(s *runs.PolicySnapshot) { s.ExecutionBudgetMS = 0 }},
		{"zero wall clock", func(s *runs.PolicySnapshot) { s.WallClockMS = 0 }},
		{"wrong schema", func(s *runs.PolicySnapshot) { s.SchemaVersion = 2 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := runs.DefaultSnapshot()
			tc.mut(&s)
			if s.Validate() == nil {
				t.Error("want validation error")
			}
		})
	}
}

func TestBudgetArithmetic(t *testing.T) {
	if got := runs.RemainingBudgetMS(100, 40); got != 60 {
		t.Errorf("remaining = %d, want 60", got)
	}
	if got := runs.RemainingBudgetMS(100, 140); got != 0 {
		t.Errorf("overspent remaining = %d, want 0", got)
	}
	if got := runs.AddUsageMS(50, -10); got != 50 {
		t.Errorf("negative delta must be ignored, got %d", got)
	}
	if got := runs.AddUsageMS(math.MaxInt64-5, 10); got != math.MaxInt64 {
		t.Errorf("usage must saturate, got %d", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/runs/ -run 'Snapshot|Budget'`
Expected: FAIL — undefined identifiers.

- [ ] **Step 3: Implement**

`backend/internal/runs/policy.go`:

```go
package runs

import (
	"errors"
	"fmt"
	"math"
)

// snapshotSchemaVersion is the only snapshot shape this binary writes or
// understands. Plan 09's policy engine bumps it.
const snapshotSchemaVersion = 1

// maxAttemptsCap bounds retry storms regardless of future baselines.
const maxAttemptsCap = 10

// PolicySnapshot is the minimal pinned policy a run carries (spec §2).
// It is evaluated once at intake and immutable thereafter; Plan 09
// replaces the evaluation, never the pinning.
type PolicySnapshot struct {
	SchemaVersion     int   `json:"schema"`
	MaxAttempts       int32 `json:"max_attempts"`
	ExecutionBudgetMS int64 `json:"execution_budget_ms"`
	WallClockMS       int64 `json:"wall_clock_ms"`
}

// DefaultSnapshot is the V1 shipped baseline (plan header pins).
func DefaultSnapshot() PolicySnapshot {
	return PolicySnapshot{
		SchemaVersion:     snapshotSchemaVersion,
		MaxAttempts:       3,
		ExecutionBudgetMS: 2 * 60 * 60 * 1000,
		WallClockMS:       24 * 60 * 60 * 1000,
	}
}

func (s PolicySnapshot) Validate() error {
	var problems []error
	if s.SchemaVersion != snapshotSchemaVersion {
		problems = append(problems, fmt.Errorf("schema must be %d", snapshotSchemaVersion))
	}
	if s.MaxAttempts < 1 || s.MaxAttempts > maxAttemptsCap {
		problems = append(problems, fmt.Errorf("max_attempts must be 1-%d", maxAttemptsCap))
	}
	if s.ExecutionBudgetMS <= 0 {
		problems = append(problems, errors.New("execution_budget_ms must be positive"))
	}
	if s.WallClockMS <= 0 {
		problems = append(problems, errors.New("wall_clock_ms must be positive"))
	}
	return errors.Join(problems...)
}

// RemainingBudgetMS never goes negative: an overspent run reports zero.
func RemainingBudgetMS(budgetMS, usedMS int64) int64 {
	if usedMS >= budgetMS {
		return 0
	}
	return budgetMS - usedMS
}

// AddUsageMS accumulates observed execution time: negative deltas are
// clock skew, ignored; the sum saturates instead of wrapping.
func AddUsageMS(usedMS, deltaMS int64) int64 {
	if deltaMS <= 0 {
		return usedMS
	}
	if usedMS > math.MaxInt64-deltaMS {
		return math.MaxInt64
	}
	return usedMS + deltaMS
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/runs/ -run 'Snapshot|Budget'`
Expected: PASS

- [ ] **Step 5: Write the budget property suite**

`backend/internal/runs/policy_property_test.go`:

```go
//go:build property

package runs_test

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// Accumulating arbitrary attempt durations keeps the budget identity:
// remaining is never negative, never grows, and remaining+used == budget
// exactly until the budget is breached (spec §7 budget arithmetic).
func TestPropertyBudgetAccounting(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		budget := rapid.Int64Range(1, 1_000_000).Draw(t, "budget")
		used := int64(0)
		prevRemaining := runs.RemainingBudgetMS(budget, used)
		steps := rapid.IntRange(1, 50).Draw(t, "steps")
		for i := 0; i < steps; i++ {
			delta := rapid.Int64Range(-100, 100_000).Draw(t, "delta")
			used = runs.AddUsageMS(used, delta)
			remaining := runs.RemainingBudgetMS(budget, used)
			if remaining < 0 {
				t.Fatalf("remaining went negative: %d", remaining)
			}
			if remaining > prevRemaining {
				t.Fatalf("remaining grew: %d -> %d", prevRemaining, remaining)
			}
			if used <= budget && remaining+used != budget {
				t.Fatalf("identity broken: %d + %d != %d", remaining, used, budget)
			}
			prevRemaining = remaining
		}
	})
}
```

- [ ] **Step 6: Run the property lane and full gate**

Run: `task backend:test:property && task backend:ci`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add backend/internal/runs
git commit -m "feat(backend): minimal policy snapshot with budget arithmetic"
```
### Task 3: Ports and execution-plan compilation

The runs-owned mirror of a definition document plus its compilation into an executable plan: topological order, per-node input bindings with **origins** resolved through pass-through gates, root/readiness helpers. The five ports the spine consumes are declared here too — the compiler is their first consumer. The mirror is deliberately minimal: the write door (Plan 04) fully validated the document; compilation re-checks only what execution relies on, and any impossibility is an `internal_error` (a stored version that fails compilation is corruption, not user input).

**Files:**
- Create: `backend/internal/runs/ports.go`
- Create: `backend/internal/runs/compile.go`
- Create: `backend/internal/runs/compile_test.go`

**Interfaces:**
- Consumes: Task 1's vocabulary, `platform/xerrors`, `github.com/google/uuid`.
- Produces:
  - Node/artifact kind strings (runs-side, matching the Plan 04 document values verbatim): `KindPlan = "plan"`, `KindImplement = "implement"`, `KindReview = "review"`, `KindGate = "gate"`, `KindGitPublish = "git-publish"`; `ArtifactKindPlan = "plan"`, `ArtifactKindPatch = "patch"`, `ArtifactKindReviewVerdict = "review_verdict"`, `ArtifactKindLogRef = "log_ref"`, `ArtifactKindFailureSnapshot = "failure_snapshot"`
  - `runs.DefinitionDoc{Nodes []DefNode; Edges []DefEdge}` · `runs.DefNode{ID, Kind, Harness, Instructions string}` · `runs.DefEdge{From, To, Artifact string}`
  - `runs.PinnedDefinition{VersionID, WorkflowID uuid.UUID; Version int32; ContentHash string; Doc DefinitionDoc}`
  - `runs.RepoRef{ID, ProjectID, CredentialID uuid.UUID; Forge, Slug, DefaultBranch string}`
  - `runs.HarnessRef{ID uuid.UUID; Kind string}`
  - `runs.AttemptRef{RunID uuid.UUID; NodeID string; Attempt int32}` with `String() string` = `caesar-<run>-<node>-<attempt>`
  - Ports: `runs.DefinitionSource{ Definition(ctx, workflowID uuid.UUID, version int32) (PinnedDefinition, error); DefinitionByVersionID(ctx, versionID uuid.UUID) (PinnedDefinition, error) }` · `runs.RepoSource{ Repo(ctx, id uuid.UUID) (RepoRef, error) }` · `runs.HarnessSource{ HarnessByName(ctx, name string) (HarnessRef, error) }` · `runs.Forge{ ResolveHead(ctx, repo RepoRef) (string, error) }` · `runs.Executor{ Execute(ctx, req ExecutionRequest) (ExecutionResult, error); Inspect(ctx, ref AttemptRef) (InspectResult, error) }`
  - `runs.ExecutionRequest{Ref AttemptRef; Kind string; HarnessID uuid.UUID; Instructions string; Inputs []ArtifactInput; TaskInputs map[string]any; BaseSHA string; Deadline time.Time}`
  - `runs.ArtifactInput{ID uuid.UUID; Type string; Payload map[string]any}` · `runs.NewArtifact{Type string; Payload map[string]any}` · `runs.ExecutionResult{Artifacts []NewArtifact}` · `runs.InspectResult{Completed bool; Result ExecutionResult}`
  - `runs.NotImplementedExecutor{}` — both methods return `OutcomeError{permanent, not_implemented}` wrapping `xerrors.ErrNotImplemented`
  - `runs.Compile(doc DefinitionDoc) (ExecPlan, error)` · `runs.ExecPlan{Order []string; Nodes map[string]PlanNode}` with methods `Roots() []string`, `Ready(nodeID string, succeeded map[string]bool) bool`, `HasUnsupportedNodes() []string` (ids of gate/git-publish nodes, sorted)
  - `runs.PlanNode{ID, Kind, Harness, Instructions string; Inputs []PlanInput}` · `runs.PlanInput{Origin, From, Artifact string}` (`From` = direct edge source; `Origin` = producer resolved through gates)

- [ ] **Step 1: Write the failing tests**

`backend/internal/runs/compile_test.go`:

```go
package runs_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// conveyor is the five-node document from Plan 04's header, mirrored.
func conveyor() runs.DefinitionDoc {
	return runs.DefinitionDoc{
		Nodes: []runs.DefNode{
			{ID: "plan", Kind: runs.KindPlan, Harness: "cc", Instructions: "Plan the change."},
			{ID: "impl", Kind: runs.KindImplement, Harness: "cc", Instructions: "Implement the plan."},
			{ID: "review", Kind: runs.KindReview, Harness: "cc", Instructions: "Review the patch."},
			{ID: "gate", Kind: runs.KindGate},
			{ID: "publish", Kind: runs.KindGitPublish},
		},
		Edges: []runs.DefEdge{
			{From: "plan", To: "impl", Artifact: runs.ArtifactKindPlan},
			{From: "impl", To: "review", Artifact: runs.ArtifactKindPatch},
			{From: "impl", To: "gate", Artifact: runs.ArtifactKindPatch},
			{From: "review", To: "gate", Artifact: runs.ArtifactKindReviewVerdict},
			{From: "gate", To: "publish", Artifact: runs.ArtifactKindPatch},
		},
	}
}

func TestCompileOrdersAndBinds(t *testing.T) {
	plan, err := runs.Compile(conveyor())
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, id := range plan.Order {
		pos[id] = i
	}
	for _, pair := range [][2]string{{"plan", "impl"}, {"impl", "review"}, {"review", "gate"}, {"gate", "publish"}} {
		if pos[pair[0]] >= pos[pair[1]] {
			t.Errorf("%s must precede %s in %v", pair[0], pair[1], plan.Order)
		}
	}
	// The publish node's patch input originates at impl, THROUGH the gate.
	pub := plan.Nodes["publish"]
	want := []runs.PlanInput{{Origin: "impl", From: "gate", Artifact: runs.ArtifactKindPatch}}
	if diff := cmp.Diff(want, pub.Inputs); diff != "" {
		t.Errorf("publish inputs (-want +got):\n%s", diff)
	}
	if got := plan.HasUnsupportedNodes(); !cmp.Equal(got, []string{"gate", "publish"}) {
		t.Errorf("unsupported = %v, want [gate publish]", got)
	}
}

func TestCompileRootsAndReadiness(t *testing.T) {
	plan, err := runs.Compile(conveyor())
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Roots(); !cmp.Equal(got, []string{"plan"}) {
		t.Errorf("roots = %v, want [plan]", got)
	}
	succeeded := map[string]bool{"plan": true}
	if !plan.Ready("impl", succeeded) {
		t.Error("impl must be ready once plan succeeded")
	}
	if plan.Ready("review", succeeded) {
		t.Error("review must wait for impl")
	}
	// gate needs both impl (origin of its examined patch) and review.
	succeeded["impl"] = true
	if plan.Ready("gate", succeeded) {
		t.Error("gate must wait for review")
	}
	succeeded["review"] = true
	if !plan.Ready("gate", succeeded) {
		t.Error("gate must be ready once impl and review succeeded")
	}
}

func TestCompileRejectsCorruptDocuments(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*runs.DefinitionDoc)
		want string
	}{
		{"unknown node kind", func(d *runs.DefinitionDoc) { d.Nodes[0].Kind = "wat" }, "unknown node kind"},
		{"unknown artifact", func(d *runs.DefinitionDoc) { d.Edges[0].Artifact = "wat" }, "unknown artifact"},
		{"edge to nowhere", func(d *runs.DefinitionDoc) { d.Edges[0].To = "ghost" }, "unknown node"},
		{"duplicate node id", func(d *runs.DefinitionDoc) { d.Nodes[1].ID = "plan" }, "duplicate node"},
		{"empty document", func(d *runs.DefinitionDoc) { d.Nodes = nil; d.Edges = nil }, "no nodes"},
		{"cycle", func(d *runs.DefinitionDoc) {
			d.Edges = append(d.Edges, runs.DefEdge{From: "review", To: "impl", Artifact: runs.ArtifactKindPatch})
		}, "cycle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := conveyor()
			tc.mut(&doc)
			_, err := runs.Compile(doc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestAttemptRefString(t *testing.T) {
	ref := runs.AttemptRef{NodeID: "impl", Attempt: 2}
	if got := ref.String(); !strings.HasPrefix(got, "caesar-00000000-") || !strings.HasSuffix(got, "-impl-2") {
		t.Errorf("ref string = %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/runs/ -run 'Compile|AttemptRef'`
Expected: FAIL — undefined identifiers.

- [ ] **Step 3: Implement ports**

`backend/internal/runs/ports.go`:

```go
package runs

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// AttemptRef is the execution identity (spec §2): everything an attempt
// touches is named by it.
type AttemptRef struct {
	RunID   uuid.UUID
	NodeID  string
	Attempt int32
}

func (r AttemptRef) String() string {
	return fmt.Sprintf("caesar-%s-%s-%d", r.RunID, r.NodeID, r.Attempt)
}

// PinnedDefinition is what a run pins at intake: the version identity
// plus the runs-owned mirror of its document.
type PinnedDefinition struct {
	VersionID   uuid.UUID
	WorkflowID  uuid.UUID
	Version     int32
	ContentHash string
	Doc         DefinitionDoc
}

// RepoRef and HarnessRef are the spine's views of peer-context assets.
type RepoRef struct {
	ID            uuid.UUID
	ProjectID     uuid.UUID
	CredentialID  uuid.UUID
	Forge         string
	Slug          string
	DefaultBranch string
}

type HarnessRef struct {
	ID   uuid.UUID
	Kind string
}

// The five ports the spine consumes. Contexts never import each other:
// app implements these over workflows.Service / assets.Service and the
// fake package (spec §1 hexagonal boundaries).
type DefinitionSource interface {
	// Definition resolves (workflow, version) coordinates at task intake.
	Definition(ctx context.Context, workflowID uuid.UUID, version int32) (PinnedDefinition, error)
	// DefinitionByVersionID resolves the version a task already pinned
	// (re-runs and the orchestrator's plan loading).
	DefinitionByVersionID(ctx context.Context, versionID uuid.UUID) (PinnedDefinition, error)
}

type RepoSource interface {
	Repo(ctx context.Context, id uuid.UUID) (RepoRef, error)
}

type HarnessSource interface {
	HarnessByName(ctx context.Context, name string) (HarnessRef, error)
}

// Forge resolves a repo's default branch to the commit a run pins as its
// base SHA. Plan 08 grows this port into the publish state machine.
type Forge interface {
	ResolveHead(ctx context.Context, repo RepoRef) (string, error)
}

// Executor runs one phase attempt to a typed result. Plan 06's Docker
// runner implements it for real; fake.Executor scripts it; and
// NotImplementedExecutor is the honest production stub until then.
type Executor interface {
	Execute(ctx context.Context, req ExecutionRequest) (ExecutionResult, error)
	Inspect(ctx context.Context, ref AttemptRef) (InspectResult, error)
}

// ExecutionRequest is the spine's precursor of Plan 06's RunSpec:
// explicit inputs, never ambient state (spec §3 artifact flow).
type ExecutionRequest struct {
	Ref          AttemptRef
	Kind         string
	HarnessID    uuid.UUID
	Instructions string
	Inputs       []ArtifactInput
	TaskInputs   map[string]any
	BaseSHA      string
	Deadline     time.Time
}

type ArtifactInput struct {
	ID      uuid.UUID
	Type    string
	Payload map[string]any
}

// NewArtifact is an executor-produced work product; the orchestrator
// stamps identity and lineage when persisting.
type NewArtifact struct {
	Type    string
	Payload map[string]any
}

type ExecutionResult struct {
	Artifacts []NewArtifact
}

// InspectResult reports what the runtime knows about an attempt during
// reconciliation: a completed execution with intact outputs is adopted
// idempotently; anything else is kill-then-inspect (spec §3).
type InspectResult struct {
	Completed bool
	Result    ExecutionResult
}

// NotImplementedExecutor is wired in production serve until Plan 06: a
// claimed phase dies fast with a typed permanent outcome. Stubs never
// fake success (decisions §6.13).
type NotImplementedExecutor struct{}

func (NotImplementedExecutor) Execute(context.Context, ExecutionRequest) (ExecutionResult, error) {
	return ExecutionResult{}, OutcomeError{Outcome: Outcome{
		Class: OutcomePermanent, Code: CodeNotImplemented,
		Message: xerrors.NotImplemented("runs: executor (Plan 06)").Error(),
	}}
}

func (NotImplementedExecutor) Inspect(context.Context, AttemptRef) (InspectResult, error) {
	return InspectResult{}, OutcomeError{Outcome: Outcome{
		Class: OutcomePermanent, Code: CodeNotImplemented,
		Message: xerrors.NotImplemented("runs: executor inspect (Plan 06)").Error(),
	}}
}
```

- [ ] **Step 4: Implement compilation**

`backend/internal/runs/compile.go`:

```go
package runs

import (
	"fmt"
	"sort"
)

// Node and artifact kinds mirror the Plan 04 document values verbatim.
// The mirror is runs-owned: bounded contexts never import each other,
// and app's DefinitionSource adapter maps typed workflows values onto
// these strings mechanically.
const (
	KindPlan       = "plan"
	KindImplement  = "implement"
	KindReview     = "review"
	KindGate       = "gate"
	KindGitPublish = "git-publish"

	ArtifactKindPlan            = "plan"
	ArtifactKindPatch           = "patch"
	ArtifactKindReviewVerdict   = "review_verdict"
	ArtifactKindLogRef          = "log_ref"
	ArtifactKindFailureSnapshot = "failure_snapshot"
)

func validNodeKind(k string) bool {
	switch k {
	case KindPlan, KindImplement, KindReview, KindGate, KindGitPublish:
		return true
	}
	return false
}

func validEdgeArtifact(a string) bool {
	return a == ArtifactKindPlan || a == ArtifactKindPatch || a == ArtifactKindReviewVerdict
}

// DefinitionDoc is the runs-owned mirror of a stored definition.
type DefinitionDoc struct {
	Nodes []DefNode
	Edges []DefEdge
}

type DefNode struct {
	ID           string
	Kind         string
	Harness      string
	Instructions string
}

type DefEdge struct {
	From     string
	To       string
	Artifact string
}

// PlanInput is one resolved binding: the artifact type, the direct edge
// source, and the origin — the producing node resolved through
// pass-through gates (Plan 04 decision 2).
type PlanInput struct {
	Origin   string
	From     string
	Artifact string
}

type PlanNode struct {
	ID           string
	Kind         string
	Harness      string
	Instructions string
	Inputs       []PlanInput
}

// ExecPlan is a compiled definition: topological order plus per-node
// resolved inputs. It is pure data — the orchestrator interprets it.
type ExecPlan struct {
	Order []string
	Nodes map[string]PlanNode
}

// Compile validates the mirror just enough to execute it and resolves
// order and origins. A stored version that fails here is corruption:
// callers surface internal_error, never a user-facing validation error.
func Compile(doc DefinitionDoc) (ExecPlan, error) {
	if len(doc.Nodes) == 0 {
		return ExecPlan{}, fmt.Errorf("runs: compile: no nodes")
	}
	byID := make(map[string]DefNode, len(doc.Nodes))
	for _, n := range doc.Nodes {
		if _, dup := byID[n.ID]; dup {
			return ExecPlan{}, fmt.Errorf("runs: compile: duplicate node %q", n.ID)
		}
		if !validNodeKind(n.Kind) {
			return ExecPlan{}, fmt.Errorf("runs: compile: unknown node kind %q on %q", n.Kind, n.ID)
		}
		byID[n.ID] = n
	}
	in := map[string][]DefEdge{}
	out := map[string][]DefEdge{}
	for _, e := range doc.Edges {
		if !validEdgeArtifact(e.Artifact) {
			return ExecPlan{}, fmt.Errorf("runs: compile: unknown artifact %q on %s->%s", e.Artifact, e.From, e.To)
		}
		if _, ok := byID[e.From]; !ok {
			return ExecPlan{}, fmt.Errorf("runs: compile: unknown node %q in edge", e.From)
		}
		if _, ok := byID[e.To]; !ok {
			return ExecPlan{}, fmt.Errorf("runs: compile: unknown node %q in edge", e.To)
		}
		in[e.To] = append(in[e.To], e)
		out[e.From] = append(out[e.From], e)
	}

	order, err := topoOrder(doc.Nodes, in, out)
	if err != nil {
		return ExecPlan{}, err
	}

	nodes := make(map[string]PlanNode, len(doc.Nodes))
	for _, n := range doc.Nodes {
		pn := PlanNode{ID: n.ID, Kind: n.Kind, Harness: n.Harness, Instructions: n.Instructions}
		for _, e := range in[n.ID] {
			origin, err := resolveOrigin(byID, in, e)
			if err != nil {
				return ExecPlan{}, err
			}
			pn.Inputs = append(pn.Inputs, PlanInput{Origin: origin, From: e.From, Artifact: e.Artifact})
		}
		sort.Slice(pn.Inputs, func(i, j int) bool {
			a, b := pn.Inputs[i], pn.Inputs[j]
			if a.Origin != b.Origin {
				return a.Origin < b.Origin
			}
			return a.Artifact < b.Artifact
		})
		nodes[n.ID] = pn
	}
	return ExecPlan{Order: order, Nodes: nodes}, nil
}

// topoOrder is Kahn's algorithm with lexicographic tie-breaking — the
// same shape as the definitions validator, re-owned here because
// contexts never share code across the boundary.
func topoOrder(nodes []DefNode, in, out map[string][]DefEdge) ([]string, error) {
	deg := make(map[string]int, len(nodes))
	var ready []string
	for _, n := range nodes {
		deg[n.ID] = len(in[n.ID])
		if deg[n.ID] == 0 {
			ready = append(ready, n.ID)
		}
	}
	order := make([]string, 0, len(nodes))
	for len(ready) > 0 {
		sort.Strings(ready)
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		for _, e := range out[id] {
			deg[e.To]--
			if deg[e.To] == 0 {
				ready = append(ready, e.To)
			}
		}
	}
	if len(order) != len(nodes) {
		return nil, fmt.Errorf("runs: compile: cycle in stored definition")
	}
	return order, nil
}

// resolveOrigin walks an edge back through pass-through gates to the
// node that actually produced the artifact. Acyclicity bounds the walk;
// a gate missing its examined input is corruption.
func resolveOrigin(byID map[string]DefNode, in map[string][]DefEdge, e DefEdge) (string, error) {
	for byID[e.From].Kind == KindGate {
		found := false
		for _, ge := range in[e.From] {
			if ge.Artifact == ArtifactKindPlan || ge.Artifact == ArtifactKindPatch {
				e, found = ge, true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("runs: compile: gate %q has no examined input", e.From)
		}
	}
	return e.From, nil
}

// Roots are the nodes materialized when a run starts.
func (p ExecPlan) Roots() []string {
	var roots []string
	for id, n := range p.Nodes {
		if len(n.Inputs) == 0 {
			roots = append(roots, id)
		}
	}
	sort.Strings(roots)
	return roots
}

// Ready reports whether every input origin has succeeded. Readiness is
// evaluated at outcome time; pending rows exist only for ready nodes.
func (p ExecPlan) Ready(nodeID string, succeeded map[string]bool) bool {
	n, ok := p.Nodes[nodeID]
	if !ok {
		return false
	}
	for _, input := range n.Inputs {
		if !succeeded[input.Origin] {
			return false
		}
		if p.Nodes[input.From].Kind == KindGate && !succeeded[input.From] {
			return false
		}
	}
	return true
}

// HasUnsupportedNodes lists gate/git-publish node ids — Plan 05 intake
// rejects documents containing them (plan decision 2; Plan 08 deletes
// the call site).
func (p ExecPlan) HasUnsupportedNodes() []string {
	var ids []string
	for id, n := range p.Nodes {
		if n.Kind == KindGate || n.Kind == KindGitPublish {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd backend && go test ./internal/runs/`
Expected: PASS

- [ ] **Step 6: Full gate and commit**

Run: `task backend:ci`
Expected: PASS (stubfake analyzer accepts NotImplementedExecutor — it returns a typed error, never fake success)

```bash
git add backend/internal/runs
git commit -m "feat(backend): spine ports and execution-plan compilation"
```

### Task 4: Schema, migration, and the sqlc store

All spine tables land: `tasks`, `runs`, `phase_runs`, `artifacts`, `run_event_counter`, `run_events`, `outbox` — plus the two Plan 04 carry-forwards: `CHECK (version > 0)` on `workflow_versions` and the runs→versions RESTRICT chain, with `workflows.DeleteWorkflow` classifying the new FK conflict. One migration, one sqlc generation block, the drift gate grows.

**Files:**
- Modify: `backend/internal/platform/pg/schema.sql` (append the spine section; add the CHECK to `workflow_versions`)
- Create: `backend/internal/platform/pg/migrations/<stamp>_execution_spine.sql` (via `task db:new -- execution_spine`; Atlas stamps the name)
- Create: `backend/internal/runs/store/queries.sql`
- Create (generated): `backend/internal/runs/store/{db.go,models.go,queries.sql.go}`
- Modify: `backend/sqlc.yaml` (fifth generation block: `internal/runs/store`, same overrides as the others verbatim)
- Modify: `Taskfile.yml` (`contracts:check` diff path list adds `backend/internal/runs/store`)
- Modify: `backend/internal/workflows/workflows.go` (`DeleteWorkflow`: FK violation → conflict)
- Modify: `backend/internal/workflows/workflows_integration_test.go` (delete-with-runs test lands in Task 7 when tasks can exist; here: keep existing tests green)

**Interfaces:**
- Consumes: existing tables (`users`, `projects`, `repos`, `workflow_versions`).
- Produces: `store.Queries` with the queries below; later tasks call them by these exact sqlc names.

- [ ] **Step 1: Append the spine schema**

Append to `backend/internal/platform/pg/schema.sql`, and add `CHECK (version > 0)` to the `workflow_versions.version` column in place (`version integer NOT NULL CHECK (version > 0),`):

```sql
-- Execution spine (spec §2). Plan 05: PhaseRun IS the queue — immutable
-- attempt rows claimed FOR UPDATE SKIP LOCKED; no separate jobs table.
-- All FKs RESTRICT: spine rows are history, never cascade-deleted.

CREATE TABLE tasks (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id          uuid NOT NULL REFERENCES projects (id) ON DELETE RESTRICT,
    repo_id             uuid NOT NULL REFERENCES repos (id) ON DELETE RESTRICT,
    workflow_version_id uuid NOT NULL REFERENCES workflow_versions (id) ON DELETE RESTRICT,
    title               text NOT NULL,
    inputs              jsonb NOT NULL DEFAULT '{}'::jsonb,
    requester_id        uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at          timestamptz NOT NULL
);

CREATE INDEX tasks_project_id_idx ON tasks (project_id);

-- A run pins definition hash, policy snapshot (jsonb record), and the
-- denormalized budget/deadline columns every deciding statement
-- predicates on against the DATABASE clock (spec §3 Budgets).
CREATE TABLE runs (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    task_id             uuid NOT NULL REFERENCES tasks (id) ON DELETE RESTRICT,
    status              text NOT NULL CHECK (status IN ('pending', 'running', 'waiting', 'succeeded', 'failed', 'superseded', 'dead')),
    definition_hash     text NOT NULL,
    base_sha            text NOT NULL,
    policy_snapshot     jsonb NOT NULL,
    execution_budget_ms bigint NOT NULL CHECK (execution_budget_ms > 0),
    execution_used_ms   bigint NOT NULL DEFAULT 0 CHECK (execution_used_ms >= 0),
    wall_deadline       timestamptz NOT NULL,
    outcome             jsonb,
    created_at          timestamptz NOT NULL,
    started_at          timestamptz,
    finished_at         timestamptz
);

CREATE INDEX runs_task_id_idx ON runs (task_id);
-- Supersede-before-work: at most one live run per task.
CREATE UNIQUE INDEX runs_task_live_key ON runs (task_id)
    WHERE status IN ('pending', 'running', 'waiting');

-- Immutable attempt rows (spec §2): one row per (run, node, attempt); at
-- most one non-terminal row per (run, node); a retry is a NEW row
-- inserted in the same tx that terminalizes its predecessor.
CREATE TABLE phase_runs (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    run_id       uuid NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    node_id      text NOT NULL,
    attempt      integer NOT NULL CHECK (attempt >= 1),
    max_attempts integer NOT NULL CHECK (max_attempts >= 1),
    status       text NOT NULL CHECK (status IN ('pending', 'running', 'reconciling', 'succeeded', 'failed', 'dead', 'superseded')),
    worker_id    text,
    leased_until timestamptz,
    claimed_at   timestamptz,
    finished_at  timestamptz,
    outcome      jsonb,
    created_at   timestamptz NOT NULL,
    UNIQUE (run_id, node_id, attempt)
);

CREATE UNIQUE INDEX phase_runs_live_key ON phase_runs (run_id, node_id)
    WHERE status IN ('pending', 'running', 'reconciling');
CREATE INDEX phase_runs_claim_idx ON phase_runs (created_at) WHERE status = 'pending';
CREATE INDEX phase_runs_lease_idx ON phase_runs (leased_until) WHERE status = 'running';

-- Attempt-scoped durable work products with recorded lineage (spec §2).
-- parent_ids: jsonb array of artifact uuid strings (plan decision 8).
CREATE TABLE artifacts (
    id         uuid PRIMARY KEY,
    run_id     uuid NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    node_id    text NOT NULL,
    attempt    integer NOT NULL,
    type       text NOT NULL CHECK (type IN ('plan', 'patch', 'review_verdict', 'log_ref', 'failure_snapshot')),
    payload    jsonb NOT NULL,
    parent_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL
);

CREATE INDEX artifacts_run_id_idx ON artifacts (run_id);

-- Commit-ordered event ids (spec §2): allocated from this single row,
-- locked FOR UPDATE inside the mutation tx. Sequences allocate in
-- non-commit order and would let a tailer cursor skip a late commit;
-- the serialization cost is accepted at V1 scale.
CREATE TABLE run_event_counter (
    onerow  boolean PRIMARY KEY DEFAULT true CHECK (onerow),
    next_id bigint NOT NULL
);

CREATE TABLE run_events (
    id             bigint PRIMARY KEY,
    run_id         uuid NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    project_id     uuid NOT NULL,
    kind           text NOT NULL,
    payload        jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at    timestamptz NOT NULL,
    correlation_id text NOT NULL DEFAULT ''
);

CREATE INDEX run_events_run_id_idx ON run_events (run_id, id);
CREATE INDEX run_events_project_id_idx ON run_events (project_id, id);

-- Same-tx outbox envelope (spec §2): one row per run event, no consumer
-- in V1; the retention sweeper prunes by age — never an immortal table.
CREATE TABLE outbox (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id   bigint NOT NULL REFERENCES run_events (id) ON DELETE CASCADE,
    kind       text NOT NULL,
    payload    jsonb NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE INDEX outbox_created_at_idx ON outbox (created_at);
```

- [ ] **Step 2: Generate and finish the migration**

Run: `task db:new -- execution_spine`
Atlas diffs schema.sql into `backend/internal/platform/pg/migrations/<stamp>_execution_spine.sql`. **Append** the counter seed row to the generated file's up section (goose annotations preserved) and a matching delete in down:

```sql
INSERT INTO run_event_counter (onerow, next_id) VALUES (true, 1);
```

(The generated ALTER for `workflow_versions` CHECK and all CREATEs stay exactly as Atlas emits them; the down section must drop the spine tables and the CHECK. Verify the down path exists — the migration up-down integration test enforces it.)

- [ ] **Step 3: Run the migration tests**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/platform/pg/`
Expected: PASS (up-down-up cycles green, atlas.sum consistent — run `task db:sqlc` first if the harness requires regenerated models)

- [ ] **Step 4: Write the store queries**

`backend/internal/runs/store/queries.sql` (sqlc names are load-bearing — later tasks call these):

```sql
-- name: InsertTask :one
INSERT INTO tasks (project_id, repo_id, workflow_version_id, title, inputs, requester_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetTask :one
SELECT * FROM tasks WHERE id = $1;

-- name: ListTasks :many
SELECT sqlc.embed(t), r.id AS latest_run_id, r.status AS latest_run_status
FROM tasks t
JOIN LATERAL (
    SELECT id, status FROM runs WHERE task_id = t.id ORDER BY created_at DESC, id DESC LIMIT 1
) r ON true
ORDER BY t.created_at DESC;

-- name: ListTasksByProjects :many
SELECT sqlc.embed(t), r.id AS latest_run_id, r.status AS latest_run_status
FROM tasks t
JOIN LATERAL (
    SELECT id, status FROM runs WHERE task_id = t.id ORDER BY created_at DESC, id DESC LIMIT 1
) r ON true
WHERE t.project_id = ANY(sqlc.arg(project_ids)::uuid[])
ORDER BY t.created_at DESC;

-- name: InsertRun :one
INSERT INTO runs (task_id, status, definition_hash, base_sha, policy_snapshot,
                  execution_budget_ms, wall_deadline, created_at)
VALUES ($1, 'pending', $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetRun :one
SELECT * FROM runs WHERE id = $1;

-- name: ListRunsForTask :many
SELECT * FROM runs WHERE task_id = $1 ORDER BY created_at DESC, id DESC;

-- name: LatestRunForUpdate :one
SELECT * FROM runs WHERE task_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1 FOR UPDATE;

-- name: GetRunForUpdate :one
SELECT * FROM runs WHERE id = $1 FOR UPDATE;

-- name: ListPendingRunIDs :many
SELECT id FROM runs WHERE status = 'pending' ORDER BY created_at LIMIT $1;

-- name: CASRunStatus :execrows
UPDATE runs SET status = sqlc.arg(to_status)
WHERE id = $1 AND status = sqlc.arg(from_status);

-- name: StartRun :execrows
UPDATE runs SET status = 'running', started_at = $2
WHERE id = $1 AND status = 'pending';

-- name: FinishRun :execrows
UPDATE runs SET status = sqlc.arg(to_status), outcome = $2, finished_at = $3
WHERE id = $1 AND status IN ('pending', 'running', 'waiting');

-- name: AddRunUsage :one
UPDATE runs SET execution_used_ms = LEAST(execution_used_ms + sqlc.arg(delta_ms), 9223372036854775807)
WHERE id = $1
RETURNING execution_used_ms, execution_budget_ms;

-- name: ListExpiredRunIDs :many
SELECT id FROM runs
WHERE status IN ('pending', 'running', 'waiting') AND wall_deadline <= now()
LIMIT $1;

-- name: InsertPhase :one
INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, status, created_at)
VALUES ($1, $2, $3, $4, 'pending', $5)
RETURNING *;

-- name: ClaimPhase :one
WITH candidate AS (
    SELECT pr.id, r.wall_deadline, r.execution_budget_ms, r.execution_used_ms
    FROM phase_runs pr
    JOIN runs r ON r.id = pr.run_id
    WHERE pr.status = 'pending'
      AND pr.attempt <= pr.max_attempts
      AND r.status = 'running'
      AND r.wall_deadline > now()
      AND r.execution_used_ms < r.execution_budget_ms
    ORDER BY pr.created_at
    FOR UPDATE OF pr SKIP LOCKED
    LIMIT 1
)
UPDATE phase_runs
SET status = 'running', worker_id = $1,
    leased_until = now() + make_interval(secs => sqlc.arg(lease_seconds)::float8),
    claimed_at = $2
FROM candidate
WHERE phase_runs.id = candidate.id
RETURNING phase_runs.*, candidate.wall_deadline AS run_wall_deadline,
          candidate.execution_budget_ms, candidate.execution_used_ms;

-- name: HeartbeatPhase :execrows
UPDATE phase_runs SET leased_until = now() + make_interval(secs => sqlc.arg(lease_seconds)::float8)
WHERE id = $1 AND status = 'running' AND worker_id = $2;

-- name: FinishPhase :execrows
UPDATE phase_runs SET status = sqlc.arg(to_status), outcome = $2, finished_at = $3
WHERE id = $1 AND status = sqlc.arg(from_status) AND (worker_id = sqlc.arg(worker_id) OR sqlc.arg(worker_id) = '');

-- name: ListExpiredLeaseIDs :many
SELECT id FROM phase_runs WHERE status = 'running' AND leased_until <= now() LIMIT $1;

-- name: MarkPhaseReconciling :one
UPDATE phase_runs SET status = 'reconciling'
WHERE id = $1 AND status = 'running' AND leased_until <= now()
RETURNING *;

-- name: SupersedeLivePhases :many
UPDATE phase_runs SET status = 'superseded', outcome = $2, finished_at = $3
WHERE run_id = $1 AND status IN ('pending', 'running', 'reconciling')
RETURNING id, node_id, attempt;

-- name: GetPhase :one
SELECT * FROM phase_runs WHERE id = $1;

-- name: ListPhasesForRun :many
SELECT * FROM phase_runs WHERE run_id = $1 ORDER BY created_at, node_id, attempt;

-- name: ListSucceededNodes :many
SELECT node_id FROM phase_runs WHERE run_id = $1 AND status = 'succeeded';

-- name: InsertArtifact :one
INSERT INTO artifacts (id, run_id, node_id, attempt, type, payload, parent_ids, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetNodeArtifact :one
SELECT * FROM artifacts
WHERE run_id = $1 AND node_id = $2 AND type = $3
ORDER BY attempt DESC, created_at DESC
LIMIT 1;

-- name: ListArtifactsForRun :many
SELECT id, run_id, node_id, attempt, type, parent_ids, created_at
FROM artifacts WHERE run_id = $1 ORDER BY created_at, id;

-- name: LockEventCounter :one
SELECT next_id FROM run_event_counter WHERE onerow FOR UPDATE;

-- name: AdvanceEventCounter :exec
UPDATE run_event_counter SET next_id = next_id + sqlc.arg(n) WHERE onerow;

-- name: InsertRunEvent :exec
INSERT INTO run_events (id, run_id, project_id, kind, payload, occurred_at, correlation_id)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: InsertOutbox :exec
INSERT INTO outbox (event_id, kind, payload, created_at)
VALUES ($1, $2, $3, $4);

-- name: ListRunEventsAfter :many
SELECT * FROM run_events WHERE run_id = $1 AND id > $2 ORDER BY id LIMIT $3;

-- name: CountOutbox :one
SELECT count(*) FROM outbox;

-- name: PruneOutbox :execrows
DELETE FROM outbox WHERE created_at < $1;
```

- [ ] **Step 5: Add the fifth sqlc block and regenerate**

In `backend/sqlc.yaml`, append a fifth entry — identical to the workflows block except `queries: internal/runs/store/queries.sql` and `out: internal/runs/store`. In `Taskfile.yml`, add `backend/internal/runs/store` to the `contracts:check` diff path list.

Run: `task db:sqlc && cd backend && go build ./...`
Expected: generation succeeds; `store` package compiles. If `sqlc.embed` fights the LATERAL join, fall back to explicit column lists (`t.id, t.project_id, … , r.id AS latest_run_id, r.status AS latest_run_status`) — same row struct, no behavioral difference.

- [ ] **Step 6: Classify the workflow-delete conflict**

`backend/internal/workflows/workflows.go`, in `DeleteWorkflow`, after the `n == 0` check — a delete that trips the runs→versions RESTRICT chain surfaces as a 23503:

```go
func isFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
```

and in the delete path:

```go
		n, err := store.New(tx).DeleteWorkflow(ctx, id)
		if isFKViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "workflow has runs; delete is blocked")
		}
		if err != nil {
			return fmt.Errorf("workflows: delete: %w", err)
		}
```

(`pgconn` is already imported by `service.go`; move `isUniqueViolation`/`isFKViolation` side by side there if the linter prefers. The end-to-end proof that a task blocks workflow deletion lands in Task 7's integration tests, when tasks can exist.)

- [ ] **Step 7: Full gate and commit**

Run: `task contracts:check && task backend:ci`
Expected: PASS (regen-diff clean, migration tests green)

```bash
git add backend/internal/platform/pg backend/internal/runs/store backend/sqlc.yaml Taskfile.yml backend/internal/workflows
git commit -m "feat(backend): execution-spine schema, migration, and sqlc store"
```
### Task 5: Commit-ordered run events and the outbox

The event appender every spine mutation calls in its own transaction: allocate ids under the counter-row lock, insert `run_events` + one `outbox` envelope per event, all-or-nothing. Integration proof for the two spec §7 bullets that exist without a tailer: same-tx atomicity and commit-ordering under deliberately contended transactions.

**Files:**
- Create: `backend/internal/runs/events.go`
- Create: `backend/internal/runs/events_integration_test.go` (build tag `integration`)
- Create: `backend/internal/runs/testutil_integration_test.go` (build tag `integration` — shared spine fixtures)
- Create: `backend/internal/runs/export_integration_test.go` (build tag `integration`, package `runs` — test exports; grows in Task 8)

**Interfaces:**
- Consumes: Task 4's store, `platform/pg`, `platform/clock`.
- Produces:
  - Event kind constants: `EventRunCreated = "run.created"`, `EventRunStarted = "run.started"`, `EventRunSuperseded = "run.superseded"`, `EventRunSucceeded = "run.succeeded"`, `EventRunFailed = "run.failed"`, `EventRunDead = "run.dead"`, `EventPhaseMaterialized = "phase.materialized"`, `EventPhaseStarted = "phase.started"`, `EventPhaseReconciling = "phase.reconciling"`, `EventPhaseRetried = "phase.retried"`, `EventPhaseSucceeded = "phase.succeeded"`, `EventPhaseDead = "phase.dead"`
  - `runs.Event{RunID, ProjectID uuid.UUID; Kind string; Payload map[string]any; CorrelationID string}`
  - `runs.appendEvents(ctx context.Context, tx pgx.Tx, now time.Time, evs []Event) error` (unexported — only spine transactions call it)

- [ ] **Step 1: Write the shared integration fixture**

`backend/internal/runs/testutil_integration_test.go` — every spine integration test needs a project/repo/user/workflow-version scaffold; build it once. (Raw SQL inserts, not peer-context services: contexts never import each other, test files included — the arch test enforces this.)

```go
//go:build integration

package runs_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

// fixture is the minimal asset scaffold a spine row graph needs. Rows
// are inserted with raw SQL: the spine's FKs point at peer-context
// tables, but the runs context never imports peer packages.
type fixture struct {
	pool      *pgxpool.Pool
	userID    uuid.UUID
	projectID uuid.UUID
	repoID    uuid.UUID
	versionID uuid.UUID
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.NewURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	f := fixture{pool: pool}
	err = pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, role) VALUES ('spine@example.com', 'x', 'admin') RETURNING id`,
	).Scan(&f.userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (name) VALUES ('spine') RETURNING id`,
	).Scan(&f.projectID); err != nil {
		t.Fatal(err)
	}
	var credID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO credentials (id, name, kind, key_id, wrapped_dek, ciphertext)
		 VALUES (uuidv7(), 'spine-token', 'forge_token', 'dev', '\x00', '\x00') RETURNING id`,
	).Scan(&credID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO repos (project_id, forge_kind, slug, default_branch, credential_id)
		 VALUES ($1, 'github', 'spine/repo', 'main', $2) RETURNING id`,
		f.projectID, credID,
	).Scan(&f.repoID); err != nil {
		t.Fatal(err)
	}
	var wfID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO workflows (name) VALUES ('spine-wf') RETURNING id`,
	).Scan(&wfID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO workflow_versions (workflow_id, version, content_hash, document)
		 VALUES ($1, 1, 'deadbeef', '{"schema":1,"nodes":[]}'::jsonb) RETURNING id`,
		wfID,
	).Scan(&f.versionID); err != nil {
		t.Fatal(err)
	}
	return f
}

// newRunRow inserts a bare task+run pair for tests below the service
// layer (events, queue). Wall deadline far in the future by default.
func (f fixture) newRunRow(t *testing.T, status string) (taskID, runID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	err := f.pool.QueryRow(ctx,
		`INSERT INTO tasks (project_id, repo_id, workflow_version_id, title, requester_id, created_at)
		 VALUES ($1, $2, $3, 'spine task', $4, $5) RETURNING id`,
		f.projectID, f.repoID, f.versionID, f.userID, time.Now().UTC(),
	).Scan(&taskID)
	if err != nil {
		t.Fatal(err)
	}
	err = f.pool.QueryRow(ctx,
		`INSERT INTO runs (task_id, status, definition_hash, base_sha, policy_snapshot,
		                   execution_budget_ms, wall_deadline, created_at)
		 VALUES ($1, $2, 'deadbeef', 'abc123', '{"schema":1,"max_attempts":3,"execution_budget_ms":7200000,"wall_clock_ms":86400000}'::jsonb,
		         7200000, now() + interval '1 day', $3) RETURNING id`,
		taskID, status, time.Now().UTC(),
	).Scan(&runID)
	if err != nil {
		t.Fatal(err)
	}
	return taskID, runID
}
```

- [ ] **Step 2: Write the failing integration tests**

`backend/internal/runs/events_integration_test.go`:

```go
//go:build integration

package runs_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/runs"
)

// Events and outbox rows land together or not at all (spec §7 same-tx
// atomicity): a rolled-back tx leaves neither, and ids are not burned
// out of order.
func TestEventAppendIsAtomic(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, runID := f.newRunRow(t, "running")

	boom := errors.New("boom")
	err := pg.WithTx(ctx, f.pool, func(tx pgx.Tx) error {
		if err := runs.AppendEventsForTest(ctx, tx, time.Now().UTC(), []runs.Event{
			{RunID: runID, ProjectID: f.projectID, Kind: runs.EventRunStarted},
		}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("tx err = %v", err)
	}
	var events, outbox int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM run_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if events != 0 || outbox != 0 {
		t.Errorf("rollback left events=%d outbox=%d", events, outbox)
	}

	// A committed append lands both, ids from the counter.
	err = pg.WithTx(ctx, f.pool, func(tx pgx.Tx) error {
		return runs.AppendEventsForTest(ctx, tx, time.Now().UTC(), []runs.Event{
			{RunID: runID, ProjectID: f.projectID, Kind: runs.EventRunStarted},
			{RunID: runID, ProjectID: f.projectID, Kind: runs.EventRunSucceeded},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.pool.Query(ctx, `SELECT id, kind FROM run_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		var kind string
		if err := rows.Scan(&id, &kind); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Errorf("ids = %v, want [1 2] (rollback must not burn ids)", ids)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if outbox != 2 {
		t.Errorf("outbox rows = %d, want 2", outbox)
	}
}

// The counter-row lock serializes appenders: a tx that allocated first
// and commits last CANNOT be outrun — the second tx blocks on the lock
// until the first commits, so id order is commit order by construction
// (spec §7: commit-ordered delivery under deliberately inverted commit
// order).
func TestEventIDsAreCommitOrdered(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, runID := f.newRunRow(t, "running")

	firstAllocated := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	go func() { // tx A: allocates first, then dawdles before committing.
		defer wg.Done()
		err := pg.WithTx(ctx, f.pool, func(tx pgx.Tx) error {
			if err := runs.AppendEventsForTest(ctx, tx, time.Now().UTC(), []runs.Event{
				{RunID: runID, ProjectID: f.projectID, Kind: "test.a"},
			}); err != nil {
				return err
			}
			close(firstAllocated)
			<-release
			return nil
		})
		if err != nil {
			t.Error(err)
		}
	}()

	bDone := make(chan struct{})
	go func() { // tx B: tries to append while A holds the counter lock.
		defer wg.Done()
		<-firstAllocated
		err := pg.WithTx(ctx, f.pool, func(tx pgx.Tx) error {
			return runs.AppendEventsForTest(ctx, tx, time.Now().UTC(), []runs.Event{
				{RunID: runID, ProjectID: f.projectID, Kind: "test.b"},
			})
		})
		if err != nil {
			t.Error(err)
		}
		close(bDone)
	}()

	select {
	case <-bDone:
		t.Fatal("tx B committed while tx A held the counter lock")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	wg.Wait()

	rows, err := f.pool.Query(ctx, `SELECT kind FROM run_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, k)
	}
	if len(kinds) != 2 || kinds[0] != "test.a" || kinds[1] != "test.b" {
		t.Errorf("kinds by id = %v, want [test.a test.b]", kinds)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/runs/ -run Event`
Expected: FAIL — `AppendEventsForTest` undefined.

- [ ] **Step 4: Implement the appender**

`backend/internal/runs/events.go`:

```go
package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// RunEvent vocabulary (plan header). Append-only observation, never
// dispatch (spec §1 evolvability rule 2).
const (
	EventRunCreated         = "run.created"
	EventRunStarted         = "run.started"
	EventRunSuperseded      = "run.superseded"
	EventRunSucceeded       = "run.succeeded"
	EventRunFailed          = "run.failed"
	EventRunDead            = "run.dead"
	EventPhaseMaterialized  = "phase.materialized"
	EventPhaseStarted       = "phase.started"
	EventPhaseReconciling   = "phase.reconciling"
	EventPhaseRetried       = "phase.retried"
	EventPhaseSucceeded     = "phase.succeeded"
	EventPhaseDead          = "phase.dead"
)

// Event is one observation appended in the same tx as its state change.
type Event struct {
	RunID         uuid.UUID
	ProjectID     uuid.UUID
	Kind          string
	Payload       map[string]any
	CorrelationID string
}

// appendEvents allocates commit-ordered ids under the counter-row lock
// and writes run_events plus one outbox envelope per event — all inside
// the caller's transaction (spec §2). The lock is held until the caller
// commits: id order IS commit order.
func appendEvents(ctx context.Context, tx pgx.Tx, now time.Time, evs []Event) error {
	if len(evs) == 0 {
		return nil
	}
	q := store.New(tx)
	next, err := q.LockEventCounter(ctx)
	if err != nil {
		return fmt.Errorf("runs: lock event counter: %w", err)
	}
	if err := q.AdvanceEventCounter(ctx, int64(len(evs))); err != nil {
		return fmt.Errorf("runs: advance event counter: %w", err)
	}
	for i, e := range evs {
		payload := e.Payload
		if payload == nil {
			payload = map[string]any{}
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("runs: marshal event payload: %w", err)
		}
		id := next + int64(i)
		if err := q.InsertRunEvent(ctx, store.InsertRunEventParams{
			ID: id, RunID: e.RunID, ProjectID: e.ProjectID, Kind: e.Kind,
			Payload: raw, OccurredAt: now, CorrelationID: e.CorrelationID,
		}); err != nil {
			return fmt.Errorf("runs: insert event: %w", err)
		}
		if err := q.InsertOutbox(ctx, store.InsertOutboxParams{
			EventID: id, Kind: e.Kind, Payload: raw, CreatedAt: now,
		}); err != nil {
			return fmt.Errorf("runs: insert outbox: %w", err)
		}
	}
	return nil
}
```

And the test-only export, `backend/internal/runs/export_integration_test.go` (build tag `integration`, package `runs` — the house pattern for reaching unexported seams from `runs_test`):

```go
//go:build integration

package runs

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// AppendEventsForTest exposes the tx-scoped appender to integration
// tests; production callers stay inside the service's transactions.
func AppendEventsForTest(ctx context.Context, tx pgx.Tx, now time.Time, evs []Event) error {
	return appendEvents(ctx, tx, now, evs)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/runs/ -run Event`
Expected: PASS

- [ ] **Step 6: Full gate and commit**

Run: `task backend:ci`
Expected: PASS

```bash
git add backend/internal/runs
git commit -m "feat(backend): commit-ordered run events with same-tx outbox"
```

### Task 6: Deterministic fakes — scripted executor, fake forge, bare-repo fixture

The golden-set substrate (spec §7, invariant 14): a deterministic fake harness executor driven by directives embedded in node instructions, a map-backed fake forge, and a local bare-repo fixture. First-class non-test packages so integration tests, the golden-set seed, and future replay tooling all share them; production serve never wires them.

**Files:**
- Create: `backend/internal/runs/fake/executor.go`
- Create: `backend/internal/runs/fake/forge.go`
- Create: `backend/internal/runs/fake/repo.go`
- Create: `backend/internal/runs/fake/executor_test.go`

**Interfaces:**
- Consumes: Task 3's port types.
- Produces:
  - `fake.NewExecutor() *fake.Executor` — implements `runs.Executor`; per-`(run,node)` execution counters; `Completed(ref runs.AttemptRef) bool` for assertions
  - Directive grammar (one per line inside node instructions, prefix `caesar-fake:`):
    - `caesar-fake: fail attempts=N class=<transient|permanent> code=<code>` — executions 1…N return that failure, N+1 onward succeed
    - `caesar-fake: hang` — block until ctx is done, then return ctx.Err()
  - Scripted success outputs by node kind (deterministic given the request): `plan` → `{"content": "plan: <instructions first line>"}` · `implement` → patch `{"diff": <deterministic unified diff>, "base_sha": req.BaseSHA, "tree_hash": <hex sha256 of diff+base>}` · `review` → `{"verdict": "approve", "notes": "fake review"}`
  - `fake.Forge{Heads map[uuid.UUID]string}` — implements `runs.Forge`; unknown repo → error
  - `fake.InitBareRepo(dir string) (headSHA string, err error)` — `git init --bare` + one deterministic commit, returns its SHA

- [ ] **Step 1: Write the failing tests**

`backend/internal/runs/fake/executor_test.go`:

```go
package fake_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

func req(kind, instructions string) runs.ExecutionRequest {
	return runs.ExecutionRequest{
		Ref:          runs.AttemptRef{RunID: uuid.Max, NodeID: "n", Attempt: 1},
		Kind:         kind,
		Instructions: instructions,
		BaseSHA:      "basesha",
		Deadline:     time.Now().Add(time.Minute),
	}
}

func TestScriptedOutputsAreDeterministic(t *testing.T) {
	ex := fake.NewExecutor()
	ctx := context.Background()

	plan1, err := ex.Execute(ctx, req(runs.KindPlan, "Plan the change."))
	if err != nil {
		t.Fatal(err)
	}
	plan2, err := fake.NewExecutor().Execute(ctx, req(runs.KindPlan, "Plan the change."))
	if err != nil {
		t.Fatal(err)
	}
	if plan1.Artifacts[0].Payload["content"] != plan2.Artifacts[0].Payload["content"] {
		t.Error("plan output must be deterministic")
	}

	impl, err := ex.Execute(ctx, req(runs.KindImplement, "Implement."))
	if err != nil {
		t.Fatal(err)
	}
	a := impl.Artifacts[0]
	if a.Type != runs.ArtifactKindPatch {
		t.Fatalf("implement produced %s", a.Type)
	}
	if a.Payload["base_sha"] != "basesha" || a.Payload["diff"] == "" || a.Payload["tree_hash"] == "" {
		t.Errorf("patch payload incomplete: %v", a.Payload)
	}

	rev, err := ex.Execute(ctx, req(runs.KindReview, "Review."))
	if err != nil {
		t.Fatal(err)
	}
	if rev.Artifacts[0].Payload["verdict"] != "approve" {
		t.Errorf("review payload: %v", rev.Artifacts[0].Payload)
	}
}

func TestFailDirectiveCountsAcrossAttempts(t *testing.T) {
	ex := fake.NewExecutor()
	ctx := context.Background()
	r := req(runs.KindImplement, "Implement.\ncaesar-fake: fail attempts=2 class=transient code=harness_error")

	for i := 1; i <= 2; i++ {
		_, err := ex.Execute(ctx, r)
		out := runs.ClassifyOutcome(err)
		if out.Class != runs.OutcomeTransient || out.Code != runs.CodeHarnessError {
			t.Fatalf("execution %d: outcome %+v", i, out)
		}
	}
	if _, err := ex.Execute(ctx, r); err != nil {
		t.Fatalf("third execution should succeed: %v", err)
	}
	if !ex.Completed(r.Ref) {
		t.Error("Completed must report the successful execution")
	}
}

func TestHangDirectiveBlocksUntilCancel(t *testing.T) {
	ex := fake.NewExecutor()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := ex.Execute(ctx, req(runs.KindPlan, "caesar-fake: hang"))
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("hang must return ctx error, got %v", err)
	}
}

func TestInspectReportsCompletedExecutions(t *testing.T) {
	ex := fake.NewExecutor()
	ctx := context.Background()
	r := req(runs.KindPlan, "Plan.")
	if _, err := ex.Execute(ctx, r); err != nil {
		t.Fatal(err)
	}
	res, err := ex.Inspect(ctx, r.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Completed || len(res.Result.Artifacts) != 1 {
		t.Errorf("inspect = %+v", res)
	}
	missing, err := ex.Inspect(ctx, runs.AttemptRef{RunID: uuid.Max, NodeID: "ghost", Attempt: 9})
	if err != nil {
		t.Fatal(err)
	}
	if missing.Completed {
		t.Error("unknown attempt must not report completed")
	}
}

func TestFakeForge(t *testing.T) {
	id := uuid.Max
	f := fake.Forge{Heads: map[uuid.UUID]string{id: "abc"}}
	sha, err := f.ResolveHead(context.Background(), runs.RepoRef{ID: id})
	if err != nil || sha != "abc" {
		t.Errorf("ResolveHead = %q, %v", sha, err)
	}
	if _, err := f.ResolveHead(context.Background(), runs.RepoRef{ID: uuid.Nil}); err == nil {
		t.Error("unknown repo must error")
	}
}

func TestInitBareRepo(t *testing.T) {
	sha, err := fake.InitBareRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(sha) != 40 {
		t.Errorf("head sha = %q", sha)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/runs/fake/`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement the fakes**

`backend/internal/runs/fake/executor.go`:

```go
// Package fake holds the deterministic doubles behind the spine's ports
// (spec §7 golden set, invariant 14): a scripted executor, a map-backed
// forge, and a bare-repo fixture. First-class packages so tests and
// replay tooling share them; production serve never wires fake.
package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/giiorg/caesar/backend/internal/runs"
)

const directivePrefix = "caesar-fake:"

// directive is one parsed scripting line from node instructions.
type directive struct {
	hang     bool
	failN    int
	failOut  runs.Outcome
}

// parseDirective scans instructions for the first caesar-fake: line.
func parseDirective(instructions string) (directive, error) {
	for _, line := range strings.Split(instructions, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, directivePrefix) {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, directivePrefix)))
		if len(fields) == 0 {
			return directive{}, fmt.Errorf("fake: empty directive")
		}
		switch fields[0] {
		case "hang":
			return directive{hang: true}, nil
		case "fail":
			d := directive{failOut: runs.Outcome{Class: runs.OutcomeTransient, Code: runs.CodeHarnessError, Message: "scripted failure"}}
			for _, kv := range fields[1:] {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return directive{}, fmt.Errorf("fake: bad directive field %q", kv)
				}
				switch k {
				case "attempts":
					n, err := strconv.Atoi(v)
					if err != nil || n < 1 {
						return directive{}, fmt.Errorf("fake: bad attempts %q", v)
					}
					d.failN = n
				case "class":
					d.failOut.Class = runs.OutcomeClass(v)
				case "code":
					d.failOut.Code = v
				default:
					return directive{}, fmt.Errorf("fake: unknown directive field %q", k)
				}
			}
			if d.failN == 0 {
				return directive{}, fmt.Errorf("fake: fail directive needs attempts=N")
			}
			return d, nil
		default:
			return directive{}, fmt.Errorf("fake: unknown directive %q", fields[0])
		}
	}
	return directive{}, nil
}

// Executor is the deterministic fake harness: scripted outputs, failure
// injection via directives, an execution registry for Inspect.
type Executor struct {
	mu        sync.Mutex
	execCount map[string]int                        // per (run,node): executions seen
	completed map[runs.AttemptRef]runs.ExecutionResult // successful executions
}

func NewExecutor() *Executor {
	return &Executor{
		execCount: map[string]int{},
		completed: map[runs.AttemptRef]runs.ExecutionResult{},
	}
}

func nodeKey(ref runs.AttemptRef) string { return ref.RunID.String() + "/" + ref.NodeID }

func (e *Executor) Execute(ctx context.Context, req runs.ExecutionRequest) (runs.ExecutionResult, error) {
	d, err := parseDirective(req.Instructions)
	if err != nil {
		return runs.ExecutionResult{}, runs.OutcomeError{Outcome: runs.Outcome{
			Class: runs.OutcomePermanent, Code: runs.CodeInvalidOutput, Message: err.Error(),
		}}
	}
	if d.hang {
		<-ctx.Done()
		return runs.ExecutionResult{}, ctx.Err()
	}
	e.mu.Lock()
	e.execCount[nodeKey(req.Ref)]++
	n := e.execCount[nodeKey(req.Ref)]
	e.mu.Unlock()
	if d.failN > 0 && n <= d.failN {
		return runs.ExecutionResult{}, runs.OutcomeError{Outcome: d.failOut}
	}

	res := runs.ExecutionResult{Artifacts: []runs.NewArtifact{scripted(req)}}
	e.mu.Lock()
	e.completed[req.Ref] = res
	e.mu.Unlock()
	return res, nil
}

// scripted builds the deterministic success artifact for a node kind.
func scripted(req runs.ExecutionRequest) runs.NewArtifact {
	firstLine, _, _ := strings.Cut(req.Instructions, "\n")
	switch req.Kind {
	case runs.KindImplement:
		diff := fmt.Sprintf("--- a/CAESAR.md\n+++ b/CAESAR.md\n@@ -0,0 +1 @@\n+%s (%s)\n", firstLine, req.Ref.NodeID)
		sum := sha256.Sum256([]byte(diff + req.BaseSHA))
		return runs.NewArtifact{Type: runs.ArtifactKindPatch, Payload: map[string]any{
			"diff": diff, "base_sha": req.BaseSHA, "tree_hash": hex.EncodeToString(sum[:]),
		}}
	case runs.KindReview:
		return runs.NewArtifact{Type: runs.ArtifactKindReviewVerdict, Payload: map[string]any{
			"verdict": "approve", "notes": "fake review",
		}}
	default: // plan and anything agentic-shaped
		return runs.NewArtifact{Type: runs.ArtifactKindPlan, Payload: map[string]any{
			"content": "plan: " + firstLine,
		}}
	}
}

func (e *Executor) Inspect(_ context.Context, ref runs.AttemptRef) (runs.InspectResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	res, ok := e.completed[ref]
	if !ok {
		return runs.InspectResult{}, nil
	}
	return runs.InspectResult{Completed: true, Result: res}, nil
}

// Completed reports whether ref finished successfully (test assertions).
func (e *Executor) Completed(ref runs.AttemptRef) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.completed[ref]
	return ok
}
```

`backend/internal/runs/fake/forge.go`:

```go
package fake

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// Forge resolves heads from a scripted map — deterministic, no network.
type Forge struct {
	Heads map[uuid.UUID]string
}

func (f Forge) ResolveHead(_ context.Context, repo runs.RepoRef) (string, error) {
	sha, ok := f.Heads[repo.ID]
	if !ok {
		return "", fmt.Errorf("fake forge: no head for repo %s", repo.ID)
	}
	return sha, nil
}
```

`backend/internal/runs/fake/repo.go`:

```go
package fake

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// InitBareRepo creates a bare repository with one deterministic commit
// and returns its head SHA. Requires the git binary (dev + CI have it);
// callers are tests and the golden-set seed only.
func InitBareRepo(dir string) (string, error) {
	work := filepath.Join(dir, "work")
	bare := filepath.Join(dir, "bare.git")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# golden set seed\n"), 0o644); err != nil {
		return "", err
	}
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=caesar", "GIT_AUTHOR_EMAIL=caesar@example.com",
		"GIT_COMMITTER_NAME=caesar", "GIT_COMMITTER_EMAIL=caesar@example.com",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
	steps := [][]string{
		{"git", "-C", work, "init", "-q", "-b", "main"},
		{"git", "-C", work, "add", "."},
		{"git", "-C", work, "commit", "-q", "-m", "seed"},
		{"git", "clone", "-q", "--bare", work, bare},
	}
	for _, s := range steps {
		cmd := exec.Command(s[0], s[1:]...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("fake repo: %v: %s", s, out)
		}
	}
	out, err := exec.Command("git", "-C", bare, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("fake repo: rev-parse: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/runs/fake/`
Expected: PASS

- [ ] **Step 5: Full gate and commit**

Run: `task backend:ci`
Expected: PASS (envaccess analyzer: `os.Environ()` here is a **test fixture package**, not a runner path — if the analyzer flags it, scope the fixture's env to the explicit GIT_* variables only, dropping the inherited environment, which also makes the fixture more hermetic: `cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, ...gitVars}` — and add the analyzer-sanctioned annotation only if the plan's reviewer agrees)

```bash
git add backend/internal/runs/fake
git commit -m "feat(backend): deterministic fake executor, forge, and bare-repo fixture"
```
### Task 7: Intake — tasks, runs, supersede-before-work

The service layer's write half: `CreateTask` (resolve repo + version + harnesses through the ports, evaluate the snapshot, pin hash/base-SHA/deadline, insert task + first run + events + audit in one tx — durable-before-acknowledge) and `Rerun` (supersede a pending run or refuse a live one, then mint the next run). Plus the reads the API needs. The workflow-delete-blocked-by-runs proof lands here too.

**Files:**
- Create: `backend/internal/runs/service.go`
- Create: `backend/internal/runs/tasks.go`
- Create: `backend/internal/runs/tasks_integration_test.go` (build tag `integration`)
- Modify: `backend/internal/workflows/workflows_integration_test.go` (delete-blocked test)

**Interfaces:**
- Consumes: Tasks 1–6 (vocabulary, snapshot, compile, store, events, fakes in tests), `platform/{pg,clock,audit,authn,httpx,xerrors}`.
- Produces:
  - `runs.Params{Snapshot PolicySnapshot}`
  - `runs.NewService(pool *pgxpool.Pool, c clock.Clock, rec *audit.Recorder, defs DefinitionSource, repos RepoSource, harnesses HarnessSource, forge Forge, p Params) *Service`
  - `runs.Task{ID, ProjectID, RepoID, WorkflowVersionID, RequesterID uuid.UUID; Title string; Inputs map[string]any; CreatedAt time.Time; LatestRunID uuid.UUID; LatestRunStatus RunStatus}`
  - `runs.Run{ID, TaskID uuid.UUID; Status RunStatus; DefinitionHash, BaseSHA string; Snapshot PolicySnapshot; ExecutionUsedMS int64; WallDeadline time.Time; Outcome *Outcome; CreatedAt time.Time; StartedAt, FinishedAt *time.Time}`
  - `runs.Phase{ID uuid.UUID; NodeID string; Attempt, MaxAttempts int32; Status PhaseStatus; WorkerID string; Outcome *Outcome; CreatedAt time.Time; FinishedAt *time.Time}`
  - `runs.ArtifactMeta{ID uuid.UUID; NodeID string; Attempt int32; Type string; ParentIDs []uuid.UUID; CreatedAt time.Time}`
  - `runs.NewTask{RepoID, WorkflowID uuid.UUID; WorkflowVersion int32; Title string; Inputs map[string]any}`
  - Methods: `RepoProject(ctx, repoID) (uuid.UUID, error)` (API pre-authz lookup) · `CreateTask(ctx, actor authn.Principal, in NewTask) (Task, Run, error)` · `Rerun(ctx, actor authn.Principal, taskID uuid.UUID) (Run, error)` · `GetTask(ctx, id) (Task, error)` · `ListTasks(ctx) ([]Task, error)` · `ListTasksByProjects(ctx, ids []uuid.UUID) ([]Task, error)` · `GetRun(ctx, id) (Run, []Phase, []ArtifactMeta, error)` · `ListRunsForTask(ctx, taskID) ([]Run, error)` · `ListRunEvents(ctx, runID uuid.UUID, afterID int64, limit int32) ([]RunEventRecord, error)` with `runs.RunEventRecord{ID int64; Kind string; Payload map[string]any; OccurredAt time.Time}`
  - Audit actions written here: `task.created`, `run.created`, `run.superseded`

- [ ] **Step 1: Write the service scaffolding**

`backend/internal/runs/service.go`:

```go
package runs

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// Params is the injected policy source until Plan 09's engine: app
// wires DefaultSnapshot(); tests wire short budgets.
type Params struct {
	Snapshot PolicySnapshot
}

type Service struct {
	pool      *pgxpool.Pool
	clock     clock.Clock
	audit     *audit.Recorder
	defs      DefinitionSource
	repos     RepoSource
	harnesses HarnessSource
	forge     Forge
	params    Params
}

func NewService(pool *pgxpool.Pool, c clock.Clock, rec *audit.Recorder,
	defs DefinitionSource, repos RepoSource, harnesses HarnessSource, forge Forge, p Params) *Service {
	return &Service{pool: pool, clock: c, audit: rec, defs: defs, repos: repos,
		harnesses: harnesses, forge: forge, params: p}
}

func actorID(p authn.Principal) uuid.NullUUID {
	return uuid.NullUUID{UUID: p.UserID, Valid: true}
}

func notFound(err error, msg string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return xerrors.E(xerrors.ClassNotFound, msg)
	}
	return err
}

// unmarshalMap decodes a jsonb column into a map, tolerating NULL/empty.
func unmarshalMap(raw []byte) (map[string]any, error) {
	out := map[string]any{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("runs: unmarshal jsonb: %w", err)
	}
	return out, nil
}

// snapshotFromRow re-parses the pinned snapshot; corruption is internal.
func snapshotFromRow(raw []byte) (PolicySnapshot, error) {
	var s PolicySnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return PolicySnapshot{}, fmt.Errorf("runs: pinned snapshot corrupt: %w", err)
	}
	return s, nil
}

// outcomeFromRow decodes the optional outcome column.
func outcomeFromRow(raw []byte) (*Outcome, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var o Outcome
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("runs: outcome corrupt: %w", err)
	}
	return &o, nil
}

// compilePinned parses a stored document mirror out of raw store bytes.
// Failures are internal_error by definition (plan Task 3 note).
func compilePinned(def PinnedDefinition) (ExecPlan, error) {
	plan, err := Compile(def.Doc)
	if err != nil {
		return ExecPlan{}, fmt.Errorf("runs: stored definition %s: %w", def.VersionID, err)
	}
	return plan, nil
}
```

- [ ] **Step 2: Write the failing integration tests**

`backend/internal/runs/tasks_integration_test.go` — these drive the whole intake surface. The fixture from Task 5 grows two helpers here: `newService` (wires fakes + short-circuit ports over the fixture rows) and `fixturePorts` (tiny in-test implementations of `DefinitionSource`/`RepoSource`/`HarnessSource` returning the fixture's rows — the real adapters live in app, Task 11).

```go
//go:build integration

package runs_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

// fixturePorts serves the fixture's own rows through the runs ports —
// integration tests below the app layer never import peer contexts.
type fixturePorts struct {
	f       fixture
	doc     runs.DefinitionDoc
	hash    string
	harness uuid.UUID
}

func (p fixturePorts) Definition(_ context.Context, workflowID uuid.UUID, version int32) (runs.PinnedDefinition, error) {
	if version != 1 {
		return runs.PinnedDefinition{}, xerrors.E(xerrors.ClassNotFound, "no such workflow version")
	}
	return runs.PinnedDefinition{
		VersionID: p.f.versionID, WorkflowID: workflowID, Version: 1,
		ContentHash: p.hash, Doc: p.doc,
	}, nil
}

func (p fixturePorts) DefinitionByVersionID(_ context.Context, versionID uuid.UUID) (runs.PinnedDefinition, error) {
	if versionID != p.f.versionID {
		return runs.PinnedDefinition{}, xerrors.E(xerrors.ClassNotFound, "no such workflow version")
	}
	return runs.PinnedDefinition{
		VersionID: p.f.versionID, WorkflowID: uuid.Max, Version: 1,
		ContentHash: p.hash, Doc: p.doc,
	}, nil
}

func (p fixturePorts) Repo(_ context.Context, id uuid.UUID) (runs.RepoRef, error) {
	if id != p.f.repoID {
		return runs.RepoRef{}, xerrors.E(xerrors.ClassNotFound, "no such repo")
	}
	return runs.RepoRef{ID: p.f.repoID, ProjectID: p.f.projectID, Forge: "github", Slug: "spine/repo", DefaultBranch: "main"}, nil
}

func (p fixturePorts) HarnessByName(_ context.Context, name string) (runs.HarnessRef, error) {
	if name != "cc" {
		return runs.HarnessRef{}, xerrors.E(xerrors.ClassNotFound, "no such harness")
	}
	return runs.HarnessRef{ID: p.harness, Kind: "claude-code"}, nil
}

func agenticDoc() runs.DefinitionDoc {
	return runs.DefinitionDoc{
		Nodes: []runs.DefNode{
			{ID: "plan", Kind: runs.KindPlan, Harness: "cc", Instructions: "Plan the change."},
			{ID: "impl", Kind: runs.KindImplement, Harness: "cc", Instructions: "Implement the plan."},
			{ID: "review", Kind: runs.KindReview, Harness: "cc", Instructions: "Review the patch."},
		},
		Edges: []runs.DefEdge{
			{From: "plan", To: "impl", Artifact: runs.ArtifactKindPlan},
			{From: "impl", To: "review", Artifact: runs.ArtifactKindPatch},
		},
	}
}

func newService(t *testing.T, f fixture, doc runs.DefinitionDoc, snap runs.PolicySnapshot) (*runs.Service, fixturePorts) {
	t.Helper()
	ports := fixturePorts{f: f, doc: doc, hash: "deadbeef", harness: uuid.Max}
	forge := fake.Forge{Heads: map[uuid.UUID]string{f.repoID: "basesha0000000000000000000000000000000000"}}
	svc := runs.NewService(f.pool, clock.System{}, audit.NewRecorder(clock.System{}),
		ports, ports, ports, forge, runs.Params{Snapshot: snap})
	return svc, ports
}

func principal(f fixture) authn.Principal {
	return authn.Principal{UserID: f.userID, Email: "spine@example.com", Role: authn.RoleAdmin}
}

func TestCreateTaskPinsEverything(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	task, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1,
		Title: "ship it", Inputs: map[string]any{"hint": "small"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.ProjectID != f.projectID || task.LatestRunID != run.ID || task.LatestRunStatus != runs.RunPending {
		t.Errorf("task projection wrong: %+v", task)
	}
	if run.Status != runs.RunPending || run.DefinitionHash != "deadbeef" || !strings.HasPrefix(run.BaseSHA, "basesha") {
		t.Errorf("run pins wrong: %+v", run)
	}
	if run.Snapshot != runs.DefaultSnapshot() {
		t.Errorf("snapshot not pinned: %+v", run.Snapshot)
	}
	wantDeadline := run.CreatedAt.Add(24 * time.Hour)
	if run.WallDeadline.Sub(wantDeadline) > time.Second || wantDeadline.Sub(run.WallDeadline) > time.Second {
		t.Errorf("wall deadline %v, want ~%v", run.WallDeadline, wantDeadline)
	}

	// run.created event exists; no phases yet (orchestrator materializes).
	evs, err := svc.ListRunEvents(ctx, run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != "run.created" {
		t.Errorf("events = %+v, want one run.created", evs)
	}
	_, phases, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 0 {
		t.Errorf("intake must not materialize phases, got %d", len(phases))
	}
}

func TestCreateTaskValidation(t *testing.T) {
	f := newFixture(t)
	gated := agenticDoc()
	gated.Nodes = append(gated.Nodes, runs.DefNode{ID: "gate", Kind: runs.KindGate})
	gated.Edges = append(gated.Edges, runs.DefEdge{From: "impl", To: "gate", Artifact: runs.ArtifactKindPatch})

	cases := []struct {
		name  string
		doc   runs.DefinitionDoc
		in    runs.NewTask
		field string
	}{
		{"empty title", agenticDoc(), runs.NewTask{RepoID: uuid.Max, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: ""}, "title"},
		{"title too long", agenticDoc(), runs.NewTask{RepoID: uuid.Max, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: strings.Repeat("x", 201)}, "title"},
		{"unknown harness", func() runs.DefinitionDoc { d := agenticDoc(); d.Nodes[0].Harness = "ghost"; return d }(), runs.NewTask{WorkflowVersion: 1, Title: "t"}, "harness"},
		{"gate rejected in plan 05", gated, runs.NewTask{WorkflowVersion: 1, Title: "t"}, "definition"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newService(t, f, tc.doc, runs.DefaultSnapshot())
			in := tc.in
			if in.RepoID == uuid.Nil {
				in.RepoID = f.repoID
			}
			if in.WorkflowID == uuid.Nil {
				in.WorkflowID = uuid.Max
			}
			_, _, err := svc.CreateTask(context.Background(), principal(f), in)
			if xerrors.ClassOf(err) != xerrors.ClassInvalid && xerrors.ClassOf(err) != xerrors.ClassNotFound {
				t.Fatalf("err = %v, want invalid/not-found", err)
			}
		})
	}
}

func TestRerunSupersedesBeforeWork(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	_, run1, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "t",
	})
	if err != nil {
		t.Fatal(err)
	}

	run2, err := svc.Rerun(ctx, principal(f), run1.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	got1, _, _, err := svc.GetRun(ctx, run1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got1.Status != runs.RunSuperseded {
		t.Errorf("run1 = %s, want superseded", got1.Status)
	}
	if run2.Status != runs.RunPending {
		t.Errorf("run2 = %s, want pending", run2.Status)
	}

	// A running run refuses: flip run2 to running and try again.
	if _, err := f.pool.Exec(ctx, `UPDATE runs SET status='running', started_at=now() WHERE id=$1`, run2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Rerun(ctx, principal(f), run1.TaskID); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Errorf("rerun of running run: %v, want conflict", err)
	}
}

func TestGrantScopedListing(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	ctx := context.Background()
	if _, _, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "t",
	}); err != nil {
		t.Fatal(err)
	}
	all, err := svc.ListTasks(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("ListTasks = %d, %v", len(all), err)
	}
	scoped, err := svc.ListTasksByProjects(ctx, []uuid.UUID{f.projectID})
	if err != nil || len(scoped) != 1 {
		t.Fatalf("scoped = %d, %v", len(scoped), err)
	}
	empty, err := svc.ListTasksByProjects(ctx, []uuid.UUID{uuid.Max})
	if err != nil || len(empty) != 0 {
		t.Fatalf("foreign project = %d, %v", len(empty), err)
	}
}
```

And in `backend/internal/workflows/workflows_integration_test.go`, append the FK proof (raw-SQL spine rows — workflows never imports runs):

```go
func TestDeleteWorkflowBlockedByRuns(t *testing.T) {
	svc, pool := newTestService(t) // the file's existing constructor helper
	ctx := context.Background()
	actor := testAdmin()           // the file's existing principal helper

	wf, err := svc.CreateWorkflow(ctx, actor, "spine-block", "")
	if err != nil {
		t.Fatal(err)
	}
	v, _, err := svc.CreateVersion(ctx, actor, wf.ID, []byte(validDoc))
	if err != nil {
		t.Fatal(err)
	}

	// A task referencing the version, via raw SQL: workflows never
	// imports the runs context (arch law), and the FK chain is what is
	// under test, not the spine service.
	var userID, projectID, credID, repoID uuid.UUID
	mustScan := func(row pgx.Row) uuid.UUID {
		var id uuid.UUID
		if err := row.Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	userID = mustScan(pool.QueryRow(ctx, `INSERT INTO users (email, password_hash, role) VALUES ('blk@example.com','x','admin') RETURNING id`))
	projectID = mustScan(pool.QueryRow(ctx, `INSERT INTO projects (name) VALUES ('blk') RETURNING id`))
	credID = mustScan(pool.QueryRow(ctx, `INSERT INTO credentials (id, name, kind, key_id, wrapped_dek, ciphertext) VALUES (uuidv7(),'blk','forge_token','dev','\x00','\x00') RETURNING id`))
	repoID = mustScan(pool.QueryRow(ctx, `INSERT INTO repos (project_id, forge_kind, slug, credential_id) VALUES ($1,'github','blk/repo',$2) RETURNING id`, projectID, credID))
	mustScan(pool.QueryRow(ctx,
		`INSERT INTO tasks (project_id, repo_id, workflow_version_id, title, requester_id, created_at)
		 VALUES ($1, $2, $3, 'blocker', $4, now()) RETURNING id`,
		projectID, repoID, v.ID, userID))

	err = svc.DeleteWorkflow(ctx, actor, wf.ID)
	if xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("delete with runs = %v, want conflict", err)
	}
	if _, err := svc.GetWorkflow(ctx, wf.ID); err != nil {
		t.Fatalf("workflow must survive the blocked delete: %v", err)
	}
}
```

(Match the file's actual helper names — `newTestService`/`testAdmin` stand for whatever that file already uses to construct the service and an admin principal; reuse its `validDoc` fixture. Add the `pgx` and `uuid` imports if missing.)

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/runs/ -run 'CreateTask|Rerun|GrantScoped'`
Expected: FAIL — service methods undefined.

- [ ] **Step 4: Implement intake**

`backend/internal/runs/tasks.go`:

```go
package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// Intake validation pins (plan header; bytes, house convention).
const (
	maxTitleLen  = 200
	maxInputsLen = 8 << 10
)

type NewTask struct {
	RepoID          uuid.UUID
	WorkflowID      uuid.UUID
	WorkflowVersion int32
	Title           string
	Inputs          map[string]any
}

type Task struct {
	ID                uuid.UUID
	ProjectID         uuid.UUID
	RepoID            uuid.UUID
	WorkflowVersionID uuid.UUID
	RequesterID       uuid.UUID
	Title             string
	Inputs            map[string]any
	CreatedAt         time.Time
	LatestRunID       uuid.UUID
	LatestRunStatus   RunStatus
}

type Run struct {
	ID              uuid.UUID
	TaskID          uuid.UUID
	Status          RunStatus
	DefinitionHash  string
	BaseSHA         string
	Snapshot        PolicySnapshot
	ExecutionUsedMS int64
	WallDeadline    time.Time
	Outcome         *Outcome
	CreatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
}

type Phase struct {
	ID          uuid.UUID
	NodeID      string
	Attempt     int32
	MaxAttempts int32
	Status      PhaseStatus
	WorkerID    string
	Outcome     *Outcome
	CreatedAt   time.Time
	FinishedAt  *time.Time
}

type ArtifactMeta struct {
	ID        uuid.UUID
	NodeID    string
	Attempt   int32
	Type      string
	ParentIDs []uuid.UUID
	CreatedAt time.Time
}

type RunEventRecord struct {
	ID         int64
	Kind       string
	Payload    map[string]any
	OccurredAt time.Time
}

func validateNewTask(in NewTask) error {
	e := xerrors.E(xerrors.ClassInvalid, "validation failed")
	ok := true
	if in.Title == "" || len(in.Title) > maxTitleLen {
		e = e.WithFields(xerrors.FieldError{Field: "title", Message: fmt.Sprintf("must be 1-%d characters", maxTitleLen)})
		ok = false
	}
	if in.WorkflowVersion < 1 {
		e = e.WithFields(xerrors.FieldError{Field: "workflow_version", Message: "must be >= 1"})
		ok = false
	}
	if in.Inputs != nil {
		raw, err := json.Marshal(in.Inputs)
		if err != nil || len(raw) > maxInputsLen {
			e = e.WithFields(xerrors.FieldError{Field: "inputs", Message: fmt.Sprintf("must be a JSON object of at most %d bytes", maxInputsLen)})
			ok = false
		}
	}
	if !ok {
		return e
	}
	return nil
}

// RepoProject resolves a repo to its project for the API's pre-write
// authorization check (run grant on the repo's project, spec §6).
func (s *Service) RepoProject(ctx context.Context, repoID uuid.UUID) (uuid.UUID, error) {
	repo, err := s.repos.Repo(ctx, repoID)
	if err != nil {
		return uuid.Nil, err
	}
	return repo.ProjectID, nil
}

// resolveIntake pins everything a run needs before any row exists:
// repo, definition (compiled, gates refused until Plan 08), per-node
// harness refs, snapshot, base SHA.
func (s *Service) resolveIntake(ctx context.Context, in NewTask) (RepoRef, PinnedDefinition, string, error) {
	repo, err := s.repos.Repo(ctx, in.RepoID)
	if err != nil {
		return RepoRef{}, PinnedDefinition{}, "", err
	}
	def, err := s.defs.Definition(ctx, in.WorkflowID, in.WorkflowVersion)
	if err != nil {
		return RepoRef{}, PinnedDefinition{}, "", err
	}
	plan, err := compilePinned(def)
	if err != nil {
		return RepoRef{}, PinnedDefinition{}, "", err
	}
	if unsupported := plan.HasUnsupportedNodes(); len(unsupported) > 0 {
		return RepoRef{}, PinnedDefinition{}, "", xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "definition", Message: fmt.Sprintf("gates and publishing arrive in Plan 08 (nodes: %v)", unsupported)})
	}
	for _, id := range plan.Order {
		n := plan.Nodes[id]
		if n.Harness == "" {
			continue
		}
		if _, err := s.harnesses.HarnessByName(ctx, n.Harness); err != nil {
			if xerrors.ClassOf(err) == xerrors.ClassNotFound {
				return RepoRef{}, PinnedDefinition{}, "", xerrors.E(xerrors.ClassInvalid, "validation failed").
					WithFields(xerrors.FieldError{Field: "harness", Message: fmt.Sprintf("node %q references unknown harness %q", id, n.Harness)})
			}
			return RepoRef{}, PinnedDefinition{}, "", err
		}
	}
	baseSHA, err := s.forge.ResolveHead(ctx, repo)
	if err != nil {
		return RepoRef{}, PinnedDefinition{}, "", fmt.Errorf("runs: resolve head: %w", err)
	}
	return repo, def, baseSHA, nil
}

// insertRun mints one pending run for a task inside tx: snapshot pinned
// as jsonb, budget and wall deadline denormalized for SQL predicates.
func (s *Service) insertRun(ctx context.Context, tx pgx.Tx, taskID, projectID uuid.UUID,
	def PinnedDefinition, baseSHA, correlationID string) (Run, error) {
	snap := s.params.Snapshot
	if err := snap.Validate(); err != nil {
		return Run{}, fmt.Errorf("runs: params snapshot: %w", err)
	}
	rawSnap, err := json.Marshal(snap)
	if err != nil {
		return Run{}, fmt.Errorf("runs: marshal snapshot: %w", err)
	}
	now := s.clock.Now()
	row, err := store.New(tx).InsertRun(ctx, store.InsertRunParams{
		TaskID: taskID, DefinitionHash: def.ContentHash, BaseSha: baseSHA,
		PolicySnapshot: rawSnap, ExecutionBudgetMs: snap.ExecutionBudgetMS,
		WallDeadline: now.Add(time.Duration(snap.WallClockMS) * time.Millisecond), CreatedAt: now,
	})
	if err != nil {
		return Run{}, fmt.Errorf("runs: insert run: %w", err)
	}
	if err := appendEvents(ctx, tx, now, []Event{{
		RunID: row.ID, ProjectID: projectID, Kind: EventRunCreated, CorrelationID: correlationID,
		Payload: map[string]any{
			"task_id": taskID.String(), "workflow_version_id": def.VersionID.String(),
			"definition_hash": def.ContentHash, "base_sha": baseSHA,
		},
	}}); err != nil {
		return Run{}, err
	}
	return runFromRow(row)
}

func (s *Service) CreateTask(ctx context.Context, actor authn.Principal, in NewTask) (Task, Run, error) {
	if err := validateNewTask(in); err != nil {
		return Task{}, Run{}, err
	}
	repo, def, baseSHA, err := s.resolveIntake(ctx, in)
	if err != nil {
		return Task{}, Run{}, err
	}
	inputs := in.Inputs
	if inputs == nil {
		inputs = map[string]any{}
	}
	rawInputs, err := json.Marshal(inputs)
	if err != nil {
		return Task{}, Run{}, xerrors.Wrap(xerrors.ClassInvalid, "inputs must be a JSON object", err)
	}

	var task Task
	var run Run
	correlationID := httpx.CorrelationID(ctx)
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.InsertTask(ctx, store.InsertTaskParams{
			ProjectID: repo.ProjectID, RepoID: repo.ID, WorkflowVersionID: def.VersionID,
			Title: in.Title, Inputs: rawInputs, RequesterID: actor.UserID, CreatedAt: s.clock.Now(),
		})
		if err != nil {
			return fmt.Errorf("runs: insert task: %w", err)
		}
		run, err = s.insertRun(ctx, tx, row.ID, repo.ProjectID, def, baseSHA, correlationID)
		if err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "task.created", SubjectKind: "task", SubjectID: row.ID.String(),
			CorrelationID: correlationID,
			Detail:        map[string]any{"title": in.Title, "repo_id": repo.ID.String(), "workflow_version_id": def.VersionID.String()},
		}); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "run.created", SubjectKind: "run", SubjectID: run.ID.String(),
			CorrelationID: correlationID,
		}); err != nil {
			return err
		}
		task = Task{
			ID: row.ID, ProjectID: row.ProjectID, RepoID: row.RepoID, WorkflowVersionID: row.WorkflowVersionID,
			RequesterID: row.RequesterID, Title: row.Title, Inputs: inputs, CreatedAt: row.CreatedAt,
			LatestRunID: run.ID, LatestRunStatus: run.Status,
		}
		return nil
	})
	if err != nil {
		return Task{}, Run{}, err
	}
	return task, run, nil
}

// Rerun supersedes a pending current run (supersede-before-work,
// invariant 12) or refuses a live one, then mints the next run. The
// task's newest run is locked FOR UPDATE: two concurrent reruns
// serialize, the loser sees the winner's pending run and refuses.
func (s *Service) Rerun(ctx context.Context, actor authn.Principal, taskID uuid.UUID) (Run, error) {
	correlationID := httpx.CorrelationID(ctx)
	var run Run
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		taskRow, err := q.GetTask(ctx, taskID)
		if err != nil {
			return notFound(err, "no such task")
		}
		latest, err := q.LatestRunForUpdate(ctx, taskID)
		if err != nil {
			return fmt.Errorf("runs: latest run: %w", err)
		}
		now := s.clock.Now()
		switch RunStatus(latest.Status) {
		case RunPending:
			superseded := Outcome{Class: OutcomePermanent, Code: CodeSuperseded, Message: "superseded by re-run"}
			raw, _ := json.Marshal(superseded)
			n, err := q.FinishRun(ctx, store.FinishRunParams{
				ID: latest.ID, ToStatus: string(RunSuperseded), Outcome: raw, FinishedAt: &now,
			})
			if err != nil || n == 0 {
				return fmt.Errorf("runs: supersede: rows=%d err=%w", n, err)
			}
			if _, err := q.SupersedeLivePhases(ctx, store.SupersedeLivePhasesParams{
				RunID: latest.ID, Outcome: raw, FinishedAt: &now,
			}); err != nil {
				return fmt.Errorf("runs: supersede phases: %w", err)
			}
			if err := s.audit.Record(ctx, tx, audit.Entry{
				Actor: actorID(actor), Action: "run.superseded", SubjectKind: "run", SubjectID: latest.ID.String(),
				CorrelationID: correlationID,
			}); err != nil {
				return err
			}
		case RunRunning, RunWaiting:
			return xerrors.E(xerrors.ClassConflict, "run is in flight; cancel arrives in Plan 09")
		}

		// Re-resolve pins fresh (plan decision 4). Version is pinned on
		// the task; look its coordinates up via the definition source.
		repo, err := s.repos.Repo(ctx, taskRow.RepoID)
		if err != nil {
			return err
		}
		def, err := s.definitionByVersionID(ctx, taskRow.WorkflowVersionID)
		if err != nil {
			return err
		}
		baseSHA, err := s.forge.ResolveHead(ctx, repo)
		if err != nil {
			return fmt.Errorf("runs: resolve head: %w", err)
		}
		run, err = s.insertRun(ctx, tx, taskID, taskRow.ProjectID, def, baseSHA, correlationID)
		if err != nil {
			return err
		}
		if RunStatus(latest.Status) == RunPending {
			if err := appendEvents(ctx, tx, now, []Event{{
				RunID: latest.ID, ProjectID: taskRow.ProjectID, Kind: EventRunSuperseded,
				CorrelationID: correlationID, Payload: map[string]any{"by_run_id": run.ID.String()},
			}}); err != nil {
				return err
			}
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "run.created", SubjectKind: "run", SubjectID: run.ID.String(),
			CorrelationID: correlationID,
		})
	})
	if err != nil {
		return Run{}, err
	}
	return run, nil
}
```

`definitionByVersionID` re-resolves a task's pinned version through the port's second method (Task 3 declared it; Task 11's app adapter backs it with a new `workflows.Service.GetVersionByID`):

```go
func (s *Service) definitionByVersionID(ctx context.Context, versionID uuid.UUID) (PinnedDefinition, error) {
	def, err := s.defs.DefinitionByVersionID(ctx, versionID)
	if err != nil {
		return PinnedDefinition{}, err
	}
	if _, err := compilePinned(def); err != nil {
		return PinnedDefinition{}, err
	}
	return def, nil
}
```

Reads (`tasks.go`, continued) — row mappers plus the query pass-throughs; write them all fully:

```go
func runFromRow(r store.Run) (Run, error) {
	snap, err := snapshotFromRow(r.PolicySnapshot)
	if err != nil {
		return Run{}, err
	}
	out, err := outcomeFromRow(r.Outcome)
	if err != nil {
		return Run{}, err
	}
	return Run{
		ID: r.ID, TaskID: r.TaskID, Status: RunStatus(r.Status),
		DefinitionHash: r.DefinitionHash, BaseSHA: r.BaseSha, Snapshot: snap,
		ExecutionUsedMS: r.ExecutionUsedMs, WallDeadline: r.WallDeadline, Outcome: out,
		CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}, nil
}

func taskFromRow(t store.Task, latestRunID uuid.UUID, latestStatus string) (Task, error) {
	inputs, err := unmarshalMap(t.Inputs)
	if err != nil {
		return Task{}, err
	}
	return Task{
		ID: t.ID, ProjectID: t.ProjectID, RepoID: t.RepoID, WorkflowVersionID: t.WorkflowVersionID,
		RequesterID: t.RequesterID, Title: t.Title, Inputs: inputs, CreatedAt: t.CreatedAt,
		LatestRunID: latestRunID, LatestRunStatus: RunStatus(latestStatus),
	}, nil
}

func (s *Service) GetTask(ctx context.Context, id uuid.UUID) (Task, error) {
	q := store.New(s.pool)
	row, err := q.GetTask(ctx, id)
	if err != nil {
		return Task{}, notFound(err, "no such task")
	}
	latest, err := q.ListRunsForTask(ctx, id)
	if err != nil || len(latest) == 0 {
		return Task{}, fmt.Errorf("runs: task %s has no runs: %w", id, err)
	}
	return taskFromRow(row, latest[0].ID, latest[0].Status)
}

func (s *Service) ListTasks(ctx context.Context) ([]Task, error) {
	rows, err := store.New(s.pool).ListTasks(ctx)
	if err != nil {
		return nil, fmt.Errorf("runs: list tasks: %w", err)
	}
	out := make([]Task, 0, len(rows))
	for _, r := range rows {
		t, err := taskFromRow(r.Task, r.LatestRunID, r.LatestRunStatus)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (s *Service) ListTasksByProjects(ctx context.Context, ids []uuid.UUID) ([]Task, error) {
	if len(ids) == 0 {
		return []Task{}, nil
	}
	rows, err := store.New(s.pool).ListTasksByProjects(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("runs: list tasks by projects: %w", err)
	}
	out := make([]Task, 0, len(rows))
	for _, r := range rows {
		t, err := taskFromRow(r.Task, r.LatestRunID, r.LatestRunStatus)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func phaseFromRow(r store.PhaseRun) (Phase, error) {
	out, err := outcomeFromRow(r.Outcome)
	if err != nil {
		return Phase{}, err
	}
	worker := ""
	if r.WorkerID != nil {
		worker = *r.WorkerID
	}
	return Phase{
		ID: r.ID, NodeID: r.NodeID, Attempt: r.Attempt, MaxAttempts: r.MaxAttempts,
		Status: PhaseStatus(r.Status), WorkerID: worker, Outcome: out,
		CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt,
	}, nil
}

func (s *Service) GetRun(ctx context.Context, id uuid.UUID) (Run, []Phase, []ArtifactMeta, error) {
	q := store.New(s.pool)
	row, err := q.GetRun(ctx, id)
	if err != nil {
		return Run{}, nil, nil, notFound(err, "no such run")
	}
	run, err := runFromRow(row)
	if err != nil {
		return Run{}, nil, nil, err
	}
	phaseRows, err := q.ListPhasesForRun(ctx, id)
	if err != nil {
		return Run{}, nil, nil, fmt.Errorf("runs: list phases: %w", err)
	}
	phases := make([]Phase, 0, len(phaseRows))
	for _, pr := range phaseRows {
		p, err := phaseFromRow(pr)
		if err != nil {
			return Run{}, nil, nil, err
		}
		phases = append(phases, p)
	}
	artRows, err := q.ListArtifactsForRun(ctx, id)
	if err != nil {
		return Run{}, nil, nil, fmt.Errorf("runs: list artifacts: %w", err)
	}
	arts := make([]ArtifactMeta, 0, len(artRows))
	for _, ar := range artRows {
		var parentStrs []string
		if err := json.Unmarshal(ar.ParentIds, &parentStrs); err != nil {
			return Run{}, nil, nil, fmt.Errorf("runs: artifact lineage corrupt: %w", err)
		}
		parents := make([]uuid.UUID, 0, len(parentStrs))
		for _, ps := range parentStrs {
			pid, err := uuid.Parse(ps)
			if err != nil {
				return Run{}, nil, nil, fmt.Errorf("runs: artifact lineage corrupt: %w", err)
			}
			parents = append(parents, pid)
		}
		arts = append(arts, ArtifactMeta{
			ID: ar.ID, NodeID: ar.NodeID, Attempt: ar.Attempt, Type: ar.Type,
			ParentIDs: parents, CreatedAt: ar.CreatedAt,
		})
	}
	return run, phases, arts, nil
}

func (s *Service) ListRunsForTask(ctx context.Context, taskID uuid.UUID) ([]Run, error) {
	if _, err := store.New(s.pool).GetTask(ctx, taskID); err != nil {
		return nil, notFound(err, "no such task")
	}
	rows, err := store.New(s.pool).ListRunsForTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("runs: list runs: %w", err)
	}
	out := make([]Run, 0, len(rows))
	for _, r := range rows {
		run, err := runFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

// eventPageCap bounds the poll endpoint until SSE lands in Plan 10.
const eventPageCap = 500

func (s *Service) ListRunEvents(ctx context.Context, runID uuid.UUID, afterID int64, limit int32) ([]RunEventRecord, error) {
	if _, err := store.New(s.pool).GetRun(ctx, runID); err != nil {
		return nil, notFound(err, "no such run")
	}
	if limit < 1 || limit > eventPageCap {
		limit = eventPageCap
	}
	rows, err := store.New(s.pool).ListRunEventsAfter(ctx, store.ListRunEventsAfterParams{
		RunID: runID, ID: afterID, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("runs: list events: %w", err)
	}
	out := make([]RunEventRecord, 0, len(rows))
	for _, r := range rows {
		payload, err := unmarshalMap(r.Payload)
		if err != nil {
			return nil, err
		}
		out = append(out, RunEventRecord{ID: r.ID, Kind: r.Kind, Payload: payload, OccurredAt: r.OccurredAt})
	}
	return out, nil
}
```

(sqlc's exact generated field casing — `BaseSha`, `ExecutionUsedMs`, `ParentIds` — is authoritative; adjust the mappers to what `task db:sqlc` actually emitted rather than fighting it. Pointer-vs-value for nullable timestamptz follows the existing overrides: nullable → `*time.Time`.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/runs/ ./internal/workflows/`
Expected: PASS, including the workflow-delete-blocked proof.

- [ ] **Step 6: Full gate and commit**

Run: `task backend:ci`
Expected: PASS (txboundary: every tx here touches only spine tables + audit — platform is shared kernel, allowed)

```bash
git add backend/internal/runs backend/internal/workflows
git commit -m "feat(backend): task intake with supersede-before-work and pinned runs"
```
### Task 8: The queue — claim, lease, heartbeat, fencing

PhaseRun-as-queue mechanics as unexported Service methods (the orchestrator composes them in Task 9), proven under the spec §7 integration bullets that don't need the full loop: concurrent claim exclusivity, attempt fencing by row identity, transactional deadline/budget predicates, heartbeat fencing.

**Files:**
- Create: `backend/internal/runs/queue.go`
- Create: `backend/internal/runs/queue_integration_test.go` (build tag `integration`)
- Modify: `backend/internal/runs/export_integration_test.go` (test exports for the new seams)

**Interfaces:**
- Consumes: Tasks 4–7.
- Produces (package-internal; Task 9 composes these):
  - `runs.claimed{PhaseID uuid.UUID; Ref AttemptRef; MaxAttempts int32; RunWallDeadline time.Time; BudgetMS, UsedMS int64}`
  - `(*Service).claimNext(ctx, workerID string, leaseSeconds float64) (claimed, bool, error)` — the single-statement CTE claim; `false` = nothing claimable
  - `(*Service).heartbeat(ctx, phaseID uuid.UUID, workerID string, leaseSeconds float64) (bool, error)` — `false` = fenced (row no longer running as this worker)
  - `(*Service).insertPhaseTx(ctx, tx, runID uuid.UUID, nodeID string, attempt, maxAttempts int32) (uuid.UUID, error)` — pending row + nothing else (events are the caller's)
  - Test exports: `ClaimNextForTest`, `HeartbeatForTest`, `InsertPhaseForTest`, `FinishPhaseForTest` (thin wrappers in the export file)

- [ ] **Step 1: Write the failing integration tests**

`backend/internal/runs/queue_integration_test.go`:

```go
//go:build integration

package runs_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// makePending inserts a running run with one pending phase, raw.
func makePending(t *testing.T, f fixture) (runID, phaseID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	_, runID = f.newRunRow(t, "running")
	err := f.pool.QueryRow(ctx,
		`INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, status, created_at)
		 VALUES ($1, 'plan', 1, 3, 'pending', now()) RETURNING id`, runID,
	).Scan(&phaseID)
	if err != nil {
		t.Fatal(err)
	}
	return runID, phaseID
}

func TestClaimIsExclusiveUnderConcurrency(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	_, phaseID := makePending(t, f)

	const workers = 8
	var wg sync.WaitGroup
	wins := make(chan uuid.UUID, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			c, ok, err := runs.ClaimNextForTest(context.Background(), svc, uuid.NewString(), 30)
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				wins <- c.PhaseID
			}
		}(i)
	}
	wg.Wait()
	close(wins)
	var won []uuid.UUID
	for id := range wins {
		won = append(won, id)
	}
	if len(won) != 1 || won[0] != phaseID {
		t.Errorf("claims won = %v, want exactly [%s]", won, phaseID)
	}
}

func TestClaimHonorsDeadlineAndBudgetPredicates(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	// Expired wall deadline: pending phase exists, claim must refuse.
	runID, _ := makePending(t, f)
	if _, err := f.pool.Exec(ctx, `UPDATE runs SET wall_deadline = now() - interval '1 second' WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := runs.ClaimNextForTest(ctx, svc, "w", 30); err != nil || ok {
		t.Errorf("expired-deadline claim: ok=%v err=%v, want no claim", ok, err)
	}

	// Exhausted budget: same refusal.
	runID2, _ := makePending(t, f)
	if _, err := f.pool.Exec(ctx, `UPDATE runs SET execution_used_ms = execution_budget_ms WHERE id = $1`, runID2); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := runs.ClaimNextForTest(ctx, svc, "w", 30); err != nil || ok {
		t.Errorf("exhausted-budget claim: ok=%v err=%v, want no claim", ok, err)
	}
}

func TestStaleWorkerIsFencedByRowIdentity(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	ctx := context.Background()
	runID, _ := makePending(t, f)

	c, ok, err := runs.ClaimNextForTest(ctx, svc, "worker-a", 30)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	// The run terminalizes underneath worker-a (deadline sweep in prod).
	if _, err := f.pool.Exec(ctx,
		`UPDATE phase_runs SET status='superseded', finished_at=now() WHERE id=$1`, c.PhaseID); err != nil {
		t.Fatal(err)
	}
	// worker-a's terminal CAS must lose: 0 rows.
	n, err := runs.FinishPhaseForTest(ctx, svc, c.PhaseID, "running", "succeeded", "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("stale worker CAS must affect 0 rows")
	}
	// And its heartbeat must report fenced.
	alive, err := runs.HeartbeatForTest(ctx, svc, c.PhaseID, "worker-a", 30)
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Error("heartbeat after supersede must report fenced")
	}
	_ = runID
}

func TestLivePhaseUniquenessIsStructural(t *testing.T) {
	f := newFixture(t)
	_, runID := f.newRunRow(t, "running")
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, status, created_at)
		 VALUES ($1, 'plan', 1, 3, 'pending', now())`, runID); err != nil {
		t.Fatal(err)
	}
	// A second live row for the same (run, node) must hit the partial
	// unique index regardless of attempt number.
	_, err := f.pool.Exec(ctx,
		`INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, status, created_at)
		 VALUES ($1, 'plan', 2, 3, 'pending', now())`, runID)
	if err == nil {
		t.Fatal("second live row for (run, node) must violate phase_runs_live_key")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/runs/ -run 'Claim|Fenced|LivePhase'`
Expected: FAIL — test exports undefined.

- [ ] **Step 3: Implement the queue**

`backend/internal/runs/queue.go`:

```go
package runs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// claimed is one leased attempt with the run columns every downstream
// decision needs (deadline, budget) — read in the claim statement itself
// so no second query can race the predicates.
type claimed struct {
	PhaseID         uuid.UUID
	Ref             AttemptRef
	MaxAttempts     int32
	RunWallDeadline time.Time
	BudgetMS        int64
	UsedMS          int64
	ClaimedAt       time.Time
}

// claimNext is the single-statement claim (spec §3): a locking CTE
// (pending row, run running, wall deadline unexpired, budget unexhausted,
// attempt within bounds — defense-in-depth) feeding the CAS UPDATE that
// takes the lease. SKIP LOCKED makes workers non-blocking.
func (s *Service) claimNext(ctx context.Context, workerID string, leaseSeconds float64) (claimed, bool, error) {
	now := s.clock.Now()
	row, err := store.New(s.pool).ClaimPhase(ctx, store.ClaimPhaseParams{
		WorkerID: &workerID, LeaseSeconds: leaseSeconds, ClaimedAt: &now,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return claimed{}, false, nil
	}
	if err != nil {
		return claimed{}, false, fmt.Errorf("runs: claim: %w", err)
	}
	return claimed{
		PhaseID:         row.ID,
		Ref:             AttemptRef{RunID: row.RunID, NodeID: row.NodeID, Attempt: row.Attempt},
		MaxAttempts:     row.MaxAttempts,
		RunWallDeadline: row.RunWallDeadline,
		BudgetMS:        row.ExecutionBudgetMs,
		UsedMS:          row.ExecutionUsedMs,
		ClaimedAt:       now,
	}, true, nil
}

// heartbeat extends the lease; false means fenced — the row is no
// longer running as this worker and the caller must abandon the attempt.
func (s *Service) heartbeat(ctx context.Context, phaseID uuid.UUID, workerID string, leaseSeconds float64) (bool, error) {
	n, err := store.New(s.pool).HeartbeatPhase(ctx, store.HeartbeatPhaseParams{
		ID: phaseID, WorkerID: &workerID, LeaseSeconds: leaseSeconds,
	})
	if err != nil {
		return false, fmt.Errorf("runs: heartbeat: %w", err)
	}
	return n > 0, nil
}

// insertPhaseTx inserts one pending attempt row inside the caller's tx.
func (s *Service) insertPhaseTx(ctx context.Context, tx pgx.Tx, runID uuid.UUID, nodeID string, attempt, maxAttempts int32) (uuid.UUID, error) {
	row, err := store.New(tx).InsertPhase(ctx, store.InsertPhaseParams{
		RunID: runID, NodeID: nodeID, Attempt: attempt, MaxAttempts: maxAttempts, CreatedAt: s.clock.Now(),
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("runs: insert phase %s/%s#%d: %w", runID, nodeID, attempt, err)
	}
	return row.ID, nil
}

// finishPhase is the fencing CAS shared by workers and sweepers:
// from-status plus worker identity (empty worker = system actor).
func (s *Service) finishPhase(ctx context.Context, tx pgx.Tx, phaseID uuid.UUID,
	from, to PhaseStatus, workerID string, outcome []byte) (int64, error) {
	if !from.CanTransition(to) {
		return 0, fmt.Errorf("runs: illegal phase transition %s -> %s", from, to)
	}
	now := s.clock.Now()
	n, err := store.New(tx).FinishPhase(ctx, store.FinishPhaseParams{
		ID: phaseID, FromStatus: string(from), ToStatus: string(to),
		WorkerID: workerID, Outcome: outcome, FinishedAt: &now,
	})
	if err != nil {
		return 0, fmt.Errorf("runs: finish phase: %w", err)
	}
	return n, nil
}
```

(Adjust nullable-pointer shapes to sqlc's generated params exactly; `FinishPhase`'s `worker_id = sqlc.arg(worker_id) OR sqlc.arg(worker_id) = ''` compares text — pass plain strings if the generated param is `string`.)

Append to `backend/internal/runs/export_integration_test.go`:

```go
type Claimed = claimed

func ClaimNextForTest(ctx context.Context, s *Service, workerID string, leaseSeconds float64) (Claimed, bool, error) {
	return s.claimNext(ctx, workerID, leaseSeconds)
}

func HeartbeatForTest(ctx context.Context, s *Service, phaseID uuid.UUID, workerID string, leaseSeconds float64) (bool, error) {
	return s.heartbeat(ctx, phaseID, workerID, leaseSeconds)
}

func InsertPhaseForTest(ctx context.Context, s *Service, runID uuid.UUID, nodeID string, attempt, maxAttempts int32) (uuid.UUID, error) {
	var id uuid.UUID
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		id, err = s.insertPhaseTx(ctx, tx, runID, nodeID, attempt, maxAttempts)
		return err
	})
	return id, err
}

func FinishPhaseForTest(ctx context.Context, s *Service, phaseID uuid.UUID, from, to, workerID string) (int64, error) {
	var n int64
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		n, err = s.finishPhase(ctx, tx, phaseID, PhaseStatus(from), PhaseStatus(to), workerID, nil)
		return err
	})
	return n, err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/runs/`
Expected: PASS

- [ ] **Step 5: Full gate and commit**

Run: `task backend:ci`
Expected: PASS

```bash
git add backend/internal/runs
git commit -m "feat(backend): phase-run queue with claim cte, leases, and fencing"
```

### Task 9: The orchestrator — workers, outcome evaluation, sweepers, reconciliation

The loop that makes Postgres the dispatch authority visible: a janitor goroutine (run startup, wall-deadline sweep, lease-expiry → `reconciling` → reconcile, outbox retention) plus N claim workers (execute via the port, heartbeat, evaluate the outcome in one transaction: artifacts + lineage, usage accounting, retry-insertion or exhaustion, successor materialization, run completion). Integration-tested end-to-end against the fake executor: happy path, retry, exhaustion, wall-deadline death, reconcile-after-hang, budget breach.

**Files:**
- Create: `backend/internal/runs/orchestrator.go`
- Create: `backend/internal/runs/orchestrator_integration_test.go` (build tag `integration`)

**Interfaces:**
- Consumes: everything above.
- Produces:
  - `runs.OrchestratorConfig{Workers int; PollInterval, LeaseTTL, HeartbeatInterval, OutboxRetention time.Duration}`
  - `runs.NewOrchestrator(svc *Service, exec Executor, cfg OrchestratorConfig, log *slog.Logger) *Orchestrator`
  - `(*Orchestrator).Run(ctx context.Context) error` — blocks until ctx cancels, then drains workers
  - Audit written here: `run.finished` (actor NULL) on every terminal transition the orchestrator performs

- [ ] **Step 1: Write the failing integration tests**

`backend/internal/runs/orchestrator_integration_test.go`:

```go
//go:build integration

package runs_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

// startOrchestrator boots the loop against a fake executor and returns
// it; cleanup cancels and waits for drain.
func startOrchestrator(t *testing.T, svc *runs.Service, ex runs.Executor, lease time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	o := runs.NewOrchestrator(svc, ex, runs.OrchestratorConfig{
		Workers: 2, PollInterval: 20 * time.Millisecond, LeaseTTL: lease,
		HeartbeatInterval: lease / 3, OutboxRetention: 7 * 24 * time.Hour,
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	go func() { defer close(done); _ = o.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

// waitForRun polls until the run reaches a terminal status.
func waitForRun(t *testing.T, svc *runs.Service, runID uuid.UUID, want runs.RunStatus) runs.Run {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		run, _, _, err := svc.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == want {
			return run
		}
		if run.Status.Terminal() {
			t.Fatalf("run reached %s (outcome %+v), want %s", run.Status, run.Outcome, want)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("run never reached %s", want)
	return runs.Run{}
}

func TestConveyorRunsToCompletion(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	startOrchestrator(t, svc, fake.NewExecutor(), 30*time.Second)
	ctx := context.Background()

	_, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "happy",
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForRun(t, svc, run.ID, runs.RunSucceeded)
	if final.ExecutionUsedMS <= 0 {
		t.Error("execution usage must be accounted")
	}

	_, phases, arts, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 3 {
		t.Fatalf("phases = %d, want 3", len(phases))
	}
	for _, p := range phases {
		if p.Status != runs.PhaseSucceeded || p.Attempt != 1 {
			t.Errorf("phase %s: %s attempt %d", p.NodeID, p.Status, p.Attempt)
		}
	}
	// Lineage: impl's patch descends from plan's artifact; review's
	// verdict descends from impl's patch (spec §2 recorded lineage).
	byNode := map[string]runs.ArtifactMeta{}
	for _, a := range arts {
		byNode[a.NodeID] = a
	}
	if len(byNode["impl"].ParentIDs) != 1 || byNode["impl"].ParentIDs[0] != byNode["plan"].ID {
		t.Errorf("impl lineage = %v, want [%s]", byNode["impl"].ParentIDs, byNode["plan"].ID)
	}
	if len(byNode["review"].ParentIDs) != 1 || byNode["review"].ParentIDs[0] != byNode["impl"].ID {
		t.Errorf("review lineage = %v", byNode["review"].ParentIDs)
	}

	// Events: ids strictly ascending, lifecycle bookends present.
	evs, err := svc.ListRunEvents(ctx, run.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	last := int64(0)
	for _, e := range evs {
		if e.ID <= last {
			t.Fatalf("event ids not ascending: %d after %d", e.ID, last)
		}
		last = e.ID
		kinds[e.Kind]++
	}
	for _, want := range []string{"run.created", "run.started", "run.succeeded"} {
		if kinds[want] != 1 {
			t.Errorf("event %s count = %d, want 1", want, kinds[want])
		}
	}
	if kinds["phase.succeeded"] != 3 || kinds["phase.materialized"] != 3 || kinds["phase.started"] != 3 {
		t.Errorf("phase event counts: %v", kinds)
	}
}

func TestTransientFailureRetriesThenSucceeds(t *testing.T) {
	f := newFixture(t)
	doc := agenticDoc()
	doc.Nodes[1].Instructions = "Implement the plan.\ncaesar-fake: fail attempts=1 class=transient code=harness_error"
	svc, _ := newService(t, f, doc, runs.DefaultSnapshot())
	startOrchestrator(t, svc, fake.NewExecutor(), 30*time.Second)
	ctx := context.Background()

	_, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "retry",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, svc, run.ID, runs.RunSucceeded)

	_, phases, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var implStatuses []string
	for _, p := range phases {
		if p.NodeID == "impl" {
			implStatuses = append(implStatuses, string(p.Status)+"#"+string(rune('0'+p.Attempt)))
		}
	}
	if len(implStatuses) != 2 || implStatuses[0] != "failed#1" || implStatuses[1] != "succeeded#2" {
		t.Errorf("impl attempts = %v, want [failed#1 succeeded#2]", implStatuses)
	}
	evs, _ := svc.ListRunEvents(ctx, run.ID, 0, 500)
	retried := 0
	for _, e := range evs {
		if e.Kind == "phase.retried" {
			retried++
		}
	}
	if retried != 1 {
		t.Errorf("phase.retried events = %d, want 1", retried)
	}
}

func TestExhaustionKillsTheRun(t *testing.T) {
	f := newFixture(t)
	doc := agenticDoc()
	doc.Nodes[1].Instructions = "Implement.\ncaesar-fake: fail attempts=99 class=transient code=harness_error"
	snap := runs.DefaultSnapshot()
	snap.MaxAttempts = 2
	svc, _ := newService(t, f, doc, snap)
	startOrchestrator(t, svc, fake.NewExecutor(), 30*time.Second)
	ctx := context.Background()

	_, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "exhaust",
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForRun(t, svc, run.ID, runs.RunFailed)
	if final.Outcome == nil || final.Outcome.Code != runs.CodeAttemptsExhausted {
		t.Errorf("run outcome = %+v, want attempts_exhausted", final.Outcome)
	}
	_, phases, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range phases {
		if p.NodeID == "impl" && p.Attempt == 2 && p.Status != runs.PhaseDead {
			t.Errorf("final impl attempt = %s, want dead", p.Status)
		}
		if p.NodeID == "review" {
			t.Error("review must never materialize after impl died")
		}
	}
}

func TestWallDeadlineTerminatesTheRun(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	ctx := context.Background()
	_, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "late",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Expire the wall clock before the orchestrator ever sees the run.
	if _, err := f.pool.Exec(ctx, `UPDATE runs SET wall_deadline = now() - interval '1 second' WHERE id = $1`, run.ID); err != nil {
		t.Fatal(err)
	}
	startOrchestrator(t, svc, fake.NewExecutor(), 30*time.Second)
	final := waitForRun(t, svc, run.ID, runs.RunDead)
	if final.Outcome == nil || final.Outcome.Code != runs.CodeWallDeadlineExceeded {
		t.Errorf("outcome = %+v, want wall_deadline_exceeded", final.Outcome)
	}
}

func TestHangingAttemptReconcilesAndExhausts(t *testing.T) {
	f := newFixture(t)
	doc := agenticDoc()
	doc.Nodes[0].Instructions = "caesar-fake: hang"
	snap := runs.DefaultSnapshot()
	snap.MaxAttempts = 2
	svc, _ := newService(t, f, doc, snap)
	// Short lease: hang -> lease expires -> reconciling -> retry -> hang
	// -> reconciling -> dead (exhausted) -> run failed.
	startOrchestrator(t, svc, fake.NewExecutor(), 300*time.Millisecond)
	ctx := context.Background()

	_, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "hang",
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForRun(t, svc, run.ID, runs.RunFailed)
	if final.Outcome == nil || final.Outcome.Code != runs.CodeAttemptsExhausted {
		t.Errorf("outcome = %+v", final.Outcome)
	}
	evs, _ := svc.ListRunEvents(ctx, run.ID, 0, 500)
	reconciling := 0
	for _, e := range evs {
		if e.Kind == "phase.reconciling" {
			reconciling++
		}
	}
	if reconciling < 2 {
		t.Errorf("phase.reconciling events = %d, want >= 2", reconciling)
	}
}

func TestBudgetBreachFailsTheRun(t *testing.T) {
	f := newFixture(t)
	snap := runs.DefaultSnapshot()
	snap.ExecutionBudgetMS = 1 // any real attempt costs more than 1ms
	svc, _ := newService(t, f, agenticDoc(), snap)
	startOrchestrator(t, svc, fake.NewExecutor(), 30*time.Second)
	ctx := context.Background()

	_, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "budget",
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForRun(t, svc, run.ID, runs.RunFailed)
	if final.Outcome == nil || final.Outcome.Code != runs.CodeBudgetExhausted {
		t.Errorf("outcome = %+v, want budget_exhausted", final.Outcome)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/runs/ -run 'Conveyor|Retries|Exhaustion|WallDeadline|Hanging|Budget'`
Expected: FAIL — `NewOrchestrator` undefined.

- [ ] **Step 3: Implement the orchestrator**

`backend/internal/runs/orchestrator.go` — the complete loop. Structure it exactly as below; every branch is exercised by a Step-1 test:

```go
package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/runs/store"
)

type OrchestratorConfig struct {
	Workers           int
	PollInterval      time.Duration
	LeaseTTL          time.Duration
	HeartbeatInterval time.Duration
	OutboxRetention   time.Duration
}

// Orchestrator drives the queue: one janitor goroutine (run startup,
// deadline sweep, lease sweep + reconcile, outbox retention) and N claim
// workers. It owns no state Postgres doesn't own first (spec §1).
type Orchestrator struct {
	svc       *Service
	exec      Executor
	cfg       OrchestratorConfig
	log       *slog.Logger
	workerTag string
	lastPrune time.Time

	planMu sync.Mutex
	plans  map[uuid.UUID]cachedPlan // run id -> compiled plan + context
}

type cachedPlan struct {
	plan       ExecPlan
	projectID  uuid.UUID
	taskInputs map[string]any
	baseSHA    string
}

func NewOrchestrator(svc *Service, exec Executor, cfg OrchestratorConfig, log *slog.Logger) *Orchestrator {
	return &Orchestrator{
		svc: svc, exec: exec, cfg: cfg, log: log,
		workerTag: uuid.NewString()[:8],
		plans:     map[uuid.UUID]cachedPlan{},
	}
}

// Run blocks until ctx is done, then drains. Errors from individual
// ticks are logged, never fatal: the loop is the process's heartbeat.
func (o *Orchestrator) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		o.janitorLoop(ctx)
	}()
	for i := 0; i < o.cfg.Workers; i++ {
		wg.Add(1)
		workerID := fmt.Sprintf("%s-w%d", o.workerTag, i)
		go func() {
			defer wg.Done()
			o.workerLoop(ctx, workerID)
		}()
	}
	wg.Wait()
	return nil
}

func (o *Orchestrator) janitorLoop(ctx context.Context) {
	tick := time.NewTicker(o.cfg.PollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if err := o.startPendingRuns(ctx); err != nil {
			o.log.Error("orchestrator: start pending runs", "error", err)
		}
		if err := o.sweepExpiredRuns(ctx); err != nil {
			o.log.Error("orchestrator: wall-deadline sweep", "error", err)
		}
		if err := o.sweepExpiredLeases(ctx); err != nil {
			o.log.Error("orchestrator: lease sweep", "error", err)
		}
		if o.svc.clock.Now().Sub(o.lastPrune) > time.Hour {
			o.lastPrune = o.svc.clock.Now()
			cutoff := o.svc.clock.Now().Add(-o.cfg.OutboxRetention)
			if _, err := store.New(o.svc.pool).PruneOutbox(ctx, cutoff); err != nil {
				o.log.Error("orchestrator: outbox prune", "error", err)
			}
		}
	}
}

func (o *Orchestrator) workerLoop(ctx context.Context, workerID string) {
	for {
		if ctx.Err() != nil {
			return
		}
		c, ok, err := o.svc.claimNext(ctx, workerID, o.cfg.LeaseTTL.Seconds())
		if err != nil {
			o.log.Error("orchestrator: claim", "error", err)
			ok = false
		}
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-time.After(o.cfg.PollInterval):
			}
			continue
		}
		o.execute(ctx, workerID, c)
	}
}

// loadPlan compiles (and caches) the run's execution context.
func (o *Orchestrator) loadPlan(ctx context.Context, runID uuid.UUID) (cachedPlan, error) {
	o.planMu.Lock()
	if cp, ok := o.plans[runID]; ok {
		o.planMu.Unlock()
		return cp, nil
	}
	o.planMu.Unlock()

	q := store.New(o.svc.pool)
	runRow, err := q.GetRun(ctx, runID)
	if err != nil {
		return cachedPlan{}, fmt.Errorf("runs: load run: %w", err)
	}
	taskRow, err := q.GetTask(ctx, runRow.TaskID)
	if err != nil {
		return cachedPlan{}, fmt.Errorf("runs: load task: %w", err)
	}
	def, err := o.svc.defs.DefinitionByVersionID(ctx, taskRow.WorkflowVersionID)
	if err != nil {
		return cachedPlan{}, err
	}
	plan, err := compilePinned(def)
	if err != nil {
		return cachedPlan{}, err
	}
	inputs, err := unmarshalMap(taskRow.Inputs)
	if err != nil {
		return cachedPlan{}, err
	}
	cp := cachedPlan{plan: plan, projectID: taskRow.ProjectID, taskInputs: inputs, baseSHA: runRow.BaseSha}
	o.planMu.Lock()
	o.plans[runID] = cp
	o.planMu.Unlock()
	return cp, nil
}

// startPendingRuns flips pending runs to running and materializes their
// roots — one tx per run; the CAS makes concurrent processes safe.
func (o *Orchestrator) startPendingRuns(ctx context.Context) error {
	ids, err := store.New(o.svc.pool).ListPendingRunIDs(ctx, 16)
	if err != nil {
		return err
	}
	for _, runID := range ids {
		cp, err := o.loadPlan(ctx, runID)
		if err != nil {
			o.log.Error("orchestrator: plan load", "run", runID, "error", err)
			continue
		}
		now := o.svc.clock.Now()
		err = pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
			n, err := store.New(tx).StartRun(ctx, store.StartRunParams{ID: runID, StartedAt: &now})
			if err != nil {
				return err
			}
			if n == 0 { // superseded or started elsewhere — not ours.
				return nil
			}
			evs := []Event{{RunID: runID, ProjectID: cp.projectID, Kind: EventRunStarted,
				CorrelationID: "caesar-run-" + runID.String()}}
			maxAttempts := int32(0) // read from the run's pinned snapshot
			runRow, err := store.New(tx).GetRun(ctx, runID)
			if err != nil {
				return err
			}
			snap, err := snapshotFromRow(runRow.PolicySnapshot)
			if err != nil {
				return err
			}
			maxAttempts = snap.MaxAttempts
			for _, root := range cp.plan.Roots() {
				if _, err := o.svc.insertPhaseTx(ctx, tx, runID, root, 1, maxAttempts); err != nil {
					return err
				}
				evs = append(evs, Event{RunID: runID, ProjectID: cp.projectID, Kind: EventPhaseMaterialized,
					CorrelationID: AttemptRef{RunID: runID, NodeID: root, Attempt: 1}.String(),
					Payload:       map[string]any{"node_id": root, "attempt": 1}})
			}
			return appendEvents(ctx, tx, now, evs)
		})
		if err != nil {
			o.log.Error("orchestrator: start run", "run", runID, "error", err)
		}
	}
	return nil
}

// sweepExpiredRuns terminalizes wall-deadline breaches (spec §3: the
// predicate in every deciding statement is the mechanism; this sweep
// just makes the death visible).
func (o *Orchestrator) sweepExpiredRuns(ctx context.Context) error {
	ids, err := store.New(o.svc.pool).ListExpiredRunIDs(ctx, 16)
	if err != nil {
		return err
	}
	for _, runID := range ids {
		out := Outcome{Class: OutcomePermanent, Code: CodeWallDeadlineExceeded, Message: "wall clock expired"}
		if err := o.terminalizeRun(ctx, runID, RunDead, out, EventRunDead); err != nil {
			o.log.Error("orchestrator: expire run", "run", runID, "error", err)
		}
	}
	return nil
}

// terminalizeRun is the shared one-tx run death: CAS the run, supersede
// live phases, append events, audit run.finished (actor NULL).
func (o *Orchestrator) terminalizeRun(ctx context.Context, runID uuid.UUID, to RunStatus, out Outcome, eventKind string) error {
	cp, err := o.loadPlan(ctx, runID)
	if err != nil {
		return err
	}
	rawOut, err := json.Marshal(out)
	if err != nil {
		return err
	}
	now := o.svc.clock.Now()
	return pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).FinishRun(ctx, store.FinishRunParams{
			ID: runID, ToStatus: string(to), Outcome: rawOut, FinishedAt: &now,
		})
		if err != nil {
			return err
		}
		if n == 0 { // already terminal — lost the race, fine.
			return nil
		}
		superOut, _ := json.Marshal(Outcome{Class: OutcomePermanent, Code: CodeSuperseded, Message: "run terminalized"})
		if _, err := store.New(tx).SupersedeLivePhases(ctx, store.SupersedeLivePhasesParams{
			RunID: runID, Outcome: superOut, FinishedAt: &now,
		}); err != nil {
			return err
		}
		if err := appendEvents(ctx, tx, now, []Event{{
			RunID: runID, ProjectID: cp.projectID, Kind: eventKind,
			CorrelationID: "caesar-run-" + runID.String(), Payload: map[string]any{"outcome": out.Map()},
		}}); err != nil {
			return err
		}
		return o.svc.audit.Record(ctx, tx, audit.Entry{
			Action: "run.finished", SubjectKind: "run", SubjectID: runID.String(),
			Detail: map[string]any{"status": string(to), "outcome": out.Map()},
		})
	})
}

// sweepExpiredLeases moves expired running attempts to reconciling and
// reconciles them immediately (spec §3: reconcile-before-retry; lease
// expiry NEVER auto-retries).
func (o *Orchestrator) sweepExpiredLeases(ctx context.Context) error {
	ids, err := store.New(o.svc.pool).ListExpiredLeaseIDs(ctx, 16)
	if err != nil {
		return err
	}
	for _, phaseID := range ids {
		row, err := store.New(o.svc.pool).MarkPhaseReconciling(ctx, phaseID)
		if err != nil {
			if errorsIsNoRows(err) { // someone else got it — fine.
				continue
			}
			o.log.Error("orchestrator: mark reconciling", "phase", phaseID, "error", err)
			continue
		}
		cp, err := o.loadPlan(ctx, row.RunID)
		if err != nil {
			o.log.Error("orchestrator: reconcile plan load", "phase", phaseID, "error", err)
			continue
		}
		ref := AttemptRef{RunID: row.RunID, NodeID: row.NodeID, Attempt: row.Attempt}
		now := o.svc.clock.Now()
		if err := pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
			return appendEvents(ctx, tx, now, []Event{{
				RunID: row.RunID, ProjectID: cp.projectID, Kind: EventPhaseReconciling,
				CorrelationID: ref.String(), Payload: map[string]any{"node_id": row.NodeID, "attempt": row.Attempt},
			}})
		}); err != nil {
			o.log.Error("orchestrator: reconciling event", "phase", phaseID, "error", err)
		}

		ins, err := o.exec.Inspect(ctx, ref)
		if err != nil || !ins.Completed {
			out := Outcome{Class: OutcomeTransient, Code: CodeReconcileLost, Message: "attempt lost its lease with no adoptable result"}
			o.evaluate(ctx, "", cp, claimedFromRow(row), PhaseReconciling, ExecutionResult{}, OutcomeError{Outcome: out})
			continue
		}
		// Completed with intact outputs: adopt idempotently.
		o.evaluate(ctx, "", cp, claimedFromRow(row), PhaseReconciling, ins.Result, nil)
	}
	return nil
}

func claimedFromRow(row store.PhaseRun) claimed {
	claimedAt := time.Time{}
	if row.ClaimedAt != nil {
		claimedAt = *row.ClaimedAt
	}
	return claimed{
		PhaseID: row.ID,
		Ref:     AttemptRef{RunID: row.RunID, NodeID: row.NodeID, Attempt: row.Attempt},
		MaxAttempts: row.MaxAttempts, ClaimedAt: claimedAt,
	}
}

// execute runs one claimed attempt: deadline from the two clocks,
// heartbeats, port call, then evaluation.
func (o *Orchestrator) execute(ctx context.Context, workerID string, c claimed) {
	cp, err := o.loadPlan(ctx, c.Ref.RunID)
	if err != nil {
		o.log.Error("orchestrator: plan load", "run", c.Ref.RunID, "error", err)
		o.evaluate(ctx, workerID, cachedPlan{}, c, PhaseRunning, ExecutionResult{}, err)
		return
	}
	node := cp.plan.Nodes[c.Ref.NodeID]

	now := o.svc.clock.Now()
	if err := pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
		return appendEvents(ctx, tx, now, []Event{{
			RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: EventPhaseStarted,
			CorrelationID: c.Ref.String(),
			Payload:       map[string]any{"node_id": c.Ref.NodeID, "attempt": c.Ref.Attempt, "worker_id": workerID},
		}})
	}); err != nil {
		o.log.Error("orchestrator: started event", "error", err)
	}

	// Deadline = the tighter of the two clocks (spec §3 Budgets).
	deadline := c.RunWallDeadline
	if rem := RemainingBudgetMS(c.BudgetMS, c.UsedMS); rem > 0 {
		if budgetEnd := now.Add(time.Duration(rem) * time.Millisecond); budgetEnd.Before(deadline) {
			deadline = budgetEnd
		}
	}
	execCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	// Heartbeat until execution finishes; a fenced heartbeat cancels.
	hbDone := make(chan struct{})
	go func() {
		tick := time.NewTicker(o.cfg.HeartbeatInterval)
		defer tick.Stop()
		for {
			select {
			case <-hbDone:
				return
			case <-execCtx.Done():
				return
			case <-tick.C:
				alive, err := o.svc.heartbeat(execCtx, c.PhaseID, workerID, o.cfg.LeaseTTL.Seconds())
				if err == nil && !alive {
					cancel()
					return
				}
			}
		}
	}()

	req, err := o.buildRequest(ctx, cp, node, c.Ref, deadline)
	var res ExecutionResult
	if err == nil {
		res, err = o.exec.Execute(execCtx, req)
	}
	close(hbDone)
	o.evaluate(ctx, workerID, cp, c, PhaseRunning, res, err)
}

// buildRequest assembles explicit inputs: latest artifacts of each input
// origin, harness resolved fresh against the registry.
func (o *Orchestrator) buildRequest(ctx context.Context, cp cachedPlan, node PlanNode, ref AttemptRef, deadline time.Time) (ExecutionRequest, error) {
	req := ExecutionRequest{
		Ref: ref, Kind: node.Kind, Instructions: node.Instructions,
		TaskInputs: cp.taskInputs, BaseSHA: cp.baseSHA, Deadline: deadline,
	}
	if node.Harness != "" {
		h, err := o.svc.harnesses.HarnessByName(ctx, node.Harness)
		if err != nil {
			return ExecutionRequest{}, OutcomeError{Outcome: Outcome{
				Class: OutcomePermanent, Code: CodeHarnessError,
				Message: fmt.Sprintf("harness %q no longer registered", node.Harness),
			}}
		}
		req.HarnessID = h.ID
	}
	q := store.New(o.svc.pool)
	for _, in := range node.Inputs {
		row, err := q.GetNodeArtifact(ctx, store.GetNodeArtifactParams{
			RunID: ref.RunID, NodeID: in.Origin, Type: in.Artifact,
		})
		if err != nil {
			return ExecutionRequest{}, fmt.Errorf("runs: input artifact %s/%s: %w", in.Origin, in.Artifact, err)
		}
		payload, err := unmarshalMap(row.Payload)
		if err != nil {
			return ExecutionRequest{}, err
		}
		req.Inputs = append(req.Inputs, ArtifactInput{ID: row.ID, Type: row.Type, Payload: payload})
	}
	return req, nil
}

// evaluate is THE outcome transaction (spec §3 exhaustion never strands
// a node): success persists artifacts + materializes successors +
// completes the run; failure retries or terminalizes — each in one tx.
// fromStatus distinguishes worker completion from reconciler adoption.
func (o *Orchestrator) evaluate(ctx context.Context, workerID string, cp cachedPlan,
	c claimed, fromStatus PhaseStatus, res ExecutionResult, execErr error) {
	now := o.svc.clock.Now()
	deltaMS := now.Sub(c.ClaimedAt).Milliseconds()
	err := pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
		if execErr == nil {
			return o.evaluateSuccess(ctx, tx, workerID, cp, c, fromStatus, res, now, deltaMS)
		}
		return o.evaluateFailure(ctx, tx, workerID, cp, c, fromStatus, ClassifyOutcome(execErr), now, deltaMS)
	})
	if err != nil {
		o.log.Error("orchestrator: evaluate", "ref", c.Ref.String(), "error", err)
	}
}

func (o *Orchestrator) evaluateSuccess(ctx context.Context, tx pgx.Tx, workerID string,
	cp cachedPlan, c claimed, fromStatus PhaseStatus, res ExecutionResult, now time.Time, deltaMS int64) error {
	n, err := o.svc.finishPhase(ctx, tx, c.PhaseID, fromStatus, PhaseSucceeded, workerID, nil)
	if err != nil {
		return err
	}
	if n == 0 { // fenced: the row moved on without us. Drop the result.
		o.log.Info("orchestrator: fenced result dropped", "ref", c.Ref.String())
		return nil
	}
	q := store.New(tx)

	// Persist artifacts with lineage = this node's input artifact ids.
	node := cp.plan.Nodes[c.Ref.NodeID]
	var parentIDs []string
	for _, in := range node.Inputs {
		row, err := q.GetNodeArtifact(ctx, store.GetNodeArtifactParams{RunID: c.Ref.RunID, NodeID: in.Origin, Type: in.Artifact})
		if err == nil {
			parentIDs = append(parentIDs, row.ID.String())
		}
	}
	if parentIDs == nil {
		parentIDs = []string{}
	}
	rawParents, err := json.Marshal(parentIDs)
	if err != nil {
		return err
	}
	var artifactIDs []string
	for _, a := range res.Artifacts {
		rawPayload, err := json.Marshal(a.Payload)
		if err != nil {
			return fmt.Errorf("runs: marshal artifact payload: %w", err)
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		if _, err := q.InsertArtifact(ctx, store.InsertArtifactParams{
			ID: id, RunID: c.Ref.RunID, NodeID: c.Ref.NodeID, Attempt: c.Ref.Attempt,
			Type: a.Type, Payload: rawPayload, ParentIds: rawParents, CreatedAt: now,
		}); err != nil {
			return fmt.Errorf("runs: insert artifact: %w", err)
		}
		artifactIDs = append(artifactIDs, id.String())
	}

	usage, err := q.AddRunUsage(ctx, store.AddRunUsageParams{ID: c.Ref.RunID, DeltaMs: deltaMS})
	if err != nil {
		return err
	}

	evs := []Event{{
		RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: EventPhaseSucceeded, CorrelationID: c.Ref.String(),
		Payload: map[string]any{"node_id": c.Ref.NodeID, "attempt": c.Ref.Attempt, "artifact_ids": artifactIDs},
	}}

	// Budget breach terminalizes in this same transaction.
	if usage.ExecutionUsedMs >= usage.ExecutionBudgetMs {
		return o.finishRunTx(ctx, tx, cp, c.Ref.RunID, RunFailed,
			Outcome{Class: OutcomePermanent, Code: CodeBudgetExhausted, Message: "execution budget exhausted"},
			EventRunFailed, now, evs)
	}

	// Materialize newly-ready successors (readiness from succeeded set).
	succeededNodes, err := q.ListSucceededNodes(ctx, c.Ref.RunID)
	if err != nil {
		return err
	}
	succeeded := map[string]bool{}
	for _, id := range succeededNodes {
		succeeded[id] = true
	}
	attempted := map[string]bool{}
	phaseRows, err := q.ListPhasesForRun(ctx, c.Ref.RunID)
	if err != nil {
		return err
	}
	for _, pr := range phaseRows {
		attempted[pr.NodeID] = true
	}
	for _, nodeID := range cp.plan.Order {
		if attempted[nodeID] || !cp.plan.Ready(nodeID, succeeded) {
			continue
		}
		if _, err := o.svc.insertPhaseTx(ctx, tx, c.Ref.RunID, nodeID, 1, c.MaxAttempts); err != nil {
			return err
		}
		evs = append(evs, Event{RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: EventPhaseMaterialized,
			CorrelationID: AttemptRef{RunID: c.Ref.RunID, NodeID: nodeID, Attempt: 1}.String(),
			Payload:       map[string]any{"node_id": nodeID, "attempt": 1}})
	}

	// Completion: every node succeeded.
	if len(succeeded) == len(cp.plan.Nodes) {
		return o.finishRunTx(ctx, tx, cp, c.Ref.RunID, RunSucceeded, Outcome{}, EventRunSucceeded, now, evs)
	}
	return appendEvents(ctx, tx, now, evs)
}

// finishRunTx completes/fails the run inside the caller's tx, appending
// the accumulated events first so ordering reads naturally.
func (o *Orchestrator) finishRunTx(ctx context.Context, tx pgx.Tx, cp cachedPlan, runID uuid.UUID,
	to RunStatus, out Outcome, eventKind string, now time.Time, evs []Event) error {
	var rawOut []byte
	payload := map[string]any{}
	if to != RunSucceeded {
		var err error
		if rawOut, err = json.Marshal(out); err != nil {
			return err
		}
		payload["outcome"] = out.Map()
	}
	n, err := store.New(tx).FinishRun(ctx, store.FinishRunParams{
		ID: runID, ToStatus: string(to), Outcome: rawOut, FinishedAt: &now,
	})
	if err != nil {
		return err
	}
	if n > 0 && to != RunSucceeded {
		superOut, _ := json.Marshal(Outcome{Class: OutcomePermanent, Code: CodeSuperseded, Message: "run terminalized"})
		if _, err := store.New(tx).SupersedeLivePhases(ctx, store.SupersedeLivePhasesParams{
			RunID: runID, Outcome: superOut, FinishedAt: &now,
		}); err != nil {
			return err
		}
	}
	evs = append(evs, Event{RunID: runID, ProjectID: cp.projectID, Kind: eventKind,
		CorrelationID: "caesar-run-" + runID.String(), Payload: payload})
	if err := appendEvents(ctx, tx, now, evs); err != nil {
		return err
	}
	detail := map[string]any{"status": string(to)}
	if to != RunSucceeded {
		detail["outcome"] = out.Map()
	}
	return o.svc.audit.Record(ctx, tx, audit.Entry{
		Action: "run.finished", SubjectKind: "run", SubjectID: runID.String(), Detail: detail,
	})
}

func (o *Orchestrator) evaluateFailure(ctx context.Context, tx pgx.Tx, workerID string,
	cp cachedPlan, c claimed, fromStatus PhaseStatus, out Outcome, now time.Time, deltaMS int64) error {
	rawOut, err := json.Marshal(out)
	if err != nil {
		return err
	}
	q := store.New(tx)
	if deltaMS > 0 {
		if _, err := q.AddRunUsage(ctx, store.AddRunUsageParams{ID: c.Ref.RunID, DeltaMs: deltaMS}); err != nil {
			return err
		}
	}
	if out.Retryable() && c.Ref.Attempt < c.MaxAttempts {
		// Retry: prior attempt -> failed, next attempt inserted, SAME tx
		// (spec §2). failed rows always have successors (plan decision 3).
		n, err := o.svc.finishPhase(ctx, tx, c.PhaseID, fromStatus, PhaseFailed, workerID, rawOut)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil // fenced
		}
		if _, err := o.svc.insertPhaseTx(ctx, tx, c.Ref.RunID, c.Ref.NodeID, c.Ref.Attempt+1, c.MaxAttempts); err != nil {
			return err
		}
		return appendEvents(ctx, tx, now, []Event{{
			RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: EventPhaseRetried, CorrelationID: c.Ref.String(),
			Payload: map[string]any{"node_id": c.Ref.NodeID, "attempt": c.Ref.Attempt,
				"next_attempt": c.Ref.Attempt + 1, "outcome": out.Map()},
		}})
	}

	// Terminal: attempt -> dead, run -> failed, one transaction.
	n, err := o.svc.finishPhase(ctx, tx, c.PhaseID, fromStatus, PhaseDead, workerID, rawOut)
	if err != nil {
		return err
	}
	if n == 0 {
		return nil // fenced
	}
	runOut := out
	if out.Retryable() { // retryable but exhausted
		runOut = Outcome{Class: OutcomePermanent, Code: CodeAttemptsExhausted,
			Message: fmt.Sprintf("node %s exhausted %d attempts", c.Ref.NodeID, c.MaxAttempts)}
	}
	evs := []Event{{
		RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: EventPhaseDead, CorrelationID: c.Ref.String(),
		Payload: map[string]any{"node_id": c.Ref.NodeID, "attempt": c.Ref.Attempt, "outcome": out.Map()},
	}}
	return o.finishRunTx(ctx, tx, cp, c.Ref.RunID, RunFailed, runOut, EventRunFailed, now, evs)
}

func errorsIsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
```

(Add the `errors` import. Where sqlc's generated parameter/field shapes differ — pointer strings, `DeltaMs` casing — follow the generated code. `terminalizeRun`'s and `finishRunTx`'s `FinishRun` CAS from-set is `('pending','running','waiting')`, so a lost race affects 0 rows and is silently correct.)

- [ ] **Step 4: Run the orchestrator suite**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/runs/ -run 'Conveyor|Retries|Exhaustion|WallDeadline|Hanging|Budget'`
Expected: PASS — all six scenarios. These are timing-sensitive by nature; the only clock they trust is the database's (rows flipped via SQL), never `sleep`-based scheduling assumptions. If `TestHangingAttemptReconcilesAndExhausts` flakes on slow CI, raise its lease to 500ms — the assertion is event-count-based, not latency-based.

- [ ] **Step 5: Full gate and commit**

Run: `task backend:ci`
Expected: PASS

```bash
git add backend/internal/runs
git commit -m "feat(backend): orchestrator with outcome evaluation, sweepers, and reconciliation"
```
### Task 10: HTTP API and the contracts extension

The seven spine endpoints (plan header table): tasks in, runs out, events polled. RBAC in the handlers via the authn ports (the assets pattern verbatim: `run` to launch, `view` to read, grant-filtered lists, 403 `insufficient project grant`). Contracts regenerate in the same commit.

**Files:**
- Create: `backend/internal/runs/api/api.go`
- Create: `backend/internal/runs/api/dto.go`
- Create: `backend/internal/runs/api/api_integration_test.go` (build tag `integration`)
- Modify: `backend/internal/contracts/contracts.go` (runs table + request overrides)
- Modify (generated): `backend/api/openapi.json`, `web/src/api/schema.d.ts`

**Interfaces:**
- Consumes: Task 7's service surface, `platform/{authn,httpx,xerrors}`.
- Produces:
  - `api.GrantSource{ GrantsForUser(ctx, userID uuid.UUID) ([]authn.Grant, error) }` (context-local copy, the assets pattern)
  - `api.New(svc *runs.Service, auth authn.SessionAuthenticator, authz authn.Authorizer, grants GrantSource) *API`
  - `api.Operations(a *API) []httpx.Operation` — ids: `createTask`, `listTasks`, `getTask`, `rerunTask`, `listTaskRuns`, `getRun`, `listRunEvents`
  - DTOs (`dto.go`): `CreateTaskRequest{RepoID, WorkflowID string; WorkflowVersion int32; Title string; Inputs map[string]any}` · `TaskResponse{ID, ProjectID, RepoID, WorkflowVersionID, Title string; Inputs map[string]any; RequesterID string; Status string; LatestRunID string; CreatedAt time.Time}` · `TaskListResponse{Tasks []TaskResponse}` · `RunResponse{ID, TaskID, Status, DefinitionHash, BaseSHA string; PolicySnapshot runs.PolicySnapshot; ExecutionUsedMS int64; WallDeadline time.Time; Outcome *OutcomeResponse; CreatedAt time.Time; StartedAt, FinishedAt *time.Time; Phases []PhaseResponse; Artifacts []ArtifactResponse}` · `OutcomeResponse{Class, Code, Message string}` · `PhaseResponse{NodeID string; Attempt, MaxAttempts int32; Status string; Outcome *OutcomeResponse; CreatedAt time.Time; FinishedAt *time.Time}` · `ArtifactResponse{ID, NodeID string; Attempt int32; Type string; ParentIDs []string; CreatedAt time.Time}` · `RunListResponse{Runs []RunResponse}` (phases/artifacts empty in lists) · `RunEventsResponse{Events []RunEventResponse}` · `RunEventResponse{ID int64; Kind string; Payload map[string]any; OccurredAt time.Time}`
  - JSON tags snake_case throughout, matching the peer API packages.

- [ ] **Step 1: Write the failing integration tests**

`backend/internal/runs/api/api_integration_test.go` mirrors `assets/api/api_integration_test.go`'s structure (boot a mux from `httpx.Mount(Operations(...))` with the identity service as authenticator/authorizer — the API package's integration tests may use real platform services and its own context's service, wired by hand; peer *context* imports stay forbidden, and `identity` is reachable here only through the `authn` ports, so the test constructs the ports from a stub — copy the pattern that file actually uses). Cover:

1. `createTask` 201 as a member holding `run`; response carries `status: "pending"` and a `latest_run_id`; the same body as a `view`-only member → 403; unauthenticated → 401.
2. `createTask` field validation → 400 with `errors[]` (empty title; unknown repo → 404-classed problem).
3. `listTasks` as admin sees the task; as a granted member sees it; as an ungranted member sees an empty list (GrantSource-filtered).
4. `getTask`/`getRun` with `view` → 200; without → 403; unknown ids → 404.
5. `rerunTask` on a pending run → 201 and the old run reads back `superseded`; flip the new run to `running` via SQL → third rerun → 409.
6. `listRunEvents?after=0` → ascending ids, `run.created` first; `after=<last>` → empty.

Write these fully in that file's established idiom (login helper, cookie jar, `postJSON`/`getJSON` helpers); every assertion above is one subtest.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/runs/api/`
Expected: FAIL — package empty.

- [ ] **Step 3: Implement DTOs and handlers**

`backend/internal/runs/api/dto.go` — the structs above plus mappers `taskResponse(runs.Task) TaskResponse`, `runResponse(runs.Run, []runs.Phase, []runs.ArtifactMeta) RunResponse`, `outcomeResponse(*runs.Outcome) *OutcomeResponse`. `backend/internal/runs/api/api.go` — copy the assets API skeleton exactly (authenticated/adminOnly-less: no admin endpoints here; `authorize` helper verbatim; `decode`, `pathUUID` verbatim), then the route table:

```go
func Operations(a *API) []httpx.Operation {
	return []httpx.Operation{
		{
			Pattern: "POST /api/v1/tasks", ID: "createTask", Summary: "Create a task and its first run (run grant)",
			Request: CreateTaskRequest{}, Response: TaskResponse{}, Status: http.StatusCreated,
			Handler: a.authenticated(a.handleCreateTask),
		},
		{
			Pattern: "GET /api/v1/tasks", ID: "listTasks", Summary: "List tasks visible to the caller",
			Response: TaskListResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListTasks),
		},
		{
			Pattern: "GET /api/v1/tasks/{id}", ID: "getTask", Summary: "Get a task (view)",
			Response: TaskResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleGetTask),
		},
		{
			Pattern: "POST /api/v1/tasks/{id}/runs", ID: "rerunTask", Summary: "Re-run a task, superseding a pending run (run grant)",
			Response: RunResponse{}, Status: http.StatusCreated,
			Handler: a.authenticated(a.handleRerunTask),
		},
		{
			Pattern: "GET /api/v1/tasks/{id}/runs", ID: "listTaskRuns", Summary: "List a task's runs, newest first (view)",
			Response: RunListResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListTaskRuns),
		},
		{
			Pattern: "GET /api/v1/runs/{id}", ID: "getRun", Summary: "Get a run with phases and artifact metadata (view)",
			Response: RunResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleGetRun),
		},
		{
			Pattern: "GET /api/v1/runs/{id}/events", ID: "listRunEvents", Summary: "Poll a run's events after an id (view)",
			Response: RunEventsResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListRunEvents),
		},
	}
}
```

Handler shapes (each is the assets idiom; the interesting lines only — write the rest mechanically):

```go
func (a *API) handleCreateTask(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	req, err := decode[CreateTaskRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	repoID, err := uuid.Parse(req.RepoID)
	if err != nil {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassNotFound, "no such repo"))
		return
	}
	workflowID, err := uuid.Parse(req.WorkflowID)
	if err != nil {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassNotFound, "no such workflow"))
		return
	}
	projectID, err := a.svc.RepoProject(r.Context(), repoID)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if !a.authorize(w, r, sess, projectID, authn.GrantRun) {
		return
	}
	task, _, err := a.svc.CreateTask(r.Context(), sess.Principal, runs.NewTask{
		RepoID: repoID, WorkflowID: workflowID, WorkflowVersion: req.WorkflowVersion,
		Title: req.Title, Inputs: req.Inputs,
	})
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, taskResponse(task))
}
```

`handleListTasks` branches on role exactly like the assets project list: admin → `ListTasks`; member → `GrantsForUser` → project ids (any level) → `ListTasksByProjects`. `handleGetTask`/`handleGetRun`/`handleListTaskRuns`/`handleListRunEvents` fetch first, then `authorize(view)` against the entity's project (`GetTask(...).ProjectID`; for runs, `GetTask(run.TaskID).ProjectID`), then respond — an unauthenticated existence probe still costs a 403 only after the fetch, matching assets' semantics. `handleRerunTask` authorizes `run` on the task's project, then `svc.Rerun`. `handleListRunEvents` parses `after` from the query string (`strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)`, absent → 0, junk → 400 with field `after`).

- [ ] **Step 4: Extend the contracts generator**

In `backend/internal/contracts/contracts.go`:

```go
runsapi "github.com/giiorg/caesar/backend/internal/runs/api"
```

- `operationTables()` appends `runsapi.Operations(runsapi.New(nil, nil, nil, nil))`.
- `requestOverrides` gains:

```go
type runEventsParams struct {
	idParams
	After string `query:"after" description:"return events with id greater than this"`
}
```

  and the entry `"listRunEvents": runEventsParams{}` (`rerunTask`/`getTask`/`getRun`/`listTaskRuns` are body-less `{id}` patterns — `requestStructure` already folds those into `idParams` automatically).

Run: `task contracts:gen`
Commit the regenerated `backend/api/openapi.json` and `web/src/api/schema.d.ts` with this task.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/runs/... && task contracts:check`
Expected: PASS, drift gate clean.

- [ ] **Step 6: Full gate and commit**

Run: `task backend:ci`
Expected: PASS

```bash
git add backend/internal/runs/api backend/internal/contracts backend/api/openapi.json web/src/api/schema.d.ts
git commit -m "feat(backend): tasks and runs http api with contracts"
```

### Task 11: App wiring, port adapters, and the golden-set seed

The composition root learns the spine: config gains the `orchestrator` block, serve builds the runs service with real adapters (workflows → `DefinitionSource`, assets → `RepoSource`/`HarnessSource`), wires `NotImplementedExecutor` + a fail-fast forge stub, mounts the API, and starts the orchestrator alongside the HTTP server. Two context additions land here because their first consumers do: `workflows.Service.GetVersionByID` and `assets.Service.GetHarnessByName`. Then the golden-set seed: the full month-one-shaped loop against fakes, through the real routes.

**Files:**
- Modify: `backend/internal/platform/config/config.go` (+ `config_test.go`)
- Modify: `backend/internal/workflows/{workflows.go,store/queries.sql}` (`GetVersionByID` + query; regen)
- Modify: `backend/internal/assets/{harnesses.go,store/queries.sql}` (`GetHarnessByName` + query; regen)
- Create: `backend/internal/app/runsports.go` (the adapters)
- Modify: `backend/internal/app/serve.go`
- Create: `backend/internal/app/goldenset_integration_test.go` (build tag `integration`)

**Interfaces:**
- Consumes: everything.
- Produces:
  - `config.Orchestrator{Workers int; PollInterval, LeaseTTL, HeartbeatInterval, OutboxRetention Duration}` with the plan-header defaults and validation (workers 1–64, positive durations, heartbeat < lease)
  - `workflows.Service.GetVersionByID(ctx, id uuid.UUID) (Version, error)` (query `GetVersionByID :one — SELECT * FROM workflow_versions WHERE id = $1`)
  - `assets.Service.GetHarnessByName(ctx, name string) (Harness, error)` (query `GetHarnessByName :one — SELECT * FROM harnesses WHERE name = $1`)
  - `app.runsDefinitionSource{svc *workflows.Service}`, `app.runsAssetSource{svc *assets.Service}`, `app.unavailableForge{}` — implementing the runs ports

- [ ] **Step 1: Config block (test-first)**

Add to `config_test.go`: defaults land when the block is absent; `heartbeat_interval >= lease_ttl` and `workers: 0`/`workers: 65` are rejected. Then `config.go`: the `Orchestrator` struct on `Config` (json `orchestrator`), defaults in `defaults()`, checks in `validate()`. Run `cd backend && go test ./internal/platform/config/` → PASS. (The deadconfig arch test forces serve to read every field — Step 4 is that read site; run the full arch suite only after Step 4.)

- [ ] **Step 2: The two peer-context lookups (test-first)**

`workflows`: integration test — `GetVersionByID` returns the version with document; unknown id → `ClassNotFound`. Implementation mirrors `GetVersion` with the new query; regen sqlc. `assets`: integration test — `GetHarnessByName` round-trips a registered harness; unknown name → `ClassNotFound`. Same shape as `GetHarness`. Run both packages' integration suites → PASS.

- [ ] **Step 3: The adapters**

`backend/internal/app/runsports.go`:

```go
package app

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/workflows"
)

// runsDefinitionSource adapts workflows.Service to runs.DefinitionSource:
// parse the stored document with the definitions context's own parser,
// then map onto the runs mirror mechanically. Only app may see both
// vocabularies (composition root).
type runsDefinitionSource struct{ svc *workflows.Service }

func (s runsDefinitionSource) Definition(ctx context.Context, workflowID uuid.UUID, version int32) (runs.PinnedDefinition, error) {
	v, err := s.svc.GetVersion(ctx, workflowID, version)
	if err != nil {
		return runs.PinnedDefinition{}, err
	}
	return pinnedFromVersion(v)
}

func (s runsDefinitionSource) DefinitionByVersionID(ctx context.Context, versionID uuid.UUID) (runs.PinnedDefinition, error) {
	v, err := s.svc.GetVersionByID(ctx, versionID)
	if err != nil {
		return runs.PinnedDefinition{}, err
	}
	return pinnedFromVersion(v)
}

func pinnedFromVersion(v workflows.Version) (runs.PinnedDefinition, error) {
	doc, err := workflows.ParseDocument(v.Document)
	if err != nil {
		return runs.PinnedDefinition{}, fmt.Errorf("app: stored definition %s unparseable: %w", v.ID, err)
	}
	mirror := runs.DefinitionDoc{}
	for _, n := range doc.Nodes {
		mirror.Nodes = append(mirror.Nodes, runs.DefNode{
			ID: n.ID, Kind: string(n.Type), Harness: n.Harness, Instructions: n.Instructions,
		})
	}
	for _, e := range doc.Edges {
		mirror.Edges = append(mirror.Edges, runs.DefEdge{From: e.From, To: e.To, Artifact: string(e.Artifact)})
	}
	return runs.PinnedDefinition{
		VersionID: v.ID, WorkflowID: v.WorkflowID, Version: v.Version,
		ContentHash: v.ContentHash, Doc: mirror,
	}, nil
}

// runsAssetSource adapts assets.Service to RepoSource + HarnessSource.
type runsAssetSource struct{ svc *assets.Service }

func (s runsAssetSource) Repo(ctx context.Context, id uuid.UUID) (runs.RepoRef, error) {
	r, err := s.svc.GetRepo(ctx, id)
	if err != nil {
		return runs.RepoRef{}, err
	}
	return runs.RepoRef{
		ID: r.ID, ProjectID: r.ProjectID, CredentialID: r.CredentialID,
		Forge: string(r.Forge), Slug: r.Slug, DefaultBranch: r.DefaultBranch,
	}, nil
}

func (s runsAssetSource) HarnessByName(ctx context.Context, name string) (runs.HarnessRef, error) {
	h, err := s.svc.GetHarnessByName(ctx, name)
	if err != nil {
		return runs.HarnessRef{}, err
	}
	return runs.HarnessRef{ID: h.ID, Kind: string(h.Kind)}, nil
}

// unavailableForge refuses task intake honestly until Plan 08's forge
// adapters exist: no run can pin a base SHA, so no task can be created
// through production wiring yet (stubs never fake success).
type unavailableForge struct{}

func (unavailableForge) ResolveHead(context.Context, runs.RepoRef) (string, error) {
	return "", xerrors.Wrap(xerrors.ClassInvalid, "forge adapters arrive in Plan 08; task intake is not yet available in production wiring", xerrors.ErrNotImplemented)
}
```

- [ ] **Step 4: Wire serve**

In `serve.go`, after the workflows service:

```go
	runsSvc := runs.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}),
		runsDefinitionSource{svc: workflowsSvc}, runsAssetSource{svc: assetsSvc}, runsAssetSource{svc: assetsSvc},
		unavailableForge{}, runs.Params{Snapshot: runs.DefaultSnapshot()})
	ops = append(ops, runsapi.Operations(runsapi.New(runsSvc, svc, svc, svc))...)

	orch := runs.NewOrchestrator(runsSvc, runs.NotImplementedExecutor{}, runs.OrchestratorConfig{
		Workers:           cfg.Orchestrator.Workers,
		PollInterval:      cfg.Orchestrator.PollInterval.Std(),
		LeaseTTL:          cfg.Orchestrator.LeaseTTL.Std(),
		HeartbeatInterval: cfg.Orchestrator.HeartbeatInterval.Std(),
		OutboxRetention:   cfg.Orchestrator.OutboxRetention.Std(),
	}, log)
	orchDone := make(chan struct{})
	go func() { defer close(orchDone); _ = orch.Run(ctx) }()
```

and after HTTP shutdown completes, wait for the orchestrator to drain: `<-orchDone`. (`identity.Service` serves as authenticator + authorizer + grant source for the runs API exactly as it does for assets.) Extend `serve_integration_test.go`'s boot assertion if it enumerates mounted routes.

- [ ] **Step 5: The golden-set seed (failing first, then green)**

`backend/internal/app/goldenset_integration_test.go` — the month-one loop minus gates (Plan 08 extends this exact test): boot `app.Routes` with real services but **fake executor + fake forge**, drive everything through HTTP as two different humans, assert the full observable record. Structure (write it fully in the file's e2e idiom — login helper, cookie clients):

1. Fixture: `fake.InitBareRepo(t.TempDir())` → `headSHA`. Bootstrap admin via `identity.Service.Bootstrap`; login admin.
2. Admin via API: create project; create `model_key` credential + `forge_token` credential; register harness `cc`; create repo (slug `golden/seed`); create workflow; POST the three-node document (plan/impl/review, harness `cc`) through the write door; create member user + grant `run` on the project (identity API).
3. Wire `fake.Forge{Heads: {repoID: headSHA}}` and `fake.NewExecutor()` into the runs service; orchestrator with 20ms poll started on a test context.
4. Member logs in, POSTs the task. Poll `GET /api/v1/runs/{id}` until `succeeded` (15s cap).
5. Assert through the API: run `base_sha == headSHA`, `definition_hash` equals the version's `content_hash` from the workflows API; three phases succeeded attempt 1; artifact lineage plan → patch → verdict; patch payload `base_sha == headSHA`.
6. Assert events via `GET /runs/{id}/events`: strictly ascending ids; `run.created` … `run.succeeded` bookends; `after=<last>` returns empty.
7. Assert audit rows via SQL: `task.created` + `run.created` with the member as actor; `run.finished` with NULL actor; count outbox rows == count run_events rows.
8. RBAC negative: a third user with no grant gets 403 on `POST /tasks` and an empty `GET /tasks`.

Run: `cd backend && go test -race -tags integration -count=1 ./internal/app/ -run GoldenSet`
Expected: PASS

- [ ] **Step 6: Full gate and commit**

Run: `task ci`
Expected: green everywhere runnable locally (the secrets lane limitation in linked worktrees is known; CI covers it)

```bash
git add backend/internal/platform/config backend/internal/workflows backend/internal/assets backend/internal/app backend/sqlc.yaml
git commit -m "feat(backend): wire the execution spine into serve with the golden-set seed"
```

### Task 12: Docs, roadmap, final green

**Files:**
- Modify: `backend/README.md` (spine section: entities, the claim contract, two clocks, event/outbox model, the Plan 05 production posture — `NotImplementedExecutor` + unavailable forge until Plans 06/08)
- Modify: `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md` (row 05 → done, plan filename)
- Create (commit): `docs/superpowers/plans/2026-08-03-caesar-05-execution-spine.md` (this document — the Plans 03/04 convention: the plan records itself)

- [ ] **Step 1: Write the README section**

Under the existing architecture notes, add "Execution spine (Plan 05)": the five entities and their status tables (link to `internal/runs/status.go` as the source of truth), PhaseRun-as-queue in two sentences (claim CTE + lease + reconcile-before-retry), the two clocks and where each is enforced, commit-ordered events + outbox retention, and the honest V1 posture: production task intake returns "forge adapters arrive in Plan 08", and any claimed phase under production wiring dies `not_implemented` until Plan 06 — fakes drive the golden-set seed in CI.

- [ ] **Step 2: Roadmap row**

Row 05 status → `**done** — 2026-08-03-caesar-05-execution-spine.md`.

- [ ] **Step 3: Final green and commit**

Run: `task ci`
Expected: green (same worktree caveats as Plans 03/04).

```bash
git add backend/README.md docs/superpowers/plans/
git commit -m "docs(repo): plan 05 recorded, readme covers the execution spine"
```

## Execution notes

- **Prose-specified tests are binding.** Tasks 10 and 11 enumerate their test scenarios as numbered prose instead of full listings; every numbered scenario must exist as a subtest — reviewers reject a task that drops one.
- **Sequencing:** Tasks 1–3 are pure Go and independent of Docker; 4 unlocks 5–9; 10 needs 7; 11 needs everything. No parallel execution across tasks that touch `schema.sql`/`sqlc.yaml` (4 and 11).
- **sqlc is authoritative for generated shapes.** Where this plan's Go snippets name generated params/fields (`BaseSha`, `DeltaMs`, pointer-ness of nullable columns), defer to what `task db:sqlc` emits and adjust call sites — never hand-edit generated files.
- **Timing-sensitive tests** trust only database-clock predicates flipped via SQL (`wall_deadline = now() - interval '1 second'`), never wall-clock sleeps as correctness gates; polling loops are bounded waits on observable state.
- **What Plan 06 picks up:** real Docker executor behind `runs.Executor`, real reconciliation via `Inspect` against container state, log_ref/failure_snapshot artifacts. **Plan 08:** gates (`waiting` states become reachable, intake guard deleted), forge adapters replace `unavailableForge`, publish, and the golden set grows gate → approve → publish with crash injection.
