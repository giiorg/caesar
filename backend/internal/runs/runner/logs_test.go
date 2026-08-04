package runner

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// logFrame is one frame of the daemon's multiplexed log stream: an 8-byte
// header then that many payload bytes (dockerapi.DemuxLogs's input shape).
func logFrame(stream byte, payload string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, payload...)
}

type logEngine struct {
	Engine // embed nil: anything but ContainerLogs panics loudly
	body   []byte
	err    error
}

func (e *logEngine) ContainerLogs(context.Context, string, bool) (io.ReadCloser, error) {
	if e.err != nil {
		return nil, e.err
	}
	return io.NopCloser(bytes.NewReader(e.body)), nil
}

type recordedEvent struct {
	Kind    string
	Payload map[string]any
}

type logSink struct {
	mu     sync.Mutex
	events []recordedEvent
}

func (s *logSink) AppendRuntimeEvent(_ context.Context, _ runs.AttemptRef, kind string, payload map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, recordedEvent{Kind: kind, Payload: payload})
	return nil
}

func (s *logSink) of(kind string) []recordedEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []recordedEvent
	for _, e := range s.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func newLogRunner(t *testing.T, eng Engine, sink EventSink, capBytes int64) *Runner {
	t.Helper()
	r, err := New(eng, nil, nil, nil, sink, Options{
		CaesarImage:   "caesar:dev",
		GitImage:      "caesar-git:dev",
		DataRoot:      t.TempDir(),
		LogCapBytes:   capBytes,
		LogFloodBytes: capBytes,
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func TestLogStreamer_CapsTheFileAndReportsTruncationOnce(t *testing.T) {
	const capBytes = 100
	var body bytes.Buffer
	var produced int64
	for i := 0; i < 5; i++ {
		out := strings.Repeat("o", 30) + "\n"
		errLine := strings.Repeat("e", 20) + "\n"
		body.Write(logFrame(1, out))
		body.Write(logFrame(2, errLine))
		produced += int64(len(out) + len(errLine))
	}

	sink := &logSink{}
	r := newLogRunner(t, &logEngine{body: body.Bytes()}, sink, capBytes)
	path := filepath.Join(t.TempDir(), "agent.log")

	s := r.streamLogs(t.Context(), "cid", path, testRef(), "agent", nil)
	n, truncated, err := s.wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !truncated {
		t.Error("a stream past the cap must report truncation")
	}
	if n != produced {
		t.Errorf("bytes = %d, want the whole produced stream %d", n, produced)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != capBytes {
		t.Errorf("file size = %d, want exactly the cap %d", fi.Size(), capBytes)
	}
	if evs := sink.of("phase.log_truncated"); len(evs) != 1 {
		t.Fatalf("phase.log_truncated fired %d times, want exactly once", len(evs))
	}
}

func TestLogStreamer_UnderTheCapIsWrittenWholeAndFiresNoEvent(t *testing.T) {
	sink := &logSink{}
	r := newLogRunner(t, &logEngine{body: logFrame(1, "hello\n")}, sink, 1<<20)
	path := filepath.Join(t.TempDir(), "agent.log")

	n, truncated, err := r.streamLogs(t.Context(), "cid", path, testRef(), "agent", nil).wait()
	if err != nil || truncated || n != 6 {
		t.Fatalf("bytes=%d truncated=%v err=%v", n, truncated, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" {
		t.Errorf("file = %q", got)
	}
	if evs := sink.of("phase.log_truncated"); len(evs) != 0 {
		t.Errorf("an uncapped stream fired %v", evs)
	}
}

func TestLogStreamer_SurfacesEgressDenials(t *testing.T) {
	// The denial line is split across frames on purpose: the daemon frames
	// by write, not by line, so reassembly is part of the contract.
	var body bytes.Buffer
	body.Write(logFrame(1, `{"time":"2026-08-04T00:00:00Z","level":"INFO","msg":"egress.start","attempt":"a"}`+"\n"))
	body.Write(logFrame(1, `{"time":"2026-08-04T00:00:01Z","level":"WARN","msg":"egress.denied","attempt":"a",`))
	body.Write(logFrame(1, `"kind":"host","host":"evil.example"}`+"\n"))
	body.Write(logFrame(1, "not json at all\n"))
	body.Write(logFrame(2, `{"msg":"egress.denied","kind":"token","host":"api.anthropic.com"}`+"\n"))

	r := newLogRunner(t, &logEngine{body: body.Bytes()}, &logSink{}, 1<<20)
	var got [][2]string
	s := r.streamLogs(t.Context(), "cid", filepath.Join(t.TempDir(), "gateway.log"), testRef(), "gateway",
		func(host, kind string) { got = append(got, [2]string{host, kind}) })
	if _, _, err := s.wait(); err != nil {
		t.Fatal(err)
	}

	want := [][2]string{{"evil.example", "host"}, {"api.anthropic.com", "token"}}
	if len(got) != len(want) {
		t.Fatalf("denials = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("denial %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestDenialReporter_EmitsThePinnedPayloadAndCapsTheEventStream(t *testing.T) {
	sink := &logSink{}
	r := newLogRunner(t, &logEngine{}, sink, 1<<20)
	report := r.denialReporter(t.Context(), testRef())
	for range maxDenialEvents + 5 {
		report("evil.example", "host")
	}

	evs := sink.of("phase.egress_denied")
	if len(evs) != maxDenialEvents {
		t.Fatalf("emitted %d denial events, want the cap of %d", len(evs), maxDenialEvents)
	}
	// The gateway logs the refusal category as "kind"; the event names it
	// "reason", which is what consumers read.
	want := map[string]any{"host": "evil.example", "reason": "host"}
	if diff := cmp.Diff(want, evs[0].Payload); diff != "" {
		t.Errorf("denial payload (-want +got):\n%s", diff)
	}
}

func TestLogStreamer_WithoutADenialCallbackScansNothing(t *testing.T) {
	// The agent's own stdout must never be able to fabricate an egress event.
	r := newLogRunner(t, &logEngine{
		body: logFrame(1, `{"msg":"egress.denied","kind":"host","host":"fabricated"}`+"\n"),
	}, &logSink{}, 1<<20)
	if _, _, err := r.streamLogs(t.Context(), "cid",
		filepath.Join(t.TempDir(), "agent.log"), testRef(), "agent", nil).wait(); err != nil {
		t.Fatal(err)
	}
}
