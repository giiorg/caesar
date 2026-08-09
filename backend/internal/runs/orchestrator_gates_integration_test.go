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

// parallelGateDoc forks one plan into two independent implementations, each
// closed by its own gate and its own publish, so that BOTH gates are open at
// the same instant.
//
// It is a legal definition and that is the point: validateConsumption
// constrains a gate's INPUTS ("exactly one plan or patch, at most one review
// verdict", workflows/validate.go), never how many gates a run may hold open
// at once. Every other gate fixture on this branch is CHAINED — twoGateDoc is
// gate_one → gate_two, twinGateChain likewise — so the concurrently-open shape
// had no cover at all, which is how the stranding below survived sixteen tasks
// and four review rounds.
//
// The fork is at the IMPLEMENT nodes rather than at a single shared patch, and
// that is forced rather than stylistic: PublicationBranch derives the marker
// from the approved artifact id (ports.go), so two gates examining ONE patch
// authorize two publications onto one branch name, and the second loses
// publish_intents_marker_key with a permanent publish_conflict. That shape is
// worth someone's attention on its own terms; it is not what this test is
// about, and staging it here would only mean asserting a second, unrelated
// failure.
func parallelGateDoc() runs.DefinitionDoc {
	return runs.DefinitionDoc{
		Nodes: []runs.DefNode{
			{ID: "plan", Kind: runs.KindPlan, Harness: "cc", Instructions: "Plan the change."},
			{ID: "impl_a", Kind: runs.KindImplement, Harness: "cc", Instructions: "Implement it one way."},
			{ID: "impl_b", Kind: runs.KindImplement, Harness: "cc", Instructions: "Implement it the other."},
			{ID: "gate_a", Kind: runs.KindGate},
			{ID: "gate_b", Kind: runs.KindGate},
			{ID: "publish_a", Kind: runs.KindGitPublish},
			{ID: "publish_b", Kind: runs.KindGitPublish},
		},
		Edges: []runs.DefEdge{
			{From: "plan", To: "impl_a", Artifact: runs.ArtifactKindPlan},
			{From: "plan", To: "impl_b", Artifact: runs.ArtifactKindPlan},
			{From: "impl_a", To: "gate_a", Artifact: runs.ArtifactKindPatch},
			{From: "impl_b", To: "gate_b", Artifact: runs.ArtifactKindPatch},
			{From: "gate_a", To: "publish_a", Artifact: runs.ArtifactKindPatch},
			{From: "gate_b", To: "publish_b", Artifact: runs.ArtifactKindPatch},
		},
	}
}

// Approving ONE of two simultaneously-waiting gates must leave the other
// decidable, and must release what the approval authorized.
//
// The mechanism under test is a SQL predicate, not Go: an approval CASes the
// run 'waiting' → 'running' and deliberately materializes nothing
// (gates.go — all successor materialization lives in materializeReady), so the
// janitor's ListRunsAwaitingMaterialization pass is the only thing that can
// release gate_a's publish. While that query excluded runs holding a
// 'waiting_decision' phase, the sibling gate hid the run from its own rescuer:
// publish_a never materialized, nothing ever called syncWaiting, the run never
// went back to 'waiting', and FinishGatePhase — which predicates on
// r.status = 'waiting' — refused gate_b forever. The run then idled to its
// wall deadline with a publication a human had explicitly authorized.
//
// Both halves are asserted deliberately. Reaching RunSucceeded alone would not
// prove the second: a fix that merely unblocked gate_a's successors while
// gate_b stayed stuck would fail at the DecideGate call, and a fix that reached
// gate_b without releasing publish_a would fail on the phase status.
func TestOrchestrator_ApprovingOneOfTwoParallelGatesLeavesTheOtherDecidable(t *testing.T) {
	f, svc, run := gatedRunAtItsGate(t, parallelGateDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	// The premise: BOTH gates are open at the same instant. Without it this
	// test degrades into another chained-gate case and proves nothing, so it
	// fatals rather than asserting.
	_, phases, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"gate_a", "gate_b"} {
		if g := phaseByNode(t, phases, id); g.Status != runs.PhaseWaitingDecision {
			t.Fatalf("gate %s is %s, want waiting_decision: this test needs both gates open at once", id, g.Status)
		}
	}

	if _, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate_a", Decision: runs.DecisionApprove, Note: "ship a",
	}); err != nil {
		t.Fatal(err)
	}

	// The run must come back to 'waiting' on its own. That return trip is the
	// whole fix: the janitor has to pick the run up, materialize publish_a,
	// let it run, and let evaluateSuccess re-park the run against the gate
	// that is still open.
	waitForRun(t, svc, run.ID, runs.RunWaiting)
	_, phases, _, err = svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p := phaseByNode(t, phases, "publish_a"); p.Status != runs.PhaseSucceeded {
		t.Errorf("publish_a is %s, want succeeded: approving gate_a must release what it authorized", p.Status)
	}
	if g := phaseByNode(t, phases, "gate_b"); g.Status != runs.PhaseWaitingDecision {
		t.Errorf("gate_b is %s, want waiting_decision", g.Status)
	}
	if _, ok := tryPhaseByNode(phases, "publish_b"); ok {
		t.Error("publish_b materialized before its own gate was decided")
	}

	// Parked, not spinning. The widened anti-join now matches a running run
	// whose only live phase is a waiting gate, which is what it must do; the
	// re-park above is what stops that from repeating, because the query's
	// outer predicate is r.status = 'running'. If the park were missing the
	// run would sit 'running' and be re-picked every tick.
	time.Sleep(gateIdleWindow)
	idle, _, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if idle.Status != runs.RunWaiting {
		t.Errorf("run status = %s after idling at the second gate, want waiting", idle.Status)
	}

	// And gate_b is genuinely decidable — the assertion the whole test exists
	// for. FinishGatePhase requires r.status = 'waiting'; before the fix this
	// call returned a 409 that never stopped being true.
	if _, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate_b", Decision: runs.DecisionApprove, Note: "ship b",
	}); err != nil {
		t.Fatalf("gate_b was undecidable after its sibling was approved: %v", err)
	}
	waitForRun(t, svc, run.ID, runs.RunSucceeded)
	_, phases, _, err = svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"publish_a", "publish_b"} {
		if p := phaseByNode(t, phases, id); p.Status != runs.PhaseSucceeded {
			t.Errorf("%s is %s, want succeeded", id, p.Status)
		}
	}
}
