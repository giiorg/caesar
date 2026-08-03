package runs_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// conveyor is the five-node document from Plan 04's header, mirrored.
func conveyor() runs.DefinitionDoc {
	return runs.DefinitionDoc{
		Nodes: []runs.DefNode{
			{ID: "plan", Kind: runs.KindPlan, Harness: "cc", Instructions: "Plan the change."},
			{ID: "impl", Kind: runs.KindImplement, Harness: "cc", Instructions: "Implement the plan."},
			{ID: "review", Kind: runs.KindReview, Harness: "cc", Instructions: "Review the patch."},
			{ID: "gate", Kind: runs.KindGate},
			{ID: "publish", Kind: runs.KindGitPublish},
		},
		Edges: []runs.DefEdge{
			{From: "plan", To: "impl", Artifact: runs.ArtifactKindPlan},
			{From: "impl", To: "review", Artifact: runs.ArtifactKindPatch},
			{From: "impl", To: "gate", Artifact: runs.ArtifactKindPatch},
			{From: "review", To: "gate", Artifact: runs.ArtifactKindReviewVerdict},
			{From: "gate", To: "publish", Artifact: runs.ArtifactKindPatch},
		},
	}
}

func TestCompileOrdersAndBinds(t *testing.T) {
	plan, err := runs.Compile(conveyor())
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, id := range plan.Order {
		pos[id] = i
	}
	for _, pair := range [][2]string{{"plan", "impl"}, {"impl", "review"}, {"review", "gate"}, {"gate", "publish"}} {
		if pos[pair[0]] >= pos[pair[1]] {
			t.Errorf("%s must precede %s in %v", pair[0], pair[1], plan.Order)
		}
	}
	// The publish node's patch input originates at impl, THROUGH the gate.
	pub := plan.Nodes["publish"]
	want := []runs.PlanInput{{Origin: "impl", From: "gate", Artifact: runs.ArtifactKindPatch}}
	if diff := cmp.Diff(want, pub.Inputs); diff != "" {
		t.Errorf("publish inputs (-want +got):\n%s", diff)
	}
	if got := plan.HasUnsupportedNodes(); !cmp.Equal(got, []string{"gate", "publish"}) {
		t.Errorf("unsupported = %v, want [gate publish]", got)
	}
}

// A gate that examines both a plan and a patch must pass each artifact
// back to its true producer. Resolving by edge-list position instead of
// artifact type binds whichever producer happens to be listed first.
func TestCompileResolvesGateOriginByArtifactType(t *testing.T) {
	doc := runs.DefinitionDoc{
		Nodes: []runs.DefNode{
			{ID: "planner", Kind: runs.KindPlan, Harness: "cc"},
			{ID: "impl", Kind: runs.KindImplement, Harness: "cc"},
			{ID: "gate", Kind: runs.KindGate},
			{ID: "publish", Kind: runs.KindGitPublish},
			{ID: "replan", Kind: runs.KindImplement, Harness: "cc"},
		},
		Edges: []runs.DefEdge{
			// The plan edge is listed first on purpose: position must not
			// decide which producer the outgoing patch resolves to.
			{From: "planner", To: "gate", Artifact: runs.ArtifactKindPlan},
			{From: "impl", To: "gate", Artifact: runs.ArtifactKindPatch},
			{From: "gate", To: "publish", Artifact: runs.ArtifactKindPatch},
			{From: "gate", To: "replan", Artifact: runs.ArtifactKindPlan},
		},
	}
	plan, err := runs.Compile(doc)
	if err != nil {
		t.Fatal(err)
	}
	wantPublish := []runs.PlanInput{{Origin: "impl", From: "gate", Artifact: runs.ArtifactKindPatch}}
	if diff := cmp.Diff(wantPublish, plan.Nodes["publish"].Inputs); diff != "" {
		t.Errorf("publish inputs (-want +got):\n%s", diff)
	}
	wantReplan := []runs.PlanInput{{Origin: "planner", From: "gate", Artifact: runs.ArtifactKindPlan}}
	if diff := cmp.Diff(wantReplan, plan.Nodes["replan"].Inputs); diff != "" {
		t.Errorf("replan inputs (-want +got):\n%s", diff)
	}
}

func TestCompileRootsAndReadiness(t *testing.T) {
	plan, err := runs.Compile(conveyor())
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Roots(); !cmp.Equal(got, []string{"plan"}) {
		t.Errorf("roots = %v, want [plan]", got)
	}
	succeeded := map[string]bool{"plan": true}
	if !plan.Ready("impl", succeeded) {
		t.Error("impl must be ready once plan succeeded")
	}
	if plan.Ready("review", succeeded) {
		t.Error("review must wait for impl")
	}
	// gate needs both impl (origin of its examined patch) and review.
	succeeded["impl"] = true
	if plan.Ready("gate", succeeded) {
		t.Error("gate must wait for review")
	}
	succeeded["review"] = true
	if !plan.Ready("gate", succeeded) {
		t.Error("gate must be ready once impl and review succeeded")
	}
}

func TestCompileRejectsCorruptDocuments(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*runs.DefinitionDoc)
		want string
	}{
		{"unknown node kind", func(d *runs.DefinitionDoc) { d.Nodes[0].Kind = "wat" }, "unknown node kind"},
		{"unknown artifact", func(d *runs.DefinitionDoc) { d.Edges[0].Artifact = "wat" }, "unknown artifact"},
		{"edge to nowhere", func(d *runs.DefinitionDoc) { d.Edges[0].To = "ghost" }, "unknown node"},
		{"duplicate node id", func(d *runs.DefinitionDoc) { d.Nodes[1].ID = "plan" }, "duplicate node"},
		{"empty document", func(d *runs.DefinitionDoc) { d.Nodes = nil; d.Edges = nil }, "no nodes"},
		{"cycle", func(d *runs.DefinitionDoc) {
			d.Edges = append(d.Edges, runs.DefEdge{From: "review", To: "impl", Artifact: runs.ArtifactKindPatch})
		}, "cycle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := conveyor()
			tc.mut(&doc)
			_, err := runs.Compile(doc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestAttemptRefString(t *testing.T) {
	ref := runs.AttemptRef{NodeID: "impl", Attempt: 2}
	if got := ref.String(); !strings.HasPrefix(got, "caesar-00000000-") || !strings.HasSuffix(got, "-impl-2") {
		t.Errorf("ref string = %q", got)
	}
}
