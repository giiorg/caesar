// In-package: ancestors, descendants and dominators are unexported graph
// helpers on ExecPlan, and this file is the TWIN of
// backend/internal/workflows/graph_test.go.
//
// The two files exist because internal/arch/rules.go forbids runs from
// importing workflows, so the definitions validator's graph computation is
// necessarily duplicated here. A second dominator computation that disagrees
// with the validator's is exactly the "corruption of a stored version"
// assignGates refuses to paper over, and nothing in the build catches the
// disagreement. So both files cover the SAME four fixture graphs — the
// conveyor, the diamond, the two-root graph and the gate chain — with every
// expectation written as a LITERAL rather than derived from either
// implementation. A change made to one file and not the other must fail.
package runs

import (
	"slices"
	"testing"
)

// twinConveyor mirrors workflows/validate_test.go's conveyor().
func twinConveyor() DefinitionDoc {
	return DefinitionDoc{
		Nodes: []DefNode{
			{ID: "plan", Kind: KindPlan, Harness: "cc", Instructions: "Plan the change."},
			{ID: "impl", Kind: KindImplement, Harness: "cc", Instructions: "Implement the plan."},
			{ID: "review", Kind: KindReview, Harness: "cc", Instructions: "Review the patch."},
			{ID: "gate", Kind: KindGate, Revision: &DefRevision{Target: "impl", MaxRounds: 3}},
			{ID: "publish", Kind: KindGitPublish},
		},
		Edges: []DefEdge{
			{From: "plan", To: "impl", Artifact: ArtifactKindPlan},
			{From: "impl", To: "review", Artifact: ArtifactKindPatch},
			{From: "impl", To: "gate", Artifact: ArtifactKindPatch},
			{From: "review", To: "gate", Artifact: ArtifactKindReviewVerdict},
			{From: "gate", To: "publish", Artifact: ArtifactKindPatch},
		},
	}
}

// twinDiamond mirrors workflows/graph_test.go's diamond: a → b → d and
// a → c → d, so a dominates everything and b, c dominate only themselves.
func twinDiamond() DefinitionDoc {
	return DefinitionDoc{
		Nodes: []DefNode{
			{ID: "a", Kind: KindPlan}, {ID: "b", Kind: KindImplement},
			{ID: "c", Kind: KindImplement}, {ID: "d", Kind: KindGate},
		},
		Edges: []DefEdge{
			{From: "a", To: "b", Artifact: ArtifactKindPlan},
			{From: "a", To: "c", Artifact: ArtifactKindPlan},
			{From: "b", To: "d", Artifact: ArtifactKindPatch},
			{From: "c", To: "d", Artifact: ArtifactKindPatch},
		},
	}
}

// twinMultiRoot mirrors workflows/graph_test.go's multi: two roots feeding
// one node, so nothing dominates x but itself.
func twinMultiRoot() DefinitionDoc {
	return DefinitionDoc{
		Nodes: []DefNode{
			{ID: "r1", Kind: KindPlan}, {ID: "r2", Kind: KindPlan}, {ID: "x", Kind: KindGate},
		},
		Edges: []DefEdge{
			{From: "r1", To: "x", Artifact: ArtifactKindPlan},
			{From: "r2", To: "x", Artifact: ArtifactKindPlan},
		},
	}
}

// twinGateChain mirrors workflows/graph_test.go's impl → gate1 → gate2 →
// publish chain: both gates pass the same patch through.
func twinGateChain() DefinitionDoc {
	return DefinitionDoc{
		Nodes: []DefNode{
			{ID: "impl", Kind: KindImplement, Harness: "cc", Instructions: "x"},
			{ID: "gate1", Kind: KindGate},
			{ID: "gate2", Kind: KindGate},
			{ID: "publish", Kind: KindGitPublish},
		},
		Edges: []DefEdge{
			{From: "impl", To: "gate1", Artifact: ArtifactKindPatch},
			{From: "gate1", To: "gate2", Artifact: ArtifactKindPatch},
			{From: "gate2", To: "publish", Artifact: ArtifactKindPatch},
		},
	}
}

