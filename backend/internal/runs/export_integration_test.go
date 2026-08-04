//go:build integration

package runs

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/platform/pg"
)

// AppendEventsForTest exposes the tx-scoped appender to integration
// tests; production callers stay inside the service's transactions.
func AppendEventsForTest(ctx context.Context, tx pgx.Tx, now time.Time, evs []Event) error {
	return appendEvents(ctx, tx, now, evs)
}

// Claimed is the queue's leased-attempt record under a name the external
// test package can spell.
type Claimed = claimed

func ClaimNextForTest(ctx context.Context, s *Service, workerID string, leaseSeconds float64) (Claimed, bool, error) {
	return s.claimNext(ctx, workerID, leaseSeconds)
}

func HeartbeatForTest(ctx context.Context, s *Service, phaseID uuid.UUID, workerID string, leaseSeconds float64) (bool, error) {
	return s.heartbeat(ctx, phaseID, workerID, leaseSeconds)
}

func InsertPhaseForTest(ctx context.Context, s *Service, runID uuid.UUID, nodeID string, attempt, maxAttempts int32) (uuid.UUID, error) {
	var id uuid.UUID
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		id, err = s.insertPhaseTx(ctx, tx, runID, nodeID, attempt, maxAttempts)
		return err
	})
	return id, err
}

func FinishPhaseForTest(ctx context.Context, s *Service, phaseID uuid.UUID, from, to, workerID string) (int64, error) {
	var n int64
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		n, err = s.finishPhase(ctx, tx, phaseID, PhaseStatus(from), PhaseStatus(to), workerID, nil)
		return err
	})
	return n, err
}
