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
			{ID: "gate", Kind: runs.KindGate, Revision: &runs.DefRevision{Target: "impl", MaxRounds: 2}},
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
}

func TestCompile_AssignsTheDominatingGateToItsPublish(t *testing.T) {
	p, err := runs.Compile(conveyor())
	if err != nil {
		t.Fatal(err)
	}
	gate, ok := p.GateFor("publish")
	if !ok || gate != "gate" {
		t.Fatalf("GateFor(publish) = %q,%v; want gate,true", gate, ok)
	}
	pub, ok := p.PublishFor("gate")
	if !ok || pub != "publish" {
		t.Fatalf("PublishFor(gate) = %q,%v; want publish,true", pub, ok)
	}
	if _, ok := p.GateFor("review"); ok {
		t.Error("only git-publish nodes have an authorizing gate")
	}
}

func TestCompile_RevisionSliceIsTheTargetToGateSpan(t *testing.T) {
	p, err := runs.Compile(conveyor())
	if err != nil {
		t.Fatal(err)
	}
	slice, ok := p.RevisionSlice("gate")
	if !ok {
		t.Fatal("the gate declares a revision transition")
	}
	want := []string{"gate", "impl", "review"}
	if diff := cmp.Diff(want, slice); diff != "" {
		t.Errorf("slice mismatch (-want +got):\n%s", diff)
	}
	// "plan" dominates the target and is NOT rewound: a revision re-does the
	// work between the target and the gate, never the inputs that fed it.
	for _, id := range slice {
		if id == "plan" {
			t.Error("the revision slice must stop at its target")
		}
	}
}

func TestCompile_GateWithoutARevisionHasNoSlice(t *testing.T) {
	doc := conveyor()
	doc.Nodes[3].Revision = nil
	p, err := runs.Compile(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.RevisionSlice("gate"); ok {
		t.Error("a gate with no on_request_changes has no slice")
	}
}

func TestCompile_ExaminedInputIsThePatchTheGateSees(t *testing.T) {
	p, err := runs.Compile(conveyor())
	if err != nil {
		t.Fatal(err)
	}
	in, ok := p.ExaminedInput("gate")
	if !ok {
		t.Fatal("the gate examines exactly one plan or patch")
	}
	if in.Artifact != runs.ArtifactKindPatch || in.Origin != "impl" {
		t.Fatalf("examined = %+v; want the implement patch", in)
	}
}

func TestCompile_InputOrderBreaksTiesOnFromSoTwoSameTypeInputsAreDeterministic(t *testing.T) {
	// Carried forward from Plan 05's final review: sorting on (Origin,
	// Artifact) alone left two edges from DIFFERENT direct sources through
	// the same origin in an order the map iteration decided.
	doc := runs.DefinitionDoc{
		Nodes: []runs.DefNode{
			{ID: "implement", Kind: runs.KindImplement, Harness: "h", Instructions: "x"},
			{ID: "gate_b", Kind: runs.KindGate},
			{ID: "gate_a", Kind: runs.KindGate},
			{ID: "sink", Kind: runs.KindReview, Harness: "h", Instructions: "x"},
		},
		Edges: []runs.DefEdge{
			{From: "implement", To: "gate_b", Artifact: runs.ArtifactKindPatch},
			{From: "implement", To: "gate_a", Artifact: runs.ArtifactKindPatch},
			{From: "gate_b", To: "sink", Artifact: runs.ArtifactKindPatch},
			{From: "gate_a", To: "sink", Artifact: runs.ArtifactKindPatch},
		},
	}
	first, err := runs.Compile(doc)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := runs.Compile(doc)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(first.Nodes["sink"].Inputs, again.Nodes["sink"].Inputs); diff != "" {
			t.Fatalf("compile %d produced a different input order (-first +again):\n%s", i, diff)
		}
	}
	got := []string{first.Nodes["sink"].Inputs[0].From, first.Nodes["sink"].Inputs[1].From}
	if diff := cmp.Diff([]string{"gate_a", "gate_b"}, got); diff != "" {
		t.Errorf("From tiebreaker mismatch (-want +got):\n%s", diff)
	}
}

