//go:build property

package workflows

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"
)

// idPool: small alphabet on purpose — collisions and dense graphs find
// bugs faster than sparse random strings.
var idPool = []string{"a", "b", "c", "d", "e", "f", "g", "h"}

var typePool = []NodeType{NodePlan, NodeImplement, NodeReview, NodeGate, NodeGitPublish, NodeType("bogus")}

// genArbitraryDocument draws documents with NO validity guarantees:
// duplicate ids, unknown types, dangling edges, missing config — the
// validator must classify all of it without panicking.
func genArbitraryDocument(t *rapid.T) Document {
	nNodes := rapid.IntRange(0, 8).Draw(t, "nNodes")
	d := Document{Schema: SchemaVersion}
	for i := 0; i < nNodes; i++ {
		d.Nodes = append(d.Nodes, Node{
			ID:           rapid.SampledFrom(idPool).Draw(t, "id"),
			Type:         rapid.SampledFrom(typePool).Draw(t, "type"),
			Harness:      rapid.SampledFrom([]string{"", "cc", " padded "}).Draw(t, "harness"),
			Instructions: rapid.SampledFrom([]string{"", "do the thing"}).Draw(t, "instructions"),
		})
		if rapid.Bool().Draw(t, "revision") {
			d.Nodes[i].OnRequestChanges = &RevisionTransition{
				Target:    rapid.SampledFrom(idPool).Draw(t, "target"),
				MaxRounds: rapid.IntRange(-1, 12).Draw(t, "rounds"),
			}
		}
	}
	nEdges := rapid.IntRange(0, 12).Draw(t, "nEdges")
	arts := []ArtifactType{ArtifactPlan, ArtifactPatch, ArtifactReviewVerdict, ArtifactType("blob")}
	for i := 0; i < nEdges; i++ {
		d.Edges = append(d.Edges, Edge{
			From:     rapid.SampledFrom(idPool).Draw(t, "from"),
			To:       rapid.SampledFrom(idPool).Draw(t, "to"),
			Artifact: rapid.SampledFrom(arts).Draw(t, "artifact"),
		})
	}
	return d
}

func TestPropValidateIsTotalAndDeterministic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		d := genArbitraryDocument(t)
		first := Validate(d)
		second := Validate(d)
		if diff := cmp.Diff(first, second); diff != "" {
			t.Fatalf("Validate is nondeterministic:\n%s", diff)
		}
	})
}

// genForwardGraph draws a DAG by construction: unique ids n0..nk, edges
// only from lower to higher index.
func genForwardGraph(t *rapid.T, maxNodes int) Document {
	n := rapid.IntRange(2, maxNodes).Draw(t, "n")
	d := Document{Schema: SchemaVersion}
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = string(rune('a' + i))
		d.Nodes = append(d.Nodes, Node{ID: ids[i], Type: NodeImplement})
	}
	nEdges := rapid.IntRange(0, n*(n-1)/2).Draw(t, "nEdges")
	for e := 0; e < nEdges; e++ {
		i := rapid.IntRange(0, n-2).Draw(t, "i")
		j := rapid.IntRange(i+1, n-1).Draw(t, "j")
		d.Edges = append(d.Edges, Edge{From: ids[i], To: ids[j], Artifact: ArtifactPatch})
	}
	return d
}

func TestPropForwardGraphsAreAcyclicUntilClosed(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		d := genForwardGraph(t, 8)
		g := newGraph(d)
		if stuck := g.cycle(); stuck != nil {
			t.Fatalf("forward-edge graph reported cyclic: %v", stuck)
		}
		if len(d.Edges) == 0 {
			return
		}
		// Close any existing edge into a 2-cycle: must be detected.
		e := rapid.SampledFrom(d.Edges).Draw(t, "edge")
		d.Edges = append(d.Edges, Edge{From: e.To, To: e.From, Artifact: ArtifactPatch})
		if stuck := newGraph(d).cycle(); stuck == nil {
			t.Fatalf("closed edge %s↔%s not detected as a cycle", e.From, e.To)
		}
	})
}

