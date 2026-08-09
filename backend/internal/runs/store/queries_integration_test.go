//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// ConsumeAuthorization carries the run's wall-deadline predicate, and it is
// driven HERE rather than through the orchestrator because nothing driven
// through the orchestrator can reach it: ClaimPhase's own
// `r.wall_deadline > now()` refuses the claim first, so an expired run's
// publish attempt is never entered at all. This statement is the second fence
// — the one that closes the window between a claim and the authorize
// transaction — and it is only observable from here.
func TestConsumeAuthorization_RefusesAnExpiredRun(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	statusOf := func(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) string {
		t.Helper()
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM approved_patches WHERE id = $1`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		return status
	}

	// A clone per subtest: seedAssets inserts a user with a fixed address, so
	// two seeds in one database collide on users_email_key.
	t.Run("a live run spends its token exactly once", func(t *testing.T) {
		pool := testdb.New(t)
		q := store.New(pool)
		_, authID, _, _ := seedRunWithAuthorization(ctx, t, pool)
		n, err := q.ConsumeAuthorization(ctx, store.ConsumeAuthorizationParams{ID: authID, SettledAt: &now})
		if err != nil || n != 1 {
			t.Fatalf("first consume = %d, %v; want 1", n, err)
		}
		if got := statusOf(t, pool, authID); got != "consumed" {
			t.Fatalf("status = %q, want consumed", got)
		}
		n, err = q.ConsumeAuthorization(ctx, store.ConsumeAuthorizationParams{ID: authID, SettledAt: &now})
		if err != nil || n != 0 {
			t.Fatalf("second consume = %d, %v; want 0: the token is single-use", n, err)
		}
	})

	t.Run("an expired run may not authorize a forge effect", func(t *testing.T) {
		pool := testdb.New(t)
		q := store.New(pool)
		runID, authID, _, _ := seedRunWithAuthorization(ctx, t, pool)
		if _, err := pool.Exec(ctx,
			`UPDATE runs SET wall_deadline = now() - interval '1 second' WHERE id = $1`, runID); err != nil {
			t.Fatal(err)
		}
		n, err := q.ConsumeAuthorization(ctx, store.ConsumeAuthorizationParams{ID: authID, SettledAt: &now})
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatal("an expired run consumed its authorization: the deadline sweeper would then race a push it cannot cancel")
		}
		if got := statusOf(t, pool, authID); got != "valid" {
			t.Fatalf("status = %q, want valid: a refused consume must leave the row alone", got)
		}
	})
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

func TestGetNodeArtifact_PrefersTheNewerGenerationOverTheNewerTimestamp(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	runID := seedRun(ctx, t, pool)
	q := store.New(pool)

	// The skew the ORDER BY exists for: the generation-2 patch is stamped
	// EARLIER than the generation-1 row it supersedes, because the two rows
	// come from two independent clocks and the host is running behind the
	// database. Provenance has to win over time.
	//
	// Both rows carry the SAME attempt on purpose. Give the newer generation a
	// higher attempt and `attempt DESC` alone would pick it, and the leading
	// `generation DESC` term — the one that stays right when the clocks
	// disagree — would go untested.
	now := time.Now().UTC()
	stale := mustInsertArtifactAt(ctx, t, pool, runID, "implement", 1, 1, "patch", now)
	current := mustInsertArtifactAt(ctx, t, pool, runID, "implement", 1, 2, "patch", now.Add(-time.Hour))

	got, err := q.GetNodeArtifact(ctx, store.GetNodeArtifactParams{
		RunID: runID, NodeID: "implement", Type: "patch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == stale {
		t.Fatal("GetNodeArtifact returned the generation-1 patch: ordering on time hands the gate stale content to approve")
	}
	if got.ID != current || got.Generation != 2 {
		t.Fatalf("want the generation-2 patch %v, got %v (generation %d)", current, got.ID, got.Generation)
	}
}

// A revision seed and the patch the new attempt produces share a GENERATION,
// so `generation DESC` cannot separate them — `attempt DESC` does. The seed is
// written from the DATABASE clock inside the decision transaction and the
// produced row from the injected application clock, so stamp the seed LATER
// here: that is the skew a host running behind the database produces, and
// ordering on created_at would then hand the gate the content the agent
// already replaced.
func TestGetNodeArtifact_PrefersTheProducedAttemptOverItsGenerationMateSeed(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	runID := seedRun(ctx, t, pool)
	q := store.New(pool)

	now := time.Now().UTC()
	produced := mustInsertArtifactAt(ctx, t, pool, runID, "implement", 2, 2, "patch", now.Add(-time.Hour))
	seed := mustInsertArtifactAt(ctx, t, pool, runID, "implement", 0, 2, "patch", now)

	got, err := q.GetNodeArtifact(ctx, store.GetNodeArtifactParams{
		RunID: runID, NodeID: "implement", Type: "patch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == seed {
		t.Fatal("GetNodeArtifact returned the attempt-0 revision seed: selection is ordering on a clock, not on provenance")
	}
	if got.ID != produced || got.Attempt != 2 {
		t.Fatalf("want the produced patch %v, got %v (attempt %d)", produced, got.ID, got.Attempt)
	}
}

// The absence stamp an expired run's settlement takes is scoped to the marker
// its status makes uncertain: the BRANCH while 'pending', the PULL REQUEST once
// 'pushed'. Nothing else in the schema says so, which is why the receipt has to
// clear it.
//
// Left behind, a stamp taken while pending is already older than the grace when
// the pull-request half first probes — so the very first PR-absence sighting
// halts an intent whose create call may still be on the wire, skipping the
// entire grace the 'pushed' arm exists for. The receipt is also the proof the
// stamp was wrong: the branch turned up.
func TestPublishIntents_ARecordedReceiptClearsTheAbsenceStamp(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	q := store.New(pool)
	runID, authID, repoID, credID := seedRunWithAuthorization(ctx, t, pool)

	var intentID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO publish_intents (run_id, node_id, attempt, authorization_id, repo_id,
		                             forge_kind, repo_slug, credential_id,
		                             branch, target_branch, base_sha, tree_hash, title, body,
		                             author_email, commit_message, status, created_at, updated_at)
		VALUES ($1,'publish',1,$2,$3,'github','acme/widgets',$4,
		        'caesar/publish','main','abc','def','t','b','who@example.com','m','pending',now(),now())
		RETURNING id`,
		runID, authID, repoID, credID,
	).Scan(&intentID); err != nil {
		t.Fatal(err)
	}

	// Settlement finds no branch and stamps the first sighting.
	n, err := q.MarkPublicationAbsent(ctx, store.MarkPublicationAbsentParams{ID: intentID, Status: "pending"})
	if err != nil || n != 1 {
		t.Fatalf("first absence sighting: rows=%d err=%v, want 1", n, err)
	}
	// Never refreshed: the grace measures from the FIRST sighting, or it would
	// be pushed away forever and the intent would never settle.
	if n, err = q.MarkPublicationAbsent(ctx, store.MarkPublicationAbsentParams{ID: intentID, Status: "pending"}); err != nil || n != 0 {
		t.Fatalf("second absence sighting: rows=%d err=%v, want 0", n, err)
	}

	// The push turns up after all, and a live driver records it.
	if n, err = q.RecordPushReceipt(ctx, store.RecordPushReceiptParams{
		ID: intentID, HeadSha: pgtype.Text{String: "cafe", Valid: true}, UpdatedAt: time.Now().UTC(),
	}); err != nil || n != 1 {
		t.Fatalf("push receipt: rows=%d err=%v, want 1", n, err)
	}
	var absent *time.Time
	if err := pool.QueryRow(ctx, `SELECT absent_since FROM publish_intents WHERE id = $1`, intentID).Scan(&absent); err != nil {
		t.Fatal(err)
	}
	if absent != nil {
		t.Fatalf("absent_since = %v after the receipt, want NULL: the branch turned up", absent)
	}

	// So the FIRST pull-request-absence probe cannot halt: the grace has not
	// started, let alone elapsed.
	reason := "expired after pushing: no pull request on the forge after the settle grace"
	if n, err = q.HaltStablyAbsentPublication(ctx, store.HaltStablyAbsentPublicationParams{
		ID: intentID, HaltReason: pgtype.Text{String: reason, Valid: true},
		Status: "pushed", GraceSeconds: 30,
	}); err != nil || n != 0 {
		t.Fatalf("first PR-absence probe halted rows=%d (err=%v), want 0: the pull-request grace "+
			"was skipped by a stamp taken while the intent was still pending", n, err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM publish_intents WHERE id = $1`, intentID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pushed" {
		t.Fatalf("intent status = %q, want pushed", status)
	}
}
