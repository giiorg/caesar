//go:build integration

package assets_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

const testKeyset = "test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func newTestService(t *testing.T) (*assets.Service, *pgxpool.Pool, *clock.Fake) {
	t.Helper()
	pool := testdb.New(t)
	fake := clock.NewFake(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	keys, err := secrets.ParseKeyset(testKeyset)
	if err != nil {
		t.Fatal(err)
	}
	return assets.NewService(pool, fake, audit.NewRecorder(fake), keys), pool, fake
}

// seedActor inserts a user row directly (assets never imports identity)
// and returns a principal for it.
func seedActor(t *testing.T, pool *pgxpool.Pool, email string, role authn.Role) authn.Principal {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		"INSERT INTO users (email, password_hash, role) VALUES ($1, 'x', $2) RETURNING id",
		email, string(role)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return authn.Principal{UserID: id, Email: email, Role: role}
}

// fieldMessage pulls one field error's message off a classified error so
// tests can assert what an operator is actually told, not just the class.
func fieldMessage(t *testing.T, err error, field string) string {
	t.Helper()
	var e *xerrors.Error
	if !errors.As(err, &e) {
		t.Fatalf("not a classified error: %v", err)
	}
	for _, f := range e.Fields {
		if f.Field == field {
			return f.Message
		}
	}
	t.Fatalf("no %q field error in %v", field, e.Fields)
	return ""
}

func auditCount(t *testing.T, pool *pgxpool.Pool, action string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM audit_entries WHERE action = $1", action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
