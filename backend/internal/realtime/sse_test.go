package realtime

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
)

// sseRecorder wraps httptest.ResponseRecorder to record the interleaving of
// body writes and flushes. Header writes go through Header() directly, so ops
// only ever holds "write" (body bytes) and "flush" — which is exactly the
// order Test 1 exists to pin.
type sseRecorder struct {
	*httptest.ResponseRecorder
	ops []string
}

func newSSERecorder() *sseRecorder {
	return &sseRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *sseRecorder) Write(p []byte) (int, error) {
	r.ops = append(r.ops, "write")
	return r.ResponseRecorder.Write(p)
}

func (r *sseRecorder) Flush() {
	r.ops = append(r.ops, "flush")
	r.ResponseRecorder.Flush()
}

// plainResponseWriter implements http.ResponseWriter and deliberately NOT
// http.Flusher. httptest.ResponseRecorder implements Flusher, so the negative
// case in Test 5 needs this wrapper — a bare recorder cannot exercise it.
type plainResponseWriter struct{ header http.Header }

func (p *plainResponseWriter) Header() http.Header         { return p.header }
func (p *plainResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (p *plainResponseWriter) WriteHeader(int)             {}

// The stream headers and their flush belong to NewWriter, before any event
// exists to write: flushing is what commits the 200 and defeats proxy
// buffering, and a client behind a buffering tier sees nothing at all until
// the first flush happens.
func TestWriter_WritesHeadersAndFlushesBeforeAnyEvent(t *testing.T) {
	t.Parallel()
	rec := newSSERecorder()
	w, err := NewWriter(rec, time.Minute, time.Now)
	if err != nil {
		t.Fatalf("NewWriter() error = %v, want nil", err)
	}

	wantHeaders := map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-cache",
		"Connection":        "keep-alive",
		"X-Accel-Buffering": "no",
	}
	for k, want := range wantHeaders {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}

	// Construction alone must have flushed — and written no body bytes.
	if diff := cmp.Diff([]string{"flush"}, rec.ops); diff != "" {
		t.Fatalf("ops after NewWriter (-want +got):\n%s", diff)
	}

	if err := w.WriteEvent(hubEvent(1, uuid.New(), uuid.New(), "run.started")); err != nil {
		t.Fatalf("WriteEvent() error = %v, want nil", err)
	}
	// The connect-time flush precedes the first event's bytes, and the event
	// is flushed as soon as it is written — a frame parked in a buffer is a
	// frame the client does not have.
	if diff := cmp.Diff([]string{"flush", "write", "flush"}, rec.ops); diff != "" {
		t.Errorf("ops after first WriteEvent (-want +got):\n%s", diff)
	}
}

// The exact frame bytes are the wire contract: id: is what the browser echoes
// back as Last-Event-ID, event: is what addEventListener dispatches on, and
// data: is one line of compact StreamFrame JSON. The expected string also pins
// what is deliberately ABSENT — project_id and correlation_id are set on the
// Event here and must not reach the wire.
func TestWriter_FrameShape(t *testing.T) {
	t.Parallel()
	rec := newSSERecorder()
	w, err := NewWriter(rec, time.Minute, time.Now)
	if err != nil {
		t.Fatalf("NewWriter() error = %v, want nil", err)
	}

	run := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	e := hubEvent(42, uuid.New(), run, "gate.waiting")
	e.Payload = map[string]any{"gate": "publish", "attempt": float64(1)}
	e.OccurredAt = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	e.CorrelationID = "corr-7"

	if err := w.WriteEvent(e); err != nil {
		t.Fatalf("WriteEvent() error = %v, want nil", err)
	}

	want := "id: 42\n" +
		"event: gate.waiting\n" +
		`data: {"id":42,"run_id":"11111111-1111-1111-1111-111111111111","kind":"gate.waiting","payload":{"attempt":1,"gate":"publish"},"occurred_at":"2026-08-10T12:00:00Z"}` +
		"\n\n"
	if diff := cmp.Diff(want, rec.Body.String()); diff != "" {
		t.Errorf("frame (-want +got):\n%s", diff)
	}
}

