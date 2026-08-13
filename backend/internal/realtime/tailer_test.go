package realtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

const (
	// testPoll is the fallback interval every test here runs at: short
	// enough that a test waiting on the poll path finishes in milliseconds,
	// long enough that several ticks fall inside a settle window.
	testPoll = 5 * time.Millisecond

	// settle is how long a test waits before concluding nothing more is
	// coming. Eight poll intervals, so "nothing arrived" is a fact rather
	// than a race with the next tick.
	settle = 8 * testPoll

	// patience bounds every wait for something that should happen. It is
	// generous on purpose: these tests assert which ids arrive and by what
	// mechanism, never how fast a loaded CI box gets there.
	patience = 2 * time.Second

	// neverPolls is a fallback interval no test will ever reach. A tailer
	// configured with it delivers only on a wake-up, so every id that
	// arrives is attributable to one drain pass rather than to a later tick.
	neverPolls = time.Hour
)

// pollingCfg is what every test but the burst one runs at: a short fallback
// so the poll path resolves in milliseconds, and a batch far larger than any
// event count here so one read drains everything.
func pollingCfg() TailerConfig {
	return TailerConfig{PollFallback: testPoll, Batch: 100}
}

// fakeSource is a hand-written EventSource over an in-memory slice. It holds
// events in the order they were added rather than by id, which is what lets
// a test hand the tailer an out-of-order read.
type fakeSource struct {
	mu        sync.Mutex
	events    []Event
	sinceErrs []error
	// seeded closes when the tailer has read Head. Everything a test adds
	// afterwards is unambiguously "committed after startup"; without the
	// signal the adds race the seed and the test asserts nothing.
	seeded   chan struct{}
	seedOnce sync.Once
}

var _ EventSource = (*fakeSource)(nil)

// newSource holds ids as already-committed history.
func newSource(ids ...int64) *fakeSource {
	s := &fakeSource{seeded: make(chan struct{})}
	s.add(ids...)
	return s
}

func (s *fakeSource) add(ids ...int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		s.events = append(s.events, Event{
			ID:        id,
			RunID:     uuid.New(),
			ProjectID: uuid.New(),
			Kind:      "run.started",
		})
	}
}

// failSinceOnce queues an error for the next Since call. Later calls succeed.
func (s *fakeSource) failSinceOnce(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sinceErrs = append(s.sinceErrs, err)
}

func (s *fakeSource) Head(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.seedOnce.Do(func() { close(s.seeded) })
	var head int64
	for _, e := range s.events {
		head = max(head, e.ID)
	}
	return head, nil
}

