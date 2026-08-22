//go:build integration

package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/realtime"
	"github.com/giiorg/caesar/backend/internal/runs"
)

// The event source reads run_events, and run_events has an FK to runs,
// which reaches tasks, repos, workflow_versions, credentials and users.
// That whole chain is seeded with raw SQL: these tests are about what the
// source reads, not about how a run comes to exist.

// seededRun is the coordinates of one run the tests write events for.
type seededRun struct {
	runID     uuid.UUID
	projectID uuid.UUID
}

// seededTask is everything a run needs to exist EXCEPT the run row. It is
// separate because one test — TestStream_CarriesAnOrchestratedPhaseTransition —
// has to insert its run only once a stream is already live, and the rows below
// it are the orchestrator's inputs rather than the subject.
type seededTask struct {
	taskID    uuid.UUID
	projectID uuid.UUID
}

// emptyWorkflowDocument is what a run whose graph nothing compiles gets. Every
// test that only appends events by hand seeds through it, and what it buys is
// a run NOTHING WILL EVER MATERIALIZE A PHASE FOR.
//
// Not because no orchestrator looks: the bootServe'd tests in the two files
// beside this one run a real janitor, and these rows are exactly what it goes
// looking for — ListRunsAwaitingMaterialization selects a 'running' run with
// no live phase, which every run seeded here is. It loads them on each poll,
// `Compile` refuses the empty node list ("runs: compile: no nodes",
// compile.go:192), and materializeResumedRuns logs "resumed plan load" and
// moves to the next id. The pass is a no-op every time.
//
// So a node list would be worse than decoration: it would let that janitor
// materialize a root node and append a real phase.* event to a run some test
// is asserting the exact event set of.
const emptyWorkflowDocument = `{"schema":1,"nodes":[]}`

func seedRealtimeRun(ctx context.Context, t *testing.T, pool *pgxpool.Pool, name string) seededRun {
	t.Helper()
	task := seedRealtimeTask(ctx, t, pool, name, emptyWorkflowDocument)
	return seededRun{runID: insertRealtimeRun(ctx, t, pool, task, "running"), projectID: task.projectID}
}

// seedRealtimeTask writes the user, project, credential, repo, workflow,
// version and task one run hangs off. document is the workflow version's
// stored definition: a run the orchestrator will START needs one that
// compiles, and every other caller passes emptyWorkflowDocument.
func seedRealtimeTask(ctx context.Context, t *testing.T, pool *pgxpool.Pool, name, document string) seededTask {
	t.Helper()
	scan := func(step string, row pgx.Row) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := row.Scan(&id); err != nil {
			t.Fatalf("seed %s for %q: %v", step, name, err)
		}
		return id
	}
	userID := scan("user", pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, role) VALUES ($1, 'x', 'admin') RETURNING id`,
		name+"@example.com"))
	projectID := scan("project", pool.QueryRow(ctx,
		`INSERT INTO projects (name) VALUES ($1) RETURNING id`, name))
	credID := scan("credential", pool.QueryRow(ctx,
		`INSERT INTO credentials (id, name, kind, key_id, wrapped_dek, ciphertext)
		 VALUES (uuidv7(), $1, 'forge_token', 'dev', '\x00', '\x00') RETURNING id`,
		name+"-token"))
	repoID := scan("repo", pool.QueryRow(ctx,
		`INSERT INTO repos (project_id, forge_kind, slug, default_branch, credential_id)
		 VALUES ($1, 'github', $2, 'main', $3) RETURNING id`,
		projectID, "acme/"+name, credID))
	workflowID := scan("workflow", pool.QueryRow(ctx,
		`INSERT INTO workflows (name) VALUES ($1) RETURNING id`, name+"-wf"))
	versionID := scan("workflow version", pool.QueryRow(ctx,
		`INSERT INTO workflow_versions (workflow_id, version, content_hash, document)
		 VALUES ($1, 1, 'deadbeef', $2::jsonb) RETURNING id`,
		workflowID, document))
	taskID := scan("task", pool.QueryRow(ctx,
		`INSERT INTO tasks (project_id, repo_id, workflow_version_id, title, requester_id, created_at)
		 VALUES ($1, $2, $3, $4, $5, now()) RETURNING id`,
		projectID, repoID, versionID, name+" task", userID))
	return seededTask{taskID: taskID, projectID: projectID}
}

// insertRealtimeRun adds one run row in the given status. 'pending' is the
// status the orchestrator's janitor acts on; every other caller wants
// 'running', which it never touches.
func insertRealtimeRun(ctx context.Context, t *testing.T, pool *pgxpool.Pool, task seededTask, status string) uuid.UUID {
	t.Helper()
	var runID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO runs (task_id, status, definition_hash, base_sha, policy_snapshot,
		                   execution_budget_ms, wall_deadline, created_at)
		 VALUES ($1, $2, 'deadbeef', 'abc123',
		         '{"schema":2,"max_attempts":3,"execution_budget_ms":7200000,"wall_clock_ms":86400000}'::jsonb,
		         7200000, now() + interval '1 day', now()) RETURNING id`,
		task.taskID, status,
	).Scan(&runID); err != nil {
		t.Fatalf("seed %s run: %v", status, err)
	}
	return runID
}

