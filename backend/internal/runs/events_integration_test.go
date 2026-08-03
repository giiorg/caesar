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

	// A committed append lands both, ids from the counter — the seeded
	// counter row starts at 1, so the first id ever allocated is 1.
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
		t.Errorf("ids = %v, want [1 2] (first id is the seeded 1; rollback must not burn ids)", ids)
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
