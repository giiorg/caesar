//go:build integration

package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// The spine's FKs point at peer-context tables (users, projects,
// credentials, repos, workflow_versions, tasks). They are seeded with raw
// SQL: the runs context never imports a peer package, and neither does its
// test code.

// assets is the minimal peer-context scaffold a run row needs.
type assets struct {
	userID    uuid.UUID
	projectID uuid.UUID
	repoID    uuid.UUID
	credID    uuid.UUID
	versionID uuid.UUID
}

func seedAssets(ctx context.Context, t *testing.T, pool *pgxpool.Pool) assets {
	t.Helper()
	var a assets
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, role)
		 VALUES ('gates@example.com', 'x', 'admin') RETURNING id`,
	).Scan(&a.userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (name) VALUES ('gates') RETURNING id`,
	).Scan(&a.projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO credentials (id, name, kind, key_id, wrapped_dek, ciphertext)
		 VALUES (uuidv7(), 'gates-token', 'forge_token', 'dev', '\x00', '\x00') RETURNING id`,
	).Scan(&a.credID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO repos (project_id, forge_kind, slug, default_branch, credential_id)
		 VALUES ($1, 'github', 'acme/widgets', 'main', $2) RETURNING id`,
		a.projectID, a.credID,
	).Scan(&a.repoID); err != nil {
		t.Fatal(err)
	}
	var wfID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO workflows (name) VALUES ('gates-wf') RETURNING id`,
	).Scan(&wfID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO workflow_versions (workflow_id, version, content_hash, document)
		 VALUES ($1, 1, 'deadbeef', '{"schema":1,"nodes":[]}'::jsonb) RETURNING id`,
		wfID,
	).Scan(&a.versionID); err != nil {
		t.Fatal(err)
	}
	return a
}

// seedRunWith inserts a task+run pair over freshly seeded assets and
// returns both, so callers that need the repo or the approver can reach
// them. The run is 'waiting' with a deadline far in the future: every
// deciding statement in this task predicates on both.
func seedRunWith(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (uuid.UUID, assets) {
	t.Helper()
	a := seedAssets(ctx, t, pool)
	var taskID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tasks (project_id, repo_id, workflow_version_id, title, requester_id, created_at)
		 VALUES ($1, $2, $3, 'gates task', $4, $5) RETURNING id`,
		a.projectID, a.repoID, a.versionID, a.userID, time.Now().UTC(),
	).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	var runID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO runs (task_id, status, definition_hash, base_sha, policy_snapshot,
		                   execution_budget_ms, wall_deadline, created_at)
		 VALUES ($1, 'waiting', 'deadbeef', 'abc123',
		         '{"schema":2,"max_attempts":3,"execution_budget_ms":7200000,"wall_clock_ms":86400000}'::jsonb,
		         7200000, now() + interval '1 day', $2) RETURNING id`,
		taskID, time.Now().UTC(),
	).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	return runID, a
}

func seedRun(ctx context.Context, t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	runID, _ := seedRunWith(ctx, t, pool)
	return runID
}

// mustInsertPhase inserts one attempt row in the given status.
func mustInsertPhase(ctx context.Context, t *testing.T, pool *pgxpool.Pool,
	runID uuid.UUID, nodeID string, attempt int, status string,
) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, status, created_at)
		 VALUES ($1, $2, $3, 3, $4, now()) RETURNING id`,
		runID, nodeID, attempt, status,
	).Scan(&id); err != nil {
		t.Fatalf("insert phase %s#%d (%s): %v", nodeID, attempt, status, err)
	}
	return id
}

// mustFinishPhase moves one attempt row to a terminal status.
func mustFinishPhase(ctx context.Context, t *testing.T, pool *pgxpool.Pool,
	runID uuid.UUID, nodeID string, attempt int, status string,
) {
	t.Helper()
	tag, err := pool.Exec(ctx,
		`UPDATE phase_runs SET status = $4, finished_at = now()
		 WHERE run_id = $1 AND node_id = $2 AND attempt = $3`,
		runID, nodeID, attempt, status)
	if err != nil {
		t.Fatalf("finish phase %s#%d: %v", nodeID, attempt, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("finish phase %s#%d: updated %d rows", nodeID, attempt, tag.RowsAffected())
	}
}

// mustInsertArtifact inserts one artifact of the given type.
func mustInsertArtifact(ctx context.Context, t *testing.T, pool *pgxpool.Pool,
	runID uuid.UUID, nodeID string, attempt int, artifactType string,
) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO artifacts (id, run_id, node_id, attempt, type, payload, created_at)
		 VALUES (uuidv7(), $1, $2, $3, $4, '{}'::jsonb, now()) RETURNING id`,
		runID, nodeID, attempt, artifactType,
	).Scan(&id); err != nil {
		t.Fatalf("insert %s artifact for %s#%d: %v", artifactType, nodeID, attempt, err)
	}
	return id
}