// seededEvent is one row a test wrote. The tests keep the whole set so the
// SQL's answer can be compared against Filter.Matches over the same events:
// the live path runs every event through Matches, so a query that selects a
// different set makes snapshot, replay and live disagree about what the
// stream carries — the one thing this design forbids.
type seededEvent struct {
	id        int64
	runID     uuid.UUID
	projectID uuid.UUID
	kind      string
}

func (s seededEvent) event() realtime.Event {
	return realtime.Event{ID: s.id, RunID: s.runID, ProjectID: s.projectID, Kind: s.kind}
}

// eventLog appends run_events rows with EXPLICIT, ascending ids. Production
// takes ids from run_event_counter under a row lock; here the test owns them
// so "which event is newer" is never a guess.
type eventLog struct {
	pool   *pgxpool.Pool
	nextID int64
	seeded []seededEvent
}

func newEventLog(pool *pgxpool.Pool) *eventLog { return &eventLog{pool: pool, nextID: 1} }

func (l *eventLog) append(ctx context.Context, t *testing.T, r seededRun, kind string) seededEvent {
	t.Helper()
	e := seededEvent{id: l.nextID, runID: r.runID, projectID: r.projectID, kind: kind}
	l.nextID++
	if _, err := l.pool.Exec(ctx,
		`INSERT INTO run_events (id, run_id, project_id, kind, payload, occurred_at, correlation_id)
		 VALUES ($1, $2, $3, $4, $5::jsonb, now(), 'corr')`,
		e.id, e.runID, e.projectID, e.kind, fmt.Sprintf(`{"seq":%d}`, e.id),
	); err != nil {
		t.Fatalf("append event %d (%s): %v", e.id, e.kind, err)
	}
	l.seeded = append(l.seeded, e)
	return e
}

// matches is what Filter.Matches selects out of everything seeded, oldest
// first. Every query in this file must reproduce it exactly.
func (l *eventLog) matches(f realtime.Filter) []int64 {
	var out []int64
	for _, s := range l.seeded {
		if f.Matches(s.event()) {
			out = append(out, s.id)
		}
	}
	return out
}

func eventIDs(events []realtime.Event) []int64 {
	var out []int64
	for _, e := range events {
		out = append(out, e.ID)
	}
	return out
}

