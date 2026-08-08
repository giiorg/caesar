//go:build integration

package runs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

// Every waitForRun(..., RunWaiting) below a DecideGate is safe only because
// openRevision CASes the run from 'waiting' to 'running' INSIDE the decision
// transaction. By the time DecideGate returns, the run is no longer waiting,
// so the poll cannot match the state the decision was made against and
// mistake it for the next gate. Move that CAS out of the transaction — or
// "optimize" it away because materializeReady would resume the run anyway —
// and every one of these tests starts passing for the wrong reason.

// twoGateDoc chains two gates over one patch, each authorizing its own publish
// (workflows/validate_test.go, "chained gates each authorize their own
// publish"). gate_two's revision rewinds to impl, so its slice is
// {impl, gate_one, gate_two} — publish_one is a descendant of gate_one but not
// an ancestor of gate_two, so the rewind cannot reach it. That is the whole
// point of the refusal this doc exercises.
func twoGateDoc() runs.DefinitionDoc {
	return runs.DefinitionDoc{
		Nodes: []runs.DefNode{
			{ID: "plan", Kind: runs.KindPlan, Harness: "cc", Instructions: "Plan the change."},
			{ID: "impl", Kind: runs.KindImplement, Harness: "cc", Instructions: "Implement the plan."},
			{ID: "gate_one", Kind: runs.KindGate, Revision: &runs.DefRevision{Target: "impl", MaxRounds: 2}},
			{ID: "gate_two", Kind: runs.KindGate, Revision: &runs.DefRevision{Target: "impl", MaxRounds: 2}},
			{ID: "publish_one", Kind: runs.KindGitPublish},
			{ID: "publish_two", Kind: runs.KindGitPublish},
		},
		Edges: []runs.DefEdge{
			{From: "plan", To: "impl", Artifact: runs.ArtifactKindPlan},
			{From: "impl", To: "gate_one", Artifact: runs.ArtifactKindPatch},
			{From: "gate_one", To: "gate_two", Artifact: runs.ArtifactKindPatch},
			{From: "gate_one", To: "publish_one", Artifact: runs.ArtifactKindPatch},
			{From: "gate_two", To: "publish_two", Artifact: runs.ArtifactKindPatch},
		},
	}
}

// noRevisionDoc is the checkpoint gate with its on_request_changes transition
// removed. The compiler does not require a gate to declare one, so this is a
// legal definition whose gate simply has no third answer.
func noRevisionDoc() runs.DefinitionDoc {
	doc := checkpointDoc()
	for i := range doc.Nodes {
		if doc.Nodes[i].ID == "gate" {
			doc.Nodes[i].Revision = nil
		}
	}
	return doc
}

// patchTargetDoc rewinds to a node that ALREADY reads a patch through an edge.
// That is the only shape where the seed and the graph collide, and it takes a
// chain of two implement nodes to build: polish binds impl's patch
// structurally, dominates the gate (so it is a legal revision target), and a
// revision then hands it the patch the gate examined on top. impl sits OUTSIDE
// the resulting slice {polish, gate}, so its generation-1 patch is still the
// current one when polish re-runs — which is exactly why the seed has to win.
//
// gatedConveyorDoc cannot show this: its target is impl, whose only structural
// input is a plan, so the collision never arises there at all.
func patchTargetDoc() runs.DefinitionDoc {
	return runs.DefinitionDoc{
		Nodes: []runs.DefNode{
			{ID: "plan", Kind: runs.KindPlan, Harness: "cc", Instructions: "Plan the change."},
			{ID: "impl", Kind: runs.KindImplement, Harness: "cc", Instructions: "Implement the plan."},
			{ID: "polish", Kind: runs.KindImplement, Harness: "cc", Instructions: "Polish the patch."},
			{ID: "gate", Kind: runs.KindGate, Revision: &runs.DefRevision{Target: "polish", MaxRounds: 2}},
		},
		Edges: []runs.DefEdge{
			{From: "plan", To: "impl", Artifact: runs.ArtifactKindPlan},
			{From: "impl", To: "polish", Artifact: runs.ArtifactKindPatch},
			{From: "polish", To: "gate", Artifact: runs.ArtifactKindPatch},
		},
	}
}

// gatedRunWithExecutor is gatedRunAtItsGate with the executor under the test's
// control, for the cases that have to watch or park an attempt.
func gatedRunWithExecutor(t *testing.T, doc runs.DefinitionDoc, ex runs.Executor) (fixture, *runs.Service, runs.Run) {
	t.Helper()
	f := newFixture(t)
	svc, _ := newService(t, f, doc, runs.DefaultSnapshot())
	startOrchestrator(t, svc, ex, 30*time.Second)
	_, run, err := svc.CreateTask(context.Background(), principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "revision",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, svc, run.ID, runs.RunWaiting)
	return f, svc, run
}

// artifactRow is the artifact as this file asserts on it: the projections
// (runs.ArtifactMeta) carry neither generation nor payload until Task 11.
type artifactRow struct {
	ID         uuid.UUID
	Attempt    int32
	Generation int32
	Payload    map[string]any
	ParentIDs  []string
}

