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
	// Plan 08 runtime outputs. Neither is an edge binding: the definitions
	// validator only knows plan/patch/review_verdict, and both of these are
	// written by the control plane rather than produced by a harness.
	ArtifactKindPublication = "publication"
	ArtifactKindGateNote    = "gate_note"
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

// DefRevision is the runs-owned mirror of a gate's bounded
// on_request_changes loop (spec §2). It is not a graph edge — the dataflow
// DAG stays acyclic; the loop is replayed by rematerializing a slice.
type DefRevision struct {
	Target    string
	MaxRounds int
}

type DefNode struct {
	ID           string
	Kind         string
	Harness      string
	Instructions string
	// Revision is set on gates that declare on_request_changes, nil otherwise.
	Revision *DefRevision
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
	Revision     *DefRevision
}

// ExecPlan is a compiled definition: topological order plus per-node
// resolved inputs. It is pure data — the orchestrator interprets it.
type ExecPlan struct {
	Order []string
	Nodes map[string]PlanNode
	// gateFor maps a git-publish node to the gate whose approval authorizes
	// it, and publishFor is its inverse. The document validator already
	// proved the assignment exists and is one-to-one (workflows §2
	// invariants 6 and 7); this re-derives it with the same nearest-eligible
	// rule so the runtime can name the gate a publish must wait for.
	gateFor    map[string]string
	publishFor map[string]string
	// revisionSlice maps a gate declaring on_request_changes to the node ids
	// its loop rewinds, sorted.
	revisionSlice map[string][]string
}

func (p ExecPlan) GateFor(publishNodeID string) (string, bool) {
	g, ok := p.gateFor[publishNodeID]
	return g, ok
}

func (p ExecPlan) PublishFor(gateNodeID string) (string, bool) {
	pub, ok := p.publishFor[gateNodeID]
	return pub, ok
}

func (p ExecPlan) RevisionSlice(gateNodeID string) ([]string, bool) {
	s, ok := p.revisionSlice[gateNodeID]
	return s, ok
}

// ExaminedInput is the single plan-or-patch a gate decides on (spec §2:
// "a gate examines exactly one plan or patch"). Approval binds this
// artifact id and nothing newer.
func (p ExecPlan) ExaminedInput(gateNodeID string) (PlanInput, bool) {
	n, ok := p.Nodes[gateNodeID]
	if !ok || n.Kind != KindGate {
		return PlanInput{}, false
	}
	for _, in := range n.Inputs {
		if in.Artifact == ArtifactKindPlan || in.Artifact == ArtifactKindPatch {
			return in, true
		}
	}
	return PlanInput{}, false
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
		pn := PlanNode{ID: n.ID, Kind: n.Kind, Harness: n.Harness, Instructions: n.Instructions, Revision: n.Revision}
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
			if a.Artifact != b.Artifact {
				return a.Artifact < b.Artifact
			}
			// Two edges may share an origin AND a type and still differ in
			// their direct source — one patch reaching a node through two
			// gates. From is the last discriminator, so this comparison is
			// a total order and compilation is deterministic.
			return a.From < b.From
		})
		nodes[n.ID] = pn
	}
	plan := ExecPlan{Order: order, Nodes: nodes}
	if err := plan.assignGates(in); err != nil {
		return ExecPlan{}, err
	}
	return plan, nil
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
		gate := e.From
		next, ok := passThrough(in[gate], e.Artifact)
		if !ok {
			return "", fmt.Errorf("runs: compile: gate %q has no examined input", gate)
		}
		e = next
	}
	return e.From, nil
}

// passThrough picks the gate input that carries the outgoing artifact:
// the same artifact type where the gate examines one, else the single
// examined work product. Matching on type first keeps a gate that sees
// both a plan and a patch from binding the wrong producer.
//
// Candidates are compared in a fixed order rather than taken as they come:
// `in` is built by ranging over doc.Edges, so its order is the document's,
// and a caller that reorders two equivalent edges must still get the same
// origin. Ties break on (From, To, Artifact) — the whole edge.
func passThrough(inputs []DefEdge, artifact string) (DefEdge, bool) {
	best, found := DefEdge{}, false
	consider := func(e DefEdge) {
		if !found || edgeLess(e, best) {
			best, found = e, true
		}
	}
	for _, ge := range inputs {
		if ge.Artifact == artifact {
			consider(ge)
		}
	}
	if found {
		return best, true
	}
	for _, ge := range inputs {
		if ge.Artifact == ArtifactKindPlan || ge.Artifact == ArtifactKindPatch {
			consider(ge)
		}
	}
	return best, found
}

