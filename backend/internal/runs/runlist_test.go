package runs

import (
	"testing"
)

// ptrPhase boxes a phase status for a summary entry. A materialized node
// carries one; an unmaterialized node carries nil, and the two are the whole
// distinction these tests exist to hold.
func ptrPhase(s PhaseStatus) *PhaseStatus { return &s }

func TestPhaseSummaryOrderFollowsThePlan(t *testing.T) {
	// The dots render the RUN'S SHAPE, so their order is the compiled
	// definition's topological order and never the order rows came back in.
	// Materialization order agrees with it today; a fan-out plus a retry is
	// where they part company, and by then nobody would look here.
	plan := gateDoc(t, ArtifactKindPatch) // n0 -> gate
	live := map[string]PhaseSummary{      // deliberately reversed
		"gate": {NodeID: "gate", Status: ptrPhase(PhaseWaitingDecision), Attempt: 1, Generation: 1},
		"n0":   {NodeID: "n0", Status: ptrPhase(PhaseSucceeded), Attempt: 1, Generation: 1},
	}
	got := phaseSummaries(plan, live)
	if len(got) != 2 || got[0].NodeID != "n0" || got[1].NodeID != "gate" {
		t.Fatalf("phase order = %v, want [n0 gate]", got)
	}
	if got[0].NodeType != KindImplement || got[1].NodeType != KindGate {
		t.Errorf("node types = %q/%q, want implement/gate", got[0].NodeType, got[1].NodeType)
	}
}

func TestPhaseSummaryKeepsUnmaterializedNodesNull(t *testing.T) {
	// A node the orchestrator has not reached yet is a DIM DOT, and its status
	// is null rather than "pending": `pending` is a real phase status meaning
	// "materialized and claimable", and conflating the two tells the client a
	// row exists that does not.
	plan := gateDoc(t, ArtifactKindPatch)
	got := phaseSummaries(plan, map[string]PhaseSummary{
		"n0": {NodeID: "n0", Status: ptrPhase(PhaseRunning), Attempt: 1, Generation: 1},
	})
	if len(got) != 2 {
		t.Fatalf("want an entry for every node of the plan, got %d", len(got))
	}
	if got[1].NodeID != "gate" || got[1].Status != nil {
		t.Errorf("unmaterialized gate = %+v, want status nil", got[1])
	}
	if got[1].Attempt != 0 || got[1].Generation != 0 {
		t.Errorf("unmaterialized gate carries attempt/generation %d/%d, want 0/0", got[1].Attempt, got[1].Generation)
	}
}
