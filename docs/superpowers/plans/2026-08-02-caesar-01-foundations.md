# caesar Plan 01 — Foundations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A bootable, migratable, mechanically-enforced Go skeleton for caesar — binary starts, connects to Postgres, runs (empty) migrations under an advisory lock, serves `/healthz`, and every locked engineering standard is enforced by a green `task ci`.

**Architecture:** Modular monolith per spec §1: `backend/cmd/caesar` is a thin entry that delegates to `backend/internal/app` (composition root); `backend/internal/platform/` is the shared kernel (config, logging, httpx, pg, buildinfo, xerrors, testdb); bounded contexts arrive in later plans as peers of `platform/` and `app/` under `internal/`. Enforcement is code: architecture tests walk the import graph, custom `go/analysis` analyzers police house law, golangci-lint + gitleaks + govulncheck gate CI. No domain logic in this plan.

**Tech Stack:** Go 1.26 · PostgreSQL 18 · pgx/v5 + pgxpool · goose/v3 (runtime) + Atlas (authoring) · testcontainers-go · golang.org/x/tools (packages, analysis) · go-cmp · Taskfile · golangci-lint v1.64.8 · gitleaks v8.21.2 · GitHub Actions + ghcr.io · distroless.

## Global Constraints

Copied from the spec/decisions — every task inherits these:

- Module path: `github.com/giiorg/caesar/backend` (repo: `github.com/giiorg/caesar`). Monorepo roots: `/backend`, `/web`, `/deploy`, `/docs` (`/web` arrives Plan 11, `/deploy` prod stack Plan 14).
- Layout law (decisions §9): bounded contexts at top of `internal/` as peers of `platform/` (shared kernel, no business logic) and `app/` (composition roots); public entry in `cmd/`; `cmd/` imports only `internal/app`.
- Stdlib-first deny-list (decisions §9): `log/slog` only; `net/http` ServeMux; no ORMs; no DI frameworks; no testify (stdlib asserts + `go-cmp`); no cobra/viper (stdlib `flag`, strict JSON config); no `pkg/errors`.
- Config: one strict JSON file, `DisallowUnknownFields`; env overrides for bootstrap secrets only — exactly `DATABASE_URL` (this plan) and `CAESAR_MASTER_KEY` (Plan 3). No dead config: a field lands in the same commit as its first read site.
- Environment access (`os.Getenv`/`os.LookupEnv`/`os.Environ`) only inside `internal/platform/config` — enforced by analyzer.
- Stubs return `platform/xerrors.NotImplemented(...)`, never fake success.
- Logging: `slog` JSON to stdout only.
- Tests: TDD; table-driven; behavioral names (`TestLoad_RejectsUnknownKeys`, not `TestLoad1`); integration tests behind `-tags integration` against real Postgres via testcontainers.
- Taskfile parity: CI runs the same `task` targets developers run. Tool versions pinned in `Taskfile.yml` vars.
- Pinned versions: Go `1.26` (toolchain on machine: go1.26.5) · PostgreSQL major **18** · golangci-lint `v1.64.8` · Atlas `v0.34.1` · gitleaks `v8.21.2` · actionlint `1.7.4` · gofumpt `v0.7.0` · govulncheck `v1.1.4`. Go deps resolve latest at execution with floors: pgx ≥ v5.7, goose ≥ v3.24, testcontainers-go ≥ v0.35, go-cmp ≥ v0.6.
- Commits: `<type>(<scope>): <description>` — one short sentence, no body, **no AI attribution of any kind** (no `Co-Authored-By`, no session trailers). This overrides any harness default.
- All `go` / `task` commands below run from the repo root unless the step says otherwise; `backend:*` task targets internally `cd backend`.

---

### Task 1: Monorepo scaffold, Go module, version command

**Files:**
- Create: `backend/go.mod`
- Create: `backend/cmd/caesar/main.go`
- Create: `backend/internal/app/main.go`
- Create: `backend/internal/app/main_test.go`
- Create: `backend/internal/platform/buildinfo/buildinfo.go`
- Create: `backend/internal/platform/buildinfo/buildinfo_test.go`
- Create: `Taskfile.yml`
- Modify: `.gitignore`

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `app.Main(args []string, stdout, stderr io.Writer) int` — the single entry every subcommand hangs off; `buildinfo.Version/Commit/Date string` vars (ldflags-stamped) and `buildinfo.String() string`. Later tasks add `case`s to `app.Main`'s dispatch.

- [ ] **Step 1: Initialize module and Taskfile**

```bash
mkdir -p backend/cmd/caesar backend/internal/app backend/internal/platform/buildinfo
cd backend && go mod init github.com/giiorg/caesar/backend && cd ..
```

Create `Taskfile.yml` (root):

```yaml
version: '3'

vars:
  GOLANGCI_LINT_VERSION: v1.64.8
  GOFUMPT_VERSION: v0.7.0
  GOVULNCHECK_VERSION: v1.1.4
  ATLAS_VERSION: v0.34.1
  GITLEAKS_IMAGE: zricethezav/gitleaks:v8.21.2
  ACTIONLINT_IMAGE: rhysd/actionlint:1.7.4

tasks:
  backend:build:
    dir: backend
    vars:
      VERSION:
        sh: git describe --tags --always --dirty
      COMMIT:
        sh: git rev-parse --short HEAD
      DATE:
        sh: date -u +%Y-%m-%dT%H:%M:%SZ
    cmds:
      - go build -trimpath -ldflags "-s -w -X github.com/giiorg/caesar/backend/internal/platform/buildinfo.Version={{.VERSION}} -X github.com/giiorg/caesar/backend/internal/platform/buildinfo.Commit={{.COMMIT}} -X github.com/giiorg/caesar/backend/internal/platform/buildinfo.Date={{.DATE}}" -o bin/caesar ./cmd/caesar

  backend:test:
    dir: backend
    cmds:
      - go test -race ./...
```

Append to `.gitignore`:

```
backend/bin/
.task/
```

- [ ] **Step 2: Write the failing buildinfo test**

`backend/internal/platform/buildinfo/buildinfo_test.go`:

```go
package buildinfo

import "testing"

func TestString_ContainsAllStampedFields(t *testing.T) {
	got := String()
	want := "caesar dev (commit none, built unknown)"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd backend && go test ./internal/platform/buildinfo/`
Expected: FAIL — `undefined: String`

- [ ] **Step 4: Implement buildinfo**

`backend/internal/platform/buildinfo/buildinfo.go`:

```go
// Package buildinfo carries the version identity stamped into the binary at
// build time via -ldflags (see the backend:build task).
package buildinfo

import "fmt"

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func String() string {
	return fmt.Sprintf("caesar %s (commit %s, built %s)", Version, Commit, Date)
}
```

Run: `cd backend && go test ./internal/platform/buildinfo/`
Expected: PASS

- [ ] **Step 5: Write the failing app.Main dispatch test**

`backend/internal/app/main_test.go`:

```go
package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestMain_VersionPrintsBuildInfoAndExitsZero(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Main([]string{"version"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "caesar ") {
		t.Fatalf("stdout = %q, want prefix %q", out.String(), "caesar ")
	}
}

func TestMain_UnknownCommandExitsTwoWithUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Main([]string{"frobnicate"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("stderr = %q, want it to mention %q", errOut.String(), "unknown command")
	}
}
```

- [ ] **Step 6: Run tests to verify they fail**

Run: `cd backend && go test ./internal/app/`
Expected: FAIL — `undefined: Main`

- [ ] **Step 7: Implement app.Main and the thin cmd entry**

`backend/internal/app/main.go`:

```go
// Package app is the composition root: it wires platform packages into the
// runnable caesar binary. cmd/caesar delegates here and nothing else.
package app

import (
	"fmt"
	"io"

	"github.com/giiorg/caesar/backend/internal/platform/buildinfo"
)

const usage = `caesar — deterministic orchestrator of agentic workflows

commands:
  serve     start the control plane (default)
  migrate   run database migrations (up|down|status)
  version   print build information
`

// Main dispatches subcommands. It returns the process exit code; main() is
// the only caller of os.Exit.
func Main(args []string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "version":
		fmt.Fprintln(stdout, buildinfo.String())
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}
```

(`serve` and `migrate` are deliberately absent from the switch until Tasks 5–6 implement them — an unknown command is an error, never a silent success.)

`backend/cmd/caesar/main.go`:

```go
package main

import (
	"os"

	"github.com/giiorg/caesar/backend/internal/app"
)

func main() {
	os.Exit(app.Main(os.Args[1:], os.Stdout, os.Stderr))
}
```

- [ ] **Step 8: Run tests and build**

Run: `task backend:test && task backend:build && ./backend/bin/caesar version`
Expected: tests PASS; version line printed with a real git commit hash.

- [ ] **Step 9: Commit**

```bash
git add backend/ Taskfile.yml .gitignore
git commit -m "chore(backend): scaffold go module with app dispatch and version command"
```

---

### Task 2: Strict JSON config loader

**Files:**
- Create: `backend/internal/platform/config/config.go`
- Create: `backend/internal/platform/config/config_test.go`
- Create: `backend/caesar.dev.json`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Load(path string) (Config, error)`; `type Config struct { Server Server; Database Database; Log Log }` with `Server.Addr string`, `Server.ShutdownTimeout config.Duration`, `Database.URL string`, `Log.Level string`; `config.Duration` with `Std() time.Duration`. Env override: `DATABASE_URL` (only here — analyzer-enforced). Plan 3 adds `CAESAR_MASTER_KEY` alongside its first read site.

- [ ] **Step 1: Write the failing tests**

`backend/internal/platform/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "caesar.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const valid = `{
  "server": {"addr": ":9090", "shutdown_timeout": "5s"},
  "database": {"url": "postgres://u:p@localhost:5432/db"},
  "log": {"level": "debug"}
}`

func TestLoad_ParsesAllFields(t *testing.T) {
	cfg, err := Load(writeFile(t, valid))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != ":9090" {
		t.Errorf("Server.Addr = %q, want %q", cfg.Server.Addr, ":9090")
	}
	if cfg.Server.ShutdownTimeout.Std() != 5*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 5s", cfg.Server.ShutdownTimeout.Std())
	}
	if cfg.Database.URL != "postgres://u:p@localhost:5432/db" {
		t.Errorf("Database.URL = %q", cfg.Database.URL)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level = %q, want debug", cfg.Log.Level)
	}
}

func TestLoad_RejectsUnknownKeys(t *testing.T) {
	tests := []struct{ name, body string }{
		{"top level", `{"database": {"url": "postgres://x"}, "wat": 1}`},
		{"nested", `{"database": {"url": "postgres://x", "wat": 1}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeFile(t, tt.body))
			if err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("err = %v, want unknown-field rejection", err)
			}
		})
	}
}

