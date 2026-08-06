//go:build runnerintegration

package runner

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// A real two-phase conveyor: phase 2 is handed phase 1's patch artifact and
// nothing else, and everything the contract promises has to hold across that
// hand-off — the predecessor's work is already in the workspace, the phase's
// instructions are on its filesystem, the patch it produces is cumulative from
// the run base, and .caesar is nowhere in it.
func TestExecute_TwoPhaseConveyorCarriesLineageThroughTheFilesystem(t *testing.T) {
	sha := fixtureSHA(t)

	// Phase 1 writes a file. Its patch is the only thing phase 2 will get.
	r1 := newDockerRunner(t, Options{}, scriptedHarness("", "set -e\nprintf 'one\\n' > /workspace/a.txt\n"), &recordingSink{}, nil)
	first, err := r1.Execute(t.Context(), implementRequest(t, dockerRef(), sha))
	if err != nil {
		t.Fatalf("phase 1: %v", err)
	}
	patch, ok := artifactTypes(first.Artifacts)[runs.ArtifactKindPatch]
	if !ok {
		t.Fatalf("phase 1 produced no patch: %v", first.Artifacts)
	}

	// Phase 2 opens where phase 1 left off: it refuses to run at all if a.txt
	// is not already there, appends to the very file phase 1 wrote, and copies
	// its contract inputs into the workspace so the patch can be asked what
	// the agent actually saw.
	script := `set -e
test -f /workspace/a.txt || { echo "phase 1's work was not staged"; exit 9; }
printf 'two\n' >> /workspace/a.txt
cp /workspace/.caesar/in/instructions.md /workspace/instructions-seen.md
cp /workspace/.caesar/in/inputs.json /workspace/inputs-seen.json
cat > /workspace/.caesar/out/artifacts.json <<'EOF'
[{"type":"plan","payload":{"content":"read the lineage"}}]
EOF
`
	r2 := newDockerRunner(t, Options{}, scriptedHarness("", script), &recordingSink{}, nil)
	ref2 := dockerRef()
	req2 := implementRequest(t, ref2, sha)
	req2.Instructions = "append a second line\n"
	req2.TaskInputs = map[string]any{"ticket": "CAESAR-12"}
	patchID := uuid.New()
	req2.Inputs = []runs.ArtifactInput{{ID: patchID, Type: runs.ArtifactKindPatch, Payload: patch.Payload}}

	second, err := r2.Execute(t.Context(), req2)
	if err != nil {
		t.Fatalf("phase 2: %v\nagent log:\n%s", err, readLogFile(t, r2, ref2, agentLogFile))
	}
	byType := artifactTypes(second.Artifacts)
	out, ok := byType[runs.ArtifactKindPatch]
	if !ok {
		t.Fatalf("phase 2 produced no patch: %v", second.Artifacts)
	}
	diff := decodedDiff(t, out.Payload)

	// Cumulative: the patch diffs from the run base, so it carries phase 1's
	// line as well as phase 2's. This is why staging takes exactly one patch.
	for _, want := range []string{"a.txt", "+one", "+two"} {
		if !strings.Contains(diff, want) {
			t.Errorf("phase 2's patch is missing %q — it is not cumulative from the base:\n%s", want, diff)
		}
	}
	if out.Payload["base_sha"] != sha {
		t.Errorf("patch base_sha = %v, want the run base %s", out.Payload["base_sha"], sha)
	}

	// The agent read its instructions and its manifest off the filesystem.
	if !strings.Contains(diff, "instructions-seen.md") || !strings.Contains(diff, "+append a second line") {
		t.Errorf("the phase never saw its instructions:\n%s", diff)
	}
	for _, want := range []string{"inputs-seen.json", patchID.String(), `"ticket":"CAESAR-12"`} {
		if !strings.Contains(diff, want) {
			t.Errorf("inputs.json did not describe the lineage (%q missing):\n%s", want, diff)
		}
	}

	// And none of that is the phase's own work, so none of it is in the patch.
	if strings.Contains(diff, ".caesar") {
		t.Errorf(".caesar leaked into the patch:\n%s", diff)
	}

	// .caesar/out is excluded from the DIFF, not from the filesystem: collect
	// still reads the harness's declarations out of it.
	if got := byType[runs.ArtifactKindPlan].Payload["content"]; got != "read the lineage" {
		t.Errorf("declared artifact lost: %v", got)
	}
}

// A node handed two cumulative patches is refused before any container runs:
// each already contains their shared ancestors, so applying both would apply
// that history twice.
func TestExecute_TwoPatchInputsIsRefusedBeforeStaging(t *testing.T) {
	r := newDockerRunner(t, Options{}, scriptedHarness("", "true"), &recordingSink{}, nil)
	ref := dockerRef()
	req := implementRequest(t, ref, fixtureSHA(t))
	payload := map[string]any{"diff": "ZGlmZgo=", "base_sha": req.BaseSHA}
	req.Inputs = []runs.ArtifactInput{
		{ID: uuid.New(), Type: runs.ArtifactKindPatch, Payload: payload},
		{ID: uuid.New(), Type: runs.ArtifactKindPatch, Payload: payload},
	}

	_, err := r.Execute(t.Context(), req)
	if err == nil {
		t.Fatal("two patch inputs were accepted; the second would double-apply their shared ancestors")
	}
	out := runs.ClassifyOutcome(err)
	if out.Class != runs.OutcomePermanent || out.Code != runs.CodeStagingFailed {
		t.Fatalf("outcome = %+v, want a permanent %s", out, runs.CodeStagingFailed)
	}
	assertNoContainer(t, r, stageName(ref))
	assertNoContainer(t, r, agentName(ref))
}
