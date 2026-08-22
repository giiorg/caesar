package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// Decision is a gate's typed outcome (spec §2): approve continues the
// forward DAG, request_changes opens a bounded revision generation, reject
// ends the run with a typed terminal outcome.
type Decision string

const (
	DecisionApprove        Decision = "approve"
	DecisionRequestChanges Decision = "request_changes"
	DecisionReject         Decision = "reject"
)

func (d Decision) Valid() bool {
	return d == DecisionApprove || d == DecisionRequestChanges || d == DecisionReject
}

// maxGateNoteLen bounds the note (bytes, house convention). The note is a
// first-class input: request_changes feeds it to the revision target as a
// context file, so it is sized like an instruction, not like a comment.
const maxGateNoteLen = 8 << 10

type GateDecisionInput struct {
	RunID    uuid.UUID
	NodeID   string
	Decision Decision
	Note     string
}

type GateDecisionResult struct {
	DecisionID uuid.UUID
	RunStatus  RunStatus
	// Generation means two different things by design. Approve and reject
	// report the generation DECIDED — the one the gate row carried, which is
	// what the caller just acted on. request_changes reports the NEW
	// generation the revision opened, because the decided one is already
	// history by the time the transaction commits and the useful answer is
	// "which round are we on now". Task 11's API contract has to pick a name
	// per path rather than assume one meaning.
	Generation int32
	// AuthorizationID is set only when the approval authorizes a publish.
	AuthorizationID *uuid.UUID
}

// GateDecision is a settled verdict as the run projection carries it. The
// approver appears twice on purpose: a run page renders an address, an audit
// trail needs the id, and naming them separately makes the disclosure of the
// address to every view-only member of the project a decision rather than an
// accident of which column the query happened to select.
type GateDecision struct {
	Decision           string
	Note               string
	ActedByID          uuid.UUID
	ActedByEmail       string
	ActedAt            time.Time
	SelfApproval       bool
	ExaminedArtifactID uuid.UUID
}

func gateDecisionFromRow(r store.ListGateDecisionsForRunRow) GateDecision {
	return GateDecision{
		Decision: r.Decision, Note: r.Note, ActedByID: r.ActedBy,
		ActedByEmail: r.ActedByEmail, ActedAt: r.ActedAt,
		SelfApproval: r.SelfApproval, ExaminedArtifactID: r.ExaminedArtifactID,
	}
}

func validateDecision(in GateDecisionInput) error {
	e := xerrors.E(xerrors.ClassInvalid, "validation failed")
	ok := true
	if !in.Decision.Valid() {
		e = e.WithFields(xerrors.FieldError{Field: "decision", Message: "must be approve, request_changes or reject"})
		ok = false
	}
	if len(in.Note) > maxGateNoteLen {
		e = e.WithFields(xerrors.FieldError{Field: "note", Message: fmt.Sprintf("must be at most %d bytes", maxGateNoteLen)})
		ok = false
	}
	if in.Decision == DecisionRequestChanges && in.Note == "" {
		// A revision with no note re-runs the same work with the same
		// information and reaches the same patch. The note IS the new input.
		e = e.WithFields(xerrors.FieldError{Field: "note", Message: "request_changes requires a note; it is the revision's only new input"})
		ok = false
	}
	if !ok {
		return e
	}
	return nil
}

// GateProject resolves a run to its project for the API's pre-write
// authorization check (approve grant on the run's project, spec §6).
func (s *Service) GateProject(ctx context.Context, runID uuid.UUID) (uuid.UUID, error) {
	row, err := store.New(s.pool).GetRunProjectID(ctx, runID)
	if err != nil {
		return uuid.Nil, notFound(err, "no such run")
	}
	return row, nil
}

