package realtime

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
)

const (
	// defaultHeartbeatInterval, defaultReplayBatch and defaultGrantRecheck are
	// config.Realtime's defaults, repeated here because a zero StreamerConfig
	// must be usable rather than broken — the same ruling NewTailer and NewHub
	// follow. What each zero costs is spelled out on NewStreamer.
	defaultHeartbeatInterval = 20 * time.Second
	defaultReplayBatch       = 500
	defaultGrantRecheck      = 30 * time.Second
)

// maxGrantFailures is how many CONSECUTIVE re-check failures a stream tolerates
// before it ends. Any single failure keeps the last known scope and retries,
// because a database hiccup is not a permissions change and closing on one
// would end every open stream in the process at once. But a resolver that
// never answers leaves a stream running on a scope nobody can confirm, which is
// the fail-open the re-check exists to close — so the tolerance is bounded.
// Three, at the 30s default, is a 90-second outage: a sustained fault rather
// than a blip.
const maxGrantFailures = 3

// forbiddenFrame is the terminal frame a stream ends with when its principal
// may no longer see it. NewWriter committed the 200 before any of this could be
// known (see Serve), so a 403 status is no longer available and these bytes are
// what say so instead.
//
// It carries NO id: line, deliberately. Ids on the wire are the client's resume
// point (see stream.cursor); this is not an event, and a client that adopted it
// would store a position that never named one. The event name cannot collide
// with a real kind — every kind runs/events.go defines is a dotted namespace —
// and data: stays one compact JSON line, because a raw newline would split the
// frame (sse.go). These bytes are wire contract alongside StreamFrame rather
// than an implementation detail, and Task 10 documents both.
const forbiddenFrame = "event: forbidden\ndata: {\"error\":\"forbidden\"}\n\n"

// closeClientGone is the reason Serve's own deferred Close carries. It is
// unexported and never observed by anyone: a subscription does not outlive
// the Serve call that made it, and Err is read before the defer runs. It
// exists so the tidy-up need not claim CloseShutdown — "hub shut down" — on a
// client that merely closed its tab.
const closeClientGone CloseReason = "client disconnected"

// StreamerConfig tunes a stream's liveness. A zero config is usable, and the
// field names match config.Realtime, which Task 11 wires straight in.
type StreamerConfig struct {
	// HeartbeatInterval is how often Serve ticks the Writer. The Writer
	// decides whether a tick becomes bytes, so this is an upper bound on
	// idleness rather than a comment rate.
	HeartbeatInterval time.Duration
	// ReplayBatch caps one catch-up page.
	ReplayBatch int
	// GrantRecheck is how often a live stream re-resolves its principal's
	// scope, so a grant revoked after the stream opened still reaches it. It
	// is therefore also the worst-case delay before a revocation takes effect.
	GrantRecheck time.Duration
}

// Streamer serves SSE streams over a shared hub and event source. It holds no
// per-request state — Serve owns all of it — so one Streamer serves every
// stream in the process.
type Streamer struct {
	hub    *Hub
	src    EventSource
	grants GrantResolver
	cfg    StreamerConfig
}

// NewStreamer builds the splice, defaulting a non-positive config.
//
// A nil GrantResolver PANICS. A Streamer that cannot re-check grants never
// closes a revoked stream — the exact fail-open follow's re-check exists to
// close — and it fails silently, as a stream that simply goes on working; the
// nil interface would otherwise panic once per request, inside an HTTP handler
// goroutine, instead of once at process start. A programming error no correct
// caller can hit is not worth turning this into a two-value constructor for.
//
// Every zero fails silently rather than loudly, which is why none is left
// honest. A zero HeartbeatInterval or GrantRecheck PANICS time.NewTicker on the
// first request served. A non-positive ReplayBatch reaches Replay, which
// answers it with an empty page (ports.go) — and a pager cannot tell an empty
// page apart from "no more events", so the stream would open, write its
// snapshot and then never catch up, with nothing logged anywhere to say so.
func NewStreamer(hub *Hub, src EventSource, grants GrantResolver, cfg StreamerConfig) *Streamer {
	if grants == nil {
		panic("realtime: NewStreamer requires a GrantResolver")
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = defaultHeartbeatInterval
	}
	if cfg.ReplayBatch <= 0 {
		cfg.ReplayBatch = defaultReplayBatch
	}
	if cfg.GrantRecheck <= 0 {
		cfg.GrantRecheck = defaultGrantRecheck
	}
	return &Streamer{hub: hub, src: src, grants: grants, cfg: cfg}
}

