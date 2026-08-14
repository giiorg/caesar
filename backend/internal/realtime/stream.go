package realtime

import (
	"context"
	"errors"
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
	//
	// Being a second definition of a configured number is what defaults_test.go
	// exists for: it fails when either end moves alone.
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

// ErrNotStarted marks the one way out of Serve that has NOT committed a
// response. Every other ending — a write failure, a source failure,
// CloseForbidden, CloseSlowConsumer, CloseShutdown — happens over a 200 that
// NewWriter fixed long before, and there the only honest report is bytes in the
// body plus a log line. The sixth is NewWriter's own untagged flush failure:
// committed too, but over a 200 net/http wrote on its way to discovering the
// peer had gone, so there is no client left to write to and the log line is the
// whole report (realtime/api.serve).
//
// It exists because an HTTP surface cannot tell the two apart from the error
// value otherwise, and guessing wrong is expensive in both directions: read a
// committed stream as "not started" and a problem document gets appended to the
// SSE body; read a stream that never opened as committed and the endpoint
// answers a bare 200 with no content, which is exactly what a working but idle
// stream looks like. That silence is the failure mode spec §4's realtime
// surfaces exist to avoid, and it is what both surfaces did before this
// sentinel — see realtime/api.handleStreamAttention.
//
// IT IS NOT "NewWriter FAILED". NewWriter fails two ways and only one of them
// has written nothing; Serve tags accordingly, and the distinction is in
// net/http rather than here. ResponseController.Flush has exactly four exits
// (responsecontroller.go): it hands off to FlushError, or to Flush, or unwraps
// and loops, or returns ErrNotSupported. The stdlib's own FlushError writes the
// header BEFORE it flushes — `if !w.wroteHeader { w.WriteHeader(StatusOK) }`,
// server.go — so an error from THAT branch, a dead peer at connect time, means
// the 200 is already chosen and a later WriteHeader is the "superfluous
// response.WriteHeader call" warning. ErrNotSupported is the only exit that
// touched nothing.
//
// The residual case, stated because it is not covered: a hand-written wrapper
// implementing FlushError could fail without writing and would go untagged,
// costing a silent 200. Nothing in the production tree implements FlushError;
// the one writer in this package that does — sse_test.go's deadPeerWriter —
// mimics net/http and writes the header first, so it is no counter-example to
// the residual. And the mistake falls on the side that never writes over a
// committed body, which is the side to fall on.
//
// Match it with errors.Is; the cause is wrapped alongside it and is what the
// log should carry.
var ErrNotStarted = errors.New("realtime: stream not started")

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
	// GrantRecheck is how often a stream re-resolves its principal's scope, so
	// a grant revoked after the stream opened still reaches it. Catch-up
	// re-checks before each page and the live loop on a ticker, so this bounds
	// the staleness window across BOTH phases rather than only the live one —
	// which matters because catch-up is the phase with no bound of its own.
	//
	// The bound is on when a revocation is ACTED ON, not on when every stream
	// it affects has closed: a per-run stream over a run that emits nothing
	// further has no event to be denied and stays open, idle. See Serve.
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
//   - ErrNotStarted, wrapping the cause, when nothing in w's Unwrap chain can
//     flush. THE ONLY ENDING THAT HAS COMMITTED NOTHING: w is untouched — no
//     status, no body, not even a header (sse.go) — so the caller still owns
//     the response and must answer with a real status. Narrowly this one
//     failure and not "the constructor failed": see ErrNotStarted.
//   - the flush error, UNTAGGED, when the writer could flush but the flush
//     failed — the peer went away before the stream got started. net/http
//     writes the 200 before it can discover that, so this ending is committed
//     like all the others below and the caller must not answer it with a
//     status. Nothing was subscribed or read either way.
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
// A per-run stream whose project leaves a scope that is still otherwise
// populated ends at the next re-check, WITHOUT waiting for an event to deny
// (refresh). That case used to be the one revocation this did not act on: the
// close was reachable only through follow, follow needs an event to test, and a
// run that has gone quiet supplies none — so the connection stayed open and
// heartbeating for the rest of its life. What closes it now is the run's own
// project, carried on the Filter (Filter.runProject).
//
// The gap survives in one shape, and it is the shape that cannot be built
// through this package's constructors: a per-run Filter LITERAL carries no run
// project, and falls back to the event-driven close. Deliberate — see refresh
// for why a missing project must not be read as a denied one.
//
// Only the first error can still become an HTTP status, and ErrNotStarted is
// how a caller recognizes it. Once NewWriter has returned successfully the
// response is committed 200 forever, which is why Tasks 9 and 10 speak of a
// "403-shaped terminal frame" rather than a 403.
func (s *Streamer) Serve(ctx context.Context, w http.ResponseWriter, req StreamRequest) error {
	// The Writer FIRST. It is the one failure that can happen before the 200
	// is committed, it reads no events — so building it first cannot miss one
	// — and it spares the rejection path a subscription to unwind. The
	// invariant this design needs is Subscribe strictly before Snapshot,
	// below, and that is what the ordering test pins.
	wr, err := NewWriter(w, s.cfg.HeartbeatInterval, nil)
	if err != nil {
		// Tagged rather than left as a bare wrapped error so the surfaces can
		// branch on it — see ErrNotStarted for what each wrong guess costs.
		//
		// ErrNotSupported and NOT "NewWriter returned an error", because those
		// are different sets. Nothing in the chain can flush is the only
		// constructor failure that wrote nothing; a flush that failed because
		// the peer is already gone went out over a 200 net/http had written on
		// its way to finding the socket dead. Tagging that one would let a
		// surface put problem+json on top of a committed response, which is the
		// bug this sentinel exists to prevent.
		if errors.Is(err, http.ErrNotSupported) {
			return fmt.Errorf("%w: %w", ErrNotStarted, err)
		}
		// Still an error, still ends the request — just not the caller's to
		// answer with a status.
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
// the grant re-check's two ways out.
func (s *Streamer) deliver(ctx context.Context, st *stream, sub *Subscription, req StreamRequest) error {
	// The re-check spans catch-up AND live, so it is owned here rather than by
	// either phase. See recheck for why that matters.
	rc := s.newRecheck(req)
	defer rc.tick.Stop()

	// prime gets no re-check of its own, and the earlier silence on that point
	// is what let the same gap go unnoticed in replay. It is ONE query, issued
	// immediately after the handler resolved this scope to build the Filter,
	// and it has no interior boundary to re-check at. A tick that lands while
	// it runs is not lost: the ticker is already running and the first replay
	// boundary — the very next thing that happens — consumes it.
	if err := s.prime(ctx, st, req); err != nil {
		return err
	}
	if err := s.replay(ctx, st, rc, req); err != nil {
		return err
	}
	return s.follow(ctx, st, sub, rc, req)
}

// recheck is the grant re-check: the ticker, the last scope known to be real,
// and the consecutive-failure count.
//
// It is owned by deliver and spans catch-up AND live, which is the whole reason
// it is a type rather than three locals in follow. Catch-up is the long phase
// (see replay and Serve), so a re-check that only began once the stream went
// live would leave the staleness window at "however long catch-up took, plus
// GrantRecheck" — and the bounded window is the security property the interval
// buys. Worse, replay reads up to the CURRENT head, so a stale scope there
// emits rows committed after the revocation.
//
// One goroutine — deliver's — touches it, so nothing here is locked.
type recheck struct {
	grants GrantResolver
	userID uuid.UUID
	tick   *time.Ticker
	// scope is the last scope known to be real. It starts at the Filter's own
	// rather than at a resolve of its own: the handler resolved exactly this
	// scope moments ago to build the Filter, so an eager re-resolve would put a
	// database round-trip on every stream open to re-learn what it was just
	// told. The first tick is the earliest anything can have changed.
	scope Scope
	// runProject is Filter.runProject, copied so refresh has it: a per-run
	// stream ends when a refreshed scope stops covering it. NIL MEANS NOT
	// SUPPLIED — never "a project that is denied" — and refresh says why.
	runProject *uuid.UUID
	// failures is the run of CONSECUTIVE resolver failures, reset by any
	// success. See maxGrantFailures for why a run rather than a lifetime.
	failures int
}

func (s *Streamer) newRecheck(req StreamRequest) *recheck {
	return &recheck{
		grants:     s.grants,
		userID:     req.UserID,
		tick:       time.NewTicker(s.cfg.GrantRecheck),
		scope:      req.Filter.Scope,
		runProject: req.Filter.runProject,
	}
}

// due reports whether the interval has elapsed since the last check, without
// blocking. It is how the catch-up phase re-checks: a select case is not
// available to a pager that must keep paging, and polling here costs a round
// trip per PAGE — the plan's "before each delivery batch" — not per event,
// which is the pressure follow's ticker case exists to avoid.
func (r *recheck) due() bool {
	select {
	case <-r.tick.C:
		return true
	default:
		return false
	}
}

// refresh re-resolves the principal and reports whether the stream must END. A
// nil error means carry on, possibly against a narrowed r.scope; a non-nil one
// is what Serve returns, and the terminal frame has already gone out by then.
func (r *recheck) refresh(ctx context.Context, st *stream) error {
	next, err := r.grants.ScopeFor(ctx, r.userID)
	if err != nil {
		r.failures++
		if r.failures < maxGrantFailures {
			// Keep the last known scope and try again next tick.
			return nil
		}
		// Out of tolerance: fail closed. The client gets the same frame a
		// revocation would send — the stream is over either way and the status
		// was committed long ago — but the caller gets the RESOLVER's error,
		// not CloseForbidden. Saying "not authorized" when the truth is "we
		// could not ask" is the same false statement closeClientGone exists to
		// avoid, and it would hand Task 10 a routine revocation and a
		// grants-store outage under one label. Hence writeForbidden here and
		// closeForbidden everywhere else: the asymmetry is deliberate.
		if werr := st.writeForbidden(); werr != nil {
			return werr
		}
		return fmt.Errorf("stream grant re-check: %w", err)
	}
	r.failures = 0
	r.scope = next
	// An empty scope matches nothing and never will again, so no event can
	// arrive to reveal it: this is the only thing that can close an IDLE
	// stream, and it covers both stream shapes at once. A deactivated principal
	// resolves to exactly this — an empty Scope with a nil error — so
	// deactivation lands here too.
	if r.scope.empty() {
		return st.closeForbidden()
	}
	// And a per-run stream ends when the refreshed scope stops covering the run's
	// OWN project, even though the scope is not empty and the run may be silent.
	// This is the other close that needs no event, and it is the only one that
	// can reach a per-run stream over a quiescent run: follow's per-run denial
	// requires an event to test, and a run that has stopped emitting supplies
	// none, so without this the connection outlived the revocation indefinitely.
	//
	// The nil check is NOT a guard for tidiness — it is the whole reason
	// runProject is unexported, and removing it inverts this rule into the
	// failure it was designed around. A stream that carries no run project
	// would be tested against the zero uuid, which a scope denies unless it is
	// All or literally lists it, so an unconditional test would close, on its
	// first tick, most streams that have none — denying principals whose grants
	// never changed.
	//
	// That is TWO populations, not one, and the second is the larger: per-run
	// filters built as literals, and EVERY ATTENTION STREAM, which has no run
	// project by construction. Dropping the guard was run as a mutant and both
	// fail — TestServe_PerRunStreamWithoutARunProjectSurvivesANarrowing and
	// TestServe_DropsEventsOutsideRefreshedScope. A missing project must degrade
	// to the event-driven close, never to a denial.
	//
	// Nil and not a zero uuid, so that a run whose project genuinely IS the zero
	// uuid — schema-valid, since projects.id has a default and no non-zero
	// constraint — is still checked rather than silently exempted.
	if r.runProject != nil && !r.scope.allows(*r.runProject) {
		return st.closeForbidden()
	}
	return nil
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
// decide: a revocation and a resolver outage look identical to the client — the
// stream is over either way — and must not look identical in the logs. Only the
// outage path calls it directly; everything else wants closeForbidden.
//
// It writes to the ResponseWriter directly and flushes by hand. That is what
// Writer.Flush was built for (sse.go) and this is its only caller; the frame is
// stream policy rather than codec, so it does not become a Writer method. The
// Writer's last-write clock is deliberately not touched — see sse.go.
func (st *stream) writeForbidden() error {
	if _, err := fmt.Fprint(st.wr.w, forbiddenFrame); err != nil {
		return fmt.Errorf("write forbidden frame: %w", err)
	}
	st.wr.Flush()
	return nil
}

// closeForbidden ends the stream because authorization was checked and denied:
// the frame, then CloseForbidden. A failed frame write wins, because then the
// client got nothing and the write error is the truer report.
func (st *stream) closeForbidden() error {
	if err := st.writeForbidden(); err != nil {
		return err
	}
	return CloseForbidden
}

// prime writes the client's starting state — a fresh snapshot, or nothing at
// all for a resume whose position the source can still continue from — and
// leaves st.cursor at the point replay must continue from.
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
			// And BELOW the head as well as above the floor. A Last-Event-ID
			// above every id that will ever be written puts the cursor above
			// them too, and send then discards every event forever: the client
			// gets a 200, the stream headers, no snapshot and nothing but
			// heartbeats — indistinguishable from a working idle stream, which
			// is the silence ErrNotStarted exists to keep off this surface. No
			// browser produces one (EventSource echoes back an id this server
			// wrote), but a hand-rolled client can, and so can restoring
			// run_events from a backup while clients hold higher ids.
			//
			// Re-snapshotting is the same degradation direction as the too-old
			// branch above: one duplicated backlog rather than a silent
			// nothing. The round trip is paid HERE rather than next to Oldest
			// because this is the only branch whose outcome it can change — the
			// too-old case re-snapshots either way — and it costs once per
			// stream opened, never per event.
			head, err := s.src.Head(ctx)
			if err != nil {
				return fmt.Errorf("stream prime: %w", err)
			}
			if req.LastEventID <= head {
				st.cursor = req.LastEventID
				return nil
			}
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
//
// It re-checks grants BEFORE EACH PAGE, which is where the interval's promise
// of a bounded staleness window is actually kept: this loop runs for as long as
// the client is behind, it reads up to the current head, and a revocation
// landing mid-catch-up would otherwise be ignored for the whole of it — while
// rows committed after the revocation went out. The refreshed scope goes into
// the NEXT page's Filter rather than re-filtering the page just written: every
// predicate is in SQL (Task 7), so pushing it down is both the cheaper and the
// only non-duplicated place to apply it.
func (s *Streamer) replay(ctx context.Context, st *stream, rc *recheck, req StreamRequest) error {
	f := req.Filter
	for {
		if rc.due() {
			if err := rc.refresh(ctx, st); err != nil {
				return err
			}
			f.Scope = rc.scope
		}
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
// Grants get the re-check ticker's OWN select case, and never a test per event:
// the per-event work is a pure in-memory scope test, so a busy stream costs no
// database round trips at all. The obvious objection — that a busy stream would
// then never get round to re-checking — does not hold: select picks uniformly
// at random among ready cases, so a saturated event channel cannot starve the
// ticker.
//
// Two consequences of that case, both stated rather than left to be found.
// First, ScopeFor is called synchronously here, so a slow grants store stops
// event delivery for the length of the round trip and the subscription's queue
// absorbs the backlog — at the 256-deep default, a slow enough store can evict
// a client as a slow consumer for a reason that is nothing to do with the
// client. Accepted: the alternative is a second goroutine and a scope handoff,
// which costs more than it saves at a 30s interval.
//
// Second, the refreshed scope is NOT pushed back into the subscription's
// filter. Hub.Publish reads that field on the tailer goroutine while holding no
// subscription lock (hub.go), so writing it here is an unsynchronized
// concurrent write — a data race, whatever the detector happens to observe on
// any one run. So events the refreshed scope denies still occupy queue slots
// and can still get this client evicted. Accepted for the same reason: a filter
// mutator needs hub-side locking and buys nothing this task needs.
func (s *Streamer) follow(ctx context.Context, st *stream, sub *Subscription, rc *recheck, req StreamRequest) error {
	tick := time.NewTicker(s.cfg.HeartbeatInterval)
	defer tick.Stop()

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
			if !rc.scope.allows(e.ProjectID) {
				// A per-run stream's entire subject just left the principal's
				// grants, so there is nothing left for it to show: end it.
				//
				// This is the only close an EVENT can trigger, and it is no
				// longer the only form the rule can take: a Filter now carries
				// the run's project, so refresh ends this stream on the re-check
				// without needing an event at all. Which of the two gets there
				// first does not matter — both write the same terminal frame —
				// and for a filter built by RunFilter the re-check normally
				// wins.
				//
				// What keeps this branch load-bearing is the filter that has NO
				// run project: a hand-built literal, where refresh deliberately
				// stands down rather than deny (see there). For those this is
				// still the only close there is, which is why it is not
				// simplified away.
				if req.Filter.RunID != nil {
					return st.closeForbidden()
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
		case <-rc.tick.C:
			if err := rc.refresh(ctx, st); err != nil {
				return err
			}
		}
	}
}
