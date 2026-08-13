package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/realtime"
	realtimeapi "github.com/giiorg/caesar/backend/internal/realtime/api"
	"github.com/giiorg/caesar/backend/internal/runs"
	runsapi "github.com/giiorg/caesar/backend/internal/runs/api"
	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// The per-run stream reaches realtime through runs/api's own port, and
// nothing else in this process calls ServeRunStream by that name. Without
// this line a signature drift between the two peers is caught only by
// whichever request happens to exercise it.
//
// It lives HERE because internal/app is the only package that may name both:
// runs and realtime are bounded contexts and never import each other.
var _ runsapi.StreamServer = (*realtimeapi.API)(nil)

// realtimeEventSource implements realtime.EventSource over the runs store.
// It lives here because realtime is a bounded context and never imports
// runs; this is the composition root, the one place that sees both.
type realtimeEventSource struct {
	pool *pgxpool.Pool
	// snapshotEvents caps Snapshot's backlog. startRealtime passes
	// config.Realtime.SnapshotEvents, which is a knob of its own and
	// deliberately NOT ReplayBatch: see there.
	snapshotEvents int32
}

// The port is not consumed inside app, so this is what keeps a signature
// drift from surviving until production wiring.
var _ realtime.EventSource = realtimeEventSource{}

// defaultSnapshotEvents is how many recent events a snapshot carries when the
// caller passes nothing usable, and it exists for the same reason NewHub,
// NewTailer and NewStreamer each default theirs: a zero here does not fail, it
// SUCCEEDS QUIETLY WITH NOTHING. It reaches the queries as LIMIT 0, so every
// stream opens on an empty backlog and the client is told the run has no
// history — no error, no log line, nothing to notice.
//
// config.Load rejects a non-positive snapshot_events, so a configured process
// cannot get here. A hand-built config.Config can, and does: runServe is
// called with one in the boot tests, which is exactly the caller that would
// never notice.
//
// This IS a second copy of config's default, and its tie-back is
// TestNewRealtimeEventSource_ZeroTakesTheConfiguredDefault, which asserts
// through this CONSTRUCTOR: it fails both when either end of the duplicated 500
// moves alone and when the defaulting is dropped. The five copies in
// internal/realtime have a tie-back of their own, that package's defaults_test,
// which compares literals and says there why a test is the strongest tie
// available to it.
const defaultSnapshotEvents = 500

func newRealtimeEventSource(pool *pgxpool.Pool, snapshotEvents int) realtimeEventSource {
	if snapshotEvents <= 0 {
		snapshotEvents = defaultSnapshotEvents
	}
	return realtimeEventSource{pool: pool, snapshotEvents: rowLimit(snapshotEvents)}
}

