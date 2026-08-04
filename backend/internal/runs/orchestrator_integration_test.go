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
	o := runs.NewOrchestrator(svc, ex, runs.AlwaysAdmit{}, runs.OrchestratorConfig{
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
			implStatuses = append(implStatuses, string(p.Status)+"#"+string('0'+p.Attempt))
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
	// A hung executor keeps its worker alive and heartbeating, so the
	// lease never expires on its own — expiry means worker death, which
	// an in-process test can't produce. Simulate it the way these tests
	// trust: flip the row via SQL until the janitor's sweep wins the race
	// against the next heartbeat (poll 20ms vs heartbeat 100ms).
	expireCtx, expireCancel := context.WithCancel(ctx)
	defer expireCancel()
	go func() {
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-expireCtx.Done():
				return
			case <-tick.C:
				_, _ = f.pool.Exec(expireCtx,
					`UPDATE phase_runs SET leased_until = now() - interval '1 second'
					 WHERE run_id = $1 AND status = 'running'`, run.ID)
			}
		}
	}()
	final := waitForRun(t, svc, run.ID, runs.RunFailed)
	if final.Outcome == nil || final.Outcome.Code != runs.CodeAttemptsExhausted {
		t.Errorf("outcome = %+v", final.Outcome)
	}
	// Single-counting invariant: usage is the sum of disjoint
	// claim->terminal intervals inside the run's lifetime, so it can
	// never exceed the run's total wall time. A fenced evaluation (the
	// hung worker losing to the reconciler) must add nothing.
	if final.FinishedAt == nil {
		t.Fatal("failed run must carry finished_at")
	}
	if wall := final.FinishedAt.Sub(final.CreatedAt).Milliseconds(); final.ExecutionUsedMS > wall {
		t.Errorf("execution_used_ms = %d exceeds run wall time %dms — usage double-counted", final.ExecutionUsedMS, wall)
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

// TestBudgetBreachOnRetryFailsTheRun crosses the budget on a transient
// failure: the retry path must not insert a successor the claim
// predicate would refuse forever — the attempt dies and the run fails
// budget_exhausted promptly, not at the 24h wall deadline.
func TestBudgetBreachOnRetryFailsTheRun(t *testing.T) {
	f := newFixture(t)
	doc := agenticDoc()
	doc.Nodes[0].Instructions = "Plan.\ncaesar-fake: fail attempts=99 class=transient code=harness_error"
	snap := runs.DefaultSnapshot()
	snap.ExecutionBudgetMS = 1 // any real attempt costs more than 1ms
	svc, _ := newService(t, f, doc, snap)
	startOrchestrator(t, svc, fake.NewExecutor(), 30*time.Second)
	ctx := context.Background()

	_, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "budget-retry",
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForRun(t, svc, run.ID, runs.RunFailed)
	if final.Outcome == nil || final.Outcome.Code != runs.CodeBudgetExhausted {
		t.Errorf("outcome = %+v, want budget_exhausted", final.Outcome)
	}
	_, phases, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 1 {
		t.Fatalf("phases = %d, want 1 (a breach must not insert a successor)", len(phases))
	}
	if phases[0].NodeID != "plan" || phases[0].Status != runs.PhaseDead {
		t.Errorf("attempt row = %s/%s, want plan/dead", phases[0].NodeID, phases[0].Status)
	}
}

// logOnFailureExecutor hands back a log_ref artifact ALONGSIDE a
// transient failure on the first attempt of one node — the runner's
// shape: an attempt that dies still produced logs worth keeping.
type logOnFailureExecutor struct {
	inner  runs.Executor
	nodeID string
}

func (e logOnFailureExecutor) Execute(ctx context.Context, req runs.ExecutionRequest) (runs.ExecutionResult, error) {
	if req.Ref.NodeID == e.nodeID && req.Ref.Attempt == 1 {
		return runs.ExecutionResult{Artifacts: []runs.NewArtifact{{
				Type:    runs.ArtifactKindLogRef,
				Payload: map[string]any{"streams": map[string]any{}},
			}}}, runs.OutcomeError{Outcome: runs.Outcome{
				Class: runs.OutcomeTransient, Code: runs.CodeHarnessError, Message: "harness died mid-attempt",
			}}
	}
	return e.inner.Execute(ctx, req)
}

func (e logOnFailureExecutor) Inspect(ctx context.Context, ref runs.AttemptRef) (runs.InspectResult, error) {
	return e.inner.Inspect(ctx, ref)
}

// A failed attempt's artifacts survive it: the evidence of why an
// attempt died is exactly what a dead attempt leaves behind, so
// evaluateFailure persists them (parentless — they descend from nothing)
// and the phase.retried event names their ids.
func TestOrchestrator_FailureArtifactsPersist(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	startOrchestrator(t, svc, logOnFailureExecutor{inner: fake.NewExecutor(), nodeID: "impl"}, 30*time.Second)
	ctx := context.Background()

	_, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "failure-artifacts",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, svc, run.ID, runs.RunSucceeded)

	var artifactID uuid.UUID
	var parents []byte
	err = f.pool.QueryRow(ctx,
		`SELECT id, parent_ids FROM artifacts
		 WHERE run_id = $1 AND node_id = 'impl' AND attempt = 1 AND type = $2`,
		run.ID, runs.ArtifactKindLogRef,
	).Scan(&artifactID, &parents)
	if err != nil {
		t.Fatalf("failed attempt's log_ref must persist: %v", err)
	}
	if string(parents) != "[]" {
		t.Errorf("parent_ids = %s, want [] (a failure artifact descends from nothing)", parents)
	}

	evs, err := svc.ListRunEvents(ctx, run.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var retried *runs.RunEventRecord
	for i, e := range evs {
		if e.Kind == runs.EventPhaseRetried {
			retried = &evs[i]
		}
	}
	if retried == nil {
		t.Fatal("no phase.retried event")
	}
	ids, ok := retried.Payload["artifact_ids"].([]any)
	if !ok || len(ids) != 1 || ids[0] != artifactID.String() {
		t.Errorf("phase.retried artifact_ids = %v, want [%s]", retried.Payload["artifact_ids"], artifactID)
	}
}

// The dead path keeps them too: an attempt with nowhere left to retry is
// exactly the one whose logs matter most.
func TestOrchestrator_DeadAttemptArtifactsPersist(t *testing.T) {
	f := newFixture(t)
	snap := runs.DefaultSnapshot()
	snap.MaxAttempts = 1 // attempt 1 has no successor: fail once, die.
	svc, _ := newService(t, f, agenticDoc(), snap)
	startOrchestrator(t, svc, logOnFailureExecutor{inner: fake.NewExecutor(), nodeID: "plan"}, 30*time.Second)
	ctx := context.Background()

	_, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "dead-artifacts",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, svc, run.ID, runs.RunFailed)

	var artifactID uuid.UUID
	err = f.pool.QueryRow(ctx,
		`SELECT id FROM artifacts WHERE run_id = $1 AND node_id = 'plan' AND attempt = 1 AND type = $2`,
		run.ID, runs.ArtifactKindLogRef,
	).Scan(&artifactID)
	if err != nil {
		t.Fatalf("dead attempt's log_ref must persist: %v", err)
	}

	evs, err := svc.ListRunEvents(ctx, run.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var dead *runs.RunEventRecord
	for i, e := range evs {
		if e.Kind == runs.EventPhaseDead {
			dead = &evs[i]
		}
	}
	if dead == nil {
		t.Fatal("no phase.dead event")
	}
	ids, ok := dead.Payload["artifact_ids"].([]any)
	if !ok || len(ids) != 1 || ids[0] != artifactID.String() {
		t.Errorf("phase.dead artifact_ids = %v, want [%s]", dead.Payload["artifact_ids"], artifactID)
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
