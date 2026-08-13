package realtime

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"
)

const (
	// defaultPollFallback is the interval a zero TailerConfig follows the
	// table at. A zero value here would be a hot loop, so the config has a
	// floor rather than an honest zero.
	//
	// It repeats config.Realtime.PollFallback's default; defaults_test.go
	// keeps the two equal.
	defaultPollFallback = 2 * time.Second

	// defaultBatch caps one read. A zero value would ask the source for
	// nothing at all and follow the table forever without moving.
	//
	// Unlike every other default here it repeats NOTHING: there is no
	// config.Realtime knob for it. It is the tailer's own read size, shared
	// by the whole process, and config.Realtime.ReplayBatch — which happens
	// to be 500 too — is one CLIENT's catch-up page. Wiring one into the
	// other would look right and tie two unrelated knobs to one number.
	defaultBatch = 500
)

// Waker reports database wake-ups. The pg implementation lives in app;
// a channel fake stands in for it in tests.
type Waker interface {
	// Wait blocks until a notification arrives, ctx is done, or the
	// implementation decides to return early. A nil error means "maybe
	// something happened" — never "definitely".
	Wait(ctx context.Context) error
}

// TailerConfig tunes the follow loop. Both fields have defaults; a zero
// config is usable rather than broken.
type TailerConfig struct {
	// PollFallback is how long the tailer waits for a wake-up before
	// reading anyway. It is the worst-case latency of a lost notification.
	PollFallback time.Duration
	// Batch caps one read of the table.
	Batch int
}

// Tailer is the single goroutine that follows run_events for a process.
//
// It never blocks on a subscriber: sink is non-blocking by contract (the hub
// drops into bounded per-subscriber queues rather than waiting), and this
// loop depends on that. A sink that blocks stalls every subscriber's stream,
// not just its own.
type Tailer struct {
	src    EventSource
	waker  Waker
	sink   func([]Event)
	cfg    TailerConfig
	log    *slog.Logger
	cursor int64
	// wakeFailed is whether the CURRENT run of waker failures has already
	// been reported. Only Run's goroutine touches it, so it is not locked.
	// See wait for why a run is reported once rather than per interval.
	wakeFailed bool
}

// NewTailer builds the follow loop. sink must not block — see Tailer.
func NewTailer(src EventSource, w Waker, sink func([]Event), cfg TailerConfig, log *slog.Logger) *Tailer {
	if cfg.PollFallback <= 0 {
		cfg.PollFallback = defaultPollFallback
	}
	if cfg.Batch <= 0 {
		cfg.Batch = defaultBatch
	}
	return &Tailer{src: src, waker: w, sink: sink, cfg: cfg, log: log}
}

// Run follows the table until ctx is cancelled, returning ctx.Err().
//
// The cursor is seeded at the current head, not at 0: subscribers get
// history from their own snapshot, so a restarting process replaying the
// table would only duplicate it into every open stream. A head that cannot
// be read is therefore fatal — there is no safe value to guess — and the
// caller should treat it as a failed startup.
//
// The loop is: drain everything past the cursor, then wait for a wake-up or
// the poll deadline, whichever comes first. Draining is unconditional and
// never trusts the wake-up for anything but timing: LISTEN/NOTIFY is
// at-most-once and non-durable, so a lost notification costs one poll
// interval of latency and never an event.
func (t *Tailer) Run(ctx context.Context) error {
	head, err := t.src.Head(ctx)
	if err != nil {
		return fmt.Errorf("seed tailer cursor: %w", err)
	}
	t.cursor = head
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		t.drain(ctx)
		t.wait(ctx)
	}
}

// drain hands the sink everything past the cursor, in batches, until the
// source is caught up. It loops while a full batch comes back so a burst
// larger than Batch is not left waiting for the next tick.
//
// A read that fails is logged and abandoned until the next tick. It must
// never end the loop: a transient blip would otherwise take realtime down
// for the rest of the process lifetime.
func (t *Tailer) drain(ctx context.Context) {
	for ctx.Err() == nil {
		batch, err := t.src.Since(ctx, t.cursor, t.cfg.Batch)
		if err != nil {
			// A cancelled context is shutdown, not a fault.
			if ctx.Err() == nil {
				t.log.Error("realtime.tailer.since", "cursor", t.cursor, "error", err)
			}
			return
		}
		if len(batch) == 0 {
			return
		}
		// Normalise the order the hub sees. This is delivery order ONLY and
		// is NOT a defence against a source that violates Since's
		// oldest-first contract: it runs after the source already truncated
		// at limit, so sorting a [22 21 11] read moves the cursor to 22 and
		// strands 12..20 forever, where leaving it alone would have stopped
		// at 11 and recovered them on the next pass. What actually makes the
		// advance gapless is the ORDER BY id in the EventSource
		// implementation.
		slices.SortFunc(batch, func(a, b Event) int { return cmp.Compare(a.ID, b.ID) })
		t.sink(batch)
		// Only to the highest id actually handed over.
		t.cursor = batch[len(batch)-1].ID
		if len(batch) < t.cfg.Batch {
			return
		}
	}
}

// wait blocks until the waker reports something, the poll deadline passes,
// or ctx is done. The deadline is what makes the poll unconditional: the
// waker is free to block forever on a healthy-looking but silent connection.
func (t *Tailer) wait(ctx context.Context) {
	waitCtx, cancel := context.WithTimeout(ctx, t.cfg.PollFallback)
	defer cancel()
	err := t.waker.Wait(waitCtx)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		// The wake-up path is working: either a notification arrived, or the
		// waker spent the whole interval blocked on it. A LATER failure is
		// news again, so end the run.
		//
		// The deadline counts as working, not only the notification: a healthy
		// but quiet database returns nothing else for as long as it stays
		// quiet, so resetting on nil alone would leave the flag set forever
		// after one outage and log every future outage at Debug.
		t.wakeFailed = false
		return
	}
	if ctx.Err() != nil {
		return
	}
	// A waker that fails immediately — a dropped LISTEN connection — would
	// spin this loop, so serve out the rest of the interval. Reads keep
	// happening meanwhile; only the wake-up is degraded.
	//
	// One Warn per RUN of failures, then Debug. At the 2s default a sustained
	// outage writes ~1800 identical lines an hour, which buries the lines that
	// matter during exactly the incident someone is reading the log for — and
	// the condition is being handled: the poll fallback is what carries
	// correctness while the wake-up is down (see PollFallback). The first line
	// is what says the outage started; the rest only say it has not ended.
	if t.wakeFailed {
		t.log.Debug("realtime.tailer.wake", "error", err)
	} else {
		t.wakeFailed = true
		t.log.Warn("realtime.tailer.wake", "error", err)
	}
	<-waitCtx.Done()
}
