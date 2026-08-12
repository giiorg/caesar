package realtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// streamSource answers the three ports the splice uses. fakeSource
// (tailer_test) deliberately refuses Snapshot/Replay/Oldest and stays that
// way, so this is a second fake rather than a taught one — and it refuses the
// two ports the splice never touches, for the same reason.
type streamSource struct {
	mu sync.Mutex

	snapshot Snapshot
	oldest   int64
	// retained is what Replay pages over, ascending by id. Replay applies the
	// filter here rather than after the limit, because the real query's
	// predicates are all in SQL (Task 7) — a fake that filtered afterwards
	// would hand the pager short pages no real source can produce.
	retained []Event
	// brokenPage, when non-nil, is what Replay answers to EVERY call
	// regardless of after: a source that cannot advance. Only the spin-guard
	// test sets it.
	brokenPage []Event

	snapshotErr error
	replayErr   error
	oldestErr   error

	// onSnapshot and onReplay run INSIDE the call, before it returns. The
	// snapshot window is the only one the subscribe-ordering mutation moves
	// across, so it is where a test publishes to prove the subscription
	// already existed; onReplay is the same window for a resume, which never
	// calls Snapshot at all.
	onSnapshot func()
	onReplay   func()

	snapshotCalls int
	replayCalls   []replayCall
}

// replayCall is one page request. The limit is recorded because "always pass
// the configured batch" is a contract: Replay answers a non-positive limit
// with an empty page, which a pager reads as "caught up".
type replayCall struct {
	After int64
	Limit int
}

var _ EventSource = (*streamSource)(nil)

// errAbortedRead is what Replay reports for a cancelled read, and it
// deliberately does NOT wrap context.Canceled: a real driver is free to
// translate a cancelled query into its own error class, so a Serve that asked
// errors.Is(err, context.Canceled) instead of asking the context would miss
// it. Snapshot wraps ctx.Err() instead, so both shapes are exercised.
var errAbortedRead = errors.New("streamSource: read aborted")

func (s *streamSource) Snapshot(ctx context.Context, _ Filter) (Snapshot, error) {
	s.mu.Lock()
	s.snapshotCalls++
	hook, snap, err := s.onSnapshot, s.snapshot, s.snapshotErr
	s.mu.Unlock()
	// Outside the lock: a hook is free to touch the source.
	if hook != nil {
		hook()
	}
	// A real query notices a cancelled context and abandons the read. A fake
	// that ignored ctx would make a whole class of defect — what Serve returns
	// when the client leaves mid-catch-up — structurally untestable.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Snapshot{}, fmt.Errorf("streamSource.Snapshot: %w", ctxErr)
	}
	if err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}

func (s *streamSource) Replay(ctx context.Context, f Filter, after int64, limit int) ([]Event, error) {
	s.mu.Lock()
	s.replayCalls = append(s.replayCalls, replayCall{After: after, Limit: limit})
	hook, err := s.onReplay, s.replayErr
	var out []Event
	switch {
	case err != nil:
	case s.brokenPage != nil:
		out = append(out, s.brokenPage...)
	case limit <= 0:
		// ports.go: a non-positive limit yields an empty page. Mimicking it
		// is what makes a caller that forgot the configured batch show up as
		// a stream that never catches up rather than as a passing test.
	default:
		for _, e := range s.retained {
			if e.ID <= after || !f.Matches(e) {
				continue
			}
			out = append(out, e)
			if len(out) == limit {
				break
			}
		}
	}
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	if ctx.Err() != nil {
		return nil, errAbortedRead
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *streamSource) Oldest(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.oldest, s.oldestErr
}

// The splice follows the hub, never the table: these two are the tailer's and
// a fake that answered them would invite a test to assert against invented
// behaviour.
func (s *streamSource) Since(context.Context, int64, int) ([]Event, error) {
	return nil, xerrors.NotImplemented("streamSource.Since")
}

func (s *streamSource) Head(context.Context) (int64, error) {
	return 0, xerrors.NotImplemented("streamSource.Head")
}

func (s *streamSource) calls() (snapshots int, replays []replayCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotCalls, slices.Clone(s.replayCalls)
}

// grantStep is one scripted answer from stubGrants: a scope, or the failure
// that stood in for one. A step with a non-nil err yields no scope at all,
// which is what a resolver that could not reach the database does.
type grantStep struct {
	scope Scope
	err   error
}

// stubGrants is the GrantResolver Serve re-consults on the re-check ticker.
//
// steps are consumed one per call and the LAST one repeats forever, so a test
// scripts the transition it cares about and never has to count the ticks that
// land afterwards. No script at all is the permissive resolver idleStreamer
// wants: every id-asserting test in this file runs through it and must not
// depend on grants.
//
// It is called from Serve's goroutine while the test goroutine reads
// callCount, so every field is behind the mutex — and it honours its context,
// because Task 8's finding was rooted in a fake that ignored one, which made a
// whole class of defect structurally invisible rather than merely uncovered.
type stubGrants struct {
	mu    sync.Mutex
	steps []grantStep
	calls int
}

var _ GrantResolver = (*stubGrants)(nil)

func (g *stubGrants) ScopeFor(ctx context.Context, _ uuid.UUID) (Scope, error) {
	g.mu.Lock()
	g.calls++
	step := grantStep{scope: Scope{All: true}}
	if len(g.steps) > 0 {
		step = g.steps[min(g.calls, len(g.steps))-1]
	}
	g.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Scope{}, fmt.Errorf("stubGrants.ScopeFor: %w", err)
	}
	if step.err != nil {
		return Scope{}, step.err
	}
	return step.scope, nil
}