func scanArtifactRow(t *testing.T, f fixture, query string, args ...any) artifactRow {
	t.Helper()
	var a artifactRow
	var payload, parents []byte
	if err := f.pool.QueryRow(context.Background(), query, args...).
		Scan(&a.ID, &a.Attempt, &a.Generation, &payload, &parents); err != nil {
		t.Fatalf("read artifact (%v): %v", args, err)
	}
	if err := json.Unmarshal(payload, &a.Payload); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(parents, &a.ParentIDs); err != nil {
		t.Fatal(err)
	}
	return a
}

// currentArtifact reads the node's current artifact of a type with the same
// ORDER BY GetNodeArtifact uses, so the row the test names is the row the
// runtime would bind.
func currentArtifact(t *testing.T, f fixture, runID uuid.UUID, nodeID, typ string) artifactRow {
	t.Helper()
	return scanArtifactRow(t, f,
		`SELECT id, attempt, generation, payload, parent_ids FROM artifacts
		 WHERE run_id = $1 AND node_id = $2 AND type = $3
		 ORDER BY generation DESC, attempt DESC, created_at DESC, id DESC LIMIT 1`,
		runID, nodeID, typ)
}

// seedArtifact reads a control-plane seed: attempt 0 at the target's own node
// id, newest generation. Distinct from currentArtifact, which deliberately
// prefers the produced row.
func seedArtifact(t *testing.T, f fixture, runID uuid.UUID, nodeID, typ string) artifactRow {
	t.Helper()
	return scanArtifactRow(t, f,
		`SELECT id, attempt, generation, payload, parent_ids FROM artifacts
		 WHERE run_id = $1 AND node_id = $2 AND type = $3 AND attempt = 0
		 ORDER BY generation DESC LIMIT 1`,
		runID, nodeID, typ)
}

func runGeneration(t *testing.T, f fixture, runID uuid.UUID) int32 {
	t.Helper()
	var gen int32
	if err := f.pool.QueryRow(context.Background(),
		`SELECT generation FROM runs WHERE id = $1`, runID).Scan(&gen); err != nil {
		t.Fatal(err)
	}
	return gen
}

// phaseStatusAt reads one attempt row by ORDINAL. Keying on the attempt and
// reading the generation back separately (phaseGeneration) is what keeps a
// generation assertion from comparing a value the selector itself supplied.
func phaseStatusAt(t *testing.T, f fixture, runID uuid.UUID, nodeID string, attempt int32) runs.PhaseStatus {
	t.Helper()
	var s string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT status FROM phase_runs WHERE run_id = $1 AND node_id = $2 AND attempt = $3`,
		runID, nodeID, attempt).Scan(&s); err != nil {
		t.Fatalf("read %s#%d: %v", nodeID, attempt, err)
	}
	return runs.PhaseStatus(s)
}

func countPhaseRows(t *testing.T, f fixture, runID uuid.UUID, nodeID string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM phase_runs WHERE run_id = $1 AND node_id = $2`,
		runID, nodeID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// livePhase reports the node's non-terminal row, if it has one. A rewound node
// keeps its superseded rows, so "has a row" and "is live" stop being the same
// question the moment a revision opens.
func livePhase(t *testing.T, f fixture, runID uuid.UUID, nodeID string) (string, bool) {
	t.Helper()
	var status string
	err := f.pool.QueryRow(context.Background(),
		`SELECT status FROM phase_runs
		 WHERE run_id = $1 AND node_id = $2
		   AND status IN ('pending', 'running', 'waiting_decision', 'reconciling')`,
		runID, nodeID).Scan(&status)
	if err != nil {
		return "", false
	}
	return status, true
}

// executionCount is how many times a node actually RAN. A phase-row count
// cannot answer that — a revision inserts rows before anything executes — so
// this counts the attempts that produced an artifact, with attempt 0 excluded
// because that ordinal belongs to the control plane's seeds.
func executionCount(t *testing.T, f fixture, runID uuid.UUID, nodeID string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(DISTINCT attempt) FROM artifacts
		 WHERE run_id = $1 AND node_id = $2 AND attempt > 0`, runID, nodeID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// rewindIntentStatus walks the run's REAL publish intent back to an earlier
// state of the same machine.
//
// Task 9 made the publication real, so the intent this run holds is already
// 'published' by the time a later gate can be decided at all — the run cannot
// reach 'waiting' while a publish phase is still live. Rewinding the row is
// therefore the only way left to ask the refusal about 'pending' and 'pushed',
// and it is a faithful question: those are states the machine really occupies,
// and each one is exactly the window openRevision must refuse in. The receipt
// columns follow the status, because each is written in the transaction after
// its effect landed: a pending intent has no head sha, a pushed one has no
// pull request.
func rewindIntentStatus(t *testing.T, f fixture, runID uuid.UUID, nodeID, status string) {
	t.Helper()
	tag, err := f.pool.Exec(context.Background(),
		`UPDATE publish_intents SET status = $3,
		        head_sha   = CASE WHEN $3 IN ('pushed', 'published') THEN head_sha END,
		        pr_number  = CASE WHEN $3 = 'published' THEN pr_number END,
		        pr_url     = CASE WHEN $3 = 'published' THEN pr_url END
		 WHERE run_id = $1 AND node_id = $2`,
		runID, nodeID, status)
	if err != nil {
		t.Fatalf("rewind %s intent to %s: %v", nodeID, status, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("rewind %s intent to %s: updated %d rows, want the one this run published",
			nodeID, status, tag.RowsAffected())
	}
}

// seedValidAuthorization writes an approved_patches row in the shape
// mintAuthorization writes, for a publish node the run has NOT reached.
//
// It is seeded rather than driven, and the reason is worth stating: after
// Task 9 a gate's approval is followed immediately by its publication, and a
// run cannot be 'waiting' at another gate while a publish phase is live. So a
// VALID (unspent) authorization and a decidable gate cannot coexist in this
// plan's definitions — the authorization the publish consumed is 'consumed',
// which InvalidateAuthorizations deliberately will not touch. The statement's
// 'valid'-only predicate still has to hold for the definitions that will exist
// later, and this is the only way to ask it.
func seedValidAuthorization(t *testing.T, f fixture, runID uuid.UUID, gateNode, publishNode string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var artifactID uuid.UUID
	if err := f.pool.QueryRow(ctx,
		`SELECT id FROM artifacts WHERE run_id = $1 AND type = 'patch' ORDER BY created_at DESC LIMIT 1`,
		runID).Scan(&artifactID); err != nil {
		t.Fatalf("seed authorization: no patch artifact: %v", err)
	}
	var phaseID uuid.UUID
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, status, generation, created_at)
		 VALUES ($1, $2, 99, 3, 'succeeded', 1, now()) RETURNING id`,
		runID, gateNode+"_seed").Scan(&phaseID); err != nil {
		t.Fatalf("seed authorization: phase: %v", err)
	}
	var decisionID uuid.UUID
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO gate_decisions (phase_run_id, run_id, node_id, generation, decision,
		                             examined_artifact_id, acted_by, acted_at)
		 VALUES ($1, $2, $3, 1, 'approve', $4, $5, now()) RETURNING id`,
		phaseID, runID, gateNode, artifactID, f.userID).Scan(&decisionID); err != nil {
		t.Fatalf("seed authorization: decision: %v", err)
	}
	var authID uuid.UUID
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO approved_patches (run_id, gate_node_id, publish_node_id, gate_decision_id,
		                               artifact_id, base_sha, tree_hash, repo_id, forge_kind,
		                               repo_slug, target_branch, generation, status, created_at)
		 SELECT $1, $2, $3, $4, $5, 'basesha0000000000000000000000000000000000', 'treehash',
		        r.id, r.forge_kind, r.slug, r.default_branch, 1, 'valid', now()
		 FROM repos r WHERE r.id = $6 RETURNING id`,
		runID, gateNode, publishNode, decisionID, artifactID, f.repoID).Scan(&authID); err != nil {
		t.Fatalf("seed authorization: approved patch: %v", err)
	}
	return authID
}