// dominatorsBrute enumerates every root→n path (graphs are ≤7 nodes) and
// intersects them — the definition of dominance, computed the slow way.
func dominatorsBrute(g *graph, target string) map[string]bool {
	var paths [][]string
	var dfs func(id string, path []string)
	dfs = func(id string, path []string) {
		path = append(path, id)
		if id == target {
			paths = append(paths, append([]string(nil), path...))
			return
		}
		for _, e := range g.out[id] {
			dfs(e.To, path)
		}
	}
	for _, n := range g.d.Nodes {
		if len(g.in[n.ID]) == 0 {
			dfs(n.ID, nil)
		}
	}
	dom := map[string]bool{}
	if len(paths) == 0 {
		return map[string]bool{target: true}
	}
	for _, cand := range g.d.Nodes {
		onAll := true
		for _, p := range paths {
			found := false
			for _, id := range p {
				if id == cand.ID {
					found = true
					break
				}
			}
			if !found {
				onAll = false
				break
			}
		}
		if onAll {
			dom[cand.ID] = true
		}
	}
	return dom
}

func TestPropDominatorsMatchPathEnumeration(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		g := newGraph(genForwardGraph(t, 7))
		if g.cycle() != nil {
			t.Fatal("forward graph must be acyclic")
		}
		dom := g.dominators()
		for _, n := range g.d.Nodes {
			if diff := cmp.Diff(dominatorsBrute(g, n.ID), dom[n.ID]); diff != "" {
				t.Fatalf("dominators(%s) disagree with path enumeration:\n%s", n.ID, diff)
			}
		}
	})
}

// genConveyor draws a valid linear pipeline: optional plan → implement
// chain → optional review → gate (optional revision to a dominating
// agentic ancestor) → publish.
func genConveyor(t *rapid.T) Document {
	d := Document{Schema: SchemaVersion}
	prevPatch := "" // id of the latest patch producer
	// targets collects legal revision targets: the agentic nodes on the
	// single spine, which all dominate the gate. review is NOT one — the
	// prevPatch→gate edge bypasses it.
	var targets []string

	if rapid.Bool().Draw(t, "withPlan") {
		d.Nodes = append(d.Nodes, Node{ID: "plan", Type: NodePlan, Harness: "cc", Instructions: "p"})
		targets = append(targets, "plan")
	}
	chain := rapid.IntRange(1, 4).Draw(t, "chain")
	for i := 0; i < chain; i++ {
		id := "impl" + string(rune('0'+i))
		d.Nodes = append(d.Nodes, Node{ID: id, Type: NodeImplement, Harness: "cc", Instructions: "i"})
		if i == 0 && len(targets) > 0 {
			d.Edges = append(d.Edges, Edge{From: "plan", To: id, Artifact: ArtifactPlan})
		}
		if prevPatch != "" {
			d.Edges = append(d.Edges, Edge{From: prevPatch, To: id, Artifact: ArtifactPatch})
		}
		prevPatch = id
		targets = append(targets, id)
	}
	withReview := rapid.Bool().Draw(t, "withReview")
	if withReview {
		d.Nodes = append(d.Nodes, Node{ID: "review", Type: NodeReview, Harness: "cc", Instructions: "r"})
		d.Edges = append(d.Edges, Edge{From: prevPatch, To: "review", Artifact: ArtifactPatch})
	}
	gate := Node{ID: "gate", Type: NodeGate}
	if rapid.Bool().Draw(t, "withRevision") {
		gate.OnRequestChanges = &RevisionTransition{
			Target:    rapid.SampledFrom(targets).Draw(t, "target"),
			MaxRounds: rapid.IntRange(1, 10).Draw(t, "rounds"),
		}
	}
	d.Nodes = append(d.Nodes, gate)
	d.Edges = append(d.Edges, Edge{From: prevPatch, To: "gate", Artifact: ArtifactPatch})
	if withReview {
		d.Edges = append(d.Edges, Edge{From: "review", To: "gate", Artifact: ArtifactReviewVerdict})
	}
	d.Nodes = append(d.Nodes, Node{ID: "publish", Type: NodeGitPublish})
	d.Edges = append(d.Edges, Edge{From: "gate", To: "publish", Artifact: ArtifactPatch})
	return d
}

func TestPropLinearConveyorsAlwaysValidate(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		d := genConveyor(t)
		if vs := Validate(d); len(vs) != 0 {
			t.Fatalf("valid conveyor rejected: %+v\ndoc: %s", vs, d.Canonical())
		}
	})
}

func TestPropCanonicalIsAFixedPointWithStableHash(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		d := genConveyor(t)
		re, err := ParseDocument(d.Canonical())
		if err != nil {
			t.Fatalf("reparse of canonical bytes: %v", err)
		}
		if diff := cmp.Diff(d, re); diff != "" {
			t.Fatalf("canonical round trip changed the document:\n%s", diff)
		}
		if d.Hash() != re.Hash() {
			t.Fatal("hash unstable across canonical round trip")
		}
	})
}
