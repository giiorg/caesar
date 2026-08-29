package workflows

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Violation is one validation failure. Path addresses the offending
// element in the document ("nodes[2].harness", "edges[0]", "nodes");
// the API maps violations onto problem-envelope field errors verbatim.
type Violation struct {
	Path    string
	Message string
}

// Validation pins (plan header). Byte counts, house convention.
const (
	maxNodes           = 50
	maxEdges           = 200
	maxInstructionsLen = 8192
	maxHarnessLen      = 100
	maxRevisionRounds  = 10
	// minRevisionRounds was the literal 1 inside validateNodeConfig's bound
	// check. It is named because the §10.9 descriptor now publishes this bound
	// as MinInt and the editor's rounds input enforces it as
	// MIN_REVISION_ROUNDS — three spellings of one number, and a literal is how
	// the validator and the control it drives drift by one.
	minRevisionRounds = 1
)

var nodeIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// produces is the fixed output signature of the producing types. Gates
// pass through their examined type (checked in the structural phase);
// git-publish produces nothing.
var produces = map[NodeType]ArtifactType{
	NodePlan:      ArtifactPlan,
	NodeImplement: ArtifactPatch,
	NodeReview:    ArtifactReviewVerdict,
}

// validateShape checks everything checkable without graph traversal:
// counts, ids, per-type config, edge endpoints, and the binding
// signature table from the plan header. Violations come back in
// deterministic document order.
func validateShape(d Document) []Violation {
	var vs []Violation
	if len(d.Nodes) == 0 {
		return []Violation{{Path: "nodes", Message: "a definition needs at least one node"}}
	}
	if len(d.Nodes) > maxNodes {
		vs = append(vs, Violation{Path: "nodes", Message: fmt.Sprintf("at most %d nodes", maxNodes)})
	}
	if len(d.Edges) > maxEdges {
		vs = append(vs, Violation{Path: "edges", Message: fmt.Sprintf("at most %d edges", maxEdges)})
	}

	index := make(map[string]int, len(d.Nodes))
	for i, n := range d.Nodes {
		path := fmt.Sprintf("nodes[%d]", i)
		if !nodeIDPattern.MatchString(n.ID) {
			vs = append(vs, Violation{Path: path + ".id", Message: "must match ^[a-z][a-z0-9_-]{0,63}$"})
		} else if _, dup := index[n.ID]; dup {
			vs = append(vs, Violation{Path: path + ".id", Message: fmt.Sprintf("duplicate node id %q", n.ID)})
		} else {
			index[n.ID] = i
		}
		vs = append(vs, validateNodeConfig(path, n)...)
	}

	seen := make(map[Edge]bool, len(d.Edges))
	// wellFormed is the deduplicated edge list the multiplicity phase counts
	// over. Counting d.Edges again would charge a duplicate edge twice, so one
	// mistake read as two violations: the duplicate AND a multiplicity breach.
	wellFormed := make([]Edge, 0, len(d.Edges))
	for i, e := range d.Edges {
		path := fmt.Sprintf("edges[%d]", i)
		ok := true
		if _, exists := index[e.From]; !exists {
			vs = append(vs, Violation{Path: path + ".from", Message: fmt.Sprintf("unknown node %q", e.From)})
			ok = false
		}
		if _, exists := index[e.To]; !exists {
			vs = append(vs, Violation{Path: path + ".to", Message: fmt.Sprintf("unknown node %q", e.To)})
			ok = false
		}
		if !e.Artifact.Valid() {
			vs = append(vs, Violation{Path: path + ".artifact", Message: fmt.Sprintf("unknown artifact type %q", e.Artifact)})
			ok = false
		}
		if !ok {
			continue
		}
		if e.From == e.To {
			vs = append(vs, Violation{Path: path, Message: "self edges are not allowed"})
			continue
		}
		if seen[e] {
			vs = append(vs, Violation{Path: path, Message: fmt.Sprintf("duplicate edge %s→%s (%s)", e.From, e.To, e.Artifact)})
			continue
		}
		seen[e] = true
		wellFormed = append(wellFormed, e)

		switch from := d.Nodes[index[e.From]]; {
		case from.Type == NodeGitPublish:
			vs = append(vs, Violation{Path: path, Message: "git-publish produces nothing; it cannot have outgoing edges"})
		case from.Type.Agentic() && e.Artifact != produces[from.Type]:
			vs = append(vs, Violation{Path: path + ".artifact", Message: fmt.Sprintf("%s produces %s, not %s", from.Type, produces[from.Type], e.Artifact)})
		}
		// Gate sources pass through their examined type — checked in the
		// structural phase once the examined edge is known.
	}

	// Revision target existence check.
	for i, n := range d.Nodes {
		if n.Type == NodeGate && n.OnRequestChanges != nil {
			if _, ok := index[n.OnRequestChanges.Target]; !ok {
				vs = append(vs, Violation{
					Path:    fmt.Sprintf("nodes[%d].on_request_changes.target", i),
					Message: fmt.Sprintf("unknown node %q", n.OnRequestChanges.Target),
				})
			}
		}
	}

	// Consumption multiplicities per node, counted over well-formed edges.
	in := map[string]map[ArtifactType]int{}
	for _, e := range wellFormed {
		if in[e.To] == nil {
			in[e.To] = map[ArtifactType]int{}
		}
		in[e.To][e.Artifact]++
	}
	for i, n := range d.Nodes {
		if index[n.ID] != i { // duplicate id row: counted once, on the first
			continue
		}
		vs = append(vs, validateConsumption(fmt.Sprintf("nodes[%d]", i), n, in[n.ID])...)
	}
	return vs
}

