//go:build integration

package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/realtime"
	"github.com/giiorg/caesar/backend/internal/runs"
)

// Plan 10's acceptance evidence. Everything here runs against the real
// process (bootServe), a real Postgres and a real socket: the properties
// asserted are properties of NOTIFY → tailer → hub → splice → codec → TCP
// together, and none of them is observable in any one of those pieces.
//
// The fixture is serve_realtime_integration_test.go's — bootServe, openStream,
// openStreamFrom, sseStream, appendRuntimeEvent, realtimeTestConfig — and this
// file adds only what those cannot express: a client whose receive buffer is
// small enough to stop reading in, and reading a stream's ids out to a stopping
// point.

// awaitFrames reads until a frame of kind stop has gone by, returning every id
// seen on the way — INCLUDING that frame's own, because WriteEvent writes the
// id: line before the event: line (sse.go).
//
// A kind is the stopping point rather than an id because the id is not known
// until the writers have finished, and the reader has to be running while they
// do: a test that waited for them first would be measuring how much the socket
// buffers, not how much the stream delivers.
func awaitFrames(t *testing.T, s *sseStream, stop string) []int64 {
	t.Helper()
	var got []int64
	s.await(t, "a "+stop+" frame", func(l string) bool {
		if id, ok := frameID(t, l); ok {
			got = append(got, id)
			return false
		}
		return l == "event: "+stop
	})
	return got
}

// awaitIDs reads until the frame carrying want — or anything above it — has
// gone by, returning every id seen. The "or above" matters under a mutation
// that drops want itself: the stream then stops at the next id and the set
// comparison names what is missing, instead of the read timing out.
func awaitIDs(t *testing.T, s *sseStream, want int64) []int64 {
	t.Helper()
	var got []int64
	s.await(t, fmt.Sprintf("event %d", want), func(l string) bool {
		id, ok := frameID(t, l)
		if !ok {
			return false
		}
		got = append(got, id)
		return id >= want
	})
	return got
}

// drainIDs reads to the END of the stream, returning every id seen. It is for
// assertions about a stream that is expected to CLOSE: a read that ended in the
// request deadline instead is a failure with its reason, never a hang.
func drainIDs(t *testing.T, s *sseStream) []int64 {
	t.Helper()
	var got []int64
	for s.sc.Scan() {
		line := s.sc.Text()
		s.seen = append(s.seen, line)
		if id, ok := frameID(t, line); ok {
			got = append(got, id)
		}
	}
	if err := s.sc.Err(); err != nil {
		t.Fatalf("the stream did not end, it broke off: %v; frames seen:\n%s",
			err, strings.Join(s.seen, "\n"))
	}
	return got
}

// frameID reads an id: line. A line that says id: and then something that is
// not a number is a codec bug and fails the test rather than being skipped.
func frameID(t *testing.T, line string) (int64, bool) {
	t.Helper()
	raw, ok := strings.CutPrefix(line, "id: ")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("frame id line %q is not an integer: %v", line, err)
	}
	return id, true
}

// ascending fails unless ids are strictly increasing — which is both "no
// duplicates" and "no reordering", the invariant stream.cursor exists to keep
// and the one that makes a stored Last-Event-ID a high-water mark.
func ascending(t *testing.T, what string, ids []int64) {
	t.Helper()
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("%s: id %d follows %d at position %d — ids on the wire must be "+
				"STRICTLY ascending, or the id a client stores is not the high-water "+
				"mark of what it has seen\nall ids: %v", what, ids[i], ids[i-1], i, ids)
		}
	}
}

