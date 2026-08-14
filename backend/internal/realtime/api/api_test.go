package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/realtime"
	realtimeapi "github.com/giiorg/caesar/backend/internal/realtime/api"
)

// patience bounds every wait in this file. The handler blocks until the client
// goes away, so a surface that never writes its headers would hang the suite
// rather than fail it — this package's neighbour has done exactly that twice on
// this plan. Every request carries a deadline instead.
const patience = 5 * time.Second

const sessionToken = "stream-tok"

// fakeSource is realtime.EventSource for a stream that catches up
// immediately. It records the Filter it was asked with, which is how the
// "this handler builds an unambiguous Filter" tests see what the handler
// built without a seam of their own.
type fakeSource struct {
	mu sync.Mutex

	snapshot realtime.Snapshot
	// replay is what Replay pages over. Everything above `after` comes back in
	// one page, so the splice reads it as caught up and goes live.
	replay []realtime.Event
	oldest int64
	// head is the table's highest id, which the splice reads on a resume to
	// bound the resume point from above. The zero is an empty table, so a
	// resume test that leaves it unset re-snapshots instead of resuming.
	head int64

	filters       []realtime.Filter
	snapshotCalls int
	replayAfter   []int64
}

var _ realtime.EventSource = (*fakeSource)(nil)

func (s *fakeSource) Snapshot(_ context.Context, f realtime.Filter) (realtime.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshotCalls++
	s.filters = append(s.filters, f)
	return s.snapshot, nil
}

func (s *fakeSource) Replay(_ context.Context, f realtime.Filter, after int64, limit int) ([]realtime.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filters = append(s.filters, f)
	s.replayAfter = append(s.replayAfter, after)
	var out []realtime.Event
	for _, e := range s.replay {
		if e.ID <= after || !f.Matches(e) {
			continue
		}
		out = append(out, e)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// Since belongs to the tailer, which no HTTP surface touches. It refuses
// rather than answers, so a caller that reaches it fails loudly instead of
// being taught a behaviour nothing needs.
func (s *fakeSource) Since(context.Context, int64, int) ([]realtime.Event, error) {
	return nil, errors.New("fakeSource: Since is the tailer's, not a stream's")
}

// Head is the tailer's too, but a resume reads it as well: the splice refuses
// a Last-Event-ID above the head rather than opening a stream whose cursor
// nothing can ever exceed.
func (s *fakeSource) Head(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.head, nil
}

func (s *fakeSource) Oldest(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.oldest, nil
}

// seen is every Filter the splice asked this source with, and how many times
// it snapshotted.
func (s *fakeSource) seen() ([]realtime.Filter, int, []int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]realtime.Filter(nil), s.filters...), s.snapshotCalls, append([]int64(nil), s.replayAfter...)
}

type fakeGrants struct {
	mu    sync.Mutex
	scope realtime.Scope
	err   error
	calls int
}

var _ realtime.GrantResolver = (*fakeGrants)(nil)

func (g *fakeGrants) ScopeFor(context.Context, uuid.UUID) (realtime.Scope, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	return g.scope, g.err
}

func (g *fakeGrants) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

type fakeAuth struct{ sessions map[string]authn.AuthSession }

func (f fakeAuth) AuthenticateSession(_ context.Context, token string) (authn.AuthSession, error) {
	s, ok := f.sessions[token]
	if !ok {
		return authn.AuthSession{}, errors.New("no such session")
	}
	return s, nil
}

// newStreamer is the splice over the fakes, tuned so nothing but the test's
// own events ever reaches the wire: a heartbeat and a re-check an hour out
// mean no comment and no scope change interleaves with the frames.
func newStreamer(src realtime.EventSource, grants realtime.GrantResolver) *realtime.Streamer {
	return realtime.NewStreamer(realtime.NewHub(realtime.HubConfig{}), src, grants, realtime.StreamerConfig{
		HeartbeatInterval: time.Hour,
		GrantRecheck:      time.Hour,
		ReplayBatch:       100,
	})
}

type harness struct {
	srv  *httptest.Server
	user authn.Principal
}