// callCount is how many times the stream has re-resolved so far. It is named
// away from streamSource.calls only because both fakes live in one package.
func (g *stubGrants) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// streamRecorder is a concurrency-safe http.ResponseWriter + http.Flusher.
// Serve runs in a goroutine in every test here while the test reads what it
// has written; sseRecorder (sse_test) appends to an unguarded slice over an
// unguarded bytes.Buffer, so reusing it would be a -race failure rather than
// a flake.
type streamRecorder struct {
	mu     sync.Mutex
	header http.Header
	body   bytes.Buffer
	// ops is the interleaving of body writes and flushes, the same op-log
	// sseRecorder keeps. It is what makes "Serve adds no Flush of its own"
	// observable at all — a Flush() that recorded nothing leaves finding 9
	// enforced by a comment.
	ops      []string
	writeErr error
}

func newStreamRecorder() *streamRecorder {
	return &streamRecorder{header: http.Header{}}
}

// Header hands back the map itself and is touched only by NewWriter, on the
// Serve goroutine. No test here reads a header; one that did would need its
// own guard.
func (r *streamRecorder) Header() http.Header { return r.header }

func (r *streamRecorder) WriteHeader(int) {}

func (r *streamRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writeErr != nil {
		return 0, r.writeErr
	}
	r.ops = append(r.ops, "write")
	return r.body.Write(p)
}

func (r *streamRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops = append(r.ops, "flush")
}

// failWrites makes every subsequent body write fail, as a client that has
// gone away does.
func (r *streamRecorder) failWrites(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writeErr = err
}

// written is every byte the stream has produced so far, safe to read while
// Serve is still running.
func (r *streamRecorder) written() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

// opLog is the write/flush interleaving so far.
func (r *streamRecorder) opLog() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.ops)
}

// ids is every id: line in the order it was written — what the client
// actually received, which is the whole subject of this file.
func (r *streamRecorder) ids(t *testing.T) []int64 {
	t.Helper()
	var out []int64
	for _, line := range strings.Split(r.written(), "\n") {
		rest, ok := strings.CutPrefix(line, "id: ")
		if !ok {
			continue
		}
		id, err := strconv.ParseInt(rest, 10, 64)
		if err != nil {
			t.Fatalf("unparseable id line %q: %v", line, err)
		}
		out = append(out, id)
	}
	return out
}

// runFilter is a per-run subscription scoped to one project — the shape both
// HTTP surfaces build.
func runFilter(run, project uuid.UUID) Filter {
	return Filter{RunID: &run, Scope: Scope{Projects: []uuid.UUID{project}}}
}

// idleStreamer is the configuration for tests that assert on ids: a heartbeat
// AND a grant re-check far beyond the test's lifetime, so neither a comment nor
// a scope change ever interleaves with the frames, and a batch each test sets
// deliberately. The resolver is permissive and, at an hour, never consulted.
func idleStreamer(hub *Hub, src EventSource, batch int) *Streamer {
	return NewStreamer(hub, src, &stubGrants{}, StreamerConfig{
		HeartbeatInterval: time.Hour,
		GrantRecheck:      time.Hour,
		ReplayBatch:       batch,
	})
}

// startServe runs Serve in the background, as the HTTP server does, and hands
// back the recorder, a cancel and the channel Serve's error lands on. Cleanup
// cancels and waits, so every test here also asserts its stream shuts down.
func startServe(t *testing.T, s *Streamer, req StreamRequest) (*streamRecorder, context.CancelFunc, <-chan error) {
	t.Helper()
	rec := newStreamRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	return rec, cancel, startServeOn(ctx, t, s, rec, req)
}

// startServeOn is startServe with the context and the recorder supplied by the
// test: a recorder that will fail its writes has to be armed BEFORE Serve
// runs, and a test that cancels from inside a source hook needs to hold the
// cancel before Serve starts. Cancelling the ctx passed here cancels the one
// Serve sees.
func startServeOn(parent context.Context, t *testing.T, s *Streamer, rec *streamRecorder, req StreamRequest) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	errc := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		errc <- s.Serve(ctx, rec, req)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(patience):
			t.Errorf("Serve did not return within %s of cancellation", patience)
		}
	})
	return errc
}

// waitServe is what Serve returned. Reading it is also the happens-before
// edge that makes the recorder and the fake safe to inspect unlocked.
func waitServe(t *testing.T, errc <-chan error) error {
	t.Helper()
	select {
	case err := <-errc:
		return err
	case <-time.After(patience):
		t.Fatalf("Serve did not return within %s", patience)
		return nil
	}
}

// The ordering guard, and the reason this task exists. Subscribing after the
// snapshot — the tidier-looking order — silently drops every event that
// commits while the snapshot is being read.
func TestServe_SubscribesBeforeSnapshotting(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	hub := NewHub(HubConfig{SubscriberQueue: 8})
	src := &streamSource{}
	var subsAtSnapshot int
	src.onSnapshot = func() {
		subsAtSnapshot = registryLen(hub)
		// End the stream from here: closing the hub closes the subscription,
		// so the drain returns as soon as it has written what was queued and
		// nothing in this file sleeps waiting for it.
		hub.Close()
	}

	_, _, errc := startServe(t, idleStreamer(hub, src, 10), StreamRequest{Filter: runFilter(run, proj)})

	if err := waitServe(t, errc); !errors.Is(err, CloseShutdown) {
		t.Fatalf("Serve() = %v, want %v", err, CloseShutdown)
	}
	if subsAtSnapshot != 1 {
		t.Errorf("registry held %d subscription(s) while Snapshot ran, want 1: "+
			"the table was read before the client was subscribed, so every event committing during the read is lost", subsAtSnapshot)
	}
}