func TestLoad_AppliesDefaultsForOmittedFields(t *testing.T) {
	cfg, err := Load(writeFile(t, `{"database": {"url": "postgres://x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != ":8080" {
		t.Errorf("default Addr = %q, want :8080", cfg.Server.Addr)
	}
	if cfg.Server.ShutdownTimeout.Std() != 15*time.Second {
		t.Errorf("default ShutdownTimeout = %v, want 15s", cfg.Server.ShutdownTimeout.Std())
	}
	if cfg.Log.Level != "info" {
		t.Errorf("default Log.Level = %q, want info", cfg.Log.Level)
	}
}

func TestLoad_DatabaseURLEnvOverridesFile(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://env-wins")
	cfg, err := Load(writeFile(t, valid))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.URL != "postgres://env-wins" {
		t.Errorf("Database.URL = %q, want env override", cfg.Database.URL)
	}
}

func TestLoad_FailsClosed(t *testing.T) {
	tests := []struct{ name, body, wantErr string }{
		{"missing database url", `{"server": {"addr": ":1"}}`, "database.url"},
		{"empty addr", `{"server": {"addr": ""}, "database": {"url": "postgres://x"}}`, "server.addr"},
		{"bad log level", `{"database": {"url": "postgres://x"}, "log": {"level": "loud"}}`, "log.level"},
		{"bad duration", `{"server": {"shutdown_timeout": "fast"}, "database": {"url": "postgres://x"}}`, "duration"},
		{"malformed json", `{`, "parse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeFile(t, tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want mention of %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_MissingFileErrors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("want error for missing file")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/platform/config/`
Expected: FAIL — `undefined: Load`

- [ ] **Step 3: Implement the loader**

`backend/internal/platform/config/config.go`:

```go
// Package config loads caesar's single strict JSON config file. Unknown keys
// are rejected (default-deny). Environment overrides exist for bootstrap
// secrets only: DATABASE_URL here, CAESAR_MASTER_KEY when credentials land.
// This is the only package allowed to touch the process environment
// (enforced by the envaccess analyzer).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

type Config struct {
	Server   Server   `json:"server"`
	Database Database `json:"database"`
	Log      Log      `json:"log"`
}

type Server struct {
	Addr            string   `json:"addr"`
	ShutdownTimeout Duration `json:"shutdown_timeout"`
}

type Database struct {
	URL string `json:"url"`
}

type Log struct {
	Level string `json:"level"`
}

// Duration is a time.Duration that unmarshals from JSON strings like "15s".
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf(`duration must be a string like "15s": %w`, err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

func defaults() Config {
	return Config{
		Server: Server{Addr: ":8080", ShutdownTimeout: Duration(15 * time.Second)},
		Log:    Log{Level: "info"},
	}
}

func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: open: %w", err)
	}
	defer f.Close()

	cfg := defaults()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}

	if url := os.Getenv("DATABASE_URL"); url != "" {
		cfg.Database.URL = url
	}

	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

func (c Config) validate() error {
	var problems []string
	if c.Server.Addr == "" {
		problems = append(problems, "server.addr must not be empty")
	}
	if c.Database.URL == "" {
		problems = append(problems, "database.url is required (file or DATABASE_URL)")
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(c.Log.Level)); err != nil {
		problems = append(problems, fmt.Sprintf("log.level %q is not one of debug|info|warn|error", c.Log.Level))
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/platform/config/`
Expected: PASS

- [ ] **Step 5: Commit dev config alongside**

`backend/caesar.dev.json` (local dev only — credentials match the dev compose stack of Task 4, no real secrets):

```json
{
  "server": {"addr": ":8080"},
  "database": {"url": "postgres://caesar:caesar@localhost:5432/caesar_dev?sslmode=disable"},
  "log": {"level": "debug"}
}
```

```bash
git add backend/internal/platform/config backend/caesar.dev.json
git commit -m "feat(backend): strict json config loader with database-url env override"
```

---

### Task 3: slog JSON logging

**Files:**
- Create: `backend/internal/platform/logging/logging.go`
- Create: `backend/internal/platform/logging/logging_test.go`

**Interfaces:**
- Consumes: `config.Log.Level` (string form).
- Produces: `logging.New(w io.Writer, level string) (*slog.Logger, error)` — JSON handler, level-filtered. `app` passes `os.Stdout`.

- [ ] **Step 1: Write the failing tests**

`backend/internal/platform/logging/logging_test.go`:

```go
package logging

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNew_EmitsOneJSONObjectPerLine(t *testing.T) {
	var buf bytes.Buffer
	log, err := New(&buf, "info")
	if err != nil {
		t.Fatal(err)
	}
	log.Info("hello", "answer", 42)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("output is not JSON: %v (raw: %s)", err, buf.String())
	}
	if line["msg"] != "hello" || line["level"] != "INFO" || line["answer"] != float64(42) {
		t.Fatalf("unexpected line: %v", line)
	}
}

func TestNew_FiltersBelowConfiguredLevel(t *testing.T) {
	var buf bytes.Buffer
	log, err := New(&buf, "warn")
	if err != nil {
		t.Fatal(err)
	}
	log.Info("dropped")
	if buf.Len() != 0 {
		t.Fatalf("info line should be filtered at warn, got: %s", buf.String())
	}
}

func TestNew_RejectsUnknownLevel(t *testing.T) {
	if _, err := New(&bytes.Buffer{}, "loud"); err == nil {
		t.Fatal("want error for unknown level")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/platform/logging/`
Expected: FAIL — `undefined: New`

- [ ] **Step 3: Implement**

`backend/internal/platform/logging/logging.go`:

```go
// Package logging builds caesar's only logger: slog with a JSON handler.
// No other logging library may be imported anywhere (arch-test deny-list).
package logging

import (
	"fmt"
	"io"
	"log/slog"
)

func New(w io.Writer, level string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("logging: unknown level %q: %w", level, err)
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/platform/logging/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/platform/logging
git commit -m "feat(backend): slog json logger factory"
```

---

### Task 4: Postgres pool, dev compose, testcontainers harness

**Files:**
- Create: `backend/internal/platform/pg/pool.go`
- Create: `backend/internal/platform/pg/pool_integration_test.go`
- Create: `backend/internal/platform/testdb/testdb.go`
- Create: `compose.dev.yaml`
- Modify: `Taskfile.yml`

**Interfaces:**
- Consumes: `config.Database.URL`.
- Produces: `pg.New(ctx context.Context, url string) (*pgxpool.Pool, error)` (connects + pings); `testdb.New(t *testing.T) *pgxpool.Pool` and `testdb.NewURL(t *testing.T) string` — each call returns a fresh isolated database cloned from a template on a per-process `postgres:18-alpine` testcontainer. Integration files carry `//go:build integration`.

- [ ] **Step 1: Add dependencies**

```bash
cd backend
go get github.com/jackc/pgx/v5 github.com/testcontainers/testcontainers-go github.com/testcontainers/testcontainers-go/modules/postgres github.com/google/go-cmp
go mod tidy
```

- [ ] **Step 2: Write the failing integration tests**

`backend/internal/platform/pg/pool_integration_test.go`:

```go
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
```

- [ ] **Step 3: Run to verify failure**

Run: `cd backend && go test -tags integration ./internal/platform/pg/`
Expected: FAIL — `undefined: pg.New` / package testdb missing

- [ ] **Step 4: Implement pool and testdb**

`backend/internal/platform/pg/pool.go`:

```go
// Package pg owns Postgres connectivity and (from Task 5) migrations.
// PostgreSQL major is pinned at 18 across dev, tests, and prod.
package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

func New(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("pg: parse url: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pg: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: ping: %w", err)
	}
	return pool, nil
}
```

`backend/internal/platform/testdb/testdb.go`:

```go
//go:build integration

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
```

```bash
cd backend && go mod tidy
```

- [ ] **Step 5: Run integration tests to verify they pass**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/platform/pg/`
Expected: PASS (first run pulls `postgres:18-alpine`; docker must be running)

- [ ] **Step 6: Dev compose + task targets**

`compose.dev.yaml` (root — local dev database only; the prod stack is Plan 14's `/deploy/compose`):

```yaml
services:
  postgres:
    image: postgres:18-alpine
    environment:
      POSTGRES_USER: caesar
      POSTGRES_PASSWORD: caesar
      POSTGRES_DB: caesar_dev
    ports:
      - "5432:5432"
    volumes:
      - caesar-dev-pg:/var/lib/postgresql
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U caesar -d caesar_dev"]
      interval: 2s
      timeout: 3s
      retries: 15

volumes:
  caesar-dev-pg:
```

(The `postgres:18` image moved its volume mount up to `/var/lib/postgresql`; mounting the old `/data` subpath silently loses persistence.)

Add to `Taskfile.yml` `tasks:`:

```yaml
  db:up:
    cmds:
      - docker compose -f compose.dev.yaml up -d --wait

  db:down:
    cmds:
      - docker compose -f compose.dev.yaml down

  backend:test:integration:
    dir: backend
    cmds:
      - go test -race -tags integration -count=1 ./...
```

Run: `task db:up && task db:down`
Expected: postgres starts healthy, then stops.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/platform/pg backend/internal/platform/testdb backend/go.mod backend/go.sum compose.dev.yaml Taskfile.yml
git commit -m "feat(backend): pgx pool, dev compose stack, and per-test database harness"
```

---

### Task 5: Migration machinery — goose runtime under advisory lock, Atlas authoring, migrate subcommand

**Files:**
- Create: `backend/internal/platform/pg/migrate.go`
- Create: `backend/internal/platform/pg/migrate_integration_test.go`
- Create: `backend/internal/platform/pg/migrations/.gitkeep`
- Create: `backend/internal/platform/pg/schema.sql`
- Create: `backend/internal/platform/pg/testdata/migrations_ok/00001_widgets.sql`
- Create: `backend/internal/app/migrate.go`
- Modify: `backend/internal/app/main.go` (add `migrate` case)
- Modify: `backend/internal/platform/testdb/testdb.go` (migrate the template)
- Modify: `Taskfile.yml`

**Interfaces:**
- Consumes: `pg.New`, `testdb`, `config.Load`.
- Produces: `pg.Migrations() fs.FS` (embedded production set — empty until Plan 2); `pg.Migrate(ctx, pool, fsys) error` (up, advisory-locked, empty-set tolerant); `pg.MigrateDown(ctx, pool, fsys) error` (one step down); `pg.MigrateStatus(ctx, pool, fsys, w io.Writer) error`; `caesar migrate up|down|status -config <path>`. Serve (Task 6) calls `pg.Migrate` at startup. Authoring flow: edit `schema.sql` → `task db:new -- <name>` diffs it into goose files via Atlas.

- [ ] **Step 1: Add goose and the pgx stdlib bridge**

```bash
cd backend && go get github.com/pressly/goose/v3 && go mod tidy
```

- [ ] **Step 2: Write the failing integration tests**

Fixture `backend/internal/platform/pg/testdata/migrations_ok/00001_widgets.sql`:

```sql
-- +goose Up
CREATE TABLE widgets (
    id   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL
);

-- +goose Down
DROP TABLE widgets;
```

`backend/internal/platform/pg/migrate_integration_test.go`:

```go
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
	url := testdb.NewURL(t)
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
	// Production set is empty until Plan 2; boot must not fail.
	if err := pg.Migrate(ctx, pool, pg.Migrations()); err != nil {
		t.Fatalf("empty migration set must be tolerated: %v", err)
	}
}

func TestMigrateDown_RevertsOneStep(t *testing.T) {
	ctx := context.Background()
	url := testdb.NewURL(t)
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
	url := testdb.NewURL(t)
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
```

- [ ] **Step 3: Run to verify failure**

Run: `cd backend && go test -tags integration ./internal/platform/pg/ -run TestMigrate`
Expected: FAIL — `undefined: pg.Migrate`

- [ ] **Step 4: Implement the runner**

Create the embedded dir placeholder `backend/internal/platform/pg/migrations/.gitkeep` (empty file) and `backend/internal/platform/pg/schema.sql`:

```sql
-- caesar desired-state schema (single source for Atlas authoring and, from
-- Plan 2, sqlc). Empty until the first domain tables land in Plan 2.
-- Workflow: edit this file, then `task db:new -- <migration_name>`.
```

`backend/internal/platform/pg/migrate.go`:

```go
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
```

- [ ] **Step 5: Run integration tests to verify they pass**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/platform/pg/`
Expected: PASS, including the concurrent-runner and empty-set cases

- [ ] **Step 6: Migrate the testdb template**

In `backend/internal/platform/testdb/testdb.go`, extend `ensureStarted` — after `CREATE DATABASE caesar_template` and before `started = true`, add:

```go
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
```

(Import `github.com/giiorg/caesar/backend/internal/platform/pg`. With the production set still empty this is a no-op today; from Plan 2 on, every test database is born fully migrated.)

Run: `cd backend && go test -race -tags integration -count=1 ./internal/platform/...`
Expected: PASS

- [ ] **Step 7: Wire the `caesar migrate` subcommand**

`backend/internal/app/migrate.go`:

```go
package app

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
)

func runMigrateCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "caesar.json", "path to config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	direction := fs.Arg(0)
	if direction == "" {
		direction = "status"
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()
	pool, err := pg.New(ctx, cfg.Database.URL)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer pool.Close()

	switch direction {
	case "up":
		err = pg.Migrate(ctx, pool, pg.Migrations())
	case "down":
		err = pg.MigrateDown(ctx, pool, pg.Migrations())
	case "status":
		err = pg.MigrateStatus(ctx, pool, pg.Migrations(), stdout)
	default:
		fmt.Fprintf(stderr, "unknown migrate direction %q (want up|down|status)\n", direction)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "migrate %s: ok\n", direction)
	return 0
}
```

In `backend/internal/app/main.go`, add to the switch (above `default`):

```go
	case "migrate":
		return runMigrateCmd(args, stdout, stderr)
```

Add to `backend/internal/app/main_test.go`:

```go
func TestMain_MigrateWithBadConfigExitsOne(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Main([]string{"migrate", "-config", "/nonexistent/caesar.json", "up"}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %s)", code, errOut.String())
	}
}
```

Run: `cd backend && go test ./internal/app/`
Expected: PASS

- [ ] **Step 8: Atlas authoring target**

Add to `Taskfile.yml` `tasks:`:

```yaml
  db:new:
    desc: "Author a migration from schema.sql changes: task db:new -- add_users"
    dir: backend
    cmds:
      - atlas migrate diff {{.CLI_ARGS}} --dir "file://internal/platform/pg/migrations?format=goose" --to "file://internal/platform/pg/schema.sql" --dev-url "docker://postgres/18/dev"
```

Run: `atlas version`
Expected: `v0.34.1` (or newer) — the flow itself is first exercised by Plan 2's initial schema.

- [ ] **Step 9: Commit**

```bash
git add backend/internal/platform/pg backend/internal/platform/testdb backend/internal/app backend/go.mod backend/go.sum Taskfile.yml
git commit -m "feat(backend): goose migration runner under advisory lock with atlas authoring flow"
```

---

### Task 6: HTTP server — middleware, /healthz, serve command, graceful shutdown

**Files:**
- Create: `backend/internal/platform/httpx/middleware.go`
- Create: `backend/internal/platform/httpx/middleware_test.go`
- Create: `backend/internal/app/server.go`
- Create: `backend/internal/app/server_test.go`
- Create: `backend/internal/app/serve.go`
- Create: `backend/internal/app/serve_integration_test.go`
- Modify: `backend/internal/app/main.go` (add `serve` case)
- Modify: `Taskfile.yml`

**Interfaces:**
- Consumes: `config.Load`, `logging.New`, `pg.New`, `pg.Migrate`, `pg.Migrations`, `buildinfo.Version`, `testdb.New`.
- Produces: `httpx.Chain(h http.Handler, mw ...httpx.Middleware) http.Handler`; `httpx.WithCorrelationID` middleware + `httpx.CorrelationID(ctx) string` + `httpx.CorrelationHeader = "X-Correlation-ID"`; `httpx.AccessLog(*slog.Logger) httpx.Middleware`; `app.Routes(log *slog.Logger, db app.Pinger) http.Handler` with `type Pinger interface { Ping(context.Context) error }`; `caesar serve -config <path>`. Plan 2 mounts `/api/v1` routes into `app.Routes`.

- [ ] **Step 1: Write the failing httpx tests**

`backend/internal/platform/httpx/middleware_test.go`:

```go
package httpx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestWithCorrelationID_GeneratesWhenAbsent(t *testing.T) {
	var seen string
	h := WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = CorrelationID(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(seen) {
		t.Fatalf("generated id = %q, want 32 hex chars", seen)
	}
	if got := rec.Header().Get(CorrelationHeader); got != seen {
		t.Fatalf("response header = %q, want %q", got, seen)
	}
}

func TestWithCorrelationID_PropagatesProvidedID(t *testing.T) {
	var seen string
	h := WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = CorrelationID(r.Context())
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(CorrelationHeader, "caller-chose-this")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if seen != "caller-chose-this" {
		t.Fatalf("ctx id = %q, want caller's id", seen)
	}
	if got := rec.Header().Get(CorrelationHeader); got != "caller-chose-this" {
		t.Fatalf("response header = %q, want caller's id", got)
	}
}

func TestAccessLog_EmitsMethodPathStatusAndCorrelationID(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	h := Chain(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		}),
		WithCorrelationID, AccessLog(log),
	)
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set(CorrelationHeader, "cid-1")
	h.ServeHTTP(httptest.NewRecorder(), req)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, buf.String())
	}
	if line["method"] != "GET" || line["path"] != "/healthz" || line["status"] != float64(418) || line["correlation_id"] != "cid-1" {
		t.Fatalf("unexpected access log line: %v", line)
	}
}