// committedEventIDs is what run_events actually holds for one run, ascending.
//
// The expected set comes from HERE and never from what a test intended to
// write. A stream compared against the test's own intentions agrees with a
// writer that silently did nothing, which is the one failure a gaplessness
// test most needs to see.
func committedEventIDs(ctx context.Context, t *testing.T, pool *pgxpool.Pool, runID uuid.UUID) []int64 {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT id FROM run_events WHERE run_id = $1 ORDER BY id`, runID)
	if err != nil {
		t.Fatalf("read back committed events: %v", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		t.Fatalf("read back committed events: %v", err)
	}
	return ids
}

// awaitLog waits for want to appear in the process log.
//
// It is a wait and not a read because the log line naming how a stream ended is
// written after Serve returns (realtime/api.serve), and the client's EOF is
// written after that — but only the handler's return orders them, and the test
// observes the EOF through a socket. Nothing makes the two visible in that
// order to the reader.
func awaitLog(t *testing.T, p *servedProcess, want string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if strings.Contains(p.log.String(), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the log never carried %q:\n%s", want, p.log.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// appendPaddedEvent is appendRuntimeEvent with a payload of pad bytes. See
// TestStream_SlowClientIsEvictedAndResumes for why the size is load-bearing.
func appendPaddedEvent(t *testing.T, pool *pgxpool.Pool, runID uuid.UUID, kind string, pad int) {
	t.Helper()
	appendEventPayload(t, pool, runID, kind, map[string]any{"pad": strings.Repeat("x", pad)})
}

// clientReadBuffer is the kernel receive buffer boundedReadClient reads
// through. See TestStream_SlowClientIsEvictedAndResumes for why it is pinned.
const clientReadBuffer = 4096

// boundedReadClient dials with a small, explicitly set receive buffer, which
// turns the kernel's autotuning off.
//
// Only the eviction test needs it, and its premise is why: a client that stops
// reading blocks Serve inside WriteEvent only once everything between them is
// full, and how much that is belongs to the operating system — both Linux and
// macOS autotune a loopback receive window into the megabytes. SO_RCVBUF, set
// explicitly, is what makes the amount a stopped client can absorb a number
// this test bounds rather than one it hopes for.
//
// The same pin is why NOTHING ELSE may dial through it. A 4 KiB window is slow
// in both directions: reading 8 MiB back through one takes 4 ms on a macOS
// loopback and 3m40s on Linux (~37 KiB/s, measured on golang:alpine and matched
// by the CI runner, which is where an earlier version of this test died on its
// deadline). Only a stream that has to STALL may dial bounded; a stream that has
// to move bytes dials with default buffers.
//
// One client per stream, because its transport pools connections and a
// connection left holding an unread stream must never be reused for the resume.
func boundedReadClient(t *testing.T) *http.Client {
	t.Helper()
	tr := &http.Transport{
		// The transport's own buffer over the connection, for the same reason.
		ReadBufferSize: 1024,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			tcp, ok := c.(*net.TCPConn)
			if !ok {
				return nil, fmt.Errorf("dialled %T, want a *net.TCPConn to bound", c)
			}
			if err := tcp.SetReadBuffer(clientReadBuffer); err != nil {
				return nil, fmt.Errorf("bound the receive buffer: %w", err)
			}
			return tcp, nil
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr}
}

// TestStream_NoGapsUnderConcurrentWriters is what the whole plan exists to
// pass: eight writers commit into four runs while two subscribers stream one of
// them, and the ids that reach each socket are exactly the ids the table holds
// for that run.
//
// THREE THINGS ARE ASSERTED AND THEY ARE NOT THE SAME THING.
//
//   - Strictly ascending: no duplicate and no reordering reached the wire, so
//     the last id a client saw is the high-water mark of everything it saw.
//   - Set equality against run_events, READ BACK BY SQL: no event was dropped
//     between the writer's COMMIT and the socket, wherever the splice happened
//     to be when it landed.
//   - The table holds what the writers set out to write. This one is a
//     comparison against the test's own intentions, and it is here precisely
//     because the assertion above is not: a writer that failed silently leaves
//     the stream and the table agreeing on an impoverished set, and the whole
//     test passes while proving nothing.
//
// "No holes" is NOT contiguity. run_events.id is one global counter across
// every run, so one run's ids are a subsequence of it with arbitrary gaps —
// with eight writers over four runs those gaps are the normal case. Set
// equality against the same run's committed rows is the property; a contiguity
// check would fail on correct behaviour.
//
// TWO SUBSCRIBERS WATCH THE SAME RUN, because a stream has two entry paths and
// only one of them reaches the splice's overlap. A fresh open primes from a
// snapshot and then jumps its cursor to that snapshot's WATERMARK (prime), so
// its catch-up spans only the microseconds between the watermark read and the
// first replay page: nothing the tailer queued can also arrive by replay, and
// the dedup that makes the overlap harmless is never asked a question. A
// resume starts its cursor at the client's Last-Event-ID instead, so catch-up
// re-reads rows the tailer has ALREADY queued for it — which is the overlap,
// and it is why the second subscriber is here rather than in a comment.
// ReplayBatch is one row so that catch-up is many pages, and the load below
// commits into them.
//
// SubscriberQueue is raised well past the number of events in flight. Eviction
// under a long catch-up is real, expected and TestStream_SlowClientIsEvicted-
// AndResumes's subject; here it would quietly turn a gaplessness test into a
// reconnect test that never reconnects.
func TestStream_NoGapsUnderConcurrentWriters(t *testing.T) {
	const (
		writers   = 8
		loadRuns  = 4
		perWriter = 20
		// Committed on the OTHER runs, and in full BEFORE the watched run's
		// backlog, so the table deterministically holds foreign rows below
		// this run's lowest id (which is 16, not 1, at these numbers). Two
		// things rest on that. The run-scoped snapshot and replay queries are
		// asked to filter from their very first row rather than being
		// accidentally right over a table that holds nothing else; and the
		// resumed stream's Last-Event-ID is a real offset into the counter.
		// Without it, whether any foreign row existed by the time the streams
		// opened would be a race against the writers below.
		//
		// It is NOT what makes this run's ids sparse — committing in full
		// first is exactly what stops it interleaving, so the backlog is one
		// CONTIGUOUS block (16..215 at these numbers). The gaps come from the
		// concurrent writer phase further down, which interleaves four runs
		// into the one counter; one observed run carried on 216, 217, 219,
		// 220, 223, 224, 227 …, and the ids missing there are the other three
		// runs'.
		preburst = 5
		// And on the watched run, deep, because it is what the resumed stream
		// has to page through: the replay query filters by run, so this run's
		// backlog IS that stream's catch-up, and the catch-up is the only phase
		// where the tailer's queue and the replay pager can both hold the same
		// event. At one row a page it is a few hundred round trips, which the
		// load below commits into throughout.
		backlog = 200
		// The one kind no writer uses, so a reader has a stopping point it can
		// recognize without knowing any id in advance.
		sentinelKind = runs.EventRunSucceeded
		// Which of the seeded runs is the one both subscribers watch.
		watchedRun = 0
	)
	// Which run writer w commits into, named once because the count below has to
	// agree with the loop that uses it.
	runFor := func(w int) int { return w % loadRuns }
	// Every writer runFor sends to the watched run, COUNTED rather than divided:
	// (writers/loadRuns) is right only while writers is a multiple of loadRuns,
	// and at writers = 9 it would expect two while writers 0, 4 and 8 all commit
	// there — a false failure over correct behaviour, which is the one direction
	// an assertion must never be wrong in.
	watchedWriters := 0
	for w := range writers {
		if runFor(w) == watchedRun {
			watchedWriters++
		}
	}
	// The backlog, then those writers, then the sentinel: what run_events must
	// hold for the watched run once the test is over.
	wantCount := backlog + watchedWriters*perWriter + 1

	ctx := context.Background()
	p := bootServe(t, func(cfg *config.Config) {
		cfg.Realtime.SubscriberQueue = 4096
		cfg.Realtime.ReplayBatch = 1
		// The fresh stream reaches this run's OLDEST id only through its
		// snapshot — prime takes the most recent snapshot_events matching rows
		// and then jumps the cursor to that snapshot's watermark, so anything
		// older is below the cursor and is never replayed. Set equality against
		// the WHOLE committed set therefore holds only while the snapshot window
		// is at least as wide as what this test commits for the run. Asserted
		// rather than commented, because what it otherwise produces is a red
		// gaplessness test over correct production code.
		if cfg.Realtime.SnapshotEvents < wantCount {
			t.Fatalf("snapshot_events is %d and this test commits %d events for the "+
				"watched run: the fresh stream would legitimately open above the run's "+
				"oldest id and its set equality below would fail on correct behaviour. "+
				"Raise the knob or shrink the load, and do not weaken the assertion",
				cfg.Realtime.SnapshotEvents, wantCount)
		}
	})
	if _, err := p.svc.Bootstrap(ctx, "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}
	seeded := make([]seededRun, loadRuns)
	for i := range seeded {
		seeded[i] = seedRealtimeRun(ctx, t, p.pool, fmt.Sprintf("load-%d", i))
	}
	// Both loops commit BEFORE either stream opens, so the watched run's backlog
	// is what its SNAPSHOT carries: the splice is what is under test, and a
	// stream whose backlog is empty exercises only its live half.
	for i, r := range seeded {
		if i == watchedRun {
			continue
		}
		for range preburst {
			appendRuntimeEvent(t, p.pool, r.runID, runs.EventPhaseMaterialized)
		}
	}
	for range backlog {
		appendRuntimeEvent(t, p.pool, seeded[watchedRun].runID, runs.EventPhaseMaterialized)
	}
	token := p.login(t, "root@example.com")

	watched := seeded[watchedRun]
	path := "/api/v1/runs/" + watched.runID.String() + "/events"
	// The backlog as the table holds it, asserted before anything indexes into
	// it: nothing has been committed for this run except those appends, so an
	// answer that is not exactly the backlog is the seeding having failed, and
	// it says so here instead of panicking one line below. The second clause is
	// not redundant — it is what keeps that index safe if backlog is ever set
	// to zero.
	seededIDs := committedEventIDs(ctx, t, p.pool, watched.runID)
	if len(seededIDs) != backlog || len(seededIDs) == 0 {
		t.Fatalf("the watched run's backlog is %d events, want %d and at least one: the "+
			"seeding above did not commit what it set out to, and every assertion below "+
			"is over the wrong set", len(seededIDs), backlog)
	}
	// This run's lowest id, which is as far back as a resume can usefully start
	// and so the longest catch-up available. Last-Event-ID resumes STRICTLY
	// above it, so the resumed stream owes everything except this one event.
	oldest := seededIDs[0]

	// THE LOAD STARTS FIRST, and that ordering is what puts commits inside every
	// phase of both splices rather than only after them: the snapshot's
	// transaction runs mid-load, and so does every page of the resumed stream's
	// catch-up. Opening first — which reads tidier — leaves catch-up over a
	// table nobody is writing to, finished before the first writer commits.
	//
	// It is also what makes the watched run's ids SPARSE: eight writers over
	// four runs interleave four runs' commits into the one counter, so from here
	// on this run holds a subsequence of it with arbitrary gaps.
	kinds := []string{
		runs.EventPhaseStarted, runs.EventPhaseSucceeded,
		runs.EventPhaseLogTruncated, runs.EventPhaseEgressDenied,
	}
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		// appendRuntimeEvent fails with t.Fatalf, which off the test goroutine
		// marks the test failed and exits only this writer. The deferred Done is
		// what keeps that from hanging the wait below.
		go func() {
			defer wg.Done()
			for i := range perWriter {
				appendRuntimeEvent(t, p.pool, seeded[runFor(w)].runID, kinds[i%len(kinds)])
			}
		}()
	}
	go func() {
		wg.Wait()
		appendRuntimeEvent(t, p.pool, watched.runID, sentinelKind)
	}()

	// Deliberately NOT awaitLive on either: a stream held until its first
	// heartbeat has finished catching up, which would put every event below on
	// the live path and leave the two phases that hand off to it untested.
	resumed, cancelResumed := openStreamFrom(t, p, path, token,
		strconv.FormatInt(oldest, 10), http.DefaultClient, 120*time.Second)
	defer cancelResumed()
	fresh, cancelFresh := openStream(t, p, path, token, 120*time.Second)
	defer cancelFresh()

	// Read one stream out and then the other. The unread one is not at risk:
	// its queue holds far more than this whole test commits, so it falls behind
	// without being evicted — which is the difference the raised queue buys.
	gotFresh := awaitFrames(t, fresh, sentinelKind)
	gotResumed := awaitFrames(t, resumed, sentinelKind)

	// Only now, with every writer finished and the sentinel the last thing
	// committed, is the table's answer final.
	committed := committedEventIDs(ctx, t, p.pool, watched.runID)
	if len(committed) != wantCount {
		t.Fatalf("run_events holds %d events for the watched run, want %d: a writer "+
			"committed less than it set out to, and the streams would agree with it",
			len(committed), wantCount)
	}
	assertGapless(t, "the stream opened on a snapshot", committed, gotFresh)
	assertGapless(t, "the stream resumed from "+strconv.FormatInt(oldest, 10),
		committed[1:], gotResumed)
}

// assertGapless is the two per-stream properties, kept apart so each names what
// broke. The third — that the table holds what the writers set out to write —
// is the caller's, because it is about the fixture and not about a stream.
func assertGapless(t *testing.T, what string, committed, got []int64) {
	t.Helper()
	ascending(t, what, got)
	if diff := cmp.Diff(committed, got); diff != "" {
		t.Errorf("%s is not what the table holds for this run (-committed +received):\n%s\n"+
			"every id here is an event that COMMITTED and never reached the socket, or "+
			"reached it without having committed", what, diff)
	}
}

// TestStream_SlowClientIsEvictedAndResumes is the bounded queue's contract, end
// to end: a client that cannot keep up is DISCONNECTED rather than allowed to
// backpressure the tailer, and what that costs it is exactly nothing, because
// Last-Event-ID resumes above the last id it actually received.
//
// The shape, and every step of it is forced by how the eviction reaches the
// client. A client that stops reading fills the socket, so Serve blocks inside
// WriteEvent; the hub's bounded queue then fills and the subscriber is evicted
// with CloseSlowConsumer — but Serve does not NOTICE until the write unblocks,
// i.e. until the client reads again. So: stop reading, commit far more than the
// queue holds, resume reading, watch the stream end, reconnect.
//
// The payload is padded because the queue is not the only buffer between the
// tailer and a stopped client — the socket is one too, and how big it is
// belongs to the operating system, not to this test. boundedReadClient pins
// the receiving end as far as it can be pinned; the padding covers the rest, by
// making the burst 8 MiB, which is ~3x what the chain has been measured
// absorbing (25 of 256 frames get through on a macOS loopback, 83 of 256 on a
// Linux CI runner, whose send side autotunes higher). The count is logged so
// the margin is re-measured on every run rather than assumed, and a burst that
// stopped being enough fails on len(missed) == 0 below, saying so.
//
// ONLY THE EVICTED STREAM DIALS BOUNDED, and its deadline is minutes rather
// than seconds because of it: everything the socket absorbed has to come back
// through that 4 KiB window before the stream can end, which is ~74s of the
// measured 2.6 MiB on a CI runner. The resume has nothing to prove about
// buffering — it is about ids — so it dials with default buffers and moves the
// same megabytes in milliseconds.
//
// Nothing prunes run_events today, so Oldest never forces a re-snapshot and the
// resume is lossless by construction rather than by luck. A deployment that
// grows a retention sweeper has to meet that here.
func TestStream_SlowClientIsEvictedAndResumes(t *testing.T) {
	const (
		queueDepth = 4
		burst      = 256
		padBytes   = 32768
	)
	ctx := context.Background()
	p := bootServe(t, func(cfg *config.Config) { cfg.Realtime.SubscriberQueue = queueDepth })
	if _, err := p.svc.Bootstrap(ctx, "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}
	seeded := seedRealtimeRun(ctx, t, p.pool, "slow")
	token := p.login(t, "root@example.com")
	path := "/api/v1/runs/" + seeded.runID.String() + "/events"

	stream, cancel := openStreamFrom(t, p, path, token, "", boundedReadClient(t), 240*time.Second)
	defer cancel()
	// Live and caught up before the burst, so everything that follows is the
	// live path meeting a client that stopped reading — and not a catch-up that
	// had not started yet.
	stream.awaitLive(t)

	for range burst {
		appendPaddedEvent(t, p.pool, seeded.runID, runs.EventPhaseStarted, padBytes)
	}

	// Reading again is what lets Serve discover it was evicted.
	drainStart := time.Now()
	first := drainIDs(t, stream)
	t.Logf("the evicted client received %d of %d frames before the stream ended, "+
		"drained through its %d-byte window in %s",
		len(first), burst, clientReadBuffer, time.Since(drainStart).Round(time.Millisecond))
	if len(first) == 0 {
		t.Fatal("the evicted stream delivered nothing, so there is no resume point and " +
			"nothing below this line proves anything")
	}
	// The reason, not merely the ending: a stream that closed because the hub
	// shut down or the writer failed would drain just the same.
	awaitLog(t, p, realtime.CloseSlowConsumer.Error())

	committed := committedEventIDs(ctx, t, p.pool, seeded.runID)
	resumeFrom := first[len(first)-1]
	var missed []int64
	for _, id := range committed {
		if id > resumeFrom {
			missed = append(missed, id)
		}
	}
	if len(missed) == 0 {
		t.Fatalf("the client received every committed event before it was evicted "+
			"(up to %d of %v): the eviction cost it nothing, so the resume below is "+
			"not being tested", resumeFrom, committed)
	}

	// Default buffers, and a client that never held the evicted stream: see
	// boundedReadClient for both halves of that.
	resumed, cancelResume := openStreamFrom(t, p, path, token,
		strconv.FormatInt(resumeFrom, 10), http.DefaultClient, 120*time.Second)
	defer cancelResume()
	got := awaitIDs(t, resumed, missed[len(missed)-1])

	ascending(t, "the ids delivered to the resumed client", got)
	if got[0] <= resumeFrom {
		t.Errorf("the resumed stream opened at %d, at or below the Last-Event-ID %d it "+
			"was given: the resume re-sent what the client already had", got[0], resumeFrom)
	}
	if diff := cmp.Diff(missed, got); diff != "" {
		t.Errorf("the resume is not exactly what the eviction cost (-missed +received):\n%s", diff)
	}
}

// TestStream_ScopedToGrants pins the authorization scope on ALL THREE paths a
// stream delivers over, because they are three different pieces of code and
// only Filter.Matches is shared between them: the snapshot query, the replay
// query, and the live scope test in follow.
//
// The project-B events are committed BEFORE any stream opens, which is the only
// way they land in the snapshot and the replay windows at all — a test that
// wrote them once the stream was live would exercise one path and report
// success for three.
//
// Each phase ends on a project-A event committed AFTER the B event it is
// paired with, so the negative has a positive terminator: a leaked B frame
// carries a lower id and therefore arrives first, and reading up to the A event
// is enough to have seen it.
func TestStream_ScopedToGrants(t *testing.T) {
	ctx := context.Background()
	p := bootServe(t, nil)
	root, err := p.svc.Bootstrap(ctx, "root@example.com", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	runA := seedRealtimeRun(ctx, t, p.pool, "scoped-a")
	runB := seedRealtimeRun(ctx, t, p.pool, "scoped-b")
	actor := identity.Principal{UserID: root.ID, Email: root.Email, Role: identity.RoleAdmin}
	member, err := p.svc.CreateUser(ctx, actor, "member@example.com", testPassword, identity.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.svc.SetGrant(ctx, actor, runA.projectID, member.ID, identity.GrantView); err != nil {
		t.Fatal(err)
	}
	token := p.login(t, "member@example.com")

	// The backlog. Attention kinds throughout — the global stream is defined by
	// the gate./publish./run. prefixes, so a phase.* event would be excluded by
	// the kind test and prove nothing about scope.
	appendRuntimeEvent(t, p.pool, runB.runID, runs.EventRunStarted)
	appendRuntimeEvent(t, p.pool, runA.runID, runs.EventRunStarted)
	appendRuntimeEvent(t, p.pool, runB.runID, runs.EventGateWaiting)
	appendRuntimeEvent(t, p.pool, runA.runID, runs.EventGateWaiting)
	backlogA := committedEventIDs(ctx, t, p.pool, runA.runID)
	backlogB := committedEventIDs(ctx, t, p.pool, runB.runID)
	// Two events each, as appended. Every phase below takes its stopping point
	// from the LAST project-A id, so a short answer here has to fail with a
	// reason rather than panic on the index — and a missing project-B id would
	// take a forbidden event out of the set being watched for.
	if len(backlogA) != 2 || len(backlogB) != 2 {
		t.Fatalf("the backlog is %d events on project A and %d on project B, want 2 and 2: "+
			"the appends above did not commit what they set out to, and the phases below "+
			"would be watching the wrong ids", len(backlogA), len(backlogB))
	}
	forbidden := make(map[int64]bool, len(backlogB))
	for _, id := range backlogB {
		forbidden[id] = true
	}

	// SNAPSHOT. A fresh open primes from the most recent matching events, which
	// is where the whole backlog above lands.
	snapshotted, cancel := openStream(t, p, "/api/v1/stream", token, 60*time.Second)
	defer cancel()
	assertNoneForbidden(t, "the snapshot", awaitIDs(t, snapshotted, backlogA[len(backlogA)-1]), forbidden)

	// REPLAY. Resuming above the FIRST project-A event makes everything after
	// it the catch-up pager's work rather than the snapshot's — including the
	// project-B event that sits between the two.
	resumed, cancelResume := openStreamFrom(t, p, "/api/v1/stream", token,
		strconv.FormatInt(backlogA[0], 10), http.DefaultClient, 60*time.Second)
	defer cancelResume()
	assertNoneForbidden(t, "the replay", awaitIDs(t, resumed, backlogA[len(backlogA)-1]), forbidden)

	// LIVE. The same stream, now past its catch-up: the heartbeat is the one
	// frame only the live phase can produce, so everything after it took the
	// live path.
	resumed.awaitLive(t)
	appendRuntimeEvent(t, p.pool, runB.runID, runs.EventPublishPushed)
	appendRuntimeEvent(t, p.pool, runA.runID, runs.EventPublishPushed)
	liveA := committedEventIDs(ctx, t, p.pool, runA.runID)
	if len(liveA) != len(backlogA)+1 {
		t.Fatalf("project A holds %d events after the live append, want %d: the event this "+
			"phase reads up to never committed", len(liveA), len(backlogA)+1)
	}
	for _, id := range committedEventIDs(ctx, t, p.pool, runB.runID) {
		forbidden[id] = true
	}
	assertNoneForbidden(t, "the live phase", awaitIDs(t, resumed, liveA[len(liveA)-1]), forbidden)
}

// assertNoneForbidden fails if any id the caller may not see reached the wire.
func assertNoneForbidden(t *testing.T, phase string, got []int64, forbidden map[int64]bool) {
	t.Helper()
	for _, id := range got {
		if forbidden[id] {
			t.Fatalf("%s delivered event %d, which belongs to a project this principal "+
				"holds no grant on\nids received: %v", phase, id, got)
		}
	}
}

// TestStream_HeartbeatArrives pins the comment frame that keeps an idle tunnel
// open, and the two things about it that are not the same claim: that it
// REPEATS on the configured interval, and that the connection it padded is
// still a working stream afterwards.
//
// The request deadline is what bounds the interval, deliberately. This stream
// writes nothing until the very end, so its ticker and the Writer's last-write
// clock stay anchored together and comments land about one interval apart: at
// the configured 200ms two of them take under a second, while at the package's
// own 20s default the second would be around 40s — past the 30s deadline. A
// Streamer that ignored heartbeat_interval therefore fails here rather than
// passing slowly.
func TestStream_HeartbeatArrives(t *testing.T) {
	ctx := context.Background()
	p := bootServe(t, nil)
	if _, err := p.svc.Bootstrap(ctx, "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}
	seeded := seedRealtimeRun(ctx, t, p.pool, "heartbeat")
	token := p.login(t, "root@example.com")

	// Nothing is ever committed for this run before the appended event below,
	// so the stream is genuinely idle: any comment frame is the ticker's.
	stream, cancel := openStream(t, p, "/api/v1/runs/"+seeded.runID.String()+"/events", token, 30*time.Second)
	defer cancel()
	started := time.Now()
	stream.awaitLive(t)
	first := time.Since(started)
	stream.awaitLive(t)
	t.Logf("first heartbeat after %s, two within %s", first, time.Since(started))

	// And the padding did not cost the stream: an event appended now still
	// arrives, so the connection stayed open and usable rather than merely
	// unclosed.
	appendRuntimeEvent(t, p.pool, seeded.runID, runs.EventPhaseSucceeded)
	stream.await(t, "an event after the heartbeats", func(l string) bool {
		return l == "event: "+runs.EventPhaseSucceeded
	})
}