func validateNodeConfig(path string, n Node) []Violation {
	var vs []Violation
	if !n.Type.Valid() {
		return []Violation{{Path: path + ".type", Message: fmt.Sprintf("unknown node type %q", n.Type)}}
	}
	if n.Type.Agentic() {
		switch {
		case n.Harness == "":
			vs = append(vs, Violation{Path: path + ".harness", Message: "required on agentic nodes"})
		case strings.TrimSpace(n.Harness) != n.Harness:
			vs = append(vs, Violation{Path: path + ".harness", Message: "must not have surrounding whitespace"})
		case len(n.Harness) > maxHarnessLen:
			vs = append(vs, Violation{Path: path + ".harness", Message: fmt.Sprintf("at most %d bytes", maxHarnessLen)})
		}
		switch {
		case n.Instructions == "":
			vs = append(vs, Violation{Path: path + ".instructions", Message: "required on agentic nodes"})
		case len(n.Instructions) > maxInstructionsLen:
			vs = append(vs, Violation{Path: path + ".instructions", Message: fmt.Sprintf("at most %d bytes", maxInstructionsLen)})
		}
	} else {
		if n.Harness != "" {
			vs = append(vs, Violation{Path: path + ".harness", Message: "only agentic nodes take a harness"})
		}
		if n.Instructions != "" {
			vs = append(vs, Violation{Path: path + ".instructions", Message: "only agentic nodes take instructions"})
		}
	}
	if n.OnRequestChanges != nil {
		if n.Type != NodeGate {
			vs = append(vs, Violation{Path: path + ".on_request_changes", Message: "only gates declare revision transitions"})
		} else {
			if r := n.OnRequestChanges.MaxRounds; r < minRevisionRounds || r > maxRevisionRounds {
				vs = append(vs, Violation{Path: path + ".on_request_changes.max_rounds", Message: fmt.Sprintf("must be %d-%d", minRevisionRounds, maxRevisionRounds)})
			}
		}
	}
	return vs
}