// auditDetailInt is auditDetailBool's numeric sibling; the revision path's
// interesting detail keys are counts and ordinals rather than flags.
func auditDetailInt(t *testing.T, f fixture, action, subjectID, key string) int {
	t.Helper()
	var got int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT (detail->>$3)::int FROM audit_entries WHERE action = $1 AND subject_id = $2`,
		action, subjectID, key,
	).Scan(&got); err != nil {
		t.Fatalf("read audit %s/%s detail %q: %v", action, subjectID, key, err)
	}
	return got
}

// waitForRequest polls a bindingRecorder for the request one attempt was
// handed. buildRequest runs before the executor is called, so the recorder
// sees the inputs exactly as assembled.
func waitForRequest(t *testing.T, rec *bindingRecorder, runID uuid.UUID, nodeID string, attempt int32) runs.ExecutionRequest {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range rec.requests() {
			if r.Ref.RunID == runID && r.Ref.NodeID == nodeID && r.Ref.Attempt == attempt {
				return r
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no execution request for %s#%d", nodeID, attempt)
	return runs.ExecutionRequest{}
}

func TestRequestChanges_RewindsTheSliceAndReDecidesOnTheNewPatch(t *testing.T) {
	f, svc, run := gatedRunAtItsGate(t, gatedConveyorDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	firstPatch := currentArtifact(t, f, run.ID, "impl", runs.ArtifactKindPatch)

	res, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges,
		Note: "rename the exported symbol",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Generation != 2 {
		t.Fatalf("generation = %d, want 2", res.Generation)
	}

	// The run re-runs the slice and stops at the gate again with a NEW patch.
	// Everything below is counted here rather than straight after the decision
	// because openRevision materializes only the TARGET: the rest of the slice
	// becomes ready as the target's output lands, through the janitor's one
	// readiness path. Counting earlier would race that pass in both directions.
	waitForRun(t, svc, run.ID, runs.RunWaiting)

	// The slice rewound: impl, review and gate all have a second row; plan
	// (which dominates the target) does not.
	for _, node := range []string{"impl", "review", "gate"} {
		if n := countPhaseRows(t, f, run.ID, node); n != 2 {
			t.Errorf("%s has %d rows, want 2 (one per generation)", node, n)
		}
	}
	if n := countPhaseRows(t, f, run.ID, "plan"); n != 1 {
		t.Errorf("plan has %d rows; a revision must not rewind past its target", n)
	}
	// The old rows stepped aside. Assert SUPERSEDED exactly — accepting
	// "succeeded or superseded" would pass on the version of this design where
	// a succeeded slice node is left alone, which is the version where the
	// node never re-runs and the gate re-decides against a stale verdict.
	if got := phaseStatusAt(t, f, run.ID, "review", 1); got != runs.PhaseSuperseded {
		t.Errorf("review#1 = %s, want superseded", got)
	}

	secondPatch := currentArtifact(t, f, run.ID, "impl", runs.ArtifactKindPatch)
	if secondPatch.ID == firstPatch.ID {
		t.Fatal("the gate must re-inspect a NEW terminal patch, not the one it rejected")
	}
	if secondPatch.Generation != 2 || secondPatch.Attempt != 2 {
		t.Errorf("new patch = generation %d attempt %d, want the produced generation-2 one",
			secondPatch.Generation, secondPatch.Attempt)
	}
	// The regenerated gate row carries the new generation too. Nothing else
	// asserts that from the WRITING end: the gate is materialized by the same
	// pass as every other node, and a 1 here is legal and therefore silent.
	if got := phaseGeneration(t, f, run.ID, "gate", 2); got != 2 {
		t.Errorf("the regenerated gate row carries generation %d, want 2", got)
	}

	// The timeline records the round. This is the operator's only view of a
	// revision — the phase rows say a node re-ran, nothing else says WHY — and
	// Task 11 builds the API on top of it. Asserted before the second decision
	// so a stray second emission cannot hide behind it.
	if n := countEvents(t, svc, run.ID, runs.EventRunRevisionOpened); n != 1 {
		t.Errorf("%s emitted %d times, want 1", runs.EventRunRevisionOpened, n)
	}
	if n := countEvents(t, svc, run.ID, runs.EventGateDecided); n != 1 {
		t.Errorf("%s emitted %d times, want 1", runs.EventGateDecided, n)
	}
	// And the audit entry carries the round the decision opened, not the one it
	// decided. The two differ by one here, so a wrong source is visible.
	if got := auditDetailInt(t, f, "gate.decided", res.DecisionID.String(), "generation"); got != 2 {
		t.Errorf("audit gate.decided generation = %d, want the opened 2", got)
	}

	// And approving now binds the SECOND patch. An old approval can never
	// authorize a new artifact, and this is the other half of that: a new
	// approval never authorizes an old one — nor the seeded copy of it.
	res2, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res2.AuthorizationID == nil {
		t.Fatal("the regenerated gate still authorizes the publish")
	}
	if auth := authorization(t, f, *res2.AuthorizationID); auth.ArtifactID != secondPatch.ID {
		t.Errorf("authorization binds %s, want the regenerated %s", auth.ArtifactID, secondPatch.ID)
	}
}

// The sharpest edge of the whole generation design. Counting phase ROWS is not
// enough — a row can exist without the node having run — and a gate that
// re-decides is not enough either, because it will happily re-decide against
// the PREVIOUS generation's review verdict. What must be true is that the
// intermediate node produced a NEW artifact of its own.
func TestRequestChanges_DownstreamSliceNodesReallyReExecute(t *testing.T) {
	f, svc, run := gatedRunAtItsGate(t, gatedConveyorDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	firstVerdict := currentArtifact(t, f, run.ID, "review", runs.ArtifactKindReviewVerdict)

	if _, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: "revise",
	}); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, svc, run.ID, runs.RunWaiting)

	// review is INSIDE the slice, so it must have executed a second time and
	// produced its own generation-2 verdict.
	secondVerdict := currentArtifact(t, f, run.ID, "review", runs.ArtifactKindReviewVerdict)
	if secondVerdict.ID == firstVerdict.ID {
		t.Fatal("review did not re-execute: the gate is re-deciding against generation 1's verdict")
	}
	if secondVerdict.Generation != 2 {
		t.Errorf("new verdict generation = %d, want 2", secondVerdict.Generation)
	}
	if got := executionCount(t, f, run.ID, "review"); got != 2 {
		t.Errorf("review executed %d times, want 2: a rewound node runs again, it does not merely get a row", got)
	}
	// plan dominates the target and is OUTSIDE the slice: it must not re-run.
	if got := executionCount(t, f, run.ID, "plan"); got != 1 {
		t.Errorf("plan executed %d times, want 1: a revision never rewinds past its target", got)
	}
	// The generation-1 rows stepped aside rather than being deleted, and the
	// attempt ordinal continued rather than colliding with them.
	if got := phaseStatusAt(t, f, run.ID, "review", 1); got != runs.PhaseSuperseded {
		t.Errorf("review#1 = %s, want superseded", got)
	}
	if got := phaseGeneration(t, f, run.ID, "review", 2); got != 2 {
		t.Errorf("review#2 generation = %d, want 2", got)
	}
}

func TestRequestChanges_SeedsTheTargetWithTheExaminedArtifactAndTheNote(t *testing.T) {
	rec := &bindingRecorder{inner: fake.NewExecutor()}
	f, svc, run := gatedRunWithExecutor(t, gatedConveyorDoc(), rec)
	ctx := context.Background()

	examined := currentArtifact(t, f, run.ID, "impl", runs.ArtifactKindPatch)

	if _, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: "use a table test",
	}); err != nil {
		t.Fatal(err)
	}

	note := seedArtifact(t, f, run.ID, "impl", runs.ArtifactKindGateNote)
	if got := note.Payload["note"]; got != "use a table test" {
		t.Errorf("note payload = %v, want the human's words", got)
	}
	if got := note.Payload["gate_node_id"]; got != "gate" {
		t.Errorf("note must name its gate, got %v", got)
	}
	if got := note.Payload["examined_artifact_id"]; got != examined.ID.String() {
		t.Errorf("note must name what was examined, got %v", got)
	}

	// The seed is a COPY, so its id is new by construction; what makes it the
	// examined artifact is its parent and its payload.
	seed := seedArtifact(t, f, run.ID, "impl", runs.ArtifactKindPatch)
	if len(seed.ParentIDs) != 1 || seed.ParentIDs[0] != examined.ID.String() {
		t.Errorf("seeded patch parents = %v, want the examined %s", seed.ParentIDs, examined.ID)
	}
	if seed.Payload["diff"] != examined.Payload["diff"] {
		t.Error("the seeded patch must carry the examined patch's content")
	}

	// The re-run impl attempt received BOTH: the patch it produced last round
	// (so it revises rather than restarting) and the note.
	req := waitForRequest(t, rec, run.ID, "impl", 2)
	var sawPatch, sawNote bool
	patches := 0
	for _, in := range req.Inputs {
		switch in.Type {
		case runs.ArtifactKindPatch:
			patches++
			sawPatch = in.ID == seed.ID
		case runs.ArtifactKindGateNote:
			sawNote = true
		}
	}
	if !sawPatch {
		t.Error("the revision target must be seeded with the examined artifact")
	}
	if !sawNote {
		t.Error("the revision target must be seeded with the gate note")
	}
	// One patch reaches a phase, ever: the runner refuses a second outright
	// (runner/execute.go lineageInto), so a seed that merely joined the
	// structural binding would kill the attempt rather than revise it.
	if patches != 1 {
		t.Errorf("the attempt was handed %d patches, want exactly 1", patches)
	}

	// And the revised output records what it was actually given: the seeds are
	// in its lineage, not only the structural inputs.
	waitForRun(t, svc, run.ID, runs.RunWaiting)
	revised := currentArtifact(t, f, run.ID, "impl", runs.ArtifactKindPatch)
	var sawSeedParent, sawNoteParent bool
	for _, p := range revised.ParentIDs {
		if p == seed.ID.String() {
			sawSeedParent = true
		}
		if p == note.ID.String() {
			sawNoteParent = true
		}
	}
	if !sawSeedParent || !sawNoteParent {
		t.Errorf("revised patch parents = %v, want them to include the seed %s and the note %s",
			revised.ParentIDs, seed.ID, note.ID)
	}
}

// One patch reaches a phase, ever (Plan 07's cumulative-patch contract): every
// runner-produced patch diffs from the run's base and subsumes its ancestors,
// so a chain would double-apply, and the runner refuses the second input
// outright with a permanent staging failure (runner/execute.go lineageInto).
//
// A revision target that also binds a patch through an edge is where the two
// meet. The seed wins: it is the artifact the human looked at and asked to be
// changed, and the structural one comes from an ancestor OUTSIDE the slice
// that produced nothing new. This is what makes collapsePatchInputs the last
// statement of buildRequest rather than a step somewhere in the middle —
// called before the seeds are appended it keeps the structural patch and the
// seed still follows it, which is two patches and a dead attempt.
func TestRequestChanges_TheSeededPatchDisplacesTheTargetsStructuralOne(t *testing.T) {
	rec := &bindingRecorder{inner: fake.NewExecutor()}
	f, svc, run := gatedRunWithExecutor(t, patchTargetDoc(), rec)
	ctx := context.Background()

	// What polish#1 was handed structurally, and what polish#2 must NOT be
	// handed alongside the seed. impl is outside the slice, so this row is
	// still the current one when polish re-runs.
	structural := currentArtifact(t, f, run.ID, "impl", runs.ArtifactKindPatch)
	if first := waitForRequest(t, rec, run.ID, "polish", 1); !hasPatchInput(first, structural.ID) {
		t.Fatalf("polish#1 was not handed impl's patch %s; this fixture does not set up the collision it claims to",
			structural.ID)
	}

	if _, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: "look again",
	}); err != nil {
		t.Fatal(err)
	}

	seed := seedArtifact(t, f, run.ID, "polish", runs.ArtifactKindPatch)
	if seed.ID == structural.ID {
		t.Fatal("the seed must be a copy with its own id, or this test cannot tell the two apart")
	}
	req := waitForRequest(t, rec, run.ID, "polish", 2)
	var patches []uuid.UUID
	for _, in := range req.Inputs {
		if in.Type == runs.ArtifactKindPatch {
			patches = append(patches, in.ID)
		}
	}
	if len(patches) != 1 {
		t.Fatalf("polish#2 was handed %d patches (%v), want exactly 1: the runner refuses a second one",
			len(patches), patches)
	}
	if patches[0] != seed.ID {
		t.Errorf("polish#2 was handed %s, want the seeded %s rather than the structural %s",
			patches[0], seed.ID, structural.ID)
	}
}

// An older round's note is spent. ListSeedArtifacts filters seeds down to the
// run's CURRENT generation, and without that filter round two's target would
// be handed round one's note as well — two contradictory instructions in one
// prompt, and, on a target that also binds a patch, two patch seeds for
// collapsePatchInputs to pick between by uuid rather than by generation.
func TestRequestChanges_ASecondRoundSeesOnlyItsOwnNote(t *testing.T) {
	rec := &bindingRecorder{inner: fake.NewExecutor()}
	f, svc, run := gatedRunWithExecutor(t, gatedConveyorDoc(), rec)
	ctx := context.Background()

	for round := 1; round <= 2; round++ {
		if _, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
			RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges,
			Note: fmt.Sprintf("round %d", round),
		}); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		waitForRun(t, svc, run.ID, runs.RunWaiting)
	}

	// The positive control: two rounds really did write two notes, so an
	// unfiltered read would have something to over-return.
	if n := countSeedArtifacts(t, f, run.ID, "impl", runs.ArtifactKindGateNote); n != 2 {
		t.Fatalf("the run holds %d gate notes, want 2: this test cannot detect an unfiltered read", n)
	}

	req := waitForRequest(t, rec, run.ID, "impl", 3)
	var notes []any
	for _, in := range req.Inputs {
		if in.Type == runs.ArtifactKindGateNote {
			notes = append(notes, in.Payload["note"])
		}
	}
	if len(notes) != 1 || notes[0] != "round 2" {
		t.Errorf("impl#3 was handed notes %v, want only round 2's", notes)
	}
}

func countSeedArtifacts(t *testing.T, f fixture, runID uuid.UUID, nodeID, typ string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM artifacts
		 WHERE run_id = $1 AND node_id = $2 AND type = $3 AND attempt = 0`,
		runID, nodeID, typ).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func hasPatchInput(req runs.ExecutionRequest, id uuid.UUID) bool {
	for _, in := range req.Inputs {
		if in.Type == runs.ArtifactKindPatch && in.ID == id {
			return true
		}
	}
	return false
}

