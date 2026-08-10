package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// RunEvent vocabulary (plan header). Append-only observation, never
// dispatch (spec §1 evolvability rule 2).
const (
	EventRunCreated        = "run.created"
	EventRunStarted        = "run.started"
	EventRunSuperseded     = "run.superseded"
	EventRunSucceeded      = "run.succeeded"
	EventRunFailed         = "run.failed"
	EventRunDead           = "run.dead"
	EventPhaseMaterialized = "phase.materialized"
	EventPhaseStarted      = "phase.started"
	EventPhaseReconciling  = "phase.reconciling"
	EventPhaseRetried      = "phase.retried"
	EventPhaseSucceeded    = "phase.succeeded"
	EventPhaseDead         = "phase.dead"
	EventPhaseLogTruncated = "phase.log_truncated"
	EventPhaseEgressDenied = "phase.egress_denied"

	// Plan 08. Gates and publications get their own namespaces rather than
	// living under phase.*: they are the run's attention items, and Plan 10's
	// global stream filters on the kind prefix to build badges and cards.
	EventGateWaiting          = "gate.waiting"
	EventGateDecided          = "gate.decided"
	EventRunRevisionOpened    = "run.revision_opened"
	EventPublishIntentCreated = "publish.intent_created"
	EventPublishPushed        = "publish.pushed"
	EventPublishPROpened      = "publish.pr_opened"
	EventPublishAdopted       = "publish.adopted"
	EventPublishHalted        = "publish.halted"
)

// Event is one observation appended in the same tx as its state change.
type Event struct {
	RunID         uuid.UUID
	ProjectID     uuid.UUID
	Kind          string
	Payload       map[string]any
	CorrelationID string
}

// appendEvents allocates commit-ordered ids under the counter-row lock
// and writes run_events plus one outbox envelope per event — all inside
// the caller's transaction (spec §2). The lock is held until the caller
// commits: id order IS commit order.
func appendEvents(ctx context.Context, tx pgx.Tx, now time.Time, evs []Event) error {
	if len(evs) == 0 {
		return nil
	}
	q := store.New(tx)
	next, err := q.LockEventCounter(ctx)
	if err != nil {
		return fmt.Errorf("runs: lock event counter: %w", err)
	}
	if err := q.AdvanceEventCounter(ctx, int64(len(evs))); err != nil {
		return fmt.Errorf("runs: advance event counter: %w", err)
	}
	for i, e := range evs {
		payload := e.Payload
		if payload == nil {
			payload = map[string]any{}
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("runs: marshal event payload: %w", err)
		}
		id := next + int64(i)
		if err := q.InsertRunEvent(ctx, store.InsertRunEventParams{
			ID: id, RunID: e.RunID, ProjectID: e.ProjectID, Kind: e.Kind,
			Payload: raw, OccurredAt: now, CorrelationID: e.CorrelationID,
		}); err != nil {
			return fmt.Errorf("runs: insert event: %w", err)
		}
		if err := q.InsertOutbox(ctx, store.InsertOutboxParams{
			EventID: id, Kind: e.Kind, Payload: raw, CreatedAt: now,
		}); err != nil {
			return fmt.Errorf("runs: insert outbox: %w", err)
		}
	}
	return nil
}
