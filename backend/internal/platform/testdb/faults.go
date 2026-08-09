//go:build integration || runnerintegration

package testdb

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// QueryFaults makes named sqlc statements fail on demand.
//
// It exists because a test could not otherwise ask the question the publish
// state machine is mostly built to answer: what happens when a transaction
// fails AFTER its effect has landed on the forge. Every guard on that path —
// receiptFailure at both receipts and at the halt, the artifact read a live
// intent depends on — was previously provable only by mutating the function
// under test, which proves the function and not the call site.
//
// How it works: it is a pgx.QueryTracer on a pool's connection config.
// TraceQueryStart is handed the SQL sqlc generated (whose leading comment
// carries the query's name) and returns the context pgx runs the statement on,
// so an already-cancelled context makes exactly that statement fail. pgconn
// checks the context BEFORE it writes anything, so nothing reaches the server:
// the connection stays healthy and the surrounding transaction rolls back
// normally. That is a database blip in its most honest form — an error the
// caller has to classify, not a poisoned session.
//
// It lives in platform rather than in one context's test helpers because two
// packages arm it: internal/runs against its service fixture, and internal/app
// against the golden set's live HTTP stack. One implementation cannot drift
// from itself.
//
// Inert until armed, so every test that never calls Fail is untouched.
type QueryFaults struct {
	mu    sync.Mutex
	rules map[string]*queryFault
	// fired counts firings per name and is never cleared, so a test can arm,
	// wait, disarm and still prove the injection happened.
	fired map[string]int
}

// queryFault is one armed statement. times is how many firings are left when
// unlimited is false.
type queryFault struct {
	skip      int
	times     int
	unlimited bool
}

func NewQueryFaults() *QueryFaults {
	return &QueryFaults{rules: map[string]*queryFault{}, fired: map[string]int{}}
}

// Fail arms the named sqlc query — the Go method name, e.g. "RecordPushReceipt"
// — to fail its next `times` executions after letting `skip` of them through.
// skip is how a statement issued from two places is narrowed to the second.
func (q *QueryFaults) Fail(name string, skip, times int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rules[name] = &queryFault{skip: skip, times: times}
}

// FailUntilDisarmed arms the named query to fail every execution until Disarm.
// A one-shot fault is disarmed by firing, which closes the window it opened
// before any reader can look at it; this keeps the window open so a test can
// assert what the failed transaction left behind while it is still failing.
func (q *QueryFaults) FailUntilDisarmed(name string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rules[name] = &queryFault{unlimited: true}
}

// Disarm removes the rule for name, letting the statement through again.
func (q *QueryFaults) Disarm(name string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.rules, name)
}

// Remaining reports how many armed failures have not fired. A test asserts it
// is zero, or it proved nothing: an injection that never triggered leaves a
// green run that looks exactly like a guard that held. It is always 0 for an
// unlimited rule, which has no end to count down to — assert Fired instead.
func (q *QueryFaults) Remaining(name string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if r, ok := q.rules[name]; ok {
		return r.times
	}
	return 0
}

// Fired reports how many times the named fault has actually failed a
// statement. It survives Disarm, so a test can arm, wait, disarm and still
// prove the injection happened.
func (q *QueryFaults) Fired(name string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.fired[name]
}

func (q *QueryFaults) armed(sql string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for name, rule := range q.rules {
		if (rule.times <= 0 && !rule.unlimited) || !strings.Contains(sql, "-- name: "+name+" ") {
			continue
		}
		if rule.skip > 0 {
			rule.skip--
			return false
		}
		if !rule.unlimited {
			rule.times--
		}
		q.fired[name]++
		return true
	}
	return false
}

func (q *QueryFaults) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !q.armed(data.SQL) {
		return ctx
	}
	dead, cancel := context.WithCancel(ctx)
	cancel()
	return dead
}

func (q *QueryFaults) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// NewWithFaults returns a pool on a fresh isolated database with the fault
// tracer already installed, closed on test cleanup. The tracer has to be set
// on the connection config before the pool is built, so a caller that wants
// faults cannot reach them through New.
func NewWithFaults(t *testing.T) (*pgxpool.Pool, *QueryFaults) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(NewURL(t))
	if err != nil {
		t.Fatalf("testdb: parse pool config: %v", err)
	}
	faults := NewQueryFaults()
	cfg.ConnConfig.Tracer = faults
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("testdb: pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, faults
}
