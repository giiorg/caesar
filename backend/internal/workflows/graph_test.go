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
	g := newGraph(conveyor())
	if got := ids(g.ancestors("publish")); !slices.Equal(got, []string{"gate", "impl", "plan", "review"}) {
		t.Fatalf("ancestors(publish) = %v", got)
	}
	if got := ids(g.descendants("plan")); !slices.Equal(got, []string{"gate", "impl", "publish", "review"}) {
		t.Fatalf("descendants(plan) = %v", got)
	}
	if g.ancestors("plan")["plan"] || g.descendants("publish")["publish"] {
		t.Fatal("reachability must be strict (exclude the node itself)")
	}
}

func TestGraphDominators_DiamondAndMultiRoot(t *testing.T) {
	// a → b → d, a → c → d: a dominates everything; b and c dominate
	// nothing but themselves.
	diamond := mk(
		[]Node{{ID: "a", Type: NodePlan}, {ID: "b", Type: NodeImplement}, {ID: "c", Type: NodeImplement}, {ID: "d", Type: NodeGate}},
		[]Edge{
			{From: "a", To: "b", Artifact: ArtifactPlan},
			{From: "a", To: "c", Artifact: ArtifactPlan},
			{From: "b", To: "d", Artifact: ArtifactPatch},
			{From: "c", To: "d", Artifact: ArtifactPatch},
		})
	g := newGraph(diamond)
	if g.cycle() != nil {
		t.Fatal("diamond is acyclic")
	}
	dom := g.dominators()
	if got := ids(dom["d"]); !slices.Equal(got, []string{"a", "d"}) {
		t.Fatalf("dom(d) = %v, want [a d]", got)
	}
	if got := ids(dom["b"]); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("dom(b) = %v, want [a b]", got)
	}

	// Two roots feeding one node: nothing dominates x but itself (the
	// virtual root stays implicit).
	multi := mk(
		[]Node{{ID: "r1", Type: NodePlan}, {ID: "r2", Type: NodePlan}, {ID: "x", Type: NodeGate}},
		[]Edge{
			{From: "r1", To: "x", Artifact: ArtifactPlan},
			{From: "r2", To: "x", Artifact: ArtifactPlan},
		})
	g = newGraph(multi)
	if g.cycle() != nil {
		t.Fatal("multi-root is acyclic")
	}
	if got := ids(g.dominators()["x"]); !slices.Equal(got, []string{"x"}) {
		t.Fatalf("dom(x) = %v, want [x]", got)
	}
}

func TestGraphExaminedAndOrigin_WalkThroughGateChains(t *testing.T) {
	// impl → gate1 → gate2 → publish: both gates pass the same patch
	// through; its origin is impl everywhere.
	d := mk(
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
