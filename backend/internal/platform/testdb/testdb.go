//go:build integration || runnerintegration

// Package testdb hands each test an isolated Postgres database. One
// postgres:18-alpine container starts per test process (reaped by
// testcontainers' ryuk); every New/NewURL call clones a fresh database
// from a template, so tests never share state.
package testdb

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/giiorg/caesar/backend/internal/platform/pg"
)

const templateDB = "caesar_template"

var (
	mu       sync.Mutex
	adminURL string // connection URL to the container's postgres database
	startErr error
	started  bool
	counter  atomic.Int64
)

// ensureStarted boots the container and creates the template database once
// per process. Task 5 extends it to run embedded migrations into the
// template before any clone is made.
func ensureStarted(ctx context.Context) error {
	if started || startErr != nil {
		return startErr
	}
	ctr, err := postgres.Run(ctx, "postgres:18-alpine",
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("caesar"),
		postgres.WithPassword("caesar"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		startErr = fmt.Errorf("testdb: start container: %w", err)
		return startErr
	}
	adminURL, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		startErr = fmt.Errorf("testdb: connection string: %w", err)
		return startErr
	}
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		startErr = fmt.Errorf("testdb: admin pool: %w", err)
		return startErr
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+templateDB); err != nil {
		startErr = fmt.Errorf("testdb: create template: %w", err)
		return startErr
	}

	tmplURL, err := url.Parse(adminURL)
	if err != nil {
		startErr = fmt.Errorf("testdb: parse admin url: %w", err)
		return startErr
	}
	tmplURL.Path = "/" + templateDB
	tmpl, err := pgxpool.New(ctx, tmplURL.String())
	if err != nil {
		startErr = fmt.Errorf("testdb: template pool: %w", err)
		return startErr
	}
	// Close before any clone: CREATE DATABASE ... TEMPLATE requires zero
	// live connections on the template.
	err = pg.Migrate(ctx, tmpl, pg.Migrations())
	tmpl.Close()
	if err != nil {
		startErr = fmt.Errorf("testdb: migrate template: %w", err)
		return startErr
	}

	started = true
	return nil
}

// NewURL returns the connection URL of a fresh database cloned from the
// migrated template.
func NewURL(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	mu.Lock()
	defer mu.Unlock()
	if err := ensureStarted(ctx); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("caesar_test_%d", counter.Add(1))
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("testdb: admin pool: %v", err)
	}
	defer admin.Close()
	// TEMPLATE clone requires no live connections on the template; the
	// package mutex serializes all clones.
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateDB)); err != nil {
		t.Fatalf("testdb: clone template: %v", err)
	}
	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatalf("testdb: parse admin url: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

// New returns a pool on a fresh isolated database, closed on test cleanup.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), NewURL(t))
	if err != nil {
		t.Fatalf("testdb: pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