// The overlap subscribing-first creates, and what makes it harmless. Both
// events commit DURING the snapshot — the one window the ordering mutation
// moves across.
//
// dup is also on the replay path, so it is the dedup case: it must appear
// exactly once. liveOnly can only ever arrive live, so it is the ordering
// case: under the mutation it is published to a hub with no subscribers and
// never arrives at all, while dup still comes back from Replay and proves
// nothing on its own.
func TestServe_DeduplicatesTheOverlap(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	dup := hubEvent(2, proj, run, "run.started")
	liveOnly := hubEvent(3, proj, run, "run.started")

	hub := NewHub(HubConfig{SubscriberQueue: 8})
	src := &streamSource{
		snapshot: Snapshot{
			Events:    []Event{hubEvent(1, proj, run, "run.started")},
			Watermark: Watermark{EventID: 1},
		},
		// Replay knows dup and NOT liveOnly.
		retained: []Event{dup},
	}
	src.onSnapshot = func() {
		hub.Publish([]Event{dup, liveOnly})
		hub.Close()
	}

	rec, _, errc := startServe(t, idleStreamer(hub, src, 10), StreamRequest{Filter: runFilter(run, proj)})

	if err := waitServe(t, errc); !errors.Is(err, CloseShutdown) {
		t.Fatalf("Serve() = %v, want %v", err, CloseShutdown)
	}
	if diff := cmp.Diff([]int64{1, 2, 3}, rec.ids(t)); diff != "" {
		t.Errorf("ids on the wire (-want +got):\n%s", diff)
	}
}

// Catch-up is paged, and every page is asked for the configured batch: the
// recorded cursors are what prove the pager advanced by the highest id it
// wrote rather than re-reading or skipping a page.
func TestServe_ReplaysPastTheWatermark(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	hub := NewHub(HubConfig{SubscriberQueue: 8})
	retained := make([]Event, 0, 7)
	for id := int64(1); id <= 7; id++ {
		retained = append(retained, hubEvent(id, proj, run, "run.started"))
	}
	src := &streamSource{
		snapshot: Snapshot{Events: retained[:2], Watermark: Watermark{EventID: 2}},
		retained: retained,
	}
	src.onSnapshot = func() { hub.Close() }

	rec, _, errc := startServe(t, idleStreamer(hub, src, 2), StreamRequest{Filter: runFilter(run, proj)})

	if err := waitServe(t, errc); !errors.Is(err, CloseShutdown) {
		t.Fatalf("Serve() = %v, want %v", err, CloseShutdown)
	}
	if diff := cmp.Diff([]int64{1, 2, 3, 4, 5, 6, 7}, rec.ids(t)); diff != "" {
		t.Errorf("ids on the wire (-want +got):\n%s", diff)
	}
	_, replays := src.calls()
	// Three pages: [3 4], [5 6], [7]. The short page is the caught-up signal.
	want := []replayCall{{After: 2, Limit: 2}, {After: 4, Limit: 2}, {After: 6, Limit: 2}}
	if diff := cmp.Diff(want, replays); diff != "" {
		t.Errorf("replay pages (-want +got):\n%s", diff)
	}
}

// A resume writes no snapshot at all: the client already has that state, and
// re-sending it would duplicate the whole backlog into a reconnecting tab.
func TestServe_ResumesFromLastEventID(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	hub := NewHub(HubConfig{SubscriberQueue: 8})
	retained := make([]Event, 0, 7)
	for id := int64(1); id <= 7; id++ {
		retained = append(retained, hubEvent(id, proj, run, "run.started"))
	}
	src := &streamSource{
		// A snapshot the test would see if Serve wrongly took that path.
		snapshot: Snapshot{Events: retained, Watermark: Watermark{EventID: 7}},
		retained: retained,
		oldest:   1,
	}
	src.onReplay = sync.OnceFunc(func() { hub.Close() })

	req := StreamRequest{Filter: runFilter(run, proj), LastEventID: 5, HasLastID: true}
	rec, _, errc := startServe(t, idleStreamer(hub, src, 10), req)

	if err := waitServe(t, errc); !errors.Is(err, CloseShutdown) {
		t.Fatalf("Serve() = %v, want %v", err, CloseShutdown)
	}
	if diff := cmp.Diff([]int64{6, 7}, rec.ids(t)); diff != "" {
		t.Errorf("ids on the wire (-want +got):\n%s", diff)
	}
	snapshots, replays := src.calls()
	if snapshots != 0 {
		t.Errorf("Snapshot called %d time(s) on a resume, want 0", snapshots)
	}
	if diff := cmp.Diff([]replayCall{{After: 5, Limit: 10}}, replays); diff != "" {
		t.Errorf("replay pages (-want +got):\n%s", diff)
	}
}