// A revision must leave no spendable token behind: every LIVE authorization
// the run holds is invalidated as the new generation opens (spec §2).
//
// It is driven against a seeded authorization, and Task 9 is why. Before it, a
// gate could approve and its publish node would quietly succeed through the
// fake executor, so a second gate downstream could still request changes with
// the first gate's token unspent. Now the approval is followed by a real
// publication: the token is CONSUMED — which InvalidateAuthorizations
// deliberately will not touch — and openRevision refuses outright once any
// intent exists (TestRequestChanges_RefusedOnceAPublicationHasSettled). A
// valid authorization and a decidable gate no longer coexist in a run this
// plan's definitions can produce, so the 'valid'-only predicate is asked
// directly instead of through a shape that cannot happen.
func TestRequestChanges_InvalidatesEveryLiveAuthorization(t *testing.T) {
	f, svc, run := gatedRunAtItsGate(t, gatedConveyorDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	authID := seedValidAuthorization(t, f, run.ID, "gate", "publish")

	if _, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: "start over",
	}); err != nil {
		t.Fatal(err)
	}
	if got := authorization(t, f, authID).Status; got != "invalidated" {
		t.Fatalf("prior authorization status = %q, want invalidated: a newer generation invalidates it (spec §2)", got)
	}
}

// Chained gates are legal and each authorizes its own publish. So the first
// publication can be live on the forge while a later gate is still waiting —
// and no transaction in this process can un-push a commit. Invalidating
// authorizations does not help: the one that funded the publication is
// CONSUMED, not valid.
//
// gate_one's approval now drives a REAL publication — a push and a pull
// request standing between two gates — so the 'published' case is end to end.
// The other two are the same row walked back to the earlier states of the same
// machine (rewindIntentStatus): a run cannot be waiting at gate_two while
// publish_one is still live, so those windows are unobservable any other way.
//
// EVERY status refuses, 'pending' included, and each case is run because the
// statement's own semantics turn on that: narrowing CountSettledPublications
// to status = 'published' is a one-word edit that leaves the design's own
// reasoning unpinned. A pending intent commits BEFORE the first forge call, so
// it is precisely the window in which a push may be in flight; a pushed one has
// a branch on the forge already. Neither can be taken back by a revision.
func TestRequestChanges_RefusedOnceAPublicationHasSettled(t *testing.T) {
	for _, status := range []string{"pending", "pushed", "published"} {
		t.Run(status, func(t *testing.T) {
			f, svc, run := gatedRunAtItsGate(t, twoGateDoc(), runs.DefaultSnapshot())
			ctx := context.Background()

			first, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
				RunID: run.ID, NodeID: "gate_one", Decision: runs.DecisionApprove,
			})
			if err != nil {
				t.Fatal(err)
			}
			if first.AuthorizationID == nil {
				t.Fatal("gate_one authorizes publish_one")
			}
			waitForRun(t, svc, run.ID, runs.RunWaiting) // gate_two
			if got := intentRow(t, f, run.ID, "publish_one").Status; got != "published" {
				t.Fatalf("publish_one status = %q, want published: gate_one's approval publishes for real now", got)
			}
			rewindIntentStatus(t, f, run.ID, "publish_one", status)

			_, err = svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
				RunID: run.ID, NodeID: "gate_two", Decision: runs.DecisionRequestChanges, Note: "start over",
			})
			if xerrors.ClassOf(err) != xerrors.ClassConflict {
				t.Fatalf("error = %v (class %v), want a conflict naming the live publication", err, xerrors.ClassOf(err))
			}
			if got := runGeneration(t, f, run.ID); got != 1 {
				t.Errorf("generation = %d, want 1: the refused revision must open nothing", got)
			}
			// The published branch is untouched: no transaction in this
			// process can un-push a commit, so the refusal must also not
			// disturb the record of it.
			if got := intentRow(t, f, run.ID, "publish_one").Status; got != status {
				t.Errorf("publication status = %q, want it left alone at %q", got, status)
			}
			// The human still has both real answers available.
			if _, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
				RunID: run.ID, NodeID: "gate_two", Decision: runs.DecisionApprove,
			}); err != nil {
				t.Fatalf("approve must still be available: %v", err)
			}
		})
	}
}

