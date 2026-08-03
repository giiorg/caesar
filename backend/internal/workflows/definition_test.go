package workflows_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/giiorg/caesar/backend/internal/workflows"
)

// validDoc is the canonical five-node conveyor from the plan header; the
// whole test suite reuses it.
const validDoc = `{
  "schema": 1,
  "nodes": [
    {"id": "plan", "type": "plan", "harness": "cc", "instructions": "Plan the change."},
    {"id": "impl", "type": "implement", "harness": "cc", "instructions": "Implement the plan."},
    {"id": "review", "type": "review", "harness": "cc", "instructions": "Review the patch."},
    {"id": "gate", "type": "gate", "on_request_changes": {"target": "impl", "max_rounds": 3}},
    {"id": "publish", "type": "git-publish"}
  ],
  "edges": [
    {"from": "plan", "to": "impl", "artifact": "plan"},
    {"from": "impl", "to": "review", "artifact": "patch"},
    {"from": "impl", "to": "gate", "artifact": "patch"},
    {"from": "review", "to": "gate", "artifact": "review_verdict"},
    {"from": "gate", "to": "publish", "artifact": "patch"}
  ]
}`

func TestParseDocument_AcceptsTheCanonicalConveyor(t *testing.T) {
	d, err := workflows.ParseDocument([]byte(validDoc))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	if len(d.Nodes) != 5 || len(d.Edges) != 5 {
		t.Fatalf("got %d nodes, %d edges, want 5 and 5", len(d.Nodes), len(d.Edges))
	}
	if d.Nodes[3].OnRequestChanges == nil || d.Nodes[3].OnRequestChanges.Target != "impl" || d.Nodes[3].OnRequestChanges.MaxRounds != 3 {
		t.Fatalf("gate revision transition not parsed: %+v", d.Nodes[3].OnRequestChanges)
	}
}

func TestParseDocument_RejectsMalformedInput(t *testing.T) {
	cases := []struct {
		name, raw, wantMsg string
	}{
		{"not json", `nope`, "invalid definition document"},
		{"unknown top-level field", `{"schema": 1, "nodes": [], "surprise": true}`, "invalid definition document"},
		{"unknown node field", `{"schema": 1, "nodes": [{"id": "a", "type": "plan", "prompt": "x"}]}`, "invalid definition document"},
		{"trailing data", `{"schema": 1, "nodes": []}{"schema": 1}`, "invalid definition document"},
		{"wrong schema version", `{"schema": 2, "nodes": []}`, "unsupported schema version"},
		{"missing schema version", `{"nodes": []}`, "unsupported schema version"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := workflows.ParseDocument([]byte(c.raw))
			if err == nil {
				t.Fatal("ParseDocument accepted malformed input")
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Fatalf("error %q does not contain %q", err, c.wantMsg)
			}
		})
	}
}

func TestParseDocument_RejectsOversizedInput(t *testing.T) {
	raw := []byte(`{"schema": 1, "nodes": [], "x": "` + strings.Repeat("a", 1<<20) + `"}`)
	if _, err := workflows.ParseDocument(raw); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("want size rejection, got %v", err)
	}
}

func TestCanonical_IsAParseFixedPoint(t *testing.T) {
	d, err := workflows.ParseDocument([]byte(validDoc))
	if err != nil {
		t.Fatal(err)
	}
	d2, err := workflows.ParseDocument(d.Canonical())
	if err != nil {
		t.Fatalf("reparse of canonical bytes: %v", err)
	}
	if diff := cmp.Diff(d, d2); diff != "" {
		t.Fatalf("canonical round trip changed the document:\n%s", diff)
	}
	if string(d.Canonical()) != string(d2.Canonical()) {
		t.Fatal("canonical bytes are not stable")
	}
}

func TestHash_IgnoresFormattingButSeesContent(t *testing.T) {
	d1, err := workflows.ParseDocument([]byte(validDoc))
	if err != nil {
		t.Fatal(err)
	}
	// Same content, different whitespace and key order.
	reordered := `{"nodes": [
    {"type": "plan", "instructions": "Plan the change.", "id": "plan", "harness": "cc"},
    {"id": "impl", "type": "implement", "harness": "cc", "instructions": "Implement the plan."},
    {"id": "review", "type": "review", "harness": "cc", "instructions": "Review the patch."},
    {"id": "gate", "type": "gate", "on_request_changes": {"max_rounds": 3, "target": "impl"}},
    {"id": "publish", "type": "git-publish"}
  ], "edges": [
    {"from": "plan", "to": "impl", "artifact": "plan"},
    {"from": "impl", "to": "review", "artifact": "patch"},
    {"from": "impl", "to": "gate", "artifact": "patch"},
    {"from": "review", "to": "gate", "artifact": "review_verdict"},
    {"from": "gate", "to": "publish", "artifact": "patch"}
  ], "schema": 1}`
	d2, err := workflows.ParseDocument([]byte(reordered))
	if err != nil {
		t.Fatal(err)
	}
	if d1.Hash() != d2.Hash() {
		t.Fatal("hash depends on formatting, not content")
	}
	if len(d1.Hash()) != 64 {
		t.Fatalf("hash %q is not hex sha-256", d1.Hash())
	}

	d2.Nodes[0].Instructions = "Plan the change differently."
	if d1.Hash() == d2.Hash() {
		t.Fatal("hash blind to a content change")
	}
}

func TestParseDocument_NormalizesEmptyEdges(t *testing.T) {
	a, err := workflows.ParseDocument([]byte(`{"schema": 1, "nodes": [{"id": "a", "type": "plan", "harness": "cc", "instructions": "x"}], "edges": []}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := workflows.ParseDocument([]byte(`{"schema": 1, "nodes": [{"id": "a", "type": "plan", "harness": "cc", "instructions": "x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash() != b.Hash() {
		t.Fatal(`"edges": [] and absent edges must hash identically`)
	}
}

func TestNodeAndArtifactTypes_Validity(t *testing.T) {
	for _, nt := range []workflows.NodeType{workflows.NodePlan, workflows.NodeImplement, workflows.NodeReview, workflows.NodeGate, workflows.NodeGitPublish} {
		if !nt.Valid() {
			t.Fatalf("%q should be valid", nt)
		}
	}
	if workflows.NodeType("compile").Valid() {
		t.Fatal("unknown node type accepted")
	}
	if !workflows.NodePlan.Agentic() || !workflows.NodeImplement.Agentic() || !workflows.NodeReview.Agentic() {
		t.Fatal("agentic classification wrong")
	}
	if workflows.NodeGate.Agentic() || workflows.NodeGitPublish.Agentic() {
		t.Fatal("gate/git-publish are not agentic")
	}
	if !workflows.ArtifactPatch.Valid() || workflows.ArtifactType("log_ref").Valid() {
		t.Fatal("artifact validity wrong: log_ref is a runtime output, never bindable")
	}
}
