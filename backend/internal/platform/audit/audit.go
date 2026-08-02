// Package audit is the append-only who-did-what recorder (spec §2). It is
// shared-kernel machinery: bounded contexts call Record inside their own
// transactions (DBTX accepts pgx.Tx and *pgxpool.Pool), so an audit write
// never spans contexts. Rows are never updated or deleted.
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/clock"
)

// Entry is one audit fact. Actor is NULL for system actions (bootstrap).
type Entry struct {
	Actor         uuid.NullUUID
	Action        string
	SubjectKind   string
	SubjectID     string
	CorrelationID string
	Detail        map[string]any // nil marshals to {}
}

type Recorder struct {
	clock clock.Clock
}

func NewRecorder(c clock.Clock) *Recorder { return &Recorder{clock: c} }

// Record appends e, stamping occurred_at from the injected clock.
func (r *Recorder) Record(ctx context.Context, db DBTX, e Entry) error {
	detail := e.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("audit: marshal detail: %w", err)
	}
	err = New(db).InsertAuditEntry(ctx, InsertAuditEntryParams{
		OccurredAt:    r.clock.Now(),
		ActorUserID:   e.Actor,
		Action:        e.Action,
		SubjectKind:   e.SubjectKind,
		SubjectID:     e.SubjectID,
		CorrelationID: e.CorrelationID,
		Detail:        raw,
	})
	if err != nil {
		return fmt.Errorf("audit: insert: %w", err)
	}
	return nil
}
