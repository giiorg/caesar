//go:build integration

package runs_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

// checkpointDoc is agenticDoc closed with a gate that authorizes NOTHING:
// plan -> impl -> review -> gate, with no git-publish anywhere. The
// compiler's dominance rule runs the other way (every publish needs a gate,
// never the reverse), so this is a legal shape and it is the one that proves
// an approval mints a token only when something can spend it.
func checkpointDoc() runs.DefinitionDoc {
	doc := agenticDoc()
	doc.Nodes = append(doc.Nodes,
		runs.DefNode{ID: "gate", Kind: runs.KindGate, Revision: &runs.DefRevision{Target: "impl", MaxRounds: 2}})
	doc.Edges = append(doc.Edges,
		runs.DefEdge{From: "impl", To: "gate", Artifact: runs.ArtifactKindPatch},
		runs.DefEdge{From: "review", To: "gate", Artifact: runs.ArtifactKindReviewVerdict})
	return doc
}

// authorizationRow is the ApprovedPatch as this file asserts on it. Read with
// a raw query rather than through the store package: these tests live outside
// `package runs` and read rows the service deliberately does not project yet
// (the API surface is Task 11).
type authorizationRow struct {
	RunID         uuid.UUID
	GateNodeID    string
	PublishNodeID string
	ArtifactID    uuid.UUID
	BaseSHA       string
	TreeHash      string
	RepoSlug      string
	TargetBranch  string
	Generation    int32
	Status        string
}

func authorization(t *testing.T, f fixture, id uuid.UUID) authorizationRow {
	t.Helper()
	var a authorizationRow
	if err := f.pool.QueryRow(context.Background(),
		`SELECT run_id, gate_node_id, publish_node_id, artifact_id, base_sha, tree_hash,
		        repo_slug, target_branch, generation, status
		 FROM approved_patches WHERE id = $1`, id,
	).Scan(&a.RunID, &a.GateNodeID, &a.PublishNodeID, &a.ArtifactID, &a.BaseSHA, &a.TreeHash,
		&a.RepoSlug, &a.TargetBranch, &a.Generation, &a.Status); err != nil {
		t.Fatalf("read authorization %s: %v", id, err)
	}
	return a
}

func countAuthorizations(t *testing.T, f fixture, runID uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM approved_patches WHERE run_id = $1`, runID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// nodeArtifact reads a node's current artifact of a type the same way the
// runtime does — newest provenance first — so the id the test compares
// against is the one the decision transaction had to bind.
func nodeArtifact(t *testing.T, f fixture, runID uuid.UUID, nodeID, typ string) (uuid.UUID, string) {
	t.Helper()
	var id uuid.UUID
	var treeHash string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT id, coalesce(payload->>'tree_hash', '') FROM artifacts
		 WHERE run_id = $1 AND node_id = $2 AND type = $3
		 ORDER BY generation DESC, attempt DESC, created_at DESC, id DESC LIMIT 1`,
		runID, nodeID, typ,
	).Scan(&id, &treeHash); err != nil {
		t.Fatalf("read %s/%s artifact: %v", nodeID, typ, err)
	}
	return id, treeHash
}

func decisionSelfApproval(t *testing.T, f fixture, decisionID uuid.UUID) bool {
	t.Helper()
	var self bool
	if err := f.pool.QueryRow(context.Background(),
		`SELECT self_approval FROM gate_decisions WHERE id = $1`, decisionID).Scan(&self); err != nil {
		t.Fatalf("read decision %s: %v", decisionID, err)
	}
	return self
}

func auditDetailBool(t *testing.T, f fixture, action, subjectID, key string) bool {
	t.Helper()
	var got bool
	if err := f.pool.QueryRow(context.Background(),
		`SELECT (detail->>$3)::boolean FROM audit_entries WHERE action = $1 AND subject_id = $2`,
		action, subjectID, key,
	).Scan(&got); err != nil {
		t.Fatalf("read audit %s/%s detail %q: %v", action, subjectID, key, err)
	}
	return got
}

func backdateWallDeadline(t *testing.T, f fixture, runID uuid.UUID) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE runs SET wall_deadline = now() - interval '1 second' WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}
}

// gatedRunAtItsGate drives a fresh gated conveyor to its gate and hands back
// everything the decision tests need.
func gatedRunAtItsGate(t *testing.T, doc runs.DefinitionDoc, snap runs.PolicySnapshot) (fixture, *runs.Service, runs.Run) {
	t.Helper()
	f := newFixture(t)
	svc, _ := newService(t, f, doc, snap)
	startOrchestrator(t, svc, fake.NewExecutor(), 30*time.Second)
	_, run, err := svc.CreateTask(context.Background(), principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "gated",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, svc, run.ID, runs.RunWaiting)
	return f, svc, run
}

