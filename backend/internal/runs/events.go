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

// EventsChannel is the LISTEN/NOTIFY channel a committed append wakes.
//
// It is exported for ONE reason: the listening half lives in internal/app
// (the realtime tailer's waker) and a disagreement between the two names is
// completely silent. NOTIFY on a channel nobody listens to succeeds, the
// tailer falls back to polling, every test still passes, and the only symptom
// is up to one poll interval of extra latency that no assertion measures.
// One definition, consumed by both sides, is what makes that unrepresentable
// rather than merely unlikely.
//
// A channel name cannot be a query parameter — NOTIFY takes an identifier —
// so the statement below is built by concatenation. That is safe here and
// only here: this is an untyped string constant, so the SQL is fixed at
// compile time and no caller can reach it.
const EventsChannel = "caesar_run_events"

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
	// Wake any tailer. Inside a tx, NOTIFY is delivered on COMMIT and
	// dropped on rollback, so a listener is only ever woken for events
	// that exist. The payload is empty on purpose: LISTEN/NOTIFY is a
	// wake-up signal and never the data (locked decision §2) — the tailer
	// re-reads from its own cursor and a lost notification costs latency,
	// never correctness. The channel name is EventsChannel and never a literal
	// — see there for what a second spelling would cost.
	if _, err := tx.Exec(ctx, "NOTIFY "+EventsChannel); err != nil {
		return fmt.Errorf("runs: notify run events: %w", err)
	}
	return nil
}