func mustPool(ctx context.Context, t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// queryGate is a pgx.QueryTracer that stops ONE named sqlc statement in its
// tracks: the first time it sees the query it publishes "reached", and every
// time it sees it, it blocks the executing goroutine until the test releases.
//
// This is what makes the gaplessness test deterministic rather than timed.
// TraceQueryStart runs BEFORE pgx writes anything to the connection, so at
// the moment the test is woken the transaction is genuinely open, its
// repeatable-read snapshot already taken by the watermark read, and the
// events read genuinely not yet issued. A sleep would only make that likely
// on a slow machine and false on a fast one.
//
// testdb.QueryFaults cannot serve here: it FAILS armed statements, it does
// not hold them open. This borrows its mechanism — a tracer on the pool's
// connection config — and nothing else. No hook exists in production code.
type queryGate struct {
	name        string
	reached     chan struct{}
	release     chan struct{}
	reachedOnce sync.Once
	releaseOnce sync.Once
}

func newQueryGate(name string) *queryGate {
	return &queryGate{name: name, reached: make(chan struct{}), release: make(chan struct{})}
}

func (g *queryGate) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	// sqlc puts the query's name in the statement's leading comment, which is
	// how QueryFaults recognises statements too.
	if strings.Contains(data.SQL, "-- name: "+g.name+" ") {
		g.reachedOnce.Do(func() { close(g.reached) })
		<-g.release
	}
	return ctx
}

func (g *queryGate) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// waitReached blocks until the gated statement is about to run. The deadline
// is a test-failure timeout, not a synchronisation device: the handshake is
// the channel, and a gate that is never reached means the code under test
// stopped issuing the query this test is built around.
func (g *queryGate) waitReached(t *testing.T) {
	t.Helper()
	select {
	case <-g.reached:
	case <-time.After(30 * time.Second):
		t.Fatalf("query %q never ran", g.name)
	}
}

func (g *queryGate) Release() { g.releaseOnce.Do(func() { close(g.release) }) }

// gatedPool builds a pool whose every connection carries the gate. The
// tracer has to be installed before the pool is built, so a gated caller
// cannot share a pool with the test's own writes — which is exactly what the
// gaplessness test needs anyway: the concurrent commit must come from a
// connection the gate is not holding.
func gatedPool(ctx context.Context, t *testing.T, url string, gate *queryGate) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse pool config: %v", err)
	}
	cfg.ConnConfig.Tracer = gate
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("gated pool: %v", err)
	}
	t.Cleanup(pool.Close)
	// Registered AFTER pool.Close, because cleanups run LIFO and this one has
	// to run FIRST. A test that fails inside the gate window leaves the gated
	// goroutine parked in TraceQueryStart holding a pooled connection, and
	// puddle.Pool.Close waits on a WaitGroup for exactly that connection — so
	// closing before releasing deadlocks the whole run into the test binary's
	// timeout panic instead of reporting the failure.
	t.Cleanup(gate.Release)
	return pool
}

// TestSnapshot_IsGaplessUnderConcurrentCommit is THE acceptance evidence for
// this task.
//
// The snapshot reads its watermark and is then stopped, mid-transaction,
// with the events read still unissued. A second connection commits a new
// event into exactly that window. Three things must hold:
//
//   - every event in the snapshot is at or below the snapshot's own
//     watermark. This is the invariant the isolation level buys: under
//     read-committed the events read would take a FRESH database snapshot
//     and pick up the concurrent commit, handing back an event the watermark
//     does not cover;
//   - the watermark is below the concurrent event's id, so the client is
//     told there is more to come;
//   - Replay from that watermark returns the concurrent event.
//
// Together they mean no event can fall between the snapshot and the replay.
func TestSnapshot_IsGaplessUnderConcurrentCommit(t *testing.T) {
	ctx := context.Background()
	url := testdb.NewURL(t)
	writer := mustPool(ctx, t, url)

	run := seedRealtimeRun(ctx, t, writer, "gapless")
	log := newEventLog(writer)
	for _, kind := range []string{
		runs.EventRunCreated, runs.EventRunStarted, runs.EventPhaseMaterialized,
		runs.EventPhaseStarted, runs.EventPhaseSucceeded,
	} {
		log.append(ctx, t, run, kind)
	}

	gate := newQueryGate("ListRecentRunEvents")
	src := newRealtimeEventSource(gatedPool(ctx, t, url, gate), 500)
	f := realtime.Filter{RunID: &run.runID, Scope: realtime.Scope{Projects: []uuid.UUID{run.projectID}}}

	type outcome struct {
		snap realtime.Snapshot
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		snap, err := src.Snapshot(ctx, f)
		done <- outcome{snap: snap, err: err}
	}()

	gate.waitReached(t)
	concurrent := log.append(ctx, t, run, runs.EventRunSucceeded)
	gate.Release()

	got := <-done
	if got.err != nil {
		t.Fatalf("Snapshot: %v", got.err)
	}

	// The invariant. It holds always under repeatable read, and is violated
	// the moment a concurrent event commits between the two reads under any
	// weaker level.
	for _, e := range got.snap.Events {
		if e.ID > got.snap.Watermark.EventID {
			t.Fatalf("snapshot carries event %d above its own watermark %d: the events read saw a commit the watermark does not cover",
				e.ID, got.snap.Watermark.EventID)
		}
	}
	if got.snap.Watermark.EventID >= concurrent.id {
		t.Fatalf("watermark = %d, want below the concurrently committed event %d",
			got.snap.Watermark.EventID, concurrent.id)
	}
	if diff := cmp.Diff([]int64{1, 2, 3, 4, 5}, eventIDs(got.snap.Events)); diff != "" {
		t.Errorf("snapshot events (-want +got):\n%s", diff)
	}

	replayed, err := src.Replay(ctx, f, got.snap.Watermark.EventID, 100)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if diff := cmp.Diff([]int64{concurrent.id}, eventIDs(replayed)); diff != "" {
		t.Errorf("replay from the watermark (-want +got):\n%s", diff)
	}

	// The payload survives the round trip as the decoded map the hub fans
	// out, not as raw jsonb bytes.
	if diff := cmp.Diff(map[string]any{"seq": float64(concurrent.id)}, replayed[0].Payload); diff != "" {
		t.Errorf("replayed payload (-want +got):\n%s", diff)
	}
}

