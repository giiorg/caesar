// In-package on purpose: gateDoc is a shared gate fixture, placed where tests
// that must be in-package — the read-path ones, which assert on unexported
// cursor and summary helpers — can reach it. Compile's own tests live in
// compile_test.go, which is package runs_test; a fixture defined there would
// be out of their reach.
package runs

import (
	"fmt"
	"testing"
)

// gateDoc builds a definition whose gate consumes exactly the artifacts named.
// Every producer of a bindable artifact is agentic, so each edge needs a node.
func gateDoc(t *testing.T, artifacts ...string) ExecPlan {
	t.Helper()
	doc := DefinitionDoc{Nodes: []DefNode{{ID: "gate", Kind: KindGate}}}
	for i, a := range artifacts {
		producer := fmt.Sprintf("n%d", i)
		kind := KindPlan
		switch a {
		case ArtifactKindPatch:
			kind = KindImplement
		case ArtifactKindReviewVerdict:
			kind = KindReview
		}
		doc.Nodes = append(doc.Nodes, DefNode{ID: producer, Kind: kind, Harness: "h", Instructions: "go"})
		doc.Edges = append(doc.Edges, DefEdge{From: producer, To: "gate", Artifact: a})
	}
	plan, err := Compile(doc)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return plan
}

func TestSummaryInput(t *testing.T) {
	t.Run("finds the review verdict beside the examined patch", func(t *testing.T) {
		plan := gateDoc(t, ArtifactKindPatch, ArtifactKindReviewVerdict)
		got, ok := plan.SummaryInput("gate")
		if !ok {
			t.Fatal("SummaryInput found nothing on a gate that consumes a verdict")
		}
		if got.Artifact != ArtifactKindReviewVerdict {
			t.Errorf("SummaryInput artifact = %q, want %q", got.Artifact, ArtifactKindReviewVerdict)
		}
		// AND it must not be the examined one. A resolver that returned the
		// first input of any type would satisfy the assertion above on this
		// graph, because the patch sorts first.
		examined, _ := plan.ExaminedInput("gate")
		if got.Origin == examined.Origin {
			t.Errorf("SummaryInput returned the EXAMINED input %+v", got)
		}
	})

	t.Run("a gate with no verdict is not an error", func(t *testing.T) {
		// workflows/validate.go:207 requires exactly one plan-or-patch and
		// permits ZERO verdicts, so this is a legal workflow and the surface
		// renders it honestly rather than failing.
		plan := gateDoc(t, ArtifactKindPatch)
		if _, ok := plan.SummaryInput("gate"); ok {
			t.Error("SummaryInput invented a verdict on a gate that consumes none")
		}
	})

	t.Run("a node that is not a gate has no summary", func(t *testing.T) {
		plan := gateDoc(t, ArtifactKindPatch, ArtifactKindReviewVerdict)
		if _, ok := plan.SummaryInput("n0"); ok {
			t.Error("SummaryInput answered for a non-gate node")
		}
		if _, ok := plan.SummaryInput("nope"); ok {
			t.Error("SummaryInput answered for an unknown node")
		}
	})

	t.Run("a non-gate that consumes a verdict still has no summary", func(t *testing.T) {
		// The subtest above cannot fail: no node in gateDoc's graph other than
		// the gate has a verdict input, so deleting the Kind guard outright
		// leaves it green. Only a graph where a NON-gate consumes a verdict
		// holds that guard to account.
		//
		// Compile is a mirror of an already-validated document and does not
		// re-check the per-kind input rules, so it accepts this one:
		// validate.go:198 forbids implement from consuming a review_verdict,
		// which makes the shape corruption of a stored version rather than a
		// workflow anyone can author. SummaryInput must not answer for it —
		// "the verdict this node was decided against" means nothing on a node
		// that decides nothing.
		plan, err := Compile(DefinitionDoc{
			Nodes: []DefNode{
				{ID: "impl", Kind: KindImplement, Harness: "h", Instructions: "go"},
				{ID: "rev", Kind: KindReview, Harness: "h", Instructions: "go"},
				{ID: "sink", Kind: KindImplement, Harness: "h", Instructions: "go"},
			},
			Edges: []DefEdge{
				{From: "impl", To: "rev", Artifact: ArtifactKindPatch},
				{From: "rev", To: "sink", Artifact: ArtifactKindReviewVerdict},
			},
		})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if got, ok := plan.SummaryInput("sink"); ok {
			t.Errorf("SummaryInput answered %+v for a non-gate node that consumes a verdict", got)
		}
	})
}
