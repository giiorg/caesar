//go:build integration

package runs_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

// runOrchestrator boots the loop and hands back the stop func, so a test
// can end one process's life on purpose (restart survival) instead of
// only at cleanup. Stopping twice is a no-op. configure runs before the
// goroutines exist — seams are set on a loop nobody is reading yet.
func runOrchestrator(t *testing.T, svc *runs.Service, ex runs.Executor, lease time.Duration,
	configure ...func(*runs.Orchestrator),
) (*runs.Orchestrator, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	o := newOrchestrator(svc, ex, lease)
	for _, cfg := range configure {
		cfg(o)
	}
	go func() { defer close(done); _ = o.Run(ctx) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		<-done
	}
	t.Cleanup(stop)
	return o, stop
}

// parkedRow polls for the run's one pending attempt carrying a not_before,
// returning its id, how far out it is in seconds, and its inherited
// chargeable-failure count. The remaining seconds are measured after the
// fact, so they undershoot the scheduled delay by the poll lag — a test
// asserting against the floor must leave that much room.
func parkedRow(t *testing.T, f fixture, runID uuid.UUID, nodeID string) (uuid.UUID, float64, int32) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var id uuid.UUID
		var secs float64
		var charged int32
		err := f.pool.QueryRow(ctx,
			`SELECT id, EXTRACT(EPOCH FROM (not_before - now()))::float8, chargeable_failures
			 FROM phase_runs
			 WHERE run_id = $1 AND node_id = $2 AND status = 'pending' AND not_before IS NOT NULL`,
			runID, nodeID,
		).Scan(&id, &secs, &charged)
		if err == nil {
			return id, secs, charged
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no parked retry row appeared for %s", nodeID)
	return uuid.Nil, 0, 0
}

// backdateParked pulls every parked row into the past, the way plan 05's
// lease tests expire a lease: the wait is real time we refuse to spend.
func backdateParked(t *testing.T, f fixture, runID uuid.UUID) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE phase_runs SET not_before = now() - interval '1 second'
		 WHERE run_id = $1 AND status = 'pending' AND not_before > now()`, runID,
	); err != nil {
		t.Fatal(err)
	}
}

// startBackdater keeps pulling parked rows into the past for tests that
// must walk a node through several waits without sleeping through them.
func startBackdater(t *testing.T, f fixture, runID uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				_, _ = f.pool.Exec(ctx,
					`UPDATE phase_runs SET not_before = now() - interval '1 second'
					 WHERE run_id = $1 AND status = 'pending' AND not_before > now()`, runID)
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
}

func runUsage(t *testing.T, f fixture, runID uuid.UUID) int64 {
	t.Helper()
	var used int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT execution_used_ms FROM runs WHERE id = $1`, runID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	return used
}

