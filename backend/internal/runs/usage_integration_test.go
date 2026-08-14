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

// staleClock is the system clock read a fixed span in the past. It stands in
// for every reason an app-side reading is older than the statement it is
// written by — a preempted goroutine, a slow round trip, a host whose clock
// trails the database's. The span is constant only so the test is
// deterministic; nothing here depends on it being constant.
type staleClock struct{ behind time.Duration }

func (c staleClock) Now() time.Time { return time.Now().Add(-c.behind) }

// slowExecutor is the fake with a floor under how long an attempt takes, so
// the run's usage is a quantity worth asserting on rather than a rounding
// artefact. It is deliberately LESS capable than the real thing, never more.
type slowExecutor struct {
	*fake.Executor
	dwell time.Duration
}

func (e slowExecutor) Execute(ctx context.Context, req runs.ExecutionRequest) (runs.ExecutionResult, error) {
	select {
	case <-time.After(e.dwell):
	case <-ctx.Done():
		return runs.ExecutionResult{}, ctx.Err()
	}
	return e.Executor.Execute(ctx, req)
}

// An attempt's claim stamp is the DATABASE's view of when the lease was
// taken, never the app's.
//
// Usage is the sum of claim->terminal intervals, so the stamp is not a label:
// it is one end of an amount the run is charged for. An app-side reading is
// taken BEFORE the claim statement lands, and everything in between —
// scheduler preemption, the query's own round trip — is billed to the run.
// That lag is unbounded on a loaded host, and once it exceeds the gap between
// a run's creation and its first claim, the charged interval starts before
// the run existed and successive attempts' intervals overlap. Usage then
// exceeds the run's own wall time, which is arithmetically impossible for
// disjoint intervals inside a lifetime, and a run can be terminalized
// budget_exhausted having spent nothing.
//
// The stale clock here reproduces that deterministically: it is what a lagged
// reading looks like when the lag holds still.
func TestUsageIntervalIgnoresTheAppClock(t *testing.T) {
	f := newFixture(t)
	svc, _ := newServiceWithClock(t, f, agenticDoc(), runs.DefaultSnapshot(),
		staleClock{behind: 2 * time.Second})
	ctx := context.Background()

	before := dbNow(t, f)
	startOrchestrator(t, svc, slowExecutor{Executor: fake.NewExecutor(), dwell: 25 * time.Millisecond},
		30*time.Second)

	_, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "usage",
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForRun(t, svc, run.ID, runs.RunSucceeded)
	after := dbNow(t, f)

	rows, err := f.pool.Query(ctx,
		`SELECT node_id, attempt, claimed_at FROM phase_runs WHERE run_id = $1 ORDER BY created_at`, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	claims := 0
	for rows.Next() {
		var nodeID string
		var attempt int32
		var claimedAt *time.Time
		if err := rows.Scan(&nodeID, &attempt, &claimedAt); err != nil {
			t.Fatal(err)
		}
		if claimedAt == nil {
			t.Errorf("%s#%d: a phase this run completed carries no claim stamp", nodeID, attempt)
			continue
		}
		claims++
		// The claim happened inside this window by construction: both bounds
		// are database readings, one taken before the run existed and one
		// after it finished. A stamp outside them was not read from the clock
		// that granted the lease.
		if claimedAt.Before(before) || claimedAt.After(after) {
			t.Errorf("%s#%d: claimed_at %v outside the database window [%v, %v] the claim provably fell in",
				nodeID, attempt, claimedAt.UTC(), before.UTC(), after.UTC())
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if claims == 0 {
		t.Fatal("no phases to check — the fixture proved nothing")
	}

	// Both ends of the interval must come from the same clock. Taking only the
	// start from the database and leaving the end app-side inverts the
	// subtraction under a lagging app clock, and the statement's own
	// GREATEST(delta, 0) floor turns that into a run that executed for free.
	if final.ExecutionUsedMS <= 0 {
		t.Errorf("execution_used_ms = %d, want positive: %d attempts each dwelled 25ms",
			final.ExecutionUsedMS, claims)
	}
	if final.FinishedAt == nil {
		t.Fatal("a succeeded run must carry finished_at")
	}
	if wall := final.FinishedAt.Sub(final.CreatedAt).Milliseconds(); final.ExecutionUsedMS > wall {
		t.Errorf("execution_used_ms = %d exceeds the run's own wall time %dms",
			final.ExecutionUsedMS, wall)
	}
}

// dbNow reads the clock that every claim, lease and deadline predicate is
// settled against.
func dbNow(t *testing.T, f fixture) time.Time {
	t.Helper()
	var now time.Time
	if err := f.pool.QueryRow(context.Background(), `SELECT now()::timestamptz`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now
}
