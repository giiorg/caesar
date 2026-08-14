package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/realtime"
	realtimeapi "github.com/giiorg/caesar/backend/internal/realtime/api"
)

// runLoopFunc is a Run loop the wiring tests drive by hand. The real one is
// realtime.Tailer, which needs a database to fail the way these tests need
// it to fail.
type runLoopFunc func(context.Context) error

func (f runLoopFunc) Run(ctx context.Context) error { return f(ctx) }

// Tailer.Run has exactly one exit that is not shutdown — a cursor it could
// not seed — and its own godoc calls that a failed startup. The goroutine
// shape the rest of serve.go uses, `go func() { _ = x.Run(ctx) }()`, would
// discard it, and the process would then serve streams that get a snapshot
// and then silence forever with nothing anywhere to say why.
func TestRunTailer_ReportsAFailedStartupButNotAShutdown(t *testing.T) {
	t.Run("a Run that fails reaches the caller", func(t *testing.T) {
		boom := errors.New("seed tailer cursor: no such table")
		done, failed := runTailer(context.Background(), runLoopFunc(func(context.Context) error {
			return boom
		}))
		select {
		case err := <-failed:
			if !errors.Is(err, boom) {
				t.Fatalf("failed = %v, want it to wrap %v", err, boom)
			}
			if !strings.Contains(err.Error(), "tailer") {
				t.Errorf("failed = %q, want it to name the tailer: it is one of several "+
					"goroutines this process starts and the log must say which died", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a tailer that could not start reported nothing")
		}
		<-done
	})

	t.Run("a cancelled Run is shutdown, not a fault", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		done, failed := runTailer(ctx, runLoopFunc(func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}))
		cancel()
		// The send, if there were one, happens before the deferred close, so
		// this is a decision and not a race.
		<-done
		select {
		case err := <-failed:
			t.Fatalf("failed = %v, want nothing: every shutdown would otherwise "+
				"report a fault", err)
		default:
		}
	})
}

// recordingCloser stands in for the waker's dedicated connection.
type recordingCloser struct {
	mu     sync.Mutex
	closed bool
}

func (c *recordingCloser) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *recordingCloser) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Stop's whole reason to exist is its order, and the wait is the half that is
// easiest to drop: cancelling the tailer's context only ASKS it to stop, and
// closing the connection it reads on before it has actually returned tears
// the ground out from under a goroutine that outlives runServe.
func TestRealtimeRuntime_StopWaitsForTheTailerBeforeTearingDownWhatItUses(t *testing.T) {
	done := make(chan struct{})
	listener := &recordingCloser{}
	hub := realtime.NewHub(realtime.HubConfig{})
	cancelled := make(chan struct{})
	rt := &realtimeRuntime{
		hub:      hub,
		listener: listener,
		stop:     func() { close(cancelled) },
		done:     done,
		log:      discard(),
	}

	returned := make(chan struct{})
	go func() { defer close(returned); rt.Stop() }()

	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop never cancelled the tailer's context")
	}

	// Nothing the tailer uses may be gone while it is still running. The bound
	// is the assertion: a Stop that does not wait returns in microseconds.
	select {
	case <-returned:
		t.Fatal("Stop returned while the tailer was still running: it closes the " +
			"connection the tailer reads on, and a goroutine outliving runServe " +
			"finds it gone underneath itself")
	case <-time.After(200 * time.Millisecond):
	}
	if listener.isClosed() {
		t.Fatal("the listening connection was closed before the tailer stopped using it")
	}
	live := hub.Subscribe(realtime.AttentionFilter(realtime.Scope{All: true}))
	if live.Err() != nil {
		t.Fatalf("the hub was closed before the tailer stopped publishing into it: %v", live.Err())
	}

	close(done)
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop never returned after the tailer did")
	}
	if !listener.isClosed() {
		t.Error("Stop left the waker's dedicated connection open")
	}
	after := hub.Subscribe(realtime.AttentionFilter(realtime.Scope{All: true}))
	if !errors.Is(after.Err(), realtime.CloseShutdown) {
		t.Errorf("hub still live after Stop: Subscribe.Err = %v, want CloseShutdown", after.Err())
	}
}

// recordingSource is the writing double finding 5 needs, and it is a SOURCE
// rather than a fake StreamServer for a reason: a double that returns an
// error without writing is exactly what makes the existing runs/api suite
// blind to this bug. Handing the REAL Streamer a source whose Snapshot fails
// produces the only shape that matters — NewWriter has committed the 200 and
// flushed the stream headers, and the error arrives after that.
//
// It also records the Filter it was handed, which is the only falsifiable
// form of "the adapter builds its filter with realtime.RunFilter".
type recordingSource struct {
	err    error
	filter realtime.Filter
}

func (s *recordingSource) Snapshot(_ context.Context, f realtime.Filter) (realtime.Snapshot, error) {
	s.filter = f
	return realtime.Snapshot{}, s.err
}

func (s *recordingSource) Replay(context.Context, realtime.Filter, int64, int) ([]realtime.Event, error) {
	return nil, nil
}

func (s *recordingSource) Since(context.Context, int64, int) ([]realtime.Event, error) {
	return nil, nil
}
func (s *recordingSource) Oldest(context.Context) (int64, error) { return 0, nil }
func (s *recordingSource) Head(context.Context) (int64, error)   { return 0, nil }

// fixedScope is a GrantResolver that answers the same thing every time.
type fixedScope struct{ scope realtime.Scope }

func (f fixedScope) ScopeFor(context.Context, uuid.UUID) (realtime.Scope, error) {
	return f.scope, nil
}