// A Last-Event-ID below the retention floor cannot be resumed from: the
// events between it and the floor are gone, and streaming from the floor
// would be a silent partial stream. The comparison is deliberately
// conservative by one — LastEventID == oldest-1 is losslessly resumable and
// re-snapshots anyway, erring toward a duplicate rather than a gap.
func TestServe_ResnapshotsWhenLastIDTooOld(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		lastID       int64
		wantSnapshot bool
		wantIDs      []int64
	}{
		{name: "far below the floor", lastID: 3, wantSnapshot: true, wantIDs: []int64{20, 21, 22}},
		{name: "one below the floor, resumable but re-snapshotted", lastID: 9, wantSnapshot: true, wantIDs: []int64{20, 21, 22}},
		{name: "exactly the floor", lastID: 10, wantSnapshot: false, wantIDs: []int64{22}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			proj, run := uuid.New(), uuid.New()
			hub := NewHub(HubConfig{SubscriberQueue: 8})
			// The snapshot carries ids Replay does not, so the two paths are
			// distinguishable by what reached the wire and not only by which
			// port was called.
			src := &streamSource{
				snapshot: Snapshot{
					Events: []Event{
						hubEvent(20, proj, run, "run.started"),
						hubEvent(21, proj, run, "run.started"),
					},
					Watermark: Watermark{EventID: 21},
				},
				retained: []Event{hubEvent(22, proj, run, "run.started")},
				oldest:   10,
			}
			closeOnce := sync.OnceFunc(func() { hub.Close() })
			src.onSnapshot, src.onReplay = closeOnce, closeOnce

			req := StreamRequest{Filter: runFilter(run, proj), LastEventID: tt.lastID, HasLastID: true}
			rec, _, errc := startServe(t, idleStreamer(hub, src, 10), req)

			if err := waitServe(t, errc); !errors.Is(err, CloseShutdown) {
				t.Fatalf("Serve() = %v, want %v", err, CloseShutdown)
			}
			if diff := cmp.Diff(tt.wantIDs, rec.ids(t)); diff != "" {
				t.Errorf("ids on the wire (-want +got):\n%s", diff)
			}
			snapshots, _ := src.calls()
			if got := snapshots > 0; got != tt.wantSnapshot {
				t.Errorf("Snapshot called %d time(s); wantSnapshot = %v", snapshots, tt.wantSnapshot)
			}
		})
	}
}

// The dedup floor starts at the CURSOR, not at "the highest id written".
// With nothing written — an empty snapshot, or a resume already caught up —
// a floor of zero writes every queued event, including ones at or below the
// position the client already holds. Ids reaching the wire must be strictly
// ascending, because a client's stored Last-Event-ID is the high-water mark
// of everything it has seen.
func TestServe_DiscardsQueuedEventsAtOrBelowTheCursor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		req  func(Filter) StreamRequest
	}{
		{
			name: "empty snapshot leaves the floor at the watermark",
			req:  func(f Filter) StreamRequest { return StreamRequest{Filter: f} },
		},
		{
			name: "a caught-up resume leaves the floor at Last-Event-ID",
			req: func(f Filter) StreamRequest {
				return StreamRequest{Filter: f, LastEventID: 10, HasLastID: true}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			proj, run := uuid.New(), uuid.New()
			hub := NewHub(HubConfig{SubscriberQueue: 8})
			// Nothing to write on either path: no snapshot events, nothing
			// retained to replay. Only the cursor stands between the client
			// and a duplicate.
			src := &streamSource{
				snapshot: Snapshot{Watermark: Watermark{EventID: 10}},
				oldest:   1,
			}
			publish := sync.OnceFunc(func() {
				hub.Publish([]Event{
					hubEvent(5, proj, run, "run.started"),  // already the client's
					hubEvent(11, proj, run, "run.started"), // genuinely new
				})
				hub.Close()
			})
			src.onSnapshot, src.onReplay = publish, publish

			f := runFilter(run, proj)
			rec, _, errc := startServe(t, idleStreamer(hub, src, 10), tt.req(f))

			if err := waitServe(t, errc); !errors.Is(err, CloseShutdown) {
				t.Fatalf("Serve() = %v, want %v", err, CloseShutdown)
			}
			if diff := cmp.Diff([]int64{11}, rec.ids(t)); diff != "" {
				t.Errorf("ids on the wire (-want +got):\n%s", diff)
			}
		})
	}
}

// A client disconnect is the normal end of an SSE request, not a fault: Serve
// returns nil so Task 10 does not log every closed tab as an error. And it
// must unsubscribe — the registry growing by one entry per request served is
// the one leak the hub cannot prevent.
func TestServe_StopsOnClientDisconnect(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	hub := NewHub(HubConfig{SubscriberQueue: 8})
	src := &streamSource{snapshot: Snapshot{Watermark: Watermark{EventID: 5}}}

	_, cancel, errc := startServe(t, idleStreamer(hub, src, 10), StreamRequest{Filter: runFilter(run, proj)})

	eventually(t, func() bool { return registryLen(hub) == 1 }, "the stream to subscribe")
	cancel()

	if err := waitServe(t, errc); err != nil {
		t.Fatalf("Serve() on a client disconnect = %v, want nil", err)
	}
	// Serve has returned, so its defer has already run: no waiting here.
	if got := registryLen(hub); got != 0 {
		t.Errorf("registry holds %d subscription(s) after Serve returned, want 0", got)
	}
}

