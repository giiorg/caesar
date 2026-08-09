//go:build runnerintegration

package runner

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// Plan 08 ledger carry-forward: a revision's gate_note is the only new
// information the loop carries, and nothing had ever put one on a real
// filesystem — execute_test.go stages plan, patch and review_verdict, and the
// real-staging lane covered the patch hand-off alone. This drives the note the
// whole way: through lineageInto, through the stage container, to the path the
// agent reads it from.
//
// The note sits at index 1 behind a plan because the context filename is
// POSITIONAL in req.Inputs. A revision hands its target the carried-forward
// patch alongside the note, and the patch takes a slot in that numbering
// without taking a context file — so the note's name is not the ordinal of
// the context it becomes.
func TestExecute_AGateNoteIsStagedForTheRevisionTargetToRead(t *testing.T) {
	const note = "the new branch has no test; add one before this lands"

	// The agent proves it read the note by copying the staged file, and its
	// manifest, out of .caesar and into its own work — the patch is then the
	// evidence of what the container actually saw.
	script := `set -e
test -f /workspace/.caesar/in/1-gate_note.json || { echo "the gate note was not staged"; exit 9; }
cp /workspace/.caesar/in/1-gate_note.json /workspace/note-seen.json
cp /workspace/.caesar/in/inputs.json /workspace/inputs-seen.json
`
	r := newDockerRunner(t, Options{}, scriptedHarness("", script), &recordingSink{}, nil)
	ref := dockerRef()
	req := implementRequest(t, ref, fixtureSHA(t))
	req.Instructions = "address the gate's note\n"
	noteID := uuid.New()
	req.Inputs = []runs.ArtifactInput{
		{ID: uuid.New(), Type: runs.ArtifactKindPlan, Payload: map[string]any{"content": "add the branch"}},
		{ID: noteID, Type: runs.ArtifactKindGateNote, Payload: map[string]any{
			"note": note, "decision": "request_changes", "gate_node_id": "gate", "generation": 2,
		}},
	}

	res, err := r.Execute(t.Context(), req)
	if err != nil {
		t.Fatalf("execute: %v\nagent log:\n%s", err, readLogFile(t, r, ref, agentLogFile))
	}
	out, ok := artifactTypes(res.Artifacts)[runs.ArtifactKindPatch]
	if !ok {
		t.Fatalf("no patch produced: %v", res.Artifacts)
	}
	diff := decodedDiff(t, out.Payload)

	// The note's own text reached the agent's filesystem, payload intact.
	for _, want := range []string{"note-seen.json", note, "request_changes"} {
		if !strings.Contains(diff, want) {
			t.Errorf("the staged gate note is missing %q:\n%s", want, diff)
		}
	}
	// And the manifest ties the file the agent read to the artifact it is,
	// which is what lets an agent tell a note from a plan without guessing.
	for _, want := range []string{"inputs-seen.json", `"1-gate_note.json"`, noteID.String(), `"gate_note"`} {
		if !strings.Contains(diff, want) {
			t.Errorf("inputs.json did not name the gate note (%q missing):\n%s", want, diff)
		}
	}
}