func TestChain_AppliesFirstMiddlewareOutermost(t *testing.T) {
	var order []string
	mk := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), mk("outer"), mk("inner"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if len(order) != 2 || order[0] != "outer" || order[1] != "inner" {
		t.Fatalf("order = %v, want [outer inner]", order)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd backend && go test ./internal/platform/httpx/`
Expected: FAIL — `undefined: WithCorrelationID`

- [ ] **Step 3: Implement httpx**

`backend/internal/platform/httpx/middleware.go`:

```go
// Package httpx holds caesar's HTTP middleware primitives. Correlation IDs
// ride every request (spec §4): propagated from X-Correlation-ID or
// generated, echoed in the response, stamped into every access-log line.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

type Middleware func(http.Handler) http.Handler

// Chain wraps h so that the first middleware is outermost.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

const CorrelationHeader = "X-Correlation-ID"

type ctxKey int

const correlationKey ctxKey = iota

// CorrelationID returns the request's correlation id, or "" outside a request.
func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey).(string)
	return id
}

func WithCorrelationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(CorrelationHeader)
		if id == "" {
			id = newID()
		}
		w.Header().Set(CorrelationHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), correlationKey, id)))
	})
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("httpx: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// AccessLog logs one line per request. It must sit inside WithCorrelationID.
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			log.LogAttrs(r.Context(), slog.LevelInfo, "http_request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
				slog.String("correlation_id", CorrelationID(r.Context())),
			)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach Flush/Hijack on the underlying
// writer — required for SSE streaming later (spec §4).
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
```

Run: `cd backend && go test ./internal/platform/httpx/`
Expected: PASS

- [ ] **Step 4: Write the failing healthz handler tests**

`backend/internal/app/server_test.go`:

```go
package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func discard() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// getHealthzWithPinger is shared with serve_integration_test.go (same package).
func getHealthzWithPinger(t *testing.T, db Pinger) (*http.Response, map[string]any) {
	t.Helper()
	srv := httptest.NewServer(Routes(discard(), db))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func TestHealthz_ReportsOKWhenDatabaseReachable(t *testing.T) {
	resp, body := getHealthzWithPinger(t, fakePinger{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body["status"] != "ok" || body["db"] != "ok" {
		t.Fatalf("body = %v", body)
	}
	if body["version"] == "" {
		t.Fatal("version missing from healthz body")
	}
}

func TestHealthz_Reports503WhenDatabaseDown(t *testing.T) {
	resp, body := getHealthzWithPinger(t, fakePinger{err: errors.New("conn refused")})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if body["status"] != "degraded" || body["db"] != "unreachable" {
		t.Fatalf("body = %v", body)
	}
}

func TestHealthz_SetsCorrelationIDHeader(t *testing.T) {
	resp, _ := getHealthzWithPinger(t, fakePinger{})
	if resp.Header.Get("X-Correlation-ID") == "" {
		t.Fatal("correlation id header missing")
	}
}
```

- [ ] **Step 5: Run to verify failure**

Run: `cd backend && go test ./internal/app/`
Expected: FAIL — `undefined: Routes` / `undefined: Pinger`

- [ ] **Step 6: Implement Routes and the serve command**

`backend/internal/app/server.go`:

```go
package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/giiorg/caesar/backend/internal/platform/buildinfo"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
)

// Pinger is the slice of the pgx pool the HTTP layer needs.
type Pinger interface {
	Ping(context.Context) error
}

// Routes assembles caesar's HTTP surface. Plan 2 mounts /api/v1 here.
func Routes(log *slog.Logger, db Pinger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", handleHealthz(db))
	return httpx.Chain(mux, httpx.WithCorrelationID, httpx.AccessLog(log))
}

func handleHealthz(db Pinger) http.Handler {
	type response struct {
		Status  string `json:"status"`
		DB      string `json:"db"`
		Version string `json:"version"`
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		resp := response{Status: "ok", DB: "ok", Version: buildinfo.Version}
		code := http.StatusOK
		if err := db.Ping(ctx); err != nil {
			resp = response{Status: "degraded", DB: "unreachable", Version: buildinfo.Version}
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	})
}
```

`backend/internal/app/serve.go`:

```go
package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/giiorg/caesar/backend/internal/platform/buildinfo"
	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/platform/logging"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
)

func runServeCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "caesar.json", "path to config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	log, err := logging.New(stdout, cfg.Log.Level)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := runServe(ctx, cfg, log); err != nil {
		log.Error("serve failed", "error", err)
		return 1
	}
	return 0
}

