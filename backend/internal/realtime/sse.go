package realtime

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// StreamFrame is the data: payload of one SSE frame, and it is a CONTRACT:
// Task 10 reflects this type into the OpenAPI document, so the JSON names
// here are the documented wire shape and the marshalled bytes cannot drift
// from it. Field order is emission order.
//
// ProjectID and CorrelationID are deliberately absent: project scope is an
// authorization internal, not client data. Adding a field here is an API
// change and should be treated as one.
type StreamFrame struct {
	ID         int64          `json:"id"`
	RunID      uuid.UUID      `json:"run_id"`
	Kind       string         `json:"kind"`
	Payload    map[string]any `json:"payload"`
	OccurredAt time.Time      `json:"occurred_at"`
}

// Writer encodes Events as SSE frames on one client's response. It is pure
// encoding — no goroutines, no locks — and belongs to a single goroutine:
// Task 8's Serve loop is its only caller per stream.
//
// Constructing one COMMITS the response: NewWriter writes the stream headers
// and flushes, which fixes the HTTP status at 200 forever. No later failure
// on this stream can become an HTTP error response — that is why Tasks 9 and
// 10 speak of a "403-shaped terminal frame" rather than a 403 status.
type Writer struct {
	w http.ResponseWriter
	// rc reaches Flush THROUGH the middleware wrappers. A type assertion to
	// http.Flusher does not: httpx.AccessLog hands every handler a wrapper
	// that records the status and exposes the real writer via Unwrap, which is
	// the contract http.ResponseController reads and a type assertion ignores.
	// Asserting made every SSE endpoint a silent empty 200 in the assembled
	// process while passing every test that used a bare recorder.
	rc    *http.ResponseController
	hb    time.Duration
	clock func() time.Time
	// lastWrite is when bytes last went to the client — connect, event or
	// heartbeat. Heartbeat consults it so that a busy stream is never padded
	// with comments: the Writer owns idleness, and Serve just ticks.
	//
	// One writer does not update it: Task 9's terminal frame, which goes
	// straight to w and flushes via Flush below. That is deliberate and
	// unobservable — the frame is the last thing the stream ever writes, so no
	// later Heartbeat exists to be misled by a stale clock. A caller that used
	// Flush for a NON-terminal write would have to revisit this.
	lastWrite time.Time
}

// sseHeaders is what makes a response a stream. It is one table rather than
// four Set calls so that NewWriter's undo below cannot drift out of step with
// its do — the pair is the whole reason it exists.
var sseHeaders = map[string]string{
	"Content-Type":  "text/event-stream",
	"Cache-Control": "no-cache",
	// Hop-by-hop and ignored by HTTP/2; harmless and mandated over HTTP/1.1.
	"Connection":        "keep-alive",
	"X-Accel-Buffering": "no",
}

// NewWriter takes over w as an SSE stream: headers written and flushed
// immediately, before any event, because the flush is what defeats proxy
// buffering and tells the client the stream is live. See Writer for the
// consequence — the 200 is committed here.
//
// hb is the idle interval Heartbeat enforces; clock feeds it (pass time.Now
// outside tests; nil defaults to it). A w that cannot be flushed — through its
// Unwrap chain, not merely by itself — is rejected outright: an unflushable
// stream buffers every frame and delivers none, with no symptom but silence.
// That is a server-side wiring fault, not an API error — it never crosses the
// boundary as itself.
//
// The header-time flush IS the check. ResponseController reports "not
// supported" for a writer nothing in its chain can flush, so the one flush
// this constructor has to do anyway answers the question, and there is no
// second, weaker test to disagree with it. The cost of checking that way is
// that the headers are already on when the answer arrives, so a rejection
// removes them again.
//
// TWO FAILURES COME OUT OF HERE AND THEY ARE NOT INTERCHANGEABLE. An
// unsupported flush touched nothing: no status, no body, and none of the stream
// headers left behind (the Del loop below — the map itself is not empty in the
// assembled process, where httpx has already set X-Correlation-ID). Serve tags
// it ErrNotStarted so a surface can still answer with a real status.
//
// A flush that was SUPPORTED and failed — the peer hung up between the
// request and the first byte — wrote the 200 first, because that is what
// net/http's FlushError does on its way to the socket; the header removal below
// is then a tidy-up of a map nobody will read again rather than a promise the
// response is clean. Serve leaves that one untagged, deliberately. Which is
// which is decided there and spelled out on realtime.ErrNotStarted, because the
// reason lives in net/http and not here.
func NewWriter(w http.ResponseWriter, hb time.Duration, clock func() time.Time) (*Writer, error) {
	if clock == nil {
		clock = time.Now
	}
	h := w.Header()
	for k, v := range sseHeaders {
		h.Set(k, v)
	}
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		// Take them back. The headers HAVE to go on before the flush — the
		// flush is what sends them — so the rejection path is reached with the
		// response already announcing a stream, and the caller then renders
		// problem+json on this same writer (realtime/api). Problem overwrites
		// Content-Type and nothing else, which would leave Cache-Control,
		// Connection and X-Accel-Buffering describing a stream that never
		// existed.
		//
		// Del, not save-and-restore: no middleware in this process sets any of
		// these before a handler runs (httpx sets only X-Correlation-ID), so
		// there is nothing of anyone else's to clobber. A chain that grows one
		// has to meet this line.
		//
		// Unconditional, though only the unsupported-flush branch can still
		// benefit: on the dead-peer branch the header has already gone into the
		// write chain and this map is no longer what anyone serializes. Not
		// worth a condition to skip a map delete, but worth not claiming it
		// undoes something there.
		for k := range sseHeaders {
			h.Del(k)
		}
		return nil, fmt.Errorf("realtime: response writer cannot flush: %w", err)
	}
	return &Writer{w: w, rc: rc, hb: hb, clock: clock, lastWrite: clock()}, nil
}

