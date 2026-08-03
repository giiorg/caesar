package workflows

import (
	"strings"
	"testing"
)

// conveyor is the canonical five-node document as a struct; mutation
// helpers below derive every invalid case from it.
func conveyor() Document {
	return Document{
		Schema: SchemaVersion,
		Nodes: []Node{
			{ID: "plan", Type: NodePlan, Harness: "cc", Instructions: "Plan the change."},
			{ID: "impl", Type: NodeImplement, Harness: "cc", Instructions: "Implement the plan."},
			{ID: "review", Type: NodeReview, Harness: "cc", Instructions: "Review the patch."},
			{ID: "gate", Type: NodeGate, OnRequestChanges: &RevisionTransition{Target: "impl", MaxRounds: 3}},
			{ID: "publish", Type: NodeGitPublish},
		},
		Edges: []Edge{
			{From: "plan", To: "impl", Artifact: ArtifactPlan},
			{From: "impl", To: "review", Artifact: ArtifactPatch},
			{From: "impl", To: "gate", Artifact: ArtifactPatch},
			{From: "review", To: "gate", Artifact: ArtifactReviewVerdict},
			{From: "gate", To: "publish", Artifact: ArtifactPatch},
		},
	}
}

func wantViolation(t *testing.T, vs []Violation, path, msgPart string) {
	t.Helper()
	for _, v := range vs {
		if v.Path == path && strings.Contains(v.Message, msgPart) {
			return
		}
	}
	t.Fatalf("no violation at %q containing %q; got %+v", path, msgPart, vs)
}

func TestValidateShape_AcceptsTheConveyor(t *testing.T) {
	if vs := validateShape(conveyor()); len(vs) != 0 {
		t.Fatalf("valid document rejected: %+v", vs)
	}
}

func TestValidateShape_NodeRules(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Document)
		path    string
		msgPart string
	}{
		{"no nodes", func(d *Document) { d.Nodes = nil }, "nodes", "at least one node"},
		{"too many nodes", func(d *Document) {
			for i := 0; i < 50; i++ {
				d.Nodes = append(d.Nodes, Node{ID: "x" + strings.Repeat("a", i+1), Type: NodePlan, Harness: "cc", Instructions: "x"})
			}
		}, "nodes", "at most 50 nodes"},
		{"bad id charset", func(d *Document) { d.Nodes[0].ID = "Plan!" }, "nodes[0].id", "must match"},
		{"bad id leading digit", func(d *Document) { d.Nodes[0].ID = "1plan" }, "nodes[0].id", "must match"},
		{"duplicate id", func(d *Document) { d.Nodes[1].ID = "plan" }, "nodes[1].id", "duplicate"},
		{"unknown type", func(d *Document) { d.Nodes[0].Type = "compile" }, "nodes[0].type", "unknown node type"},
		{"agentic without harness", func(d *Document) { d.Nodes[0].Harness = "" }, "nodes[0].harness", "required"},
		{"harness untrimmed", func(d *Document) { d.Nodes[0].Harness = " cc " }, "nodes[0].harness", "whitespace"},
		{"harness too long", func(d *Document) { d.Nodes[0].Harness = strings.Repeat("h", 101) }, "nodes[0].harness", "at most 100"},
		{"agentic without instructions", func(d *Document) { d.Nodes[0].Instructions = "" }, "nodes[0].instructions", "required"},
		{"instructions too long", func(d *Document) { d.Nodes[0].Instructions = strings.Repeat("i", 8193) }, "nodes[0].instructions", "at most 8192"},
		{"gate with harness", func(d *Document) { d.Nodes[3].Harness = "cc" }, "nodes[3].harness", "only agentic"},
		{"gate with instructions", func(d *Document) { d.Nodes[3].Instructions = "decide" }, "nodes[3].instructions", "only agentic"},
		{"publish with harness", func(d *Document) { d.Nodes[4].Harness = "cc" }, "nodes[4].harness", "only agentic"},
		{"revision on non-gate", func(d *Document) {
			d.Nodes[1].OnRequestChanges = &RevisionTransition{Target: "plan", MaxRounds: 1}
		}, "nodes[1].on_request_changes", "only gates"},
		{"revision target unknown", func(d *Document) { d.Nodes[3].OnRequestChanges.Target = "ghost" }, "nodes[3].on_request_changes.target", "unknown node"},
		{"max_rounds zero", func(d *Document) { d.Nodes[3].OnRequestChanges.MaxRounds = 0 }, "nodes[3].on_request_changes.max_rounds", "1-10"},
		{"max_rounds too high", func(d *Document) { d.Nodes[3].OnRequestChanges.MaxRounds = 11 }, "nodes[3].on_request_changes.max_rounds", "1-10"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := conveyor()
			c.mutate(&d)
			wantViolation(t, validateShape(d), c.path, c.msgPart)
		})
	}
}

