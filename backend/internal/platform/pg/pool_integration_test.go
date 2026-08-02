//go:build integration

package pg_test

import (
	"context"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

func TestNew_ConnectsAndPings(t *testing.T) {
	pool, err := pg.New(context.Background(), testdb.NewURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var one int
	if err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("SELECT 1 = %d, %v", one, err)
	}
}

func TestNew_FailsFastOnUnreachableServer(t *testing.T) {
	_, err := pg.New(context.Background(), "postgres://nobody:nope@127.0.0.1:1/void?connect_timeout=1")
	if err == nil {
		t.Fatal("want connection error")
	}
}

func TestTestdb_DatabasesAreIsolated(t *testing.T) {
	ctx := context.Background()
	a, b := testdb.New(t), testdb.New(t)
	if _, err := a.Exec(ctx, "CREATE TABLE only_in_a (id int)"); err != nil {
		t.Fatal(err)
	}
	var exists bool
	err := b.QueryRow(ctx, "SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'only_in_a')").Scan(&exists)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("table leaked between test databases")
	}
}
