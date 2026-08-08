//go:build integration

package runs_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

// gateIdleWindow is many janitor ticks wide (newOrchestrator polls every
// 20ms), so a run that is still spending anything at a gate has had ample
// opportunity to show it.
const gateIdleWindow = 500 * time.Millisecond

// phaseByNode is the assertion helper for a node that MUST have
// materialized; its absence is precisely the regression these tests exist
// to catch, so it fatals rather than returning a zero row.
func phaseByNode(t *testing.T, phases []runs.Phase, nodeID string) runs.Phase {
	t.Helper()
	p, ok := tryPhaseByNode(phases, nodeID)
	if !ok {
		t.Fatalf("no phase row for node %q", nodeID)
	}
	return p
}

// tryPhaseByNode reports the node's latest attempt row, or false when the
// node never materialized. Rows arrive ordered by created_at.
func tryPhaseByNode(phases []runs.Phase, nodeID string) (runs.Phase, bool) {
	var found runs.Phase
	ok := false
	for _, p := range phases {
		if p.NodeID == nodeID {
			found, ok = p, true
		}
	}
	return found, ok
}

func countEvents(t *testing.T, svc *runs.Service, runID uuid.UUID, kind string) int {
	t.Helper()
	evs, err := svc.ListRunEvents(context.Background(), runID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// A gate is where the machine hands the run to a human: the phase row is
// born 'waiting_decision' (the claim CTE predicates on 'pending', so no
// worker can ever lease it), the run moves to 'waiting', and the node the
// gate authorizes stays unmaterialized until the decision lands.
func TestOrchestrator_GateMaterializesWaitingAndStopsTheRun(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, gatedConveyorDoc(), runs.DefaultSnapshot())
	startOrchestrator(t, svc, fake.NewExecutor(), 30*time.Second)
	ctx := context.Background()

	_, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "gated",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The orchestrator drives everything up to the gate and then stops.
	parked := waitForRun(t, svc, run.ID, runs.RunWaiting)

	_, phases, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	gate := phaseByNode(t, phases, "gate")
	if gate.Status != runs.PhaseWaitingDecision {
		t.Fatalf("gate status = %s, want waiting_decision", gate.Status)
	}
	if gate.WorkerID != "" {
		t.Errorf("a gate must never be claimed; worker = %q", gate.WorkerID)
	}
	if _, ok := tryPhaseByNode(phases, "publish"); ok {
		t.Error("the publish node must not materialize before its gate is decided")
	}

	// The gate consumed no execution budget: the run's usage is whatever the
	// three agentic phases spent, and it does not grow while the gate waits.
	// The positive control matters — comparing 0 against 0 would pass on a
	// build that never accounted anything.
	if parked.ExecutionUsedMS <= 0 {
		t.Fatalf("execution usage = %d; the three agentic phases must have accounted some", parked.ExecutionUsedMS)
	}
	time.Sleep(gateIdleWindow)
	after, phasesAfter, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ExecutionUsedMS != parked.ExecutionUsedMS {
		t.Errorf("execution usage grew from %d to %d while a human was thinking; the budget must pause at gates",
			parked.ExecutionUsedMS, after.ExecutionUsedMS)
	}
	// Still waiting, still unclaimed: nothing woke the gate up on its own.
	if after.Status != runs.RunWaiting {
		t.Errorf("run status = %s after idling at a gate, want waiting", after.Status)
	}
	if g := phaseByNode(t, phasesAfter, "gate"); g.Status != runs.PhaseWaitingDecision || g.WorkerID != "" {
		t.Errorf("gate drifted to %s (worker %q) with no decision", g.Status, g.WorkerID)
	}

	// And the event stream announced it exactly once, with the publish it
	// authorizes and the revision it offers named in the payload.
	if n := countEvents(t, svc, run.ID, runs.EventGateWaiting); n != 1 {
		t.Errorf("gate.waiting events = %d, want 1", n)
	}
	if n := countEvents(t, svc, run.ID, runs.EventPhaseStarted); n != 3 {
		t.Errorf("phase.started events = %d, want 3 — a gate starts nothing", n)
	}
	evs, err := svc.ListRunEvents(ctx, run.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var announced *runs.RunEventRecord
	for i, e := range evs {
		if e.Kind == runs.EventGateWaiting {
			announced = &evs[i]
		}
	}
	if announced == nil {
		t.Fatal("no gate.waiting event")
	}
	if announced.Payload["authorizes"] != "publish" {
		t.Errorf("gate.waiting authorizes = %v, want publish", announced.Payload["authorizes"])
	}
	if announced.Payload["revision_target"] != "impl" {
		t.Errorf("gate.waiting revision_target = %v, want impl", announced.Payload["revision_target"])
	}
}

// The execution budget pauses at gates; the wall clock never does (spec §3
// Budgets). A gate is not a place to hide from the deadline, and the waiting
// row is superseded with the run rather than left decidable over its corpse.
func TestOrchestrator_WallDeadlineStillKillsAWaitingRun(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, gatedConveyorDoc(), runs.PolicySnapshot{
		SchemaVersion: 2, MaxAttempts: 3, ExecutionBudgetMS: 60_000, WallClockMS: 60_000,
		AllowSelfApproval: true,
	})
	startOrchestrator(t, svc, fake.NewExecutor(), 30*time.Second)
	ctx := context.Background()

	_, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "gated-late",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, svc, run.ID, runs.RunWaiting)

	if _, err := f.pool.Exec(ctx,
		`UPDATE runs SET wall_deadline = now() - interval '1 second' WHERE id = $1`, run.ID,
	); err != nil {
		t.Fatal(err)
	}
	final := waitForRun(t, svc, run.ID, runs.RunDead)
	if final.Outcome == nil || final.Outcome.Code != runs.CodeWallDeadlineExceeded {
		t.Fatalf("outcome = %+v, want wall_deadline_exceeded", final.Outcome)
	}
	_, phases, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gate := phaseByNode(t, phases, "gate"); gate.Status != runs.PhaseSuperseded {
		t.Errorf("the waiting gate must be superseded with the run, got %s", gate.Status)
	}
}

