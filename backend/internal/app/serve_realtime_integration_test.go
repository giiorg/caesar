//go:build integration

package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/runs"
)

// These are the plan's acceptance evidence for Plan 10: they run the whole
// chain — NOTIFY → tailer → hub → splice → codec → socket — inside one real
// process, which nothing before this task has ever done.
//
// Every read off a stream is bounded. A streaming test that HANGS takes the
// suite to its own timeout and reports nothing useful, which this plan has
// done twice; the request context carries a deadline, so a stream that says
// nothing fails with the frames it did see.

const testPassword = "a long enough password"

// servedProcess is one booted runServe: the real composition root over a
// fresh database, plus the address its listener actually bound.
type servedProcess struct {
	baseURL string
	pool    *pgxpool.Pool
	svc     *identity.Service
	log     *syncBuffer
	stop    context.CancelFunc
	done    chan error
	// reaped is set once someone has taken runServe's return value, so the
	// cleanup below does not wait out its own patience on a channel that has
	// already been read.
	reaped bool
}

// bootServe starts runServe and waits until it is listening. tune adjusts the
// config before boot; realtimeTestConfig explains the defaults it starts from.
func bootServe(t *testing.T, tune func(*config.Config)) *servedProcess {
	t.Helper()
	ctx := context.Background()
	dbURL := testdb.NewURL(t)
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	cfg := realtimeTestConfig(dbURL)
	if tune != nil {
		tune(&cfg)
	}

	logBuf := &syncBuffer{}
	serveCtx, cancel := context.WithCancel(ctx)
	p := &servedProcess{
		pool: pool,
		svc: identity.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), identity.Params{
			SessionIdleTTL:     cfg.Auth.SessionIdleTTL.Std(),
			SessionAbsoluteTTL: cfg.Auth.SessionAbsoluteTTL.Std(),
		}),
		log:  logBuf,
		stop: cancel,
		done: make(chan error, 1),
	}
	go func() { p.done <- runServe(serveCtx, cfg, slog.New(slog.NewJSONHandler(logBuf, nil))) }()
	t.Cleanup(func() {
		if p.reaped {
			return
		}
		cancel()
		select {
		case <-p.done:
		case <-time.After(30 * time.Second):
			t.Error("runServe never returned after cancel")
		}
	})
	p.baseURL = "http://" + p.awaitListening(t)
	return p
}