// DecideGate is THE gate transaction (spec §4: deciding mutations are CAS
// end-to-end). Under one row lock it validates the decision against the
// live gate, appends the decision row, transitions the gate and the run,
// mints or withholds the ApprovedPatch, audits, and emits its events.
//
// The plan is compiled outside the transaction for the same reason Rerun
// resolves its pins outside one: the definition source draws on this pool,
// and holding the gate's lock across that query could self-deadlock a
// saturated pool. The pinned version is immutable, so reading it up front is
// safe.
func (s *Service) DecideGate(ctx context.Context, actor authn.Principal, in GateDecisionInput) (GateDecisionResult, error) {
	if err := validateDecision(in); err != nil {
		return GateDecisionResult{}, err
	}
	q := store.New(s.pool)
	runRow, err := q.GetRun(ctx, in.RunID)
	if err != nil {
		return GateDecisionResult{}, notFound(err, "no such run")
	}
	taskRow, err := q.GetTask(ctx, runRow.TaskID)
	if err != nil {
		return GateDecisionResult{}, err
	}
	def, err := s.defs.DefinitionByVersionID(ctx, taskRow.WorkflowVersionID)
	if err != nil {
		return GateDecisionResult{}, err
	}
	plan, err := compilePinned(def)
	if err != nil {
		return GateDecisionResult{}, err
	}
	repo, err := s.repos.Repo(ctx, taskRow.RepoID)
	if err != nil {
		return GateDecisionResult{}, err
	}

	correlationID := httpx.CorrelationID(ctx)
	var res GateDecisionResult
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		gate, err := q.GetLiveGateForUpdate(ctx, store.GetLiveGateForUpdateParams{
			RunID: in.RunID, NodeID: in.NodeID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// Either the node is not a gate of this run, or its attempt has
			// already been decided, superseded or swept. All of them are the
			// same answer to the caller: this decision is not available.
			return xerrors.E(xerrors.ClassConflict, "no gate is awaiting a decision on this node")
		}
		if err != nil {
			return fmt.Errorf("runs: lock gate: %w", err)
		}
		dbNow, err := q.DBNow(ctx)
		if err != nil {
			return err
		}
		if !gate.RunWallDeadline.After(dbNow) {
			// The database clock decides, so the sweeper and this transaction
			// cannot disagree about which side of the deadline they are on.
			// FinishGatePhase carries the same predicate; refusing here as
			// well is what makes the refusal legible instead of a lost CAS.
			return xerrors.E(xerrors.ClassConflict, "the run's wall deadline has passed; it can no longer be decided")
		}

		snap, err := snapshotFromRow(gate.PolicySnapshot)
		if err != nil {
			return err
		}
		selfApproval := actor.UserID == gate.RequesterID
		if selfApproval && !snap.AllowSelfApproval {
			return xerrors.E(xerrors.ClassForbidden, "this run's policy does not permit the requester to decide their own gate")
		}

		examined, ok := plan.ExaminedInput(in.NodeID)
		if !ok {
			return fmt.Errorf("runs: gate %q examines nothing; stored definition %s is corrupt", in.NodeID, def.VersionID)
		}
		art, err := q.GetNodeArtifact(ctx, store.GetNodeArtifactParams{
			RunID: in.RunID, NodeID: examined.Origin, Type: examined.Artifact,
		})
		if err != nil {
			return fmt.Errorf("runs: examined artifact %s/%s: %w", examined.Origin, examined.Artifact, err)
		}

		if in.Decision == DecisionRequestChanges {
			return s.openRevision(ctx, tx, revisionContext{
				plan: plan, gate: gate, art: art, in: in, actor: actor,
				projectID: taskRow.ProjectID, correlationID: correlationID, now: dbNow,
			}, &res)
		}

		decision, err := q.InsertGateDecision(ctx, store.InsertGateDecisionParams{
			PhaseRunID: gate.ID, RunID: in.RunID, NodeID: in.NodeID, Generation: gate.Generation,
			Decision: string(in.Decision), ExaminedArtifactID: art.ID, Note: in.Note,
			ActedBy: actor.UserID, SelfApproval: selfApproval, ActedAt: dbNow,
		})
		if isUniqueViolation(err) {
			// Another decider winning under the same lock is impossible; this
			// is the row having been decided in a prior, committed transaction
			// whose gate row we somehow still read as waiting. Report it as
			// what it is rather than as a 500.
			return xerrors.E(xerrors.ClassConflict, "this gate attempt has already been decided")
		}
		if err != nil {
			return fmt.Errorf("runs: insert gate decision: %w", err)
		}
		res.DecisionID = decision.ID
		res.Generation = gate.Generation

		evs := []Event{{
			RunID: in.RunID, ProjectID: taskRow.ProjectID, Kind: EventGateDecided,
			CorrelationID: correlationID,
			Payload: map[string]any{
				"node_id": in.NodeID, "attempt": gate.Attempt, "generation": gate.Generation,
				"decision": string(in.Decision), "examined_artifact_id": art.ID.String(),
				"acted_by": actor.UserID.String(), "self_approval": selfApproval,
			},
		}}

		switch in.Decision {
		case DecisionReject:
			rejected := Outcome{
				Class: OutcomePermanent, Code: CodeGateRejected,
				Message: fmt.Sprintf("gate %q was rejected", in.NodeID),
			}
			if err := s.finishGate(ctx, tx, in.RunID, gate.ID, PhaseFailed, rejected, dbNow); err != nil {
				return err
			}
			res.RunStatus = RunFailed
			if err := s.finishRunFromGate(ctx, tx, taskRow.ProjectID, in.RunID, RunFailed,
				rejected, EventRunFailed, dbNow, evs); err != nil {
				return err
			}
		case DecisionApprove:
			if err := s.finishGate(ctx, tx, in.RunID, gate.ID, PhaseSucceeded, Outcome{}, dbNow); err != nil {
				return err
			}
			if publishNode, ok := plan.PublishFor(in.NodeID); ok {
				auth, err := s.mintAuthorization(ctx, q, mintInput{
					runID: in.RunID, gateNode: in.NodeID, publishNode: publishNode,
					decisionID: decision.ID, art: art, baseSHA: runRow.BaseSha,
					repo: repo, generation: gate.Generation, now: dbNow,
				})
				if err != nil {
					return err
				}
				res.AuthorizationID = &auth.ID
				// ONE decision, ONE event. The authorization is an ATTRIBUTE
				// of this approval, so it rides the approval's own event
				// rather than a second gate.decided of its own.
				//
				// A second emission was the shape here, and it carried
				// authorization_id/authorizes and no `decision` field at all —
				// so the kind meant two different things and any consumer
				// counting gate.decided double-counted exactly the approvals
				// that authorize a publish and no others. The audit's one row
				// per decision was already the honest count. No new kind was
				// invented for the same reason: "this approval released
				// something" is a rendering question, and Plan 10 can ask it
				// of these fields.
				//
				// artifact_id is not repeated: examined_artifact_id below is
				// the same value, set from the same art.ID.
				evs[0].Payload["authorization_id"] = auth.ID.String()
				evs[0].Payload["authorizes"] = publishNode
			}
			// The run resumes; the janitor's next tick materializes what the
			// gate unblocked. Resuming here rather than materializing inline
			// keeps ALL successor materialization in one place
			// (materializeReady), which is the only reason readiness has a
			// single definition.
			if _, err := q.CASRunStatus(ctx, store.CASRunStatusParams{
				ID: in.RunID, FromStatus: string(RunWaiting), ToStatus: string(RunRunning),
			}); err != nil {
				return err
			}
			res.RunStatus = RunRunning
			if err := appendEvents(ctx, tx, dbNow, evs); err != nil {
				return err
			}
		}

		return s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "gate.decided", SubjectKind: "gate_decision",
			SubjectID: decision.ID.String(), CorrelationID: correlationID,
			Detail: map[string]any{
				"run_id": in.RunID.String(), "node_id": in.NodeID,
				"decision": string(in.Decision), "self_approval": selfApproval,
				"examined_artifact_id": art.ID.String(),
			},
		})
	})
	if err != nil {
		return GateDecisionResult{}, err
	}
	return res, nil
}