func (s *fakeSource) Since(_ context.Context, after int64, limit int) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sinceErrs) > 0 {
		err := s.sinceErrs[0]
		s.sinceErrs = s.sinceErrs[1:]
		return nil, err
	}
	var out []Event
	for _, e := range s.events {
		if e.ID <= after {
			continue
		}
		out = append(out, e)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// The tailer touches neither of these; a fake that answered them would be
// inviting a future test to assert against invented behaviour.
func (s *fakeSource) Snapshot(context.Context, Filter) (Snapshot, error) {
	return Snapshot{}, xerrors.NotImplemented("fakeSource.Snapshot")
}

func (s *fakeSource) Replay(context.Context, Filter, int64, int) ([]Event, error) {
	return nil, xerrors.NotImplemented("fakeSource.Replay")
}

func (s *fakeSource) Oldest(context.Context) (int64, error) {
	return 0, xerrors.NotImplemented("fakeSource.Oldest")
}

// fakeWaker stands in for the LISTEN connection. Wait returns nil only when
// a test signals, so whatever a test that never signals observes came from
// the poll path.
type fakeWaker struct{ ch chan struct{} }

func newWaker() *fakeWaker { return &fakeWaker{ch: make(chan struct{}, 1)} }

// wake delivers one notification, dropping it if one is already pending —
// the real signal is coalescing too, and carries no payload.
func (w *fakeWaker) wake() {
	select {
	case w.ch <- struct{}{}:
	default:
	}
}

func (w *fakeWaker) Wait(ctx context.Context) error {
	select {
	case <-w.ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// outageWaker is the LISTEN connection DOWN: Wait fails immediately, which is
// the shape pgWaker takes when it cannot dial or its connection has just
// dropped (app/realtimeports.go). fakeWaker cannot express that — its Wait
// only ever answers a signal or the deadline — and teaching it to would let a
// test script mixtures nothing produces, so this is a second waker rather than
// a taught one.
//
// Healed, it blocks until the caller's deadline and returns that, because a
// live LISTEN over a quiet database returns nothing else.
type outageWaker struct {
	mu    sync.Mutex
	err   error
	waits int
}

var _ Waker = (*outageWaker)(nil)

func newOutageWaker(err error) *outageWaker { return &outageWaker{err: err} }

func (w *outageWaker) Wait(ctx context.Context) error {
	w.mu.Lock()
	w.waits++
	err := w.err
	w.mu.Unlock()
	if err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

// setErr breaks or heals the waker while the tailer is running. A nil err is
// healthy.
func (w *outageWaker) setErr(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.err = err
}

// count is how many waits have STARTED. Counting on entry is what makes
// "count reached n+2" mean a wait that began after a heal has also returned,
// since Run waits one at a time.
func (w *outageWaker) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.waits
}

// wakeLines counts realtime.tailer.wake lines at one level, read out of the
// TextHandler output these tests already capture — so the level asserted is
// the one the handler wrote rather than one inferred from the code.
func wakeLines(lb *logBuf, level slog.Level) int {
	n := 0
	for _, line := range strings.Split(lb.String(), "\n") {
		if strings.Contains(line, "level="+level.String()) &&
			strings.Contains(line, "msg=realtime.tailer.wake") {
			n++
		}
	}
	return n
}

// collector is the hub's stand-in. It records ids, because what the tailer
// promises is which ids reach the sink in which order.
type collector struct {
	mu      sync.Mutex
	batches [][]int64
}

func (c *collector) sink(evs []Event) {
	ids := make([]int64, len(evs))
	for i, e := range evs {
		ids[i] = e.ID
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batches = append(c.batches, ids)
}

// ids is every id handed to the sink in delivery order, batches flattened.
func (c *collector) ids() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []int64
	for _, b := range c.batches {
		out = append(out, b...)
	}
	return out
}

// batchCount is how many times the sink was called. It is what separates
// "everything arrived" from "everything arrived in one read".
func (c *collector) batchCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.batches)
}

// logBuf captures the tailer's output so a test can assert a fault was
// reported, and so nothing leaks into the test run's own output.
type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// assertQuiet fails if the tailer logged anything. Following the table is a
// silent activity: output means a fault the test did not ask for, or noise
// on the shutdown path.
func assertQuiet(t *testing.T, lb *logBuf) {
	t.Helper()
	if s := lb.String(); s != "" {
		t.Errorf("tailer logged on a path that should be silent:\n%s", s)
	}
}

// eventually waits for cond, failing the test if patience runs out.
func eventually(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", patience, what)
}

// startTailer runs a tailer in the background and hands back its cancel and
// the channel its Run error lands on. It returns once the startup cursor has
// been seeded, so a test may add events straight away and know they count as
// having committed after startup.
//
// Cleanup cancels and waits, so every test here also asserts that its tailer
// shuts down.
func startTailer(t *testing.T, src *fakeSource, w Waker, sink func([]Event), cfg TailerConfig, log *slog.Logger) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	stopped := make(chan struct{})
	tailer := NewTailer(src, w, sink, cfg, log)
	go func() {
		defer close(stopped)
		errc <- tailer.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(patience):
			t.Errorf("Run did not return within %s of cancellation", patience)
		}
	})
	select {
	case <-src.seeded:
	case <-time.After(patience):
		t.Fatalf("tailer did not seed its cursor within %s", patience)
	}
	return cancel, errc
}

// A fresh process must not replay the table into the hub: subscribers get
// history from their own snapshot, and the hub has no subscriber older than
// the process. So the startup cursor is max(id), never 0.
func TestTailer_StartsAtHead(t *testing.T) {
	t.Parallel()
	src := newSource(1, 2, 3, 4, 5)
	w, c, lb := newWaker(), &collector{}, &logBuf{}
	startTailer(t, src, w, c.sink, pollingCfg(), newLogger(lb))

	// Several poll intervals, plus a wake-up to remove any doubt that the
	// tailer looked: a cursor seeded at 0 would have replayed 1..5 by now.
	w.wake()
	time.Sleep(settle)
	if got := c.ids(); len(got) != 0 {
		t.Fatalf("fresh tailer replayed history: delivered %v, want nothing", got)
	}

	// What commits after startup is still followed.
	src.add(6)
	w.wake()
	eventually(t, func() bool { return len(c.ids()) > 0 }, "id 6")
	time.Sleep(settle)
	if diff := cmp.Diff([]int64{6}, c.ids()); diff != "" {
		t.Errorf("delivered ids (-want +got):\n%s", diff)
	}
	assertQuiet(t, lb)
}

