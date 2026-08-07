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

// phaseState reads a phase row's live fencing columns.
func phaseState(t *testing.T, f fixture, phaseID uuid.UUID) (status, workerID string) {
	t.Helper()
	err := f.pool.QueryRow(context.Background(),
		`SELECT status, coalesce(worker_id, '') FROM phase_runs WHERE id = $1`, phaseID,
	).Scan(&status, &workerID)
	if err != nil {
		t.Fatal(err)
	}
	return status, workerID
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

	// The fence is row identity, not (run, node): once a successor attempt
	// exists and is live under another worker, worker-a's writes — which
	// name only its own row id — still cannot reach it.
	succID, err := runs.InsertPhaseForTest(ctx, svc, runID, "plan", 2, 3, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	c2, ok, err := runs.ClaimNextForTest(ctx, svc, "worker-b", 30)
	if err != nil || !ok {
		t.Fatalf("successor claim: ok=%v err=%v", ok, err)
	}
	if c2.PhaseID != succID || c2.Ref.Attempt != 2 || c2.Ref.RunID != runID || c2.Ref.NodeID != "plan" {
		t.Fatalf("successor claim = %+v, want row %s attempt 2", c2, succID)
	}
	if n, err := runs.FinishPhaseForTest(ctx, svc, c.PhaseID, "running", "succeeded", "worker-a"); err != nil || n != 0 {
		t.Errorf("worker-a retry: rows=%d err=%v, want 0 rows", n, err)
	}
	if status, worker := phaseState(t, f, succID); status != "running" || worker != "worker-b" {
		t.Errorf("successor row = (%s, %s), want (running, worker-b)", status, worker)
	}
	// Positive control: the rejections above only mean something if the
	// same CAS lands for the worker that actually holds the lease.
	if n, err := runs.FinishPhaseForTest(ctx, svc, succID, "running", "succeeded", "worker-b"); err != nil || n != 1 {
		t.Errorf("worker-b finish: rows=%d err=%v, want 1 row", n, err)
	}
}

// A lease that has run out does not by itself fence the worker — the
// sweeper's running -> reconciling move is what does, and after it both
// the heartbeat and the terminal CAS must lose.
func TestExpiredLeaseIsFencedByReconciling(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	ctx := context.Background()
	makePending(t, f)

	c, ok, err := runs.ClaimNextForTest(ctx, svc, "worker-a", 0)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	alive, err := runs.HeartbeatForTest(ctx, svc, c.PhaseID, "worker-a", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !alive {
		t.Error("an expired lease alone must not fence a still-running row")
	}

	// The sweeper's transition (MarkPhaseReconciling's predicate).
	tag, err := f.pool.Exec(ctx,
		`UPDATE phase_runs SET status='reconciling'
		 WHERE id=$1 AND status='running' AND leased_until <= now()`, c.PhaseID)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("sweep affected %d rows, want 1 (lease should be expired)", tag.RowsAffected())
	}

	alive, err = runs.HeartbeatForTest(ctx, svc, c.PhaseID, "worker-a", 30)
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Error("heartbeat after reconciling must report fenced")
	}
	if n, err := runs.FinishPhaseForTest(ctx, svc, c.PhaseID, "running", "succeeded", "worker-a"); err != nil || n != 0 {
		t.Errorf("terminal CAS after reconciling: rows=%d err=%v, want 0 rows", n, err)
	}
	// The sweeper is the system actor: an empty worker id skips the
	// identity predicate instead of matching NULL and losing every CAS.
	if n, err := runs.FinishPhaseForTest(ctx, svc, c.PhaseID, "reconciling", "dead", ""); err != nil || n != 1 {
		t.Errorf("system-actor finish: rows=%d err=%v, want 1 row", n, err)
	}
	// Transitions the table forbids never reach the database.
	if _, err := runs.FinishPhaseForTest(ctx, svc, c.PhaseID, "succeeded", "running", ""); err == nil {
		t.Error("succeeded -> running must be refused before the CAS")
	}
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
