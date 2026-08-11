package realtime

import (
	"encoding/json"
	"errors"
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
	w       http.ResponseWriter
	flusher http.Flusher
	hb      time.Duration
	clock   func() time.Time
	// lastWrite is when bytes last went to the client — connect, event or
	// heartbeat. Heartbeat consults it so that a busy stream is never padded
	// with comments: the Writer owns idleness, and Serve just ticks.
	lastWrite time.Time
}

// NewWriter takes over w as an SSE stream: headers written and flushed
// immediately, before any event, because the flush is what defeats proxy
// buffering and tells the client the stream is live. See Writer for the
// consequence — the 200 is committed here.
//
// hb is the idle interval Heartbeat enforces; clock feeds it (pass time.Now
// outside tests; nil defaults to it). A w that does not implement
// http.Flusher is rejected outright: an unflushable stream buffers every
// frame and delivers none, with no symptom but silence. That is a server-side
// wiring fault, not an API error — it never crosses the boundary as itself.
func NewWriter(w http.ResponseWriter, hb time.Duration, clock func() time.Time) (*Writer, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("realtime: response writer does not implement http.Flusher")
	}
	if clock == nil {
		clock = time.Now
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	// Hop-by-hop and ignored by HTTP/2; harmless and mandated over HTTP/1.1.
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	flusher.Flush()
	return &Writer{w: w, flusher: flusher, hb: hb, clock: clock, lastWrite: clock()}, nil
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
	wr.flusher.Flush()
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
	wr.flusher.Flush()
	wr.lastWrite = wr.clock()
	return nil
}

// Flush pushes anything buffered to the client. WriteEvent and Heartbeat
// already flush their own bytes, so this exists for callers that write to
// the underlying ResponseWriter directly — Task 9's terminal frame.
func (wr *Writer) Flush() { wr.flusher.Flush() }
