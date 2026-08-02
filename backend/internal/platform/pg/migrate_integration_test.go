//go:build integration

package pg_test

import (
	"context"
	"io"
	"io/fs"
	"os"
	"sync"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

func fixtures() fs.FS { return os.DirFS("testdata/migrations_ok") }

// bareURL returns a fresh testdb clone with the production schema stripped.
// These tests exercise the generic migration runner against a synthetic
// fixture set unrelated to the real schema; since Plan 2, testdb clones
// arrive pre-migrated to the production version, and goose's single linear
// version table can't reconcile that history against the fixture set's own
// version numbering. Reset to a blank schema first.
func bareURL(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	url := testdb.NewURL(t)
	pool, err := pg.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatalf("bareURL: reset schema: %v", err)
	}
	return url
}

func tableExists(t *testing.T, url, table string) bool {
	t.Helper()
	ctx := context.Background()
	pool, err := pg.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var exists bool
	err = pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = $1)", table).Scan(&exists)
	if err != nil {
		t.Fatal(err)
	}
	return exists
}

func TestMigrate_AppliesFixturesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	url := bareURL(t)
	pool, err := pg.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	for i := 0; i < 2; i++ { // second run must be a no-op, not an error
		if err := pg.Migrate(ctx, pool, fixtures()); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if !tableExists(t, url, "widgets") {
		t.Fatal("widgets table missing after migrate")
	}
}

func TestMigrate_EmptySetIsANoOp(t *testing.T) {
	ctx := context.Background()
	pool, err := pg.New(ctx, testdb.NewURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// testdb clones already carry the production set; re-running Migrate on
	// an up-to-date database must be a no-op, not an error.
	if err := pg.Migrate(ctx, pool, pg.Migrations()); err != nil {
		t.Fatalf("already-migrated database must be tolerated: %v", err)
	}
}

func TestMigrateDown_RevertsOneStep(t *testing.T) {
	ctx := context.Background()
	url := bareURL(t)
	pool, err := pg.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pg.Migrate(ctx, pool, fixtures()); err != nil {
		t.Fatal(err)
	}
	if err := pg.MigrateDown(ctx, pool, fixtures()); err != nil {
		t.Fatal(err)
	}
	if tableExists(t, url, "widgets") {
		t.Fatal("widgets table still present after down")
	}
}

func TestMigrate_ConcurrentRunnersSerializeOnAdvisoryLock(t *testing.T) {
	ctx := context.Background()
	url := bareURL(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool, err := pg.New(ctx, url)
			if err != nil {
				errs <- err
				return
			}
			defer pool.Close()
			errs <- pg.Migrate(ctx, pool, fixtures())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent migrate failed: %v", err)
		}
	}
}

func TestMigrateStatus_ListsFixtureMigration(t *testing.T) {
	ctx := context.Background()
	pool, err := pg.New(ctx, testdb.NewURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pg.MigrateStatus(ctx, pool, fixtures(), io.Discard); err != nil {
		t.Fatal(err)
	}
}
