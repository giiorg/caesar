//go:build integration

package pg_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

// The contracts lane requirement (spec §7): every migration must go down
// and come back up. testdb clones are already migrated; down-then-up
// exercises the Down section against real objects.
func TestMigrations_DownUpRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.NewURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if err := pg.MigrateDown(ctx, pool, pg.Migrations()); err != nil {
		t.Fatalf("down: %v", err)
	}
	var exists bool
	err = pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'users')`).Scan(&exists)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("users table still present after down migration")
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
}
