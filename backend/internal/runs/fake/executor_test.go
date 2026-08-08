package fake_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

func req(kind, instructions string) runs.ExecutionRequest {
	return runs.ExecutionRequest{
		Ref:          runs.AttemptRef{RunID: uuid.Max, NodeID: "n", Attempt: 1},
		Kind:         kind,
		Instructions: instructions,
		BaseSHA:      "basesha",
		Deadline:     time.Now().Add(time.Minute),
	}
}

func TestScriptedOutputsAreDeterministic(t *testing.T) {
	ex := fake.NewExecutor()
	ctx := context.Background()

	plan1, err := ex.Execute(ctx, req(runs.KindPlan, "Plan the change."))
	if err != nil {
		t.Fatal(err)
	}
	plan2, err := fake.NewExecutor().Execute(ctx, req(runs.KindPlan, "Plan the change."))
	if err != nil {
		t.Fatal(err)
	}
	if plan1.Artifacts[0].Payload["content"] != plan2.Artifacts[0].Payload["content"] {
		t.Error("plan output must be deterministic")
	}

	impl, err := ex.Execute(ctx, req(runs.KindImplement, "Implement."))
	if err != nil {
		t.Fatal(err)
	}
	a := impl.Artifacts[0]
	if a.Type != runs.ArtifactKindPatch {
		t.Fatalf("implement produced %s", a.Type)
	}
	if a.Payload["base_sha"] != "basesha" || a.Payload["diff"] == "" || a.Payload["tree_hash"] == "" {
		t.Errorf("patch payload incomplete: %v", a.Payload)
	}

	rev, err := ex.Execute(ctx, req(runs.KindReview, "Review."))
	if err != nil {
		t.Fatal(err)
	}
	if rev.Artifacts[0].Payload["verdict"] != "approve" {
		t.Errorf("review payload: %v", rev.Artifacts[0].Payload)
	}
}

func TestFailDirectiveCountsAcrossAttempts(t *testing.T) {
	ex := fake.NewExecutor()
	ctx := context.Background()
	r := req(runs.KindImplement, "Implement.\ncaesar-fake: fail attempts=2 class=transient code=harness_error")

	for i := 1; i <= 2; i++ {
		_, err := ex.Execute(ctx, r)
		out := runs.ClassifyOutcome(err)
		if out.Class != runs.OutcomeTransient || out.Code != runs.CodeHarnessError {
			t.Fatalf("execution %d: outcome %+v", i, out)
		}
	}
	if _, err := ex.Execute(ctx, r); err != nil {
		t.Fatalf("third execution should succeed: %v", err)
	}
	if !ex.Completed(r.Ref) {
		t.Error("Completed must report the successful execution")
	}
}

func TestHangDirectiveBlocksUntilCancel(t *testing.T) {
	ex := fake.NewExecutor()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := ex.Execute(ctx, req(runs.KindPlan, "caesar-fake: hang"))
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("hang must return ctx error, got %v", err)
	}
}

func TestInspectReportsCompletedExecutions(t *testing.T) {
	ex := fake.NewExecutor()
	ctx := context.Background()
	r := req(runs.KindPlan, "Plan.")
	if _, err := ex.Execute(ctx, r); err != nil {
		t.Fatal(err)
	}
	res, err := ex.Inspect(ctx, r.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Completed || len(res.Result.Artifacts) != 1 {
		t.Errorf("inspect = %+v", res)
	}
	missing, err := ex.Inspect(ctx, runs.AttemptRef{RunID: uuid.Max, NodeID: "ghost", Attempt: 9})
	if err != nil {
		t.Fatal(err)
	}
	if missing.Completed {
		t.Error("unknown attempt must not report completed")
	}
}

func TestFakeForge(t *testing.T) {
	id := uuid.Max
	f := fake.NewForge(map[uuid.UUID]string{id: "abc"})
	sha, err := f.ResolveHead(context.Background(), runs.RepoRef{ID: id})
	if err != nil || sha != "abc" {
		t.Errorf("ResolveHead = %q, %v", sha, err)
	}
	if _, err := f.ResolveHead(context.Background(), runs.RepoRef{ID: uuid.Nil}); err == nil {
		t.Error("unknown repo must error")
	}
}

func TestInitBareRepo(t *testing.T) {
	sha, err := fake.InitBareRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(sha) != 40 {
		t.Errorf("head sha = %q", sha)
	}
}
