package runs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// GateEvidence is what a gate's decision surface needs and cannot find for
// itself. Every field here answers a question the client would otherwise have
// to GUESS at, and every guess available to it is wrong in a way the guess
// cannot detect (spec §2.4).
//
// The two artifact ids come from the SAME compiled plan the decision path
// reads, so the artifact the screen renders and the artifact the decision
// records cannot drift.
type GateEvidence struct {
	// ExaminedArtifactID is the plan-or-patch this gate decides on. A ZERO id
	// means the pinned definition binds an artifact the run never produced —
	// the same state DecideGate refuses. The surface says the evidence is
	// missing; it must not render an empty diff.
	ExaminedArtifactID   uuid.UUID
	ExaminedArtifactType string
	// SummaryArtifactID is nil for two DIFFERENT reasons and the client needs
	// to distinguish neither: the graph has no verdict node (legal — a gate may
	// consume zero review verdicts), or it has one that declared nothing (legal
	// — declaring artifacts is optional). Both mean "no summary", and the
	// surface renders that rather than an empty block pretending to be one.
	SummaryArtifactID *uuid.UUID
	// AllowSelfApproval and RequesterID are the two halves of §5's first
	// refusal. They ride the projection because the API layer holds the grant
	// and this layer holds the run; can_decide is computed where both are.
	AllowSelfApproval bool
	RequesterID       uuid.UUID
	// WallDeadline rides along because §5's third refusal is displayable BEFORE
	// it bites: a run past its deadline can no longer be decided and the
	// deadline is on the run, so a card can say "this expires in 20 minutes"
	// instead of only reporting the 409 afterwards.
	WallDeadline time.Time
}

// gateEvidenceInput is what resolving one gate's evidence needs that is not a
// query. It is a struct rather than six positional parameters because two of
// them are uuids and two more are strings, which is the shape that compiles
// while transposed.
type gateEvidenceInput struct {
	// plan is passed in rather than looked up so a list can compile once per
	// distinct workflow version instead of once per row.
	plan         ExecPlan
	runID        uuid.UUID
	nodeID       string
	snapshot     PolicySnapshot
	requesterID  uuid.UUID
	wallDeadline time.Time
}

// gateEvidence resolves one gate's evidence against an already-compiled plan.
func gateEvidence(ctx context.Context, q *store.Queries, in gateEvidenceInput) (GateEvidence, error) {
	ev := GateEvidence{
		AllowSelfApproval: in.snapshot.AllowSelfApproval,
		RequesterID:       in.requesterID,
		WallDeadline:      in.wallDeadline,
	}

	examined, ok := in.plan.ExaminedInput(in.nodeID)
	if !ok {
		// Corruption of a stored definition, exactly as DecideGate reads it.
		return GateEvidence{}, fmt.Errorf("runs: gate %q examines nothing", in.nodeID)
	}
	ev.ExaminedArtifactType = examined.Artifact
	art, err := q.GetNodeArtifact(ctx, store.GetNodeArtifactParams{
		RunID: in.runID, NodeID: examined.Origin, Type: examined.Artifact,
	})
	switch {
	case err == nil:
		ev.ExaminedArtifactID = art.ID
	case isNoRows(err):
		// The definition binds it and the run has not got one. Left ZERO on
		// purpose: DecideGate will refuse this gate too, and a projection that
		// substituted some other artifact would show a human one thing and
		// stamp another — the precise failure §2.4 exists to prevent.
	default:
		return GateEvidence{}, fmt.Errorf("runs: examined artifact %s/%s: %w", examined.Origin, examined.Artifact, err)
	}

	if summary, ok := in.plan.SummaryInput(in.nodeID); ok {
		row, err := q.GetNodeArtifact(ctx, store.GetNodeArtifactParams{
			RunID: in.runID, NodeID: summary.Origin, Type: summary.Artifact,
		})
		switch {
		case err == nil:
			// GetNodeArtifact orders by generation then attempt, so a gate on
			// generation 3 gets generation 3's verdict and never generation 1's.
			id := row.ID
			ev.SummaryArtifactID = &id
		case isNoRows(err):
			// The node exists and declared nothing. Not an error.
		default:
			return GateEvidence{}, fmt.Errorf("runs: summary artifact %s/%s: %w", summary.Origin, summary.Artifact, err)
		}
	}
	return ev, nil
}

// isNoRows names the ONE decision both switches above make: on this surface a
// missing artifact row is a state to report, never a failure to raise. Spelling
// it out is what makes the two sites read as the same decision rather than as a
// pair of coincidentally similar errors.Is calls — DecideGate makes the
// opposite call on the very same query, and the contrast is the point.
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