// runServe boots the control plane: connect, migrate (advisory-locked),
// serve HTTP until ctx is cancelled, then drain within the shutdown timeout.
func runServe(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	pool, err := pg.New(ctx, cfg.Database.URL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := pg.Migrate(ctx, pool, pg.Migrations()); err != nil {
		return err
	}
	log.Info("migrations applied")

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           Routes(log, pool),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		// WriteTimeout deliberately unset: SSE streams (Plan 10) are
		// long-lived responses.
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", cfg.Server.Addr, "version", buildinfo.Version)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	log.Info("shutting down", "timeout", cfg.Server.ShutdownTimeout.Std().String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout.Std())
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
```

In `backend/internal/app/main.go`, add to the switch:

```go
	case "serve":
		return runServeCmd(args, stdout, stderr)
```

Run: `cd backend && go test ./internal/app/ ./internal/platform/httpx/`
Expected: PASS

- [ ] **Step 7: Integration test — boot against a real database, then shut down gracefully**

`backend/internal/app/serve_integration_test.go`:

```go
//go:build integration

package app

import (
	"context"
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

func TestRunServe_BootsMigratesAndShutsDownOnCancel(t *testing.T) {
	cfg := config.Config{}
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Server.ShutdownTimeout = config.Duration(5 * time.Second)
	cfg.Database.URL = testdb.NewURL(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, cfg, discard()) }()

	time.Sleep(200 * time.Millisecond) // let it bind and migrate
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe = %v, want clean shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runServe did not return after cancel")
	}
}

func TestRoutes_HealthzAgainstRealDatabase(t *testing.T) {
	pool := testdb.New(t)
	resp, body := getHealthzWithPinger(t, pool)
	if resp.StatusCode != 200 || body["db"] != "ok" {
		t.Fatalf("status=%d body=%v", resp.StatusCode, body)
	}
}
```

Run: `cd backend && go test -race -tags integration -count=1 ./internal/app/`
Expected: PASS

- [ ] **Step 8: Dev run target**

Add to `Taskfile.yml` `tasks:`:

```yaml
  backend:run:
    desc: "Run the control plane against the dev database (task db:up first)"
    dir: backend
    cmds:
      - go run ./cmd/caesar serve -config caesar.dev.json
```

Verify manually: `task db:up`, then `task backend:run` in one shell and `curl -s localhost:8080/healthz` in another.
Expected: `{"status":"ok","db":"ok","version":"dev"}` and a JSON access-log line on stdout; Ctrl-C exits 0 after "shutting down".

- [ ] **Step 9: Commit**

```bash
git add backend/internal/platform/httpx backend/internal/app Taskfile.yml
git commit -m "feat(backend): http server with healthz, correlation ids, and graceful shutdown"
```

---

### Task 7: xerrors + custom go/analysis analyzers (envaccess, stubfake, txboundary)

**Files:**
- Create: `backend/internal/platform/xerrors/xerrors.go`
- Create: `backend/internal/platform/xerrors/xerrors_test.go`
- Create: `backend/internal/analyzers/envaccess/envaccess.go`
- Create: `backend/internal/analyzers/envaccess/envaccess_test.go`
- Create: `backend/internal/analyzers/envaccess/testdata/src/a/a.go`
- Create: `backend/internal/analyzers/envaccess/testdata/src/internal/platform/config/config.go`
- Create: `backend/internal/analyzers/stubfake/stubfake.go`
- Create: `backend/internal/analyzers/stubfake/stubfake_test.go`
- Create: `backend/internal/analyzers/stubfake/testdata/src/a/a.go`
- Create: `backend/internal/analyzers/txboundary/txboundary.go`
- Create: `backend/internal/analyzers/txboundary/txboundary_test.go`
- Create: `backend/internal/analyzers/txboundary/testdata/src/github.com/jackc/pgx/v5/pgx.go`
- Create: `backend/internal/analyzers/txboundary/testdata/src/internal/billing/billing.go`
- Create: `backend/internal/analyzers/cmd/analyze/main.go`
- Modify: `Taskfile.yml`

**Interfaces:**
- Consumes: nothing from earlier tasks (standalone tooling).
- Produces: `xerrors.ErrNotImplemented` sentinel + `xerrors.NotImplemented(what string) error`; analyzers `envaccess.Analyzer`, `stubfake.Analyzer`, `txboundary.Analyzer` (`*analysis.Analyzer`); `task backend:analyze` running all three over the tree. House law encoded: env access only in `platform/config`; stubs use the typed sentinel; exported context APIs never expose transaction types.

- [ ] **Step 1: xerrors — failing test, then implement**

`backend/internal/platform/xerrors/xerrors_test.go`:

```go
package xerrors

import (
	"errors"
	"testing"
)

func TestNotImplemented_WrapsTypedSentinel(t *testing.T) {
	err := NotImplemented("k8s runner adapter")
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatal("NotImplemented must wrap ErrNotImplemented")
	}
	if got := err.Error(); got != "k8s runner adapter: not implemented" {
		t.Fatalf("Error() = %q", got)
	}
}
```

Run: `cd backend && go test ./internal/platform/xerrors/` → FAIL `undefined: NotImplemented`

`backend/internal/platform/xerrors/xerrors.go`:

```go
// Package xerrors holds caesar's cross-cutting error sentinels. Stubs must
// return NotImplemented — never a fake success (decisions §6.13); the
// stubfake analyzer points violators here.
package xerrors

import (
	"errors"
	"fmt"
)

var ErrNotImplemented = errors.New("not implemented")

func NotImplemented(what string) error {
	return fmt.Errorf("%s: %w", what, ErrNotImplemented)
}
```

Run again → PASS.

- [ ] **Step 2: Add the analysis dependency**

```bash
cd backend && go get golang.org/x/tools/go/analysis golang.org/x/tools/go/analysis/analysistest && go mod tidy
```

- [ ] **Step 3: envaccess — failing analysistest, then implement**

`backend/internal/analyzers/envaccess/testdata/src/a/a.go`:

```go
package a

import "os"

func bad() {
	_ = os.Getenv("HOME")     // want `environment access outside internal/platform/config`
	_, _ = os.LookupEnv("X")  // want `environment access outside internal/platform/config`
	_ = os.Environ()          // want `environment access outside internal/platform/config`
}
```

`backend/internal/analyzers/envaccess/testdata/src/internal/platform/config/config.go`:

```go
package config

import "os"

func allowed() string { return os.Getenv("DATABASE_URL") } // no diagnostic: this is the one allowed package
```

`backend/internal/analyzers/envaccess/envaccess_test.go`:

```go
package envaccess_test

import (
	"testing"

	"github.com/giiorg/caesar/backend/internal/analyzers/envaccess"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), envaccess.Analyzer, "a", "internal/platform/config")
}
```

Run: `cd backend && go test ./internal/analyzers/envaccess/` → FAIL (package doesn't compile yet)

`backend/internal/analyzers/envaccess/envaccess.go`:

```go
// Package envaccess enforces the constructed-env rule (spec §3): the process
// environment may only be read inside internal/platform/config. Everything
// an agent container sees is an explicitly constructed allowlist, so ambient
// env reads anywhere else are a designed-out bug class.
package envaccess

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "envaccess",
	Doc:  "forbids process-environment access outside internal/platform/config",
	Run:  run,
}

var forbidden = map[string]bool{
	"os.Getenv":       true,
	"os.LookupEnv":    true,
	"os.Environ":      true,
	"os.ExpandEnv":    true,
	"syscall.Getenv":  true,
	"syscall.Environ": true,
}

func run(pass *analysis.Pass) (any, error) {
	if strings.HasSuffix(pass.Pkg.Path(), "internal/platform/config") {
		return nil, nil
	}
	for _, file := range pass.Files {
		filename := pass.Fset.File(file.Pos()).Name()
		if strings.HasSuffix(filename, "_test.go") {
			continue // t.Setenv etc. are fine in tests
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			fn, ok := pass.TypesInfo.Uses[sel.Sel].(*types.Func)
			if !ok {
				return true
			}
			if forbidden[fn.FullName()] {
				pass.Reportf(call.Pos(), "environment access outside internal/platform/config (constructed env allowlist rule): %s", fn.FullName())
			}
			return true
		})
	}
	return nil, nil
}
```

Run: `cd backend && go test ./internal/analyzers/envaccess/`
Expected: PASS

- [ ] **Step 4: stubfake — failing analysistest, then implement**

`backend/internal/analyzers/stubfake/testdata/src/a/a.go`:

```go
package a