// assignGates runs on every Compile, so it is a new way for a stored
// version to fail to compile. Each case is a document the definitions
// validator already rejects; reaching one here is corruption, and the
// compiler must say which invariant broke rather than hand the runtime a
// plan with no authorizing gate.
func TestCompile_RejectsAnUnassignableGraph(t *testing.T) {
	cases := []struct {
		name string
		doc  runs.DefinitionDoc
		want string
	}{
		{
			name: "publish with no gate above it",
			doc: runs.DefinitionDoc{
				Nodes: []runs.DefNode{
					{ID: "impl", Kind: runs.KindImplement, Harness: "cc"},
					{ID: "publish", Kind: runs.KindGitPublish},
				},
				Edges: []runs.DefEdge{{From: "impl", To: "publish", Artifact: runs.ArtifactKindPatch}},
			},
			want: `publish "publish" has no dominating gate examining its patch`,
		},
		{
			name: "one gate over two publishes",
			doc: runs.DefinitionDoc{
				Nodes: []runs.DefNode{
					{ID: "impl", Kind: runs.KindImplement, Harness: "cc"},
					{ID: "gate", Kind: runs.KindGate},
					{ID: "pub1", Kind: runs.KindGitPublish},
					{ID: "pub2", Kind: runs.KindGitPublish},
				},
				Edges: []runs.DefEdge{
					{From: "impl", To: "gate", Artifact: runs.ArtifactKindPatch},
					{From: "gate", To: "pub1", Artifact: runs.ArtifactKindPatch},
					{From: "gate", To: "pub2", Artifact: runs.ArtifactKindPatch},
				},
			},
			want: `gate "gate" authorizes both "pub1" and "pub2"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runs.Compile(tc.doc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestCompile_RejectsARevisionTargetItCannotRewindTo(t *testing.T) {
	cases := []struct {
		name, target, want string
	}{
		{"unknown node", "ghost", `gate "gate" rewinds to unknown node "ghost"`},
		// review is an ancestor of the gate but not a dominator: rewinding
		// to it would leave the patch the gate examined untouched.
		{"not a dominator", "review", `revision target "review" does not dominate gate "gate"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := conveyor()
			doc.Nodes[3].Revision = &runs.DefRevision{Target: tc.target, MaxRounds: 2}
			_, err := runs.Compile(doc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestCompile_PassThroughPrefersTheTypedInputDeterministically(t *testing.T) {
	// A gate examining a patch and consuming a review verdict passes the
	// PATCH through, whatever order the edges arrive in.
	p, err := runs.Compile(conveyor())
	if err != nil {
		t.Fatal(err)
	}
	in := p.Nodes["publish"].Inputs
	if len(in) != 1 || in[0].Origin != "impl" || in[0].Artifact != runs.ArtifactKindPatch {
		t.Fatalf("publish inputs = %+v; want the implement patch through the gate", in)
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

// The schema version is the ONE thing Compile carries through untouched, and
// the whole payload-contract compatibility rule rests on it: ValidateArtifactPayload
// is asked which document version this run pinned, and a plan that answered 0
// for every run would silently switch validation off everywhere.
//
// The versions are literal, not runs' or workflows' constants: a test that
// reads the constant it is pinning cannot fail when the constant is wrong.
func TestCompileCarriesTheDocumentSchemaVersion(t *testing.T) {
	for _, schema := range []int{1, 2} {
		doc := conveyor()
		doc.Schema = schema
		plan, err := runs.Compile(doc)
		if err != nil {
			t.Fatal(err)
		}
		if plan.DocSchemaVersion != schema {
			t.Errorf("DocSchemaVersion = %d, want %d", plan.DocSchemaVersion, schema)
		}
	}
}
