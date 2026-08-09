//go:build integration

package runs_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs"
)

// liveRuns counts the runs the partial unique index calls live.
func liveRuns(t *testing.T, f fixture, taskID uuid.UUID) int {
	t.Helper()
	var n int
	err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM runs WHERE task_id = $1 AND status IN ('pending','running','waiting')`,
		taskID).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Plan 05 carry-forward: two reruns racing past LatestRunForUpdate hit
// runs_task_live_key and surfaced as an unclassified 23505 — a 500 for what is
// plainly a conflict.
//
// How many reruns WIN is not an invariant: LatestRunForUpdate re-evaluates the
// row it locked rather than the ORDER BY, so a caller that arrives after the
// winner committed locks the NEW pending run and supersedes it legitimately.
// Measured winners vary run to run. What is invariant is that no loser is an
// internal fault and that the index's promise holds: exactly one live run
// survives.
func TestRerun_ConcurrentRerunsConflictInsteadOf500(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	_, run1, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "t",
	})
	if err != nil {
		t.Fatal(err)
	}

	const n = 6
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = svc.Rerun(ctx, principal(f), run1.TaskID)
		}(i)
	}
	close(start)
	wg.Wait()

	won, lost := 0, 0
	for i, err := range errs {
		switch {
		case err == nil:
			won++
		case xerrors.ClassOf(err) == xerrors.ClassConflict:
			lost++
		default:
			t.Errorf("rerun %d: %v (class %d), want a conflict", i, err, xerrors.ClassOf(err))
		}
	}
	if won == 0 {
		t.Fatal("no rerun succeeded")
	}
	// Without a loser the race never staged and the classification above was
	// never exercised, so a green run would prove nothing.
	if lost == 0 {
		t.Fatalf("no rerun lost the race (%d of %d won); the unique-violation path was never reached", won, n)
	}
	if got := liveRuns(t, f, run1.TaskID); got != 1 {
		t.Errorf("live runs = %d, want 1: runs_task_live_key admits one", got)
	}
}

// Plan 05 carry-forward: Rerun skipped the harness re-resolution CreateTask
// does, so a deleted harness surfaced only as a typed harness_error at
// execution — after minting a run that could never run.
func TestRerun_ReResolvesHarnessesSoADeletedOneIsCaughtAtIntake(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	_, run1, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "t",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The registry answers for "cc" and nothing else, so a definition naming
	// another harness is what a DELETED harness looks like at re-run time.
	ghost := agenticDoc()
	for i := range ghost.Nodes {
		ghost.Nodes[i].Harness = "ghost"
	}
	svc2, _ := newService(t, f, ghost, runs.DefaultSnapshot())

	_, err = svc2.Rerun(ctx, principal(f), run1.TaskID)
	if xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatalf("err = %v (class %d), want an invalid naming the missing harness", err, xerrors.ClassOf(err))
	}
	var xe *xerrors.Error
	if !errors.As(err, &xe) || len(xe.Fields) == 0 || xe.Fields[0].Field != "harness" {
		t.Errorf("err = %v, want a field error blaming \"harness\"", err)
	}
	if got := countRows(t, f, "runs"); got != 1 {
		t.Errorf("runs = %d, want 1: a refused rerun mints nothing", got)
	}
}