// TestSince_HandsTheTailerEveryEventOldestFirst drives the cursor exactly as
// Tailer.drain does — sort the batch, advance to the highest id handed over
// — because that is what makes Since's ordering load-bearing. A source that
// returns a truncation of some OTHER order moves the cursor past ids it
// never handed over, and the tailer's sort cannot recover them.
func TestSince_HandsTheTailerEveryEventOldestFirst(t *testing.T) {
	ctx := context.Background()
	pool := mustPool(ctx, t, testdb.NewURL(t))

	runA := seedRealtimeRun(ctx, t, pool, "since-a")
	runB := seedRealtimeRun(ctx, t, pool, "since-b")
	log := newEventLog(pool)
	for i, r := range []seededRun{runA, runB, runA, runB, runA, runA, runB} {
		kind := runs.EventPhaseStarted
		if i%2 == 0 {
			kind = runs.EventRunStarted
		}
		log.append(ctx, t, r, kind)
	}

	src := newRealtimeEventSource(pool, 500)

	const batch = 3
	var (
		cursor int64
		seen   []int64
	)
	for range 10 {
		got, err := src.Since(ctx, cursor, batch)
		if err != nil {
			t.Fatalf("Since(%d, %d): %v", cursor, batch, err)
		}
		if len(got) == 0 {
			break
		}
		slices.SortFunc(got, func(a, b realtime.Event) int {
			switch {
			case a.ID < b.ID:
				return -1
			case a.ID > b.ID:
				return 1
			}
			return 0
		})
		seen = append(seen, eventIDs(got)...)
		cursor = got[len(got)-1].ID
	}
	if diff := cmp.Diff([]int64{1, 2, 3, 4, 5, 6, 7}, seen); diff != "" {
		t.Errorf("ids the tailer was handed (-want +got):\n%s", diff)
	}

	// Since is deliberately filter-blind: fan-out is per subscriber.
	all, err := src.Since(ctx, 0, 100)
	if err != nil {
		t.Fatalf("Since(0, 100): %v", err)
	}
	if diff := cmp.Diff([]int64{1, 2, 3, 4, 5, 6, 7}, eventIDs(all)); diff != "" {
		t.Errorf("unfiltered drain (-want +got):\n%s", diff)
	}
}