func TestTailer_DeliversInIDOrder(t *testing.T) {
	t.Parallel()
	src := newSource(10)
	w, c, lb := newWaker(), &collector{}, &logBuf{}
	startTailer(t, src, w, c.sink, pollingCfg(), newLogger(lb))

	// Deliberately not ascending, and deliberately not ending on the
	// highest id: a cursor taken from the last row read rather than from
	// the highest one delivered would land on 11 and re-deliver 12 and 13.
	src.add(12, 13, 11)
	w.wake()
	eventually(t, func() bool { return len(c.ids()) >= 3 }, "ids 11..13")
	time.Sleep(settle)
	if diff := cmp.Diff([]int64{11, 12, 13}, c.ids()); diff != "" {
		t.Errorf("delivered ids (-want +got):\n%s", diff)
	}

	// The cursor ended at 13: what follows is 14 alone.
	src.add(14)
	w.wake()
	eventually(t, func() bool { return len(c.ids()) >= 4 }, "id 14")
	time.Sleep(settle)
	if diff := cmp.Diff([]int64{11, 12, 13, 14}, c.ids()); diff != "" {
		t.Errorf("delivered ids after the second batch (-want +got):\n%s", diff)
	}
	assertQuiet(t, lb)
}

// A burst larger than Batch must drain within a single wake-up. If drain
// read only once per tick, a backlog would trickle out one batch per
// PollFallback — 2s each at the default — with nothing logged to say why the
// stream was falling behind.
//
// The tailer here polls only once an hour, so the poll path cannot rescue a
// partial drain: every id below arrived because one drain pass kept reading.
func TestTailer_DrainsBurstLargerThanBatch(t *testing.T) {
	t.Parallel()
	src := newSource(1)
	w, c, lb := newWaker(), &collector{}, &logBuf{}
	startTailer(t, src, w, c.sink, TailerConfig{PollFallback: neverPolls, Batch: 2}, newLogger(lb))

	// One atomic add: all five are visible to whichever read comes next, so
	// nothing here depends on the burst being sliced up by arrival timing.
	src.add(2, 3, 4, 5, 6)
	w.wake()
	eventually(t, func() bool { return len(c.ids()) >= 5 }, "the whole burst")
	time.Sleep(settle)

	// In order, and each exactly once — a cursor advanced per batch must not
	// re-read what it already delivered.
	if diff := cmp.Diff([]int64{2, 3, 4, 5, 6}, c.ids()); diff != "" {
		t.Errorf("delivered ids (-want +got):\n%s", diff)
	}
	// Five events at Batch 2 is 3 reads. The count is what proves the batch
	// limit was real and the loop is what got past it, rather than one
	// oversized read making the whole test vacuous.
	if got := c.batchCount(); got < 2 {
		t.Errorf("sink called %d time(s); want the burst split across several batches", got)
	}
	assertQuiet(t, lb)
}

// LISTEN/NOTIFY is at-most-once and non-durable, so a dropped notification
// must cost latency and never an event. This waker is never signalled: the
// delivery below can only have come from the poll fallback.
func TestTailer_PollsWithoutNotification(t *testing.T) {
	t.Parallel()
	src := newSource(1)
	w, c, lb := newWaker(), &collector{}, &logBuf{}
	startTailer(t, src, w, c.sink, pollingCfg(), newLogger(lb))

	src.add(2)
	eventually(t, func() bool { return len(c.ids()) > 0 }, "id 2 to arrive without any notification")
	if diff := cmp.Diff([]int64{2}, c.ids()); diff != "" {
		t.Errorf("delivered ids (-want +got):\n%s", diff)
	}
	assertQuiet(t, lb)
}