func TestValidateShape_EdgeRules(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Document)
		path    string
		msgPart string
	}{
		{"unknown from", func(d *Document) { d.Edges[0].From = "ghost" }, "edges[0].from", "unknown node"},
		{"unknown to", func(d *Document) { d.Edges[0].To = "ghost" }, "edges[0].to", "unknown node"},
		{"self edge", func(d *Document) { d.Edges[0].To = "plan" }, "edges[0]", "self"},
		{"unknown artifact", func(d *Document) { d.Edges[0].Artifact = "binary" }, "edges[0].artifact", "unknown artifact"},
		{"duplicate edge", func(d *Document) { d.Edges = append(d.Edges, d.Edges[1]) }, "edges[5]", "duplicate"},
		{"too many edges", func(d *Document) {
			for i := 0; i < 200; i++ {
				d.Edges = append(d.Edges, Edge{From: "plan", To: "impl", Artifact: ArtifactPlan})
			}
		}, "edges", "at most 200 edges"},
		{"plan producing patch", func(d *Document) { d.Edges[0].Artifact = ArtifactPatch }, "edges[0].artifact", "produces plan"},
		{"publish producing", func(d *Document) {
			d.Edges = append(d.Edges, Edge{From: "publish", To: "review", Artifact: ArtifactPatch})
		}, "edges[5]", "git-publish produces nothing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := conveyor()
			c.mutate(&d)
			wantViolation(t, validateShape(d), c.path, c.msgPart)
		})
	}
}

func TestValidateShape_ConsumptionMultiplicities(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Document)
		path    string
		msgPart string
	}{
		{"plan consuming anything", func(d *Document) {
			d.Nodes = append(d.Nodes, Node{ID: "plan2", Type: NodePlan, Harness: "cc", Instructions: "x"})
			d.Edges = append(d.Edges, Edge{From: "plan", To: "plan2", Artifact: ArtifactPlan})
		}, "nodes[5]", "consumes nothing"},
		{"implement with two plans", func(d *Document) {
			d.Nodes = append(d.Nodes, Node{ID: "plan2", Type: NodePlan, Harness: "cc", Instructions: "x"})
			d.Edges = append(d.Edges, Edge{From: "plan2", To: "impl", Artifact: ArtifactPlan})
		}, "nodes[1]", "at most 1 plan"},
		{"review without patch", func(d *Document) {
			d.Edges = append(d.Edges[:1], d.Edges[2:]...) // drop impl→review
		}, "nodes[2]", "exactly 1 patch"},
		{"review consuming verdict", func(d *Document) {
			d.Nodes = append(d.Nodes, Node{ID: "review2", Type: NodeReview, Harness: "cc", Instructions: "x"})
			d.Edges = append(d.Edges,
				Edge{From: "impl", To: "review2", Artifact: ArtifactPatch},
				Edge{From: "review", To: "review2", Artifact: ArtifactReviewVerdict})
		}, "nodes[5]", "cannot consume review_verdict"},
		{"gate without examined", func(d *Document) {
			d.Edges = append(d.Edges[:2], d.Edges[3:]...) // drop impl→gate patch
		}, "nodes[3]", "exactly one plan or patch"},
		{"gate with two examined", func(d *Document) {
			d.Edges = append(d.Edges, Edge{From: "plan", To: "gate", Artifact: ArtifactPlan})
		}, "nodes[3]", "exactly one plan or patch"},
		{"publish without patch", func(d *Document) {
			d.Edges = d.Edges[:4] // drop gate→publish
		}, "nodes[4]", "exactly 1 patch"},
		{"implement consuming verdict", func(d *Document) {
			d.Nodes = append(d.Nodes, Node{ID: "impl2", Type: NodeImplement, Harness: "cc", Instructions: "x"})
			d.Edges = append(d.Edges, Edge{From: "review", To: "impl2", Artifact: ArtifactReviewVerdict})
		}, "nodes[5]", "cannot consume review_verdict"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := conveyor()
			c.mutate(&d)
			wantViolation(t, validateShape(d), c.path, c.msgPart)
		})
	}
}