func TestDecideGate_ApproveMintsASingleUseAuthorizationBoundToTheExaminedArtifact(t *testing.T) {
	f, svc, run := gatedRunAtItsGate(t, gatedConveyorDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	// The artifact the gate examines, read before the decision: the
	// authorization must bind THIS id, not whatever is newest at publish time.
	patchID, treeHash := nodeArtifact(t, f, run.ID, "impl", runs.ArtifactKindPatch)
	if treeHash == "" {
		t.Fatal("the fake implement artifact must carry a tree hash for this test to mean anything")
	}

	res, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove, Note: "ship it",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.AuthorizationID == nil {
		t.Fatal("an approval that dominates a publish must mint an authorization")
	}
	if res.RunStatus != runs.RunRunning {
		t.Errorf("run status = %s, want running: an approval resumes the run", res.RunStatus)
	}

	auth := authorization(t, f, *res.AuthorizationID)
	if auth.ArtifactID != patchID {
		t.Errorf("authorization binds artifact %s, want the examined %s", auth.ArtifactID, patchID)
	}
	if auth.BaseSHA != run.BaseSHA {
		t.Errorf("base_sha = %q, want the run's pinned %q", auth.BaseSHA, run.BaseSHA)
	}
	if auth.TreeHash != treeHash {
		t.Errorf("tree_hash = %q, want the patch's %q", auth.TreeHash, treeHash)
	}
	if auth.PublishNodeID != "publish" || auth.GateNodeID != "gate" {
		t.Errorf("authorization scope = %s/%s, want gate/publish", auth.GateNodeID, auth.PublishNodeID)
	}
	if auth.Status != "valid" {
		t.Errorf("status = %q, want valid", auth.Status)
	}
	// The repository coordinates are pinned AS APPROVED, not left to be
	// re-read from a repo row that UpdateRepo can move (schema.sql:374).
	if auth.RepoSlug != "spine/repo" || auth.TargetBranch != "main" {
		t.Errorf("authorization destination = %s@%s, want spine/repo@main", auth.RepoSlug, auth.TargetBranch)
	}

	// The gate row succeeded and the run resumed: the publish node the gate
	// was holding back materializes and runs, which only happens if the
	// janitor picks up a run a decision moved back to 'running'.
	_, phases, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gate := phaseByNode(t, phases, "gate"); gate.Status != runs.PhaseSucceeded {
		t.Errorf("gate status = %s, want succeeded", gate.Status)
	}
	waitForRun(t, svc, run.ID, runs.RunSucceeded)
	_, phases, _, err = svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pub := phaseByNode(t, phases, "publish"); pub.Status != runs.PhaseSucceeded {
		t.Errorf("publish status = %s, want succeeded", pub.Status)
	}
	if n := countEvents(t, svc, run.ID, runs.EventGateDecided); n != 2 {
		t.Errorf("gate.decided events = %d, want 2 (the decision and the authorization it minted)", n)
	}
}

func TestDecideGate_SecondDecisionOnTheSameAttemptIsAConflict(t *testing.T) {
	f, svc, run := gatedRunAtItsGate(t, gatedConveyorDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	if _, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionReject,
	})
	if xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("second decision error = %v (class %v), want conflict", err, xerrors.ClassOf(err))
	}
	// The refused reject changed nothing: the approval still stands.
	got, _, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == runs.RunFailed {
		t.Fatal("a refused reject must not terminalize the run")
	}
	if n := countAuthorizations(t, f, run.ID); n != 1 {
		t.Errorf("authorizations = %d, want the first decision's 1", n)
	}
}

