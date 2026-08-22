package workflows_test

import (
	"fmt"
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

// The compatibility rule that the whole payload contract rests on: this binary
// parses BOTH versions, and the version it read is the version it reports. A
// run pins its definition at intake, so a schema-1 document keeps being a
// schema-1 document after the deploy that introduced schema 2 — which is why
// no drain is needed and why validation can be a property of the pin.
//
// validDoc above is a schema-1 document and every other test in this package
// parses it, so the v1 leg stays exercised by more than this one case.
// The versions are LITERAL 1 and 2, not a loop from MinSchemaVersion to
// SchemaVersion. Written the second way the test is a function of the two
// constants it exists to pin: setting SchemaVersion back to 1 makes the loop
// run one iteration and pass, which is the mutant this case has to kill.
func TestParseDocument_AcceptsBothSchemaVersions(t *testing.T) {
	for _, schema := range []int{1, 2} {
		raw := fmt.Sprintf(`{"schema": %d, "nodes": [{"id": "a", "type": "plan", "harness": "cc", "instructions": "x"}]}`, schema)
		d, err := workflows.ParseDocument([]byte(raw))
		if err != nil {
			t.Errorf("schema %d was rejected: %v", schema, err)
			continue
		}
		if d.Schema != schema {
			t.Errorf("schema %d parsed as %d; the pin a run is judged against is the one it was written with", schema, d.Schema)
		}
	}
	// And the constants say the same thing, because everything downstream
	// reads them rather than the literals above: runs' validator switches on
	// the pinned version, and a new version is WRITTEN as SchemaVersion.
	if workflows.MinSchemaVersion != 1 || workflows.SchemaVersion != 2 {
		t.Errorf("min/current = %d/%d; want 1/2", workflows.MinSchemaVersion, workflows.SchemaVersion)
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
		// 3, not 2: schema 2 is now ACCEPTED, and a rejection case that starts
		// passing for a new reason is worse than one that fails. The rejected
		// value has to sit outside [MinSchemaVersion, SchemaVersion] on the
		// high side, and 0 below covers the low side.
		{"schema version above the newest this binary writes", `{"schema": 3, "nodes": []}`, "unsupported schema version"},
		{"schema version below the oldest this binary parses", `{"schema": 0, "nodes": []}`, "unsupported schema version"},
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