// mustInsertGateDecision records one decided gate attempt. phase_run_id is
// UNIQUE on gate_decisions, so each decision needs its own attempt row.
func mustInsertGateDecision(ctx context.Context, t *testing.T, pool *pgxpool.Pool,
	runID, artifactID, actedBy uuid.UUID, attempt int,
) uuid.UUID {
	t.Helper()
	phaseID := mustInsertPhase(ctx, t, pool, runID, "gate", attempt, "succeeded")
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO gate_decisions (phase_run_id, run_id, node_id, generation, decision,
		                             examined_artifact_id, note, acted_by, self_approval, acted_at)
		 VALUES ($1, $2, 'gate', $3, 'approve', $4, '', $5, false, now()) RETURNING id`,
		phaseID, runID, attempt, artifactID, actedBy,
	).Scan(&id); err != nil {
		t.Fatalf("insert gate decision #%d: %v", attempt, err)
	}
	return id
}

// seedRunWithTwoGateDecisions gives a run one patch artifact and two
// decided gate attempts — the two generations a live-authorization test
// needs one authorization each for.
func seedRunWithTwoGateDecisions(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (
	runID, artifactID, decisionA, decisionB, repoID uuid.UUID,
) {
	t.Helper()
	runID, a := seedRunWith(ctx, t, pool)
	artifactID = mustInsertArtifact(ctx, t, pool, runID, "implement", 1, "patch")
	decisionA = mustInsertGateDecision(ctx, t, pool, runID, artifactID, a.userID, 1)
	decisionB = mustInsertGateDecision(ctx, t, pool, runID, artifactID, a.userID, 2)
	return runID, artifactID, decisionA, decisionB, a.repoID
}

// seedRunWithAuthorization mints one valid ApprovedPatch and returns the
// coordinates a publish intent has to pin alongside it.
func seedRunWithAuthorization(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (
	runID, authID, repoID, credID uuid.UUID,
) {
	t.Helper()
	runID, a := seedRunWith(ctx, t, pool)
	artifactID := mustInsertArtifact(ctx, t, pool, runID, "implement", 1, "patch")
	decisionID := mustInsertGateDecision(ctx, t, pool, runID, artifactID, a.userID, 1)
	if err := pool.QueryRow(ctx, `
		INSERT INTO approved_patches (run_id, gate_node_id, publish_node_id, gate_decision_id,
		                              artifact_id, base_sha, tree_hash, repo_id,
		                              forge_kind, repo_slug, target_branch,
		                              generation, status, created_at)
		VALUES ($1,'gate','publish',$2,$3,'abc','def',$4,'github','acme/widgets','main',1,'valid',now())
		RETURNING id`,
		runID, decisionID, artifactID, a.repoID,
	).Scan(&authID); err != nil {
		t.Fatalf("seed authorization: %v", err)
	}
	return runID, authID, a.repoID, a.credID
}

func TestApprovedPatches_OneLiveAuthorizationPerPublishNode(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	runID, artifactID, decisionA, decisionB, repoID := seedRunWithTwoGateDecisions(ctx, t, pool)

	insert := func(decisionID uuid.UUID) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO approved_patches (run_id, gate_node_id, publish_node_id, gate_decision_id,
			                              artifact_id, base_sha, tree_hash, repo_id,
			                              forge_kind, repo_slug, target_branch,
			                              generation, status, created_at)
			VALUES ($1,'gate','publish',$2,$3,'abc','def',$4,'github','acme/widgets','main',1,'valid',now())`,
			runID, decisionID, artifactID, repoID)
		return err
	}
	if err := insert(decisionA); err != nil {
		t.Fatalf("first authorization: %v", err)
	}
	err := insert(decisionB)
	if err == nil {
		t.Fatal("a second LIVE authorization for the same publish node must be refused: the token is single-use")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("want unique violation, got %v", err)
	}

	// Invalidating the first makes room for the next generation's.
	if _, err := pool.Exec(ctx, `UPDATE approved_patches SET status='invalidated', settled_at=now() WHERE run_id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if err := insert(decisionB); err != nil {
		t.Fatalf("after invalidation a new authorization must fit: %v", err)
	}
}

func TestPublishIntents_AnAuthorizationIsConsumedByAtMostOneIntent(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	// The seed returns the credential too: publish_intents pins forge_kind,
	// repo_slug and credential_id at creation (they are the coordinates the
	// approval named), and all three are NOT NULL.
	runID, authID, repoID, credID := seedRunWithAuthorization(ctx, t, pool)

	// The branch is derived from the NODE, not from the authorization id: two
	// intents sharing a branch would collide on publish_intents_marker_key
	// instead, and this test would pass while proving the wrong constraint.
	insert := func(node string) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO publish_intents (run_id, node_id, attempt, authorization_id, repo_id,
			                             forge_kind, repo_slug, credential_id,
			                             branch, target_branch, base_sha, tree_hash, title, body,
			                             status, created_at, updated_at)
			VALUES ($1,$2,1,$3,$4,'github','acme/widgets',$5,
			        'caesar/'||$2,'main','abc','def','t','b','pending',now(),now())`,
			runID, node, authID, repoID, credID)
		return err
	}
	if err := insert("publish"); err != nil {
		t.Fatal(err)
	}
	err := insert("publish_two")
	if err == nil {
		t.Fatal("one authorization must not fund two publications")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "publish_intents_authorization_id_key" {
		t.Fatalf("want the single-use authorization key to refuse this, got %v", err)
	}
}

