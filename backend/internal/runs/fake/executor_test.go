package fake_test

import (
	"context"
	"encoding/base64"
	"path/filepath"
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

	const planInstructions = "Plan the change."
	plan1, err := ex.Execute(ctx, req(runs.KindPlan, planInstructions))
	if err != nil {
		t.Fatal(err)
	}
	plan2, err := fake.NewExecutor().Execute(ctx, req(runs.KindPlan, planInstructions))
	if err != nil {
		t.Fatal(err)
	}
	// PINNED TO A LITERAL FIRST, and that ordering is the point. Comparing the
	// two emissions to each other proves determinism — including the
	// determinism of emitting NOTHING. MEASURED: deleting `content` from the
	// fake's plan payload degraded this to nil == nil and left the whole
	// backend suite, unit and integration, green. The literal is what makes one
	// assertion here load-bearing on the emitted value.
	//
	// It is derived from planInstructions rather than written out, so the
	// expectation cannot drift away from the request that produces it.
	const wantContent = "plan: " + planInstructions
	if got := plan1.Artifacts[0].Payload["content"]; got != wantContent {
		t.Errorf("plan content = %v, want %q", got, wantContent)
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

// scripted reads the FIRST LINE of the instructions it was handed, and since
// Plan 12a the orchestrator appends an output-shape block to them. The two
// facts meet on a node whose instructions are a single line with no trailing
// newline — the ordinary case — where a block that did not open with its own
// newline would be glued onto that line and every assertion about the fake's
// plan content would shift.
//
// The REAL block is used, never a hand-written stand-in: the property under
// test is a property of runs.OutputShapeInstruction's text, and a local copy
// would keep passing after that text lost its leading blank line.
func TestAppendedOutputShapeLeavesScriptedContentAlone(t *testing.T) {
	const own = "Plan the change."
	block := runs.OutputShapeInstruction(2, runs.KindPlan)
	if block == "" {
		t.Fatal("a schema-2 plan node gets no output-shape block; this test would be vacuous")
	}
	ctx := context.Background()

	bare, err := fake.NewExecutor().Execute(ctx, req(runs.KindPlan, own))
	if err != nil {
		t.Fatal(err)
	}
	appended, err := fake.NewExecutor().Execute(ctx, req(runs.KindPlan, own+block))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"content", "summary"} {
		if got, want := appended.Artifacts[0].Payload[key], bare.Artifacts[0].Payload[key]; got != want {
			t.Errorf("%s = %q with the block appended, %q without", key, got, want)
		}
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

// The two implement payload shapes are materially different, and this is the
// only test that says so on its own.
//
// Registering a Remote switches the implement artifact from the historical
// SYNTHETIC patch (raw-text diff, a sha256 of the diff standing in for a tree
// hash — unpublishable) to the PUBLISHABLE one (base64 canonical wire form, a
// real git tree oid). Which shape a fixture gets is therefore decided by
// whether it called Remote, and nothing else.
//
// Written as insurance against a specific refactor that keeps being proposed:
// collapsing the golden environment's builders into one with a wire-remote
// FLAG argument. The flag is the trap — it turns "which patch shape" into a
// call-site boolean nobody reads — and the branch it selects is otherwise
// asserted on in exactly one place tree-wide (the golden seed test). If that
// collapse is ever taken and the boolean is threaded through wrong, this fails
// in ten lines instead of somewhere unrelated.
func TestRegisteringARemoteChangesTheImplementPatchShape(t *testing.T) {
	dir := t.TempDir()
	base, err := fake.InitBareRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(dir, "bare.git")
	repoID := uuid.MustParse("019fe7b7-6e37-7d9c-97db-d48907ccaff2")

	r := req(runs.KindImplement, "Implement.")
	r.Repo = runs.RepoRef{ID: repoID}
	r.BaseSHA = base

	synthetic, err := fake.NewExecutor().Execute(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	withRemote := fake.NewExecutor()
	withRemote.Remote(repoID, bare)
	publishable, err := withRemote.Execute(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}

	s, p := synthetic.Artifacts[0].Payload, publishable.Artifacts[0].Payload
	if s["diff"] == p["diff"] {
		t.Errorf("both shapes produced the same diff %q: Remote is supposed to select between a "+
			"raw-text synthetic patch and the base64 canonical wire form", s["diff"])
	}
	if s["tree_hash"] == p["tree_hash"] {
		t.Errorf("both shapes produced the same tree hash %q: the synthetic one digests the diff, "+
			"the publishable one is a git tree object id", s["tree_hash"])
	}
	// And name each side, so a swap is a failure rather than a still-passing
	// difference.
	if !strings.HasPrefix(s["diff"].(string), "--- a/") {
		t.Errorf("no remote registered: diff = %q, want the raw-text synthetic patch", s["diff"])
	}
	raw, err := base64.StdEncoding.DecodeString(p["diff"].(string))
	if err != nil || !strings.Contains(string(raw), "+++ b/") {
		t.Errorf("remote registered: diff = %q, want base64 of a patch (decode err %v)", p["diff"], err)
	}
	if len(p["tree_hash"].(string)) != 40 {
		t.Errorf("remote registered: tree_hash = %q, want a 40-hex git tree object id", p["tree_hash"])
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
