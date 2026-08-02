package pg

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed all:migrations
var embeddedMigrations embed.FS

// Migrations is the production migration set compiled into the binary.
func Migrations() fs.FS {
	sub, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		// Unreachable: the directory is embedded at compile time.
		panic("pg: embedded migrations missing: " + err.Error())
	}
	return sub
}

// caesarMigrationLockKey scopes pg_advisory_lock so concurrent caesar
// processes (or a deploy racing a manual `caesar migrate`) serialize.
const caesarMigrationLockKey int64 = 0x_CAE5A4_000001

// goose keeps package-level state (base FS, dialect, logger); serialize our use.
var gooseMu sync.Mutex

// Migrate applies all pending migrations. An empty set is a no-op: the
// binary must boot before the first domain migration exists (Plan 2).
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error {
	return withGoose(ctx, pool, fsys, func(ctx context.Context, db *sql.DB) error {
		return goose.UpContext(ctx, db, ".")
	})
}

// MigrateDown rolls back the most recent migration (one step).
func MigrateDown(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error {
	return withGoose(ctx, pool, fsys, func(ctx context.Context, db *sql.DB) error {
		return goose.DownContext(ctx, db, ".")
	})
}

// MigrateStatus writes migration status to w for ops inspection.
func MigrateStatus(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, w io.Writer) error {
	return withGoose(ctx, pool, fsys, func(ctx context.Context, db *sql.DB) error {
		goose.SetLogger(gooseLogger{w})
		defer goose.SetLogger(gooseLogger{io.Discard})
		return goose.StatusContext(ctx, db, ".")
	})
}

// gooseLogger adapts an io.Writer to goose's Logger interface.
type gooseLogger struct{ w io.Writer }

func (l gooseLogger) Fatalf(format string, v ...any) { fmt.Fprintf(l.w, format+"\n", v...) }
func (l gooseLogger) Printf(format string, v ...any) { fmt.Fprintf(l.w, format, v...) }

func withGoose(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, run func(context.Context, *sql.DB) error) error {
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return fmt.Errorf("pg: scan migrations: %w", err)
	}
	if len(names) == 0 {
		return nil // pre-first-migration: nothing to do, and goose errors on empty sets
	}

	gooseMu.Lock()
	defer gooseMu.Unlock()

	// Independent database/sql handle from the pool's config: safe to close
	// without touching the pool's own connections.
	db := stdlib.OpenDB(*pool.Config().ConnConfig)
	defer func() { _ = db.Close() }()

	// Hold the advisory lock on one dedicated session for the whole run;
	// session end releases it even on crash.
	lockConn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pg: migration lock conn: %w", err)
	}
	defer func() { _ = lockConn.Close() }()
	if _, err := lockConn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", caesarMigrationLockKey); err != nil {
		return fmt.Errorf("pg: acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = lockConn.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", caesarMigrationLockKey)
	}()

	goose.SetBaseFS(fsys)
	defer goose.SetBaseFS(nil)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("pg: goose dialect: %w", err)
	}
	if err := run(ctx, db); err != nil {
		return fmt.Errorf("pg: migrate: %w", err)
	}
	return nil
}
