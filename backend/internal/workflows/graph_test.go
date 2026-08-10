// This file is the TWIN of backend/internal/runs/compile_graph_test.go.
//
// internal/arch/rules.go forbids runs from importing workflows, so the
// orchestrator re-owns ancestors, descendants and dominators on ExecPlan. A
// second dominator computation that disagrees with this validator's would be
// caught by nothing in the build. So both files cover the SAME four fixture
// graphs — the conveyor, the diamond, the two-root graph and the gate chain —
// with every expectation written as a LITERAL rather than derived from either
// implementation. A change made to one file and not the other must fail.
package workflows

import (
	"slices"
	"testing"
)

// mk builds a bare document for graph tests: shape-level config is
// irrelevant here, only ids, types, and edges matter.
func mk(nodes []Node, edges []Edge) Document {
	return Document{Schema: SchemaVersion, Nodes: nodes, Edges: edges}
}

// diamond is a → b → d and a → c → d: a dominates everything, and b and c
// dominate nothing but themselves.
func diamond() Document {
	return mk(
		[]Node{{ID: "a", Type: NodePlan}, {ID: "b", Type: NodeImplement}, {ID: "c", Type: NodeImplement}, {ID: "d", Type: NodeGate}},
		[]Edge{
			{From: "a", To: "b", Artifact: ArtifactPlan},
			{From: "a", To: "c", Artifact: ArtifactPlan},
			{From: "b", To: "d", Artifact: ArtifactPatch},
			{From: "c", To: "d", Artifact: ArtifactPatch},
		})
}

// multiRoot is two roots feeding one node: nothing dominates x but itself,
// because the virtual root stays implicit.
func multiRoot() Document {
	return mk(
		[]Node{{ID: "r1", Type: NodePlan}, {ID: "r2", Type: NodePlan}, {ID: "x", Type: NodeGate}},
		[]Edge{
			{From: "r1", To: "x", Artifact: ArtifactPlan},
			{From: "r2", To: "x", Artifact: ArtifactPlan},
		})
}

// gateChain is impl → gate1 → gate2 → publish: both gates pass the same
// patch through, so its origin is impl everywhere.
func gateChain() Document {
	return mk(
		[]Node{
			{ID: "impl", Type: NodeImplement, Harness: "cc", Instructions: "x"},
			{ID: "gate1", Type: NodeGate},
			{ID: "gate2", Type: NodeGate},
			{ID: "publish", Type: NodeGitPublish},
		},
		[]Edge{
			{From: "impl", To: "gate1", Artifact: ArtifactPatch},
			{From: "gate1", To: "gate2", Artifact: ArtifactPatch},
			{From: "gate2", To: "publish", Artifact: ArtifactPatch},
		})
}

// orderedGraph builds the graph and records the topological order the
// dominator pass needs.
func orderedGraph(t *testing.T, d Document) *graph {
	t.Helper()
	g := newGraph(d)
	if stuck := g.cycle(); stuck != nil {
		t.Fatalf("fixture reported cyclic: %v", stuck)
	}
	return g
}