// unattendedKind is an event kind in NONE of the attention namespaces, and
// this fixture cannot do its job without one.
//
// Plan 12a added `phase.` to the prefixes, which made every kind
// runs/events.go defines attention-worthy — so a fixture seeded from that
// vocabulary alone would be selected in full by a query that dropped the kind
// test altogether, and the SQL-vs-Matches comparison below would pass on a
// snapshot that carries everything. It is a literal rather than a constant
// from runs precisely because no production writer may emit it: the moment one
// does, this row stops testing the exclusion and has to be replaced.
const unattendedKind = "audit.recorded"

// TestAttentionStream_SelectsExactlyWhatMatchesSelects pins the two gaps the
// attention query has to close in SQL: the kind prefixes the global stream
// is defined by, and the admin case that no project-id list can express.
//
// Both assertions are stated as "the same set Filter.Matches selects", so a
// query that drifts from Matches fails here rather than in production, where
// it would show up as a snapshot carrying events the live stream drops.
func TestAttentionStream_SelectsExactlyWhatMatchesSelects(t *testing.T) {
	ctx := context.Background()
	pool := mustPool(ctx, t, testdb.NewURL(t))

	runA := seedRealtimeRun(ctx, t, pool, "attn-a")
	runB := seedRealtimeRun(ctx, t, pool, "attn-b")
	log := newEventLog(pool)
	for _, kind := range []string{
		runs.EventRunStarted,        // 1 attention
		runs.EventPhaseMaterialized, // 2 attention (Plan 12a)
		runs.EventGateWaiting,       // 3 attention
		unattendedKind,              // 4 NOT attention
		runs.EventPublishPushed,     // 5 attention
	} {
		log.append(ctx, t, runA, kind)
	}
	log.append(ctx, t, runB, runs.EventRunStarted) // 6 attention, other project
	log.append(ctx, t, runB, unattendedKind)       // 7 neither

	src := newRealtimeEventSource(pool, 500)

	member := realtime.Filter{Attention: true, Scope: realtime.Scope{Projects: []uuid.UUID{runA.projectID}}}
	admin := realtime.Filter{Attention: true, Scope: realtime.Scope{All: true}}

	for _, tc := range []struct {
		name string
		f    realtime.Filter
	}{
		{"granted one project", member},
		{"admin sees every project", admin},
		{"no grants at all", realtime.Filter{Attention: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := log.matches(tc.f)

			snap, err := src.Snapshot(ctx, tc.f)
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			if diff := cmp.Diff(want, eventIDs(snap.Events)); diff != "" {
				t.Errorf("snapshot vs Filter.Matches (-want +got):\n%s", diff)
			}

			replayed, err := src.Replay(ctx, tc.f, 0, 100)
			if err != nil {
				t.Fatalf("Replay: %v", err)
			}
			if diff := cmp.Diff(want, eventIDs(replayed)); diff != "" {
				t.Errorf("replay vs Filter.Matches (-want +got):\n%s", diff)
			}
		})
	}
}