func edgeLess(a, b DefEdge) bool {
	if a.From != b.From {
		return a.From < b.From
	}
	if a.To != b.To {
		return a.To < b.To
	}
	return a.Artifact < b.Artifact
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

// assignGates re-derives the two graph facts the runtime needs from an
// already-validated document. It is deliberately a re-derivation and not a
// second validation: reaching a state the validator rejects is corruption of
// a stored version, so it returns an error the caller surfaces as
// internal_error rather than a user-facing violation.
func (p *ExecPlan) assignGates(in map[string][]DefEdge) error {
	anc := map[string]map[string]bool{}
	for id := range p.Nodes {
		anc[id] = p.ancestors(id, in)
	}
	dom := p.dominators(in)

	p.gateFor = map[string]string{}
	p.publishFor = map[string]string{}
	for _, pubID := range p.Order {
		if p.Nodes[pubID].Kind != KindGitPublish {
			continue
		}
		var patch PlanInput
		for _, pin := range p.Nodes[pubID].Inputs {
			if pin.Artifact == ArtifactKindPatch {
				patch = pin
				break
			}
		}
		nearest, nearestDepth := "", -1
		for _, gateID := range p.Order {
			if p.Nodes[gateID].Kind != KindGate || !dom[pubID][gateID] {
				continue
			}
			ex, ok := p.ExaminedInput(gateID)
			if !ok || ex.Artifact != ArtifactKindPatch || ex.Origin != patch.Origin {
				continue
			}
			// No patch producer may sit between the gate and the publish, or
			// the approved artifact and the published artifact could diverge.
			divergent := false
			for id := range p.descendants(gateID) {
				if anc[pubID][id] && p.Nodes[id].Kind == KindImplement {
					divergent = true
					break
				}
			}
			if divergent {
				continue
			}
			// dom(publish) is a chain, so |dom(gate)| orders the candidates
			// and the deepest eligible gate is the nearest one.
			if d := len(dom[gateID]); d > nearestDepth {
				nearest, nearestDepth = gateID, d
			}
		}
		if nearest == "" {
			return fmt.Errorf("runs: compile: publish %q has no dominating gate examining its patch", pubID)
		}
		if other, taken := p.publishFor[nearest]; taken {
			return fmt.Errorf("runs: compile: gate %q authorizes both %q and %q", nearest, other, pubID)
		}
		p.gateFor[pubID] = nearest
		p.publishFor[nearest] = pubID
	}

	p.revisionSlice = map[string][]string{}
	for _, gateID := range p.Order {
		n := p.Nodes[gateID]
		if n.Kind != KindGate || n.Revision == nil {
			continue
		}
		target := n.Revision.Target
		if _, ok := p.Nodes[target]; !ok {
			return fmt.Errorf("runs: compile: gate %q rewinds to unknown node %q", gateID, target)
		}
		if !dom[gateID][target] {
			return fmt.Errorf("runs: compile: revision target %q does not dominate gate %q", target, gateID)
		}
		slice := map[string]bool{target: true, gateID: true}
		desc := p.descendants(target)
		for a := range anc[gateID] {
			if desc[a] {
				slice[a] = true
			}
		}
		ids := make([]string, 0, len(slice))
		for id := range slice {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		p.revisionSlice[gateID] = ids
	}
	return nil
}

// ancestors is the strict ancestor set of id.
func (p ExecPlan) ancestors(id string, in map[string][]DefEdge) map[string]bool {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(cur string) {
		for _, e := range in[cur] {
			if !seen[e.From] {
				seen[e.From] = true
				walk(e.From)
			}
		}
	}
	walk(id)
	return seen
}

// descendants is the strict descendant set of id, walked over the plan's
// resolved inputs so it needs no edge index of its own.
func (p ExecPlan) descendants(id string) map[string]bool {
	children := map[string][]string{}
	for _, n := range p.Nodes {
		for _, pin := range n.Inputs {
			children[pin.From] = append(children[pin.From], n.ID)
		}
	}
	seen := map[string]bool{}
	var walk func(string)
	walk = func(cur string) {
		for _, c := range children[cur] {
			if !seen[c] {
				seen[c] = true
				walk(c)
			}
		}
	}
	walk(id)
	return seen
}

// dominators is the classic iterative fixed point over the topological
// order: dom(n) = {n} ∪ ⋂ dom(pred). Order is already topological, so one
// forward pass suffices. It is reflexive, matching the definitions
// validator: dom[pubID][gateID] reads "gateID dominates pubID", and a
// publish is trivially its own dominator.
func (p ExecPlan) dominators(in map[string][]DefEdge) map[string]map[string]bool {
	dom := make(map[string]map[string]bool, len(p.Order))
	for _, id := range p.Order {
		cur := map[string]bool{id: true}
		first := true
		for _, e := range in[id] {
			pd := dom[e.From]
			if first {
				for k := range pd {
					cur[k] = true
				}
				first = false
				continue
			}
			for k := range cur {
				if k != id && !pd[k] {
					delete(cur, k)
				}
			}
		}
		dom[id] = cur
	}
	return dom
}