func ids(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestGraphCycle_AcceptsTheConveyorAndOrdersIt(t *testing.T) {
	g := newGraph(conveyor())
	if got := g.cycle(); got != nil {
		t.Fatalf("conveyor reported cyclic: %v", got)
	}
	pos := map[string]int{}
	for i, id := range g.order {
		pos[id] = i
	}
	for _, e := range g.d.Edges {
		if pos[e.From] >= pos[e.To] {
			t.Fatalf("order %v is not topological: %s→%s", g.order, e.From, e.To)
		}
	}
}

func TestGraphCycle_ReportsBackEdges(t *testing.T) {
	d := conveyor()
	d.Edges = append(d.Edges, Edge{From: "publish", To: "plan", Artifact: ArtifactPatch})
	got := newGraph(d).cycle()
	if got == nil {
		t.Fatal("cycle not detected")
	}
	if !slices.Contains(got, "plan") || !slices.IsSorted(got) {
		t.Fatalf("cycle report %v: want sorted, containing the cycle members", got)
	}
}

func TestGraphReachability_StrictAncestorsAndDescendants(t *testing.T) {
	cases := []struct {
		name          string
		doc           Document
		anc, desc     []string
		ancOf, descOf string
	}{
		{
			name: "conveyor", doc: conveyor(),
			ancOf: "publish", anc: []string{"gate", "impl", "plan", "review"},
			descOf: "plan", desc: []string{"gate", "impl", "publish", "review"},
		},
		{
			name: "diamond", doc: diamond(),
			ancOf: "d", anc: []string{"a", "b", "c"},
			descOf: "a", desc: []string{"b", "c", "d"},
		},
		{
			name: "multi-root", doc: multiRoot(),
			ancOf: "x", anc: []string{"r1", "r2"},
			descOf: "r1", desc: []string{"x"},
		},
		{
			name: "gate chain", doc: gateChain(),
			ancOf: "publish", anc: []string{"gate1", "gate2", "impl"},
			descOf: "impl", desc: []string{"gate1", "gate2", "publish"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newGraph(tc.doc)
			if got := ids(g.ancestors(tc.ancOf)); !slices.Equal(got, tc.anc) {
				t.Fatalf("ancestors(%s) = %v, want %v", tc.ancOf, got, tc.anc)
			}
			if got := ids(g.descendants(tc.descOf)); !slices.Equal(got, tc.desc) {
				t.Fatalf("descendants(%s) = %v, want %v", tc.descOf, got, tc.desc)
			}
		})
	}

	g := newGraph(conveyor())
	if g.ancestors("plan")["plan"] || g.descendants("publish")["publish"] {
		t.Fatal("reachability must be strict (exclude the node itself)")
	}
	if got := ids(g.ancestors("plan")); len(got) != 0 {
		t.Fatalf("ancestors(plan) = %v, want none", got)
	}
	if got := ids(g.descendants("publish")); len(got) != 0 {
		t.Fatalf("descendants(publish) = %v, want none", got)
	}
}

func TestGraphDominators_DiamondAndMultiRoot(t *testing.T) {
	// Dominance is reflexive: dom(n) always contains n, and the virtual
	// root over the in-degree-0 nodes stays implicit.
	cases := []struct {
		name string
		doc  Document
		of   string
		want []string
	}{
		{"conveyor publish", conveyor(), "publish", []string{"gate", "impl", "plan", "publish"}},
		{"conveyor gate", conveyor(), "gate", []string{"gate", "impl", "plan"}},
		{"diamond join", diamond(), "d", []string{"a", "d"}},
		{"diamond branch", diamond(), "b", []string{"a", "b"}},
		{"multi-root join", multiRoot(), "x", []string{"x"}},
		{"gate chain publish", gateChain(), "publish", []string{"gate1", "gate2", "impl", "publish"}},
		{"gate chain second gate", gateChain(), "gate2", []string{"gate1", "gate2", "impl"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := orderedGraph(t, tc.doc)
			if got := ids(g.dominators()[tc.of]); !slices.Equal(got, tc.want) {
				t.Fatalf("dom(%s) = %v, want %v", tc.of, got, tc.want)
			}
		})
	}
}

func TestGraphExaminedAndOrigin_WalkThroughGateChains(t *testing.T) {
	d := gateChain()
	g := newGraph(d)
	if ex := g.examined("gate1"); ex.From != "impl" || ex.Artifact != ArtifactPatch {
		t.Fatalf("examined(gate1) = %+v", ex)
	}
	if ex := g.examined("gate2"); ex.From != "gate1" {
		t.Fatalf("examined(gate2) = %+v", ex)
	}
	last := d.Edges[2]
	if got := g.origin(last); got != "impl" {
		t.Fatalf("origin(gate2→publish) = %q, want impl", got)
	}
}
