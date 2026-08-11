package realtime

import "sync"

// defaultSubscriberQueue is the depth a zero HubConfig gets, matching
// config.Realtime.SubscriberQueue's default. An honest zero would evict every
// subscriber on its first event — a total failure with no symptom but empty
// streams — so a zero config is usable rather than broken, as TailerConfig is.
const defaultSubscriberQueue = 256

// CloseReason says why a subscription ended, and IS the error the subscription
// reports: the values are comparable, so errors.Is matches a reason by value
// and a new reason costs exactly one const and nothing else.
type CloseReason string

const (
	// CloseSlowConsumer: the client could not keep up and its queue
	// overflowed. It is expected, not exceptional — the client reconnects
	// with Last-Event-ID and loses nothing.
	CloseSlowConsumer CloseReason = "subscriber fell behind"

	// CloseShutdown: the process is going away.
	CloseShutdown CloseReason = "hub shut down"
)

func (r CloseReason) Error() string { return "realtime: " + string(r) }

// ErrSlowConsumer is CloseSlowConsumer under the name error-handling code
// reaches for. Matching either with errors.Is is the same test. It is the only
// alias there will be: the CloseReason constants above are the canonical
// sentinels, and a reason added later gets no Err* alias of its own.
var ErrSlowConsumer error = CloseSlowConsumer

// HubConfig tunes fan-out. A zero config is usable.
type HubConfig struct {
	// SubscriberQueue is how many events one subscriber may fall behind by
	// before it is evicted. It bounds a slow client's cost in memory and,
	// more importantly, bounds it at all: the alternative to a bounded queue
	// is either an unbounded one or a stalled tailer.
	SubscriberQueue int
}

// Hub fans events out to process-local subscribers. Process-local is a stated
// monolith assumption (spec §4): there is no cross-process fan-out and none is
// wanted — every process runs its own tailer over the same table.
type Hub struct {
	mu     sync.RWMutex
	subs   map[*Subscription]struct{}
	closed bool
	cfg    HubConfig
}

// NewHub builds an empty hub.
func NewHub(cfg HubConfig) *Hub {
	if cfg.SubscriberQueue <= 0 {
		cfg.SubscriberQueue = defaultSubscriberQueue
	}
	return &Hub{subs: make(map[*Subscription]struct{}), cfg: cfg}
}

// Subscribe registers a stream for f. The caller owns the returned
// subscription and MUST Close it when its request ends, or the registry grows
// by one entry per request served.
//
// Subscribing to a closed hub returns an already-closed subscription rather
// than nil or a live one: a request that races shutdown must find its channel
// closed immediately, because nothing is left to close it later and its
// handler would park until the client gave up.
func (h *Hub) Subscribe(f Filter) *Subscription {
	s := &Subscription{hub: h, filter: f, ch: make(chan Event, h.cfg.SubscriberQueue)}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		s.close(CloseShutdown)
		return s
	}
	h.subs[s] = struct{}{}
	return s
}

// Publish delivers evs to every matching subscriber WITHOUT blocking. It is
// the tailer's sink and runs on that single per-process goroutine, so a wait
// here is a wait for every stream in the process. A full queue therefore means
// one thing: the client cannot keep up, so evict it and let it resume from
// Last-Event-ID. Never grow the queue and never wait.
//
// evs is the tailer's own slice and its events' payload maps are shared with
// every subscriber: Publish neither mutates nor retains them.
func (h *Hub) Publish(evs []Event) {
	// Collect under the read lock and deliver outside it, so an eviction —
	// which takes the write lock to unregister — cannot deadlock against the
	// delivery that caused it.
	h.mu.RLock()
	targets := make([]*Subscription, 0, len(h.subs))
	for s := range h.subs {
		targets = append(targets, s)
	}
	h.mu.RUnlock()

	for _, s := range targets {
		for _, e := range evs {
			// Matches checks the authorization scope first and
			// unconditionally. Every delivery path funnels through it.
			if !s.filter.Matches(e) {
				continue
			}
			if s.offer(e) {
				continue
			}
			// The subscription is finished — evicted by this very send, or
			// closed by its own handler while we were walking the batch. Drop
			// it and move to the next target.
			//
			// What forbids a send on a closed channel is offer's closed check
			// under the subscription's mutex, NOT this break: continuing the
			// batch would be safe, merely wasteful. The break is here to skip
			// the rest of the batch — up to Batch-1 further Matches calls and
			// the write-lock churn of re-removing an entry already gone.
			h.remove(s)
			break
		}
	}
}

// Close ends every live subscription and empties the registry, so that no
// handler is left parked on a channel nothing will ever close. Task 11 calls
// it before srv.Shutdown for exactly that reason. Subscribing afterwards
// yields an already-closed subscription.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	live := make([]*Subscription, 0, len(h.subs))
	for s := range h.subs {
		live = append(live, s)
	}
	h.subs = make(map[*Subscription]struct{})
	h.mu.Unlock()

	// Outside the lock: closing a subscription takes its own mutex, and this
	// is the one place both are in play.
	for _, s := range live {
		s.close(CloseShutdown)
	}
}

// remove unregisters s. Removing something already gone is a no-op, which is
// what makes it safe to call from both the eviction path and Close.
func (h *Hub) remove(s *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, s)
}

// Subscription is one client's stream: a filter and the bounded queue its
// events land in.
//
// Its channel is written by the tailer goroutine and closed by the handler
// goroutine, so both go through mu. A send racing a close(ch) is a PANIC and
// not merely a data race — the race detector may never flag it — which is why
// the flag is a mutex rather than an atomic bool checked before the send.
// Holding mu across the send is safe because the send is non-blocking: the
// select returns immediately whether or not the queue had room.
type Subscription struct {
	hub    *Hub
	filter Filter
	ch     chan Event

	mu     sync.Mutex
	closed bool
	reason CloseReason
}

// C is the stream. It is closed when the subscription ends, for whatever
// reason; Err then says which.
func (s *Subscription) C() <-chan Event { return s.ch }

// Err is nil while the subscription is live and, once it has ended, the reason
// it ended — matchable with errors.Is against ErrSlowConsumer, CloseShutdown
// and the reasons added beside them. Read it after C has closed.
func (s *Subscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		return nil
	}
	return s.reason
}

// Close ends the subscription with reason and unregisters it from the hub.
// It is idempotent and safe to call from any goroutine; the FIRST reason wins,
// so a handler tidying up after an eviction does not overwrite the explanation
// the client is about to be given.
func (s *Subscription) Close(reason CloseReason) {
	// Unregister first and unlocked: the hub's lock is never taken while this
	// subscription's own lock is held, which is what keeps the two lock
	// orders from meeting.
	s.hub.remove(s)
	s.close(reason)
}

// offer queues e without ever blocking, reporting whether the subscription is
// still live. A full queue ends it on the spot: the caller is the tailer, and
// there is nothing else it could do that would not cost every other stream.
func (s *Subscription) offer(e Event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	select {
	case s.ch <- e:
		return true
	default:
		s.closeLocked(CloseSlowConsumer)
		return false
	}
}

func (s *Subscription) close(reason CloseReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked(reason)
}

func (s *Subscription) closeLocked(reason CloseReason) {
	if s.closed {
		return
	}
	s.closed = true
	s.reason = reason
	close(s.ch)
}