func TestPhaseRuns_AWaitingGateBlocksASecondLiveRowForItsNode(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	runID := seedRun(ctx, t, pool)
	insert := func(attempt int, status string) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, status, created_at)
			VALUES ($1,'gate',$2,3,$3,now())`, runID, attempt, status)
		return err
	}
	if err := insert(1, "waiting_decision"); err != nil {
		t.Fatal(err)
	}
	if err := insert(2, "pending"); err == nil {
		t.Fatal("waiting_decision is live: a second live row for the node must be refused")
	}
	if _, err := pool.Exec(ctx, `UPDATE phase_runs SET status='succeeded', finished_at=now() WHERE run_id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if err := insert(2, "pending"); err != nil {
		t.Fatalf("once the gate is decided a successor row must fit: %v", err)
	}
}

func TestListSucceededNodes_ForgetsANodeThatWasRematerialized(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	runID := seedRun(ctx, t, pool)
	q := store.New(pool)

	mustInsertPhase(ctx, t, pool, runID, "implement", 1, "succeeded")
	got, err := q.ListSucceededNodes(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"implement"}, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}

	// A revision rewinds the node: a NEWER pending attempt row appears.
	mustInsertPhase(ctx, t, pool, runID, "implement", 2, "pending")
	got, err = q.ListSucceededNodes(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a rematerialized node must stop counting as succeeded, got %v", got)
	}

	// And counts again once the new attempt lands.
	mustFinishPhase(ctx, t, pool, runID, "implement", 2, "succeeded")
	got, err = q.ListSucceededNodes(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"implement"}, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}
