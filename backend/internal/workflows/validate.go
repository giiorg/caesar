package workflows

import (
	"fmt"
	"regexp"
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
	for _, e := range d.Edges {
		if _, ok := index[e.From]; !ok || e.From == e.To || !e.Artifact.Valid() {
			continue
		}
		if _, ok := index[e.To]; !ok {
			continue
		}
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
			if r := n.OnRequestChanges.MaxRounds; r < 1 || r > maxRevisionRounds {
				vs = append(vs, Violation{Path: path + ".on_request_changes.max_rounds", Message: fmt.Sprintf("must be 1-%d", maxRevisionRounds)})
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