// TestRunStream_IsScopedAndCarriesTheMostRecentEvents pins the per-run
// stream: the snapshot is the most recent N matching events ASCENDING, and
// scope is enforced on the run stream too — Matches checks it first and
// unconditionally, so a query that only filters by run_id would hand a
// revoked subscriber a run they may not see.
func TestRunStream_IsScopedAndCarriesTheMostRecentEvents(t *testing.T) {
	ctx := context.Background()
	pool := mustPool(ctx, t, testdb.NewURL(t))

	runA := seedRealtimeRun(ctx, t, pool, "run-a")
	runB := seedRealtimeRun(ctx, t, pool, "run-b")
	log := newEventLog(pool)
	for i := range 5 {
		kind := runs.EventPhaseStarted
		if i == 0 {
			kind = runs.EventRunCreated
		}
		log.append(ctx, t, runA, kind)
	}
	log.append(ctx, t, runB, runs.EventRunCreated) // 6, another run and project

	// Three at a time, so the "most recent N" trim is observable.
	src := newRealtimeEventSource(pool, 3)

	granted := realtime.Filter{RunID: &runA.runID, Scope: realtime.Scope{Projects: []uuid.UUID{runA.projectID}}}
	snap, err := src.Snapshot(ctx, granted)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if diff := cmp.Diff([]int64{3, 4, 5}, eventIDs(snap.Events)); diff != "" {
		t.Errorf("snapshot is not the most recent 3 ascending (-want +got):\n%s", diff)
	}
	if snap.Watermark.EventID != 6 {
		t.Errorf("watermark = %d, want 6 — the head of the WHOLE table, not of the filter",
			snap.Watermark.EventID)
	}

	// Replay pages by the last id returned, oldest first.
	first, err := src.Replay(ctx, granted, 0, 2)
	if err != nil {
		t.Fatalf("Replay page 1: %v", err)
	}
	if diff := cmp.Diff([]int64{1, 2}, eventIDs(first)); diff != "" {
		t.Errorf("replay page 1 (-want +got):\n%s", diff)
	}
	second, err := src.Replay(ctx, granted, first[len(first)-1].ID, 2)
	if err != nil {
		t.Fatalf("Replay page 2: %v", err)
	}
	if diff := cmp.Diff([]int64{3, 4}, eventIDs(second)); diff != "" {
		t.Errorf("replay page 2 (-want +got):\n%s", diff)
	}

	// The same run, without a grant on its project: Matches says nothing, and
	// so must every query.
	revoked := realtime.Filter{RunID: &runA.runID, Scope: realtime.Scope{Projects: []uuid.UUID{runB.projectID}}}
	if want := log.matches(revoked); want != nil {
		t.Fatalf("fixture bug: Filter.Matches selects %v for an ungranted project", want)
	}
	snap, err = src.Snapshot(ctx, revoked)
	if err != nil {
		t.Fatalf("Snapshot ungranted: %v", err)
	}
	if got := eventIDs(snap.Events); got != nil {
		t.Errorf("snapshot for an ungranted project = %v, want none", got)
	}
	replayed, err := src.Replay(ctx, revoked, 0, 100)
	if err != nil {
		t.Fatalf("Replay ungranted: %v", err)
	}
	if got := eventIDs(replayed); got != nil {
		t.Errorf("replay for an ungranted project = %v, want none", got)
	}

	// RunID WINS over Attention. Matches states the precedence and both
	// dispatch switches in the adapter reproduce it by case ORDER, which
	// nothing else would catch: reorder either switch and this filter would
	// be served as a global stream by the snapshot or the replay while the
	// live path still treats it as a per-run one. The events are seeded so
	// the two answers differ — event 6 is attention-worthy, on a project this
	// scope grants, and belongs to a DIFFERENT run.
	both := realtime.Filter{
		RunID: &runA.runID, Attention: true,
		Scope: realtime.Scope{Projects: []uuid.UUID{runA.projectID, runB.projectID}},
	}
	wantBoth := log.matches(both)
	if diff := cmp.Diff([]int64{1, 2, 3, 4, 5}, wantBoth); diff != "" {
		t.Fatalf("fixture bug: Filter.Matches does not give RunID precedence (-want +got):\n%s", diff)
	}
	replayed, err = src.Replay(ctx, both, 0, 100)
	if err != nil {
		t.Fatalf("Replay with both selectors: %v", err)
	}
	if diff := cmp.Diff(wantBoth, eventIDs(replayed)); diff != "" {
		t.Errorf("replay with both selectors vs Filter.Matches (-want +got):\n%s", diff)
	}
	snap, err = src.Snapshot(ctx, both)
	if err != nil {
		t.Fatalf("Snapshot with both selectors: %v", err)
	}
	if diff := cmp.Diff([]int64{3, 4, 5}, eventIDs(snap.Events)); diff != "" {
		t.Errorf("snapshot with both selectors is not the per-run stream's most recent 3 (-want +got):\n%s", diff)
	}

	// A filter that selects neither stream selects nothing at all, whatever
	// its scope — the third arm of Matches.
	snap, err = src.Snapshot(ctx, realtime.Filter{Scope: realtime.Scope{All: true}})
	if err != nil {
		t.Fatalf("Snapshot with no selector: %v", err)
	}
	if got := eventIDs(snap.Events); got != nil {
		t.Errorf("snapshot with no selector = %v, want none", got)
	}
}

