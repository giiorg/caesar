package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// seedAttempt is the attempt ordinal a seeded artifact carries. Zero is
// unreachable for a produced artifact (phase attempts start at 1), so the
// value itself says "the control plane put this here, no phase produced it".
const seedAttempt = 0

// openRevision replays a gate's bounded loop (spec §2, §3). In ONE
// transaction, sharing the decision's row lock:
//
//  1. refuse a gate that declares no on_request_changes transition;
//  2. refuse a run that has already begun publishing — nothing here can
//     un-push a commit;
//  3. bound the loop by max_rounds;
//  4. record the decision;
//  5. retire the gate's own attempt row, which is also the statement carrying
//     the wall-deadline predicate;
//  6. bump the run's generation;
//  7. invalidate every live authorization — a newer patch generation
//     invalidates prior approvals, so nothing spendable survives;
//  8. supersede every live row in the target→gate slice;
//  9. seed the target with the examined artifact and the gate note;
//  10. materialize the target's next attempt and resume the run.
//
// Everything downstream of the gate is invalidated implicitly: readiness is
// each node's NEWEST attempt row, and the slice's rows are now pending.
func (s *Service) openRevision(ctx context.Context, tx pgx.Tx, rc revisionContext, res *GateDecisionResult) error {
	q := store.New(tx)
	node := rc.plan.Nodes[rc.in.NodeID]
	if node.Revision == nil {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "decision", Message: fmt.Sprintf("gate %q declares no on_request_changes transition", rc.in.NodeID),
		})
	}
	slice, ok := rc.plan.RevisionSlice(rc.in.NodeID)
	if !ok {
		return fmt.Errorf("runs: gate %q declares a revision with no compiled slice", rc.in.NodeID)
	}

	// A revision may not rewind work that has already been published.
	//
	// Chained gates are legal, and each authorizes its own publish
	// (workflows/validate_test.go, "chained gates each authorize their own
	// publish"). So gate_one can approve, its publish node can push a branch
	// and open a pull request, and gate_two — still waiting — can then be sent
	// back for changes. Invalidating authorizations does nothing about that:
	// the first one was CONSUMED, and the plan deliberately leaves consumed
	// rows alone because rewriting them would erase the fact that they funded
	// a publication. The branch and the pull request stay live on the forge,
	// now carrying work of a generation caesar considers superseded — which is
	// exactly what spec §3 forbids ("every downstream output and approval of
	// the superseded generation is invalidated").
	//
	// Nothing in this transaction can un-push a commit, so the honest move is
	// to refuse the decision, the same way an exhausted max_rounds does
	// (decision 12): the human keeps approve and reject, and no work is
	// destroyed over a click. Structural refusal in the validator was the
	// alternative and is worse — it would outlaw definitions that are fine
	// whenever the earlier publish has not actually run yet.
	published, err := q.CountSettledPublications(ctx, rc.in.RunID)
	if err != nil {
		return fmt.Errorf("runs: count settled publications: %w", err)
	}
	if published > 0 {
		return xerrors.E(xerrors.ClassConflict,
			"this run has already published; a revision cannot rewind work that is live on the forge — approve or reject")
	}

	spent, err := q.CountRequestChanges(ctx, store.CountRequestChangesParams{
		RunID: rc.in.RunID, NodeID: rc.in.NodeID,
	})
	if err != nil {
		return fmt.Errorf("runs: count revision rounds: %w", err)
	}
	if spent >= int64(node.Revision.MaxRounds) {
		// Refuse the decision rather than killing the run: the human keeps
		// approve and reject, and a bounded loop should not cost the work.
		return xerrors.E(xerrors.ClassConflict, fmt.Sprintf(
			"gate %q has spent all %d revision rounds; approve or reject it",
			rc.in.NodeID, node.Revision.MaxRounds))
	}

	selfApproval := rc.actor.UserID == rc.gate.RequesterID
	decision, err := q.InsertGateDecision(ctx, store.InsertGateDecisionParams{
		PhaseRunID: rc.gate.ID, RunID: rc.in.RunID, NodeID: rc.in.NodeID,
		Generation: rc.gate.Generation, Decision: string(DecisionRequestChanges),
		ExaminedArtifactID: rc.art.ID, Note: rc.in.Note, ActedBy: rc.actor.UserID,
		SelfApproval: selfApproval, ActedAt: rc.now,
	})
	if isUniqueViolation(err) {
		return xerrors.E(xerrors.ClassConflict, "this gate attempt has already been decided")
	}
	if err != nil {
		return fmt.Errorf("runs: insert gate decision: %w", err)
	}
	res.DecisionID = decision.ID

	// The gate's OWN row is decided BEFORE the slice supersede below, because
	// that supersede covers this row too and whichever runs first wins the
	// status. Deciding first records what actually happened — this attempt was
	// decided, and the decision row holds the answer — and the slice pass then
	// finds the row already superseded and skips it. Running it the other way
	// round makes this call a permanent no-op.
	//
	// This is also the statement carrying the wall-deadline predicate, so a
	// run that expired under the decision refuses here rather than opening a
	// generation nothing may execute.
	if err := s.finishGate(ctx, tx, rc.gate.ID, PhaseSuperseded, Outcome{
		Class: OutcomePermanent, Code: CodeSuperseded,
		Message: fmt.Sprintf("changes requested at gate %q", rc.in.NodeID),
	}, rc.now); err != nil {
		return err
	}

	generation, err := q.BumpRunGeneration(ctx, rc.in.RunID)
	if errorsIsNoRows(err) {
		return xerrors.E(xerrors.ClassConflict,
			"the run's wall deadline has passed; it can no longer be decided")
	}
	if err != nil {
		return fmt.Errorf("runs: bump generation: %w", err)
	}
	res.Generation = generation
	res.RunStatus = RunRunning

	invalidated, err := q.InvalidateAuthorizations(ctx, store.InvalidateAuthorizationsParams{
		RunID: rc.in.RunID, SettledAt: &rc.now,
	})
	if err != nil {
		return fmt.Errorf("runs: invalidate authorizations: %w", err)
	}

	superOut, err := json.Marshal(Outcome{
		Class: OutcomePermanent, Code: CodeSuperseded,
		Message: fmt.Sprintf("superseded by generation %d", generation),
	})
	if err != nil {
		return err
	}
	// This retires EVERY row in the slice, succeeded ones included. That is
	// the whole mechanism: readiness is each node's newest attempt row, so a
	// slice node whose generation-1 row succeeded would otherwise stay
	// readable as done and never re-run — the target would regenerate its
	// patch and the gate would re-decide against the OLD review verdict,
	// which is precisely the bug a revision exists to prevent.
	if _, err := q.SupersedePhasesInSlice(ctx, store.SupersedePhasesInSliceParams{
		RunID: rc.in.RunID, NodeIds: slice, Outcome: superOut, FinishedAt: &rc.now,
	}); err != nil {
		return fmt.Errorf("runs: supersede revision slice: %w", err)
	}

	// Seed the target BEFORE its phase row exists, so the row is never
	// claimable without its inputs in place.
	if err := s.seedRevisionTarget(ctx, q, rc, node.Revision.Target, generation); err != nil {
		return err
	}

	// Only the target is materialized here. Everything else in the slice
	// becomes ready as the target's output lands, through the one readiness
	// path — the janitor's materializeReady.
	nextAttempt, err := q.MaxAttemptForNode(ctx, store.MaxAttemptForNodeParams{
		RunID: rc.in.RunID, NodeID: node.Revision.Target,
	})
	if err != nil {
		return err
	}
	snap, err := snapshotFromRow(rc.gate.PolicySnapshot)
	if err != nil {
		return err
	}
	if _, err := s.insertPhaseTx(ctx, tx, rc.in.RunID, node.Revision.Target,
		nextAttempt+1, snap.MaxAttempts, 0, generation, nil); err != nil {
		return err
	}

	if _, err := q.CASRunStatus(ctx, store.CASRunStatusParams{
		ID: rc.in.RunID, FromStatus: string(RunWaiting), ToStatus: string(RunRunning),
	}); err != nil {
		return err
	}

	invalidatedIDs := make([]string, 0, len(invalidated))
	for _, a := range invalidated {
		invalidatedIDs = append(invalidatedIDs, a.ID.String())
	}
	evs := []Event{
		{
			RunID: rc.in.RunID, ProjectID: rc.projectID, Kind: EventGateDecided,
			CorrelationID: rc.correlationID,
			Payload: map[string]any{
				"node_id": rc.in.NodeID, "attempt": rc.gate.Attempt,
				"generation": rc.gate.Generation, "decision": string(DecisionRequestChanges),
				"examined_artifact_id": rc.art.ID.String(), "acted_by": rc.actor.UserID.String(),
				"self_approval": selfApproval,
			},
		},
		{
			RunID: rc.in.RunID, ProjectID: rc.projectID, Kind: EventRunRevisionOpened,
			CorrelationID: rc.correlationID,
			Payload: map[string]any{
				"generation": generation, "gate_node_id": rc.in.NodeID,
				"target": node.Revision.Target, "slice": slice,
				"round": spent + 1, "max_rounds": node.Revision.MaxRounds,
				"invalidated_authorizations": invalidatedIDs,
			},
		},
	}
	if err := appendEvents(ctx, tx, rc.now, evs); err != nil {
		return err
	}
	return s.audit.Record(ctx, tx, audit.Entry{
		Actor: actorID(rc.actor), Action: "gate.decided", SubjectKind: "gate_decision",
		SubjectID: decision.ID.String(), CorrelationID: rc.correlationID,
		Detail: map[string]any{
			"run_id": rc.in.RunID.String(), "node_id": rc.in.NodeID,
			"decision": string(DecisionRequestChanges), "self_approval": selfApproval,
			"examined_artifact_id": rc.art.ID.String(), "generation": generation,
			"invalidated_authorizations": invalidatedIDs,
		},
	})
}