func scalarCount(t *testing.T, f fixture, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// resetExecutor reports a cap window with a provider-stated reset — the
// shape a real harness surfaces when it names when it will let us back in.
type resetExecutor struct {
	inner  runs.Executor
	nodeID string
	after  time.Duration
}

func (e resetExecutor) Execute(ctx context.Context, req runs.ExecutionRequest) (runs.ExecutionResult, error) {
	if req.Ref.NodeID == e.nodeID {
		at := time.Now().UTC().Add(e.after)
		return runs.ExecutionResult{}, runs.OutcomeError{Outcome: runs.Outcome{
			Class: runs.OutcomeTransient, Code: runs.CodeRateLimited,
			Message: "cap window shut", RetryAt: &at,
		}}
	}
	return e.inner.Execute(ctx, req)
}

func (e resetExecutor) Inspect(ctx context.Context, ref runs.AttemptRef) (runs.InspectResult, error) {
	return e.inner.Inspect(ctx, ref)
}

// slowBusyExecutor burns measurable wall time before reporting the
// credential held elsewhere: if that contention charged, the run's usage
// could not still read zero afterwards.
type slowBusyExecutor struct {
	inner  runs.Executor
	nodeID string
	take   time.Duration
}

func (e slowBusyExecutor) Execute(ctx context.Context, req runs.ExecutionRequest) (runs.ExecutionResult, error) {
	if req.Ref.NodeID == e.nodeID {
		select {
		case <-ctx.Done():
			return runs.ExecutionResult{}, ctx.Err()
		case <-time.After(e.take):
		}
		return runs.ExecutionResult{}, runs.OutcomeError{Outcome: runs.Outcome{
			Class: runs.OutcomeTransient, Code: runs.CodeCredentialBusy,
			Message: "single-flight lease held elsewhere",
		}}
	}
	return e.inner.Execute(ctx, req)
}

func (e slowBusyExecutor) Inspect(ctx context.Context, ref runs.AttemptRef) (runs.InspectResult, error) {
	return e.inner.Inspect(ctx, ref)
}

// newWaitingRun creates a task whose root node fails with the scripted
// wait outcome, and starts the loop against it.
func newWaitingRun(t *testing.T, instructions string, snap runs.PolicySnapshot) (fixture, *runs.Service, uuid.UUID) {
	t.Helper()
	f := newFixture(t)
	doc := agenticDoc()
	doc.Nodes[0].Instructions = instructions
	svc, _ := newService(t, f, doc, snap)
	_, run, err := svc.CreateTask(context.Background(), principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "wait",
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, svc, run.ID
}

// A parked retry is invisible to every claimant until its not_before
// passes on the database clock — that invisibility IS the wait. Nothing
// in-process enforces it: the row simply does not match the claim CTE.
func TestDelayedRetry_ParkedRowIsInvisibleUntilEligible(t *testing.T) {
	f, svc, runID := newWaitingRun(t,
		"Plan.\ncaesar-fake: fail attempts=1 class=transient code=rate_limited", runs.DefaultSnapshot())
	runOrchestrator(t, svc, fake.NewExecutor(), 30*time.Second)
	ctx := context.Background()

	parkedID, secs, charged := parkedRow(t, f, runID, "plan")
	if secs < 120 {
		t.Errorf("parked %.1fs out, want at least the 2m rate_limited floor", secs)
	}
	if charged != 0 {
		t.Errorf("chargeable_failures = %d after a cap wait, want 0", charged)
	}

	// The claim statement itself is the enforcement: an independent
	// claimant, racing nothing, still sees nothing.
	if _, ok, err := runs.ClaimNextForTest(ctx, svc, "outsider", 30); err != nil || ok {
		t.Fatalf("parked row was claimable: ok=%v err=%v", ok, err)
	}
	// And the running workers leave it alone too.
	time.Sleep(150 * time.Millisecond)
	var status string
	if err := f.pool.QueryRow(ctx, `SELECT status FROM phase_runs WHERE id = $1`, parkedID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("parked row moved to %s while its window was shut", status)
	}

	// Eligible: the same row, same process, now runs and the node clears.
	backdateParked(t, f, runID)
	waitForRun(t, svc, runID, runs.RunSucceeded)
}

// Waits are not failures: a node may wait far past max_attempts and still
// succeed, because the claim predicate bounds chargeable_failures, not the
// attempt ordinal.
func TestDelayedRetry_WaitsNeverExhaustAttempts(t *testing.T) {
	snap := runs.DefaultSnapshot()
	snap.MaxAttempts = 2
	f, svc, runID := newWaitingRun(t,
		"Plan.\ncaesar-fake: fail attempts=4 class=transient code=rate_limited", snap)
	startBackdater(t, f, runID)
	runOrchestrator(t, svc, fake.NewExecutor(), 30*time.Second)

	waitForRun(t, svc, runID, runs.RunSucceeded)

	var maxAttempt, maxCharged int32
	if err := f.pool.QueryRow(context.Background(),
		`SELECT max(attempt), max(chargeable_failures) FROM phase_runs WHERE run_id = $1 AND node_id = 'plan'`,
		runID,
	).Scan(&maxAttempt, &maxCharged); err != nil {
		t.Fatal(err)
	}
	if maxAttempt != 5 {
		t.Errorf("final plan attempt ordinal = %d, want 5 (four waits, then the run)", maxAttempt)
	}
	if maxAttempt <= snap.MaxAttempts {
		t.Errorf("ordinal %d never passed max_attempts %d — the test proved nothing", maxAttempt, snap.MaxAttempts)
	}
	if maxCharged != 0 {
		t.Errorf("chargeable_failures reached %d across four waits, want 0", maxCharged)
	}
}

// A wait that would end after the run's wall deadline is not a wait — the
// row could never legally run again, so the run dies now, of the wait's
// own typed outcome rather than a generic exhaustion.
func TestDelayedRetry_WaitPastWallDeadlineTerminalizesNow(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	ctx := context.Background()
	_, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "deadline-boundary",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Alive enough to claim and run, far too short to outlast the reset.
	if _, err := f.pool.Exec(ctx,
		`UPDATE runs SET wall_deadline = now() + interval '30 seconds' WHERE id = $1`, run.ID); err != nil {
		t.Fatal(err)
	}
	runOrchestrator(t, svc, resetExecutor{inner: fake.NewExecutor(), nodeID: "plan", after: 2 * time.Hour}, 30*time.Second)

	final := waitForRun(t, svc, run.ID, runs.RunFailed)
	if final.Outcome == nil || final.Outcome.Code != runs.CodeRateLimited {
		t.Errorf("outcome = %+v, want the wait's own rate_limited code", final.Outcome)
	}
	if n := scalarCount(t, f,
		`SELECT count(*) FROM phase_runs WHERE run_id = $1 AND not_before IS NOT NULL`, run.ID); n != 0 {
		t.Errorf("%d parked rows survive a run that can never reach them, want 0", n)
	}
}

// not_before is durable state, not a timer someone is holding: the process
// that parked the row can die and a fresh one picks the work up.
func TestDelayedRetry_ParkedRowSurvivesRestart(t *testing.T) {
	f, svc, runID := newWaitingRun(t,
		"Plan.\ncaesar-fake: fail attempts=1 class=transient code=rate_limited", runs.DefaultSnapshot())
	// One executor across both loops: the harness is the world outside
	// this process, and only the process restarts.
	ex := fake.NewExecutor()
	_, stop := runOrchestrator(t, svc, ex, 30*time.Second)

	parkedID, _, _ := parkedRow(t, f, runID, "plan")
	stop() // the process that scheduled the wait is gone

	backdateParked(t, f, runID)
	runOrchestrator(t, svc, ex, 30*time.Second)
	waitForRun(t, svc, runID, runs.RunSucceeded)

	var status string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT status FROM phase_runs WHERE id = $1`, parkedID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" {
		t.Errorf("the parked row ended %s, want succeeded — a new process must be able to run it", status)
	}
}

// With no provider reset to trust, the delay is our own guess and sits on
// a hard [floor, cap] — jitter included — and one wait costs exactly one
// new row.
func TestDelayedRetry_FallbackDelayHoldsFloorAndCap(t *testing.T) {
	f, svc, runID := newWaitingRun(t,
		"Plan.\ncaesar-fake: fail attempts=1 class=transient code=rate_limited", runs.DefaultSnapshot())
	runOrchestrator(t, svc, fake.NewExecutor(), 30*time.Second, func(o *runs.Orchestrator) {
		runs.SetJitterForTest(o, func() float64 { return 1 }) // the widest the band goes
	})

	_, secs, _ := parkedRow(t, f, runID, "plan")
	if secs < 120 || secs > 3600 {
		t.Errorf("fallback wait %.1fs outside the hard [2m, 1h] bound", secs)
	}
	if n := scalarCount(t, f,
		`SELECT count(*) FROM phase_runs WHERE run_id = $1 AND node_id = 'plan'`, runID); n != 2 {
		t.Errorf("plan has %d rows after one wait, want 2 (the failure and its one successor)", n)
	}
}

// Lease contention is somebody else's turn, not our failure: it spends
// neither the execution budget nor the node's failure budget, and the
// requeued row is invisible while it waits.
func TestDelayedRetry_CredentialBusyChargesNothing(t *testing.T) {
	f, svc, runID := newWaitingRun(t, "Plan.", runs.DefaultSnapshot())
	runOrchestrator(t, svc, slowBusyExecutor{
		inner: fake.NewExecutor(), nodeID: "plan", take: 50 * time.Millisecond,
	}, 30*time.Second)
	ctx := context.Background()

	_, secs, charged := parkedRow(t, f, runID, "plan")
	if charged != 0 {
		t.Errorf("chargeable_failures = %d after lease contention, want 0", charged)
	}
	if secs < 29 || secs > 600 {
		t.Errorf("parked %.1fs out, want the [30s, 10m] credential_busy band (less the poll lag)", secs)
	}
	// The attempt burned 50ms of wall time; charging it would show here.
	if used := runUsage(t, f, runID); used != 0 {
		t.Errorf("execution_used_ms = %d after a zero-charge requeue, want 0", used)
	}
	if _, ok, err := runs.ClaimNextForTest(ctx, svc, "outsider", 30); err != nil || ok {
		t.Fatalf("a waiting credential_busy row was claimable: ok=%v err=%v", ok, err)
	}
}

// The charged path is untouched: an ordinary transient failure still
// spends the budget and still kills the run when it runs out.
func TestDelayedRetry_ChargedFailuresStillExhaust(t *testing.T) {
	snap := runs.DefaultSnapshot()
	snap.MaxAttempts = 2
	f, svc, runID := newWaitingRun(t,
		"Plan.\ncaesar-fake: fail attempts=99 class=transient code=harness_error", snap)
	runOrchestrator(t, svc, fake.NewExecutor(), 30*time.Second)

	final := waitForRun(t, svc, runID, runs.RunFailed)
	if final.Outcome == nil || final.Outcome.Code != runs.CodeAttemptsExhausted {
		t.Errorf("outcome = %+v, want attempts_exhausted", final.Outcome)
	}
	if n := scalarCount(t, f,
		`SELECT count(*) FROM phase_runs WHERE run_id = $1 AND not_before IS NOT NULL`, runID); n != 0 {
		t.Errorf("%d charged failures parked a row, want 0 — only waits park", n)
	}
	var charged int32
	if err := f.pool.QueryRow(context.Background(),
		`SELECT max(chargeable_failures) FROM phase_runs WHERE run_id = $1 AND node_id = 'plan'`,
		runID,
	).Scan(&charged); err != nil {
		t.Fatal(err)
	}
	if charged != snap.MaxAttempts-1 {
		t.Errorf("final chargeable_failures = %d, want %d", charged, snap.MaxAttempts-1)
	}
}
