package runs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// claimed is one leased attempt with the run columns every downstream
// decision needs (deadline, budget) — read in the claim statement itself
// so no second query can race the predicates.
type claimed struct {
	PhaseID uuid.UUID
	Ref     AttemptRef
	// ChargeableFailures is the count this attempt inherited — what the
	// claim predicate bounds by MaxAttempts, since waits requeue free.
	ChargeableFailures int32
	MaxAttempts        int32
	RunWallDeadline    time.Time
	BudgetMS           int64
	UsedMS             int64
	ClaimedAt          time.Time
}

// workerText is the worker identity as the store sees it. The escape
// hatch in FinishPhase compares text, so the empty system actor must
// arrive as a valid empty string: a NULL parameter would make every
// comparison NULL and the CAS would never match.
func workerText(workerID string) pgtype.Text {
	return pgtype.Text{String: workerID, Valid: true}
}

// claimNext is the single-statement claim (spec §3): a locking CTE
// (pending row, run running, wall deadline unexpired, budget unexhausted,
// chargeable failures within bounds — defense-in-depth — and any parked
// not_before elapsed) feeding the CAS UPDATE that takes the lease. SKIP
// LOCKED makes workers non-blocking. An empty claim means nothing was
// claimable *at this instant* — never that the queue is drained, since
// SKIP LOCKED hides rows other workers hold and a parked retry is
// invisible until its not_before passes on the database clock.
func (s *Service) claimNext(ctx context.Context, workerID string, leaseSeconds float64) (claimed, bool, error) {
	now := s.clock.Now()
	row, err := store.New(s.pool).ClaimPhase(ctx, store.ClaimPhaseParams{
		WorkerID: workerText(workerID), LeaseSeconds: leaseSeconds, ClaimedAt: &now,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return claimed{}, false, nil
	}
	if err != nil {
		return claimed{}, false, fmt.Errorf("runs: claim: %w", err)
	}
	return claimed{
		PhaseID:            row.ID,
		Ref:                AttemptRef{RunID: row.RunID, NodeID: row.NodeID, Attempt: row.Attempt},
		ChargeableFailures: row.ChargeableFailures,
		MaxAttempts:        row.MaxAttempts,
		RunWallDeadline:    row.RunWallDeadline,
		BudgetMS:           row.ExecutionBudgetMs,
		UsedMS:             row.ExecutionUsedMs,
		ClaimedAt:          now,
	}, true, nil
}

// heartbeat extends the lease; false means fenced — the row is no
// longer running as this worker and the caller must abandon the attempt.
func (s *Service) heartbeat(ctx context.Context, phaseID uuid.UUID, workerID string, leaseSeconds float64) (bool, error) {
	n, err := store.New(s.pool).HeartbeatPhase(ctx, store.HeartbeatPhaseParams{
		ID: phaseID, WorkerID: workerText(workerID), LeaseSeconds: leaseSeconds,
	})
	if err != nil {
		return false, fmt.Errorf("runs: heartbeat: %w", err)
	}
	return n > 0, nil
}

// insertPhaseTx inserts one pending attempt row inside the caller's tx.
// chargeableFailures carries the node's spent failure budget forward;
// notBefore parks the row until the database clock passes it (nil = now).
func (s *Service) insertPhaseTx(ctx context.Context, tx pgx.Tx, runID uuid.UUID, nodeID string,
	attempt, maxAttempts, chargeableFailures int32, notBefore *time.Time,
) (uuid.UUID, error) {
	row, err := store.New(tx).InsertPhase(ctx, store.InsertPhaseParams{
		RunID: runID, NodeID: nodeID, Attempt: attempt, MaxAttempts: maxAttempts,
		ChargeableFailures: chargeableFailures, NotBefore: notBefore, CreatedAt: s.clock.Now(),
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("runs: insert phase %s/%s#%d: %w", runID, nodeID, attempt, err)
	}
	return row.ID, nil
}

// finishPhase is the fencing CAS shared by workers and sweepers:
// from-status plus worker identity (empty worker = system actor). It
// names one row id, so a stale worker can never reach the successor
// attempt's row.
func (s *Service) finishPhase(ctx context.Context, tx pgx.Tx, phaseID uuid.UUID,
	from, to PhaseStatus, workerID string, outcome []byte,
) (int64, error) {
	if !from.CanTransition(to) {
		return 0, fmt.Errorf("runs: illegal phase transition %s -> %s", from, to)
	}
	now := s.clock.Now()
	n, err := store.New(tx).FinishPhase(ctx, store.FinishPhaseParams{
		ID: phaseID, FromStatus: string(from), ToStatus: string(to),
		WorkerID: workerText(workerID), Outcome: outcome, FinishedAt: &now,
	})
	if err != nil {
		return 0, fmt.Errorf("runs: finish phase: %w", err)
	}
	return n, nil
}
