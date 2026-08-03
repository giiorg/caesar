//go:build integration

package pg_test

import (
	"context"
	"io/fs"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

// The contracts lane requirement (spec §7): every migration must go down
// and come back up. testdb clones are already migrated; down-then-up
// exercises every Down section against real objects. MigrateDown reverts
// one step at a time (see TestMigrateDown_RevertsOneStep), so walk the
// whole embedded set down to zero, then back up.
func TestMigrations_DownUpRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.NewURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	names, err := fs.Glob(pg.Migrations(), "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no embedded migrations to exercise")
	}

	for range names {
		if err := pg.MigrateDown(ctx, pool, pg.Migrations()); err != nil {
			t.Fatalf("down: %v", err)
		}
	}
	var exists bool
	err = pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'users')`).Scan(&exists)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("users table still present after walking every migration down")
	}

	if err := pg.Migrate(ctx, pool, pg.Migrations()); err != nil {
		t.Fatalf("up after down: %v", err)
	}
	err = pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'users')`).Scan(&exists)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("users table missing after re-up")
	}

	err = pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'credentials')`).Scan(&exists)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("credentials table missing after re-up")
	}
}