// Serve's contract says nil when ctx is done, WHEREVER the stream had got to
// — and catch-up is the long part, so a tab closed during it is an ordinary
// event rather than a corner. Before the fix these two returned a wrapped
// context.Canceled, which Task 10's logging rule would have raised as a fault
// on every closed tab.
//
// The two cases differ in what the source reports for the cancelled read:
// Snapshot wraps ctx.Err(), Replay answers with an error class of its own that
// does not wrap it at all. Both must yield nil, which is what pins Serve on
// asking the CONTEXT rather than errors.Is-ing the error.
func TestServe_ReturnsNilWhenTheClientLeavesDuringCatchUp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		phase string
	}{
		{name: "during the snapshot", phase: "snapshot"},
		{name: "during the replay", phase: "replay"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			proj, run := uuid.New(), uuid.New()
			hub := NewHub(HubConfig{SubscriberQueue: 8})
			src := &streamSource{
				snapshot: Snapshot{
					Events:    []Event{hubEvent(1, proj, run, "run.started")},
					Watermark: Watermark{EventID: 1},
				},
				retained: []Event{hubEvent(2, proj, run, "run.started")},
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			// The client goes away in the middle of the read, which is the
			// window the contract is about.
			if tt.phase == "snapshot" {
				src.onSnapshot = cancel
			} else {
				src.onReplay = sync.OnceFunc(cancel)
			}

			rec := newStreamRecorder()
			errc := startServeOn(ctx, t, idleStreamer(hub, src, 10), rec, StreamRequest{Filter: runFilter(run, proj)})

			if err := waitServe(t, errc); err != nil {
				t.Fatalf("Serve() with the client gone during the %s = %v, want nil", tt.phase, err)
			}
			// And it still unsubscribed on the way out.
			if got := registryLen(hub); got != 0 {
				t.Errorf("registry holds %d subscription(s) after the client left, want 0", got)
			}
		})
	}
}

// Finding 9: the Writer owns flushing, and Serve adds none of its own. Task 6
// pinned ["flush","write","flush"] with an exact op-log and upheld it against
// an over-pin objection, deliberately, so that a future batching change has to
// renegotiate the guarantee rather than quietly acquire it. This is the other
// end of that chain: without it, an st.wr.Flush() added to send is invisible.
//
// The live event is load-bearing and was added when the first legitimate
// Flush() caller arrived. Ending on the snapshot alone leaves follow — the loop
// most likely to acquire a stray flush — entirely uncovered here: a Flush()
// added to its event case survived this test verbatim.
func TestServe_AddsNoFlushOfItsOwn(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	hub := NewHub(HubConfig{SubscriberQueue: 8})
	src := &streamSource{
		snapshot: Snapshot{
			Events:    []Event{hubEvent(1, proj, run, "run.started")},
			Watermark: Watermark{EventID: 1},
		},
		retained: []Event{hubEvent(2, proj, run, "run.started")},
	}
	src.onSnapshot = func() {
		// Queued now, drained by follow after the close: a closed channel still
		// yields what it was buffering.
		hub.Publish([]Event{hubEvent(3, proj, run, "run.started")})
		hub.Close()
	}

	rec, _, errc := startServe(t, idleStreamer(hub, src, 10), StreamRequest{Filter: runFilter(run, proj)})

	if err := waitServe(t, errc); !errors.Is(err, CloseShutdown) {
		t.Fatalf("Serve() = %v, want %v", err, CloseShutdown)
	}
	if diff := cmp.Diff([]int64{1, 2, 3}, rec.ids(t)); diff != "" {
		t.Errorf("ids on the wire (-want +got):\n%s", diff)
	}
	// NewWriter's connect flush, then exactly one write and one flush per
	// frame — nothing else. The heartbeat is an hour away and cannot appear.
	want := []string{"flush", "write", "flush", "write", "flush", "write", "flush"}
	if diff := cmp.Diff(want, rec.opLog()); diff != "" {
		t.Errorf("write/flush interleaving (-want +got):\n%s", diff)
	}
}

// Nothing else here exercises the ticker at all: one that is never started,
// or started at the wrong interval, would pass every other test in this file
// and leave idle streams to be culled by the proxy.
func TestServe_HeartbeatsWhileIdle(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	hub := NewHub(HubConfig{SubscriberQueue: 8})
	src := &streamSource{}
	// GrantRecheck is spelled out rather than defaulted: a default that merely
	// happens not to fire inside this test is not a guarantee that it will not.
	s := NewStreamer(hub, src, &stubGrants{}, StreamerConfig{
		HeartbeatInterval: testPoll,
		GrantRecheck:      time.Hour,
		ReplayBatch:       10,
	})

	rec, cancel, errc := startServe(t, s, StreamRequest{Filter: runFilter(run, proj)})

	eventually(t, func() bool { return strings.Contains(rec.written(), ": hb\n\n") }, "a heartbeat comment on an idle stream")
	cancel()
	if err := waitServe(t, errc); err != nil {
		t.Fatalf("Serve() on a client disconnect = %v, want nil", err)
	}
}

