//go:build integration

package runs_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

// spineHarness is newFixture + newService + the fake executor + a started
// orchestrator, wired so a test can say "run this document to a terminal
// status and tell me what the database holds". It builds on the existing
// integration scaffolding (newFixture, newService, startOrchestrator) rather
// than standing up a second one.
type spineHarness struct {
	f        fixture
	svc      *runs.Service
	executor *fake.Executor
	doc      runs.DefinitionDoc
}

// docOption mutates the pinned document before the service is built. The
// document is what a run pins at intake, and the schema version on it is the
// only input that decides whether payloads are validated at all.
type docOption func(*runs.DefinitionDoc)

// schemaVersion sets the document's `schema`. agenticDoc() leaves it zero,
// which reads as "older than every constrained version" — fine for the suites
// that predate the payload contract, useless for one about the boundary.
func schemaVersion(n int) docOption {
	return func(d *runs.DefinitionDoc) { d.Schema = n }
}

func newSpineHarness(t *testing.T, opts ...docOption) *spineHarness {
	t.Helper()
	doc := agenticDoc()
	for _, o := range opts {
		o(&doc)
	}
	f := newFixture(t)
	svc, _ := newService(t, f, doc, runs.DefaultSnapshot())
	return &spineHarness{f: f, svc: svc, executor: fake.NewExecutor(), doc: doc}
}