func TestDecideGate_ConcurrentDecidersProduceExactlyOneDecision(t *testing.T) {
	f, svc, run := gatedRunAtItsGate(t, gatedConveyorDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	const n = 8
	var wg sync.WaitGroup
	results := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, results[i] = svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
				RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	won := 0
	for i, err := range results {
		switch {
		case err == nil:
			won++
		case xerrors.ClassOf(err) == xerrors.ClassConflict:
		default:
			t.Errorf("decider %d got an unexpected error: %v", i, err)
		}
	}
	if won != 1 {
		t.Fatalf("%d deciders won, want exactly 1", won)
	}
	if got := countAuthorizations(t, f, run.ID); got != 1 {
		t.Fatalf("authorizations = %d, want 1: the token is single-use", got)
	}
	var decisions int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM gate_decisions WHERE run_id = $1`, run.ID).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if decisions != 1 {
		t.Fatalf("gate_decisions = %d, want 1", decisions)
	}
}

func TestDecideGate_RejectTerminalizesTheRun(t *testing.T) {
	f, svc, run := gatedRunAtItsGate(t, gatedConveyorDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	res, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionReject, Note: "wrong approach",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RunStatus != runs.RunFailed {
		t.Fatalf("run status = %s, want failed", res.RunStatus)
	}
	got, phases, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != runs.RunFailed {
		t.Fatalf("persisted run status = %s, want failed", got.Status)
	}
	if got.Outcome == nil || got.Outcome.Code != runs.CodeGateRejected {
		t.Fatalf("outcome = %+v, want gate_rejected", got.Outcome)
	}
	if !strings.Contains(got.Outcome.Message, "gate") {
		t.Errorf("the outcome must name the gate: %q", got.Outcome.Message)
	}
	if countAuthorizations(t, f, run.ID) != 0 {
		t.Error("a reject authorizes nothing")
	}
	// The publish node never materialized, so nothing is left to supersede
	// except the gate row itself.
	if gate := phaseByNode(t, phases, "gate"); gate.Status != runs.PhaseFailed {
		t.Errorf("gate status = %s, want failed", gate.Status)
	}
	if _, ok := tryPhaseByNode(phases, "publish"); ok {
		t.Error("a rejected gate must never materialize the publish it guarded")
	}
}

// The wall deadline is checked against the DATABASE clock inside the decision
// transaction, before anything is written (spec §3 Budgets).
//
// The observable end state alone would not pin that: FinishGatePhase carries
// its own `wall_deadline > now()` predicate, so deleting the up-front check
// leaves the same rows behind — the transaction just rolls back one statement
// later. The discriminator is the message. Only the up-front branch names the
// deadline; the CAS-lost branch says the gate stopped waiting.
func TestDecideGate_ExpiredRunRefusesTheDecisionAndDoesNotAuthorize(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, gatedConveyorDoc(), runs.DefaultSnapshot())
	_, stop := startStoppableOrchestrator(t, svc, fake.NewExecutor(), 30*time.Second)
	ctx := context.Background()

	_, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "gated-expired",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, svc, run.ID, runs.RunWaiting)

	stop() // freeze the sweeper so this is a pure predicate test
	backdateWallDeadline(t, f, run.ID)

	_, err = svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	})
	if xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("error = %v (class %v), want a conflict naming the expired run", err, xerrors.ClassOf(err))
	}
	if !strings.Contains(err.Error(), "wall deadline") {
		t.Errorf("error = %q, want the up-front deadline branch, which names the wall deadline", err)
	}
	if countAuthorizations(t, f, run.ID) != 0 {
		t.Fatal("an expired run must authorize nothing")
	}
	_, phases, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gate := phaseByNode(t, phases, "gate"); gate.Status != runs.PhaseWaitingDecision {
		t.Errorf("a refused decision must leave the gate waiting, got %s", gate.Status)
	}
}

func TestDecideGate_SelfApprovalIsRefusedWhenThePolicyForbidsIt(t *testing.T) {
	// The fixture's single user requests the task and then decides its gate.
	strict := runs.DefaultSnapshot()
	strict.AllowSelfApproval = false
	f, svc, run := gatedRunAtItsGate(t, gatedConveyorDoc(), strict)

	_, err := svc.DecideGate(context.Background(), principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	})
	if xerrors.ClassOf(err) != xerrors.ClassForbidden {
		t.Fatalf("error = %v (class %v), want forbidden", err, xerrors.ClassOf(err))
	}
	if countAuthorizations(t, f, run.ID) != 0 {
		t.Error("a refused self-approval must authorize nothing")
	}
}

func TestDecideGate_SelfApprovalIsMarkedWhenThePolicyAllowsIt(t *testing.T) {
	// The shipped baseline allows it (policy.go DefaultSnapshot), and marks it.
	f, svc, run := gatedRunAtItsGate(t, gatedConveyorDoc(), runs.DefaultSnapshot())

	res, err := svc.DecideGate(context.Background(), principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !decisionSelfApproval(t, f, res.DecisionID) {
		t.Error("a self-approval must be marked distinctly (spec §6)")
	}
	if !auditDetailBool(t, f, "gate.decided", res.DecisionID.String(), "self_approval") {
		t.Error("the audit entry must carry the self-approval marker too")
	}
}

func TestDecideGate_GateWithoutAPublishAuthorizesNothingAndStillResumesTheRun(t *testing.T) {
	// plan -> impl -> review -> gate: the gate is a checkpoint, not a release
	// decision. Approving it must not mint a token nothing can spend.
	f, svc, run := gatedRunAtItsGate(t, checkpointDoc(), runs.DefaultSnapshot())

	res, err := svc.DecideGate(context.Background(), principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.AuthorizationID != nil {
		t.Fatal("a gate that dominates no publish must mint no authorization")
	}
	if countAuthorizations(t, f, run.ID) != 0 {
		t.Error("a checkpoint gate authorizes nothing")
	}
	// The gate was the last node, so resuming the run completes it — and only
	// the janitor's materialization pass can notice that, because a decision
	// is not a phase evaluation.
	waitForRun(t, svc, run.ID, runs.RunSucceeded)
}

// Input validation happens before any row is read, and request_changes is
// Task 6's: it must REFUSE rather than quietly approve-by-accident
// (decisions §6.13, stubs never fake success).
func TestDecideGate_ValidationAndTheRequestChangesStub(t *testing.T) {
	f, svc, run := gatedRunAtItsGate(t, gatedConveyorDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	invalid := []struct {
		name  string
		in    runs.GateDecisionInput
		field string
	}{
		{"unknown decision", runs.GateDecisionInput{RunID: run.ID, NodeID: "gate", Decision: "lgtm"}, "decision"},
		{"empty decision", runs.GateDecisionInput{RunID: run.ID, NodeID: "gate"}, "decision"},
		{
			"note too long",
			runs.GateDecisionInput{
				RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
				Note: strings.Repeat("x", (8<<10)+1),
			},
			"note",
		},
		{
			"request_changes with no note",
			runs.GateDecisionInput{RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges},
			"note",
		},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.DecideGate(ctx, principal(f), tc.in)
			if xerrors.ClassOf(err) != xerrors.ClassInvalid {
				t.Fatalf("err = %v (class %v), want invalid", err, xerrors.ClassOf(err))
			}
			var xe *xerrors.Error
			if !errors.As(err, &xe) {
				t.Fatalf("err = %v, want a classified error", err)
			}
			blamed := false
			for _, fe := range xe.Fields {
				if fe.Field == tc.field {
					blamed = true
				}
			}
			if !blamed {
				t.Errorf("fields = %+v, want one blaming %q", xe.Fields, tc.field)
			}
		})
	}

	// A well-formed request_changes gets as far as the stub and stops there.
	_, err := svc.DecideGate(ctx, principal(f), runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: "narrow the diff",
	})
	if !errors.Is(err, xerrors.ErrNotImplemented) {
		t.Fatalf("request_changes err = %v, want not-implemented until Task 6", err)
	}

	// None of the above wrote anything: the gate is still awaiting a decision.
	var decisions int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM gate_decisions WHERE run_id = $1`, run.ID).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if decisions != 0 {
		t.Errorf("gate_decisions = %d, want 0: a refused decision writes nothing", decisions)
	}
	_, phases, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gate := phaseByNode(t, phases, "gate"); gate.Status != runs.PhaseWaitingDecision {
		t.Errorf("gate status = %s, want waiting_decision", gate.Status)
	}
}

