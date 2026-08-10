//go:build integration

package runs_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

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
	faults *testdb.QueryFaults
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
	pool, faults := testdb.NewWithFaults(t)

	f := fixture{pool: pool, faults: faults}
	err := pool.QueryRow(ctx,
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