// StreamRequest is one client's ask. Both HTTP surfaces (Task 10) build it.
type StreamRequest struct {
	// Filter is what this subscription may see. It carries the authorization
	// scope, and every path — snapshot, replay and live — funnels through
	// Filter.Matches.
	Filter Filter
	// UserID is the principal whose grants follow re-resolves every
	// GrantRecheck for the life of the stream, so a grant revoked after the
	// stream opened still closes it.
	UserID uuid.UUID
	// LastEventID is the client's resume point and HasLastID is whether the
	// header was present at all — 0 is a legitimate id-less value, not a
	// sentinel.
	LastEventID int64
	HasLastID   bool
}

// Serve streams to w until the client disconnects, the subscription ends or a
// write fails. It returns:
//
//   - nil when ctx is done, WHEREVER the stream had got to. A disconnect is
//     how an SSE request normally ends, and Task 10 must not log every closed
//     tab as a fault.
//   - the subscription's Err when its channel closes: CloseSlowConsumer if
//     this client fell behind, CloseShutdown if the process is going away.
//   - CloseForbidden when a grant re-check finds this principal may no longer
//     see the stream. The terminal frame has already gone out by then.
//   - the write or source error otherwise — including a re-check that failed
//     often enough to end the stream, which is a fault and not a denial.
//
// Only the first error can still become an HTTP status. Once NewWriter has
// returned, the response is committed 200 forever, which is why Tasks 9 and
// 10 speak of a "403-shaped terminal frame" rather than a 403.
func (s *Streamer) Serve(ctx context.Context, w http.ResponseWriter, req StreamRequest) error {
	// The Writer FIRST. It is the one failure that can happen before the 200
	// is committed, it reads no events — so building it first cannot miss one
	// — and it spares the rejection path a subscription to unwind. The
	// invariant this design needs is Subscribe strictly before Snapshot,
	// below, and that is what the ordering test pins.
	wr, err := NewWriter(w, s.cfg.HeartbeatInterval, nil)
	if err != nil {
		return err
	}

	// SUBSCRIBE BEFORE READING ANYTHING. From this instant every matching
	// event is being queued for this client, so an event that commits while
	// the snapshot and replay below are running waits in the queue instead of
	// vanishing. Snapshotting first — the tidier-looking order, and the one
	// the next reader's instinct will be to "clean up" to — drops exactly
	// those events, silently, and only under load. The overlap subscribing
	// early creates is harmless: the cursor below discards anything the
	// client has already been sent.
	sub := s.hub.Subscribe(req.Filter)
	// On EVERY return path, or the registry grows by one entry per request
	// served — the one leak the hub cannot prevent (hub.go). Close is
	// idempotent and the FIRST reason wins, so this cannot overwrite the
	// CloseSlowConsumer of a subscription that was already evicted.
	defer sub.Close(closeClientGone)

	err = s.deliver(ctx, &stream{wr: wr}, sub, req)
	// ONE translation point for the whole stream rather than a check per
	// phase, because the question is not "which phase failed" but "is there
	// still a client to serve": once the answer is no, there is nobody left to
	// report anything to. Catch-up is expected to be long here — see replay —
	// so a tab closed mid-snapshot or mid-replay is an ordinary event, and
	// without this it returns a wrapped context.Canceled that Task 10 would
	// log at error level for every closed tab.
	//
	// The test is ctx, NOT errors.Is(err, context.Canceled): a source is free
	// to translate a cancelled query into its own error class, and the
	// disconnect is knowable here without guessing which one it picked.
	//
	// The accepted cost, stated rather than silent: a genuine source or write
	// failure that RACES a disconnect is reported as nil. That is the right
	// trade — the request is over either way, and a real outage is visible on
	// every other stream and in the tailer. It does not extend to a failure
	// with the client still connected: that one can be a real fault and stays
	// an error.
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// deliver is the three phases of a stream: the client's starting state, the
// catch-up to now, then live. It is split out of Serve for one reason — every
// way out of it passes through the disconnect translation above, including
// the phase Task 9 adds.
func (s *Streamer) deliver(ctx context.Context, st *stream, sub *Subscription, req StreamRequest) error {
	if err := s.prime(ctx, st, req); err != nil {
		return err
	}
	if err := s.replay(ctx, st, req.Filter); err != nil {
		return err
	}
	return s.follow(ctx, st, sub, req)
}

// stream is one request's writing state: the Writer, and the cursor that
// makes the snapshot/live overlap harmless.
type stream struct {
	wr *Writer
	// cursor is the highest event id this client has been told about. It is
	// deliberately ONE value serving as both the replay cursor and the live
	// dedup floor: max(starting position, highest id written).
	//
	// It starts at the snapshot's watermark, or at the client's
	// Last-Event-ID, rather than at zero. "Highest id written" alone collapses
	// to zero whenever nothing was written — an empty snapshot, or a resume
	// that was already caught up — and every queued event would then be
	// written, including ones at or below the position the client already
	// holds.
	//
	// The invariant that buys, and the reason it is the right one: IDS
	// REACHING THE WIRE ARE STRICTLY ASCENDING. That is precisely what makes
	// Last-Event-ID resume correct — the id a client stores must always be the
	// high-water mark of everything it has seen.
	cursor int64
}

// send writes e unless the client already has it, and advances the cursor.
func (st *stream) send(e Event) error {
	if e.ID <= st.cursor {
		return nil
	}
	if err := st.wr.WriteEvent(e); err != nil {
		return err
	}
	st.cursor = e.ID
	return nil
}

// writeForbidden puts the terminal frame on the wire and reports only whether
// that write itself failed. What the STREAM then returns is the caller's to
// decide: a revocation and a resolver outage look identical to the client —
// the stream is over either way — and must not look identical in the logs.
//
// It writes to the ResponseWriter directly and flushes by hand. That is what
// Writer.Flush was built for (sse.go) and this is its only caller; the frame is
// stream policy rather than codec, so it does not become a Writer method.
func (st *stream) writeForbidden() error {
	if _, err := fmt.Fprint(st.wr.w, forbiddenFrame); err != nil {
		return fmt.Errorf("write forbidden frame: %w", err)
	}
	st.wr.Flush()
	return nil
}

// prime writes the client's starting state — a fresh snapshot, or nothing at
// all for a resume whose tail is still retained — and leaves st.cursor at the
// point replay must continue from.
func (s *Streamer) prime(ctx context.Context, st *stream, req StreamRequest) error {
	if req.HasLastID {
		oldest, err := s.src.Oldest(ctx)
		if err != nil {
			// The phase, not the port: an EventSource names itself and what
			// it was doing (app: realtime oldest event: …), so repeating the
			// noun here would only stutter.
			return fmt.Errorf("stream prime: %w", err)
		}
		// Deliberately conservative by one. A lossless resume only needs
		// everything above LastEventID to still be retained — LastEventID+1
		// >= oldest — so LastEventID == oldest-1 re-snapshots when it did not
		// have to. That errs toward one duplicated backlog rather than toward
		// a gap, the same degradation direction Task 7 chose for its read
		// order, and it is the direction to keep. Nothing prunes run_events
		// today, so the else branch is unreachable in production.
		if req.LastEventID >= oldest {
			st.cursor = req.LastEventID
			return nil
		}
	}
	snap, err := s.src.Snapshot(ctx, req.Filter)
	if err != nil {
		return fmt.Errorf("stream prime: %w", err)
	}
	// Each event is its own frame. The snapshot is the most recent N matching
	// events, ascending, for a per-run stream and for the attention stream
	// alike — not a projection — so the client parses one shape all stream.
	for _, e := range snap.Events {
		if err := st.send(e); err != nil {
			return err
		}
	}
	// max(watermark, highest id written). The watermark is what the snapshot
	// is consistent up to; taking the maximum also keeps a snapshot event
	// above the watermark — the only direction Task 7's read order allows —
	// from being replayed straight back to the client.
	st.cursor = max(st.cursor, snap.Watermark.EventID)
	return nil
}

// replay pages from the cursor up to the present, writing each event.
//
// A long replay keeps the live queue filling and may get this subscriber
// evicted as a slow consumer. That is the design working rather than a bug:
// the client reconnects with Last-Event-ID and makes progress each time.
func (s *Streamer) replay(ctx context.Context, st *stream, f Filter) error {
	for {
		// Always the configured batch — never a bare literal, and never the
		// zero that would make every page look like "caught up".
		page, err := s.src.Replay(ctx, f, st.cursor, s.cfg.ReplayBatch)
		if err != nil {
			return fmt.Errorf("stream replay from %d: %w", st.cursor, err)
		}
		before := st.cursor
		for _, e := range page {
			if err := st.send(e); err != nil {
				return err
			}
		}
		// A short page is the caught-up signal. Every filtering predicate is
		// in SQL (Task 7), so a non-empty page never comes back empty after
		// filtering and the pager cannot stop early on a working source.
		if len(page) < s.cfg.ReplayBatch {
			return nil
		}
		// And a full page that did not advance the cursor would loop forever
		// on the same rows. A broken source should end a stream, not pin a
		// CPU with no request in flight to explain it.
		if st.cursor <= before {
			return nil
		}
	}
}

// follow writes live events until the client goes away, the subscription ends,
// a write fails, or the principal's grants stop covering this stream.
//
// It flushes nothing and tracks no idleness: WriteEvent and Heartbeat flush
// their own bytes (a tested guarantee, sse_test.go), and the Writer holds the
// last-write time, so this loop runs a plain never-reset ticker and lets the
// Writer no-op a tick on a busy stream. The cost of never resetting is that an
// idle stream's first comment lands somewhere in [hb, 2*hb) after the last
// write — intended, and fine at the 20s default; anyone tightening a proxy
// idle timeout below 2*hb needs to meet that fact here.
//
// Grants are re-resolved on a SECOND ticker and never per event: the per-event
// work is a pure in-memory scope test, so a busy stream costs no database round
// trips at all. The obvious objection — that a busy stream would then never get
// round to re-checking — does not hold: select picks uniformly at random among
// ready cases, so a saturated event channel cannot starve the ticker.
//
// The refreshed scope is NOT pushed back into the subscription's filter.
// Hub.Publish reads that field on the tailer goroutine while holding no
// subscription lock (hub.go), so writing it here is an unsynchronized
// concurrent write — a data race, whatever the detector happens to observe on
// any one run. The consequence, stated rather than left to be discovered:
// events the refreshed scope denies still occupy queue slots and can still get
// this client evicted as a slow consumer. Accepted — a filter mutator needs
// hub-side locking and buys nothing this task needs.
func (s *Streamer) follow(ctx context.Context, st *stream, sub *Subscription, req StreamRequest) error {
	tick := time.NewTicker(s.cfg.HeartbeatInterval)
	defer tick.Stop()
	recheck := time.NewTicker(s.cfg.GrantRecheck)
	defer recheck.Stop()

	// The last scope known to be real. It starts at the Filter's own rather
	// than at a resolve of its own: the handler resolved exactly this scope
	// moments ago to build the Filter, so an eager re-resolve would put a
	// database round-trip on every stream open to re-learn what it was just
	// told. The first tick is the earliest anything can have changed.
	scope := req.Filter.Scope
	// Consecutive re-check failures. Reset on any success — see
	// maxGrantFailures for why the cap counts a run rather than a lifetime.
	failures := 0

	for {
		select {
		case <-ctx.Done():
			// The normal end of an SSE request. See Serve.
			return nil
		case e, ok := <-sub.C():
			if !ok {
				// Err is meaningful only now that the channel has closed.
				return sub.Err()
			}
			// The scope alone, NOT Filter.Matches. The hub already applied an
			// identical selector and the kind prefixes on the way in, and the
			// scope is the only part that can have changed since; re-running
			// Matches would conflate "this principal may no longer see the
			// project" with "the selector rejects it", and the two have
			// opposite outcomes right below.
			if !scope.allows(e.ProjectID) {
				// A per-run stream's entire subject just left the principal's
				// grants, so there is nothing left for it to show: end it.
				//
				// This is the only close an event can trigger, and it is also
				// the only form the rule can take. "The scope no longer covers
				// this run's project" is not computable without an event: a
				// Filter carries a RunID and never that run's project, and an
				// admin's per-run Filter carries no project id at all.
				if req.Filter.RunID != nil {
					if err := st.writeForbidden(); err != nil {
						return err
					}
					return CloseForbidden
				}
				// An attention stream keeps going — the rest of its scope is
				// still live. Nothing was written, so the cursor does not move:
				// it is the high-water mark of what reached the WIRE.
				continue
			}
			if err := st.send(e); err != nil {
				return err
			}
		case <-tick.C:
			if err := st.wr.Heartbeat(); err != nil {
				return err
			}
		case <-recheck.C:
			next, err := s.grants.ScopeFor(ctx, req.UserID)
			if err != nil {
				failures++
				if failures < maxGrantFailures {
					// Keep the last known scope and try again next tick.
					continue
				}
				// Out of tolerance: fail closed. The client gets the same frame
				// a revocation would send — the stream is over either way and
				// the status was committed long ago — but the caller gets the
				// RESOLVER's error, not CloseForbidden. Saying "not authorized"
				// when the truth is "we could not ask" is the same false
				// statement closeClientGone exists to avoid, and it would hand
				// Task 10 a routine revocation and a grants-store outage under
				// one label.
				if werr := st.writeForbidden(); werr != nil {
					return werr
				}
				return fmt.Errorf("stream grant re-check: %w", err)
			}
			failures = 0
			scope = next
			// An empty scope matches nothing and never will again, so no event
			// can arrive to reveal it: this is the only thing that can close an
			// IDLE stream, and it covers both stream shapes at once. A
			// deactivated principal resolves to exactly this — an empty Scope
			// with a nil error — so deactivation lands here too.
			if scope.empty() {
				if err := st.writeForbidden(); err != nil {
					return err
				}
				return CloseForbidden
			}
		}
	}
}
