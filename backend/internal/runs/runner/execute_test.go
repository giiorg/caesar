package runner

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/runner/gitops"
)

// The lineage a node was compiled with becomes the staging contract: its
// instructions, its task inputs, the one cumulative patch it opens from, and a
// context file per remaining artifact — each of them named in the manifest the
// harness reads.
func TestLineageInto_MapsTheRequestOntoTheStagingContract(t *testing.T) {
	patchID, planID, verdictID := uuid.New(), uuid.New(), uuid.New()
	diff := base64.StdEncoding.EncodeToString([]byte("diff --git a/x b/x\n"))
	req := runs.ExecutionRequest{
		Instructions: "implement the thing\n",
		TaskInputs:   map[string]any{"ticket": "CAESAR-12"},
		Inputs: []runs.ArtifactInput{
			{ID: planID, Type: runs.ArtifactKindPlan, Payload: map[string]any{"content": "the plan"}},
			{ID: patchID, Type: runs.ArtifactKindPatch, Payload: map[string]any{"diff": diff, "base_sha": "abc"}},
			{ID: verdictID, Type: runs.ArtifactKindReviewVerdict, Payload: map[string]any{"verdict": "changes_requested"}},
		},
	}

	var in gitops.StageInput
	if err := lineageInto(&in, req); err != nil {
		t.Fatalf("lineageInto: %v", err)
	}

	if in.Instructions != req.Instructions {
		t.Errorf("instructions = %q", in.Instructions)
	}
	if in.TaskInputs["ticket"] != "CAESAR-12" {
		t.Errorf("task inputs = %v", in.TaskInputs)
	}
	// The patch travels as the base64 wire form collect wrote, byte for byte:
	// decoding and re-encoding it here would be the corruption the wire form
	// exists to avoid.
	if in.Patch != diff {
		t.Errorf("patch = %q, want the payload's diff string unchanged", in.Patch)
	}

	wantContexts := []gitops.StageContext{
		{Name: "0-plan.json", JSON: json.RawMessage(`{"content":"the plan"}`)},
		{Name: "2-review_verdict.json", JSON: json.RawMessage(`{"verdict":"changes_requested"}`)},
	}
	if len(in.Contexts) != len(wantContexts) {
		t.Fatalf("contexts = %+v, want %+v", in.Contexts, wantContexts)
	}
	for i, want := range wantContexts {
		if in.Contexts[i].Name != want.Name || string(in.Contexts[i].JSON) != string(want.JSON) {
			t.Errorf("contexts[%d] = %s %s, want %s %s",
				i, in.Contexts[i].Name, in.Contexts[i].JSON, want.Name, want.JSON)
		}
	}

	// The manifest keeps the request's order, and only a context artifact
	// carries a filename — an applied patch is in the tree, not in a file.
	wantRefs := []gitops.InputRef{
		{ID: planID.String(), Type: runs.ArtifactKindPlan, Name: "0-plan.json"},
		{ID: patchID.String(), Type: runs.ArtifactKindPatch},
		{ID: verdictID.String(), Type: runs.ArtifactKindReviewVerdict, Name: "2-review_verdict.json"},
	}
	if len(in.InputRefs) != len(wantRefs) {
		t.Fatalf("input refs = %+v, want %+v", in.InputRefs, wantRefs)
	}
	for i, want := range wantRefs {
		if in.InputRefs[i] != want {
			t.Errorf("input refs[%d] = %+v, want %+v", i, in.InputRefs[i], want)
		}
	}
}

// Every patch diffs from the run's base and so already contains its
// ancestors': applying two of them would apply every shared ancestor twice.
// A node declaring two is a shape this plan does not implement, and saying so
// is the only honest answer.
func TestLineageInto_TwoPatchInputsIsPermanentStagingFailed(t *testing.T) {
	diff := base64.StdEncoding.EncodeToString([]byte("diff --git a/x b/x\n"))
	req := runs.ExecutionRequest{Inputs: []runs.ArtifactInput{
		{ID: uuid.New(), Type: runs.ArtifactKindPatch, Payload: map[string]any{"diff": diff}},
		{ID: uuid.New(), Type: runs.ArtifactKindPatch, Payload: map[string]any{"diff": diff}},
	}}

	var in gitops.StageInput
	err := lineageInto(&in, req)
	out := runs.ClassifyOutcome(err)
	if out.Class != runs.OutcomePermanent || out.Code != runs.CodeStagingFailed {
		t.Fatalf("err = %v (%+v), want a permanent %s", err, out, runs.CodeStagingFailed)
	}
	if !strings.Contains(out.Message, "patch input") {
		t.Errorf("message = %q, want it to name the multi-patch shape", out.Message)
	}
}

// A patch artifact without the base64 diff collect writes would stage no
// lineage at all — the phase would silently open at the run base and its own
// patch would then read as a revert of its predecessor. Refuse instead.
func TestLineageInto_PatchWithoutTheWireFormIsRefused(t *testing.T) {
	for name, payload := range map[string]map[string]any{
		"no diff key":       {"base_sha": "abc"},
		"a non-string diff": {"diff": 42},
		"an empty diff":     {"diff": ""},
	} {
		t.Run(name, func(t *testing.T) {
			req := runs.ExecutionRequest{Inputs: []runs.ArtifactInput{
				{ID: uuid.New(), Type: runs.ArtifactKindPatch, Payload: payload},
			}}
			var in gitops.StageInput
			out := runs.ClassifyOutcome(lineageInto(&in, req))
			if out.Class != runs.OutcomePermanent || out.Code != runs.CodeInvalidOutput {
				t.Fatalf("outcome = %+v, want a permanent %s", out, runs.CodeInvalidOutput)
			}
		})
	}
}

// A payload that cannot be encoded cannot be handed to a phase, and no retry
// would encode it either.
func TestLineageInto_UnencodableContextPayloadIsPermanent(t *testing.T) {
	req := runs.ExecutionRequest{Inputs: []runs.ArtifactInput{
		{ID: uuid.New(), Type: runs.ArtifactKindPlan, Payload: map[string]any{"content": make(chan int)}},
	}}
	var in gitops.StageInput
	out := runs.ClassifyOutcome(lineageInto(&in, req))
	if out.Class != runs.OutcomePermanent || out.Code != runs.CodeInvalidOutput {
		t.Fatalf("outcome = %+v, want a permanent %s", out, runs.CodeInvalidOutput)
	}
}

// A harnessless node with no lineage stages the contract and nothing else —
// no empty patch, no context files.
func TestLineageInto_NoLineageLeavesTheContractFieldsEmpty(t *testing.T) {
	var in gitops.StageInput
	if err := lineageInto(&in, runs.ExecutionRequest{}); err != nil {
		t.Fatal(err)
	}
	if in.Patch != "" || len(in.Contexts) != 0 || len(in.InputRefs) != 0 {
		t.Errorf("in = %+v, want the lineage fields empty", in)
	}
}