// validateConsumption applies the per-type input signature table from
// the plan header to one node's incoming-edge counts.
func validateConsumption(path string, n Node, in map[ArtifactType]int) []Violation {
	var vs []Violation
	plans, patches, verdicts := in[ArtifactPlan], in[ArtifactPatch], in[ArtifactReviewVerdict]
	switch n.Type {
	case NodePlan:
		if plans+patches+verdicts > 0 {
			vs = append(vs, Violation{Path: path, Message: "a plan node consumes nothing"})
		}
	case NodeImplement:
		if plans > 1 {
			vs = append(vs, Violation{Path: path, Message: "implement consumes at most 1 plan"})
		}
		if patches > 1 {
			vs = append(vs, Violation{Path: path, Message: "implement consumes at most 1 patch"})
		}
		if verdicts > 0 {
			vs = append(vs, Violation{Path: path, Message: "implement cannot consume review_verdict"})
		}
	case NodeReview:
		if patches != 1 {
			vs = append(vs, Violation{Path: path, Message: "review consumes exactly 1 patch"})
		}
		if plans > 0 || verdicts > 0 {
			vs = append(vs, Violation{Path: path, Message: "review cannot consume review_verdict or plan"})
		}
	case NodeGate:
		if plans+patches != 1 {
			vs = append(vs, Violation{Path: path, Message: "a gate examines exactly one plan or patch"})
		}
		if verdicts > 1 {
			vs = append(vs, Violation{Path: path, Message: "a gate consumes at most 1 review_verdict"})
		}
	case NodeGitPublish:
		if patches != 1 {
			vs = append(vs, Violation{Path: path, Message: "git-publish consumes exactly 1 patch"})
		}
		if plans > 0 || verdicts > 0 {
			vs = append(vs, Violation{Path: path, Message: "git-publish consumes only a patch"})
		}
	}
	return vs
}

// Validate checks d against every §2 invariant (plan header, pinned
// formalizations). Shape violations short-circuit — the graph phases
// assume well-formed nodes and edges; a cycle short-circuits the
// path-based phases. An empty result means the document is valid.
func Validate(d Document) []Violation {
	if vs := validateShape(d); len(vs) > 0 {
		return vs
	}
	g := newGraph(d)
	if stuck := g.cycle(); stuck != nil {
		return []Violation{{Path: "edges", Message: "the dataflow graph must be acyclic; unresolvable nodes: " + strings.Join(stuck, ", ")}}
	}
	var vs []Violation
	vs = append(vs, g.validateGatePassThrough()...)
	vs = append(vs, g.validateLineage()...)
	dom := g.dominators()
	vs = append(vs, g.validateRevisions(dom)...)
	vs = append(vs, g.validatePublishes(dom)...)
	return vs
}

// validateGatePassThrough pins decision 2: a gate's outgoing edges all
// carry exactly its examined artifact type.
func (g *graph) validateGatePassThrough() []Violation {
	var vs []Violation
	for _, n := range g.d.Nodes {
		if n.Type != NodeGate {
			continue
		}
		ex := g.examined(n.ID)
		for j, e := range g.d.Edges {
			if e.From == n.ID && e.Artifact != ex.Artifact {
				vs = append(vs, Violation{
					Path:    fmt.Sprintf("edges[%d].artifact", j),
					Message: fmt.Sprintf("gate %q passes through %s, not %s", n.ID, ex.Artifact, e.Artifact),
				})
			}
		}
	}
	return vs
}

// validateLineage pins decision 3: per node, the implement nodes in its
// strict ancestry must be totally ordered by ancestry — one lineage,
// never a merge of two.
func (g *graph) validateLineage() []Violation {
	anc := make(map[string]map[string]bool, len(g.d.Nodes))
	for _, n := range g.d.Nodes {
		anc[n.ID] = g.ancestors(n.ID)
	}
	var vs []Violation
	for i, n := range g.d.Nodes {
		var producers []string
		for _, p := range g.d.Nodes { // document order → deterministic pairs
			if p.Type == NodeImplement && anc[n.ID][p.ID] {
				producers = append(producers, p.ID)
			}
		}
		for a := 0; a < len(producers); a++ {
			for b := a + 1; b < len(producers); b++ {
				pa, pb := producers[a], producers[b]
				if !anc[pa][pb] && !anc[pb][pa] {
					vs = append(vs, Violation{
						Path:    fmt.Sprintf("nodes[%d]", i),
						Message: fmt.Sprintf("%q merges divergent patch lineages: implement ancestors %q and %q are unordered (V1: patch lineage is linear)", n.ID, pa, pb),
					})
				}
			}
		}
	}
	return vs
}