// awaitListening reads the bound address out of the log. It is read from
// there rather than from the config because the config says :0 — the port is
// the operating system's answer and the log line is where runServe writes it
// down.
func (p *servedProcess) awaitListening(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		for _, line := range strings.Split(p.log.String(), "\n") {
			if !strings.Contains(line, `"http server listening"`) {
				continue
			}
			var rec struct {
				Addr string `json:"addr"`
			}
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("listening log line is not JSON: %v\n%s", err, line)
			}
			if rec.Addr == "" || strings.HasSuffix(rec.Addr, ":0") {
				t.Fatalf("listening log line carries %q, not the port the listener bound", rec.Addr)
			}
			return rec.Addr
		}
		select {
		case err := <-p.done:
			t.Fatalf("runServe returned before listening: %v\n%s", err, p.log.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never reported listening:\n%s", p.log.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// shutdown cancels the process's context and reports how long runServe took
// to return. The caller asserts the bound; a shutdown that outlives the test's
// own patience is a failure and never a hang.
func (p *servedProcess) shutdown(t *testing.T, within time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	p.reaped = true
	p.stop()
	select {
	case err := <-p.done:
		if err != nil {
			t.Fatalf("runServe = %v, want a clean shutdown\n%s", err, p.log.String())
		}
		return time.Since(start)
	case <-time.After(within):
		t.Fatalf("runServe did not return within %s\n%s", within, p.log.String())
		return 0
	}
}

// login mints a session token for an existing principal.
func (p *servedProcess) login(t *testing.T, email string) string {
	t.Helper()
	_, token, err := p.svc.Login(context.Background(), email, testPassword, "127.0.0.1")
	if err != nil {
		t.Fatalf("login %s: %v", email, err)
	}
	return token
}

// realtimeTestConfig is a bootable config whose realtime block is tuned for a
// test's patience rather than a deployment's.
//
// poll_fallback is deliberately FAR longer than any assertion here waits. The
// fallback exists so a lost notification costs latency and never an event —
// which also means it would mask a LISTEN that never worked, and the symptom
// would be nothing but extra latency no assertion measures. Pushed out to a
// minute, an event that arrives in these tests arrived because NOTIFY woke the
// tailer.
func realtimeTestConfig(dbURL string) config.Config {
	cfg := config.Config{}
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Server.ShutdownTimeout = config.Duration(30 * time.Second)
	cfg.Server.AppOrigin = "http://caesar.test"
	cfg.Database.URL = dbURL
	cfg.Security.MasterKey = "dev:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	cfg.Auth.SessionIdleTTL = config.Duration(14 * 24 * time.Hour)
	cfg.Auth.SessionAbsoluteTTL = config.Duration(90 * 24 * time.Hour)
	cfg.Orchestrator = config.Orchestrator{
		Workers:           2,
		PollInterval:      config.Duration(50 * time.Millisecond),
		LeaseTTL:          config.Duration(30 * time.Second),
		HeartbeatInterval: config.Duration(10 * time.Second),
		OutboxRetention:   config.Duration(168 * time.Hour),
	}
	cfg.Realtime = config.Realtime{
		// Short enough that a test can wait for one: the first heartbeat is
		// what proves a stream has reached its live phase.
		HeartbeatInterval: config.Duration(200 * time.Millisecond),
		PollFallback:      config.Duration(time.Minute),
		GrantRecheck:      config.Duration(200 * time.Millisecond),
		SubscriberQueue:   256,
		ReplayBatch:       500,
		SnapshotEvents:    500,
	}
	return cfg
}

// openStream opens an SSE stream and returns a reader over its frames. The
// returned cancel ends the request, which is how a client disconnects.
func openStream(t *testing.T, p *servedProcess, path, token string, life time.Duration) (*sseStream, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), life)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cookie", authn.SessionCookie+"="+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("open %s: status = %d", path, resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		cancel()
		t.Fatalf("open %s: Content-Type = %q", path, got)
	}
	return &sseStream{sc: bufio.NewScanner(resp.Body)}, cancel
}

// sseStream reads one client's frames. Every wait is bounded by the request
// deadline openStream set, so a stream that goes quiet ends the scan instead
// of parking the test.
type sseStream struct {
	sc   *bufio.Scanner
	seen []string
}

// await reads lines until one satisfies want, and fails with everything it
// saw if the stream ends or the deadline passes first.
func (s *sseStream) await(t *testing.T, what string, want func(string) bool) string {
	t.Helper()
	for s.sc.Scan() {
		line := s.sc.Text()
		s.seen = append(s.seen, line)
		if want(line) {
			return line
		}
	}
	t.Fatalf("stream ended without %s (scan error: %v); frames seen:\n%s",
		what, s.sc.Err(), strings.Join(s.seen, "\n"))
	return ""
}

// rest drains what is left of the stream, for assertions about how it ENDED.
func (s *sseStream) rest() string {
	var b strings.Builder
	for s.sc.Scan() {
		b.WriteString(s.sc.Text())
		b.WriteString("\n")
	}
	return b.String()
}

// awaitLive waits for the first heartbeat comment. That is the one frame only
// the LIVE phase can produce (realtime.Streamer.follow ticks it), so it is
// what makes "the event arrived over the live path" a fact rather than a
// hope: an event appended afterwards cannot have been picked up by the
// snapshot or by catch-up, both of which are already finished.
func (s *sseStream) awaitLive(t *testing.T) {
	t.Helper()
	s.await(t, "a heartbeat", func(l string) bool { return strings.HasPrefix(l, ": hb") })
}