// A raw newline inside data: splits the frame: everything after it stops being
// data and the parser treats the remainder as field garbage. Compact JSON
// encoding escapes the newline instead, and this test is what notices if the
// encoder ever stops doing that — including json.Encoder's own trailing
// newline, which would land a blank line inside the frame.
func TestWriter_DataIsSingleLine(t *testing.T) {
	t.Parallel()
	rec := newSSERecorder()
	w, err := NewWriter(rec, time.Minute, time.Now)
	if err != nil {
		t.Fatalf("NewWriter() error = %v, want nil", err)
	}

	e := hubEvent(7, uuid.New(), uuid.New(), "run.failed")
	e.Payload = map[string]any{"message": "line one\nline two"}

	if err := w.WriteEvent(e); err != nil {
		t.Fatalf("WriteEvent() error = %v, want nil", err)
	}

	body := rec.Body.String()
	if !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("frame does not end with a blank line: %q", body)
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("frame has %d lines %q, want exactly 3 (id, event, data)", len(lines), lines)
	}
	var dataLines int
	for _, l := range lines {
		if strings.HasPrefix(l, "data:") {
			dataLines++
		}
	}
	if dataLines != 1 {
		t.Errorf("frame has %d data: lines, want exactly 1", dataLines)
	}
	if !strings.Contains(lines[2], `line one\nline two`) {
		t.Errorf("data line does not carry the JSON-escaped newline: %q", lines[2])
	}
}

// Heartbeat keeps the tunnel open without appearing as an event, and only
// when the stream is actually idle: the Writer tracks its own last write, so
// Task 8's Serve just ticks and calls Heartbeat rather than resetting a
// ticker on every event. Both branches matter — a comment on a busy stream is
// wasted bytes, and no comment on an idle one is a dead tunnel.
func TestWriter_HeartbeatIsAComment(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000, 0)
	clock := func() time.Time { return now }

	rec := newSSERecorder()
	w, err := NewWriter(rec, 30*time.Second, clock)
	if err != nil {
		t.Fatalf("NewWriter() error = %v, want nil", err)
	}

	// Not elapsed: the connect counts as the last write, so an immediate tick
	// writes nothing.
	if err := w.Heartbeat(); err != nil {
		t.Fatalf("Heartbeat() error = %v, want nil", err)
	}
	if got := rec.Body.String(); got != "" {
		t.Fatalf("Heartbeat wrote %q before the idle interval elapsed, want nothing", got)
	}

	// Elapsed: the comment goes out, and nothing in it can parse as an event.
	now = now.Add(30 * time.Second)
	if err := w.Heartbeat(); err != nil {
		t.Fatalf("Heartbeat() error = %v, want nil", err)
	}
	if diff := cmp.Diff(": hb\n\n", rec.Body.String()); diff != "" {
		t.Errorf("heartbeat bytes (-want +got):\n%s", diff)
	}

	// The heartbeat itself counts as a write: a second tick at the same
	// instant is idle-by-zero and must stay silent.
	if err := w.Heartbeat(); err != nil {
		t.Fatalf("Heartbeat() error = %v, want nil", err)
	}
	if diff := cmp.Diff(": hb\n\n", rec.Body.String()); diff != "" {
		t.Errorf("body after an immediate second tick (-want +got):\n%s", diff)
	}
}

// A ResponseWriter that cannot flush buffers the stream forever: every frame
// is accepted and none is delivered, with no symptom but silence. Refusing at
// construction turns that into a loud startup fault.
func TestWriter_RejectsNonFlusher(t *testing.T) {
	t.Parallel()
	w, err := NewWriter(&plainResponseWriter{header: http.Header{}}, time.Minute, time.Now)
	if err == nil {
		t.Fatal("NewWriter() on a non-Flusher returned nil error, want an error")
	}
	if w != nil {
		t.Errorf("NewWriter() on a non-Flusher returned %v, want nil", w)
	}
}