func TestRequestChanges_RefusesOnceMaxRoundsIsSpent(t *testing.T) {
	f, svc, run := gatedRunAtItsGate(t, gatedConveyorDoc(), runs.DefaultSnapshot()) // max_rounds 2
	ctx := context.Background()

	for round := 1; round <= 2; round++ {
		if _, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
			RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges,
			Note: fmt.Sprintf("round %d", round),
		}); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		waitForRun(t, svc, run.ID, runs.RunWaiting)
	}

	_, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: "one more",
	})
	if xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("error = %v (class %v), want a conflict naming the exhausted loop", err, xerrors.ClassOf(err))
	}
	// The gate is still decidable — the human must approve or reject, and the
	// run is NOT killed over a click.
	got, _, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != runs.RunWaiting {
		t.Fatalf("run status = %s, want it still waiting", got.Status)
	}
	if got := runGeneration(t, f, run.ID); got != 3 {
		t.Errorf("generation = %d, want 3: two rounds opened two generations and the refusal opened none", got)
	}
	if _, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionReject,
	}); err != nil {
		t.Fatalf("reject must still be available: %v", err)
	}
}

func TestRequestChanges_RefusedOnAGateThatDeclaresNoTransition(t *testing.T) {
	f, svc, run := gatedRunAtItsGate(t, noRevisionDoc(), runs.DefaultSnapshot())

	_, err := svc.DecideGate(context.Background(), principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: "n/a",
	})
	if xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatalf("error = %v (class %v), want invalid", err, xerrors.ClassOf(err))
	}
	if got := runGeneration(t, f, run.ID); got != 1 {
		t.Errorf("generation = %d, want 1: a refused revision opens nothing", got)
	}
}

