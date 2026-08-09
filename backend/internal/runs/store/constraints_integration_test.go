//go:build integration

package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

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
			                             author_email, commit_message, status, created_at, updated_at)
			VALUES ($1,$2,1,$3,$4,'github','acme/widgets',$5,
			        'caesar/'||$2,'main','abc','def','t','b','who@example.com','m','pending',now(),now())`,
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