// validateRevisions pins invariant 5: agentic target, strict dominance,
// pairwise-disjoint loop slices.
func (g *graph) validateRevisions(dom map[string]map[string]bool) []Violation {
	var vs []Violation
	type loop struct {
		gateID string
		slice  map[string]bool
	}
	var loops []loop
	for i, n := range g.d.Nodes {
		if n.Type != NodeGate || n.OnRequestChanges == nil {
			continue
		}
		path := fmt.Sprintf("nodes[%d].on_request_changes", i)
		target := n.OnRequestChanges.Target
		if !g.node(target).Type.Agentic() {
			vs = append(vs, Violation{Path: path + ".target", Message: "revision target must be an agentic node"})
			continue
		}
		if !dom[n.ID][target] {
			vs = append(vs, Violation{Path: path + ".target", Message: fmt.Sprintf("revision target %q must dominate its gate %q", target, n.ID)})
			continue
		}
		slice := map[string]bool{target: true, n.ID: true}
		desc := g.descendants(target)
		for a := range g.ancestors(n.ID) {
			if desc[a] {
				slice[a] = true
			}
		}
		for _, prior := range loops {
			var shared []string
			for id := range slice {
				if prior.slice[id] {
					shared = append(shared, id)
				}
			}
			if len(shared) > 0 {
				sort.Strings(shared)
				vs = append(vs, Violation{
					Path:    path,
					Message: fmt.Sprintf("revision loops of gates %q and %q overlap on %s (V1: loops are disjoint)", prior.gateID, n.ID, strings.Join(shared, ", ")),
				})
			}
		}
		loops = append(loops, loop{gateID: n.ID, slice: slice})
	}
	return vs
}

// validatePublishes pins invariants 6 and 7: every git-publish has an
// eligible dominating gate examining exactly the patch it consumes, and
// no gate authorizes two publishes (nearest-eligible assignment).
func (g *graph) validatePublishes(dom map[string]map[string]bool) []Violation {
	var vs []Violation
	claimed := map[string]string{} // gate id → publish id
	for i, n := range g.d.Nodes {
		if n.Type != NodeGitPublish {
			continue
		}
		path := fmt.Sprintf("nodes[%d]", i)
		var patchEdge Edge
		for _, e := range g.in[n.ID] {
			if e.Artifact == ArtifactPatch {
				patchEdge = e
				break
			}
		}
		origin := g.origin(patchEdge)
		anc := g.ancestors(n.ID)

		var nearest string
		nearestDepth := -1
		for _, cand := range g.d.Nodes {
			if cand.Type != NodeGate || !dom[n.ID][cand.ID] {
				continue
			}
			ex := g.examined(cand.ID)
			if ex.Artifact != ArtifactPatch || g.origin(ex) != origin {
				continue
			}
			between := g.descendants(cand.ID)
			divergent := false
			for id := range between {
				if anc[id] && g.node(id).Type == NodeImplement {
					divergent = true
					break
				}
			}
			if divergent {
				continue
			}
			// dom(P) is a chain, so depth (= |dom(G)|) orders candidates;
			// the deepest eligible gate is the nearest one.
			if depth := len(dom[cand.ID]); depth > nearestDepth {
				nearest, nearestDepth = cand.ID, depth
			}
		}
		if nearest == "" {
			vs = append(vs, Violation{
				Path:    path,
				Message: fmt.Sprintf("%q must be dominated by a gate examining exactly the patch it publishes (spec §2: never publish-then-gate)", n.ID),
			})
			continue
		}
		if other, taken := claimed[nearest]; taken {
			vs = append(vs, Violation{
				Path:    path,
				Message: fmt.Sprintf("gate %q already authorizes %q; an ApprovedPatch authorization is single-use, so each publish needs its own gate", nearest, other),
			})
			continue
		}
		claimed[nearest] = n.ID
	}
	return vs
}