// A zero StreamerConfig is what an unset config section hands over, and every
// one of its zeroes fails silently rather than loudly: a zero
// HeartbeatInterval or GrantRecheck PANICS time.NewTicker on the first request
// served, and a zero ReplayBatch reaches Replay, which answers a non-positive
// limit with an empty page — a stream that opens, writes its snapshot and then
// never catches up, with nothing logged anywhere to say so.
func TestServe_ZeroConfigIsUsable(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	hub := NewHub(HubConfig{SubscriberQueue: 8})
	src := &streamSource{
		snapshot: Snapshot{
			Events:    []Event{hubEvent(1, proj, run, "run.started")},
			Watermark: Watermark{EventID: 1},
		},
		retained: []Event{hubEvent(2, proj, run, "run.started")},
	}
	src.onSnapshot = func() { hub.Close() }

	s := NewStreamer(hub, src, &stubGrants{}, StreamerConfig{})
	if s.cfg.HeartbeatInterval != defaultHeartbeatInterval {
		t.Errorf("HeartbeatInterval = %v, want the default %v", s.cfg.HeartbeatInterval, defaultHeartbeatInterval)
	}
	if s.cfg.GrantRecheck != defaultGrantRecheck {
		t.Errorf("GrantRecheck = %v, want the default %v", s.cfg.GrantRecheck, defaultGrantRecheck)
	}

	rec, _, errc := startServe(t, s, StreamRequest{Filter: runFilter(run, proj)})

	if err := waitServe(t, errc); !errors.Is(err, CloseShutdown) {
		t.Fatalf("Serve() = %v, want %v", err, CloseShutdown)
	}
	if diff := cmp.Diff([]int64{1, 2}, rec.ids(t)); diff != "" {
		t.Errorf("ids on the wire (-want +got):\n%s", diff)
	}
	_, replays := src.calls()
	if diff := cmp.Diff([]replayCall{{After: 1, Limit: defaultReplayBatch}}, replays); diff != "" {
		t.Errorf("replay pages (-want +got):\n%s", diff)
	}
}

