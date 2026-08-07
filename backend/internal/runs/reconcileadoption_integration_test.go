//go:build integration

package runs_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// adoptedOutcomeExecutor is a runtime that outlived its worker: the attempt
// finished, the harness said why it stopped, and only then did the process
// holding the lease die. Reconciliation meets that answer, not a corpse.
type adoptedOutcomeExecutor struct{ out runs.Outcome }

func (e adoptedOutcomeExecutor) Execute(context.Context, runs.ExecutionRequest) (runs.ExecutionResult, error) {
	return runs.ExecutionResult{}, runs.OutcomeError{Outcome: e.out}
}

func (e adoptedOutcomeExecutor) Inspect(context.Context, runs.AttemptRef) (runs.InspectResult, error) {
	out := e.out
	return runs.InspectResult{Completed: true, Outcome: &out}, nil
}

// strandedAttempt reproduces the crash window the stuck sweep re-drives: a
// row already 'reconciling' whose lease is long past the grace period, with
// the failure budget it inherited.
func strandedAttempt(t *testing.T, f fixture, runID uuid.UUID, charged int32) uuid.UUID {
	t.Helper()
	var phaseID uuid.UUID
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, chargeable_failures, status,
		                         worker_id, claimed_at, leased_until, created_at)
		 VALUES ($1, 'plan', 1, 3, $2, 'reconciling', 'dead-worker',
		         now() - interval '70 seconds', now() - interval '65 seconds', now())
		 RETURNING id`, runID, charged,
	).Scan(&phaseID); err != nil {
		t.Fatal(err)
	}
	return phaseID
}

func phaseStatus(t *testing.T, f fixture, phaseID uuid.UUID) string {
	t.Helper()
	var status string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := f.pool.QueryRow(context.Background(),
			`SELECT status FROM phase_runs WHERE id = $1`, phaseID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "reconciling" {
			return status
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("attempt %s never left reconciling", phaseID)
	return ""
}

// A cap window the harness reported before the worker died is a WAIT, and it
// waits the same way the worker path does: a parked successor carrying the
// provider's own reset, with the node's failure budget untouched. Concluding
// it reconcile_lost instead would charge the node for a subscription's
// bookkeeping.
func TestReconcileAdoption_RateLimitedParksWithoutCharging(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	_, runID := f.newRunRow(t, "running")
	phaseID := strandedAttempt(t, f, runID, 1)

	at := time.Now().UTC().Add(20 * time.Minute)
	startOrchestrator(t, svc, adoptedOutcomeExecutor{out: runs.Outcome{
		Class: runs.OutcomeTransient, Code: runs.CodeRateLimited,
		Message: "harness exited 1: usage limit reached", RetryAt: &at,
	}}, 30*time.Second)

	if got := phaseStatus(t, f, phaseID); got != "failed" {
		t.Fatalf("adopted attempt status = %q, want failed (a wait always has a successor)", got)
	}
	_, secs, charged := parkedRow(t, f, runID, "plan")
	if charged != 1 {
		t.Errorf("chargeable_failures = %d after an adopted cap wait, want the inherited 1", charged)
	}
	// The provider named the moment; the successor must wait for THAT, not
	// for the fallback band a reconcile_lost retry would have used.
	if secs < 900 || secs > 1300 {
		t.Errorf("parked %.1fs out, want the stated ~20m reset", secs)
	}
}

// The same wait, against a run that cannot outlive it: parking would leave a
// row that could never legally run when it woke, so the run dies now — of the
// cap's own typed code, on the reconcile path exactly as on the worker's.
func TestReconcileAdoption_RateLimitedPastWallDeadlineTerminalizes(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	ctx := context.Background()
	_, runID := f.newRunRow(t, "running")
	if _, err := f.pool.Exec(ctx,
		`UPDATE runs SET wall_deadline = now() + interval '30 seconds' WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	strandedAttempt(t, f, runID, 0)

	at := time.Now().UTC().Add(2 * time.Hour)
	startOrchestrator(t, svc, adoptedOutcomeExecutor{out: runs.Outcome{
		Class: runs.OutcomeTransient, Code: runs.CodeRateLimited,
		Message: "harness exited 1: usage limit reached", RetryAt: &at,
	}}, 30*time.Second)

	final := waitForRun(t, svc, runID, runs.RunFailed)
	if final.Outcome == nil || final.Outcome.Code != runs.CodeRateLimited {
		t.Errorf("outcome = %+v, want the wait's own rate_limited code", final.Outcome)
	}
	if n := scalarCount(t, f,
		`SELECT count(*) FROM phase_runs WHERE run_id = $1 AND not_before IS NOT NULL`, runID); n != 0 {
		t.Errorf("%d parked rows survive a run that can never reach them, want 0", n)
	}
}

// A dead credential is permanent however it is discovered: adoption reports
// it as the harness stated it — naming the credential an operator has to fix
// — instead of retrying an attempt that cannot authenticate.
func TestReconcileAdoption_AuthExpiredFailsTheRunNamingTheCredential(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	_, runID := f.newRunRow(t, "running")
	phaseID := strandedAttempt(t, f, runID, 0)
	credentialID := uuid.New()

	startOrchestrator(t, svc, adoptedOutcomeExecutor{out: runs.Outcome{
		Class: runs.OutcomePermanent, Code: runs.CodeAuthExpired,
		Message: "credential " + credentialID.String() + ": harness exited 1: Please run /login",
	}}, 30*time.Second)

	final := waitForRun(t, svc, runID, runs.RunFailed)
	if final.Outcome == nil || final.Outcome.Code != runs.CodeAuthExpired {
		t.Fatalf("outcome = %+v, want auth_expired", final.Outcome)
	}
	if !strings.Contains(final.Outcome.Message, credentialID.String()) {
		t.Errorf("outcome message = %q, want it to name credential %s", final.Outcome.Message, credentialID)
	}
	if got := phaseStatus(t, f, phaseID); got != "dead" {
		t.Errorf("attempt status = %q, want dead", got)
	}
	if n := scalarCount(t, f,
		`SELECT count(*) FROM phase_runs WHERE run_id = $1 AND attempt > 1`, runID); n != 0 {
		t.Errorf("%d successors were created for a permanent failure, want 0", n)
	}
}
