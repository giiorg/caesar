package realtime

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
)

// publishDeadline is how long a Publish may take before the test calls it
// blocked. It is enormous compared to 1000 non-blocking sends and still
// finite, which is the whole point: a blocking send never finishes at all.
const publishDeadline = time.Second

// hubEvent is one row's worth of Event — only the fields Filter.Matches
// reads, because delivery is all these tests assert.
func hubEvent(id int64, project, run uuid.UUID, kind string) Event {
	return Event{ID: id, RunID: run, ProjectID: project, Kind: kind}
}

// drainIDs is every event currently queued, in order. Publish is synchronous,
// so once it has returned the queue holds exactly what was delivered — no
// waiting is needed and none is done, which is what lets a test assert that
// something was NOT delivered.
func drainIDs(s *Subscription) []int64 {
	var out []int64
	for {
		select {
		case e, ok := <-s.C():
			if !ok {
				return out
			}
			out = append(out, e.ID)
		default:
			return out
		}
	}
}

// assertClosed drains s until its channel closes. Draining is the only way to
// observe the close: whatever was already queued comes out first, and the
// close is the receive after it.
func assertClosed(t *testing.T, s *Subscription) {
	t.Helper()
	deadline := time.After(patience)
	for {
		select {
		case _, ok := <-s.C():
			if !ok {
				return
			}
		case <-deadline:
			t.Fatalf("subscription channel was not closed within %s", patience)
		}
	}
}

// registryLen is how many subscriptions Publish would fan out to. The test is
// in package realtime precisely so eviction and unsubscribe can be checked
// where they happen rather than inferred from what arrived.
func registryLen(h *Hub) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

func registryHas(h *Hub, s *Subscription) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.subs[s]
	return ok
}