// "Lineage extends across generations on the run's budget" (spec §3): a
// revision is not a fresh run and does not refund anything.
//
// This is a cheap regression fence, not coverage of the rule: openRevision
// touches neither column and AddRunUsage already clamps a negative delta in
// SQL, so no plausible wrong implementation fails it.
func TestRequestChanges_BudgetAndDeadlineSurviveTheGeneration(t *testing.T) {
	f, svc, run := gatedRunAtItsGate(t, gatedConveyorDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	before, _, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: "again",
	}); err != nil {
		t.Fatal(err)
	}
	after, _, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ExecutionUsedMS < before.ExecutionUsedMS {
		t.Errorf("usage fell from %d to %d; a revision refunds nothing", before.ExecutionUsedMS, after.ExecutionUsedMS)
	}
	if !after.WallDeadline.Equal(before.WallDeadline) {
		t.Errorf("wall deadline moved from %s to %s; the clock never pauses", before.WallDeadline, after.WallDeadline)
	}
}

// The seeded copy shares the target's node id, so between the seed landing and
// the new attempt producing its own patch, "the target's current patch" IS the
// seed. That is safe only because no successor is ready to ask in that window
// — the target is pending and every other slice node was superseded with it.
// This test freezes the loop to hold the window open and checks exactly that.
func TestRequestChanges_SeedIsNeverReadAsTheTargetsProducedArtifact(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, gatedConveyorDoc(), runs.DefaultSnapshot())
	_, stop := startStoppableOrchestrator(t, svc, fake.NewExecutor(), 30*time.Second)
	ctx := context.Background()

	_, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "seed-window",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, svc, run.ID, runs.RunWaiting)
	stop() // freeze so the window stays open

	if _, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: "hold",
	}); err != nil {
		t.Fatal(err)
	}

	// The window is real: the newest impl patch right now is the attempt-0 seed.
	current := currentArtifact(t, f, run.ID, "impl", runs.ArtifactKindPatch)
	if current.Attempt != 0 || current.Generation != 2 {
		t.Fatalf("current impl patch = generation %d attempt %d, want the generation-2 seed; "+
			"the window this test guards does not exist as described", current.Generation, current.Attempt)
	}
	// And nobody can read it: the only live row is the target's own.
	for _, node := range []string{"review", "gate", "publish"} {
		if status, ok := livePhase(t, f, run.ID, node); ok {
			t.Errorf("%s has a live row (%s) while the target has not re-run", node, status)
		}
	}
	if status, ok := livePhase(t, f, run.ID, "impl"); !ok || status != "pending" {
		t.Errorf("impl live row = %q/%v, want a pending one: the revision target must be the run's only claimable work",
			status, ok)
	}
}