// appendRuntimeEvent writes one event through an ordinary production path —
// Service.AppendRuntimeEvent, its own transaction, the same appendEvents and
// the same NOTIFY every other writer uses.
func appendRuntimeEvent(t *testing.T, pool *pgxpool.Pool, runID uuid.UUID, kind string) {
	t.Helper()
	svc := runs.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}),
		nil, nil, nil, nil, nil, nil, runs.Params{})
	if err := svc.AppendRuntimeEvent(context.Background(),
		runs.AttemptRef{RunID: runID, NodeID: "build", Attempt: 1}, kind,
		map[string]any{"note": "task 11"}); err != nil {
		t.Fatalf("append %s: %v", kind, err)
	}
}

// The acceptance test for the whole plan: an event committed by an ordinary
// domain write reaches an open socket.
func TestServe_StreamsAnEventEndToEnd(t *testing.T) {
	ctx := context.Background()
	p := bootServe(t, nil)
	// Bootstrap FIRST: it refuses once any admin row exists, and
	// seedRealtimeRun writes one of its own.
	if _, err := p.svc.Bootstrap(ctx, "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}
	seeded := seedRealtimeRun(ctx, t, p.pool, "e2e")
	token := p.login(t, "root@example.com")

	stream, cancel := openStream(t, p, "/api/v1/runs/"+seeded.runID.String()+"/events", token, 30*time.Second)
	defer cancel()
	stream.awaitLive(t)

	appendRuntimeEvent(t, p.pool, seeded.runID, runs.EventPhaseEgressDenied)

	line := stream.await(t, "the appended event", func(l string) bool {
		return strings.HasPrefix(l, "event: "+runs.EventPhaseEgressDenied)
	})
	if line != "event: "+runs.EventPhaseEgressDenied {
		t.Fatalf("event line = %q", line)
	}
	data := stream.await(t, "the frame's data line", func(l string) bool {
		return strings.HasPrefix(l, "data: ")
	})
	var frame struct {
		ID      int64          `json:"id"`
		RunID   string         `json:"run_id"`
		Kind    string         `json:"kind"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &frame); err != nil {
		t.Fatalf("data line is not a StreamFrame: %v\n%s", err, data)
	}
	if frame.RunID != seeded.runID.String() || frame.ID == 0 || frame.Payload["note"] != "task 11" {
		t.Fatalf("frame = %+v, want the appended event on run %s", frame, seeded.runID)
	}
}

// An SSE connection does not drain on its own, so a shutdown that waits for
// the HTTP server first waits for every open stream — the whole
// ShutdownTimeout, on every deploy. Closing the hub ends the handlers, and
// only then can Shutdown complete.
func TestServe_ShutdownDoesNotWaitForOpenStreams(t *testing.T) {
	ctx := context.Background()
	p := bootServe(t, nil)
	if _, err := p.svc.Bootstrap(ctx, "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}
	seeded := seedRealtimeRun(ctx, t, p.pool, "shutdown")
	token := p.login(t, "root@example.com")

	// The client stays connected on purpose: a stream the test closed first
	// would let Shutdown finish for a reason that has nothing to do with the
	// hub. The stream is LIVE — a stream still in catch-up would not be ended
	// by Hub.Close either, since it only notices the closed channel once it
	// reaches the live loop.
	stream, cancel := openStream(t, p, "/api/v1/runs/"+seeded.runID.String()+"/events", token, 5*time.Minute)
	defer cancel()
	stream.awaitLive(t)

	// Well under the 30s ShutdownTimeout this process booted with: the point
	// is that shutdown does not WAIT for the stream, so anything near the
	// timeout is the failure, and 10s is slack for a loaded CI box rather
	// than a threshold anyone tuned.
	took := p.shutdown(t, 10*time.Second)
	t.Logf("shutdown with one open stream took %s", took)
}

// Finding 5's trap, end to end and through the real caller. A grant revoked
// under an open stream ends it with the terminal frame — a POST-COMMIT
// ending, which realtime.Serve reports as an error and runs/api would answer
// with httpx.Problem if the adapter forwarded it. The bytes in the body are
// the assertion: a problem document appended to a committed SSE response is
// what forwarding actually costs.
func TestServe_ARevokedStreamEndsWithoutAProblemDocumentInItsBody(t *testing.T) {
	ctx := context.Background()
	p := bootServe(t, nil)
	root, err := p.svc.Bootstrap(ctx, "root@example.com", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	seeded := seedRealtimeRun(ctx, t, p.pool, "revoked")
	actor := identity.Principal{UserID: root.ID, Email: root.Email, Role: identity.RoleAdmin}
	member, err := p.svc.CreateUser(ctx, actor, "member@example.com", testPassword, identity.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.svc.SetGrant(ctx, actor, seeded.projectID, member.ID, identity.GrantView); err != nil {
		t.Fatal(err)
	}
	token := p.login(t, "member@example.com")

	stream, cancel := openStream(t, p, "/api/v1/runs/"+seeded.runID.String()+"/events", token, 60*time.Second)
	defer cancel()
	stream.awaitLive(t)

	if err := p.svc.DeleteGrant(ctx, actor, seeded.projectID, member.ID); err != nil {
		t.Fatal(err)
	}

	stream.await(t, "the terminal forbidden frame", func(l string) bool { return l == "event: forbidden" })
	tail := stream.rest()
	if strings.Contains(tail, `"title"`) || strings.Contains(tail, "/errors/") {
		t.Fatalf("a problem document was appended to a committed SSE response:\n%s\n"+
			"StreamServer's error means the stream committed NOTHING; every later "+
			"ending is the implementation's to report", tail)
	}
	if !strings.Contains(p.log.String(), "sse stream ended") {
		t.Errorf("the revocation was neither returned nor logged:\n%s", p.log.String())
	}
}

// A wrong channel name is the worst failure shape in this wiring: NOTIFY goes
// nowhere, the tailer falls back to polling, and the only symptom is latency
// nothing measures. The two sides share runs.EventsChannel so they cannot
// disagree; this is the other half — proof that the name they share is the
// one the database actually delivers on.
func TestPGWaker_IsWokenByAnOrdinaryAppend(t *testing.T) {
	ctx := context.Background()
	dbURL := testdb.NewURL(t)
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	seeded := seedRealtimeRun(ctx, t, pool, "waker")

	waker := newPGWaker(dbURL, discard())
	t.Cleanup(func() { _ = waker.Close() })

	// One Wait that times out first: it is what establishes the LISTEN, and a
	// waker that never connected would answer the real notification from a
	// connection it opened afterwards, which proves nothing about the name.
	warm, cancelWarm := context.WithTimeout(ctx, 2*time.Second)
	defer cancelWarm()
	if err := waker.Wait(warm); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("warm-up Wait = %v, want a deadline", err)
	}

	woken := make(chan error, 1)
	go func() {
		waitCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		woken <- waker.Wait(waitCtx)
	}()
	// A beat for the goroutine to be inside WaitForNotification. Missing it
	// costs nothing — the notification would simply be delivered to the next
	// Wait on the same connection — so this is not a race the test can lose.
	time.Sleep(200 * time.Millisecond)
	appendRuntimeEvent(t, pool, seeded.runID, runs.EventPhaseLogTruncated)

	select {
	case err := <-woken:
		if err != nil {
			t.Fatalf("Wait = %v, want a wake-up: an ordinary append NOTIFYs %q",
				err, runs.EventsChannel)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("a committed append never woke the listener")
	}
}