// finishGate is the gate's fencing CAS. There is no worker identity to
// match — a gate has no worker — so winning means being the transaction
// that found the row still waiting.
func (s *Service) finishGate(ctx context.Context, tx pgx.Tx, runID, phaseID uuid.UUID, to PhaseStatus, out Outcome, now time.Time) error {
	if !PhaseWaitingDecision.CanTransition(to) {
		return fmt.Errorf("runs: illegal gate transition waiting_decision -> %s", to)
	}
	var raw []byte
	if out.Code != "" {
		var err error
		if raw, err = json.Marshal(out); err != nil {
			return err
		}
	}
	q := store.New(tx)
	n, err := q.FinishGatePhase(ctx, store.FinishGatePhaseParams{
		ID: phaseID, ToStatus: string(to), Outcome: raw, FinishedAt: &now,
	})
	if err != nil {
		return fmt.Errorf("runs: finish gate: %w", err)
	}
	if n == 0 {
		return decisionRefused(ctx, q, runID, now, "the gate stopped waiting while this decision was being made")
	}
	return nil
}

// decisionRefused explains a lost gate CAS from what is ACTUALLY false rather
// than from which statement noticed.
//
// Every deciding statement carries the same pair of run predicates — the run is
// 'waiting', and its wall deadline is still ahead — and an empty result says
// only that one of them stopped holding, never which. Parallel and chained
// gates are legal, so the ordinary way to lose is a concurrent decision on a
// SIBLING gate, whose last act moves the run from 'waiting' to 'running'.
// Reporting that as an expired deadline sends a human to look at a clock that
// has not moved; reporting it as the gate having stopped waiting is equally
// false, because the gate row is still waiting_decision and still decidable
// once the run is parked again.
//
// The fallback covers the case where both run predicates still hold, which
// leaves only the phase row itself as the thing that moved.
//
// This is the BEST AVAILABLE DIAGNOSIS, not a replay of the predicate that
// failed. The re-read is a new statement, so under READ COMMITTED it takes a
// new snapshot: between the lost CAS and this read the janitor's
// RunIsWaitingOnAGate pass can park the run back to 'waiting', restoring both
// predicates and dropping this into the fallback. That is a rarer version of
// the very lie it exists to stop, and it is the honest ceiling of diagnosing a
// CAS after the fact — the alternative is a lock this transaction cannot take
// without deadlocking against the sibling it is racing.
//
// now is the caller's dbNow rather than a fresh read. Postgres now() is
// transaction_timestamp(), frozen for the whole transaction, and both call
// sites run inside the decision's transaction — so a fresh DBNow here would
// return this exact value at the cost of a round trip.
func decisionRefused(ctx context.Context, q *store.Queries, runID uuid.UUID, now time.Time, fallback string) error {
	run, err := q.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("runs: read run after a refused decision: %w", err)
	}
	switch {
	case !run.WallDeadline.After(now):
		// UNREACHABLE TODAY, kept for the day it stops being. wall_deadline is
		// written once at insert and never updated, and the frozen now() above
		// is the same instant DecideGate's up-front check used — so that check
		// and this arm compare the same two immutable values and cannot
		// disagree. A lost CAS therefore never lands here. The day a deadline
		// becomes extendable mid-run, this is the arm that stops the refusal
		// from being reported as the gate having moved.
		return xerrors.E(xerrors.ClassConflict,
			"the run's wall deadline passed while this decision was being made")
	case RunStatus(run.Status) != RunWaiting:
		return xerrors.E(xerrors.ClassConflict, fmt.Sprintf(
			"the run is no longer waiting on a decision (it is %s); another decision on this run resumed it",
			run.Status))
	default:
		return xerrors.E(xerrors.ClassConflict, fallback)
	}
}