// The one failure that can still become an HTTP error response, because it
// happens before NewWriter commits the 200.
//
// It does NOT pin that the rejection precedes Subscribe: the registry is
// asserted after Serve has returned, by which time the deferred Close would
// have tidied a subscription away regardless, and moving NewWriter after
// Subscribe is behaviourally equivalent anyway — finding 10 is a preference,
// not an invariant. What it pins is the error, the absence of a leak, and
// that no read was issued.
//
// Serve runs in a goroutine even though the call is expected to return at
// once. If NewWriter ever stopped rejecting non-flushers, Serve would park in
// follow on the one-hour heartbeat: called inline that hangs the test — and
// with it the whole package, on the suite timeout, burying its own message —
// and t.Cleanup cannot rescue it, because the test is the thing that is
// blocked. In a goroutine the same regression fails at patience with the
// message below, and the cleanup cancel releases the parked stream.
func TestServe_RejectsANonFlusher(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	hub := NewHub(HubConfig{SubscriberQueue: 8})
	src := &streamSource{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	errc := make(chan error, 1)
	go func() {
		errc <- idleStreamer(hub, src, 10).Serve(
			ctx,
			&plainResponseWriter{header: http.Header{}},
			StreamRequest{Filter: runFilter(run, proj)},
		)
	}()

	err := waitServe(t, errc)
	if err == nil {
		t.Fatal("Serve() on a non-Flusher returned nil, want an error")
	}
	if got := registryLen(hub); got != 0 {
		t.Errorf("registry holds %d subscription(s) after a rejected stream, want 0", got)
	}
	if snapshots, _ := src.calls(); snapshots != 0 {
		t.Errorf("Snapshot called %d time(s) on a rejected stream, want 0", snapshots)
	}
}

// Every return path unsubscribes, including the ones that are nobody's normal
// case. A write failure is the client that vanished mid-frame.
func TestServe_ClosesTheSubscriptionOnAWriteError(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	hub := NewHub(HubConfig{SubscriberQueue: 8})
	src := &streamSource{
		snapshot: Snapshot{
			Events:    []Event{hubEvent(1, proj, run, "run.started")},
			Watermark: Watermark{EventID: 1},
		},
	}
	boom := errors.New("connection reset by peer")
	rec := newStreamRecorder()
	rec.failWrites(boom)

	errc := startServeOn(context.Background(), t, idleStreamer(hub, src, 10), rec, StreamRequest{Filter: runFilter(run, proj)})

	if err := waitServe(t, errc); !errors.Is(err, boom) {
		t.Fatalf("Serve() = %v, want it to wrap %v", err, boom)
	}
	if got := registryLen(hub); got != 0 {
		t.Errorf("registry holds %d subscription(s) after a failed write, want 0", got)
	}
}

// A source that answers a full page of rows the client already has would spin
// the pager forever, pinning a CPU with no request in flight to explain it.
// Every filtering predicate is in SQL, so this cannot happen to a working
// source — a broken one should end a stream instead.
func TestServe_StopsWhenReplayCannotAdvance(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	hub := NewHub(HubConfig{SubscriberQueue: 8})
	src := &streamSource{
		snapshot: Snapshot{Watermark: Watermark{EventID: 10}},
		brokenPage: []Event{
			hubEvent(1, proj, run, "run.started"),
			hubEvent(2, proj, run, "run.started"),
		},
	}
	src.onSnapshot = func() { hub.Close() }

	rec, _, errc := startServe(t, idleStreamer(hub, src, 2), StreamRequest{Filter: runFilter(run, proj)})

	if err := waitServe(t, errc); !errors.Is(err, CloseShutdown) {
		t.Fatalf("Serve() = %v, want %v", err, CloseShutdown)
	}
	if _, replays := src.calls(); len(replays) != 1 {
		t.Errorf("Replay called %d time(s) on a source that cannot advance, want 1", len(replays))
	}
	// And it wrote nothing: every row in the page is at or below the cursor.
	if got := rec.ids(t); len(got) != 0 {
		t.Errorf("ids on the wire = %v, want none", got)
	}
}

// forbiddenOnTheWire is the terminal frame spelled out, deliberately as a
// literal rather than as the constant Serve writes: a test that compared the
// implementation against itself would pass whatever bytes the implementation
// drifted to, and these bytes are a wire contract Task 10 documents.
const forbiddenOnTheWire = "event: forbidden\ndata: {\"error\":\"forbidden\"}\n\n"

// Revocation must END a stream, not merely stop new events from matching. An
// IDLE stream has no event to test anything against, so the empty scope is the
// only thing that can close it — and the empty scope is exactly what a revoked
// or deactivated principal resolves to.
func TestServe_ClosesWhenGrantRevoked(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	hub := NewHub(HubConfig{SubscriberQueue: 8})
	src := &streamSource{}
	grants := &stubGrants{steps: []grantStep{
		{scope: Scope{Projects: []uuid.UUID{proj}}},
		{scope: Scope{}},
	}}
	s := NewStreamer(hub, src, grants, StreamerConfig{
		HeartbeatInterval: time.Hour,
		GrantRecheck:      testPoll,
		ReplayBatch:       10,
	})

	rec, _, errc := startServe(t, s, StreamRequest{Filter: runFilter(run, proj)})

	if err := waitServe(t, errc); !errors.Is(err, CloseForbidden) {
		t.Fatalf("Serve() after the grant was revoked = %v, want %v", err, CloseForbidden)
	}
	// The whole body, not a substring: nothing was streamed before the frame
	// and there is NO id: line in it. Ids on the wire are the client's resume
	// point, and a client that adopted this one would resume from a position
	// that never named an event.
	if diff := cmp.Diff(forbiddenOnTheWire, rec.written()); diff != "" {
		t.Errorf("wire bytes (-want +got):\n%s", diff)
	}
}

// The attention stream survives a narrowing: the rest of the scope is still
// live, so an event from a project that left it is dropped and the stream
// carries on. The hub is NOT told about the new scope — Serve does not mutate
// a subscription's filter, because Publish reads it on the tailer goroutine —
// so both events really do arrive and follow is what decides between them.
func TestServe_DropsEventsOutsideRefreshedScope(t *testing.T) {
	t.Parallel()
	kept, lost, run := uuid.New(), uuid.New(), uuid.New()
	hub := NewHub(HubConfig{SubscriberQueue: 8})
	src := &streamSource{}
	grants := &stubGrants{steps: []grantStep{{scope: Scope{Projects: []uuid.UUID{kept}}}}}
	s := NewStreamer(hub, src, grants, StreamerConfig{
		HeartbeatInterval: time.Hour,
		GrantRecheck:      testPoll,
		ReplayBatch:       10,
	})

	f := Filter{Attention: true, Scope: Scope{Projects: []uuid.UUID{kept, lost}}}
	rec, cancel, errc := startServe(t, s, StreamRequest{Filter: f})

	// One completed re-check means follow holds the narrowed scope: the
	// assignment happens on the same goroutine before it can receive again.
	eventually(t, func() bool { return grants.callCount() > 0 }, "the first grant re-check")
	hub.Publish([]Event{
		hubEvent(1, lost, run, "run.started"),
		hubEvent(2, kept, run, "run.started"),
	})

	// Waiting for the KEPT event is what makes this deterministic without a
	// settle window: events are delivered and consumed in id order, so once 2
	// is on the wire, 1 has already been decided.
	eventually(t, func() bool { return slices.Contains(rec.ids(t), int64(2)) }, "the kept project's event")
	if diff := cmp.Diff([]int64{2}, rec.ids(t)); diff != "" {
		t.Errorf("ids on the wire (-want +got):\n%s", diff)
	}

	// And the stream is still serving: a narrowing is not a close.
	cancel()
	if err := waitServe(t, errc); err != nil {
		t.Fatalf("Serve() on an attention stream that only narrowed = %v, want nil", err)
	}
}

// A per-run stream is the opposite case: its whole subject just left the
// principal's grants, so there is nothing left for it to show and it ends.
//
// The refreshed scope here is NON-empty — the principal still holds another
// project — so the idle close cannot fire and only the event can end this
// stream. That is deliberate: "the run's project left the scope" is not
// computable without an event, because a Filter carries a RunID and never that
// run's project.
func TestServe_ClosesPerRunStreamOnAForbiddenEvent(t *testing.T) {
	t.Parallel()
	proj, other, run := uuid.New(), uuid.New(), uuid.New()
	hub := NewHub(HubConfig{SubscriberQueue: 8})
	src := &streamSource{}
	grants := &stubGrants{steps: []grantStep{{scope: Scope{Projects: []uuid.UUID{other}}}}}
	s := NewStreamer(hub, src, grants, StreamerConfig{
		HeartbeatInterval: time.Hour,
		GrantRecheck:      testPoll,
		ReplayBatch:       10,
	})

	rec, _, errc := startServe(t, s, StreamRequest{Filter: runFilter(run, proj)})

	eventually(t, func() bool { return grants.callCount() > 0 }, "the first grant re-check")
	hub.Publish([]Event{hubEvent(1, proj, run, "run.started")})

	if err := waitServe(t, errc); !errors.Is(err, CloseForbidden) {
		t.Fatalf("Serve() on a per-run stream whose project left the scope = %v, want %v", err, CloseForbidden)
	}
	// The whole body: the event that triggered the close never reached the
	// client, which is the point of checking before writing.
	if diff := cmp.Diff(forbiddenOnTheWire, rec.written()); diff != "" {
		t.Errorf("wire bytes (-want +got):\n%s", diff)
	}
}

// A resolver failure is not a permissions change. Closing on one would make
// every database hiccup look like a revocation to every open stream at once.
//
// Four blips in eight checks is more failures than the cap, but never three in
// a row: this pins BOTH that a blip is tolerated and that a success resets the
// count, which is what stops a long-lived stream from accumulating unrelated
// hiccups until it trips the cap.
func TestServe_ResolverBlipsDoNotCloseTheStream(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	hub := NewHub(HubConfig{SubscriberQueue: 8})
	src := &streamSource{}
	boom := errors.New("grants: connection refused")
	live := Scope{Projects: []uuid.UUID{proj}}
	script := []grantStep{
		{err: boom},
		{scope: live},
		{err: boom},
		{scope: live},
		{err: boom},
		{scope: live},
		{err: boom},
		{scope: live},
	}
	grants := &stubGrants{steps: script}
	s := NewStreamer(hub, src, grants, StreamerConfig{
		HeartbeatInterval: time.Hour,
		GrantRecheck:      testPoll,
		ReplayBatch:       10,
	})

	rec, cancel, errc := startServe(t, s, StreamRequest{Filter: runFilter(run, proj)})

	eventually(t, func() bool { return grants.callCount() >= len(script) }, "the scripted blips to run out")
	hub.Publish([]Event{hubEvent(1, proj, run, "run.started")})
	eventually(t, func() bool { return slices.Contains(rec.ids(t), int64(1)) }, "an event after the blips")

	cancel()
	if err := waitServe(t, errc); err != nil {
		t.Fatalf("Serve() after transient resolver failures = %v, want nil", err)
	}
}

// A resolver that never answers leaves the stream running on a scope nobody
// can confirm — the fail-open the re-check exists to close. So the tolerance
// is bounded, and past the bound the stream ends.
//
// It ends as a FAULT, not as a denial: nothing denied this principal, we could
// not ask. Reporting CloseForbidden here would put "not authorized" in the
// logs when the truth is "the grants store is down", and hand Task 10 a
// routine revocation and a real outage under one label.
func TestServe_ClosesAfterSustainedResolverFailure(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	hub := NewHub(HubConfig{SubscriberQueue: 8})
	src := &streamSource{}
	boom := errors.New("grants: connection refused")
	grants := &stubGrants{steps: []grantStep{{err: boom}}}
	s := NewStreamer(hub, src, grants, StreamerConfig{
		HeartbeatInterval: time.Hour,
		GrantRecheck:      testPoll,
		ReplayBatch:       10,
	})

	rec, _, errc := startServe(t, s, StreamRequest{Filter: runFilter(run, proj)})

	err := waitServe(t, errc)
	if !errors.Is(err, boom) {
		t.Fatalf("Serve() after a sustained resolver outage = %v, want it to wrap %v", err, boom)
	}
	if errors.Is(err, CloseForbidden) {
		t.Errorf("Serve() = %v, want it NOT to report %v: authorization was never checked, only unreachable", err, CloseForbidden)
	}
	if got := grants.callCount(); got != maxGrantFailures {
		t.Errorf("resolver called %d time(s) before the stream ended, want exactly %d", got, maxGrantFailures)
	}
	// The client is told the same thing either way — the stream is over and a
	// status is long since committed — even though the log is not.
	if diff := cmp.Diff(forbiddenOnTheWire, rec.written()); diff != "" {
		t.Errorf("wire bytes (-want +got):\n%s", diff)
	}
}

// The handler resolved this exact scope moments ago to build the Filter, so
// the first tick is the earliest a re-check can tell anyone anything new. An
// eager resolve at the top of follow would put a database round-trip on every
// stream open for an answer nothing has had time to change.
func TestServe_DoesNotResolveGrantsBeforeTheFirstTick(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	hub := NewHub(HubConfig{SubscriberQueue: 8})
	src := &streamSource{}
	grants := &stubGrants{}
	s := NewStreamer(hub, src, grants, StreamerConfig{
		HeartbeatInterval: time.Hour,
		GrantRecheck:      time.Hour,
		ReplayBatch:       10,
	})

	rec, cancel, errc := startServe(t, s, StreamRequest{Filter: runFilter(run, proj)})

	// A live event on the wire is proof the stream reached follow — where an
	// eager resolve would live — and got past its first iteration.
	eventually(t, func() bool { return registryLen(hub) == 1 }, "the stream to subscribe")
	hub.Publish([]Event{hubEvent(1, proj, run, "run.started")})
	eventually(t, func() bool { return slices.Contains(rec.ids(t), int64(1)) }, "a live event on the wire")

	if got := grants.callCount(); got != 0 {
		t.Errorf("resolver called %d time(s) before the first re-check tick, want 0", got)
	}
	cancel()
	if err := waitServe(t, errc); err != nil {
		t.Fatalf("Serve() on a client disconnect = %v, want nil", err)
	}
}

// A Streamer with no resolver never re-checks anything, which is precisely the
// fail-open this task closes — and it fails silently, as a stream that simply
// keeps working. Refusing at construction turns it into one panic at process
// start instead of one per request, inside an HTTP handler goroutine.
func TestNewStreamer_PanicsOnANilGrantResolver(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("NewStreamer with a nil GrantResolver returned normally, want a panic")
		}
	}()
	NewStreamer(NewHub(HubConfig{}), &streamSource{}, nil, StreamerConfig{})
}