// TestHeadAndOldest_BoundTheRetainedRange pins the two ids the tailer and
// the resume path steer by. Both answer 0 on an empty table: Head seeds a
// startup cursor and Oldest decides whether a Last-Event-ID is too old, and
// neither has anything to fail over.
func TestHeadAndOldest_BoundTheRetainedRange(t *testing.T) {
	ctx := context.Background()
	pool := mustPool(ctx, t, testdb.NewURL(t))
	src := newRealtimeEventSource(pool, 500)

	head, err := src.Head(ctx)
	if err != nil {
		t.Fatalf("Head on an empty table: %v", err)
	}
	oldest, err := src.Oldest(ctx)
	if err != nil {
		t.Fatalf("Oldest on an empty table: %v", err)
	}
	if head != 0 || oldest != 0 {
		t.Fatalf("empty table: head = %d, oldest = %d, want 0 and 0", head, oldest)
	}

	run := seedRealtimeRun(ctx, t, pool, "bounds")
	log := newEventLog(pool)
	log.nextID = 7
	for range 3 {
		log.append(ctx, t, run, runs.EventRunStarted)
	}

	if head, err = src.Head(ctx); err != nil || head != 9 {
		t.Fatalf("Head = %d, %v, want 9", head, err)
	}
	if oldest, err = src.Oldest(ctx); err != nil || oldest != 7 {
		t.Fatalf("Oldest = %d, %v, want 7", oldest, err)
	}
}

// TestScopeFor_MirrorsTheApprovalsInbox pins the grant resolver on the same
// branch handleListApprovals takes: admin sees every project, a member sees
// exactly their grants. It reads the ROLE from the database rather than from
// the session, because it is re-consulted for the lifetime of a stream — a
// demotion has to reach an already-open subscription.
func TestScopeFor_MirrorsTheApprovalsInbox(t *testing.T) {
	ctx := context.Background()
	pool := mustPool(ctx, t, testdb.NewURL(t))

	svc := identity.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), identity.Params{
		SessionIdleTTL: 14 * 24 * time.Hour, SessionAbsoluteTTL: 90 * 24 * time.Hour,
	})
	admin, err := svc.Bootstrap(ctx, "root@example.com", "a long enough password")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	actor := identity.Principal{UserID: admin.ID, Email: admin.Email, Role: identity.RoleAdmin}
	member, err := svc.CreateUser(ctx, actor, "member@example.com", "a long enough password", identity.RoleMember)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}

	var projectA, projectB uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (name) VALUES ('scope-a') RETURNING id`).Scan(&projectA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects (name) VALUES ('scope-b') RETURNING id`).Scan(&projectB); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetGrant(ctx, actor, projectA, member.ID, authn.GrantView); err != nil {
		t.Fatalf("grant: %v", err)
	}

	grants := realtimeGrants{svc: svc}

	got, err := grants.ScopeFor(ctx, admin.ID)
	if err != nil {
		t.Fatalf("ScopeFor(admin): %v", err)
	}
	if diff := cmp.Diff(realtime.Scope{All: true}, got); diff != "" {
		t.Errorf("admin scope (-want +got):\n%s", diff)
	}

	got, err = grants.ScopeFor(ctx, member.ID)
	if err != nil {
		t.Fatalf("ScopeFor(member): %v", err)
	}
	if diff := cmp.Diff(realtime.Scope{Projects: []uuid.UUID{projectA}}, got); diff != "" {
		t.Errorf("member scope (-want +got):\n%s", diff)
	}

	// A deactivated principal sees nothing. Scope is default-deny, and this
	// is the only signal a periodic re-check has: the grant rows survive
	// deactivation, so grants alone would keep a disabled admin's stream
	// open forever.
	inactive := false
	if _, err := svc.UpdateUser(ctx, actor, member.ID, identity.UserUpdate{Active: &inactive}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	got, err = grants.ScopeFor(ctx, member.ID)
	if err != nil {
		t.Fatalf("ScopeFor(deactivated): %v", err)
	}
	if diff := cmp.Diff(realtime.Scope{}, got); diff != "" {
		t.Errorf("deactivated scope (-want +got):\n%s", diff)
	}
}