type mintInput struct {
	runID                 uuid.UUID
	gateNode, publishNode string
	decisionID            uuid.UUID
	art                   store.Artifact
	baseSHA               string
	repo                  RepoRef
	generation            int32
	now                   time.Time
}

// mintAuthorization writes the single-use ApprovedPatch (spec §2). Every
// field it records is re-validated at consumption, so recording the WRONG
// value here is caught there rather than published: the tree hash comes off
// the examined artifact's own payload, and a patch artifact that carries no
// tree hash is corruption, not a publishable state.
func (s *Service) mintAuthorization(ctx context.Context, q *store.Queries, in mintInput) (store.ApprovedPatch, error) {
	payload, err := unmarshalMap(in.art.Payload)
	if err != nil {
		return store.ApprovedPatch{}, err
	}
	treeHash, _ := payload["tree_hash"].(string)
	if treeHash == "" {
		return store.ApprovedPatch{}, xerrors.E(xerrors.ClassConflict,
			"the examined patch carries no tree hash, so an approval over it could not be re-verified at publish time")
	}
	artBase, _ := payload["base_sha"].(string)
	if artBase != "" && artBase != in.baseSHA {
		// The artifact says it diffs from a different commit than the run
		// pinned. Publishing it would apply a patch to the wrong tree.
		return store.ApprovedPatch{}, xerrors.E(xerrors.ClassConflict,
			fmt.Sprintf("the examined patch diffs from %s but the run pinned %s", artBase, in.baseSHA))
	}
	row, err := q.InsertApprovedPatch(ctx, store.InsertApprovedPatchParams{
		RunID: in.runID, GateNodeID: in.gateNode, PublishNodeID: in.publishNode,
		GateDecisionID: in.decisionID, ArtifactID: in.art.ID, BaseSha: in.baseSHA,
		TreeHash: treeHash, RepoID: in.repo.ID,
		ForgeKind: in.repo.Forge, RepoSlug: in.repo.Slug,
		TargetBranch: in.repo.DefaultBranch,
		Generation:   in.generation, CreatedAt: in.now,
	})
	if isUniqueViolation(err) {
		return store.ApprovedPatch{}, xerrors.E(xerrors.ClassConflict,
			"this publish node already holds a live authorization")
	}
	if err != nil {
		return store.ApprovedPatch{}, fmt.Errorf("runs: mint authorization: %w", err)
	}
	return row, nil
}