// revisionRacer parks one named attempt until released, so a test can observe
// the run mid-revision — after the decision has committed and before the new
// generation's first attempt has produced anything.
type revisionRacer struct {
	inner    runs.Executor
	node     string
	attempt  int32
	release  chan struct{}
	inFlight chan struct{}
	once     sync.Once
}

func (e *revisionRacer) Execute(ctx context.Context, req runs.ExecutionRequest) (runs.ExecutionResult, error) {
	if req.Ref.NodeID == e.node && req.Ref.Attempt == e.attempt {
		e.once.Do(func() { close(e.inFlight) })
		select {
		case <-e.release:
		case <-ctx.Done():
			return runs.ExecutionResult{}, ctx.Err()
		}
	}
	return e.inner.Execute(ctx, req)
}

func (e *revisionRacer) Inspect(ctx context.Context, ref runs.AttemptRef) (runs.InspectResult, error) {
	return e.inner.Inspect(ctx, ref)
}

func (e *revisionRacer) waitUntilInFlight(t *testing.T) {
	t.Helper()
	select {
	case <-e.inFlight:
	case <-time.After(20 * time.Second):
		t.Fatal("the revision target never reached the executor")
	}
}

// Every row a revised generation writes must carry the run's CURRENT
// generation — including the rows openRevision itself inserts before any
// worker touches them, and the rows the released attempt's evaluation writes
// afterwards. A missed stamp does not fail: 1 is a legal generation, and
// GetNodeArtifact's provenance-first ordering then serves the STALE artifact
// downstream, silently.
//
// Unlike Task 4's generationBumper — a raw UPDATE from inside an attempt —
// the bump here is a real DecideGate transaction, so this is the first test
// that stamps generations through the code path production uses.
//
// It is NOT a test of the GetRunForUpdate lock in generationOf, and it cannot
// be: a request_changes decision provably cannot commit while another attempt
// is in flight. BumpRunGeneration and FinishGatePhase both predicate on the
// run being 'waiting' (queries.sql), and RunIsWaitingOnAGate reports waiting
// only when the run has no other live phase row, so the two transactions are
// serialized by the run's own status rather than by the lock. The strongest
// leg is ClaimPhase's own join: it requires r.status = 'running' on the run
// row, so no attempt can be CLAIMED while the run waits — and none can already
// be running, because RunIsWaitingOnAGate would not have reported waiting with
// a live row present. The exclusion therefore holds across the whole waiting
// interval, not merely at the instant of the transition. Task 4's
// generationBumper stays the probe for the lock itself.
func TestRequestChanges_EveryRowOfTheNewGenerationCarriesIt(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, gatedConveyorDoc(), runs.DefaultSnapshot())
	ex := &revisionRacer{
		inner: fake.NewExecutor(), node: "impl", attempt: 2,
		release: make(chan struct{}), inFlight: make(chan struct{}),
	}
	startOrchestrator(t, svc, ex, 30*time.Second)
	ctx := context.Background()

	_, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "generation stamping",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, svc, run.ID, runs.RunWaiting)

	if _, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: "one",
	}); err != nil {
		t.Fatal(err)
	}
	// impl#2 is claimed and parks inside the executor. Everything asserted
	// here was written by the DECISION, before any evaluation could revise it.
	ex.waitUntilInFlight(t)
	if got := runGeneration(t, f, run.ID); got != 2 {
		t.Fatalf("run generation = %d, want 2", got)
	}
	if got := phaseGeneration(t, f, run.ID, "impl", 2); got != 2 {
		t.Errorf("the phase row the decision inserted carries generation %d, want 2", got)
	}

	close(ex.release)
	waitForRun(t, svc, run.ID, runs.RunWaiting)

	// (a) No row of either table sits above the run's committed generation,
	// and the generation-2 attempt's own rows carry 2 rather than the older
	// value, which is legal and therefore silent.
	gen := runGeneration(t, f, run.ID)
	var aheadPhases, aheadArtifacts int
	if err := f.pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM phase_runs WHERE run_id = $1 AND generation > $2),
		        (SELECT count(*) FROM artifacts  WHERE run_id = $1 AND generation > $2)`,
		run.ID, gen).Scan(&aheadPhases, &aheadArtifacts); err != nil {
		t.Fatal(err)
	}
	if aheadPhases != 0 || aheadArtifacts != 0 {
		t.Fatalf("%d phase and %d artifact rows carry a generation above the run's %d",
			aheadPhases, aheadArtifacts, gen)
	}
	if got := artifactGeneration(t, f, run.ID, "impl", 2); got != gen {
		t.Errorf("impl#2 artifact generation = %d, want the run's %d", got, gen)
	}
	// The generation-1 rows kept their 1: a build that stamped everything 2
	// would pass every assertion above.
	if got := phaseGeneration(t, f, run.ID, "plan", 1); got != 1 {
		t.Errorf("plan#1 generation = %d, want 1 (it predates the revision)", got)
	}

	// (b) The downstream read returns the generation-2 patch, never the
	// generation-1 one and never the seed.
	patch := currentArtifact(t, f, run.ID, "impl", runs.ArtifactKindPatch)
	if patch.Generation != gen || patch.Attempt != 2 {
		t.Errorf("current impl patch = generation %d attempt %d, want the produced generation-%d one",
			patch.Generation, patch.Attempt, gen)
	}
}