// runToCompletion creates a task against the pinned document, starts the
// orchestrator, and blocks until the run reaches any terminal status. It
// deliberately does not name the status it expects: half the subtests below
// are about a run that must fail, and a helper that fataled on a failure
// would take the assertion away from the test.
func (h *spineHarness) runToCompletion(t *testing.T) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	_, run, err := h.svc.CreateTask(ctx, principal(h.f), runs.NewTask{
		RepoID: h.f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "payload enforcement",
	})
	if err != nil {
		t.Fatal(err)
	}
	startOrchestrator(t, h.svc, h.executor, 30*time.Second)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		got, err := h.svc.GetRunSummary(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status.Terminal() {
			return run.ID
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("run %s never reached a terminal status", run.ID)
	return uuid.Nil
}

func (h *spineHarness) runStatus(t *testing.T, runID uuid.UUID) runs.RunStatus {
	t.Helper()
	got, err := h.svc.GetRunSummary(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return got.Status
}

// nodeInstructions is what the PINNED DOCUMENT says the node was told, before
// anything the runtime appends.
func (h *spineHarness) nodeInstructions(nodeID string) string {
	for _, n := range h.doc.Nodes {
		if n.ID == nodeID {
			return n.Instructions
		}
	}
	return ""
}

// artifactPayload reads the stored payload back out of the DATABASE rather
// than off the ExecutionResult: the question these tests ask is what an
// operator can later read, and a payload mutated on the way to storage would
// be invisible anywhere upstream of the row.
func (h *spineHarness) artifactPayload(t *testing.T, runID uuid.UUID, nodeID string) map[string]any {
	t.Helper()
	var raw []byte
	err := h.f.pool.QueryRow(context.Background(),
		`SELECT payload FROM artifacts WHERE run_id = $1 AND node_id = $2 ORDER BY created_at DESC LIMIT 1`,
		runID, nodeID,
	).Scan(&raw)
	if err != nil {
		t.Fatalf("read artifact payload for %s: %v", nodeID, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *spineHarness) artifactCount(t *testing.T, runID uuid.UUID, nodeID string) int {
	t.Helper()
	var n int
	if err := h.f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM artifacts WHERE run_id = $1 AND node_id = $2`, runID, nodeID,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (h *spineHarness) attemptCount(t *testing.T, runID uuid.UUID, nodeID string) int {
	t.Helper()
	var n int
	if err := h.f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM phase_runs WHERE run_id = $1 AND node_id = $2`, runID, nodeID,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// phaseOutcome is the recorded outcome of the node's last attempt, or nil if
// it has none.
func (h *spineHarness) phaseOutcome(t *testing.T, runID uuid.UUID, nodeID string) *runs.Outcome {
	t.Helper()
	var raw []byte
	err := h.f.pool.QueryRow(context.Background(),
		`SELECT outcome FROM phase_runs WHERE run_id = $1 AND node_id = $2 ORDER BY attempt DESC LIMIT 1`,
		runID, nodeID,
	).Scan(&raw)
	if err != nil {
		t.Fatalf("read phase outcome for %s: %v", nodeID, err)
	}
	if len(raw) == 0 {
		return nil
	}
	var out runs.Outcome
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func TestPayloadEnforcement(t *testing.T) {
	t.Run("a conforming payload is persisted and the run advances", func(t *testing.T) {
		h := newSpineHarness(t, schemaVersion(2))
		run := h.runToCompletion(t)
		if got := h.runStatus(t, run); got != runs.RunSucceeded {
			t.Fatalf("run = %s, want succeeded — the fake's schema-2 payloads must pass their own validator", got)
		}
	})

	t.Run("a payload with extra keys is persisted unchanged", func(t *testing.T) {
		// The extra key must reach the DATABASE, not merely survive validation.
		// A validator that stripped unknown keys would pass every test in
		// payload_test.go and quietly delete whatever a harness added.
		h := newSpineHarness(t, schemaVersion(2))
		h.executor.PlanExtra(map[string]any{"confidence": 0.8})
		run := h.runToCompletion(t)
		payload := h.artifactPayload(t, run, "plan")
		if _, ok := payload["confidence"]; !ok {
			t.Errorf("the extra key was dropped on the way to storage: %v", payload)
		}
	})

	t.Run("a payload missing a required key fails the attempt PERMANENTLY", func(t *testing.T) {
		h := newSpineHarness(t, schemaVersion(2))
		h.executor.PlanPayload(map[string]any{"content": "the old shape"})
		run := h.runToCompletion(t)
		if got := h.runStatus(t, run); got != runs.RunFailed {
			t.Fatalf("run = %s, want failed", got)
		}
		out := h.phaseOutcome(t, run, "plan")
		if out == nil || out.Code != runs.CodeInvalidOutput {
			t.Fatalf("outcome = %+v, want %s", out, runs.CodeInvalidOutput)
		}
		if out.Class != runs.OutcomePermanent {
			t.Errorf("class = %s, want permanent — retrying reaches the same bad shape", out.Class)
		}
		// PERMANENT MEANS ONE ATTEMPT. A transient classification would burn
		// max_attempts against a model that is going to answer identically.
		if n := h.attemptCount(t, run, "plan"); n != 1 {
			t.Errorf("the node was attempted %d times for a permanent outcome", n)
		}
		// AND THE REJECTED PAYLOAD IS STORED. This asserts the opposite of what
		// it looks like it should, deliberately.
		//
		// The malformed payload is the only evidence an operator has for WHY
		// the node failed — the outcome message names the shape breach, the
		// artifact row is the thing that breached it — and both evaluateFailure
		// branches persist res.Artifacts unconditionally, so suppressing it
		// would mean teaching the failure path to filter, i.e. adding a second
		// artifact policy to delete the diagnostic. It is safe to keep: a
		// permanently-failed node opens no gate and completes no run, so
		// nothing downstream reads or renders this row. Enforcement is about
		// what ADVANCES, not about what is written down.
		if n := h.artifactCount(t, run, "plan"); n != 1 {
			t.Errorf("the rejected payload was not stored (%d artifacts); it is the evidence for the failure", n)
		}
	})

	t.Run("a schema-1 run is untouched by any of this", func(t *testing.T) {
		// The compatibility rule, end to end. Same executor, same bad-by-v2
		// payload, a v1 document — and it succeeds, because a v1 run pinned a
		// document that never promised the shape.
		h := newSpineHarness(t, schemaVersion(1))
		h.executor.PlanPayload(map[string]any{"content": "the old shape"})
		run := h.runToCompletion(t)
		if got := h.runStatus(t, run); got != runs.RunSucceeded {
			t.Fatalf("schema-1 run = %s, want succeeded", got)
		}
	})

	t.Run("the node is TOLD the shape, and only at schema 2", func(t *testing.T) {
		h := newSpineHarness(t, schemaVersion(2))
		h.runToCompletion(t)
		got := h.executor.LastInstructions("plan")
		if !strings.Contains(got, `"summary"`) || !strings.Contains(got, `"steps"`) {
			t.Errorf("a schema-2 plan node was not told its output shape:\n%s", got)
		}
		if !strings.HasPrefix(got, h.nodeInstructions("plan")) {
			t.Error("the appended block displaced the node's own instructions instead of following them")
		}

		h1 := newSpineHarness(t, schemaVersion(1))
		h1.runToCompletion(t)
		if h1.executor.LastInstructions("plan") != h1.nodeInstructions("plan") {
			t.Error("a schema-1 node's instructions were modified; v1 behaviour must be byte-identical")
		}
	})
}