import (
	"errors"
	"fmt"
)

func bad1() error { return errors.New("not implemented") } // want `use platform/xerrors.NotImplemented`
func bad2() error { return fmt.Errorf("frobnicator Not Implemented yet") } // want `use platform/xerrors.NotImplemented`
func bad3() { panic("not implemented") } // want `use platform/xerrors.NotImplemented`
func ok() error { return errors.New("widget exploded") }
```

`backend/internal/analyzers/stubfake/stubfake_test.go`:

```go
package stubfake_test

import (
	"testing"

	"github.com/giiorg/caesar/backend/internal/analyzers/stubfake"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), stubfake.Analyzer, "a")
}
```

Run: `cd backend && go test ./internal/analyzers/stubfake/` → FAIL

`backend/internal/analyzers/stubfake/stubfake.go`:

```go
// Package stubfake forces stubs onto the typed sentinel: ad-hoc
// "not implemented" strings in errors.New/fmt.Errorf/panic must become
// platform/xerrors.NotImplemented so callers can errors.Is on it and no
// stub can masquerade as domain failure (decisions §6.13).
package stubfake

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "stubfake",
	Doc:  "requires platform/xerrors.NotImplemented for not-implemented stubs",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	if strings.HasSuffix(pass.Pkg.Path(), "platform/xerrors") {
		return nil, nil // the sentinel's own definition site
	}
	for _, file := range pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			if !isFlagged(pass, call) {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok {
				return true
			}
			if strings.Contains(strings.ToLower(lit.Value), "not implemented") {
				pass.Reportf(call.Pos(), "use platform/xerrors.NotImplemented — stubs must return the typed sentinel, never ad-hoc strings")
			}
			return true
		})
	}
	return nil, nil
}

// isFlagged reports whether call is errors.New, fmt.Errorf, or builtin panic.
func isFlagged(pass *analysis.Pass, call *ast.CallExpr) bool {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		_, isBuiltin := pass.TypesInfo.Uses[fun].(*types.Builtin)
		return isBuiltin && fun.Name == "panic"
	case *ast.SelectorExpr:
		fn, ok := pass.TypesInfo.Uses[fun.Sel].(*types.Func)
		if !ok {
			return false
		}
		full := fn.FullName()
		return full == "errors.New" || full == "fmt.Errorf"
	}
	return false
}
```

Run: `cd backend && go test ./internal/analyzers/stubfake/`
Expected: PASS

- [ ] **Step 5: txboundary — failing analysistest, then implement**

`backend/internal/analyzers/txboundary/testdata/src/github.com/jackc/pgx/v5/pgx.go` (type stub so analysistest can resolve the import without network):

```go
package pgx

type Tx interface {
	Commit() error
	Rollback() error
}
```

`backend/internal/analyzers/txboundary/testdata/src/internal/billing/billing.go`:

```go
package billing

import pgx "github.com/jackc/pgx/v5"

type Service struct{}

func (s *Service) Charge(tx pgx.Tx, amount int) error { return nil } // want `transaction type in exported bounded-context API`

func ChargeAll(txs []pgx.Tx) error { return nil } // want `transaction type in exported bounded-context API`

type Port interface {
	Settle(tx pgx.Tx) error // want `transaction type in exported bounded-context API`
}

func internalHelper(tx pgx.Tx) error { return nil } // unexported: fine within the context
```

`backend/internal/analyzers/txboundary/txboundary_test.go`:

```go
package txboundary_test

import (
	"testing"

	"github.com/giiorg/caesar/backend/internal/analyzers/txboundary"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), txboundary.Analyzer, "internal/billing")
}
```

Run: `cd backend && go test ./internal/analyzers/txboundary/` → FAIL

`backend/internal/analyzers/txboundary/txboundary.go`:

```go
// Package txboundary mechanically approximates "transactions never span
// bounded contexts" (spec §1 evolvability rule): no exported function,
// method, or interface method declared inside a bounded context may mention
// a pgx transaction type in its signature. Transactions stay an internal
// implementation detail of the context that opened them.
package txboundary

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "txboundary",
	Doc:  "forbids pgx transaction types in exported bounded-context signatures",
	Run:  run,
}

// nonContext are the internal/ segments that are not bounded contexts.
var nonContext = map[string]bool{"platform": true, "app": true, "arch": true, "analyzers": true}

// boundedContext extracts the context segment from a package path, if any.
func boundedContext(pkgPath string) (string, bool) {
	idx := strings.LastIndex(pkgPath, "internal/")
	if idx == -1 {
		return "", false
	}
	rest := pkgPath[idx+len("internal/"):]
	seg, _, _ := strings.Cut(rest, "/")
	if seg == "" || nonContext[seg] {
		return "", false
	}
	return seg, true
}

func run(pass *analysis.Pass) (any, error) {
	if _, ok := boundedContext(pass.Pkg.Path()); !ok {
		return nil, nil
	}
	for _, file := range pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch decl := n.(type) {
			case *ast.FuncDecl:
				if !decl.Name.IsExported() {
					return true
				}
				fn, ok := pass.TypesInfo.Defs[decl.Name].(*types.Func)
				if ok && signatureMentionsTx(fn.Type().(*types.Signature)) {
					pass.Reportf(decl.Name.Pos(), "transaction type in exported bounded-context API %s — transactions never span contexts", decl.Name.Name)
				}
			case *ast.TypeSpec:
				if !decl.Name.IsExported() {
					return true
				}
				iface, ok := pass.TypesInfo.TypeOf(decl.Type).(*types.Interface)
				if !ok {
					return true
				}
				for i := 0; i < iface.NumExplicitMethods(); i++ {
					m := iface.ExplicitMethod(i)
					if signatureMentionsTx(m.Type().(*types.Signature)) {
						// Report at the method, so the diagnostic lands on
						// the offending line.
						pass.Reportf(m.Pos(), "transaction type in exported bounded-context API %s.%s — transactions never span contexts", decl.Name.Name, m.Name())
					}
				}
			}
			return true
		})
	}
	return nil, nil
}

var txTypePkgs = map[string]bool{
	"github.com/jackc/pgx/v5":         true,
	"github.com/jackc/pgx/v5/pgxpool": true,
}

func signatureMentionsTx(sig *types.Signature) bool {
	seen := map[types.Type]bool{}
	check := func(tuple *types.Tuple) bool {
		for i := 0; i < tuple.Len(); i++ {
			if mentionsTx(tuple.At(i).Type(), seen) {
				return true
			}
		}
		return false
	}
	return check(sig.Params()) || check(sig.Results())
}

func mentionsTx(t types.Type, seen map[types.Type]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	switch tt := t.(type) {
	case *types.Named:
		obj := tt.Obj()
		if obj.Pkg() != nil && txTypePkgs[obj.Pkg().Path()] && obj.Name() == "Tx" {
			return true
		}
		return false
	case *types.Pointer:
		return mentionsTx(tt.Elem(), seen)
	case *types.Slice:
		return mentionsTx(tt.Elem(), seen)
	case *types.Array:
		return mentionsTx(tt.Elem(), seen)
	case *types.Map:
		return mentionsTx(tt.Key(), seen) || mentionsTx(tt.Elem(), seen)
	case *types.Chan:
		return mentionsTx(tt.Elem(), seen)
	case *types.Signature:
		return signatureMentionsTx(tt)
	}
	return false
}
```

Run: `cd backend && go test ./internal/analyzers/txboundary/`
Expected: PASS

- [ ] **Step 6: Multichecker command + task target**

`backend/internal/analyzers/cmd/analyze/main.go`:

```go
// Command analyze bundles caesar's house-law analyzers as a `go vet
// -vettool` plugin (unitchecker speaks exactly that protocol, and vet
// handles build tags for us). Not under backend/cmd: internal tooling, not
// a public entry point.
package main

import (
	"golang.org/x/tools/go/analysis/unitchecker"

	"github.com/giiorg/caesar/backend/internal/analyzers/envaccess"
	"github.com/giiorg/caesar/backend/internal/analyzers/stubfake"
	"github.com/giiorg/caesar/backend/internal/analyzers/txboundary"
)

func main() {
	unitchecker.Main(envaccess.Analyzer, stubfake.Analyzer, txboundary.Analyzer)
}
```

Add to `Taskfile.yml` `tasks:`:

```yaml
  backend:analyze:
    dir: backend
    cmds:
      - go build -o bin/caesar-analyze ./internal/analyzers/cmd/analyze
      - go vet -tags integration -vettool=./bin/caesar-analyze ./...
```

Run: `task backend:analyze`
Expected: exit 0, no findings (config package is the only env reader; no stubs; no contexts yet).

- [ ] **Step 7: Commit**

```bash
git add backend/internal/platform/xerrors backend/internal/analyzers backend/go.mod backend/go.sum Taskfile.yml
git commit -m "feat(backend): house-law analyzers for env access, stub sentinels, and tx boundaries"
```

---

### Task 8: Architecture tests — import-graph rules and dependency deny-list

**Files:**
- Create: `backend/internal/arch/rules.go`
- Create: `backend/internal/arch/rules_test.go`
- Create: `backend/internal/arch/live_test.go`

**Interfaces:**
- Consumes: the real package tree (via `golang.org/x/tools/go/packages`).
- Produces: `arch.Graph map[string][]string`; rule funcs `arch.CheckContextIsolation`, `arch.CheckPlatformPurity`, `arch.CheckNoAppImports`, `arch.CheckCmdOnlyApp`, `arch.CheckDenyList` (each `func(Graph) []string` — empty slice means clean); `arch.LoadGraph() (Graph, error)`; `arch.DenyList map[string]string`. Task 9 adds `CheckDeadConfig` beside these. Later plans never edit the rules to admit violations — they fix the code.

- [ ] **Step 1: Write the failing rule-engine tests (synthetic graphs)**

`backend/internal/arch/rules_test.go`:

```go
package arch

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

const m = "github.com/giiorg/caesar/backend"