// A transient read failure costs one tick. If it ended the goroutine instead,
// a single DB blip would silently kill realtime for the process lifetime.
func TestTailer_SurvivesSourceError(t *testing.T) {
	t.Parallel()
	src := newSource(1)
	src.failSinceOnce(errors.New("connection reset by peer"))
	w, c, lb := newWaker(), &collector{}, &logBuf{}
	_, errc := startTailer(t, src, w, c.sink, pollingCfg(), newLogger(lb))

	src.add(2)
	eventually(t, func() bool { return len(c.ids()) > 0 }, "id 2 after a failed Since")
	time.Sleep(settle)
	if diff := cmp.Diff([]int64{2}, c.ids()); diff != "" {
		t.Errorf("delivered ids (-want +got):\n%s", diff)
	}

	// Reported, not swallowed.
	if s := lb.String(); !strings.Contains(s, "connection reset by peer") {
		t.Errorf("the failed read was not logged; log was:\n%s", s)
	}

	// And still running.
	select {
	case err := <-errc:
		t.Fatalf("Run returned on a transient Since error: %v", err)
	default:
	}
}

// A sustained LISTEN outage must be reported ONCE rather than once per poll
// interval: at the 2s default that is ~1800 identical lines an hour, and they
// bury the lines that matter during exactly the incident someone is reading
// the log for. The condition is already being handled correctly meanwhile —
// the poll fallback is what carries correctness — so the first line is the
// news and the rest only repeat it. They still have to be THERE, at Debug, and
// a later outage has to be news again.
func TestTailer_ReportsAWakerOutageOnceUntilItRecovers(t *testing.T) {
	t.Parallel()
	src := newSource(1)
	w := newOutageWaker(errors.New("listen connection dropped"))
	c, lb := &collector{}, &logBuf{}
	startTailer(t, src, w, c.sink, pollingCfg(), newLogger(lb))

	// Several intervals of a waker that fails every time. The wait is on the
	// TOTAL, at either level, so that a build which warns every interval gets
	// here and fails the split below rather than timing out on a Debug line it
	// never writes.
	eventually(t, func() bool {
		return wakeLines(lb, slog.LevelWarn)+wakeLines(lb, slog.LevelDebug) >= 3
	}, "the outage to be reported several times")
	if got := wakeLines(lb, slog.LevelWarn); got != 1 {
		t.Errorf("%d Warn lines during ONE outage, want exactly 1:\n%s", got, lb.String())
	}
	// And the repeats are still recorded rather than dropped: an outage that
	// stops being visible entirely is the opposite failure.
	if got := wakeLines(lb, slog.LevelDebug); got < 2 {
		t.Errorf("%d Debug lines during ONE outage, want the repeats at Debug:\n%s", got, lb.String())
	}
	// The waker's own error is on the line, not merely the fact of a failure.
	if s := lb.String(); !strings.Contains(s, "listen connection dropped") {
		t.Errorf("the waker's error was not logged; log was:\n%s", s)
	}

	// Healed. A wait that STARTS after this cannot fault, and its return is
	// what ends the run of failures.
	w.setErr(nil)
	started := w.count()
	eventually(t, func() bool { return w.count() >= started+2 }, "a wait that did not fault")
	if got := wakeLines(lb, slog.LevelWarn); got != 1 {
		t.Fatalf("%d Warn lines before the second outage, want 1:\n%s", got, lb.String())
	}

	// And the next outage is news again rather than another Debug line.
	w.setErr(errors.New("listen connection dropped again"))
	eventually(t, func() bool { return wakeLines(lb, slog.LevelWarn) > 1 }, "the second outage to warn")
	if got := wakeLines(lb, slog.LevelWarn); got != 2 {
		t.Errorf("%d Warn lines across TWO outages, want exactly 2:\n%s", got, lb.String())
	}
}

func TestTailer_StopsOnContextCancel(t *testing.T) {
	t.Parallel()
	src := newSource(1)
	w, c, lb := newWaker(), &collector{}, &logBuf{}
	cancel, errc := startTailer(t, src, w, c.sink, pollingCfg(), newLogger(lb))

	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(patience):
		t.Fatalf("Run did not return within %s of cancellation", patience)
	}

	// Nothing the tailer left behind is still following the table: events
	// and a wake-up after Run returned reach no sink.
	src.add(2, 3)
	w.wake()
	time.Sleep(settle)
	if got := c.ids(); len(got) != 0 {
		t.Errorf("sink called after Run returned: %v", got)
	}
	assertQuiet(t, lb)
}