// compiledTwin compiles doc and rebuilds the incoming-edge index the graph
// helpers take, so the tests exercise exactly what Compile hands assignGates.
func compiledTwin(t *testing.T, doc DefinitionDoc) (ExecPlan, map[string][]DefEdge) {
	t.Helper()
	p, err := Compile(doc)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	in := map[string][]DefEdge{}
	for _, e := range doc.Edges {
		in[e.To] = append(in[e.To], e)
	}
	return p, in
}

func sortedIDs(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestAncestors_MatchesTheDefinitionsValidatorOnTheSameGraphs(t *testing.T) {
	cases := []struct {
		name string
		doc  DefinitionDoc
		of   string
		want []string
	}{
		{"conveyor", twinConveyor(), "publish", []string{"gate", "impl", "plan", "review"}},
		{"diamond", twinDiamond(), "d", []string{"a", "b", "c"}},
		{"multi-root", twinMultiRoot(), "x", []string{"r1", "r2"}},
		{"gate chain", twinGateChain(), "publish", []string{"gate1", "gate2", "impl"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, in := compiledTwin(t, tc.doc)
			if got := sortedIDs(p.ancestors(tc.of, in)); !slices.Equal(got, tc.want) {
				t.Fatalf("ancestors(%s) = %v, want %v", tc.of, got, tc.want)
			}
		})
	}

	// Reachability is strict: a node is never its own ancestor.
	p, in := compiledTwin(t, twinConveyor())
	if p.ancestors("plan", in)["plan"] {
		t.Fatal("ancestors must exclude the node itself")
	}
	if got := sortedIDs(p.ancestors("plan", in)); len(got) != 0 {
		t.Fatalf("ancestors(plan) = %v, want none", got)
	}
}

func TestDescendants_MatchesTheDefinitionsValidatorOnTheSameGraphs(t *testing.T) {
	cases := []struct {
		name string
		doc  DefinitionDoc
		of   string
		want []string
	}{
		{"conveyor", twinConveyor(), "plan", []string{"gate", "impl", "publish", "review"}},
		{"diamond", twinDiamond(), "a", []string{"b", "c", "d"}},
		{"multi-root", twinMultiRoot(), "r1", []string{"x"}},
		{"gate chain", twinGateChain(), "impl", []string{"gate1", "gate2", "publish"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := compiledTwin(t, tc.doc)
			if got := sortedIDs(p.descendants(tc.of)); !slices.Equal(got, tc.want) {
				t.Fatalf("descendants(%s) = %v, want %v", tc.of, got, tc.want)
			}
		})
	}

	// Reachability is strict: a node is never its own descendant.
	p, _ := compiledTwin(t, twinConveyor())
	if p.descendants("publish")["publish"] {
		t.Fatal("descendants must exclude the node itself")
	}
	if got := sortedIDs(p.descendants("publish")); len(got) != 0 {
		t.Fatalf("descendants(publish) = %v, want none", got)
	}
}

func TestDominators_MatchesTheDefinitionsValidatorOnTheSameGraphs(t *testing.T) {
	cases := []struct {
		name string
		doc  DefinitionDoc
		of   string
		want []string
	}{
		// Dominance is reflexive here, exactly as in the validator: the
		// virtual root over the in-degree-0 nodes stays implicit.
		{"conveyor publish", twinConveyor(), "publish", []string{"gate", "impl", "plan", "publish"}},
		{"conveyor gate", twinConveyor(), "gate", []string{"gate", "impl", "plan"}},
		{"diamond join", twinDiamond(), "d", []string{"a", "d"}},
		{"diamond branch", twinDiamond(), "b", []string{"a", "b"}},
		{"multi-root join", twinMultiRoot(), "x", []string{"x"}},
		{"gate chain publish", twinGateChain(), "publish", []string{"gate1", "gate2", "impl", "publish"}},
		{"gate chain second gate", twinGateChain(), "gate2", []string{"gate1", "gate2", "impl"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, in := compiledTwin(t, tc.doc)
			if got := sortedIDs(p.dominators(in)[tc.of]); !slices.Equal(got, tc.want) {
				t.Fatalf("dom(%s) = %v, want %v", tc.of, got, tc.want)
			}
		})
	}
}
