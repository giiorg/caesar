//go:build integration

package audit_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

func TestRecord_PersistsEntryWithClockTimestamp(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	t0 := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	rec := audit.NewRecorder(clock.NewFake(t0))

	err := rec.Record(ctx, pool, audit.Entry{
		Action:        "auth.login",
		SubjectKind:   "user",
		SubjectID:     "someone@example.com",
		CorrelationID: "corr-123",
		Detail:        map[string]any{"ip": "203.0.113.9"},
	})
	if err != nil {
		t.Fatal(err)
	}

	rows, err := audit.New(pool).ListAuditEntriesByAction(ctx, "auth.login")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	got := rows[0]
	if !got.OccurredAt.Equal(t0) {
		t.Fatalf("occurred_at = %v, want fake-clock %v", got.OccurredAt, t0)
	}
	if got.ActorUserID.Valid {
		t.Fatal("actor should be NULL for system entries")
	}
	var detail map[string]any
	if err := json.Unmarshal(got.Detail, &detail); err != nil {
		t.Fatal(err)
	}
	if detail["ip"] != "203.0.113.9" {
		t.Fatalf("detail = %v", detail)
	}
}

func TestRecord_InsideCallerTransaction(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	rec := audit.NewRecorder(clock.NewFake(time.Unix(0, 0).UTC()))

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	actor := uuid.New()
	// FK: actor must exist inside the same tx for the insert to succeed —
	// use a raw insert to avoid depending on identity here.
	_, err = tx.Exec(ctx, `INSERT INTO users (id, email, password_hash, role) VALUES ($1, 'a@b.co', 'x', 'admin')`, actor)
	if err != nil {
		t.Fatal(err)
	}
	err = rec.Record(ctx, tx, audit.Entry{
		Actor:       uuid.NullUUID{UUID: actor, Valid: true},
		Action:      "user.created",
		SubjectKind: "user",
		SubjectID:   actor.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	rows, err := audit.New(pool).ListAuditEntriesByAction(ctx, "user.created")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatal("rolled-back transaction must take its audit entry with it")
	}
}