// unflushableWriter has nothing in its Unwrap chain that can flush, which is
// the ONE Serve failure that has written nothing at all.
type unflushableWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *unflushableWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *unflushableWriter) Write(p []byte) (int, error) { return w.body.Write(p) }
func (w *unflushableWriter) WriteHeader(status int)      { w.status = status }

func newTestStreamAPI(src realtime.EventSource, log *slog.Logger) *realtimeapi.API {
	streamer := realtime.NewStreamer(realtime.NewHub(realtime.HubConfig{}), src,
		fixedScope{scope: realtime.Scope{All: true}}, realtime.StreamerConfig{})
	// The authenticator is nil because ServeRunStream never authenticates: the
	// runs surface has already done it and hands the principal in.
	return realtimeapi.New(streamer, nil, fixedScope{scope: realtime.Scope{All: true}}, log)
}

// The trap this task exists to walk past. StreamServer's error means exactly
// one thing — the stream committed nothing and the caller may still answer
// with a status — while realtime.Serve returns an error for post-commit
// endings too. An adapter that forwards Serve's error verbatim makes
// runs/api call httpx.Problem over a committed 200 and append a problem
// document to the SSE body.
func TestServeRunStream_TranslatesAnEndingThatAlreadyCommittedTheResponse(t *testing.T) {
	boom := errors.New("app: realtime snapshot: watermark: connection refused")
	src := &recordingSource{err: boom}
	logBuf := &bytes.Buffer{}
	api := newTestStreamAPI(src, slog.New(slog.NewJSONHandler(logBuf, nil)))

	rec := httptest.NewRecorder()
	runID, projectID, userID := uuid.New(), uuid.New(), uuid.New()
	err := api.ServeRunStream(context.Background(), rec, runID, projectID, userID, 0, false)
	if err != nil {
		t.Fatalf("ServeRunStream = %v, want nil: runs/api answers a non-nil error with "+
			"httpx.Problem, and this response was committed 200 the moment the stream "+
			"headers flushed", err)
	}
	// The double really did commit, or the assertion above proves nothing.
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream: the failure under test is "+
			"only interesting once the response is committed", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// Translating is not swallowing: the implementation is the only side that
	// can tell a routine ending from a fault, so it has to say so.
	if !strings.Contains(logBuf.String(), "connection refused") {
		t.Errorf("the source failure was neither returned nor logged:\n%s", logBuf.String())
	}
	// realtime.RunFilter, never a literal: Filter's selector fields are exported
	// and the combination with Attention also set is a per-run stream wearing
	// the attention stream's clothes (realtime.AttentionFilter).
	if src.filter.RunID == nil || *src.filter.RunID != runID || src.filter.Attention {
		t.Errorf("filter = %+v, want a RunFilter over %s", src.filter, runID)
	}
	// And the project reached it. This is the assertion a Filter LITERAL would
	// fail while every other check here still passed: runProject is unexported,
	// so a literal leaves it nil, and nil is read as "not supplied" — the
	// stream would serve correctly and simply never close on a partial
	// revocation over a quiescent run. Nothing else in this process can see
	// that, because the value crosses two package boundaries to get here.
	got, ok := src.filter.RunProject()
	if !ok {
		t.Errorf("filter carries no run project at all: the adapter built a Filter " +
			"literal, or dropped the id runs/api handed it")
	} else if got != projectID {
		t.Errorf("filter.RunProject() = %s, want %s: the run's project did not survive "+
			"runs/api -> realtime/api -> the Filter", got, projectID)
	}
}

// The other half of the same rule: the ONE ending that committed nothing must
// still reach the caller, because a stream that never opened and answers a
// bare 200 is byte-for-byte what a working, idle stream looks like.
func TestServeRunStream_ErrNotStartedStillReachesTheCaller(t *testing.T) {
	api := newTestStreamAPI(&recordingSource{}, discard())

	w := &unflushableWriter{}
	err := api.ServeRunStream(context.Background(), w, uuid.New(), uuid.New(), uuid.New(), 0, false)

	if !errors.Is(err, realtime.ErrNotStarted) {
		t.Fatalf("ServeRunStream = %v, want ErrNotStarted: nothing was written, so "+
			"runs/api can and must answer this one with a real status", err)
	}
	if w.body.Len() != 0 {
		t.Errorf("body = %q, want empty", w.body.String())
	}
}

// A zero SnapshotEvents must not reach the queries. It would arrive as
// LIMIT 0, and a stream that opens on an empty backlog is indistinguishable
// from a run with no history — the failure succeeds. config.Load rejects a
// non-positive value, so this is about the callers that never went through
// Load: runServe takes a config.Config, and the boot tests hand it one built
// by hand.
//
// It is also the tie-back for defaultSnapshotEvents, which is a second copy of
// config's own default. Asserting the RESULT rather than the constant is
// deliberate: this fails both when the defaulting is removed and when either
// end of the duplicated 500 moves alone, and it needs no compile-time
// reference to survive.
func TestNewRealtimeEventSource_ZeroTakesTheConfiguredDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caesar.json")
	if err := os.WriteFile(path, []byte(`{"database": {"url": "postgres://x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	want := rowLimit(cfg.Realtime.SnapshotEvents)
	if got := newRealtimeEventSource(nil, 0).snapshotEvents; got != want {
		t.Errorf("a zero backlog size became LIMIT %d, want the configured default %d: "+
			"zero opens every stream on an empty backlog and reports nothing", got, want)
	}
}