// WriteEvent emits e as one frame and flushes it: id: is what the browser
// echoes back as Last-Event-ID, event: is the kind addEventListener keys on,
// and data: is one line of compact StreamFrame JSON. Compact marshalling is
// load-bearing — a raw newline in data: would split the frame — and
// json.Marshal both escapes newlines and sorts map keys, so the line is
// single and deterministic. It only reads e.Payload, whose backing map is
// shared with every other subscriber of this event.
//
// e.Kind is interpolated into the event: line RAW, on the assumption that
// kinds are server-controlled constants (runs/events.go) and never client
// input — a newline-bearing kind would inject arbitrary SSE fields into the
// frame. A future task that routes user-supplied kinds must sanitize them
// before they reach this codec; deliberately, the codec does not rewrite a
// kind itself, because a silently mangled constant would be a worse failure
// than an obviously bad one.
func (wr *Writer) WriteEvent(e Event) error {
	data, err := json.Marshal(StreamFrame{
		ID:         e.ID,
		RunID:      e.RunID,
		Kind:       e.Kind,
		Payload:    e.Payload,
		OccurredAt: e.OccurredAt,
	})
	if err != nil {
		return fmt.Errorf("encode stream frame %d: %w", e.ID, err)
	}
	if _, err := fmt.Fprintf(wr.w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Kind, data); err != nil {
		return fmt.Errorf("write stream frame %d: %w", e.ID, err)
	}
	if err := wr.rc.Flush(); err != nil {
		return fmt.Errorf("flush stream frame %d: %w", e.ID, err)
	}
	wr.lastWrite = wr.clock()
	return nil
}

// Heartbeat writes the SSE comment ": hb\n\n" — invisible to EventSource,
// enough to keep the tunnel open — but only when nothing has been written
// for the hb interval; otherwise it is a no-op. Serve calls it on a plain
// ticker and the Writer decides, which is what spares Serve from tracking
// last-write and resetting a ticker on every event.
func (wr *Writer) Heartbeat() error {
	if wr.clock().Sub(wr.lastWrite) < wr.hb {
		return nil
	}
	if _, err := fmt.Fprint(wr.w, ": hb\n\n"); err != nil {
		return fmt.Errorf("write heartbeat: %w", err)
	}
	if err := wr.rc.Flush(); err != nil {
		return fmt.Errorf("flush heartbeat: %w", err)
	}
	wr.lastWrite = wr.clock()
	return nil
}

// Flush pushes anything buffered to the client. WriteEvent and Heartbeat
// already flush their own bytes, so this exists for callers that write to
// the underlying ResponseWriter directly — Task 9's terminal frame.
//
// It reports nothing, and its one caller is why: the terminal frame is the
// last thing the stream ever writes, and the write itself has already been
// reported. A flush that fails after it has nothing left to change.
func (wr *Writer) Flush() { _ = wr.rc.Flush() }