// finishRunFromGate terminalizes a run whose gate ended it. The decision path
// runs in the API's goroutine, not the janitor's, so it has no Orchestrator
// receiver and no plan cache — but it must not have its own terminalization
// either. It forwards to the free function extracted from orchestrator.go.
func (s *Service) finishRunFromGate(ctx context.Context, tx pgx.Tx, projectID, runID uuid.UUID,
	to RunStatus, out Outcome, eventKind string, now time.Time, evs []Event,
) error {
	return finishRunTx(ctx, tx, s.audit, projectID, runID, to, out, eventKind, now, evs)
}

// isUniqueViolation mirrors the assets context's helper; contexts never
// share hand-rolled helpers across the boundary (six lines beats a seam).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

type revisionContext struct {
	plan          ExecPlan
	gate          store.GetLiveGateForUpdateRow
	art           store.Artifact
	in            GateDecisionInput
	actor         authn.Principal
	projectID     uuid.UUID
	correlationID string
	now           time.Time
}

// WaitingGate is one attention item: a gate somebody has to decide.
type WaitingGate struct {
	PhaseID      uuid.UUID
	RunID        uuid.UUID
	TaskID       uuid.UUID
	ProjectID    uuid.UUID
	NodeID       string
	TaskTitle    string
	Attempt      int32
	Generation   int32
	RequesterID  uuid.UUID
	WaitingSince time.Time
	// Evidence is what the row's decision surface needs and cannot find for
	// itself. Every row of this list is a gate awaiting a decision, so it is
	// never absent (spec §2.4).
	Evidence GateEvidence
}

func waitingGateFrom(r store.ListWaitingGatesRow, ev GateEvidence) WaitingGate {
	return WaitingGate{
		PhaseID: r.ID, RunID: r.RunID, TaskID: r.TaskID, ProjectID: r.ProjectID,
		NodeID: r.NodeID, TaskTitle: r.Title, Attempt: r.Attempt, Generation: r.Generation,
		RequesterID: r.RequesterID, WaitingSince: r.CreatedAt, Evidence: ev,
	}
}

// ListWaitingGates is the approvals inbox. all=true is the admin view; a
// member passes the project ids their grants cover, and an empty list is an
// empty inbox rather than everything.
//
// It resolves each gate's EVIDENCE (spec §2.4), which costs one compiled plan
// per distinct workflow version on the page — memoized, because an inbox is
// usually several gates over one or two workflows — plus one or two artifact
// reads per gate. The alternative is the client fetching each run, which is the
// N+1 this endpoint exists to prevent.
func (s *Service) ListWaitingGates(ctx context.Context, projectIDs []uuid.UUID, all bool) ([]WaitingGate, error) {
	q := store.New(s.pool)
	// The two queries differ only in their WHERE clause, so sqlc's two row
	// structs are field-for-field identical and one converts to the other. The
	// conversion is deliberate rather than convenient: it makes "these two
	// queries select the same columns" a COMPILE-TIME fact, and everything below
	// — a compiled plan, two artifact reads, a projection — then happens in one
	// loop that the admin and member paths cannot drift apart in.
	var rows []store.ListWaitingGatesRow
	switch {
	case all:
		found, err := q.ListWaitingGates(ctx)
		if err != nil {
			return nil, fmt.Errorf("runs: list waiting gates: %w", err)
		}
		rows = found
	case len(projectIDs) == 0:
		return []WaitingGate{}, nil
	default:
		found, err := q.ListWaitingGatesByProjects(ctx, projectIDs)
		if err != nil {
			return nil, fmt.Errorf("runs: list waiting gates: %w", err)
		}
		rows = make([]store.ListWaitingGatesRow, 0, len(found))
		for _, r := range found {
			rows = append(rows, store.ListWaitingGatesRow(r))
		}
	}

	plans := map[uuid.UUID]ExecPlan{}
	compiled := func(versionID uuid.UUID) (ExecPlan, error) {
		if p, ok := plans[versionID]; ok {
			return p, nil
		}
		def, err := s.defs.DefinitionByVersionID(ctx, versionID)
		if err != nil {
			return ExecPlan{}, err
		}
		p, err := compilePinned(def)
		if err != nil {
			return ExecPlan{}, err
		}
		plans[versionID] = p
		return p, nil
	}

	out := make([]WaitingGate, 0, len(rows))
	for _, r := range rows {
		snap, err := snapshotFromRow(r.PolicySnapshot)
		if err != nil {
			return nil, err
		}
		plan, err := compiled(r.WorkflowVersionID)
		if err != nil {
			return nil, err
		}
		ev, err := gateEvidence(ctx, q, gateEvidenceInput{
			plan: plan, runID: r.RunID, nodeID: r.NodeID, snapshot: snap,
			requesterID: r.RequesterID, wallDeadline: r.WallDeadline,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, waitingGateFrom(r, ev))
	}
	return out, nil
}