// newHarness mounts the real route table behind the SAME middleware chain
// app.Routes builds. That is not decoration: AccessLog wraps the
// ResponseWriter, and whether a stream can flush through that wrapper is the
// difference between a working endpoint and one that buffers forever.
func newHarness(t *testing.T, streams *realtime.Streamer, grants realtime.GrantResolver) *harness {
	t.Helper()
	return newHarnessBehind(t, streams, grants)
}

// newHarnessBehind is newHarness with extra middleware between the real chain
// and the handler — INNERMOST, so what it hands the handler is the last word on
// what the response writer can do.
func newHarnessBehind(
	t *testing.T, streams *realtime.Streamer, grants realtime.GrantResolver, inner ...httpx.Middleware,
) *harness {
	t.Helper()
	user := authn.Principal{UserID: uuid.New(), Email: "watcher@example.com", Role: authn.RoleMember}
	auth := fakeAuth{sessions: map[string]authn.AuthSession{
		sessionToken: {Principal: user, SessionID: uuid.New()},
	}}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mux := http.NewServeMux()
	httpx.Mount(mux, realtimeapi.Operations(realtimeapi.New(streams, auth, grants, log)))
	srv := httptest.NewServer(httpx.Chain(mux, append([]httpx.Middleware{
		httpx.WithCorrelationID, httpx.AccessLog(log), httpx.CheckOrigin("http://caesar.test"),
	}, inner...)...))
	t.Cleanup(srv.Close)
	return &harness{srv: srv, user: user}
}

// open sends the request and returns once the response headers have arrived —
// which for a stream is as soon as it has committed its 200. The deadline on
// the request context is what turns "never answered" into a failure at
// patience rather than a hung suite.
func (h *harness) open(t *testing.T, token string, headers map[string]string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.srv.URL+"/api/v1/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Cookie", authn.SessionCookie+"="+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/stream: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// readFrame reads one SSE frame: every line up to the blank one that ends it.
func readFrame(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	var b strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("reading a frame: %v (read %q so far)", err, b.String())
		}
		if line == "\n" {
			return b.String()
		}
		b.WriteString(line)
	}
}

func problemOf(t *testing.T, resp *http.Response) httpx.ProblemDetails {
	t.Helper()
	if got := resp.Header.Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}
	var pd httpx.ProblemDetails
	if err := json.NewDecoder(resp.Body).Decode(&pd); err != nil {
		t.Fatalf("decoding the problem: %v", err)
	}
	return pd
}

func attentionEvent(id int64, project, run uuid.UUID, kind string) realtime.Event {
	return realtime.Event{
		ID: id, RunID: run, ProjectID: project, Kind: kind,
		Payload:    map[string]any{"node": "gate"},
		OccurredAt: time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC),
	}
}

// The rejection has to happen BEFORE anything commits a 200, or the client
// gets a half-open stream where an authentication failure belongs.
func TestStreamAttention_RequiresAuthentication(t *testing.T) {
	t.Parallel()
	src := &fakeSource{}
	grants := &fakeGrants{scope: realtime.Scope{All: true}}
	h := newHarness(t, newStreamer(src, grants), grants)

	resp := h.open(t, "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if pd := problemOf(t, resp); pd.Status != http.StatusUnauthorized {
		t.Errorf("problem status = %d, want 401", pd.Status)
	}
	_, snapshots, _ := src.seen()
	if snapshots != 0 || grants.callCount() != 0 {
		t.Errorf("an unauthenticated request reached the stream: %d snapshots, %d scope lookups",
			snapshots, grants.callCount())
	}
}