func TestCheckContextIsolation_FlagsCrossContextImports(t *testing.T) {
	g := Graph{
		m + "/internal/execution":        {m + "/internal/identity", m + "/internal/platform/pg"},
		m + "/internal/identity":         {m + "/internal/platform/config"},
		m + "/internal/execution/worker": {m + "/internal/execution"},
	}
	got := CheckContextIsolation(g)
	want := []string{"internal/execution imports internal/identity: bounded contexts are peers and never import each other"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
}

func TestCheckContextIsolation_AllowsPlatformAndIntraContext(t *testing.T) {
	g := Graph{
		m + "/internal/execution":        {m + "/internal/platform/pg", m + "/internal/execution/queue"},
		m + "/internal/execution/queue":  {m + "/internal/platform/pg"},
	}
	if got := CheckContextIsolation(g); len(got) != 0 {
		t.Fatalf("want clean, got %v", got)
	}
}

func TestCheckPlatformPurity_FlagsPlatformImportingContextOrApp(t *testing.T) {
	g := Graph{
		m + "/internal/platform/pg": {m + "/internal/execution"},
		m + "/internal/platform/httpx": {m + "/internal/app"},
	}
	got := CheckPlatformPurity(g)
	if len(got) != 2 {
		t.Fatalf("want 2 violations, got %v", got)
	}
}

func TestCheckNoAppImports_FlagsContextImportingApp(t *testing.T) {
	g := Graph{
		m + "/internal/identity": {m + "/internal/app"},
	}
	if got := CheckNoAppImports(g); len(got) != 1 {
		t.Fatalf("want 1 violation, got %v", got)
	}
}

func TestCheckCmdOnlyApp_FlagsCmdImportingAnythingButApp(t *testing.T) {
	g := Graph{
		m + "/cmd/caesar": {m + "/internal/app", "os"},
		m + "/cmd/rogue":  {m + "/internal/platform/pg", "fmt"},
	}
	got := CheckCmdOnlyApp(g)
	want := []string{"cmd/rogue imports internal/platform/pg: cmd packages import only internal/app"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
}

func TestCheckDenyList_FlagsBannedDependencies(t *testing.T) {
	g := Graph{
		m + "/internal/identity": {"github.com/stretchr/testify/require"},
		m + "/internal/app":      {"go.uber.org/zap"},
		m + "/internal/platform/pg": {"github.com/jackc/pgx/v5"},
	}
	got := CheckDenyList(g)
	if len(got) != 2 {
		t.Fatalf("want 2 violations (testify, zap), got %v", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd backend && go test ./internal/arch/`
Expected: FAIL — `undefined: Graph`

- [ ] **Step 3: Implement the engine**

`backend/internal/arch/rules.go`:

```go
// Package arch encodes the layout law (decisions §9) as tests over the real
// import graph: bounded contexts at the top of internal/ are peers of
// platform/ (shared kernel) and app/ (composition root); cmd/ is thin.
// Violations fail CI; the fix is always the code, never the rule.
package arch

import (
	"fmt"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

const module = "github.com/giiorg/caesar/backend"

// Graph maps a package's import path to its direct imports.
type Graph map[string][]string

// nonContext segments under internal/ that are not bounded contexts.
var nonContext = map[string]bool{"platform": true, "app": true, "arch": true, "analyzers": true}

// contextOf returns the bounded-context segment of a module package path.
func contextOf(pkg string) (string, bool) {
	rest, ok := strings.CutPrefix(pkg, module+"/internal/")
	if !ok {
		return "", false
	}
	seg, _, _ := strings.Cut(rest, "/")
	if seg == "" || nonContext[seg] {
		return "", false
	}
	return seg, true
}

func isPlatform(pkg string) bool { return strings.HasPrefix(pkg, module+"/internal/platform") }
func isApp(pkg string) bool      { return pkg == module+"/internal/app" || strings.HasPrefix(pkg, module+"/internal/app/") }
func isCmd(pkg string) bool      { return strings.HasPrefix(pkg, module+"/cmd/") }

func short(pkg string) string { return strings.TrimPrefix(pkg, module+"/") }

// CheckContextIsolation: a bounded context never imports another one.
func CheckContextIsolation(g Graph) []string {
	var out []string
	for pkg, imps := range g {
		from, ok := contextOf(pkg)
		if !ok {
			continue
		}
		for _, imp := range imps {
			to, ok := contextOf(imp)
			if ok && to != from {
				out = append(out, fmt.Sprintf("%s imports %s: bounded contexts are peers and never import each other", short(pkg), short(imp)))
			}
		}
	}
	sort.Strings(out)
	return out
}

// CheckPlatformPurity: the shared kernel imports no context and no app.
func CheckPlatformPurity(g Graph) []string {
	var out []string
	for pkg, imps := range g {
		if !isPlatform(pkg) {
			continue
		}
		for _, imp := range imps {
			if _, isCtx := contextOf(imp); isCtx || isApp(imp) {
				out = append(out, fmt.Sprintf("%s imports %s: platform is the shared kernel and imports no business code", short(pkg), short(imp)))
			}
		}
	}
	sort.Strings(out)
	return out
}

// CheckNoAppImports: contexts never import the composition root.
func CheckNoAppImports(g Graph) []string {
	var out []string
	for pkg, imps := range g {
		if _, ok := contextOf(pkg); !ok {
			continue
		}
		for _, imp := range imps {
			if isApp(imp) {
				out = append(out, fmt.Sprintf("%s imports %s: contexts never import the composition root", short(pkg), short(imp)))
			}
		}
	}
	sort.Strings(out)
	return out
}

// CheckCmdOnlyApp: cmd packages import only internal/app from the module.
func CheckCmdOnlyApp(g Graph) []string {
	var out []string
	for pkg, imps := range g {
		if !isCmd(pkg) {
			continue
		}
		for _, imp := range imps {
			if strings.HasPrefix(imp, module+"/") && !isApp(imp) {
				out = append(out, fmt.Sprintf("%s imports %s: cmd packages import only internal/app", short(pkg), short(imp)))
			}
		}
	}
	sort.Strings(out)
	return out
}

// DenyList maps banned import prefixes to the locked decision behind them.
var DenyList = map[string]string{
	"github.com/stretchr/testify": "stdlib asserts + go-cmp only (spec §7)",
	"gorm.io":                     "no ORMs — pgx + sqlc (decisions §9)",
	"entgo.io":                    "no ORMs — pgx + sqlc (decisions §9)",
	"go.uber.org/fx":              "no DI frameworks — manual composition roots (decisions §9)",
	"go.uber.org/dig":             "no DI frameworks — manual composition roots (decisions §9)",
	"go.uber.org/zap":             "log/slog only (decisions §9)",
	"github.com/sirupsen/logrus":  "log/slog only (decisions §9)",
	"github.com/rs/zerolog":       "log/slog only (decisions §9)",
	"github.com/spf13/cobra":      "stdlib flag only (decisions §9)",
	"github.com/spf13/viper":      "strict JSON config via platform/config only (spec §8)",
	"github.com/joho/godotenv":    "env access only inside platform/config (spec §3)",
	"github.com/pkg/errors":       "stdlib errors + %w (decisions §9)",
}

// CheckDenyList: no module package imports a banned dependency.
func CheckDenyList(g Graph) []string {
	var out []string
	for pkg, imps := range g {
		for _, imp := range imps {
			for prefix, reason := range DenyList {
				if imp == prefix || strings.HasPrefix(imp, prefix+"/") {
					out = append(out, fmt.Sprintf("%s imports %s: %s", short(pkg), imp, reason))
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// LoadGraph builds the import graph of every package in the module,
// including integration-tagged files and test files. The module-path
// pattern (not "./...") makes the load independent of the test's working
// directory (internal/arch), covering the whole module.
func LoadGraph() (Graph, error) {
	cfg := &packages.Config{
		Mode:       packages.NeedName | packages.NeedImports,
		Dir:        ".",
		Tests:      true,
		BuildFlags: []string{"-tags", "integration"},
	}
	pkgs, err := packages.Load(cfg, module+"/...")
	if err != nil {
		return nil, err
	}
	if packages.PrintErrors(pkgs) > 0 {
		return nil, fmt.Errorf("arch: packages loaded with errors")
	}
	g := Graph{}
	for _, p := range pkgs {
		path := strings.TrimSuffix(p.PkgPath, ".test")
		path = strings.TrimSuffix(path, "_test")
		for imp := range p.Imports {
			g[path] = append(g[path], imp)
		}
	}
	return g, nil
}
```

Run: `cd backend && go test ./internal/arch/ -run TestCheck`
Expected: PASS

- [ ] **Step 4: Live-graph test**

`backend/internal/arch/live_test.go`:

```go
package arch

import "testing"

func TestArchitecture_RealImportGraphIsClean(t *testing.T) {
	g, err := LoadGraph()
	if err != nil {
		t.Fatal(err)
	}
	if len(g) == 0 {
		t.Fatal("empty graph: LoadGraph found no packages")
	}
	for name, check := range map[string]func(Graph) []string{
		"context isolation": CheckContextIsolation,
		"platform purity":   CheckPlatformPurity,
		"no app imports":    CheckNoAppImports,
		"cmd thinness":      CheckCmdOnlyApp,
		"dependency deny-list": CheckDenyList,
	} {
		if violations := check(g); len(violations) != 0 {
			t.Errorf("%s violated:\n  %v", name, violations)
		}
	}
}
```

Run: `cd backend && go test ./internal/arch/`
Expected: PASS (the live tree is clean today; this test is the tripwire forever after)

- [ ] **Step 5: Commit**

```bash
git add backend/internal/arch
git commit -m "test(backend): architecture tests enforcing layout law and dependency deny-list"
```

---

### Task 9: Dead-config checker

**Files:**
- Create: `backend/internal/arch/deadconfig.go`
- Create: `backend/internal/arch/deadconfig_test.go`
- Create: `backend/internal/arch/testdata/deadconfig-module/go.mod`
- Create: `backend/internal/arch/testdata/deadconfig-module/config/config.go`
- Create: `backend/internal/arch/testdata/deadconfig-module/consumer/consumer.go`

**Interfaces:**
- Consumes: `arch` package conventions from Task 8.
- Produces: `arch.CheckDeadConfig(dir, configPkgPath, configTypeName string) ([]string, error)` — every leaf field of the config struct must have a read site outside the config package (decisions §6.13: a field lands in the same commit as its first read). Live test pins it to `internal/platform/config.Config`.

- [ ] **Step 1: Write the failing fixture-module test**

`backend/internal/arch/testdata/deadconfig-module/go.mod`:

```
module example.com/deadconfig

go 1.26
```

`backend/internal/arch/testdata/deadconfig-module/config/config.go`:

```go
package config

type Config struct {
	Server Server
	Unused string
}

type Server struct {
	Addr string
}
```

`backend/internal/arch/testdata/deadconfig-module/consumer/consumer.go`:

```go
package consumer

import "example.com/deadconfig/config"

func Addr(c config.Config) string { return c.Server.Addr }
```

`backend/internal/arch/deadconfig_test.go`:

```go
package arch

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestCheckDeadConfig_FlagsFieldsWithoutReadSites(t *testing.T) {
	got, err := CheckDeadConfig("testdata/deadconfig-module", "example.com/deadconfig/config", "Config")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"config field Config.Unused has no read site outside the config package (no dead config)"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
}

func TestCheckDeadConfig_LiveConfigHasNoDeadFields(t *testing.T) {
	// "../.." = the backend module root: the checker loads "./..." under
	// dir, and this test runs with cwd internal/arch.
	got, err := CheckDeadConfig("../..", "github.com/giiorg/caesar/backend/internal/platform/config", "Config")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("dead config fields:\n  %v", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd backend && go test ./internal/arch/ -run TestCheckDeadConfig`
Expected: FAIL — `undefined: CheckDeadConfig`

- [ ] **Step 3: Implement**

`backend/internal/arch/deadconfig.go`:

```go
package arch

import (
	"fmt"
	"go/types"
	"sort"

	"golang.org/x/tools/go/packages"
)

// CheckDeadConfig verifies every leaf field of the named config struct is
// read at least once outside its defining package. Nested structs declared
// in the same package are recursed into; anything else is a leaf.
func CheckDeadConfig(dir, configPkgPath, configTypeName string) ([]string, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedSyntax | packages.NeedImports | packages.NeedDeps,
		Dir:        dir,
		Tests:      true,
		BuildFlags: []string{"-tags", "integration"},
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}
	if packages.PrintErrors(pkgs) > 0 {
		return nil, fmt.Errorf("arch: packages loaded with errors")
	}

	var configPkg *packages.Package
	packages.Visit(pkgs, func(p *packages.Package) bool {
		if p.PkgPath == configPkgPath && p.Types != nil {
			configPkg = p
		}
		return true
	}, nil)
	if configPkg == nil {
		return nil, fmt.Errorf("arch: config package %s not found under %s", configPkgPath, dir)
	}

	obj := configPkg.Types.Scope().Lookup(configTypeName)
	if obj == nil {
		return nil, fmt.Errorf("arch: type %s not found in %s", configTypeName, configPkgPath)
	}
	st, ok := obj.Type().Underlying().(*types.Struct)
	if !ok {
		return nil, fmt.Errorf("arch: %s is not a struct", configTypeName)
	}

	fields := map[types.Object]string{} // leaf field object -> dotted path
	collectLeafFields(st, configTypeName, configPkg.Types, fields)

	used := map[types.Object]bool{}
	packages.Visit(pkgs, func(p *packages.Package) bool {
		if p.PkgPath == configPkgPath || p.TypesInfo == nil {
			return true
		}
		for _, o := range p.TypesInfo.Uses {
			if _, tracked := fields[o]; tracked {
				used[o] = true
			}
		}
		return true
	}, nil)

	var out []string
	for o, path := range fields {
		if !used[o] {
			out = append(out, fmt.Sprintf("config field %s has no read site outside the config package (no dead config)", path))
		}
	}
	sort.Strings(out)
	return out, nil
}

func collectLeafFields(st *types.Struct, prefix string, home *types.Package, into map[types.Object]string) {
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		path := prefix + "." + f.Name()
		if named, ok := f.Type().(*types.Named); ok && named.Obj().Pkg() == home {
			if nested, ok := named.Underlying().(*types.Struct); ok {
				collectLeafFields(nested, path, home, into)
				continue
			}
		}
		into[f] = path
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/arch/`
Expected: PASS — the fixture's `Unused` is flagged; the live config is clean (Addr, ShutdownTimeout, URL, Level all have read sites from Tasks 5–6).

If the live test flags a genuinely-read field, the bug is in the checker — fix the checker, never silence the field.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/arch
git commit -m "test(backend): dead-config checker requiring a read site for every config field"
```

---

### Task 10: golangci-lint config, gofumpt, tool pinning

**Files:**
- Create: `backend/.golangci.yml`
- Modify: `Taskfile.yml`
- Modify: (any files the first full lint run flags)

**Interfaces:**
- Consumes: the whole tree.
- Produces: `task backend:lint`, `task backend:fmt`, `task backend:fmt:check`, `task backend:vet`, `task backend:vuln`, `task tools` (repo-local pinned installs in `backend/bin/`). This is the spec's "golangci-lint config detail" pin.

- [ ] **Step 1: Write the config**

`backend/.golangci.yml`:

```yaml
# caesar house lint (Plan 01 pin). Broad set, justified disables only.
# House law beyond lint lives in internal/analyzers and internal/arch.
run:
  timeout: 5m
  build-tags:
    - integration

linters:
  disable-all: true
  enable:
    - errcheck
    - govet
    - staticcheck
    - unused
    - gosimple
    - ineffassign
    - errorlint      # %w-aware error comparisons
    - bodyclose
    - noctx          # outbound requests must carry a context
    - rowserrcheck
    - sqlclosecheck
    - copyloopvar
    - gocritic
    - misspell
    - unconvert
    - wastedassign
    - nilerr
    - gosec
    - gofumpt
    - goimports
    - depguard
    - forbidigo
    - revive

linters-settings:
  goimports:
    local-prefixes: github.com/giiorg/caesar/backend
  gosec:
    excludes:
      - G104 # duplicate of errcheck
  depguard:
    # Fast-feedback mirror of the canonical deny-list in internal/arch/rules.go
    # (arch test is authoritative; keep both in sync when the list changes).
    rules:
      main:
        files:
          - $all
        deny:
          - pkg: github.com/stretchr/testify
            desc: stdlib asserts + go-cmp only
          - pkg: gorm.io
            desc: no ORMs
          - pkg: entgo.io
            desc: no ORMs
          - pkg: go.uber.org/zap
            desc: log/slog only
          - pkg: github.com/sirupsen/logrus
            desc: log/slog only
          - pkg: github.com/rs/zerolog
            desc: log/slog only
          - pkg: github.com/spf13/cobra
            desc: stdlib flag only
          - pkg: github.com/spf13/viper
            desc: strict JSON config only
          - pkg: github.com/pkg/errors
            desc: stdlib errors + %w
  forbidigo:
    forbid:
      - p: ^fmt\.Print(f|ln)?$
        msg: write to an injected io.Writer or use slog
  revive:
    rules:
      - name: blank-imports
      - name: context-as-argument
      - name: error-return
      - name: error-naming
      - name: increment-decrement
      - name: receiver-naming
      - name: time-naming
      - name: var-declaration
      - name: range

issues:
  exclude-rules:
    - path: _test\.go
      linters:
        - gosec
        - noctx
        - bodyclose
        - forbidigo
```

- [ ] **Step 2: Tool install + lint/fmt targets**

Add to `Taskfile.yml` `tasks:` (note `GOBIN` makes installs repo-local and pinned):

```yaml
  tools:
    dir: backend
    env:
      GOBIN: '{{.ROOT_DIR}}/backend/bin'
    status:
      # Version-aware: a pin bump makes these fail and reinstalls.
      - bin/golangci-lint version 2>/dev/null | grep -q {{.GOLANGCI_LINT_VERSION}}
      - bin/gofumpt --version 2>/dev/null | grep -q {{.GOFUMPT_VERSION}}
      - bin/govulncheck -version 2>/dev/null | grep -q govulncheck@{{.GOVULNCHECK_VERSION}}
    cmds:
      - go install github.com/golangci/golangci-lint/cmd/golangci-lint@{{.GOLANGCI_LINT_VERSION}}
      - go install mvdan.cc/gofumpt@{{.GOFUMPT_VERSION}}
      - go install golang.org/x/vuln/cmd/govulncheck@{{.GOVULNCHECK_VERSION}}

  backend:fmt:
    deps: [tools]
    dir: backend
    cmds:
      - bin/gofumpt -w .

  backend:fmt:check:
    deps: [tools]
    dir: backend
    cmds:
      - test -z "$(bin/gofumpt -l .)" || (bin/gofumpt -l . && exit 1)

  backend:vet:
    dir: backend
    cmds:
      - go vet -tags integration ./...

  backend:lint:
    deps: [tools]
    dir: backend
    cmds:
      - bin/golangci-lint run

  backend:vuln:
    deps: [tools]
    dir: backend
    cmds:
      - bin/govulncheck -tags integration ./...
```

- [ ] **Step 3: Run and fix everything it finds**

Run: `task tools && task backend:fmt && task backend:vet && task backend:lint && task backend:vuln`
Expected: after `gofumpt -w` reformats, lint may flag real issues in Tasks 1–9 code (unchecked errors, shadowed vars). Fix each finding properly — no `//nolint` without a one-line justification comment. Re-run until clean.

Run: `task backend:test && task backend:test:integration`
Expected: PASS (fixes didn't break behavior)

- [ ] **Step 4: Commit**

```bash
git add backend/.golangci.yml Taskfile.yml backend/
git commit -m "chore(backend): pin golangci-lint config and repo-local tool installs"
```

---

### Task 11: Secret scanning (gitleaks)

**Files:**
- Create: `.gitleaks.toml`
- Modify: `Taskfile.yml`

**Interfaces:**
- Consumes: full git history.
- Produces: `task secrets` — pinned gitleaks over the repo's git history; CI job calls exactly this target.

- [ ] **Step 1: Config**

`.gitleaks.toml`:

```toml
[extend]
useDefault = true

[allowlist]
paths = [
  '''backend/internal/.*/testdata/.*''',
  '''backend/caesar\.dev\.json''',
]
```

(dev-compose credentials `caesar:caesar` are local-only by construction; testdata will hold deliberately fake tokens from Plan 6's staging probes onward.)

- [ ] **Step 2: Task target**

Add to `Taskfile.yml` `tasks:`:

```yaml
  secrets:
    cmds:
      - docker run --rm -u "$(id -u):$(id -g)" -v "{{.ROOT_DIR}}:/repo" {{.GITLEAKS_IMAGE}} git /repo --config /repo/.gitleaks.toml --verbose
```

If the containerized `git` mode fights repo-ownership checks in some environment, the documented fallback is `... {{.GITLEAKS_IMAGE}} dir /repo --config ...` (working-tree scan) — prefer `git` mode, it scans history.

- [ ] **Step 3: Run**

Run: `task secrets`
Expected: `no leaks found`. If it flags something real: rotate it, purge it, then re-run — never allowlist a live secret.

- [ ] **Step 4: Commit**

```bash
git add .gitleaks.toml Taskfile.yml
git commit -m "chore(repo): pinned gitleaks scan with task parity"
```

---

### Task 12: Dockerfile

**Files:**
- Create: `Dockerfile`
- Create: `.dockerignore`

**Interfaces:**
- Consumes: `backend/` tree, `buildinfo` ldflags contract from Task 1.
- Produces: a distroless, nonroot caesar image built from the repo root; `task image:build`. Plan 6 revisits the runtime layer when the staging/extraction toolchain needs git in-image; Plan 11 adds the embedded web build stage; Plan 14 consumes the pushed digest.

- [ ] **Step 1: Write it**

`Dockerfile` (repo root — monorepo context so later stages can add `/web`):

```dockerfile
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w \
      -X github.com/giiorg/caesar/backend/internal/platform/buildinfo.Version=${VERSION} \
      -X github.com/giiorg/caesar/backend/internal/platform/buildinfo.Commit=${COMMIT} \
      -X github.com/giiorg/caesar/backend/internal/platform/buildinfo.Date=${DATE}" \
    -o /out/caesar ./cmd/caesar

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/caesar /usr/local/bin/caesar
ENTRYPOINT ["/usr/local/bin/caesar"]
CMD ["serve", "-config", "/etc/caesar/config.json"]
```

`.dockerignore`:

```
.git
.task
.superpowers
docs
backend/bin
compose.dev.yaml
```

- [ ] **Step 2: Task target + verify**

Add to `Taskfile.yml` `tasks:`:

```yaml
  image:build:
    vars:
      VERSION:
        sh: git describe --tags --always --dirty
      COMMIT:
        sh: git rev-parse --short HEAD
      DATE:
        sh: date -u +%Y-%m-%dT%H:%M:%SZ
    cmds:
      - docker build --build-arg VERSION={{.VERSION}} --build-arg COMMIT={{.COMMIT}} --build-arg DATE={{.DATE}} -t caesar:dev .
```

Run: `task image:build && docker run --rm caesar:dev version`
Expected: version line with the real commit; image runs as nonroot by construction.

- [ ] **Step 3: Commit**

```bash
git add Dockerfile .dockerignore Taskfile.yml
git commit -m "build(repo): distroless nonroot caesar image"
```

---

### Task 13: CI — composite required gate + image publish

**Files:**
- Create: `.github/workflows/ci.yml`
- Create: `.github/workflows/image.yml`
- Modify: `Taskfile.yml`

**Interfaces:**
- Consumes: every `task` target defined so far (CI parity rule: CI runs only `task` targets).
- Produces: required check **`ci-ok`** (composite over backend + secrets + workflow-lint lanes); `task backend:ci` and root `task ci`; digest-pinned image push to `ghcr.io/giiorg/caesar` on `main`. Plan 2 appends the contracts lane (regen-diff) to the same workflow; Plan 11 appends the frontend lane.

- [ ] **Step 1: Composite task targets**

Add to `Taskfile.yml` `tasks:`:

```yaml
  backend:ci:
    cmds:
      - task: backend:fmt:check
      - task: backend:vet
      - task: backend:lint
      - task: backend:analyze
      - task: backend:test
      - task: backend:test:integration
      - task: backend:vuln
      - task: backend:build

  lint:actions:
    cmds:
      - docker run --rm -v "{{.ROOT_DIR}}:/repo" -w /repo {{.ACTIONLINT_IMAGE}} -color

  ci:
    cmds:
      - task: backend:ci
      - task: secrets
      - task: lint:actions
```

- [ ] **Step 2: The workflows**

`.github/workflows/ci.yml`:

```yaml
name: ci

on:
  pull_request:
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  backend:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: backend/go.mod
          cache-dependency-path: backend/go.sum
      - uses: arduino/setup-task@v2
        with:
          version: 3.x
          repo-token: ${{ secrets.GITHUB_TOKEN }}
      - run: task backend:ci

  secrets:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: arduino/setup-task@v2
        with:
          version: 3.x
          repo-token: ${{ secrets.GITHUB_TOKEN }}
      - run: task secrets

  workflow-lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: arduino/setup-task@v2
        with:
          version: 3.x
          repo-token: ${{ secrets.GITHUB_TOKEN }}
      - run: task lint:actions

  ci-ok:
    # The single required status check; branch protection points here.
    needs: [backend, secrets, workflow-lint]
    if: always()
    runs-on: ubuntu-latest
    steps:
      - name: verify all lanes passed
        run: |
          [ "${{ contains(needs.*.result, 'failure') || contains(needs.*.result, 'cancelled') }}" = "false" ]
```

`.github/workflows/image.yml`:

```yaml
name: image

on:
  push:
    branches: [main]

permissions:
  contents: read
  packages: write

jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - id: build
        uses: docker/build-push-action@v6
        with:
          context: .
          push: true
          tags: |
            ghcr.io/giiorg/caesar:main
            ghcr.io/giiorg/caesar:sha-${{ github.sha }}
          build-args: |
            VERSION=sha-${{ github.sha }}
            COMMIT=${{ github.sha }}
            DATE=${{ github.event.head_commit.timestamp }}
      - name: record digest (deploys pin this, never a tag)
        run: echo "**image digest:** \`${{ steps.build.outputs.digest }}\`" >> "$GITHUB_STEP_SUMMARY"
```

- [ ] **Step 3: Lint the workflows, run the full local gate**

Run: `task lint:actions`
Expected: no findings.

Run: `task ci`
Expected: every lane green locally — this is the exact gate CI runs.

- [ ] **Step 4: Commit**

```bash
git add .github Taskfile.yml
git commit -m "ci(repo): composite ci-ok gate and ghcr image publish on main"
```

After pushing (user's call), set the repo's branch protection to require the `ci-ok` check.

---

### Task 14: Backend README, roadmap status, final verification

**Files:**
- Create: `backend/README.md`
- Modify: `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md`

**Interfaces:**
- Consumes: everything above.
- Produces: the standards document future contributors (and Plan 2's implementer) read first; roadmap shows Plan 01 done.

- [ ] **Step 1: Write the README**

`backend/README.md`:

```markdown
# caesar backend

Go control plane. Spec: `../docs/superpowers/specs/2026-08-02-caesar-v1-design.md` · Decisions (LOCKED): `../docs/brainstorm/2026-08-01-decisions.md`.

## Layout law

    cmd/caesar/            thin entry — imports internal/app, nothing else
    internal/app/          composition root: config→logger→pool→migrate→serve wiring, subcommands
    internal/platform/     shared kernel (no business logic): buildinfo, config, httpx, logging, pg, testdb, xerrors
    internal/<context>/    bounded contexts (Plan 2+): peers, never import each other
    internal/arch/         architecture tests — the law, executable
    internal/analyzers/    house-law go/analysis analyzers

Enforced mechanically — `internal/arch` (import graph, deny-list, dead config)
and `internal/analyzers` (env access only in platform/config; stubs return
xerrors.NotImplemented; no tx types in exported context APIs). Violations fail
CI. Fix the code, never the rule.

## Dev loop

    task db:up                     # dev postgres (compose.dev.yaml)
    task backend:run               # serve with caesar.dev.json
    task backend:test              # unit, race
    task backend:test:integration  # real postgres via testcontainers
    task ci                        # exactly what CI runs

## Migrations

Desired state lives in `internal/platform/pg/schema.sql`. Author with
`task db:new -- <name>` (Atlas diff → goose files in
`internal/platform/pg/migrations/`). Runtime: embedded goose under a Postgres
advisory lock at startup, or `caesar migrate up|down|status`.

## Conventions

- Strict JSON config; env overrides only DATABASE_URL (+ CAESAR_MASTER_KEY from Plan 3).
- slog JSON to stdout; correlation id on every request and log line.
- TDD; table-driven tests; behavioral names; stdlib asserts + go-cmp.
- Tool versions pinned in the root Taskfile; `task tools` installs to backend/bin.
- Commits: `type(scope): one short sentence`, no body, no AI attribution.
```

- [ ] **Step 2: Flip the roadmap row**

In `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md`, change Plan 01's status cell from `**written** — 2026-08-02-caesar-01-foundations.md` to `**done** — 2026-08-02-caesar-01-foundations.md`.

- [ ] **Step 3: Full-gate verification**

Run: `task ci`
Expected: green end to end. Also verify the walking skeleton by hand once: `task db:up && task backend:run` + `curl -s localhost:8080/healthz` → `{"status":"ok","db":"ok",...}`.

- [ ] **Step 4: Commit**

```bash
git add backend/README.md docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md
git commit -m "docs(backend): standards readme and roadmap status for plan 01"
```

---

## Execution notes

- **Order is dependency order** — do not reorder: 4 needs 2's config shape only conceptually, but 5 needs 4's testdb, 6 needs 2/3/4/5, 9's live test needs 5/6's read sites, 10 sweeps everything, 13 needs every task target.
- **Docker must be running** for Tasks 4–6 integration tests, 11, 12, and `task ci`.
- **If a pinned tool or action version is unavailable at execution time** (yanked tag, registry move), pick the nearest newer patch release, update the pin in `Taskfile.yml`/workflow in the same commit, and note it in the commit message — never float to `latest`.
- **goose/testcontainers API drift**: the code above targets goose ≥ v3.24 (`UpContext`/`DownContext`/`StatusContext`, `SetBaseFS`) and testcontainers-go ≥ v0.35 (`postgres.Run`). If signatures differ at resolved versions, adapt call sites minimally — the behavior contract (advisory lock, empty-set no-op, template clone isolation) is what the tests pin.
- **What this plan deliberately does not build** (later plans own them): sqlc + first schema (2), OpenAPI/TS contract pipeline (2), clock seam (2), `CAESAR_MASTER_KEY` (3), `/metrics` (14), prod compose + cloudflared + backups (14), `rapid`/fuzz suites (4+), `caesar egress-proxy` (6).
