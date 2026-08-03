package runs

import (
	"fmt"
	"sort"
)

// Node and artifact kinds mirror the Plan 04 document values verbatim.
// The mirror is runs-owned: bounded contexts never import each other,
// and app's DefinitionSource adapter maps typed workflows values onto
// these strings mechanically.
const (
	KindPlan       = "plan"
	KindImplement  = "implement"
	KindReview     = "review"
	KindGate       = "gate"
	KindGitPublish = "git-publish"

	ArtifactKindPlan            = "plan"
	ArtifactKindPatch           = "patch"
	ArtifactKindReviewVerdict   = "review_verdict"
	ArtifactKindLogRef          = "log_ref"
	ArtifactKindFailureSnapshot = "failure_snapshot"
)

func validNodeKind(k string) bool {
	switch k {
	case KindPlan, KindImplement, KindReview, KindGate, KindGitPublish:
		return true
	}
	return false
}

func validEdgeArtifact(a string) bool {
	return a == ArtifactKindPlan || a == ArtifactKindPatch || a == ArtifactKindReviewVerdict
}

// DefinitionDoc is the runs-owned mirror of a stored definition.
type DefinitionDoc struct {
	Nodes []DefNode
	Edges []DefEdge
}

type DefNode struct {
	ID           string
	Kind         string
	Harness      string
	Instructions string
}

type DefEdge struct {
	From     string
	To       string
	Artifact string
}

// PlanInput is one resolved binding: the artifact type, the direct edge
// source, and the origin — the producing node resolved through
// pass-through gates (Plan 04 decision 2).
type PlanInput struct {
	Origin   string
	From     string
	Artifact string
}

type PlanNode struct {
	ID           string
	Kind         string
	Harness      string
	Instructions string
	Inputs       []PlanInput
}

// ExecPlan is a compiled definition: topological order plus per-node
// resolved inputs. It is pure data — the orchestrator interprets it.
type ExecPlan struct {
	Order []string
	Nodes map[string]PlanNode
}

// Compile validates the mirror just enough to execute it and resolves
// order and origins. A stored version that fails here is corruption:
// callers surface internal_error, never a user-facing validation error.
func Compile(doc DefinitionDoc) (ExecPlan, error) {
	if len(doc.Nodes) == 0 {
		return ExecPlan{}, fmt.Errorf("runs: compile: no nodes")
	}
	byID := make(map[string]DefNode, len(doc.Nodes))
	for _, n := range doc.Nodes {
		if _, dup := byID[n.ID]; dup {
			return ExecPlan{}, fmt.Errorf("runs: compile: duplicate node %q", n.ID)
		}
		if !validNodeKind(n.Kind) {
			return ExecPlan{}, fmt.Errorf("runs: compile: unknown node kind %q on %q", n.Kind, n.ID)
		}
		byID[n.ID] = n
	}
	in := map[string][]DefEdge{}
	out := map[string][]DefEdge{}
	for _, e := range doc.Edges {
		if !validEdgeArtifact(e.Artifact) {
			return ExecPlan{}, fmt.Errorf("runs: compile: unknown artifact %q on %s->%s", e.Artifact, e.From, e.To)
		}
		if _, ok := byID[e.From]; !ok {
			return ExecPlan{}, fmt.Errorf("runs: compile: unknown node %q in edge", e.From)
		}
		if _, ok := byID[e.To]; !ok {
			return ExecPlan{}, fmt.Errorf("runs: compile: unknown node %q in edge", e.To)
		}
		in[e.To] = append(in[e.To], e)
		out[e.From] = append(out[e.From], e)
	}

	order, err := topoOrder(doc.Nodes, in, out)
	if err != nil {
		return ExecPlan{}, err
	}

	nodes := make(map[string]PlanNode, len(doc.Nodes))
	for _, n := range doc.Nodes {
		pn := PlanNode{ID: n.ID, Kind: n.Kind, Harness: n.Harness, Instructions: n.Instructions}
		for _, e := range in[n.ID] {
			origin, err := resolveOrigin(byID, in, e)
			if err != nil {
				return ExecPlan{}, err
			}
			pn.Inputs = append(pn.Inputs, PlanInput{Origin: origin, From: e.From, Artifact: e.Artifact})
		}
		sort.Slice(pn.Inputs, func(i, j int) bool {
			a, b := pn.Inputs[i], pn.Inputs[j]
			if a.Origin != b.Origin {
				return a.Origin < b.Origin
			}
			return a.Artifact < b.Artifact
		})
		nodes[n.ID] = pn
	}
	return ExecPlan{Order: order, Nodes: nodes}, nil
}

// topoOrder is Kahn's algorithm with lexicographic tie-breaking — the
// same shape as the definitions validator, re-owned here because
// contexts never share code across the boundary.
func topoOrder(nodes []DefNode, in, out map[string][]DefEdge) ([]string, error) {
	deg := make(map[string]int, len(nodes))
	var ready []string
	for _, n := range nodes {
		deg[n.ID] = len(in[n.ID])
		if deg[n.ID] == 0 {
			ready = append(ready, n.ID)
		}
	}
	order := make([]string, 0, len(nodes))
	for len(ready) > 0 {
		sort.Strings(ready)
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		for _, e := range out[id] {
			deg[e.To]--
			if deg[e.To] == 0 {
				ready = append(ready, e.To)
			}
		}
	}
	if len(order) != len(nodes) {
		return nil, fmt.Errorf("runs: compile: cycle in stored definition")
	}
	return order, nil
}

// resolveOrigin walks an edge back through pass-through gates to the
// node that actually produced the artifact. Acyclicity bounds the walk;
// a gate missing its examined input is corruption.
func resolveOrigin(byID map[string]DefNode, in map[string][]DefEdge, e DefEdge) (string, error) {
	for byID[e.From].Kind == KindGate {
		found := false
		for _, ge := range in[e.From] {
			if ge.Artifact == ArtifactKindPlan || ge.Artifact == ArtifactKindPatch {
				e, found = ge, true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("runs: compile: gate %q has no examined input", e.From)
		}
	}
	return e.From, nil
}

// Roots are the nodes materialized when a run starts.
func (p ExecPlan) Roots() []string {
	var roots []string
	for id, n := range p.Nodes {
		if len(n.Inputs) == 0 {
			roots = append(roots, id)
		}
	}
	sort.Strings(roots)
	return roots
}

// Ready reports whether every input origin has succeeded. Readiness is
// evaluated at outcome time; pending rows exist only for ready nodes.
func (p ExecPlan) Ready(nodeID string, succeeded map[string]bool) bool {
	n, ok := p.Nodes[nodeID]
	if !ok {
		return false
	}
	for _, input := range n.Inputs {
		if !succeeded[input.Origin] {
			return false
		}
		if p.Nodes[input.From].Kind == KindGate && !succeeded[input.From] {
			return false
		}
	}
	return true
}

// HasUnsupportedNodes lists gate/git-publish node ids — Plan 05 intake
// rejects documents containing them (plan decision 2; Plan 08 deletes
// the call site).
func (p ExecPlan) HasUnsupportedNodes() []string {
	var ids []string
	for id, n := range p.Nodes {
		if n.Kind == KindGate || n.Kind == KindGitPublish {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