func TestStreamAttention_SetsStreamHeaders(t *testing.T) {
	t.Parallel()
	project, run := uuid.New(), uuid.New()
	src := &fakeSource{snapshot: realtime.Snapshot{
		Events:    []realtime.Event{attentionEvent(7, project, run, "gate.opened")},
		Watermark: realtime.Watermark{EventID: 7},
	}}
	grants := &fakeGrants{scope: realtime.Scope{Projects: []uuid.UUID{project}}}
	h := newHarness(t, newStreamer(src, grants), grants)

	resp := h.open(t, sessionToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	// A Content-Length on an endless response is a promise the stream cannot
	// keep; the server must chunk instead.
	if got := resp.Header.Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q, want none", got)
	}
	if resp.ContentLength != -1 {
		t.Errorf("ContentLength = %d, want -1 (unknown)", resp.ContentLength)
	}

	frame := readFrame(t, bufio.NewReader(resp.Body))
	if !strings.Contains(frame, "id: 7\n") || !strings.Contains(frame, "event: gate.opened\n") {
		t.Fatalf("first frame = %q, want the snapshot event", frame)
	}
	_, data, ok := strings.Cut(frame, "data: ")
	if !ok {
		t.Fatalf("first frame = %q, want a data: line", frame)
	}
	var got realtime.StreamFrame
	if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &got); err != nil {
		t.Fatalf("data: line %q is not a StreamFrame: %v", data, err)
	}
	if got.ID != 7 || got.RunID != run || got.Kind != "gate.opened" {
		t.Errorf("frame = %+v, want the snapshot event", got)
	}
}

// A Filter with neither selector set opens a stream that writes nothing and
// heartbeats forever; one with both is a per-run stream wearing the attention
// stream's clothes. This handler can build only one of the two legitimate
// shapes, and this is what says which.
func TestStreamAttention_BuildsTheAttentionFilterAlone(t *testing.T) {
	t.Parallel()
	project, run := uuid.New(), uuid.New()
	src := &fakeSource{snapshot: realtime.Snapshot{
		Events:    []realtime.Event{attentionEvent(3, project, run, "publish.opened")},
		Watermark: realtime.Watermark{EventID: 3},
	}}
	scope := realtime.Scope{Projects: []uuid.UUID{project}}
	grants := &fakeGrants{scope: scope}
	h := newHarness(t, newStreamer(src, grants), grants)

	resp := h.open(t, sessionToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	readFrame(t, bufio.NewReader(resp.Body)) // the snapshot has been read by now

	filters, _, _ := src.seen()
	if len(filters) == 0 {
		t.Fatal("the source was never asked for anything")
	}
	want := realtime.Filter{Attention: true, Scope: scope}
	// AllowUnexported rather than IgnoreUnexported, and the difference matters
	// here. realtime.Filter carries an unexported runProject; ignoring it would
	// still compile and still pass, while quietly dropping the assertion that
	// an ATTENTION filter carries no run project at all — which is exactly the
	// kind of thing this test exists to catch. Allowing it keeps the comparison
	// total.
	if diff := cmp.Diff(want, filters[0], cmp.AllowUnexported(realtime.Filter{})); diff != "" {
		t.Errorf("filter (-want +got):\n%s", diff)
	}
}

// The resume header is the whole reason a dropped connection loses nothing.
func TestStreamAttention_ResumesFromLastEventID(t *testing.T) {
	t.Parallel()
	project, run := uuid.New(), uuid.New()
	src := &fakeSource{
		oldest: 1,
		// A snapshot that must NOT be sent: a resume whose tail is still
		// retained replays instead.
		snapshot: realtime.Snapshot{
			Events:    []realtime.Event{attentionEvent(2, project, run, "gate.opened")},
			Watermark: realtime.Watermark{EventID: 2},
		},
		replay: []realtime.Event{attentionEvent(6, project, run, "gate.decided")},
		head:   6,
	}
	grants := &fakeGrants{scope: realtime.Scope{Projects: []uuid.UUID{project}}}
	h := newHarness(t, newStreamer(src, grants), grants)

	resp := h.open(t, sessionToken, map[string]string{"Last-Event-ID": "5"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	frame := readFrame(t, bufio.NewReader(resp.Body))
	if !strings.Contains(frame, "id: 6\n") {
		t.Errorf("first frame = %q, want the replayed event 6", frame)
	}

	_, snapshots, after := src.seen()
	if snapshots != 0 {
		t.Errorf("snapshots = %d, want none — a live resume must not re-snapshot", snapshots)
	}
	if len(after) == 0 || after[0] != 5 {
		t.Errorf("replay cursors = %v, want the first to be 5", after)
	}
}

func TestStreamAttention_RejectsAMalformedLastEventID(t *testing.T) {
	t.Parallel()
	src := &fakeSource{}
	grants := &fakeGrants{scope: realtime.Scope{All: true}}
	h := newHarness(t, newStreamer(src, grants), grants)

	resp := h.open(t, sessionToken, map[string]string{"Last-Event-ID": "soon"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	pd := problemOf(t, resp)
	if len(pd.Errors) != 1 || pd.Errors[0].Field != "Last-Event-ID" {
		t.Errorf("errors = %+v, want one blaming Last-Event-ID", pd.Errors)
	}
	if _, snapshots, _ := src.seen(); snapshots != 0 {
		t.Errorf("snapshots = %d, want none", snapshots)
	}
}

// Resolving the scope is the LAST thing that happens while a real status is
// still available: nothing has committed the 200 yet. A failure here is a
// problem+json, never a stream that opens and shows nothing.
func TestStreamAttention_ScopeFailureIsAProblemNotAStream(t *testing.T) {
	t.Parallel()
	src := &fakeSource{}
	grants := &fakeGrants{err: errors.New("grants store unreachable")}
	h := newHarness(t, newStreamer(src, grants), grants)

	resp := h.open(t, sessionToken, nil)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if pd := problemOf(t, resp); pd.Status != http.StatusInternalServerError {
		t.Errorf("problem status = %d, want 500", pd.Status)
	}
	if _, snapshots, _ := src.seen(); snapshots != 0 {
		t.Errorf("snapshots = %d, want none", snapshots)
	}
}

// A dependency this surface cannot work without is a WIRING omission, and the
// one thing it must never do is look like a working endpoint.
func TestStreamAttention_UnwiredDependenciesAre500(t *testing.T) {
	t.Parallel()
	grants := &fakeGrants{scope: realtime.Scope{All: true}}
	for _, tc := range []struct {
		name    string
		streams *realtime.Streamer
		grants  realtime.GrantResolver
	}{
		{"no streamer", nil, grants},
		{"no grant resolver", newStreamer(&fakeSource{}, grants), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, tc.streams, tc.grants)
			resp := h.open(t, sessionToken, nil)
			if resp.StatusCode != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", resp.StatusCode)
			}
			if got := resp.Header.Get("Content-Type"); got == "text/event-stream" {
				t.Error("an unwired surface opened a stream")
			}
			if pd := problemOf(t, resp); pd.Status != http.StatusInternalServerError {
				t.Errorf("problem status = %d, want 500", pd.Status)
			}
		})
	}
}

// hideFlusher is a middleware that hands the handler a writer nothing in its
// chain can flush. Unlike httpx.AccessLog's wrapper it exposes no Unwrap, so
// http.ResponseController cannot reach the flusher underneath — the exact fault
// Task 6's type assertion used to create for EVERY request in this process.
func hideFlusher(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(unflushable{w}, r)
	})
}