// The approvals inbox and the API's pre-write project lookup. Both are
// ten-column positional plumbing, which is exactly the kind of code that
// compiles while transposing two uuids.
func TestListWaitingGatesAndGateProject(t *testing.T) {
	f, svc, run := gatedRunAtItsGate(t, gatedConveyorDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	projectID, err := svc.GateProject(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if projectID != f.projectID {
		t.Errorf("GateProject = %s, want %s", projectID, f.projectID)
	}
	if _, err := svc.GateProject(ctx, uuid.Max); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Errorf("GateProject of an unknown run = %v, want not-found", err)
	}

	all, err := svc.ListWaitingGates(ctx, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("waiting gates = %d, want 1", len(all))
	}
	_, phases, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	gate := phaseByNode(t, phases, "gate")
	got := all[0]
	if got.PhaseID != gate.ID || got.RunID != run.ID || got.TaskID != run.TaskID ||
		got.ProjectID != f.projectID || got.RequesterID != f.userID {
		t.Errorf("waiting gate ids wrong: %+v (phase %s, run %s, task %s, project %s, requester %s)",
			got, gate.ID, run.ID, run.TaskID, f.projectID, f.userID)
	}
	if got.NodeID != "gate" || got.TaskTitle != "gated" || got.Attempt != gate.Attempt {
		t.Errorf("waiting gate = %+v, want node gate / title gated / attempt %d", got, gate.Attempt)
	}
	if got.WaitingSince.IsZero() {
		t.Error("a waiting gate must say since when")
	}

	scoped, err := svc.ListWaitingGates(ctx, []uuid.UUID{f.projectID}, false)
	if err != nil || len(scoped) != 1 {
		t.Fatalf("scoped inbox = %d, %v", len(scoped), err)
	}
	foreign, err := svc.ListWaitingGates(ctx, []uuid.UUID{uuid.Max}, false)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("foreign inbox = %d, %v", len(foreign), err)
	}
	// An empty grant list is an empty inbox, never everything.
	empty, err := svc.ListWaitingGates(ctx, nil, false)
	if err != nil || len(empty) != 0 {
		t.Fatalf("ungranted inbox = %d, %v", len(empty), err)
	}
}
