//go:build integration

package pg_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

func TestWithTx_CommitsWhenFnSucceeds(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)

	err := pg.WithTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO projects (name) VALUES ('committed')")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM projects WHERE name = 'committed'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestWithTx_RollsBackWhenFnErrors(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	boom := errors.New("boom")

	err := pg.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO projects (name) VALUES ('doomed')"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the fn error verbatim", err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM projects WHERE name = 'doomed'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rows = %d, want 0 (rolled back)", n)
	}
}