// Fan-out is per-subscriber filtering, not a broadcast the subscriber sorts
// out afterwards: a subscription must never see an event outside its filter,
// because Filter carries the authorization scope.
func TestHub_FansOutToMatchingSubscribersOnly(t *testing.T) {
	t.Parallel()
	projA, projB := uuid.New(), uuid.New()
	runA, runB := uuid.New(), uuid.New()

	h := NewHub(HubConfig{SubscriberQueue: 16})
	perRun := h.Subscribe(Filter{RunID: &runA, Scope: Scope{Projects: []uuid.UUID{projA}}})
	attention := h.Subscribe(Filter{Attention: true, Scope: Scope{Projects: []uuid.UUID{projB}}})

	h.Publish([]Event{
		hubEvent(1, projA, runA, "run.started"),    // perRun
		hubEvent(2, projA, runB, "run.started"),    // another run, and outside attention's scope
		hubEvent(3, projB, runB, "gate.waiting"),   // attention
		hubEvent(4, projA, runA, "phase.started"),  // perRun: in scope for it, out of scope for attention
		hubEvent(5, projB, runB, "phase.started"),  // attention: Plan 12a carries phase transitions
		hubEvent(6, projB, runB, "audit.recorded"), // neither: a kind in no attention namespace
	})

	if diff := cmp.Diff([]int64{1, 4}, drainIDs(perRun)); diff != "" {
		t.Errorf("per-run subscriber received (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]int64{3, 5}, drainIDs(attention)); diff != "" {
		t.Errorf("attention subscriber received (-want +got):\n%s", diff)
	}
}

// The rule the whole task exists for. Publish runs on the single per-process
// tailer goroutine, so one subscriber that stops reading must cost that
// subscriber its stream and cost every other stream nothing.
func TestHub_PublishNeverBlocks(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	h := NewHub(HubConfig{SubscriberQueue: 4})
	// Subscribed and genuinely idle: nothing in this test ever reads from
	// the channel before Publish returns.
	s := h.Subscribe(Filter{RunID: &run, Scope: Scope{Projects: []uuid.UUID{proj}}})

	evs := make([]Event, 0, 1000)
	for id := int64(1); id <= 1000; id++ {
		evs = append(evs, hubEvent(id, proj, run, "run.started"))
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Publish(evs)
	}()
	select {
	case <-done:
	case <-time.After(publishDeadline):
		t.Fatalf("Publish did not return within %s with an idle subscriber: the tailer is being backpressured", publishDeadline)
	}

	assertClosed(t, s)
	if err := s.Err(); !errors.Is(err, ErrSlowConsumer) {
		t.Errorf("Err() after overflow = %v, want %v", err, ErrSlowConsumer)
	}
}

// A client that was evicted must be able to tell that from a clean shutdown:
// Task 8 writes a different terminal frame for each, and only one of them is
// worth reconnecting from.
func TestHub_EvictedSubscriberReportsWhy(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	h := NewHub(HubConfig{SubscriberQueue: 1})
	s := h.Subscribe(Filter{RunID: &run, Scope: Scope{Projects: []uuid.UUID{proj}}})

	if err := s.Err(); err != nil {
		t.Fatalf("Err() on a live subscription = %v, want nil", err)
	}

	h.Publish([]Event{
		hubEvent(1, proj, run, "run.started"),
		hubEvent(2, proj, run, "run.started"),
		hubEvent(3, proj, run, "run.started"),
	})

	assertClosed(t, s)
	if err := s.Err(); !errors.Is(err, ErrSlowConsumer) {
		t.Fatalf("Err() after eviction = %v, want %v", err, ErrSlowConsumer)
	}

	// Evicting must unregister, not merely close. Publish is synchronous, so
	// by the time it has returned the registry is already final. A hub that
	// closed the channel and kept the entry fails silently — nothing panics
	// and nothing is misdelivered, because offer's closed check absorbs it —
	// it just accumulates one dead entry per slow client, walked on every
	// batch for the life of the process.
	if registryHas(h, s) {
		t.Error("an evicted subscription is still in the registry")
	}
	if got := registryLen(h); got != 0 {
		t.Errorf("registry holds %d subscription(s) after the only one was evicted, want 0", got)
	}

	// Closed exactly once: the handler's own Close lands on an already-evicted
	// subscription on every disconnect, and must neither double-close the
	// channel nor rewrite the reason the client is about to be told.
	s.Close(CloseShutdown)
	if err := s.Err(); !errors.Is(err, ErrSlowConsumer) {
		t.Errorf("Err() after a late Close = %v, want the eviction reason %v", err, ErrSlowConsumer)
	}
}

func TestHub_CloseIsIdempotent(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	h := NewHub(HubConfig{SubscriberQueue: 4})
	s := h.Subscribe(Filter{RunID: &run, Scope: Scope{Projects: []uuid.UUID{proj}}})

	s.Close(CloseShutdown)
	s.Close(CloseShutdown)

	assertClosed(t, s)
	if err := s.Err(); !errors.Is(err, CloseShutdown) {
		t.Errorf("Err() = %v, want %v", err, CloseShutdown)
	}
}

// Close must unregister, not merely mark. A registry that keeps closed
// subscriptions leaks one entry per served request for the process lifetime,
// and Publish would walk them all on every batch.
func TestHub_UnsubscribeRemovesFromRegistry(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	f := Filter{RunID: &run, Scope: Scope{Projects: []uuid.UUID{proj}}}
	h := NewHub(HubConfig{SubscriberQueue: 8})
	live, gone := h.Subscribe(f), h.Subscribe(f)

	gone.Close(CloseShutdown)

	if registryHas(h, gone) {
		t.Error("a closed subscription is still in the registry")
	}
	if got := registryLen(h); got != 1 {
		t.Errorf("registry holds %d subscription(s) after one Close, want 1", got)
	}

	h.Publish([]Event{hubEvent(1, proj, run, "run.started")})

	if diff := cmp.Diff([]int64{1}, drainIDs(live)); diff != "" {
		t.Errorf("live subscriber received (-want +got):\n%s", diff)
	}
	if got := drainIDs(gone); len(got) != 0 {
		t.Errorf("the closed subscription received %v, want nothing", got)
	}
}

// Publish runs on the tailer goroutine; Close runs on the handler goroutine
// when the client disconnects. A non-blocking send racing a close(ch) is a
// panic rather than a data race, so -race alone does not cover this — the
// repetition does.
func TestHub_ConcurrentPublishAndCloseDoNotPanic(t *testing.T) {
	t.Parallel()
	const (
		subscribers = 16
		batches     = 50
	)
	proj, run := uuid.New(), uuid.New()
	f := Filter{RunID: &run, Scope: Scope{Projects: []uuid.UUID{proj}}}
	h := NewHub(HubConfig{SubscriberQueue: 4})

	subs := make([]*Subscription, subscribers)
	for i := range subs {
		subs[i] = h.Subscribe(f)
	}
	batch := []Event{
		hubEvent(1, proj, run, "run.started"),
		hubEvent(2, proj, run, "run.started"),
		hubEvent(3, proj, run, "run.started"),
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range subs {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			for range batches {
				h.Publish(batch)
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			subs[i].Close(CloseShutdown)
		}()
	}
	close(start)
	wg.Wait()

	for _, s := range subs {
		assertClosed(t, s)
	}
}

// Task 11 closes the hub before srv.Shutdown. A subscription the hub forgot
// to close is a handler still parked on its channel, which is the shutdown
// hang that ordering exists to prevent.
func TestHub_CloseTerminatesEverySubscription(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	f := Filter{RunID: &run, Scope: Scope{Projects: []uuid.UUID{proj}}}
	h := NewHub(HubConfig{SubscriberQueue: 4})
	subs := []*Subscription{h.Subscribe(f), h.Subscribe(f), h.Subscribe(f)}

	// Twice: Task 11 can plausibly reach shutdown from more than one path, and
	// a double-close panic is an expensive thing to discover at shutdown.
	h.Close()
	h.Close()

	for i, s := range subs {
		assertClosed(t, s)
		if err := s.Err(); !errors.Is(err, CloseShutdown) {
			t.Errorf("subscription %d: Err() = %v, want %v", i, err, CloseShutdown)
		}
	}
	if got := registryLen(h); got != 0 {
		t.Errorf("registry holds %d subscription(s) after Close, want 0", got)
	}
}

// A request that arrives between Hub.Close and the server's own shutdown must
// not get a live subscription: nothing would ever close it, and the handler
// would sit on it until the client gave up.
func TestHub_SubscribeAfterCloseReturnsAClosedSubscription(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	f := Filter{RunID: &run, Scope: Scope{Projects: []uuid.UUID{proj}}}
	h := NewHub(HubConfig{SubscriberQueue: 4})

	h.Close()
	s := h.Subscribe(f)

	assertClosed(t, s)
	if err := s.Err(); !errors.Is(err, CloseShutdown) {
		t.Errorf("Err() = %v, want %v", err, CloseShutdown)
	}
	if got := registryLen(h); got != 0 {
		t.Errorf("registry holds %d subscription(s) after a post-shutdown Subscribe, want 0", got)
	}

	// And the tailer may still be draining a final batch into it.
	h.Publish([]Event{hubEvent(1, proj, run, "run.started")})
}

// A zero HubConfig is what an unset config section hands over. A zero queue
// would evict every subscriber on its first event — total failure, silently —
// so it takes the default instead, as TailerConfig's fields do.
func TestHub_ZeroQueueConfigFallsBackToTheDefault(t *testing.T) {
	t.Parallel()
	proj, run := uuid.New(), uuid.New()
	h := NewHub(HubConfig{})
	s := h.Subscribe(Filter{RunID: &run, Scope: Scope{Projects: []uuid.UUID{proj}}})

	if got := cap(s.C()); got != defaultSubscriberQueue {
		t.Errorf("queue capacity = %d, want the default %d", got, defaultSubscriberQueue)
	}
	h.Publish([]Event{hubEvent(1, proj, run, "run.started")})
	if err := s.Err(); err != nil {
		t.Fatalf("a subscriber was evicted by its first event: %v", err)
	}
	if diff := cmp.Diff([]int64{1}, drainIDs(s)); diff != "" {
		t.Errorf("received (-want +got):\n%s", diff)
	}
}