// generationBumper moves the run to generation 2 from inside one attempt —
// standing in for the revision rematerialization a decided gate performs.
// Everything the orchestrator writes after that instant must carry the new
// marker, so the bump lands before the evaluation transaction opens.
type generationBumper struct {
	inner  runs.Executor
	pool   *pgxpool.Pool
	nodeID string
	fail   bool
}

func (e generationBumper) Execute(ctx context.Context, req runs.ExecutionRequest) (runs.ExecutionResult, error) {
	if req.Ref.NodeID != e.nodeID || req.Ref.Attempt != 1 {
		return e.inner.Execute(ctx, req)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE runs SET generation = 2 WHERE id = $1`, req.Ref.RunID); err != nil {
		return runs.ExecutionResult{}, err
	}
	if !e.fail {
		return e.inner.Execute(ctx, req)
	}
	return runs.ExecutionResult{Artifacts: []runs.NewArtifact{{
			Type:    runs.ArtifactKindLogRef,
			Payload: map[string]any{"streams": map[string]any{}},
		}}}, runs.OutcomeError{Outcome: runs.Outcome{
			Class: runs.OutcomeTransient, Code: runs.CodeHarnessError, Message: "harness died mid-attempt",
		}}
}

func (e generationBumper) Inspect(ctx context.Context, ref runs.AttemptRef) (runs.InspectResult, error) {
	return e.inner.Inspect(ctx, ref)
}

func phaseGeneration(t *testing.T, f fixture, runID uuid.UUID, nodeID string, attempt int32) int32 {
	t.Helper()
	var gen int32
	if err := f.pool.QueryRow(context.Background(),
		`SELECT generation FROM phase_runs WHERE run_id = $1 AND node_id = $2 AND attempt = $3`,
		runID, nodeID, attempt,
	).Scan(&gen); err != nil {
		t.Fatalf("read generation of %s#%d: %v", nodeID, attempt, err)
	}
	return gen
}

func artifactGeneration(t *testing.T, f fixture, runID uuid.UUID, nodeID string, attempt int32) int32 {
	t.Helper()
	var gen int32
	if err := f.pool.QueryRow(context.Background(),
		`SELECT generation FROM artifacts WHERE run_id = $1 AND node_id = $2 AND attempt = $3 LIMIT 1`,
		runID, nodeID, attempt,
	).Scan(&gen); err != nil {
		t.Fatalf("read generation of %s#%d's artifact: %v", nodeID, attempt, err)
	}
	return gen
}

// Every row the orchestrator writes carries the generation its own
// transaction reads, not a scaffolding literal.
//
// A missed site does not fail — 1 is a legal generation, so it silently
// stamps a revised run's rows as generation 1, and GetNodeArtifact's
// provenance-first ordering then serves the STALE artifact: exactly the bug
// that ordering exists to prevent, reintroduced from the writing end. The
// grep for the scaffolding comment cannot see that; only reading a row back
// can.
func TestOrchestrator_MaterializedRowsCarryTheRunsCurrentGeneration(t *testing.T) {
	tests := []struct {
		name        string
		fail        bool
		maxAttempts int32
		want        runs.RunStatus
		// phaseNode/phaseAttempt is the attempt row born after the bump —
		// empty where the case writes none (a dead attempt has no successor).
		phaseNode    string
		phaseAttempt int32
		// artifactNode/artifactAttempt is the attempt whose artifacts were
		// persisted after the bump.
		artifactNode    string
		artifactAttempt int32
	}{
		{
			name:        "a successor row and the artifacts of a succeeded attempt",
			maxAttempts: 3, want: runs.RunSucceeded,
			phaseNode: "impl", phaseAttempt: 1, artifactNode: "impl", artifactAttempt: 1,
		},
		{
			name: "a retry row and the artifacts a failed attempt left behind",
			fail: true, maxAttempts: 3, want: runs.RunSucceeded,
			phaseNode: "plan", phaseAttempt: 2, artifactNode: "plan", artifactAttempt: 1,
		},
		{
			name: "the artifacts of an attempt with nowhere left to retry",
			fail: true, maxAttempts: 1, want: runs.RunFailed,
			artifactNode: "plan", artifactAttempt: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			snap := runs.DefaultSnapshot()
			snap.MaxAttempts = tc.maxAttempts
			svc, _ := newService(t, f, agenticDoc(), snap)
			ex := generationBumper{inner: fake.NewExecutor(), pool: f.pool, nodeID: "plan", fail: tc.fail}
			startOrchestrator(t, svc, ex, 30*time.Second)
			ctx := context.Background()

			_, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
				RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "generation",
			})
			if err != nil {
				t.Fatal(err)
			}
			waitForRun(t, svc, run.ID, tc.want)

			// The root was materialized before the bump and keeps its 1: a
			// test that saw 2 everywhere would pass on a hard-coded 2.
			if got := phaseGeneration(t, f, run.ID, "plan", 1); got != 1 {
				t.Errorf("root phase generation = %d, want 1 (it predates the revision)", got)
			}
			if tc.phaseNode != "" {
				if got := phaseGeneration(t, f, run.ID, tc.phaseNode, tc.phaseAttempt); got != 2 {
					t.Errorf("phase %s#%d generation = %d, want 2", tc.phaseNode, tc.phaseAttempt, got)
				}
			}
			if got := artifactGeneration(t, f, run.ID, tc.artifactNode, tc.artifactAttempt); got != 2 {
				t.Errorf("artifact of %s#%d generation = %d, want 2", tc.artifactNode, tc.artifactAttempt, got)
			}
		})
	}
}
