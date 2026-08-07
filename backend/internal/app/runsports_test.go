// In-package: pinnedFromVersion is the composition root's private mapping
// from the definitions vocabulary onto the runs mirror, and it is the only
// place both are visible at once.
package app

import (
	"testing"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/workflows"
)

func TestPinnedFromVersion_CarriesTheRevisionTransition(t *testing.T) {
	doc := workflows.Document{
		Schema: 1,
		Nodes: []workflows.Node{
			{ID: "implement", Type: workflows.NodeImplement, Harness: "h", Instructions: "x"},
			{ID: "gate", Type: workflows.NodeGate, OnRequestChanges: &workflows.RevisionTransition{Target: "implement", MaxRounds: 3}},
		},
		Edges: []workflows.Edge{{From: "implement", To: "gate", Artifact: workflows.ArtifactPatch}},
	}
	got, err := pinnedFromVersion(workflows.Version{ID: uuid.New(), Document: doc.Canonical()})
	if err != nil {
		t.Fatal(err)
	}
	var gate runs.DefNode
	for _, n := range got.Doc.Nodes {
		if n.ID == "gate" {
			gate = n
		}
	}
	if gate.Revision == nil {
		t.Fatal("the mirror must carry on_request_changes; without it no revision can ever open")
	}
	if gate.Revision.Target != "implement" || gate.Revision.MaxRounds != 3 {
		t.Errorf("revision = %+v; want implement/3", *gate.Revision)
	}
}

func TestPinnedFromVersion_LeavesNonGatesWithoutARevision(t *testing.T) {
	doc := workflows.Document{
		Schema: 1,
		Nodes:  []workflows.Node{{ID: "implement", Type: workflows.NodeImplement, Harness: "h", Instructions: "x"}},
	}
	got, err := pinnedFromVersion(workflows.Version{ID: uuid.New(), Document: doc.Canonical()})
	if err != nil {
		t.Fatal(err)
	}
	if got.Doc.Nodes[0].Revision != nil {
		t.Error("only gates carry a revision transition")
	}
}
