//go:build integration

package runs_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
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

// The runner appends bounded runtime observations mid-attempt, outside
// any outcome transaction: each lands in its own tx, scoped to the run's
// project (resolved from the row, never passed in) and correlated by the
// attempt ref, in call order.
func TestAppendRuntimeEvent_OwnTxProjectScopedAndOrdered(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	ctx := t.Context()
	_, runID := f.newRunRow(t, "running")
	ref := runs.AttemptRef{RunID: runID, NodeID: "impl", Attempt: 2}

	if err := svc.AppendRuntimeEvent(ctx, ref, runs.EventPhaseLogTruncated, map[string]any{
		"node_id": ref.NodeID, "attempt": ref.Attempt, "stream": "agent", "cap_bytes": 16777216,
	}); err != nil {
		t.Fatalf("append runtime event: %v", err)
	}
	if err := svc.AppendRuntimeEvent(ctx, ref, runs.EventPhaseEgressDenied, map[string]any{
		"node_id": ref.NodeID, "attempt": ref.Attempt, "host": "evil.example.com",
	}); err != nil {
		t.Fatalf("append runtime event: %v", err)
	}

	rows, err := f.pool.Query(ctx,
		`SELECT kind, project_id, correlation_id FROM run_events WHERE run_id = $1 ORDER BY id`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct {
		kind        string
		projectID   uuid.UUID
		correlation string
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.kind, &r.projectID, &r.correlation); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("events = %d, want 2", len(got))
	}
	if got[0].kind != runs.EventPhaseLogTruncated || got[1].kind != runs.EventPhaseEgressDenied {
		t.Errorf("kinds by id = %s, %s, want %s, %s",
			got[0].kind, got[1].kind, runs.EventPhaseLogTruncated, runs.EventPhaseEgressDenied)
	}
	for _, r := range got {
		if r.projectID != f.projectID {
			t.Errorf("%s: project id not resolved: %s", r.kind, r.projectID)
		}
		if r.correlation != ref.String() {
			t.Errorf("%s: correlation = %s, want %s", r.kind, r.correlation, ref.String())
		}
	}

	// Own tx means committed and visible on its own — outbox envelopes
	// land with them, exactly one per event.
	var outbox int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if outbox != 2 {
		t.Errorf("outbox rows = %d, want 2", outbox)
	}
}

// The wake-up signal pins both halves of its contract: a committed append
// wakes a listener, and a rolled-back one does not. The second half is the
// one worth having — a tailer woken for an event that never committed
// would advance its cursor past a gap. The payload is asserted empty
// because LISTEN/NOTIFY is a wake-up signal and never the data.
func TestAppendEvents_NotifiesOnCommitOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, runID := f.newRunRow(t, "running")

	// The listener holds a connection of its own: a pooled one may be
	// handed to another caller between LISTEN and the wait.
	lc, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lc.Release()
	if _, err := lc.Exec(ctx, `LISTEN caesar_run_events`); err != nil {
		t.Fatal(err)
	}

	appendOne := func(tx pgx.Tx) error {
		return runs.AppendEventsForTest(ctx, tx, time.Now().UTC(), []runs.Event{
			{RunID: runID, ProjectID: f.projectID, Kind: runs.EventRunStarted},
		})
	}

	if err := pg.WithTx(ctx, f.pool, appendOne); err != nil {
		t.Fatal(err)
	}
	wake, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	n, err := lc.Conn().WaitForNotification(wake)
	if err != nil {
		t.Fatalf("commit did not wake the listener: %v", err)
	}
	if n.Channel != "caesar_run_events" {
		t.Errorf("channel = %q, want caesar_run_events", n.Channel)
	}
	if n.Payload != "" {
		t.Errorf("payload = %q, want empty: the channel carries no data", n.Payload)
	}

	boom := errors.New("boom")
	err = pg.WithTx(ctx, f.pool, func(tx pgx.Tx) error {
		if err := appendOne(tx); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("tx err = %v, want boom", err)
	}
	quiet, cancelQuiet := context.WithTimeout(ctx, time.Second)
	defer cancelQuiet()
	if n, err := lc.Conn().WaitForNotification(quiet); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("rolled-back append notified (%+v, err %v): the woken tailer would advance its cursor past a gap", n, err)
	}
}
