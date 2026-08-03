//go:build integration

package workflows_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/workflows"
)

func newTestService(t *testing.T) (*workflows.Service, *pgxpool.Pool, *clock.Fake) {
	t.Helper()
	pool := testdb.New(t)
	fake := clock.NewFake(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	return workflows.NewService(pool, fake, audit.NewRecorder(fake)), pool, fake
}

// seedActor inserts a user row directly (workflows never imports
// identity) and returns a principal for it.
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

func auditCount(t *testing.T, pool *pgxpool.Pool, action string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM audit_entries WHERE action = $1", action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// docWith returns a valid single-node document whose instructions make
// its content (and hash) unique.
func docWith(instructions string) []byte {
	return []byte(`{"schema": 1, "nodes": [{"id": "impl", "type": "implement", "harness": "cc", "instructions": "` + instructions + `"}]}`)
}
