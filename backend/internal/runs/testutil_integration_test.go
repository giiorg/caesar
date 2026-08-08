//go:build integration

package runs_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

// queryFaults makes ONE named statement fail, once, on demand.
//
// It exists because this package could not ask the question the publish state
// machine is mostly built to answer: what happens when a transaction fails
// AFTER its effect has landed on the forge. Every guard on that path —
// receiptFailure at both receipts and at the halt, the artifact read a live
// intent depends on — was previously provable only by mutating the function
// under test, which proves the function and not the call site.
//
// How it works: it is a pgx.QueryTracer on the fixture's pool.
// TraceQueryStart is handed the SQL sqlc generated (whose leading comment
// carries the query's name) and returns the context pgx runs the statement on,
// so an already-cancelled context makes exactly that statement fail. pgconn
// checks the context BEFORE it writes anything, so nothing reaches the server:
// the connection stays healthy and the surrounding transaction rolls back
// normally. That is a database blip in its most honest form — an error the
// caller has to classify, not a poisoned session.
//
// Inert until armed, so every other test in this package is untouched.
type queryFaults struct {
	mu    sync.Mutex
	rules map[string]*queryFault
}

type queryFault struct{ skip, times int }

func newQueryFaults() *queryFaults { return &queryFaults{rules: map[string]*queryFault{}} }

// fail arms the named sqlc query — the Go method name, e.g. "RecordPushReceipt"
// — to fail its next `times` executions after letting `skip` of them through.
// skip is how a statement issued from two places is narrowed to the second.
func (q *queryFaults) fail(name string, skip, times int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rules[name] = &queryFault{skip: skip, times: times}
}

// remaining reports how many armed failures have not fired. A test asserts it
// is zero, or it proved nothing: an injection that never triggered leaves a
// green run that looks exactly like a guard that held.
func (q *queryFaults) remaining(name string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if r, ok := q.rules[name]; ok {
		return r.times
	}
	return 0
}

func (q *queryFaults) armed(sql string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for name, rule := range q.rules {
		if rule.times <= 0 || !strings.Contains(sql, "-- name: "+name+" ") {
			continue
		}
		if rule.skip > 0 {
			rule.skip--
			return false
		}
		rule.times--
		return true
	}
	return false
}

func (q *queryFaults) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !q.armed(data.SQL) {
		return ctx
	}
	dead, cancel := context.WithCancel(ctx)
	cancel()
	return dead
}

func (q *queryFaults) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// fixture is the minimal asset scaffold a spine row graph needs. Rows
// are inserted with raw SQL: the spine's FKs point at peer-context
// tables, but the runs context never imports peer packages.
type fixture struct {
	pool      *pgxpool.Pool
	userID    uuid.UUID
	projectID uuid.UUID
	repoID    uuid.UUID
	credID    uuid.UUID
	versionID uuid.UUID
	// remote and baseSHA are set only by newPublishFixture: a real local bare
	// repository and its head. They are empty for every other test, which
	// keeps the synthetic "basesha000…" pin those tests assert against.
	remote  string
	baseSHA string
	// faults is the database-fault seam, armed on this pool and inert until a
	// test asks for a statement to fail.
	faults *queryFaults
}

// newPublishFixture is newFixture plus a real remote. Nothing in this package
// had ever pushed anything before Plan 08 Task 9, and a publication is only
// meaningfully testable against a repository that really receives it: the
// approved tree hash has to be a git tree object id, and the base sha has to
// be a commit that exists.
func newPublishFixture(t *testing.T) fixture {
	t.Helper()
	f := newFixture(t)
	dir := t.TempDir()
	base, err := fake.InitBareRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	f.remote, f.baseSHA = filepath.Join(dir, "bare.git"), base
	return f
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(testdb.NewURL(t))
	if err != nil {
		t.Fatal(err)
	}
	faults := newQueryFaults()
	cfg.ConnConfig.Tracer = faults
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	f := fixture{pool: pool, faults: faults}
	err = pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, role) VALUES ('spine@example.com', 'x', 'admin') RETURNING id`,
	).Scan(&f.userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (name) VALUES ('spine') RETURNING id`,
	).Scan(&f.projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO credentials (id, name, kind, key_id, wrapped_dek, ciphertext)
		 VALUES (uuidv7(), 'spine-token', 'forge_token', 'dev', '\x00', '\x00') RETURNING id`,
	).Scan(&f.credID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO repos (project_id, forge_kind, slug, default_branch, credential_id)
		 VALUES ($1, 'github', 'spine/repo', 'main', $2) RETURNING id`,
		f.projectID, f.credID,
	).Scan(&f.repoID); err != nil {
		t.Fatal(err)
	}
	var wfID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO workflows (name) VALUES ('spine-wf') RETURNING id`,
	).Scan(&wfID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO workflow_versions (workflow_id, version, content_hash, document)
		 VALUES ($1, 1, 'deadbeef', '{"schema":1,"nodes":[]}'::jsonb) RETURNING id`,
		wfID,
	).Scan(&f.versionID); err != nil {
		t.Fatal(err)
	}
	return f
}

// newRunRow inserts a bare task+run pair for tests below the service
// layer (events, queue). Wall deadline far in the future by default.
func (f fixture) newRunRow(t *testing.T, status string) (taskID, runID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	err := f.pool.QueryRow(ctx,
		`INSERT INTO tasks (project_id, repo_id, workflow_version_id, title, requester_id, created_at)
		 VALUES ($1, $2, $3, 'spine task', $4, $5) RETURNING id`,
		f.projectID, f.repoID, f.versionID, f.userID, time.Now().UTC(),
	).Scan(&taskID)
	if err != nil {
		t.Fatal(err)
	}
	err = f.pool.QueryRow(ctx,
		`INSERT INTO runs (task_id, status, definition_hash, base_sha, policy_snapshot,
		                   execution_budget_ms, wall_deadline, created_at)
		 VALUES ($1, $2, 'deadbeef', 'abc123', '{"schema":1,"max_attempts":3,"execution_budget_ms":7200000,"wall_clock_ms":86400000}'::jsonb,
		         7200000, now() + interval '1 day', $3) RETURNING id`,
		taskID, status, time.Now().UTC(),
	).Scan(&runID)
	if err != nil {
		t.Fatal(err)
	}
	return taskID, runID
}