// Snapshot returns the caller's backlog AND the watermark it was computed
// at, from ONE repeatable-read transaction. This is where gaplessness is
// won or lost, and two decisions carry it.
//
// FIRST, the read ORDER: watermark, then events. Both orders are safe under
// repeatable read, so the order is chosen for how each DEGRADES. Watermark
// first can only ever let the events read see something the watermark does
// not cover — a duplicate the subscriber dedups on id. Events first would
// let the watermark cover an event the snapshot never returned, which is a
// GAP, and a gap is the failure this whole design exists to prevent. The
// first statement is also what takes Postgres's transaction snapshot, so
// reading the watermark first is what pins the view the events read gets.
//
// SECOND, what is NOT here: there is deliberately no `id <= watermark`
// predicate on the events query. The transaction's snapshot already implies
// it, and writing it out would make the isolation level irrelevant — the
// result would look consistent under read committed too, and the invariant
// that every returned event is at or below the watermark would stop being
// falsifiable. That invariant is the acceptance evidence for this task.
//
// The transaction is read-only, so it is rolled back rather than committed:
// there is nothing to commit, and rollback releases the snapshot just as
// promptly.
func (s realtimeEventSource) Snapshot(ctx context.Context, f realtime.Filter) (realtime.Snapshot, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return realtime.Snapshot{}, fmt.Errorf("app: realtime snapshot: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := store.New(tx)
	head, err := q.MaxRunEventID(ctx)
	if err != nil {
		return realtime.Snapshot{}, fmt.Errorf("app: realtime snapshot: watermark: %w", err)
	}
	events, err := s.recent(ctx, q, f)
	if err != nil {
		return realtime.Snapshot{}, err
	}
	return realtime.Snapshot{Events: events, Watermark: realtime.Watermark{EventID: head}}, nil
}

// recent reads the most recent matching events, oldest first.
//
// The queries order DESCENDING because "the most recent N" is a LIMIT on a
// backwards index scan, and the slice is reversed here. That reversal is the
// one Go-side reordering in this file that is safe: it happens over the
// exact rows the LIMIT already chose, so it can neither hide a row nor
// invent one. Contrast the tailer's sort, which cannot repair a source that
// truncated the wrong order — which is why every ASCENDING query below
// orders in SQL.
func (s realtimeEventSource) recent(ctx context.Context, q *store.Queries, f realtime.Filter) ([]realtime.Event, error) {
	all, projects := scopeArgs(f.Scope)
	var (
		rows []store.RunEvent
		err  error
	)
	switch {
	case f.RunID != nil:
		rows, err = q.ListRecentRunEvents(ctx, store.ListRecentRunEventsParams{
			RunID: *f.RunID, AllProjects: all, ProjectIds: projects, Lim: s.snapshotEvents,
		})
	case f.Attention:
		rows, err = q.ListRecentAttentionEvents(ctx, store.ListRecentAttentionEventsParams{
			KindPatterns: realtime.AttentionKindPatterns(),
			AllProjects:  all, ProjectIds: projects, Lim: s.snapshotEvents,
		})
	default:
		// A filter with neither selector matches nothing at all, whatever its
		// scope (realtime.Filter.Matches). Answering with an empty backlog
		// rather than an error keeps that the HTTP edge's refusal to make.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("app: realtime snapshot: events: %w", err)
	}
	slices.Reverse(rows)
	return realtimeEvents(rows)
}

// Replay is the catch-up path for both the initial splice and a
// Last-Event-ID resume: matching events with id > after, oldest first.
//
// It runs OUTSIDE a transaction on purpose. A page is a cursor query, and
// the caller pages by the last id returned; a repeatable-read view would
// only pin it to a moment that is already past.
func (s realtimeEventSource) Replay(ctx context.Context, f realtime.Filter, after int64, limit int) ([]realtime.Event, error) {
	all, projects := scopeArgs(f.Scope)
	q := store.New(s.pool)
	var (
		rows []store.RunEvent
		err  error
	)
	switch {
	case f.RunID != nil:
		rows, err = q.ListRunEventsForRunAfter(ctx, store.ListRunEventsForRunAfterParams{
			RunID: *f.RunID, After: after, AllProjects: all, ProjectIds: projects, Lim: rowLimit(limit),
		})
	case f.Attention:
		rows, err = q.ListAttentionEventsAfter(ctx, store.ListAttentionEventsAfterParams{
			After:        after,
			KindPatterns: realtime.AttentionKindPatterns(),
			AllProjects:  all, ProjectIds: projects, Lim: rowLimit(limit),
		})
	default:
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("app: realtime replay after %d: %w", after, err)
	}
	return realtimeEvents(rows)
}

// Since is the tailer's drain: everything past the cursor, filter-blind,
// ORDER BY id LIMIT limit. The ordering is the contract — the tailer moves
// its cursor to the highest id it was handed, so a source that skipped lower
// ids and truncated at limit would strand them permanently, and no amount of
// sorting downstream recovers them.
func (s realtimeEventSource) Since(ctx context.Context, after int64, limit int) ([]realtime.Event, error) {
	rows, err := store.New(s.pool).ListRunEventsSince(ctx, store.ListRunEventsSinceParams{
		After: after, Lim: rowLimit(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("app: realtime since %d: %w", after, err)
	}
	return realtimeEvents(rows)
}

// Oldest is the lowest retained id: below it, a Last-Event-ID can no longer
// be resumed from and the subscriber must re-snapshot.
func (s realtimeEventSource) Oldest(ctx context.Context) (int64, error) {
	id, err := store.New(s.pool).MinRunEventID(ctx)
	if err != nil {
		return 0, fmt.Errorf("app: realtime oldest event: %w", err)
	}
	return id, nil
}

// Head is the current maximum id. It seeds the tailer's startup cursor, and
// a failure here is fatal to startup by design: there is no safe id to
// guess, and guessing 0 would replay the whole table into every stream.
func (s realtimeEventSource) Head(ctx context.Context) (int64, error) {
	id, err := store.New(s.pool).MaxRunEventID(ctx)
	if err != nil {
		return 0, fmt.Errorf("app: realtime head event: %w", err)
	}
	return id, nil
}

// scopeArgs is realtime.Scope in the two columns the queries take. All is a
// separate boolean because "every project" is not a project list: an admin's
// footprint is not enumerable, and an empty list is the default-deny case
// rather than a wildcard.
func scopeArgs(s realtime.Scope) (bool, []uuid.UUID) {
	if s.All {
		return true, nil
	}
	// pgx encodes a nil slice as NULL, and `project_id = ANY(NULL)` is NULL
	// rather than false — which the WHERE clause would drop anyway, but an
	// empty array says default-deny in the plan as well as in the result.
	if s.Projects == nil {
		return false, []uuid.UUID{}
	}
	return false, s.Projects
}

// rowLimit narrows a caller's row count onto the int32 the generated
// queries take. Both ends are clamped rather than converted: a limit past
// int32 would wrap into a NEGATIVE LIMIT, which Postgres rejects outright,
// turning a caller's arithmetic slip into a failed stream rather than a
// large page.
func rowLimit(n int) int32 {
	switch {
	case n < 0:
		return 0
	case n > math.MaxInt32:
		return math.MaxInt32
	}
	return int32(n)
}

func realtimeEvents(rows []store.RunEvent) ([]realtime.Event, error) {
	out := make([]realtime.Event, 0, len(rows))
	for _, r := range rows {
		e, err := realtimeEvent(r)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// realtimeEvent decodes one row. The payload is decoded FRESH per row and
// never cached: the hub fans one Event out to every matching subscriber
// without copying the map, so two subscribers holding the same map would be
// one write away from corrupting each other's view.
func realtimeEvent(r store.RunEvent) (realtime.Event, error) {
	var payload map[string]any
	if len(r.Payload) > 0 {
		if err := json.Unmarshal(r.Payload, &payload); err != nil {
			return realtime.Event{}, fmt.Errorf("app: realtime: decode payload of event %d: %w", r.ID, err)
		}
	}
	return realtime.Event{
		ID:            r.ID,
		RunID:         r.RunID,
		ProjectID:     r.ProjectID,
		Kind:          r.Kind,
		Payload:       payload,
		OccurredAt:    r.OccurredAt,
		CorrelationID: r.CorrelationID,
	}, nil
}

// realtimeGrants implements realtime.GrantResolver over identity. It is
// re-consulted on an interval for the lifetime of every subscription, so a
// revoked grant closes the stream without coupling realtime to identity.
type realtimeGrants struct{ svc *identity.Service }

var _ realtime.GrantResolver = realtimeGrants{}

// ScopeFor answers what a principal may see RIGHT NOW, on the same branch
// the approvals inbox takes: an admin sees every project, a member sees
// exactly their grants. Sharing the branch is the point — "what a stream
// shows" and "what the approvals list shows" cannot be allowed to diverge.
//
// Two things differ, and both follow from this being a repeated question
// about a live stream rather than a one-shot answer to a request:
//
//   - the role is read from the database, not from the session that opened
//     the stream, so a demotion reaches a subscription that is already open;
//   - a deactivated principal gets the empty scope, which matches nothing.
//     Grant rows survive deactivation, so grants alone would keep a disabled
//     admin streaming forever. Nothing diverges from the inbox here: a
//     deactivated user cannot authenticate a request at all.
func (g realtimeGrants) ScopeFor(ctx context.Context, userID uuid.UUID) (realtime.Scope, error) {
	u, err := g.svc.GetUser(ctx, userID)
	if err != nil {
		return realtime.Scope{}, err
	}
	if !u.Active {
		return realtime.Scope{}, nil
	}
	if u.Role == identity.RoleAdmin {
		return realtime.Scope{All: true}, nil
	}
	grants, err := g.svc.GrantsForUser(ctx, userID)
	if err != nil {
		return realtime.Scope{}, err
	}
	projects := make([]uuid.UUID, 0, len(grants))
	for _, gr := range grants {
		projects = append(projects, gr.ProjectID)
	}
	return realtime.Scope{Projects: projects}, nil
}

// realtimeRuntime is the process's realtime subsystem (spec §4): ONE tailer
// over run_events, ONE hub it publishes into, ONE Streamer both HTTP surfaces
// serve from, and the dedicated connection the tailer is woken on. They are
// one value because they have one lifetime and one shutdown order.
type realtimeRuntime struct {
	streamer *realtime.Streamer
	hub      *realtime.Hub
	// listener is the waker's dedicated connection, and the only thing here
	// the tailer is still holding when shutdown starts.
	listener io.Closer
	// stop cancels the tailer's context. It is the tailer's OWN cancel and
	// not the process's, so Stop means the same thing on the paths where
	// runServe returns without the process context ever being cancelled.
	stop context.CancelFunc
	done <-chan struct{}
	// failed carries the one error Tailer.Run can return that is not
	// shutdown. See runTailer.
	failed <-chan error
	log    *slog.Logger
	once   sync.Once
}

// startRealtime builds the subsystem and starts its tailer.
//
// grants is passed in rather than built here because serve.go hands the SAME
// value to the HTTP surface: one resolver answers both the scope a stream
// opens with and the re-check that runs for its lifetime.
func startRealtime(ctx context.Context, pool *pgxpool.Pool, dbURL string, cfg config.Realtime,
	grants realtime.GrantResolver, log *slog.Logger,
) *realtimeRuntime {
	hub := realtime.NewHub(realtime.HubConfig{SubscriberQueue: cfg.SubscriberQueue})
	src := newRealtimeEventSource(pool, cfg.SnapshotEvents)
	streamer := realtime.NewStreamer(hub, src, grants, realtime.StreamerConfig{
		HeartbeatInterval: cfg.HeartbeatInterval.Std(),
		ReplayBatch:       cfg.ReplayBatch,
		GrantRecheck:      cfg.GrantRecheck.Std(),
	})

	waker := newPGWaker(dbURL, log)
	tailerCtx, stop := context.WithCancel(ctx)
	// TailerConfig.Batch is left at its default DELIBERATELY. It is how many
	// rows one read of the table takes — the tailer's own I/O, shared by every
	// stream in the process — while ReplayBatch is one client's catch-up page.
	// They both default to 500, which is exactly what makes feeding one into
	// the other look right, test green, and quietly tie two unrelated knobs to
	// one number. There is no config.Realtime knob for it because nobody has
	// needed to tune it.
	tailer := realtime.NewTailer(src, waker, hub.Publish, realtime.TailerConfig{
		PollFallback: cfg.PollFallback.Std(),
	}, log)
	done, failed := runTailer(tailerCtx, tailer)

	return &realtimeRuntime{
		streamer: streamer, hub: hub, listener: waker,
		stop: stop, done: done, failed: failed, log: log,
	}
}

// runLoop is the Run(ctx) shape this process's long-lived goroutines share.
// It is an interface so the failure handling below can be driven without a
// database.
type runLoop interface {
	Run(ctx context.Context) error
}

// runTailer starts the process's single tailer and reports a FAILED STARTUP.
//
// It deviates from the `go func() { defer close(done); _ = x.Run(ctx) }()`
// shape serve.go uses for the runner and the orchestrator, and the tailer is
// genuinely different. Those two log their own faults and keep looping, so
// their Run returns nothing a caller could act on. Tailer.Run has exactly one
// exit that is not context cancellation — a cursor it could not seed from
// EventSource.Head — and its own godoc calls that a failed startup: there is
// no safe id to guess, so it does not guess. Discarded, that produces a
// process which serves streams that get a snapshot and then silence, forever,
// with nothing anywhere to say why.
//
// runServe treats it as fatal. The seed happens moments after Migrate and
// pool.Ping both succeeded, so it is not a transient blip on a healthy
// database; and realtime is a per-process singleton, so a process without it
// cannot serve spec §4 at all. Exiting hands the supervisor a restart, which
// is the only recovery that exists.
//
// A cancelled context is shutdown, not a fault, and reports nothing.
func runTailer(ctx context.Context, t runLoop) (<-chan struct{}, <-chan error) {
	done := make(chan struct{})
	// Buffered: nothing may be waiting on it yet, and the send must not
	// outlive the goroutine that reports the failure.
	failed := make(chan error, 1)
	go func() {
		defer close(done)
		if err := t.Run(ctx); err != nil && ctx.Err() == nil {
			failed <- fmt.Errorf("serve: realtime tailer: %w", err)
		}
	}()
	return done, failed
}

// Stop ends the subsystem, and the ORDER is the whole reason it is a method.
//
//  1. Cancel the tailer's context — which only ASKS it to stop.
//  2. WAIT for Run to return. Until it does it is still reading on the
//     connection step 3 closes, and without the wait the goroutine outlives
//     runServe holding a dedicated database connection that nothing then
//     closes — pool.Close does not reach it, because it is not the pool's.
//  3. Close the dedicated LISTEN connection.
//  4. Close the hub, which ends every live subscription and so ends the
//     handlers parked on them.
//
// Step 4 must happen before srv.Shutdown, which is runServe's business rather
// than this method's: Shutdown waits for active connections and an SSE
// connection does not drain on its own.
//
// Step 2 is the ONE UNBOUNDED wait in that path — Close below carries its own
// deadline, srv.Shutdown carries ShutdownTimeout — and a deadline on it is
// left out because it would not bound anything. What it is NOT is a race:
// every use of the waker's connection is under w.mu and Close takes that
// mutex, so abandoning the wait cannot reach a connection mid-read. What it
// does is MOVE THE HANG, and both hangs are worse than this one. A tailer
// stuck inside Wait is holding w.mu, so step 3 blocks there instead. A tailer
// stuck in a drain is holding a pooled connection, so runServe's deferred
// pool.Close blocks instead — pgxpool.Close blocks until every connection is
// returned. Either way the deadline buys a shutdown path that READS bounded
// and is not, one frame further from the cause. What does bound this is that
// every blocking call in the tailer's loop takes the context step 1 has just
// cancelled.
//
// What the wait is NOT for: a Publish racing Hub.Close is already safe
// whichever order they land in. Publish collects its targets under the hub's
// read lock and releases it before delivering; offer re-checks the closed
// flag under the subscription's own mutex, which is the same lock close takes,
// so a send can never race the close of that channel; and neither lock is
// ever held while the other is taken, so the two cannot deadlock either.
//
// Idempotent, because runServe defers it and also calls it on the ordinary
// shutdown path — an early return must not leave the tailer running.
func (rt *realtimeRuntime) Stop() {
	rt.once.Do(func() {
		rt.stop()
		<-rt.done
		if err := rt.listener.Close(); err != nil {
			rt.log.Warn("realtime.waker.close", "error", err)
		}
		rt.hub.Close()
	})
}

// pgWaker is the tailer's wake-up side: a connection blocked in
// WaitForNotification on runs.EventsChannel.
//
// The connection is DEDICATED and never pooled. A pooled connection can be
// handed to another caller between the LISTEN and the wait, which silently
// unsubscribes the listener — the tailer then wakes only on its poll
// deadline, and the symptom is latency nothing measures rather than an error
// anyone sees.
type pgWaker struct {
	url string
	log *slog.Logger

	// mu is held for the whole of Wait, including the blocking read. That
	// makes Close safe from any goroutine at any time at the cost of Close
	// waiting out an in-flight Wait — which never happens in this wiring,
	// because realtimeRuntime.Stop waits for the tailer before it closes.
	mu     sync.Mutex
	conn   *pgx.Conn
	closed bool
}

func newPGWaker(url string, log *slog.Logger) *pgWaker {
	return &pgWaker{url: url, log: log}
}

var errWakerClosed = errors.New("app: realtime waker is closed")

// Wait blocks until a notification arrives, ctx is done, or the connection
// fails. Its contract (realtime.Waker) is that a nil error means "maybe
// something happened" and never "definitely" — the tailer drains
// unconditionally either way — which is what makes a reconnect legal here.
//
// A FAILURE MUST COME BACK AS AN ERROR, never as nil. The tailer's wait
// serves out the rest of its poll interval after an error and returns
// immediately on nil, so a waker that answered a dead database with nil would
// spin the follow loop at full speed.
//
// That is also where the reconnect's rate limit comes from, and why there is
// no backoff of its own here: one failed attempt per poll interval, imposed
// by the caller. Until an attempt succeeds the poll fallback is what carries
// correctness, which is why the fallback is not optional.
func (w *pgWaker) Wait(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errWakerClosed
	}
	conn, err := w.connect(ctx)
	if err != nil {
		return err
	}
	if _, err := conn.WaitForNotification(ctx); err != nil {
		// The tailer's poll deadline, not a fault, and the connection survives
		// it: pgx interrupts the read by putting a deadline on the socket and
		// clears it again on the way out, and a read that ended in a timeout
		// is the one case it does not close the connection over (pgconn's
		// DeadlineContextWatcherHandler and peekMessage). The LISTEN therefore
		// stands — which matters, because otherwise every poll interval would
		// cost a reconnect. TestPGWaker_IsWokenByAnOrdinaryAppend holds the
		// waker across TWO deadlines and asserts the same BACKEND PID
		// throughout — the wake-up alone would not show it, because a waker
		// that redialled would re-issue LISTEN and be woken just the same.
		//
		// Only a read that failed for another reason has broken it.
		if ctx.Err() != nil {
			return err
		}
		w.drop(ctx, conn)
		return fmt.Errorf("app: realtime waker: %w", err)
	}
	return nil
}

// connect returns the live LISTEN connection, dialling and subscribing if
// there is not one. Called with mu held.
func (w *pgWaker) connect(ctx context.Context) (*pgx.Conn, error) {
	if w.conn != nil && !w.conn.IsClosed() {
		return w.conn, nil
	}
	conn, err := pgx.Connect(ctx, w.url)
	if err != nil {
		return nil, fmt.Errorf("app: realtime waker: connect: %w", err)
	}
	// The channel name comes from runs and is never spelled here: the two
	// halves disagreeing is completely silent (runs.EventsChannel).
	if _, err := conn.Exec(ctx, "LISTEN "+runs.EventsChannel); err != nil {
		_ = conn.Close(ctx)
		return nil, fmt.Errorf("app: realtime waker: listen: %w", err)
	}
	w.conn = conn
	w.log.Info("realtime.waker.listening", "channel", runs.EventsChannel)
	return conn, nil
}

// drop discards a broken connection so the next Wait dials a new one and
// re-issues LISTEN. Called with mu held.
func (w *pgWaker) drop(ctx context.Context, conn *pgx.Conn) {
	w.conn = nil
	// Best effort on a connection already known to be broken; ctx may well be
	// the one that just failed.
	_ = conn.Close(ctx)
}

// Close ends the waker and its connection. realtimeRuntime.Stop calls it, and
// only after the tailer has stopped using it.
func (w *pgWaker) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	// The flag, and not just the nil, is what makes Close final: a Wait that
	// arrived afterwards would otherwise find w.conn nil and dial a REPLACEMENT
	// nothing is left to close. No caller does that in this wiring — Stop waits
	// for the tailer first — and the leak is cheap to make unrepresentable.
	w.closed = true
	conn := w.conn
	w.conn = nil
	if conn == nil {
		return nil
	}
	// A context of its own, and a short one. Stop runs after the process
	// context is already cancelled, and pgx watches whatever context it is
	// handed: on a cancelled one the socket deadline is already past, so the
	// polite Terminate never makes it out. The socket is closed either way —
	// this only buys the server being told.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return conn.Close(ctx)
}