// seedRevisionTarget writes the two inputs the new generation's target must
// see that its edges cannot give it: the artifact the gate examined (so the
// agent REVISES rather than starting over), and the note (the only new
// information in the loop). Both are written at the target's node id with
// attempt 0, which is unreachable for a produced artifact — the ordinal
// itself says the control plane put them there.
func (s *Service) seedRevisionTarget(ctx context.Context, q *store.Queries, rc revisionContext,
	target string, generation int32,
) error {
	noteID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	notePayload, err := json.Marshal(map[string]any{
		"note":                 rc.in.Note,
		"decision":             string(DecisionRequestChanges),
		"gate_node_id":         rc.in.NodeID,
		"examined_artifact_id": rc.art.ID.String(),
		"acted_by":             rc.actor.UserID.String(),
		"acted_at":             rc.now.UTC().Format(time.RFC3339),
		"generation":           generation,
	})
	if err != nil {
		return err
	}
	parents, err := json.Marshal([]string{rc.art.ID.String()})
	if err != nil {
		return err
	}
	if _, err := q.InsertArtifact(ctx, store.InsertArtifactParams{
		ID: noteID, RunID: rc.in.RunID, NodeID: target, Attempt: seedAttempt,
		Generation: generation, Type: ArtifactKindGateNote, Payload: notePayload,
		ParentIds: parents, CreatedAt: rc.now,
	}); err != nil {
		return fmt.Errorf("runs: seed gate note: %w", err)
	}

	// The examined artifact is carried forward under the TARGET's node id so
	// the seed reader finds it without knowing the graph. It is a copy of the
	// payload with the original as its parent — lineage stays intact, and the
	// original row is untouched because history is append-only.
	carryID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	if _, err := q.InsertArtifact(ctx, store.InsertArtifactParams{
		ID: carryID, RunID: rc.in.RunID, NodeID: target, Attempt: seedAttempt,
		Generation: generation, Type: rc.art.Type, Payload: rc.art.Payload,
		ParentIds: parents, CreatedAt: rc.now,
	}); err != nil {
		return fmt.Errorf("runs: seed examined artifact: %w", err)
	}
	return nil
}