// unflushable promotes Header, Write and WriteHeader off the embedded
// interface and nothing else: no Flush, no Unwrap.
type unflushable struct{ http.ResponseWriter }

// The one failure inside Serve that happens BEFORE the 200 is committed. The
// surface has to answer it with a real status: a stream that could not open and
// returns a bare 200 with no body is indistinguishable from a working stream
// that has nothing to say yet, which is the silent dead endpoint spec §4 exists
// to prevent — and the sibling per-run surface answers 500 for the same fault.
//
// The response also has to be a clean problem+json. NewWriter announces a
// stream in the header map before it can discover it cannot flush one, and
// those headers must not survive onto the error.
func TestStreamAttention_AWriterThatCannotFlushIsA500(t *testing.T) {
	t.Parallel()
	src := &fakeSource{}
	grants := &fakeGrants{scope: realtime.Scope{All: true}}
	h := newHarnessBehind(t, newStreamer(src, grants), grants, hideFlusher)

	resp := h.open(t, sessionToken, nil)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if pd := problemOf(t, resp); pd.Status != http.StatusInternalServerError {
		t.Errorf("problem status = %d, want 500", pd.Status)
	}
	for _, name := range []string{"Cache-Control", "Connection", "X-Accel-Buffering"} {
		if got := resp.Header.Get(name); got != "" {
			t.Errorf("%s = %q on the error response, want it taken back with the rest of the stream headers", name, got)
		}
	}
	// And it never got as far as reading anything to stream.
	if _, snapshots, _ := src.seen(); snapshots != 0 {
		t.Errorf("snapshots = %d, want none", snapshots)
	}
}
