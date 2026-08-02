# caesar Plan 02 — API, Auth & RBAC Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The first real domain slice: users, sessions, and the grant ladder in Postgres behind a `/api/v1` surface with the problem+json envelope, argon2id + `__Host-` cookie auth with DB-backed login rate limiting, `caesar admin bootstrap`, append-only audit — and the contract pipeline (Go types → OpenAPI 3.1 → TS types) plus sqlc, all gated by regen-diff in CI.

**Architecture:** `internal/identity` is the first bounded context (peer of `platform/` and `app/` per the layout law): domain types + service + sqlc-generated store + HTTP handlers. Cross-cutting machinery lands in the shared kernel: `platform/clock` (injected clock seam), `platform/audit` (append-only recorder callable inside any context's transaction), `platform/xerrors` grows the error-class axis, `platform/httpx` grows the problem+json writer, Origin-check middleware, and the `Operation` route table that is the single source for both mux registration and OpenAPI generation. `internal/contracts` (tooling, not a context) reflects the route table into a committed OpenAPI 3.1 document; `openapi-typescript` turns that into committed TS types under `/web`. Transactions never leave the identity service (txboundary analyzer enforces).

**Tech Stack:** Everything from Plan 01, plus: sqlc v1.31.1 (generated pgx/v5 stores) · `golang.org/x/crypto/argon2` · `github.com/google/uuid` (promoted to direct) · `github.com/swaggest/openapi-go` (OpenAPI 3.1 reflection) · Node ≥ 20 + `openapi-typescript` 7.13.0 (TS types generation).

## Global Constraints

Everything in Plan 01's Global Constraints still applies verbatim (module path, layout law, deny-list, strict JSON config, env access only in `platform/config`, stubs return `NotImplemented`, slog JSON, TDD + table-driven + behavioral names, Taskfile parity, commit style — one short sentence, **no AI attribution of any kind**). New pins for this plan:

- **New tool pins:** sqlc `v1.31.1` (installed to `backend/bin` via `task tools`) · `openapi-typescript` `7.13.0` (npm, exact) · Node `>= 20` (local dev has 20.9; CI pins 20). New Go deps resolve latest at execution with floors: `swaggest/openapi-go` ≥ v0.2.61, `google/uuid` ≥ v1.6.0, `x/crypto` ≥ v0.54.0.
- **argon2id parameters (pinned, OWASP baseline):** memory 19456 KiB, time 2, parallelism 1, salt 16 bytes, key 32 bytes. Stored in PHC string form `$argon2id$v=19$m=19456,t=2,p=1$<b64salt>$<b64key>` so parameters can be raised later without invalidating old hashes.
- **Password rules (pinned):** minimum 12 characters, maximum 512. No composition rules.
- **Session cookie (pinned):** name `__Host-caesar_session`; `Path=/; Secure; HttpOnly; SameSite=Lax`. Token = 32 random bytes, base64url (no padding) in the cookie; only its SHA-256 lands in the database.
- **Session TTLs (pinned defaults, config-overridable):** idle 14 days (`"336h"`), absolute 90 days (`"2160h"`). `last_seen_at` refreshed at most every 5 minutes.
- **Login rate limits (pinned — the roadmap left the parameters to this plan):** per-account: 5 consecutive failures, then lock `30s × 2^(n−5)` capped at 15 minutes; reset on success. Per-IP: fixed 15-minute window, 30 failures locks the IP for 15 minutes. Both live in `login_rate` (Postgres — restart-proof, multi-process-safe). IP = `r.RemoteAddr` host in V1 (tunnel-header trust is a Plan 14 deployment decision).
- **API surface:** everything under `/api/v1`; every non-2xx response is RFC 9457 `application/problem+json` with `type`, `title`, `status`, `detail`, `correlation_id`, `errors[]`.
- **RBAC (spec §6):** global roles `admin`/`member`; per-project ladder `view ⊂ run ⊂ approve ⊂ manage`, one grant row per (user, project), checks are `grant >= level`; global admin passes every project check. Grant *write* endpoints land in Plan 03 with the projects API — this plan ships the schema, the ladder, `Authorize`, and grants in `/auth/me`.
- **Sessions status model (the roadmap's "status enums + transition tables: 02 (sessions)"):** state is derived, never stored: `active` (revoked_at NULL ∧ now < expires_at ∧ now < last_seen_at + idle TTL) → `revoked` (revocation sets `revoked_at`, terminal) or `expired` (idle or absolute, terminal by clock). There is no resurrection path; re-auth is always a new row. Users carry no status enum — `active` is a flag (spec §2).
- **Audit actions (pinned vocabulary for this plan):** `user.bootstrapped`, `user.created`, `user.updated`, `auth.login`, `auth.login_failed`, `auth.rate_limited`, `auth.logout`, `auth.password_changed`. Every entry records actor (NULL = system/bootstrap), subject kind + id, correlation id, jsonb detail.
- **Generated code is never hand-edited:** sqlc output, `backend/api/openapi.json`, `web/src/api/schema.d.ts` are all reproduced by `task` targets and gated by `task contracts:check` (regen + `git diff --exit-code`).
- All `go` / `task` commands run from the repo root unless the step says otherwise.

---

### Task 1: Gate the image publish behind the CI gate

Plan 01's `image.yml` publishes to ghcr on every push to `main`, ungated — the final review flagged it. Fold the publish job into `ci.yml` behind `ci-ok` and delete `image.yml`.

**Files:**
- Modify: `.github/workflows/ci.yml`
- Delete: `.github/workflows/image.yml`

**Interfaces:**
- Consumes: the existing `ci-ok` composite job in `ci.yml`.
- Produces: a `publish` job that runs only on `main` pushes after `ci-ok` succeeds. Later plans keep extending `ci.yml`; `image.yml` no longer exists.

- [ ] **Step 1: Move the publish job into ci.yml**

Append to `.github/workflows/ci.yml` (job level, after `ci-ok`). This is the exact job body from `image.yml` with three changes: `needs: [ci-ok]`, a `main`-push-only `if`, and job-level permissions (the workflow keeps `contents: read` at the top; `packages: write` moves to this job only):

```yaml
  publish:
    needs: [ci-ok]
    if: github.event_name == 'push' && github.ref == 'refs/heads/main'
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
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

Then delete `.github/workflows/image.yml`.

Note: `ci-ok` itself is unchanged — it stays the single required check and must NOT list `publish` in its `needs` (publish depends on ci-ok, not the reverse, and it is skipped on PRs).

- [ ] **Step 2: Lint the workflows**

Run: `task lint:actions`
Expected: exit 0, no findings.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git rm .github/workflows/image.yml
git commit -m "ci(repo): gate ghcr publish behind the ci-ok composite check"
```

---

### Task 2: Strengthen the dead-config checker

Deferred ruling from Plan 01's final review, accepted: `CheckDeadConfig` currently counts *any* `TypesInfo.Uses` occurrence as a read site — including assignment LHS (`cfg.Server.Addr = x`), composite-literal keys (`config.Config{Server: …}`), and uses inside `_test.go` files. Plan 02's own tests will construct `Config` literals, which would quietly defang the check. Tighten it: only **selector reads in non-test code** count.

**Files:**
- Modify: `backend/internal/arch/deadconfig.go`
- Modify: `backend/internal/arch/testdata/deadconfig-module/config/config.go`
- Modify: `backend/internal/arch/testdata/deadconfig-module/consumer/consumer.go`
- Test: `backend/internal/arch/deadconfig_test.go` (exists — extend)

**Interfaces:**
- Consumes: `CheckDeadConfig(dir, configPkgPath, configTypeName string) ([]string, error)` — signature unchanged.
- Produces: same signature, stricter semantics. The live test in `arch_live_test.go` (or wherever Plan 01 put the module-level invocation — find it with `grep -rn CheckDeadConfig backend/internal/arch`) keeps passing because every current `Config` field has a real non-test read site (`serve.go`, `migrate.go`).

- [ ] **Step 1: Extend the testdata module with decoy fields**

Open the existing testdata module and read it first — it compiles as a real module via `packages.Load`. Extend `backend/internal/arch/testdata/deadconfig-module/config/config.go` so the struct contains, alongside whatever live/dead fields Plan 01 put there, three new decoys:

```go
	// OnlyAssigned is written by the consumer but never read: dead.
	OnlyAssigned string
	// OnlyLiteralKey appears as a composite-literal key in the consumer: dead.
	OnlyLiteralKey string
	// OnlyTestRead is read solely from a _test.go file in the consumer: dead.
	OnlyTestRead string
```

In `consumer/consumer.go` add exactly these use shapes:

```go
// Touch exercises non-read use shapes the checker must NOT count.
func Touch(c *config.Config) config.Config {
	c.OnlyAssigned = "written, never read"
	return config.Config{OnlyLiteralKey: "key position, not a read"}
}
```

And a new file `consumer/consumer_test.go` in the testdata module:

```go
package consumer

import "deadconfig-module/config"

// testRead reads OnlyTestRead — but from a test file, so it must not count.
func testRead(c config.Config) string { return c.OnlyTestRead }
```

(Adjust the import path to the testdata module's actual module name — read its `go.mod`.)

- [ ] **Step 2: Extend the checker test to expect the decoys reported**

In `deadconfig_test.go`, extend the expected-findings assertion so `Config.OnlyAssigned`, `Config.OnlyLiteralKey`, and `Config.OnlyTestRead` are all expected in the returned findings (alongside Plan 01's existing dead-field expectation), and the pre-existing live field is still absent.

- [ ] **Step 3: Run to verify it fails**

Run: `cd backend && go test ./internal/arch/ -run TestCheckDeadConfig -v`
Expected: FAIL — the current checker counts all three decoys as "used", so they are missing from the findings.

- [ ] **Step 4: Tighten the checker**

In `deadconfig.go`, replace the blanket `TypesInfo.Uses` scan (the `packages.Visit` block that fills `used`) with a syntax-aware scan:

```go
	used := map[types.Object]bool{}
	packages.Visit(pkgs, func(p *packages.Package) bool {
		// Skip the config package itself, packages without syntax, and
		// test variants: with Tests:true the plain package (ID == PkgPath)
		// excludes _test.go files, so restricting to it excludes test-only
		// reads wholesale ("pkg [pkg.test]" and "pkg_test" IDs differ).
		if p.PkgPath == configPkgPath || p.TypesInfo == nil || p.ID != p.PkgPath {
			return true
		}
		for _, file := range p.Syntax {
			nonReads := nonReadSelections(file)
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				obj := p.TypesInfo.Uses[sel.Sel]
				if obj == nil || nonReads[sel.Sel] {
					return true
				}
				if _, tracked := fields[obj]; tracked {
					used[obj] = true
				}
				return true
			})
		}
		return true
	}, nil)
```

And add the helper (same file):

```go
// nonReadSelections collects selector identifiers that appear in non-read
// positions: assignment left-hand sides and composite-literal keys. A field
// that only ever appears there has no read site (no dead config).
func nonReadSelections(file *ast.File) map[*ast.Ident]bool {
	out := map[*ast.Ident]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok {
					out[sel.Sel] = true
				}
			}
		case *ast.CompositeLit:
			for _, elt := range node.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					if key, ok := kv.Key.(*ast.Ident); ok {
						out[key] = true
					}
				}
			}
		}
		return true
	})
	return out
}
```

Add `"go/ast"` to the imports. Note `nonReads` marks the *identifier node*, not the object — the same field read elsewhere in a genuine rvalue position still counts (each occurrence is a distinct `*ast.Ident`).

- [ ] **Step 5: Run to verify it passes**

Run: `cd backend && go test ./internal/arch/ -run TestCheckDeadConfig -v`
Expected: PASS.

- [ ] **Step 6: Run the live module check**

Run: `cd backend && go test ./internal/arch/ -v`
Expected: PASS — all current config fields have real non-test read sites. If a field now reports dead, that is a genuine finding: fix the code (or the field), never the checker.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/arch
git commit -m "test(backend): dead-config checker counts only non-test selector reads"
```

---

### Task 3: Injected clock seam

The roadmap pins the clock seam to this plan; first consumers are session expiry and login rate limiting. One tiny package, no magic.

**Files:**
- Create: `backend/internal/platform/clock/clock.go`
- Test: `backend/internal/platform/clock/clock_test.go`

**Interfaces:**
- Produces (later tasks depend on these exact names):
  - `type Clock interface { Now() time.Time }`
  - `clock.System{}` — the production implementation.
  - `clock.NewFake(t0 time.Time) *Fake` with `Now() time.Time`, `Advance(d time.Duration)`, `Set(t time.Time)` — safe for concurrent use.

- [ ] **Step 1: Write the failing test**

`backend/internal/platform/clock/clock_test.go`:

```go
package clock_test

import (
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/platform/clock"
)

func TestSystem_TracksRealTime(t *testing.T) {
	before := time.Now()
	got := clock.System{}.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("System.Now() = %v, want between %v and %v", got, before, after)
	}
}

func TestFake_AdvanceAndSet(t *testing.T) {
	t0 := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	fake := clock.NewFake(t0)
	if got := fake.Now(); !got.Equal(t0) {
		t.Fatalf("Now() = %v, want %v", got, t0)
	}
	fake.Advance(90 * time.Minute)
	if got, want := fake.Now(), t0.Add(90*time.Minute); !got.Equal(want) {
		t.Fatalf("after Advance: Now() = %v, want %v", got, want)
	}
	t1 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	fake.Set(t1)
	if got := fake.Now(); !got.Equal(t1) {
		t.Fatalf("after Set: Now() = %v, want %v", got, t1)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/platform/clock/`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

`backend/internal/platform/clock/clock.go`:

```go
// Package clock is the injected time seam (decisions §9): production code
// takes a Clock, tests take a Fake. First consumers: session expiry and
// login rate limiting (identity).
package clock

import (
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

// System is the production clock.
type System struct{}

func (System) Now() time.Time { return time.Now() }

// Fake is a manually-advanced clock for tests. Safe for concurrent use.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

func NewFake(t0 time.Time) *Fake { return &Fake{now: t0} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && go test ./internal/platform/clock/ -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/platform/clock
git commit -m "feat(backend): injected clock seam"
```

---
### Task 4: Error classes and the problem+json envelope

Spec §4: two-axis errors internally, one stable RFC 9457 envelope at the API. This task ships the class axis in `platform/xerrors` and the envelope writer in `platform/httpx`. (The retry-effect axis arrives with typed outcomes in Plan 05 — do not add it now.)

**Files:**
- Modify: `backend/internal/platform/xerrors/xerrors.go`
- Create: `backend/internal/platform/httpx/problem.go`
- Test: `backend/internal/platform/xerrors/xerrors_test.go`
- Test: `backend/internal/platform/httpx/problem_test.go`

**Interfaces:**
- Produces (exact — later tasks import these):
  - `xerrors.Class` with constants `ClassInternal` (zero value), `ClassInvalid`, `ClassUnauthenticated`, `ClassForbidden`, `ClassNotFound`, `ClassConflict`, `ClassRateLimited`.
  - `xerrors.FieldError{Field, Message string}` (json tags `field`, `message`).
  - `xerrors.E(c Class, msg string) *Error` · `xerrors.Wrap(c Class, msg string, err error) *Error` · `(*Error).WithFields(...FieldError) *Error` · `(*Error).Error()` · `(*Error).Unwrap()`.
  - `xerrors.ClassOf(err error) Class` — `errors.As` walk, default `ClassInternal`.
  - `httpx.Problem(w http.ResponseWriter, r *http.Request, err error)` — writes the envelope.
  - `httpx.ProblemDetails` — the envelope struct (exported: OpenAPI generation reflects it in Task 13).
  - `httpx.JSON(w http.ResponseWriter, status int, v any)` — success-path JSON writer.

- [ ] **Step 1: Write the failing xerrors test**

`backend/internal/platform/xerrors/xerrors_test.go`:

```go
package xerrors_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func TestClassOf_TableDriven(t *testing.T) {
	wrapped := xerrors.Wrap(xerrors.ClassConflict, "already exists", errors.New("duplicate key"))
	tests := []struct {
		name string
		err  error
		want xerrors.Class
	}{
		{"typed error", xerrors.E(xerrors.ClassNotFound, "no such user"), xerrors.ClassNotFound},
		{"wrapped typed error", fmt.Errorf("outer: %w", wrapped), xerrors.ClassConflict},
		{"plain error defaults to internal", errors.New("boom"), xerrors.ClassInternal},
		{"not-implemented sentinel defaults to internal", xerrors.NotImplemented("thing"), xerrors.ClassInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := xerrors.ClassOf(tt.err); got != tt.want {
				t.Fatalf("ClassOf() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestError_MessageAndUnwrap(t *testing.T) {
	cause := errors.New("pq: duplicate key")
	err := xerrors.Wrap(xerrors.ClassConflict, "email already registered", cause)
	if got := err.Error(); got != "email already registered: pq: duplicate key" {
		t.Fatalf("Error() = %q", got)
	}
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is should reach the wrapped cause")
	}
	if xerrors.E(xerrors.ClassInvalid, "bad").Error() != "bad" {
		t.Fatal("unwrapped Error() should be the bare message")
	}
}

func TestWithFields_CarriesFieldErrors(t *testing.T) {
	err := xerrors.E(xerrors.ClassInvalid, "validation failed").
		WithFields(xerrors.FieldError{Field: "email", Message: "not a valid address"})
	if len(err.Fields) != 1 || err.Fields[0].Field != "email" {
		t.Fatalf("Fields = %+v", err.Fields)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/platform/xerrors/`
Expected: FAIL — `Class`, `E`, `Wrap`, `ClassOf` undefined.

- [ ] **Step 3: Extend xerrors**

Append to `backend/internal/platform/xerrors/xerrors.go` (keep the existing `ErrNotImplemented`/`NotImplemented` exactly as they are):

```go
// Class is the internal error taxonomy's first axis (spec §4). The zero
// value is ClassInternal: an unclassified error is always treated as an
// internal fault, never leaked to clients (default-deny).
type Class int

const (
	ClassInternal Class = iota
	ClassInvalid
	ClassUnauthenticated
	ClassForbidden
	ClassNotFound
	ClassConflict
	ClassRateLimited
)

// FieldError is one field-level validation failure, surfaced verbatim in
// the API problem envelope.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error is a classified error. Msg is safe for API clients; Err is the
// internal cause and never crosses the API boundary.
type Error struct {
	Class  Class
	Msg    string
	Fields []FieldError
	Err    error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Msg + ": " + e.Err.Error()
	}
	return e.Msg
}

func (e *Error) Unwrap() error { return e.Err }

// WithFields returns e with field errors attached (builder style).
func (e *Error) WithFields(fields ...FieldError) *Error {
	e.Fields = append(e.Fields, fields...)
	return e
}

func E(c Class, msg string) *Error { return &Error{Class: c, Msg: msg} }

func Wrap(c Class, msg string, err error) *Error { return &Error{Class: c, Msg: msg, Err: err} }

// ClassOf walks the error chain for a classified error; anything else is
// internal.
func ClassOf(err error) Class {
	var e *Error
	if errors.As(err, &e) {
		return e.Class
	}
	return ClassInternal
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && go test ./internal/platform/xerrors/`
Expected: PASS.

- [ ] **Step 5: Write the failing problem-writer test**

`backend/internal/platform/httpx/problem_test.go`:

```go
package httpx_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func problemFor(t *testing.T, err error) (int, httpx.ProblemDetails, http.Header) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/whatever", nil)
	// Run through the correlation middleware so the id is in context.
	httpx.WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.Problem(w, r, err)
	})).ServeHTTP(rec, req)
	var pd httpx.ProblemDetails
	if err := json.Unmarshal(rec.Body.Bytes(), &pd); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.Body.String())
	}
	return rec.Code, pd, rec.Header()
}

func TestProblem_MapsClassesToStatuses(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantType   string
	}{
		{"invalid", xerrors.E(xerrors.ClassInvalid, "bad input"), http.StatusBadRequest, "/errors/invalid"},
		{"unauthenticated", xerrors.E(xerrors.ClassUnauthenticated, "who are you"), http.StatusUnauthorized, "/errors/unauthenticated"},
		{"forbidden", xerrors.E(xerrors.ClassForbidden, "no"), http.StatusForbidden, "/errors/forbidden"},
		{"not found", xerrors.E(xerrors.ClassNotFound, "gone"), http.StatusNotFound, "/errors/not-found"},
		{"conflict", xerrors.E(xerrors.ClassConflict, "clash"), http.StatusConflict, "/errors/conflict"},
		{"rate limited", xerrors.E(xerrors.ClassRateLimited, "slow down"), http.StatusTooManyRequests, "/errors/rate-limited"},
		{"internal", errors.New("pq: connection refused"), http.StatusInternalServerError, "/errors/internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, pd, hdr := problemFor(t, tt.err)
			if status != tt.wantStatus {
				t.Fatalf("status = %d, want %d", status, tt.wantStatus)
			}
			if pd.Type != tt.wantType {
				t.Fatalf("type = %q, want %q", pd.Type, tt.wantType)
			}
			if pd.Status != tt.wantStatus {
				t.Fatalf("body status = %d, want %d", pd.Status, tt.wantStatus)
			}
			if got := hdr.Get("Content-Type"); got != "application/problem+json" {
				t.Fatalf("Content-Type = %q", got)
			}
			if pd.CorrelationID == "" {
				t.Fatal("correlation_id must be present")
			}
		})
	}
}

func TestProblem_NeverLeaksInternalDetail(t *testing.T) {
	_, pd, _ := problemFor(t, errors.New("pq: password authentication failed for user caesar"))
	if pd.Detail != "internal error" {
		t.Fatalf("internal detail leaked: %q", pd.Detail)
	}
}

func TestProblem_CarriesFieldErrors(t *testing.T) {
	err := xerrors.E(xerrors.ClassInvalid, "validation failed").
		WithFields(xerrors.FieldError{Field: "email", Message: "not a valid address"})
	_, pd, _ := problemFor(t, err)
	want := []xerrors.FieldError{{Field: "email", Message: "not a valid address"}}
	if diff := cmp.Diff(want, pd.Errors); diff != "" {
		t.Fatalf("errors mismatch (-want +got):\n%s", diff)
	}
	if pd.Detail != "validation failed" {
		t.Fatalf("detail = %q", pd.Detail)
	}
}
```

- [ ] **Step 6: Run to verify it fails**

Run: `cd backend && go test ./internal/platform/httpx/`
Expected: FAIL — `Problem`, `ProblemDetails` undefined.

- [ ] **Step 7: Implement the problem writer**

`backend/internal/platform/httpx/problem.go`:

```go
package httpx

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// ProblemDetails is caesar's RFC 9457 envelope — the single error shape
// every /api/v1 endpoint returns (spec §4).
type ProblemDetails struct {
	Type          string               `json:"type"`
	Title         string               `json:"title"`
	Status        int                  `json:"status"`
	Detail        string               `json:"detail,omitempty"`
	CorrelationID string               `json:"correlation_id,omitempty"`
	Errors        []xerrors.FieldError `json:"errors,omitempty"`
}

type problemShape struct {
	status int
	slug   string
	title  string
}

var problemShapes = map[xerrors.Class]problemShape{
	xerrors.ClassInvalid:         {http.StatusBadRequest, "invalid", "Invalid request"},
	xerrors.ClassUnauthenticated: {http.StatusUnauthorized, "unauthenticated", "Authentication required"},
	xerrors.ClassForbidden:       {http.StatusForbidden, "forbidden", "Forbidden"},
	xerrors.ClassNotFound:        {http.StatusNotFound, "not-found", "Not found"},
	xerrors.ClassConflict:        {http.StatusConflict, "conflict", "Conflict"},
	xerrors.ClassRateLimited:     {http.StatusTooManyRequests, "rate-limited", "Rate limited"},
	xerrors.ClassInternal:        {http.StatusInternalServerError, "internal", "Internal error"},
}

// Problem writes err as a problem+json response. Unclassified errors are
// internal by default and their detail is never leaked (default-deny).
func Problem(w http.ResponseWriter, r *http.Request, err error) {
	class := xerrors.ClassOf(err)
	shape := problemShapes[class]

	pd := ProblemDetails{
		Type:          "/errors/" + shape.slug,
		Title:         shape.title,
		Status:        shape.status,
		Detail:        "internal error",
		CorrelationID: CorrelationID(r.Context()),
	}
	var xe *xerrors.Error
	if class != xerrors.ClassInternal && errors.As(err, &xe) {
		pd.Detail = xe.Msg
		pd.Errors = xe.Fields
	}

	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(shape.status)
	_ = json.NewEncoder(w).Encode(pd)
}

// JSON writes the success-path response body.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
```

- [ ] **Step 8: Run to verify it passes**

Run: `cd backend && go test ./internal/platform/httpx/ ./internal/platform/xerrors/ -race`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add backend/internal/platform/xerrors backend/internal/platform/httpx
git commit -m "feat(backend): error classes and problem+json envelope"
```

---

### Task 5: Identity schema, first migration, sqlc machinery

The first real schema: `users`, `sessions`, `projects` (minimal — Plan 03 owns the projects API; grants need the FK now), `grants`, `audit_entries`, `login_rate`. Atlas authors the goose migration from `schema.sql`; sqlc generates the typed store. Postgres 18's built-in `uuidv7()` provides time-ordered ids.

**Files:**
- Modify: `backend/internal/platform/pg/schema.sql`
- Create: `backend/internal/platform/pg/migrations/<timestamp>_identity_foundations.sql` (generated by Atlas — do not hand-write)
- Create: `backend/sqlc.yaml`
- Create: `backend/internal/identity/store/queries.sql`
- Create: `backend/internal/identity/store/*.go` (generated by sqlc — do not hand-write)
- Modify: `Taskfile.yml` (sqlc pin + `db:sqlc` target)
- Test: `backend/internal/platform/pg/migrate_updown_integration_test.go`

**Interfaces:**
- Consumes: `testdb.NewURL(t)`, `pg.Migrate`/`pg.MigrateDown`/`pg.Migrations()` from Plan 01.
- Produces:
  - The six tables below, live in every testdb clone (testdb migrates its template automatically — no harness change needed).
  - `store.New(db store.DBTX) *store.Queries` with user queries: `CreateUser`, `GetUserByEmail`, `GetUserByID`, `ListUsers`, `UpdateUserRoleActive`, `UpdateUserPassword`, `CountAdmins` (exact signatures listed in Step 6).
  - `task db:sqlc` — regenerates all sqlc output; `task tools` now also installs pinned sqlc.
  - Later tasks append queries to `queries.sql` (sessions/grants/rate in Tasks 9–10, audit in Task 6) and rerun `task db:sqlc`.

- [ ] **Step 1: Write the desired-state schema**

Replace the placeholder body of `backend/internal/platform/pg/schema.sql` (keep its header comment) with:

```sql
-- Identity & access (spec §2, §6). Plan 02.

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    email         text NOT NULL,
    password_hash text NOT NULL,
    role          text NOT NULL CHECK (role IN ('admin', 'member')),
    active        boolean NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX users_email_key ON users (lower(email));

-- Server-side sessions: token never stored, only its SHA-256. State is
-- derived (active/revoked/expired) — see the plan's session status model.
CREATE TABLE sessions (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   bytea NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);

-- Minimal projects table: Plan 03 owns the projects API; the grant ladder
-- needs the foreign key today.
CREATE TABLE projects (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    name       text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- One grant row per (user, project); ladder view ⊂ run ⊂ approve ⊂ manage.
CREATE TABLE grants (
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    level      text NOT NULL CHECK (level IN ('view', 'run', 'approve', 'manage')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, project_id)
);

-- Append-only who-did-what (spec §2). Writers pass occurred_at from the
-- injected clock; actor NULL = system (bootstrap).
CREATE TABLE audit_entries (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at    timestamptz NOT NULL,
    actor_user_id  uuid REFERENCES users (id),
    action         text NOT NULL,
    subject_kind   text NOT NULL,
    subject_id     text NOT NULL,
    correlation_id text NOT NULL DEFAULT '',
    detail         jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX audit_entries_occurred_idx ON audit_entries (occurred_at);

-- DB-backed login rate limiting: scope is 'account:<lower(email)>' or
-- 'ip:<host>'. Parameters pinned in the plan header.
CREATE TABLE login_rate (
    scope             text PRIMARY KEY,
    failures          integer NOT NULL DEFAULT 0,
    window_started_at timestamptz NOT NULL,
    locked_until      timestamptz
);
```

- [ ] **Step 2: Author the migration with Atlas**

Run: `task db:new -- identity_foundations`
(Docker must be running — Atlas uses a `docker://postgres/18/dev` dev database.)

Expected: a new file `backend/internal/platform/pg/migrations/<timestamp>_identity_foundations.sql` containing `-- +goose Up` statements equivalent to the schema above and a `-- +goose Down` section dropping the objects. Read the generated file. If Atlas emits no Down section (or an incomplete one), hand-author it — drop order: `login_rate`, `audit_entries`, `grants`, `projects`, `sessions`, `users`. If Atlas also emits an `atlas.sum` file into the migrations dir, delete it (goose is the runtime; the desired-state file is the authoring source of truth).

- [ ] **Step 3: Write the failing up-down integration test**

`backend/internal/platform/pg/migrate_updown_integration_test.go`:

```go
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
```

- [ ] **Step 4: Run the integration tests**

Run: `cd backend && go test -tags integration -count=1 ./internal/platform/pg/ -run TestMigrations_DownUpRoundTrip -v`
Expected: PASS once the migration from Step 2 is correct (this validates the generated Down). If the Down section fails, fix the migration file, not the test.

- [ ] **Step 5: Pin sqlc and add the task targets**

In `Taskfile.yml` `vars:` add:

```yaml
  SQLC_VERSION: v1.31.1
```

In the `tools` task: add a status line and an install command:

```yaml
      - bin/sqlc version 2>/dev/null | grep -q {{.SQLC_VERSION}}
```
```yaml
      - go install github.com/sqlc-dev/sqlc/cmd/sqlc@{{.SQLC_VERSION}}
```

New task (after `db:new`):

```yaml
  db:sqlc:
    desc: "Regenerate sqlc stores from schema.sql + queries.sql"
    deps: [tools]
    dir: backend
    cmds:
      - bin/sqlc generate
```

- [ ] **Step 6: Configure sqlc and write the user queries**

`backend/sqlc.yaml`:

```yaml
version: "2"
sql:
  - engine: postgresql
    schema: internal/platform/pg/schema.sql
    queries: internal/identity/store/queries.sql
    gen:
      go:
        package: store
        out: internal/identity/store
        sql_package: pgx/v5
        omit_unused_structs: true
        emit_json_tags: false
        overrides:
          - db_type: uuid
            go_type:
              import: github.com/google/uuid
              type: UUID
          - db_type: uuid
            nullable: true
            go_type:
              import: github.com/google/uuid
              type: NullUUID
          - db_type: timestamptz
            nullable: true
            go_type:
              import: time
              type: Time
              pointer: true
```

`backend/internal/identity/store/queries.sql`:

```sql
-- Identity store queries. sqlc generates the Go into this package:
-- `task db:sqlc` after any change here or in schema.sql.

-- name: CreateUser :one
INSERT INTO users (email, password_hash, role)
VALUES ($1, $2, $3)
RETURNING id, email, password_hash, role, active, created_at, updated_at;

-- name: GetUserByEmail :one
SELECT id, email, password_hash, role, active, created_at, updated_at
FROM users
WHERE lower(email) = lower($1);

-- name: GetUserByID :one
SELECT id, email, password_hash, role, active, created_at, updated_at
FROM users
WHERE id = $1;

-- name: ListUsers :many
SELECT id, email, password_hash, role, active, created_at, updated_at
FROM users
ORDER BY created_at, id;

-- name: UpdateUserRoleActive :one
UPDATE users
SET role = $2, active = $3, updated_at = $4
WHERE id = $1
RETURNING id, email, password_hash, role, active, created_at, updated_at;

-- name: UpdateUserPassword :exec
UPDATE users
SET password_hash = $2, updated_at = $3
WHERE id = $1;

-- name: CountAdmins :one
SELECT count(*) FROM users WHERE role = 'admin';
```

- [ ] **Step 7: Generate and inspect**

Run: `task db:sqlc`
Expected: `backend/internal/identity/store/` gains `db.go` (the `DBTX` interface + `New`), `models.go` (a `User` row struct only — `omit_unused_structs` drops the rest), `queries.sql.go` (one method per query on `*Queries`). `go.mod` gains `github.com/google/uuid` as a direct dependency — run `cd backend && go mod tidy` and confirm `go build ./...` passes.

- [ ] **Step 8: Run the full backend gate slice**

Run: `cd backend && go vet -tags integration ./... && go test -race ./... && go test -race -tags integration -count=1 ./internal/platform/pg/`
Expected: PASS. (Generated code must satisfy vet/lint — if golangci flags generated files, they already carry sqlc's `// Code generated` header and are skipped.)

- [ ] **Step 9: Commit**

```bash
git add backend/internal/platform/pg backend/sqlc.yaml backend/internal/identity Taskfile.yml backend/go.mod backend/go.sum
git commit -m "feat(backend): identity schema, first migration and sqlc machinery"
```

---
### Task 6: Append-only audit recorder

`platform/audit` is shared-kernel machinery (generic append-only who-did-what — no business logic), so any bounded context can record inside its own transaction without a cross-context import. Identity uses it from Task 8 on; credentials (Plan 03), gates and publishing (Plan 08) reuse it.

**Files:**
- Create: `backend/internal/platform/audit/audit.go`
- Create: `backend/internal/platform/audit/queries.sql`
- Create: `backend/internal/platform/audit/db.go`, `models.go`, `queries.sql.go` (generated by sqlc)
- Modify: `backend/sqlc.yaml` (second sql entry)
- Test: `backend/internal/platform/audit/audit_integration_test.go`

**Interfaces:**
- Consumes: `clock.Clock` (Task 3), the `audit_entries` table (Task 5).
- Produces (exact):
  - `audit.Entry{Actor uuid.NullUUID; Action, SubjectKind, SubjectID, CorrelationID string; Detail map[string]any}`
  - `audit.Recorder` · `audit.NewRecorder(c clock.Clock) *Recorder`
  - `(*Recorder).Record(ctx context.Context, db DBTX, e Entry) error` — `DBTX` is the sqlc-generated interface in this package; both `*pgxpool.Pool` and `pgx.Tx` satisfy it, so contexts call it inside their own transactions.

- [ ] **Step 1: Add the audit sql entry and queries**

Append a second entry to the `sql:` list in `backend/sqlc.yaml` (same overrides as the identity entry, verbatim):

```yaml
  - engine: postgresql
    schema: internal/platform/pg/schema.sql
    queries: internal/platform/audit/queries.sql
    gen:
      go:
        package: audit
        out: internal/platform/audit
        sql_package: pgx/v5
        omit_unused_structs: true
        emit_json_tags: false
        overrides:
          - db_type: uuid
            go_type:
              import: github.com/google/uuid
              type: UUID
          - db_type: uuid
            nullable: true
            go_type:
              import: github.com/google/uuid
              type: NullUUID
          - db_type: timestamptz
            nullable: true
            go_type:
              import: time
              type: Time
              pointer: true
```

`backend/internal/platform/audit/queries.sql`:

```sql
-- name: InsertAuditEntry :exec
INSERT INTO audit_entries (occurred_at, actor_user_id, action, subject_kind, subject_id, correlation_id, detail)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListAuditEntriesByAction :many
SELECT id, occurred_at, actor_user_id, action, subject_kind, subject_id, correlation_id, detail
FROM audit_entries
WHERE action = $1
ORDER BY id;
```

(`ListAuditEntriesByAction` exists for tests and the Plan 12 admin audit view; append-only means no update/delete query ever lands here.)

Run: `task db:sqlc`
Expected: generated `db.go`, `models.go`, `queries.sql.go` in `backend/internal/platform/audit/`.

- [ ] **Step 2: Write the failing integration test**

`backend/internal/platform/audit/audit_integration_test.go`:

```go
//go:build integration

package audit_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

func TestRecord_PersistsEntryWithClockTimestamp(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	t0 := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	rec := audit.NewRecorder(clock.NewFake(t0))

	err := rec.Record(ctx, pool, audit.Entry{
		Action:        "auth.login",
		SubjectKind:   "user",
		SubjectID:     "someone@example.com",
		CorrelationID: "corr-123",
		Detail:        map[string]any{"ip": "203.0.113.9"},
	})
	if err != nil {
		t.Fatal(err)
	}

	rows, err := audit.New(pool).ListAuditEntriesByAction(ctx, "auth.login")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	got := rows[0]
	if !got.OccurredAt.Equal(t0) {
		t.Fatalf("occurred_at = %v, want fake-clock %v", got.OccurredAt, t0)
	}
	if got.ActorUserID.Valid {
		t.Fatal("actor should be NULL for system entries")
	}
	var detail map[string]any
	if err := json.Unmarshal(got.Detail, &detail); err != nil {
		t.Fatal(err)
	}
	if detail["ip"] != "203.0.113.9" {
		t.Fatalf("detail = %v", detail)
	}
}

func TestRecord_InsideCallerTransaction(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	rec := audit.NewRecorder(clock.NewFake(time.Unix(0, 0).UTC()))

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	actor := uuid.New()
	// FK: actor must exist inside the same tx for the insert to succeed —
	// use a raw insert to avoid depending on identity here.
	_, err = tx.Exec(ctx, `INSERT INTO users (id, email, password_hash, role) VALUES ($1, 'a@b.co', 'x', 'admin')`, actor)
	if err != nil {
		t.Fatal(err)
	}
	err = rec.Record(ctx, tx, audit.Entry{
		Actor:       uuid.NullUUID{UUID: actor, Valid: true},
		Action:      "user.created",
		SubjectKind: "user",
		SubjectID:   actor.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	rows, err := audit.New(pool).ListAuditEntriesByAction(ctx, "user.created")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatal("rolled-back transaction must take its audit entry with it")
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `cd backend && go test -tags integration -count=1 ./internal/platform/audit/ -v`
Expected: FAIL — `Recorder`, `NewRecorder`, `Entry` undefined (generated code compiles; the hand-written layer is missing).

- [ ] **Step 4: Implement the recorder**

`backend/internal/platform/audit/audit.go`:

```go
// Package audit is the append-only who-did-what recorder (spec §2). It is
// shared-kernel machinery: bounded contexts call Record inside their own
// transactions (DBTX accepts pgx.Tx and *pgxpool.Pool), so an audit write
// never spans contexts. Rows are never updated or deleted.
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/clock"
)

// Entry is one audit fact. Actor is NULL for system actions (bootstrap).
type Entry struct {
	Actor         uuid.NullUUID
	Action        string
	SubjectKind   string
	SubjectID     string
	CorrelationID string
	Detail        map[string]any // nil marshals to {}
}

type Recorder struct {
	clock clock.Clock
}

func NewRecorder(c clock.Clock) *Recorder { return &Recorder{clock: c} }

// Record appends e, stamping occurred_at from the injected clock.
func (r *Recorder) Record(ctx context.Context, db DBTX, e Entry) error {
	detail := e.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("audit: marshal detail: %w", err)
	}
	err = New(db).InsertAuditEntry(ctx, InsertAuditEntryParams{
		OccurredAt:    r.clock.Now(),
		ActorUserID:   e.Actor,
		Action:        e.Action,
		SubjectKind:   e.SubjectKind,
		SubjectID:     e.SubjectID,
		CorrelationID: e.CorrelationID,
		Detail:        raw,
	})
	if err != nil {
		return fmt.Errorf("audit: insert: %w", err)
	}
	return nil
}
```

(If sqlc generated a different param struct name or field types — e.g. `Detail []byte` — match the generated code; the generated file is authoritative for names.)

- [ ] **Step 5: Run to verify it passes**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/platform/audit/ -v`
Expected: PASS (both tests).

- [ ] **Step 6: Commit**

```bash
git add backend/internal/platform/audit backend/sqlc.yaml
git commit -m "feat(backend): append-only audit recorder"
```

---

### Task 7: argon2id password hashing and identity domain types

Pure domain material, no database: PHC-encoded argon2id with pinned parameters, the role and grant-ladder types, and the principal that rides the request context.

**Files:**
- Create: `backend/internal/identity/identity.go`
- Create: `backend/internal/identity/password.go`
- Test: `backend/internal/identity/identity_test.go`
- Test: `backend/internal/identity/password_test.go`

**Interfaces:**
- Produces (exact — the service, API, and later plans consume these):
  - `identity.Role` (`RoleAdmin`, `RoleMember`), `(Role).Valid() bool`
  - `identity.GrantLevel` (`GrantView`, `GrantRun`, `GrantApprove`, `GrantManage`), `(GrantLevel).Valid() bool`, `(GrantLevel).AtLeast(min GrantLevel) bool`
  - `identity.Principal{UserID uuid.UUID; Email string; Role Role}`
  - `identity.AuthSession{Principal; SessionID uuid.UUID}`
  - `identity.User{ID uuid.UUID; Email string; Role Role; Active bool; CreatedAt, UpdatedAt time.Time}`
  - `identity.Grant{ProjectID uuid.UUID; Level GrantLevel}`
  - `identity.HashPassword(pw string) (string, error)` · `identity.VerifyPassword(phc, pw string) (bool, error)`
  - Password length bounds `identity.MinPasswordLen = 12`, `identity.MaxPasswordLen = 512`

- [ ] **Step 1: Write the failing domain-type tests**

`backend/internal/identity/identity_test.go`:

```go
package identity_test

import (
	"testing"

	"github.com/giiorg/caesar/backend/internal/identity"
)

func TestGrantLevel_AtLeast_IsTheInclusiveLadder(t *testing.T) {
	tests := []struct {
		have, need identity.GrantLevel
		want       bool
	}{
		{identity.GrantView, identity.GrantView, true},
		{identity.GrantRun, identity.GrantView, true},
		{identity.GrantApprove, identity.GrantRun, true},
		{identity.GrantManage, identity.GrantApprove, true},
		{identity.GrantView, identity.GrantRun, false},
		{identity.GrantRun, identity.GrantApprove, false},
		{identity.GrantApprove, identity.GrantManage, false},
	}
	for _, tt := range tests {
		if got := tt.have.AtLeast(tt.need); got != tt.want {
			t.Errorf("%s.AtLeast(%s) = %v, want %v", tt.have, tt.need, got, tt.want)
		}
	}
}

func TestGrantLevel_Valid(t *testing.T) {
	for _, ok := range []identity.GrantLevel{identity.GrantView, identity.GrantRun, identity.GrantApprove, identity.GrantManage} {
		if !ok.Valid() {
			t.Errorf("%s should be valid", ok)
		}
	}
	if identity.GrantLevel("owner").Valid() {
		t.Error(`"owner" should be invalid`)
	}
}

func TestRole_Valid(t *testing.T) {
	if !identity.RoleAdmin.Valid() || !identity.RoleMember.Valid() {
		t.Error("admin and member are the V1 roles")
	}
	if identity.Role("root").Valid() {
		t.Error(`"root" should be invalid`)
	}
}
```

`backend/internal/identity/password_test.go`:

```go
package identity_test

import (
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/identity"
)

func TestHashPassword_ProducesVerifiablePHC(t *testing.T) {
	phc, err := identity.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(phc, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected PHC prefix: %s", phc)
	}
	ok, err := identity.VerifyPassword(phc, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("verify(correct) = %v, %v", ok, err)
	}
	ok, err = identity.VerifyPassword(phc, "wrong password entirely")
	if err != nil || ok {
		t.Fatalf("verify(wrong) = %v, %v", ok, err)
	}
}

func TestHashPassword_SaltsDiffer(t *testing.T) {
	a, err := identity.HashPassword("same password twice")
	if err != nil {
		t.Fatal(err)
	}
	b, err := identity.HashPassword("same password twice")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two hashes of the same password must differ (random salt)")
	}
}

func TestVerifyPassword_RejectsMalformedPHC(t *testing.T) {
	tests := []string{
		"",
		"not a phc string",
		"$argon2i$v=19$m=19456,t=2,p=1$AAAA$AAAA",       // wrong variant
		"$argon2id$v=18$m=19456,t=2,p=1$AAAA$AAAA",      // wrong version
		"$argon2id$v=19$m=19456,t=2$AAAA$AAAA",          // missing param
		"$argon2id$v=19$m=19456,t=2,p=1$!notb64!$AAAA",  // bad salt encoding
	}
	for _, phc := range tests {
		if _, err := identity.VerifyPassword(phc, "whatever"); err == nil {
			t.Errorf("VerifyPassword(%q) should error", phc)
		}
	}
}

// Verification must honour the parameters embedded in the stored string,
// so raising the pinned defaults later never invalidates old hashes.
func TestVerifyPassword_HonoursEmbeddedParams(t *testing.T) {
	// Hash of "legacy password!" under m=8192,t=1,p=1, salt
	// "somesaltsomesalt" — precomputed with x/crypto/argon2 v0.54.0.
	const legacy = "$argon2id$v=19$m=8192,t=1,p=1$c29tZXNhbHRzb21lc2FsdA$Jnh3G/TTu8wXQU0fIQZ0vqMqD2+ljUh3ckeadOlwHmI"
	ok, err := identity.VerifyPassword(legacy, "legacy password!")
	if err != nil || !ok {
		t.Fatalf("legacy-params verify = %v, %v", ok, err)
	}
}
```

(The `legacy` literal was computed at plan-writing time with `argon2.IDKey([]byte("legacy password!"), []byte("somesaltsomesalt"), 1, 8192, 1, 32)` — it is a real hash, do not regenerate or skip the test.)

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/identity/`
Expected: FAIL — package exists (store/ subpackage) but `identity.go`/`password.go` don't.

- [ ] **Step 3: Implement the domain types**

`backend/internal/identity/identity.go`:

```go
// Package identity is the identity & access bounded context (spec §2, §6):
// users, sessions, the grant ladder, login. Peers never import it except
// through app wiring; it owns its transactions end to end.
package identity

import (
	"time"

	"github.com/google/uuid"
)

type Role string

const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

func (r Role) Valid() bool { return r == RoleAdmin || r == RoleMember }

// GrantLevel is the per-project inclusive ladder (spec §6):
// view ⊂ run ⊂ approve ⊂ manage. Checks are always AtLeast.
type GrantLevel string

const (
	GrantView    GrantLevel = "view"
	GrantRun     GrantLevel = "run"
	GrantApprove GrantLevel = "approve"
	GrantManage  GrantLevel = "manage"
)

var grantRank = map[GrantLevel]int{GrantView: 1, GrantRun: 2, GrantApprove: 3, GrantManage: 4}

func (g GrantLevel) Valid() bool { return grantRank[g] != 0 }

func (g GrantLevel) AtLeast(min GrantLevel) bool {
	return grantRank[g] != 0 && grantRank[min] != 0 && grantRank[g] >= grantRank[min]
}

// Principal is the authenticated human riding the request context — the
// requester principal of spec §6.
type Principal struct {
	UserID uuid.UUID
	Email  string
	Role   Role
}

// AuthSession is a principal plus the session that authenticated it —
// what logout and rotation operate on.
type AuthSession struct {
	Principal
	SessionID uuid.UUID
}

// User is the domain view of a user row (never exposes the hash).
type User struct {
	ID        uuid.UUID
	Email     string
	Role      Role
	Active    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Grant is one rung of the ladder on one project.
type Grant struct {
	ProjectID uuid.UUID
	Level     GrantLevel
}
```

- [ ] **Step 4: Implement password hashing**

`backend/internal/identity/password.go`:

```go
package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Pinned argon2id parameters (plan header; OWASP baseline). Verification
// reads parameters from the stored PHC string, so raising these later
// never invalidates existing hashes.
const (
	argonMemoryKiB uint32 = 19456
	argonTime      uint32 = 2
	argonThreads   uint8  = 1
	argonSaltLen          = 16
	argonKeyLen    uint32 = 32

	MinPasswordLen = 12
	MaxPasswordLen = 512
)

var b64 = base64.RawStdEncoding

// HashPassword returns the PHC-encoded argon2id hash of pw.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("identity: salt: %w", err)
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword recomputes pw under the parameters embedded in phc and
// compares in constant time.
func VerifyPassword(phc, pw string) (bool, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errors.New("identity: malformed password hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("identity: unsupported hash version")
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, errors.New("identity: malformed hash parameters")
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, errors.New("identity: malformed hash salt")
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, errors.New("identity: malformed hash key")
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
```

Run `cd backend && go mod tidy` — `golang.org/x/crypto` moves to a direct dependency.

- [ ] **Step 5: Run to verify it passes**

Run: `cd backend && go test ./internal/identity/ -race`
Expected: PASS (argon2 tests take a moment — 19 MiB per hash is intended).

- [ ] **Step 6: Commit**

```bash
git add backend/internal/identity backend/go.mod backend/go.sum
git commit -m "feat(backend): argon2id hashing and identity domain types"
```

---
### Task 8: User service — bootstrap, invite-only creation, grants, authorize

The identity service owns its transactions (txboundary: no pgx.Tx in exported signatures). This task: user lifecycle + the grant ladder read side. Sessions (Task 9) and login (Task 10) extend the same struct.

**Files:**
- Create: `backend/internal/identity/service.go`
- Modify: `backend/internal/identity/store/queries.sql` (grants queries) + regen
- Test: `backend/internal/identity/service_integration_test.go`

**Interfaces:**
- Consumes: `store.New`/queries (Task 5), `audit.Recorder` (6), `clock.Clock` (3), domain types + hashing (7), `xerrors` (4), `httpx.CorrelationID` (Plan 01).
- Produces (exact — Tasks 9–12 extend/consume):
  - `identity.Params{SessionIdleTTL, SessionAbsoluteTTL time.Duration}`
  - `identity.NewService(pool *pgxpool.Pool, c clock.Clock, rec *audit.Recorder, p Params) *Service`
  - `(*Service).Bootstrap(ctx, email, password string) (User, error)`
  - `(*Service).CreateUser(ctx, actor Principal, email, password string, role Role) (User, error)`
  - `(*Service).ListUsers(ctx) ([]User, error)` · `(*Service).GetUser(ctx, id uuid.UUID) (User, error)`
  - `(*Service).UpdateUser(ctx, actor Principal, id uuid.UUID, upd UserUpdate) (User, error)` with `identity.UserUpdate{Role *Role; Active *bool}`
  - `(*Service).GrantsForUser(ctx, userID uuid.UUID) ([]Grant, error)`
  - `(*Service).Authorize(ctx, p Principal, projectID uuid.UUID, min GrantLevel) (bool, error)` — global admin always true.

- [ ] **Step 1: Add the grants queries and regenerate**

Append to `backend/internal/identity/store/queries.sql`:

```sql
-- name: GrantsForUser :many
SELECT project_id, level FROM grants WHERE user_id = $1 ORDER BY project_id;

-- name: GetGrantLevel :one
SELECT level FROM grants WHERE user_id = $1 AND project_id = $2;
```

Run: `task db:sqlc` — regenerates the store.

- [ ] **Step 2: Write the failing integration tests**

`backend/internal/identity/service_integration_test.go`:

```go
//go:build integration

package identity_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

const testPassword = "a long enough password"

func newTestService(t *testing.T) (*identity.Service, *pgxpool.Pool, *clock.Fake) {
	t.Helper()
	pool := testdb.New(t)
	fake := clock.NewFake(time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	svc := identity.NewService(pool, fake, audit.NewRecorder(fake), identity.Params{
		SessionIdleTTL:     14 * 24 * time.Hour,
		SessionAbsoluteTTL: 90 * 24 * time.Hour,
	})
	return svc, pool, fake
}

func wantClass(t *testing.T, err error, want xerrors.Class) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := xerrors.ClassOf(err); got != want {
		t.Fatalf("class = %v, want %v (err: %v)", got, want, err)
	}
}

func TestBootstrap_CreatesFirstAdminOnceOnly(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)

	admin, err := svc.Bootstrap(ctx, "root@example.com", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if admin.Role != identity.RoleAdmin || !admin.Active {
		t.Fatalf("bootstrap user = %+v", admin)
	}

	_, err = svc.Bootstrap(ctx, "second@example.com", testPassword)
	wantClass(t, err, xerrors.ClassConflict)

	rows, err := audit.New(pool).ListAuditEntriesByAction(ctx, "user.bootstrapped")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ActorUserID.Valid {
		t.Fatalf("bootstrap audit rows = %+v (want one, system actor)", rows)
	}
}

func TestCreateUser_ValidatesAndAudits(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin, err := svc.Bootstrap(ctx, "root@example.com", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Principal{UserID: admin.ID, Email: admin.Email, Role: admin.Role}

	tests := []struct {
		name           string
		email, pw      string
		role           identity.Role
		wantClass      xerrors.Class
	}{
		{"bad email", "not-an-email", testPassword, identity.RoleMember, xerrors.ClassInvalid},
		{"short password", "ok@example.com", "tooshort", identity.RoleMember, xerrors.ClassInvalid},
		{"bad role", "ok@example.com", testPassword, identity.Role("root"), xerrors.ClassInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.CreateUser(ctx, actor, tt.email, tt.pw, tt.role)
			wantClass(t, err, tt.wantClass)
		})
	}

	member, err := svc.CreateUser(ctx, actor, "Member@Example.com", testPassword, identity.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	// Duplicate detection is case-insensitive (unique on lower(email)).
	_, err = svc.CreateUser(ctx, actor, "member@example.COM", testPassword, identity.RoleMember)
	wantClass(t, err, xerrors.ClassConflict)

	rows, err := audit.New(pool).ListAuditEntriesByAction(ctx, "user.created")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].ActorUserID.Valid || rows[0].ActorUserID.UUID != admin.ID {
		t.Fatalf("user.created audit = %+v", rows)
	}
	if rows[0].SubjectID != member.ID.String() {
		t.Fatalf("subject = %q, want member id", rows[0].SubjectID)
	}
}

func TestUpdateUser_GuardsAndRoleChange(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTestService(t)
	admin, _ := svc.Bootstrap(ctx, "root@example.com", testPassword)
	actor := identity.Principal{UserID: admin.ID, Email: admin.Email, Role: admin.Role}
	member, err := svc.CreateUser(ctx, actor, "m@example.com", testPassword, identity.RoleMember)
	if err != nil {
		t.Fatal(err)
	}

	// Admin cannot touch their own role/active (lockout guard).
	adminRole := identity.RoleMember
	_, err = svc.UpdateUser(ctx, actor, admin.ID, identity.UserUpdate{Role: &adminRole})
	wantClass(t, err, xerrors.ClassForbidden)

	// Promote the member.
	toAdmin := identity.RoleAdmin
	updated, err := svc.UpdateUser(ctx, actor, member.ID, identity.UserUpdate{Role: &toAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Role != identity.RoleAdmin {
		t.Fatalf("role = %s", updated.Role)
	}

	// Unknown user → not found.
	_, err = svc.UpdateUser(ctx, actor, uuid.New(), identity.UserUpdate{Role: &toAdmin})
	wantClass(t, err, xerrors.ClassNotFound)
}

func TestUpdateUser_LastActiveAdminGuard(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTestService(t)
	admin, _ := svc.Bootstrap(ctx, "root@example.com", testPassword)
	actor := identity.Principal{UserID: admin.ID, Email: admin.Email, Role: admin.Role}
	second, err := svc.CreateUser(ctx, actor, "second@example.com", testPassword, identity.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	// Deactivate the second admin: fine (root remains).
	inactive := false
	if _, err := svc.UpdateUser(ctx, actor, second.ID, identity.UserUpdate{Active: &inactive}); err != nil {
		t.Fatal(err)
	}
	// Now try to demote root via the (still-active-in-principal) second
	// admin — the guard must count *active* admins, so demoting the last
	// active admin fails even when another, deactivated admin row exists.
	secondActor := identity.Principal{UserID: second.ID, Email: second.Email, Role: identity.RoleAdmin}
	demote := identity.RoleMember
	_, err = svc.UpdateUser(ctx, secondActor, admin.ID, identity.UserUpdate{Role: &demote})
	wantClass(t, err, xerrors.ClassConflict)
}

func TestAuthorize_LadderAndAdminBypass(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin, _ := svc.Bootstrap(ctx, "root@example.com", testPassword)
	actor := identity.Principal{UserID: admin.ID, Email: admin.Email, Role: admin.Role}
	member, err := svc.CreateUser(ctx, actor, "m@example.com", testPassword, identity.RoleMember)
	if err != nil {
		t.Fatal(err)
	}

	var projectID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (name) VALUES ('demo') RETURNING id`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO grants (user_id, project_id, level) VALUES ($1, $2, 'run')`, member.ID, projectID); err != nil {
		t.Fatal(err)
	}

	memberP := identity.Principal{UserID: member.ID, Email: member.Email, Role: identity.RoleMember}
	tests := []struct {
		name string
		p    identity.Principal
		min  identity.GrantLevel
		want bool
	}{
		{"member at granted level", memberP, identity.GrantRun, true},
		{"member below granted level", memberP, identity.GrantView, true},
		{"member above granted level", memberP, identity.GrantApprove, false},
		{"admin bypasses grants", actor, identity.GrantManage, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.Authorize(ctx, tt.p, projectID, tt.min)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("Authorize = %v, want %v", got, tt.want)
			}
		})
	}

	// No grant row at all → false, not an error.
	ok, err := svc.Authorize(ctx, memberP, uuid.New(), identity.GrantView)
	if err != nil || ok {
		t.Fatalf("ungranted project: ok=%v err=%v", ok, err)
	}

	grants, err := svc.GrantsForUser(ctx, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].Level != identity.GrantRun || grants[0].ProjectID != projectID {
		t.Fatalf("grants = %+v", grants)
	}
}

var _ = errors.Is // silence unused-import churn while iterating
```

(Remove the trailing `var _` line once the file compiles without it.)

- [ ] **Step 3: Run to verify it fails**

Run: `cd backend && go test -tags integration -count=1 ./internal/identity/ -run 'TestBootstrap|TestCreateUser|TestUpdateUser|TestAuthorize' -v`
Expected: FAIL — `NewService` undefined.

- [ ] **Step 4: Implement the service**

`backend/internal/identity/service.go`:

```go
package identity

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/identity/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// Params are the deployment-tunable knobs (config supplies them in app
// wiring; defaults pinned in the plan header).
type Params struct {
	SessionIdleTTL     time.Duration
	SessionAbsoluteTTL time.Duration
}

type Service struct {
	pool   *pgxpool.Pool
	clock  clock.Clock
	audit  *audit.Recorder
	params Params
}

func NewService(pool *pgxpool.Pool, c clock.Clock, rec *audit.Recorder, p Params) *Service {
	return &Service{pool: pool, clock: c, audit: rec, params: p}
}

// UserUpdate is a partial admin update; nil fields are left unchanged.
type UserUpdate struct {
	Role   *Role
	Active *bool
}

func userFromRow(row store.User) User {
	return User{
		ID:        row.ID,
		Email:     row.Email,
		Role:      Role(row.Role),
		Active:    row.Active,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

func validateEmail(email string) error {
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "email", Message: "not a valid email address"})
	}
	return nil
}

func validatePassword(pw string) error {
	if len(pw) < MinPasswordLen || len(pw) > MaxPasswordLen {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{
				Field:   "password",
				Message: fmt.Sprintf("must be between %d and %d characters", MinPasswordLen, MaxPasswordLen),
			})
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Bootstrap creates the first admin. It refuses once any admin row exists
// (spec §6); `caesar admin bootstrap` is its only caller besides tests.
func (s *Service) Bootstrap(ctx context.Context, email, password string) (User, error) {
	if err := validateEmail(email); err != nil {
		return User{}, err
	}
	if err := validatePassword(password); err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("identity: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	admins, err := q.CountAdmins(ctx)
	if err != nil {
		return User{}, fmt.Errorf("identity: count admins: %w", err)
	}
	if admins > 0 {
		return User{}, xerrors.E(xerrors.ClassConflict, "an admin already exists; bootstrap runs once")
	}
	row, err := q.CreateUser(ctx, store.CreateUserParams{
		Email: email, PasswordHash: hash, Role: string(RoleAdmin),
	})
	if err != nil {
		return User{}, fmt.Errorf("identity: create admin: %w", err)
	}
	err = s.audit.Record(ctx, tx, audit.Entry{
		Action: "user.bootstrapped", SubjectKind: "user", SubjectID: row.ID.String(),
		CorrelationID: httpx.CorrelationID(ctx),
		Detail:        map[string]any{"email": email},
	})
	if err != nil {
		return User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("identity: commit: %w", err)
	}
	return userFromRow(row), nil
}

// CreateUser is admin-invite-only user creation. The API layer enforces
// that actor is an admin; the service records it as the audit actor.
func (s *Service) CreateUser(ctx context.Context, actor Principal, email, password string, role Role) (User, error) {
	if err := validateEmail(email); err != nil {
		return User{}, err
	}
	if err := validatePassword(password); err != nil {
		return User{}, err
	}
	if !role.Valid() {
		return User{}, xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "role", Message: "must be admin or member"})
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("identity: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	row, err := q.CreateUser(ctx, store.CreateUserParams{
		Email: email, PasswordHash: hash, Role: string(role),
	})
	if isUniqueViolation(err) {
		return User{}, xerrors.E(xerrors.ClassConflict, "email already registered")
	}
	if err != nil {
		return User{}, fmt.Errorf("identity: create user: %w", err)
	}
	err = s.audit.Record(ctx, tx, audit.Entry{
		Actor:  uuid.NullUUID{UUID: actor.UserID, Valid: true},
		Action: "user.created", SubjectKind: "user", SubjectID: row.ID.String(),
		CorrelationID: httpx.CorrelationID(ctx),
		Detail:        map[string]any{"email": email, "role": string(role)},
	})
	if err != nil {
		return User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("identity: commit: %w", err)
	}
	return userFromRow(row), nil
}

func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := store.New(s.pool).ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("identity: list users: %w", err)
	}
	users := make([]User, 0, len(rows))
	for _, r := range rows {
		users = append(users, userFromRow(r))
	}
	return users, nil
}

func (s *Service) GetUser(ctx context.Context, id uuid.UUID) (User, error) {
	row, err := store.New(s.pool).GetUserByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, xerrors.E(xerrors.ClassNotFound, "no such user")
	}
	if err != nil {
		return User{}, fmt.Errorf("identity: get user: %w", err)
	}
	return userFromRow(row), nil
}

// UpdateUser applies an admin's role/active change. Guards: an admin never
// modifies their own row (lockout prevention), and the last active admin
// can be neither demoted nor deactivated. Task 9 extends this to revoke
// the target's sessions on privilege change.
func (s *Service) UpdateUser(ctx context.Context, actor Principal, id uuid.UUID, upd UserUpdate) (User, error) {
	if actor.UserID == id {
		return User{}, xerrors.E(xerrors.ClassForbidden, "cannot change own role or active flag")
	}
	if upd.Role != nil && !upd.Role.Valid() {
		return User{}, xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "role", Message: "must be admin or member"})
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("identity: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	current, err := q.GetUserByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, xerrors.E(xerrors.ClassNotFound, "no such user")
	}
	if err != nil {
		return User{}, fmt.Errorf("identity: get user: %w", err)
	}

	newRole := Role(current.Role)
	if upd.Role != nil {
		newRole = *upd.Role
	}
	newActive := current.Active
	if upd.Active != nil {
		newActive = *upd.Active
	}

	losesAdmin := Role(current.Role) == RoleAdmin && current.Active && (newRole != RoleAdmin || !newActive)
	if losesAdmin {
		activeAdmins, err := q.CountActiveAdmins(ctx)
		if err != nil {
			return User{}, fmt.Errorf("identity: count active admins: %w", err)
		}
		if activeAdmins <= 1 {
			return User{}, xerrors.E(xerrors.ClassConflict, "cannot remove the last active admin")
		}
	}

	row, err := q.UpdateUserRoleActive(ctx, store.UpdateUserRoleActiveParams{
		ID: id, Role: string(newRole), Active: newActive, UpdatedAt: s.clock.Now(),
	})
	if err != nil {
		return User{}, fmt.Errorf("identity: update user: %w", err)
	}
	err = s.audit.Record(ctx, tx, audit.Entry{
		Actor:  uuid.NullUUID{UUID: actor.UserID, Valid: true},
		Action: "user.updated", SubjectKind: "user", SubjectID: id.String(),
		CorrelationID: httpx.CorrelationID(ctx),
		Detail:        map[string]any{"role": string(newRole), "active": newActive},
	})
	if err != nil {
		return User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("identity: commit: %w", err)
	}
	return userFromRow(row), nil
}

func (s *Service) GrantsForUser(ctx context.Context, userID uuid.UUID) ([]Grant, error) {
	rows, err := store.New(s.pool).GrantsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("identity: grants: %w", err)
	}
	grants := make([]Grant, 0, len(rows))
	for _, r := range rows {
		grants = append(grants, Grant{ProjectID: r.ProjectID, Level: GrantLevel(r.Level)})
	}
	return grants, nil
}

// Authorize is the single RBAC check (spec §6): global admin passes
// everything; members need a grant row at or above min on the project.
func (s *Service) Authorize(ctx context.Context, p Principal, projectID uuid.UUID, min GrantLevel) (bool, error) {
	if p.Role == RoleAdmin {
		return true, nil
	}
	level, err := store.New(s.pool).GetGrantLevel(ctx, store.GetGrantLevelParams{
		UserID: p.UserID, ProjectID: projectID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("identity: grant lookup: %w", err)
	}
	return GrantLevel(level).AtLeast(min), nil
}
```

(If the compiler flags any unused import in the block above, drop it — the list is written for the final file state.)

Add one more query to `queries.sql` (used by the last-admin guard) and regen:

```sql
-- name: CountActiveAdmins :one
SELECT count(*) FROM users WHERE role = 'admin' AND active;
```

Remove the trailing `var _` line if `strings` ends up unused.

- [ ] **Step 5: Run to verify it passes**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/identity/ -v`
Expected: PASS.

- [ ] **Step 6: Run the analyzers (txboundary must stay quiet)**

Run: `task backend:analyze`
Expected: clean — no exported identity signature mentions a pgx transaction.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/identity
git commit -m "feat(backend): user service with bootstrap, grants and authorize"
```

---

### Task 9: Sessions — mint, authenticate, revoke, rotate

Server-side sessions per spec §6: random token in a `__Host-` cookie (API layer, Task 11), SHA-256 in the row, idle + absolute expiry from the injected clock, instant revocation, rotation on password change, revoke-all on privilege change.

**Files:**
- Modify: `backend/internal/identity/store/queries.sql` (session queries) + regen
- Create: `backend/internal/identity/session.go`
- Modify: `backend/internal/identity/service.go` (UpdateUser revokes on privilege change)
- Test: `backend/internal/identity/session_integration_test.go`

**Interfaces:**
- Consumes: `Service` internals (Task 8).
- Produces (exact — Tasks 10–12 consume):
  - `(*Service).AuthenticateSession(ctx, token string) (AuthSession, error)` — `ClassUnauthenticated` on unknown/revoked/expired/inactive-user; refreshes `last_seen_at` at most every 5 minutes.
  - `(*Service).Logout(ctx, sess AuthSession) error` — revokes + audits `auth.logout`.
  - `(*Service).ChangePassword(ctx, sess AuthSession, current, new string) (newToken string, err error)` — verifies current, updates hash, revokes ALL the user's sessions, mints a replacement, audits `auth.password_changed`.
  - unexported `(*Service).mintSession(ctx context.Context, q *store.Queries, userID uuid.UUID) (token string, err error)` — Task 10's login reuses it.
  - `UpdateUser` now revokes the target's sessions whenever role or active changes.

- [ ] **Step 1: Add the session queries and regenerate**

Append to `backend/internal/identity/store/queries.sql`:

```sql
-- name: InsertSession :one
INSERT INTO sessions (user_id, token_hash, created_at, last_seen_at, expires_at)
VALUES ($1, $2, $3, $3, $4)
RETURNING id;

-- name: GetSessionByTokenHash :one
SELECT s.id, s.user_id, s.created_at, s.last_seen_at, s.expires_at, s.revoked_at,
       u.email, u.role, u.active
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = $1;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = $2 WHERE id = $1;

-- name: RevokeSession :exec
UPDATE sessions SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeUserSessions :exec
UPDATE sessions SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL;
```

Run: `task db:sqlc`.

- [ ] **Step 2: Write the failing integration tests**

`backend/internal/identity/session_integration_test.go`:

```go
//go:build integration

package identity_test

import (
	"context"
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// Login lands in Task 10; until then sessions are minted through the
// MintSessionForTest seam, and everything else here exercises the public
// surface this task ships: AuthenticateSession, Logout, ChangePassword.

func bootstrapWithSession(t *testing.T) (*identity.Service, identity.AuthSession, string, *clock.Fake) {
	t.Helper()
	ctx := context.Background()
	svc, _, fake := newTestService(t)
	admin, err := svc.Bootstrap(ctx, "root@example.com", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	token, err := svc.MintSessionForTest(ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := svc.AuthenticateSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	return svc, sess, token, fake
}

func TestAuthenticateSession_Lifecycle(t *testing.T) {
	ctx := context.Background()
	svc, sess, token, fake := bootstrapWithSession(t)

	if sess.Email != "root@example.com" || sess.Role != identity.RoleAdmin {
		t.Fatalf("principal = %+v", sess.Principal)
	}

	// Garbage token → unauthenticated.
	_, err := svc.AuthenticateSession(ctx, "not-a-real-token")
	wantClass(t, err, xerrors.ClassUnauthenticated)

	// Idle expiry: advance past 14 days without touching.
	fake.Advance(14*24*time.Hour + time.Minute)
	_, err = svc.AuthenticateSession(ctx, token)
	wantClass(t, err, xerrors.ClassUnauthenticated)
}

func TestAuthenticateSession_IdleRefreshKeepsSessionAlive(t *testing.T) {
	ctx := context.Background()
	svc, _, token, fake := bootstrapWithSession(t)

	// Touch every 7 days: idle window (14d) never trips, absolute (90d) does.
	for day := 0; day < 84; day += 7 {
		fake.Advance(7 * 24 * time.Hour)
		if _, err := svc.AuthenticateSession(ctx, token); err != nil {
			t.Fatalf("day %d: %v", day+7, err)
		}
	}
	fake.Advance(7 * 24 * time.Hour) // day 91 > 90d absolute
	_, err := svc.AuthenticateSession(ctx, token)
	wantClass(t, err, xerrors.ClassUnauthenticated)
}

func TestLogout_RevokesImmediately(t *testing.T) {
	ctx := context.Background()
	svc, sess, token, _ := bootstrapWithSession(t)

	if err := svc.Logout(ctx, sess); err != nil {
		t.Fatal(err)
	}
	_, err := svc.AuthenticateSession(ctx, token)
	wantClass(t, err, xerrors.ClassUnauthenticated)
}

func TestChangePassword_RotatesAndRevokesEverythingElse(t *testing.T) {
	ctx := context.Background()
	svc, sess, oldToken, _ := bootstrapWithSession(t)

	// A second device.
	otherToken, err := svc.MintSessionForTest(ctx, sess.UserID)
	if err != nil {
		t.Fatal(err)
	}

	// Wrong current password refused.
	_, err = svc.ChangePassword(ctx, sess, "wrong current pw!", "a brand new password")
	wantClass(t, err, xerrors.ClassUnauthenticated)

	newToken, err := svc.ChangePassword(ctx, sess, testPassword, "a brand new password")
	if err != nil {
		t.Fatal(err)
	}
	for _, dead := range []string{oldToken, otherToken} {
		if _, err := svc.AuthenticateSession(ctx, dead); err == nil {
			t.Fatal("old sessions must be revoked after password change")
		}
	}
	if _, err := svc.AuthenticateSession(ctx, newToken); err != nil {
		t.Fatalf("replacement session: %v", err)
	}
}

func TestUpdateUser_PrivilegeChangeRevokesSessions(t *testing.T) {
	ctx := context.Background()
	svc, adminSess, _, _ := bootstrapWithSession(t)

	member, err := svc.CreateUser(ctx, adminSess.Principal, "m@example.com", testPassword, identity.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	memberToken, err := svc.MintSessionForTest(ctx, member.ID)
	if err != nil {
		t.Fatal(err)
	}

	toAdmin := identity.RoleAdmin
	if _, err := svc.UpdateUser(ctx, adminSess.Principal, member.ID, identity.UserUpdate{Role: &toAdmin}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.AuthenticateSession(ctx, memberToken)
	wantClass(t, err, xerrors.ClassUnauthenticated)
}
```

Note: `MintSessionForTest` is a deliberate exported test seam on `*Service` (Step 4 defines it) — it wraps the unexported mint path in its own transaction. Nothing in production code calls it; Task 10's `Login` is the production mint path.

- [ ] **Step 3: Run to verify it fails**

Run: `cd backend && go test -tags integration -count=1 ./internal/identity/ -run 'Session|Logout|ChangePassword|PrivilegeChange' -v`
Expected: FAIL — session methods undefined.

- [ ] **Step 4: Implement sessions**

`backend/internal/identity/session.go`:

```go
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/identity/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

const (
	sessionTokenLen      = 32
	sessionTouchInterval = 5 * time.Minute
)

var errBadSession = xerrors.E(xerrors.ClassUnauthenticated, "invalid or expired session")

func newSessionToken() (token string, hash []byte, err error) {
	raw := make([]byte, sessionTokenLen)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("identity: session token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// mintSession inserts a fresh session row inside the caller's transaction
// and returns the bearer token (never stored).
func (s *Service) mintSession(ctx context.Context, q *store.Queries, userID uuid.UUID) (string, error) {
	token, hash, err := newSessionToken()
	if err != nil {
		return "", err
	}
	now := s.clock.Now()
	_, err = q.InsertSession(ctx, store.InsertSessionParams{
		UserID:    userID,
		TokenHash: hash,
		CreatedAt: now,
		ExpiresAt: now.Add(s.params.SessionAbsoluteTTL),
	})
	if err != nil {
		return "", fmt.Errorf("identity: insert session: %w", err)
	}
	return token, nil
}

// AuthenticateSession resolves a bearer token to its principal. Any
// failure mode is the same client-facing error (no oracle).
func (s *Service) AuthenticateSession(ctx context.Context, token string) (AuthSession, error) {
	row, err := store.New(s.pool).GetSessionByTokenHash(ctx, hashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthSession{}, errBadSession
	}
	if err != nil {
		return AuthSession{}, fmt.Errorf("identity: session lookup: %w", err)
	}
	now := s.clock.Now()
	switch {
	case row.RevokedAt != nil:
		return AuthSession{}, errBadSession
	case !now.Before(row.ExpiresAt):
		return AuthSession{}, errBadSession
	case !now.Before(row.LastSeenAt.Add(s.params.SessionIdleTTL)):
		return AuthSession{}, errBadSession
	case !row.Active:
		return AuthSession{}, errBadSession
	}
	if now.Sub(row.LastSeenAt) >= sessionTouchInterval {
		if err := store.New(s.pool).TouchSession(ctx, store.TouchSessionParams{ID: row.ID, LastSeenAt: now}); err != nil {
			return AuthSession{}, fmt.Errorf("identity: touch session: %w", err)
		}
	}
	return AuthSession{
		Principal: Principal{UserID: row.UserID, Email: row.Email, Role: Role(row.Role)},
		SessionID: row.ID,
	}, nil
}

// Logout revokes the calling session.
func (s *Service) Logout(ctx context.Context, sess AuthSession) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("identity: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if err := q.RevokeSession(ctx, store.RevokeSessionParams{ID: sess.SessionID, RevokedAt: s.clock.Now()}); err != nil {
		return fmt.Errorf("identity: revoke session: %w", err)
	}
	err = s.audit.Record(ctx, tx, audit.Entry{
		Actor:  uuid.NullUUID{UUID: sess.UserID, Valid: true},
		Action: "auth.logout", SubjectKind: "session", SubjectID: sess.SessionID.String(),
		CorrelationID: httpx.CorrelationID(ctx),
	})
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ChangePassword verifies the current password, rewrites the hash,
// revokes every session of the user, and mints a replacement for the
// caller (rotation on privilege change, spec §6).
func (s *Service) ChangePassword(ctx context.Context, sess AuthSession, current, newPassword string) (string, error) {
	if err := validatePassword(newPassword); err != nil {
		return "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("identity: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	row, err := q.GetUserByID(ctx, sess.UserID)
	if err != nil {
		return "", fmt.Errorf("identity: get user: %w", err)
	}
	ok, err := VerifyPassword(row.PasswordHash, current)
	if err != nil {
		return "", fmt.Errorf("identity: verify: %w", err)
	}
	if !ok {
		return "", xerrors.E(xerrors.ClassUnauthenticated, "current password is incorrect")
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return "", err
	}
	if err := q.UpdateUserPassword(ctx, store.UpdateUserPasswordParams{ID: sess.UserID, PasswordHash: hash, UpdatedAt: s.clock.Now()}); err != nil {
		return "", fmt.Errorf("identity: update password: %w", err)
	}
	if err := q.RevokeUserSessions(ctx, store.RevokeUserSessionsParams{UserID: sess.UserID, RevokedAt: s.clock.Now()}); err != nil {
		return "", fmt.Errorf("identity: revoke sessions: %w", err)
	}
	token, err := s.mintSession(ctx, q, sess.UserID)
	if err != nil {
		return "", err
	}
	err = s.audit.Record(ctx, tx, audit.Entry{
		Actor:  uuid.NullUUID{UUID: sess.UserID, Valid: true},
		Action: "auth.password_changed", SubjectKind: "user", SubjectID: sess.UserID.String(),
		CorrelationID: httpx.CorrelationID(ctx),
	})
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("identity: commit: %w", err)
	}
	return token, nil
}
```

Field names on generated params/rows (`LastSeenAt`, `RevokedAt *time.Time`, joined `Email`/`Role`/`Active`) come from sqlc — match the generated code.

Also in `service.go`, extend `UpdateUser`: after the successful `UpdateUserRoleActive` call and before the audit write, add:

```go
	if newRole != Role(current.Role) || newActive != current.Active {
		if err := q.RevokeUserSessions(ctx, store.RevokeUserSessionsParams{UserID: id, RevokedAt: s.clock.Now()}); err != nil {
			return User{}, fmt.Errorf("identity: revoke sessions: %w", err)
		}
	}
```

And add the test seam (choose the `export_test.go` form if preferred):

```go
// MintSessionForTest mints a session outside a login flow. Test seam only.
func (s *Service) MintSessionForTest(ctx context.Context, userID uuid.UUID) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	token, err := s.mintSession(ctx, store.New(tx), userID)
	if err != nil {
		return "", err
	}
	return token, tx.Commit(ctx)
}
```

- [ ] **Step 5: Run to verify it passes**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/identity/ -v`
Expected: PASS — all Task 8 + Task 9 tests.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/identity
git commit -m "feat(backend): server-side sessions with rotation and revocation"
```

---
### Task 10: Login with dual-scope rate limiting

Login is the one unauthenticated mutation, so it carries the pinned rate limits: per-account exponential lockout (restart-proof, in Postgres) and a per-IP window. Timing is equalized — an unknown email still pays one argon2 verification.

**Files:**
- Create: `backend/internal/identity/ratelimit.go`
- Create: `backend/internal/identity/login.go`
- Modify: `backend/internal/identity/store/queries.sql` (rate queries) + regen
- Test: `backend/internal/identity/ratelimit_test.go` (pure unit)
- Test: `backend/internal/identity/login_integration_test.go`

**Interfaces:**
- Consumes: `mintSession` (Task 9), store, audit, clock.
- Produces (exact):
  - `(*Service).Login(ctx, email, password, ip string) (User, string, error)` — returns the user and bearer token; `ClassUnauthenticated` for any credential failure (one indistinguishable message), `ClassRateLimited` when locked.
  - Pinned parameters as unexported consts (below) — changing them is a code change, reviewed like one.

- [ ] **Step 1: Add the rate queries and regenerate**

Append to `backend/internal/identity/store/queries.sql`:

```sql
-- name: GetLoginRateForUpdate :one
SELECT scope, failures, window_started_at, locked_until
FROM login_rate WHERE scope = $1
FOR UPDATE;

-- name: UpsertLoginRate :exec
INSERT INTO login_rate (scope, failures, window_started_at, locked_until)
VALUES ($1, $2, $3, $4)
ON CONFLICT (scope) DO UPDATE
SET failures = excluded.failures,
    window_started_at = excluded.window_started_at,
    locked_until = excluded.locked_until;

-- name: DeleteLoginRate :exec
DELETE FROM login_rate WHERE scope = $1;
```

Run: `task db:sqlc`.

- [ ] **Step 2: Write the failing pure-logic test**

`backend/internal/identity/ratelimit_test.go` — note `package identity` (internal test; the state machine is unexported):

```go
package identity

import (
	"testing"
	"time"
)

func TestRateFail_AccountBackoffLadder(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	st := rateState{}
	// Failures 1–4: no lock.
	for i := 1; i <= 4; i++ {
		st = rateFail(rateAccount, st, now)
		if st.LockedUntil != nil {
			t.Fatalf("failure %d: locked too early", i)
		}
	}
	// Failure 5 → 30s, 6 → 1m, 7 → 2m, 8 → 4m, 9 → 8m, 10+ → capped 15m.
	wants := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for i, want := range wants {
		st = rateFail(rateAccount, st, now)
		if st.LockedUntil == nil {
			t.Fatalf("failure %d: expected lock", i+5)
		}
		if got := st.LockedUntil.Sub(now); got != want {
			t.Fatalf("failure %d: lock = %v, want %v", i+5, got, want)
		}
	}
}

func TestRateFail_IPWindowResetsAndLocks(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	st := rateState{}
	for i := 0; i < 29; i++ {
		st = rateFail(rateIP, st, now)
	}
	if st.LockedUntil != nil {
		t.Fatal("29 failures must not lock")
	}
	// Window expiry resets the count.
	later := now.Add(16 * time.Minute)
	st = rateFail(rateIP, st, later)
	if st.Failures != 1 || st.LockedUntil != nil {
		t.Fatalf("after window reset: %+v", st)
	}
	// 30 in-window failures lock for 15m.
	for i := 0; i < 29; i++ {
		st = rateFail(rateIP, st, later)
	}
	if st.LockedUntil == nil || !st.LockedUntil.Equal(later.Add(15*time.Minute)) {
		t.Fatalf("ip lock = %+v", st.LockedUntil)
	}
}

func TestRateBlocked(t *testing.T) {
	now := time.Unix(1000, 0)
	until := now.Add(time.Minute)
	if !rateBlocked(rateState{LockedUntil: &until}, now) {
		t.Fatal("inside lock window must block")
	}
	if rateBlocked(rateState{LockedUntil: &until}, now.Add(2*time.Minute)) {
		t.Fatal("past lock window must not block")
	}
	if rateBlocked(rateState{}, now) {
		t.Fatal("no lock must not block")
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `cd backend && go test ./internal/identity/ -run TestRate -v`
Expected: FAIL — `rateState`, `rateFail`, `rateBlocked` undefined.

- [ ] **Step 4: Implement the rate state machine**

`backend/internal/identity/ratelimit.go`:

```go
package identity

import "time"

// Pinned login rate-limit parameters (plan header). DB-backed via the
// login_rate table so they survive restarts and hold across processes.
const (
	accountLockThreshold = 5
	accountLockBase      = 30 * time.Second
	accountLockMax       = 15 * time.Minute
	ipWindow             = 15 * time.Minute
	ipLockThreshold      = 30
	ipLockDuration       = 15 * time.Minute
)

type rateKind int

const (
	rateAccount rateKind = iota
	rateIP
)

// rateState mirrors one login_rate row.
type rateState struct {
	Failures    int
	WindowStart time.Time
	LockedUntil *time.Time
}

func rateBlocked(st rateState, now time.Time) bool {
	return st.LockedUntil != nil && now.Before(*st.LockedUntil)
}

// rateFail applies one failed attempt and returns the next state.
// Account scope: consecutive failures, exponential lock from the 5th,
// capped at 15m; the counter resets only on success (Login deletes the
// row). IP scope: fixed 15-minute window; the 30th in-window failure
// locks for 15 minutes.
func rateFail(kind rateKind, st rateState, now time.Time) rateState {
	switch kind {
	case rateAccount:
		if st.WindowStart.IsZero() {
			st.WindowStart = now
		}
		st.Failures++
		if st.Failures >= accountLockThreshold {
			d := accountLockMax
			if over := st.Failures - accountLockThreshold; over < 6 {
				if v := accountLockBase << over; v < d {
					d = v
				}
			}
			t := now.Add(d)
			st.LockedUntil = &t
		}
	case rateIP:
		if st.WindowStart.IsZero() || now.Sub(st.WindowStart) >= ipWindow {
			st = rateState{WindowStart: now}
		}
		st.Failures++
		if st.Failures >= ipLockThreshold {
			t := now.Add(ipLockDuration)
			st.LockedUntil = &t
		}
	}
	return st
}
```

Run: `cd backend && go test ./internal/identity/ -run TestRate -v` — PASS.

- [ ] **Step 5: Write the failing login integration tests**

`backend/internal/identity/login_integration_test.go`:

```go
//go:build integration

package identity_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

const loginIP = "203.0.113.9"

func TestLogin_SuccessMintsSessionAndAudits(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	if _, err := svc.Bootstrap(ctx, "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}

	user, token, err := svc.Login(ctx, "Root@Example.com", testPassword, loginIP) // case-insensitive email
	if err != nil {
		t.Fatal(err)
	}
	sess, err := svc.AuthenticateSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if sess.UserID != user.ID {
		t.Fatal("session principal mismatch")
	}
	rows, err := audit.New(pool).ListAuditEntriesByAction(ctx, "auth.login")
	if err != nil || len(rows) != 1 {
		t.Fatalf("auth.login audit rows = %d, err %v", len(rows), err)
	}
}

func TestLogin_WrongPasswordAndUnknownEmailAreIndistinguishable(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTestService(t)
	if _, err := svc.Bootstrap(ctx, "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}

	_, _, errWrong := svc.Login(ctx, "root@example.com", "totally wrong pw", loginIP)
	_, _, errUnknown := svc.Login(ctx, "ghost@example.com", "totally wrong pw", loginIP)
	wantClass(t, errWrong, xerrors.ClassUnauthenticated)
	wantClass(t, errUnknown, xerrors.ClassUnauthenticated)
	if errWrong.Error() != errUnknown.Error() {
		t.Fatalf("credential errors must be identical: %q vs %q", errWrong, errUnknown)
	}
}

func TestLogin_AccountLockoutAndRecovery(t *testing.T) {
	ctx := context.Background()
	svc, pool, fake := newTestService(t)
	if _, err := svc.Bootstrap(ctx, "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		_, _, err := svc.Login(ctx, "root@example.com", "wrong password!!", loginIP)
		wantClass(t, err, xerrors.ClassUnauthenticated)
	}
	// 5 consecutive failures → locked even with the CORRECT password.
	_, _, err := svc.Login(ctx, "root@example.com", testPassword, loginIP)
	wantClass(t, err, xerrors.ClassRateLimited)

	rows, aerr := audit.New(pool).ListAuditEntriesByAction(ctx, "auth.rate_limited")
	if aerr != nil || len(rows) != 1 {
		t.Fatalf("auth.rate_limited audit rows = %d (want 1: on transition only), err %v", len(rows), aerr)
	}

	// Lock at 5 failures is 30s; past it, login succeeds and resets.
	fake.Advance(31 * time.Second)
	if _, _, err := svc.Login(ctx, "root@example.com", testPassword, loginIP); err != nil {
		t.Fatal(err)
	}
	// Counter was reset: 4 fresh failures don't lock.
	for i := 0; i < 4; i++ {
		_, _, err := svc.Login(ctx, "root@example.com", "wrong password!!", loginIP)
		wantClass(t, err, xerrors.ClassUnauthenticated)
	}
	if _, _, err := svc.Login(ctx, "root@example.com", testPassword, loginIP); err != nil {
		t.Fatal(err)
	}
}

func TestLogin_IPLockoutSpansAccounts(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTestService(t)
	if _, err := svc.Bootstrap(ctx, "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}

	// 30 failures from one IP across many (unknown) accounts.
	for i := 0; i < 30; i++ {
		email := fmt.Sprintf("spray%d@example.com", i)
		_, _, err := svc.Login(ctx, email, "wrong password!!", loginIP)
		wantClass(t, err, xerrors.ClassUnauthenticated)
	}
	_, _, err := svc.Login(ctx, "root@example.com", testPassword, loginIP)
	wantClass(t, err, xerrors.ClassRateLimited)

	// A different IP is unaffected.
	if _, _, err := svc.Login(ctx, "root@example.com", testPassword, "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 6: Run to verify it fails**

Run: `cd backend && go test -tags integration -count=1 ./internal/identity/ -run TestLogin -v`
Expected: FAIL — `Login` undefined.

- [ ] **Step 7: Implement login**

`backend/internal/identity/login.go`:

```go
package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/identity/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

var errBadCredentials = xerrors.E(xerrors.ClassUnauthenticated, "invalid email or password")

var errLoginLocked = xerrors.E(xerrors.ClassRateLimited, "too many failed attempts, try again later")

// dummyVerify equalizes timing for unknown emails: one argon2 run happens
// on every credential failure path.
var (
	dummyOnce sync.Once
	dummyPHC  string
)

func dummyVerify(pw string) {
	dummyOnce.Do(func() {
		dummyPHC, _ = HashPassword("caesar.dummy.timing.equalizer")
	})
	_, _ = VerifyPassword(dummyPHC, pw)
}

func rateStateFromRow(row store.LoginRate) rateState {
	return rateState{Failures: int(row.Failures), WindowStart: row.WindowStartedAt, LockedUntil: row.LockedUntil}
}

// loadRate reads a scope's row under FOR UPDATE (absent row = zero state).
func loadRate(ctx context.Context, q *store.Queries, scope string) (rateState, error) {
	row, err := q.GetLoginRateForUpdate(ctx, scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return rateState{}, nil
	}
	if err != nil {
		return rateState{}, fmt.Errorf("identity: rate lookup: %w", err)
	}
	return rateStateFromRow(row), nil
}

func saveRate(ctx context.Context, q *store.Queries, scope string, st rateState) error {
	err := q.UpsertLoginRate(ctx, store.UpsertLoginRateParams{
		Scope: scope, Failures: int32(st.Failures), WindowStartedAt: st.WindowStart, LockedUntil: st.LockedUntil,
	})
	if err != nil {
		return fmt.Errorf("identity: rate upsert: %w", err)
	}
	return nil
}

// Login authenticates email+password from ip. One transaction: rate rows
// locked FOR UPDATE (serializing concurrent attempts per scope), failure
// counters and the session mint commit atomically with their audit rows.
func (s *Service) Login(ctx context.Context, email, password, ip string) (User, string, error) {
	if len(password) > MaxPasswordLen {
		return User{}, "", errBadCredentials
	}
	accountScope := "account:" + strings.ToLower(email)
	ipScope := "ip:" + ip
	now := s.clock.Now()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, "", fmt.Errorf("identity: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	// Deterministic lock order: account scope, then ip scope.
	accountRate, err := loadRate(ctx, q, accountScope)
	if err != nil {
		return User{}, "", err
	}
	ipRate, err := loadRate(ctx, q, ipScope)
	if err != nil {
		return User{}, "", err
	}
	if rateBlocked(accountRate, now) || rateBlocked(ipRate, now) {
		return User{}, "", errLoginLocked
	}

	row, lookupErr := q.GetUserByEmail(ctx, email)
	authenticated := false
	if lookupErr == nil {
		ok, verr := VerifyPassword(row.PasswordHash, password)
		if verr != nil {
			return User{}, "", fmt.Errorf("identity: verify: %w", verr)
		}
		authenticated = ok && row.Active
	} else if errors.Is(lookupErr, pgx.ErrNoRows) {
		dummyVerify(password)
	} else {
		return User{}, "", fmt.Errorf("identity: user lookup: %w", lookupErr)
	}

	if !authenticated {
		nextAccount := rateFail(rateAccount, accountRate, now)
		nextIP := rateFail(rateIP, ipRate, now)
		if err := saveRate(ctx, q, accountScope, nextAccount); err != nil {
			return User{}, "", err
		}
		if err := saveRate(ctx, q, ipScope, nextIP); err != nil {
			return User{}, "", err
		}
		entries := []audit.Entry{{
			Action: "auth.login_failed", SubjectKind: "user", SubjectID: strings.ToLower(email),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"ip": ip},
		}}
		// Audit the lock only on the transition into it.
		newlyLocked := (rateBlocked(nextAccount, now) && !rateBlocked(accountRate, now)) ||
			(rateBlocked(nextIP, now) && !rateBlocked(ipRate, now))
		if newlyLocked {
			entries = append(entries, audit.Entry{
				Action: "auth.rate_limited", SubjectKind: "user", SubjectID: strings.ToLower(email),
				CorrelationID: httpx.CorrelationID(ctx),
				Detail:        map[string]any{"ip": ip},
			})
		}
		for _, e := range entries {
			if err := s.audit.Record(ctx, tx, e); err != nil {
				return User{}, "", err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return User{}, "", fmt.Errorf("identity: commit: %w", err)
		}
		return User{}, "", errBadCredentials
	}

	// Success: reset the account counter (the IP window stands — a spray
	// attack doesn't earn credit from one valid login).
	if err := q.DeleteLoginRate(ctx, accountScope); err != nil {
		return User{}, "", fmt.Errorf("identity: rate reset: %w", err)
	}
	token, err := s.mintSession(ctx, q, row.ID)
	if err != nil {
		return User{}, "", err
	}
	err = s.audit.Record(ctx, tx, audit.Entry{
		Actor:  uuid.NullUUID{UUID: row.ID, Valid: true},
		Action: "auth.login", SubjectKind: "user", SubjectID: row.ID.String(),
		CorrelationID: httpx.CorrelationID(ctx),
		Detail:        map[string]any{"ip": ip},
	})
	if err != nil {
		return User{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, "", fmt.Errorf("identity: commit: %w", err)
	}
	return userFromRow(row), token, nil
}
```

(An inactive user takes the failure path — indistinguishable from bad credentials, no account-state oracle.)

- [ ] **Step 8: Run to verify it passes**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/identity/ -v`
Expected: PASS — Tasks 8–10 suites all green.

- [ ] **Step 9: Commit**

```bash
git add backend/internal/identity
git commit -m "feat(backend): login with dual-scope rate limiting"
```

---

### Task 11: HTTP API — operation table, auth middleware, identity endpoints

The route table (`httpx.Operation`) is the single source of truth for both mux registration and (Task 13) OpenAPI generation — pattern, DTO types, and status are declared once. Handlers are thin: decode strict JSON → service → encode.

**Files:**
- Create: `backend/internal/platform/httpx/operation.go`
- Create: `backend/internal/platform/httpx/origin.go`
- Create: `backend/internal/identity/api/api.go`
- Create: `backend/internal/identity/api/dto.go`
- Test: `backend/internal/platform/httpx/origin_test.go`
- Test: `backend/internal/identity/api/api_integration_test.go`

**Interfaces:**
- Consumes: the full identity `Service` (Tasks 8–10), `httpx.Problem`/`JSON` (4).
- Produces (exact — Tasks 12–13 consume):
  - `httpx.Operation{Pattern, ID, Summary string; Request, Response any; Status int; Handler http.Handler}`
  - `httpx.Mount(mux *http.ServeMux, ops []Operation)`
  - `httpx.CheckOrigin(appOrigin string) Middleware` — rejects unsafe-method requests whose `Origin` header is present and ≠ appOrigin (absent Origin passes: non-browser clients; SameSite=Lax is the other CSRF layer).
  - `api.New(svc *identity.Service, sessionTTL time.Duration) *API` (package `api`, path `internal/identity/api`)
  - `api.Operations(a *API) []httpx.Operation` — the eight operations listed below.
  - `api.SessionCookie = "__Host-caesar_session"`
- Routes (fixed): `POST /api/v1/auth/login` (public) · `POST /api/v1/auth/logout` · `GET /api/v1/auth/me` · `PUT /api/v1/auth/password` · `POST /api/v1/users` (admin) · `GET /api/v1/users` (admin) · `GET /api/v1/users/{id}` (admin) · `PATCH /api/v1/users/{id}` (admin).

- [ ] **Step 1: Write the failing origin-middleware test**

`backend/internal/platform/httpx/origin_test.go`:

```go
package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/httpx"
)

func TestCheckOrigin(t *testing.T) {
	const app = "https://caesar.example.com"
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := httpx.CheckOrigin(app)(ok)

	tests := []struct {
		name, method, origin string
		want                 int
	}{
		{"safe method any origin", http.MethodGet, "https://evil.example.com", http.StatusNoContent},
		{"unsafe same origin", http.MethodPost, app, http.StatusNoContent},
		{"unsafe absent origin (curl)", http.MethodPost, "", http.StatusNoContent},
		{"unsafe cross origin", http.MethodPost, "https://evil.example.com", http.StatusForbidden},
		{"delete cross origin", http.MethodDelete, "https://evil.example.com", http.StatusForbidden},
		{"null origin", http.MethodPost, "null", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, "/api/v1/x", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}
```

Run: `cd backend && go test ./internal/platform/httpx/ -run TestCheckOrigin` — FAIL (undefined).

- [ ] **Step 2: Implement Operation, Mount, CheckOrigin**

`backend/internal/platform/httpx/operation.go`:

```go
package httpx

import "net/http"

// Operation declares one API endpoint once: the mux mounts Handler at
// Pattern, and the contracts generator (internal/contracts) reflects
// Request/Response/Status into the OpenAPI document. Route and contract
// can never drift because they are the same value.
type Operation struct {
	Pattern  string // ServeMux pattern: "POST /api/v1/auth/login"
	ID       string // OpenAPI operationId
	Summary  string
	Request  any // zero value of the request body struct; nil = no body
	Response any // zero value of the success response struct; nil = no body
	Status   int // success status code
	Handler  http.Handler
}

// Mount registers every operation on mux.
func Mount(mux *http.ServeMux, ops []Operation) {
	for _, op := range ops {
		mux.Handle(op.Pattern, op.Handler)
	}
}
```

`backend/internal/platform/httpx/origin.go`:

```go
package httpx

import (
	"net/http"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// CheckOrigin is the CSRF belt over SameSite=Lax braces (spec §4): on
// unsafe methods, a present Origin header must equal the app origin.
// Absent Origin passes — non-browser clients don't send it, and Lax
// cookies already don't ride cross-site POSTs.
func CheckOrigin(appOrigin string) Middleware {
	unsafe := map[string]bool{
		http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if unsafe[r.Method] {
				if o := r.Header.Get("Origin"); o != "" && o != appOrigin {
					Problem(w, r, xerrors.E(xerrors.ClassForbidden, "cross-origin request rejected"))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
```

Run: `cd backend && go test ./internal/platform/httpx/` — PASS.

- [ ] **Step 3: Write the DTOs**

`backend/internal/identity/api/dto.go`:

```go
// Package api is identity's HTTP surface. DTO structs here are the source
// the OpenAPI document is generated from (Go types → contract, spec §4).
package api

import (
	"time"

	"github.com/giiorg/caesar/backend/internal/identity"
)

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type UserResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type GrantResponse struct {
	ProjectID string `json:"project_id"`
	Level     string `json:"level"`
}

type MeResponse struct {
	User   UserResponse    `json:"user"`
	Grants []GrantResponse `json:"grants"`
}

type UserListResponse struct {
	Users []UserResponse `json:"users"`
}

type CreateUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

type UpdateUserRequest struct {
	Role   *string `json:"role,omitempty"`
	Active *bool   `json:"active,omitempty"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func userResponse(u identity.User) UserResponse {
	return UserResponse{
		ID: u.ID.String(), Email: u.Email, Role: string(u.Role), Active: u.Active,
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
}
```

- [ ] **Step 4: Write the failing API integration test**

`backend/internal/identity/api/api_integration_test.go`:

```go
//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/identity"
	identityapi "github.com/giiorg/caesar/backend/internal/identity/api"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

const testPassword = "a long enough password"

// newTestServer mirrors app wiring: correlation → access log → origin →
// mounted operations. The app-level e2e (Task 12) covers the real Routes.
func newTestServer(t *testing.T) (*httptest.Server, *identity.Service) {
	t.Helper()
	pool := testdb.New(t)
	fake := clock.NewFake(time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	svc := identity.NewService(pool, fake, audit.NewRecorder(fake), identity.Params{
		SessionIdleTTL: 14 * 24 * time.Hour, SessionAbsoluteTTL: 90 * 24 * time.Hour,
	})
	a := identityapi.New(svc, 90*24*time.Hour)
	mux := http.NewServeMux()
	httpx.Mount(mux, identityapi.Operations(a))
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := httptest.NewServer(httpx.Chain(mux, httpx.WithCorrelationID, httpx.AccessLog(log), httpx.CheckOrigin("http://caesar.test")))
	t.Cleanup(srv.Close)
	return srv, svc
}

func do(t *testing.T, method, url, cookie string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "" {
		req.Header.Set("Cookie", identityapi.SessionCookie+"="+cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decodeInto[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// sessionCookie extracts the session token from Set-Cookie and asserts
// the pinned attributes.
func sessionCookie(t *testing.T, resp *http.Response) string {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == identityapi.SessionCookie {
			if !c.HttpOnly || !c.Secure || c.Path != "/" || c.SameSite != http.SameSiteLaxMode {
				t.Fatalf("cookie attributes wrong: %+v", c)
			}
			return c.Value
		}
	}
	t.Fatal("no session cookie set")
	return ""
}

func bootstrapAndLogin(t *testing.T, srv *httptest.Server, svc *identity.Service) string {
	t.Helper()
	if _, err := svc.Bootstrap(context.Background(), "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}
	resp := do(t, http.MethodPost, srv.URL+"/api/v1/auth/login", "", identityapi.LoginRequest{Email: "root@example.com", Password: testPassword})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	return sessionCookie(t, resp)
}

func TestAPI_LoginMeLogout(t *testing.T) {
	srv, svc := newTestServer(t)
	token := bootstrapAndLogin(t, srv, svc)

	resp := do(t, http.MethodGet, srv.URL+"/api/v1/auth/me", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me status = %d", resp.StatusCode)
	}
	me := decodeInto[identityapi.MeResponse](t, resp)
	if me.User.Email != "root@example.com" || me.User.Role != "admin" {
		t.Fatalf("me = %+v", me)
	}

	resp = do(t, http.MethodPost, srv.URL+"/api/v1/auth/logout", token, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status = %d", resp.StatusCode)
	}
	resp = do(t, http.MethodGet, srv.URL+"/api/v1/auth/me", token, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me after logout = %d", resp.StatusCode)
	}
}

func TestAPI_ProblemEnvelopeShape(t *testing.T) {
	srv, svc := newTestServer(t)
	if _, err := svc.Bootstrap(context.Background(), "root@example.com", testPassword); err != nil {
		t.Fatal(err)
	}
	resp := do(t, http.MethodPost, srv.URL+"/api/v1/auth/login", "", identityapi.LoginRequest{Email: "root@example.com", Password: "wrong password!!"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q", ct)
	}
	pd := decodeInto[httpx.ProblemDetails](t, resp)
	if pd.Type != "/errors/unauthenticated" || pd.CorrelationID == "" {
		t.Fatalf("problem = %+v", pd)
	}
}

func TestAPI_UserAdministration(t *testing.T) {
	srv, svc := newTestServer(t)
	admin := bootstrapAndLogin(t, srv, svc)

	// Create a member.
	resp := do(t, http.MethodPost, srv.URL+"/api/v1/users", admin, identityapi.CreateUserRequest{
		Email: "m@example.com", Password: testPassword, Role: "member",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	member := decodeInto[identityapi.UserResponse](t, resp)

	// Member logs in; admin endpoints are forbidden to them.
	resp = do(t, http.MethodPost, srv.URL+"/api/v1/auth/login", "", identityapi.LoginRequest{Email: "m@example.com", Password: testPassword})
	memberTok := sessionCookie(t, resp)
	resp = do(t, http.MethodGet, srv.URL+"/api/v1/users", memberTok, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member list users = %d", resp.StatusCode)
	}

	// Unknown JSON fields are rejected (strict decode).
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/users", strings.NewReader(`{"email":"x@example.com","password":"long enough password","role":"member","surprise":true}`))
	req.Header.Set("Cookie", identityapi.SessionCookie+"="+admin)
	rawResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer rawResp.Body.Close()
	if rawResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", rawResp.StatusCode)
	}

	// Admin lists, gets, patches.
	resp = do(t, http.MethodGet, srv.URL+"/api/v1/users", admin, nil)
	if got := decodeInto[identityapi.UserListResponse](t, resp); len(got.Users) != 2 {
		t.Fatalf("users = %d, want 2", len(got.Users))
	}
	resp = do(t, http.MethodGet, srv.URL+"/api/v1/users/"+member.ID, admin, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d", resp.StatusCode)
	}
	role := "admin"
	resp = do(t, http.MethodPatch, srv.URL+"/api/v1/users/"+member.ID, admin, identityapi.UpdateUserRequest{Role: &role})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch status = %d", resp.StatusCode)
	}
	// Privilege change revoked the member's session.
	resp = do(t, http.MethodGet, srv.URL+"/api/v1/auth/me", memberTok, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("member session after promote = %d", resp.StatusCode)
	}
}

func TestAPI_ChangePasswordRotatesCookie(t *testing.T) {
	srv, svc := newTestServer(t)
	token := bootstrapAndLogin(t, srv, svc)

	resp := do(t, http.MethodPut, srv.URL+"/api/v1/auth/password", token, identityapi.ChangePasswordRequest{
		CurrentPassword: testPassword, NewPassword: "an even better password",
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("change password status = %d", resp.StatusCode)
	}
	fresh := sessionCookie(t, resp)
	if fresh == token {
		t.Fatal("cookie must rotate")
	}
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/auth/me", token, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("old session must be dead")
	}
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/auth/me", fresh, nil); resp.StatusCode != http.StatusOK {
		t.Fatal("fresh session must work")
	}
}
```

- [ ] **Step 5: Run to verify it fails**

Run: `cd backend && go test -tags integration -count=1 ./internal/identity/api/ -v`
Expected: FAIL — `api.New`, `api.Operations` undefined.

- [ ] **Step 6: Implement the API**

`backend/internal/identity/api/api.go`:

```go
package api

import (
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// SessionCookie is the pinned __Host- session cookie name (spec §6).
const SessionCookie = "__Host-caesar_session"

type API struct {
	svc        *identity.Service
	sessionTTL time.Duration // cookie Max-Age; server-side expiry is authoritative
}

func New(svc *identity.Service, sessionTTL time.Duration) *API {
	return &API{svc: svc, sessionTTL: sessionTTL}
}

// Operations is the single route table: mux registration (httpx.Mount)
// and OpenAPI generation (internal/contracts) both consume it.
func Operations(a *API) []httpx.Operation {
	return []httpx.Operation{
		{
			Pattern: "POST /api/v1/auth/login", ID: "login", Summary: "Authenticate and set the session cookie",
			Request: LoginRequest{}, Response: UserResponse{}, Status: http.StatusOK,
			Handler: http.HandlerFunc(a.handleLogin),
		},
		{
			Pattern: "POST /api/v1/auth/logout", ID: "logout", Summary: "Revoke the current session",
			Status:  http.StatusNoContent,
			Handler: a.authenticated(a.handleLogout),
		},
		{
			Pattern: "GET /api/v1/auth/me", ID: "me", Summary: "Current user with project grants",
			Response: MeResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleMe),
		},
		{
			Pattern: "PUT /api/v1/auth/password", ID: "changePassword", Summary: "Change own password (revokes all sessions, rotates the cookie)",
			Request: ChangePasswordRequest{}, Status: http.StatusNoContent,
			Handler: a.authenticated(a.handleChangePassword),
		},
		{
			Pattern: "POST /api/v1/users", ID: "createUser", Summary: "Create a user (admin)",
			Request: CreateUserRequest{}, Response: UserResponse{}, Status: http.StatusCreated,
			Handler: a.adminOnly(a.handleCreateUser),
		},
		{
			Pattern: "GET /api/v1/users", ID: "listUsers", Summary: "List users (admin)",
			Response: UserListResponse{}, Status: http.StatusOK,
			Handler: a.adminOnly(a.handleListUsers),
		},
		{
			Pattern: "GET /api/v1/users/{id}", ID: "getUser", Summary: "Get a user (admin)",
			Response: UserResponse{}, Status: http.StatusOK,
			Handler: a.adminOnly(a.handleGetUser),
		},
		{
			Pattern: "PATCH /api/v1/users/{id}", ID: "updateUser", Summary: "Update role or active flag (admin)",
			Request: UpdateUserRequest{}, Response: UserResponse{}, Status: http.StatusOK,
			Handler: a.adminOnly(a.handleUpdateUser),
		},
	}
}

type sessionHandler func(w http.ResponseWriter, r *http.Request, sess identity.AuthSession)

func (a *API) authenticated(next sessionHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(SessionCookie)
		if err != nil {
			httpx.Problem(w, r, xerrors.E(xerrors.ClassUnauthenticated, "authentication required"))
			return
		}
		sess, err := a.svc.AuthenticateSession(r.Context(), c.Value)
		if err != nil {
			httpx.Problem(w, r, err)
			return
		}
		next(w, r, sess)
	})
}

func (a *API) adminOnly(next sessionHandler) http.Handler {
	return a.authenticated(func(w http.ResponseWriter, r *http.Request, sess identity.AuthSession) {
		if sess.Role != identity.RoleAdmin {
			httpx.Problem(w, r, xerrors.E(xerrors.ClassForbidden, "admin role required"))
			return
		}
		next(w, r, sess)
	})
}

func decode[T any](r *http.Request) (T, error) {
	var v T
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, xerrors.Wrap(xerrors.ClassInvalid, "invalid request body", err)
	}
	return v, nil
}

func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.UUID{}, xerrors.E(xerrors.ClassNotFound, "no such user")
	}
	return id, nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *API) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/",
		MaxAge: int(a.sessionTTL.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	req, err := decode[LoginRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	user, token, err := a.svc.Login(r.Context(), req.Email, req.Password, clientIP(r))
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	a.setSessionCookie(w, token)
	httpx.JSON(w, http.StatusOK, userResponse(user))
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request, sess identity.AuthSession) {
	if err := a.svc.Logout(r.Context(), sess); err != nil {
		httpx.Problem(w, r, err)
		return
	}
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleMe(w http.ResponseWriter, r *http.Request, sess identity.AuthSession) {
	user, err := a.svc.GetUser(r.Context(), sess.UserID)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	grants, err := a.svc.GrantsForUser(r.Context(), sess.UserID)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := MeResponse{User: userResponse(user), Grants: make([]GrantResponse, 0, len(grants))}
	for _, g := range grants {
		out.Grants = append(out.Grants, GrantResponse{ProjectID: g.ProjectID.String(), Level: string(g.Level)})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleChangePassword(w http.ResponseWriter, r *http.Request, sess identity.AuthSession) {
	req, err := decode[ChangePasswordRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	token, err := a.svc.ChangePassword(r.Context(), sess, req.CurrentPassword, req.NewPassword)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	a.setSessionCookie(w, token)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleCreateUser(w http.ResponseWriter, r *http.Request, sess identity.AuthSession) {
	req, err := decode[CreateUserRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	user, err := a.svc.CreateUser(r.Context(), sess.Principal, req.Email, req.Password, identity.Role(req.Role))
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, userResponse(user))
}

func (a *API) handleListUsers(w http.ResponseWriter, r *http.Request, _ identity.AuthSession) {
	users, err := a.svc.ListUsers(r.Context())
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := UserListResponse{Users: make([]UserResponse, 0, len(users))}
	for _, u := range users {
		out.Users = append(out.Users, userResponse(u))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleGetUser(w http.ResponseWriter, r *http.Request, _ identity.AuthSession) {
	id, err := pathID(r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	user, err := a.svc.GetUser(r.Context(), id)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, userResponse(user))
}

func (a *API) handleUpdateUser(w http.ResponseWriter, r *http.Request, sess identity.AuthSession) {
	id, err := pathID(r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	req, err := decode[UpdateUserRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	upd := identity.UserUpdate{Active: req.Active}
	if req.Role != nil {
		role := identity.Role(*req.Role)
		upd.Role = &role
	}
	user, err := a.svc.UpdateUser(r.Context(), sess.Principal, id, upd)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, userResponse(user))
}
```

(`http.MaxBytesReader(nil, …)` is valid — the writer parameter is optional and only used to flag the response.)

- [ ] **Step 7: Run to verify it passes**

Run: `cd backend && go test -race -tags integration -count=1 ./internal/identity/... -v`
Expected: PASS.

- [ ] **Step 8: Analyzers + arch stay green**

Run: `task backend:analyze && cd backend && go test ./internal/arch/`
Expected: clean — `internal/identity/api` is inside the identity context; nothing new crosses a boundary.

- [ ] **Step 9: Commit**

```bash
git add backend/internal/platform/httpx backend/internal/identity
git commit -m "feat(backend): identity http api with operation route table"
```

---
### Task 12: Config, app wiring, `caesar admin bootstrap`

New config fields land in the same commit as their read sites (no-dead-config): `server.app_origin` feeds `CheckOrigin`, `auth.*` TTLs feed the identity service. The bootstrap subcommand creates the first admin through the normal domain service (spec §6).

**Files:**
- Modify: `backend/internal/platform/config/config.go`
- Modify: `backend/internal/app/main.go` (usage + `admin` dispatch; `Main` gains a stdin parameter)
- Modify: `backend/cmd/caesar/main.go` (pass `os.Stdin`)
- Modify: `backend/internal/app/server.go` (Routes mounts operations + /api fallback)
- Modify: `backend/internal/app/serve.go` (wire service)
- Create: `backend/internal/app/admin.go`
- Test: `backend/internal/platform/config/config_test.go` (extend)
- Test: `backend/internal/app/admin_integration_test.go`
- Test: `backend/internal/app/api_e2e_integration_test.go`

**Interfaces:**
- Consumes: everything from Tasks 3–11.
- Produces:
  - `config.Auth{SessionIdleTTL, SessionAbsoluteTTL Duration}` on `Config` (json `auth`), `config.Server.AppOrigin` (json `app_origin`). Defaults: `http://localhost:8080`, `336h`, `2160h`. Validation: `app_origin` non-empty, idle > 0, absolute ≥ idle.
  - `app.Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int` — signature change; `cmd/caesar` passes `os.Stdin`.
  - `app.Routes(log *slog.Logger, db Pinger, ops []httpx.Operation, appOrigin string) http.Handler` — signature change; unknown `/api/…` paths return a problem+json 404. (Stdlib 405s on matched paths remain plain text — accepted V1 limitation, noted in README.)
  - `caesar admin bootstrap -email <email> [-config path]` — password on stdin, refuses when an admin exists (exit 1), usage errors exit 2.

- [ ] **Step 1: Extend config with tests first**

Add cases to the existing table in `config_test.go` (follow its established style — read it first):
- defaults: absent `auth`/`app_origin` → `AppOrigin == "http://localhost:8080"`, `SessionIdleTTL == Duration(14*24*time.Hour)`, `SessionAbsoluteTTL == Duration(90*24*time.Hour)`.
- explicit values parse (`{"auth":{"session_idle_ttl":"1h","session_absolute_ttl":"2h"}}`).
- rejection: `session_absolute_ttl` < `session_idle_ttl` → error mentioning `auth.session_absolute_ttl`; zero/negative idle → error; empty `app_origin` (explicit `"app_origin": ""`) → error.

Run: `cd backend && go test ./internal/platform/config/` — FAIL.

Then implement in `config.go`:

```go
type Server struct {
	Addr            string   `json:"addr"`
	ShutdownTimeout Duration `json:"shutdown_timeout"`
	AppOrigin       string   `json:"app_origin"`
}

type Auth struct {
	SessionIdleTTL     Duration `json:"session_idle_ttl"`
	SessionAbsoluteTTL Duration `json:"session_absolute_ttl"`
}
```

Add `Auth Auth \`json:"auth"\`` to `Config`; defaults `AppOrigin: "http://localhost:8080"`, `Auth{SessionIdleTTL: Duration(14 * 24 * time.Hour), SessionAbsoluteTTL: Duration(90 * 24 * time.Hour)}`; validation lines appended to `validate()`:

```go
	if c.Server.AppOrigin == "" {
		problems = append(problems, "server.app_origin must not be empty")
	}
	if c.Auth.SessionIdleTTL.Std() <= 0 {
		problems = append(problems, "auth.session_idle_ttl must be positive")
	}
	if c.Auth.SessionAbsoluteTTL.Std() < c.Auth.SessionIdleTTL.Std() {
		problems = append(problems, "auth.session_absolute_ttl must be >= auth.session_idle_ttl")
	}
```

Run: `cd backend && go test ./internal/platform/config/` — PASS.

- [ ] **Step 2: Rewire Routes and serve**

`server.go` — new signature and /api fallback (update the existing healthz tests' `Routes(...)` call sites with `nil, "http://localhost:8080"`):

```go
// Routes assembles caesar's HTTP surface: health, the mounted /api/v1
// operations, and a problem+json 404 for unknown API paths.
func Routes(log *slog.Logger, db Pinger, ops []httpx.Operation, appOrigin string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", handleHealthz(db))
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassNotFound, "no such endpoint"))
	}))
	httpx.Mount(mux, ops)
	return httpx.Chain(mux, httpx.WithCorrelationID, httpx.AccessLog(log), httpx.CheckOrigin(appOrigin))
}
```

`serve.go` — inside `runServe`, after migrations, before the `http.Server`:

```go
	svc := identity.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), identity.Params{
		SessionIdleTTL:     cfg.Auth.SessionIdleTTL.Std(),
		SessionAbsoluteTTL: cfg.Auth.SessionAbsoluteTTL.Std(),
	})
	ops := identityapi.Operations(identityapi.New(svc, cfg.Auth.SessionAbsoluteTTL.Std()))
```

and `Handler: Routes(log, pool, ops, cfg.Server.AppOrigin)`. Import `identityapi "github.com/giiorg/caesar/backend/internal/identity/api"`.

- [ ] **Step 3: Thread stdin through Main**

`app/main.go`: `func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int`; add to the switch:

```go
	case "admin":
		return runAdminCmd(args, stdin, stdout, stderr)
```

Usage block gains `  admin     administrative commands (bootstrap)`. `cmd/caesar/main.go`: pass `os.Stdin` (this file already owns the process boundary; envaccess only polices env functions). Update every existing `app.Main(...)` call site — Plan 01's tests in `internal/app` call the old three-argument form; pass them `strings.NewReader("")` (always a non-nil reader, never `nil`).

- [ ] **Step 4: Write the failing bootstrap-command test**

`backend/internal/app/admin_integration_test.go`:

```go
//go:build integration

package app_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/app"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

func writeConfig(t *testing.T, dbURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "caesar.json")
	body := fmt.Sprintf(`{"database": {"url": %q}}`, dbURL)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAdminBootstrap_CreatesFirstAdminExactlyOnce(t *testing.T) {
	cfgPath := writeConfig(t, testdb.NewURL(t))

	var out, errOut bytes.Buffer
	code := app.Main(
		[]string{"admin", "bootstrap", "-config", cfgPath, "-email", "root@example.com"},
		strings.NewReader("a long enough password\n"), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "root@example.com") {
		t.Fatalf("stdout = %q", out.String())
	}

	// Second run refuses.
	out.Reset()
	errOut.Reset()
	code = app.Main(
		[]string{"admin", "bootstrap", "-config", cfgPath, "-email", "other@example.com"},
		strings.NewReader("a long enough password\n"), &out, &errOut)
	if code != 1 {
		t.Fatalf("second bootstrap exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "admin already exists") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestAdminBootstrap_UsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		in   string
	}{
		{"missing email", []string{"admin", "bootstrap"}, "pw is long enough\n"},
		{"unknown subcommand", []string{"admin", "frobnicate"}, ""},
		{"empty password", []string{"admin", "bootstrap", "-email", "a@b.co"}, "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := app.Main(tt.args, strings.NewReader(tt.in), &out, &errOut); code != 2 {
				t.Fatalf("exit = %d, want 2 (stderr: %s)", code, errOut.String())
			}
		})
	}
}
```

Run: `cd backend && go test -tags integration -count=1 ./internal/app/ -run TestAdminBootstrap -v` — FAIL.

- [ ] **Step 5: Implement the admin command**

`backend/internal/app/admin.go`:

```go
package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
)

const adminUsage = `usage: caesar admin bootstrap -email <email> [-config path]

Reads the password from stdin (pipe or interactive) and creates the first
admin through the normal domain service. Refuses once any admin exists.
`

func runAdminCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "bootstrap" {
		fmt.Fprint(stderr, adminUsage)
		return 2
	}
	fs := flag.NewFlagSet("admin bootstrap", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "caesar.json", "path to config file")
	email := fs.String("email", "", "email of the first admin")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *email == "" {
		fmt.Fprint(stderr, adminUsage)
		return 2
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "read password from stdin:", err)
		return 1
	}
	password := strings.TrimRight(string(raw), "\r\n")
	if password == "" {
		fmt.Fprint(stderr, "empty password on stdin\n\n"+adminUsage)
		return 2
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
	if err := pg.Migrate(ctx, pool, pg.Migrations()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	svc := identity.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), identity.Params{
		SessionIdleTTL:     cfg.Auth.SessionIdleTTL.Std(),
		SessionAbsoluteTTL: cfg.Auth.SessionAbsoluteTTL.Std(),
	})
	user, err := svc.Bootstrap(ctx, *email, password)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "bootstrap: created admin %s (%s)\n", user.Email, user.ID)
	return 0
}
```

Note: `config.Load` requires `database.url` — the test config supplies it; empty-password and usage checks run before config loading so they need no database. Order the checks exactly as above.

Run the Task 4 tests again: `cd backend && go test -tags integration -count=1 ./internal/app/ -run TestAdminBootstrap -v` — PASS.

- [ ] **Step 6: Write the end-to-end wiring test**

`backend/internal/app/api_e2e_integration_test.go`:

```go
//go:build integration

package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/app"
	"github.com/giiorg/caesar/backend/internal/identity"
	identityapi "github.com/giiorg/caesar/backend/internal/identity/api"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

// The month-one auth loop, through the real Routes wiring: bootstrap →
// login → me → unknown API path is problem+json.
func TestAppRoutes_AuthLoopEndToEnd(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.NewURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	svc := identity.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), identity.Params{
		SessionIdleTTL: 14 * 24 * time.Hour, SessionAbsoluteTTL: 90 * 24 * time.Hour,
	})
	if _, err := svc.Bootstrap(ctx, "root@example.com", "a long enough password"); err != nil {
		t.Fatal(err)
	}

	ops := identityapi.Operations(identityapi.New(svc, 90*24*time.Hour))
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := httptest.NewServer(app.Routes(log, pool, ops, "http://caesar.test"))
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]string{"email": "root@example.com", "password": "a long enough password"})
	resp, err := http.Post(srv.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	var token string
	for _, c := range resp.Cookies() {
		if c.Name == identityapi.SessionCookie {
			token = c.Value
		}
	}
	if token == "" {
		t.Fatal("no session cookie")
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/auth/me", nil)
	req.Header.Set("Cookie", identityapi.SessionCookie+"="+token)
	me, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer me.Body.Close()
	if me.StatusCode != http.StatusOK {
		t.Fatalf("me = %d", me.StatusCode)
	}

	// Unknown API path → problem+json 404, not the stdlib text page.
	nf, err := http.Get(srv.URL + "/api/v1/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer nf.Body.Close()
	if nf.StatusCode != http.StatusNotFound || nf.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("unknown path: %d %s", nf.StatusCode, nf.Header.Get("Content-Type"))
	}
}
```

Run: `cd backend && go test -race -tags integration -count=1 ./internal/app/ -v`
Expected: PASS (this plus the updated healthz tests and Task 5's admin tests).

- [ ] **Step 7: Full gate + dead-config proof**

Run: `task backend:ci`
Expected: green. In particular `internal/arch`'s dead-config test now proves `app_origin` and both TTLs have non-test read sites (serve.go/admin.go) — the Task 2 strengthening is live against real new fields.

- [ ] **Step 8: Commit**

```bash
git add backend/cmd backend/internal/app backend/internal/platform/config
git commit -m "feat(backend): wire identity into the control plane and admin bootstrap"
```

---

### Task 13: OpenAPI 3.1 generation from the operation table

`internal/contracts` is tooling, not a bounded context (like `internal/analyzers`): it imports context API packages to reflect their route tables into `backend/api/openapi.json`. The arch rules learn that.

**Files:**
- Create: `backend/internal/contracts/contracts.go`
- Create: `backend/internal/contracts/cmd/openapigen/main.go`
- Create: `backend/api/openapi.json` (generated, committed)
- Modify: `backend/internal/arch/rules.go` (`nonContext` + "contracts")
- Modify: `backend/internal/analyzers/txboundary/txboundary.go` (`nonContext` + "contracts")
- Modify: `Taskfile.yml` (`contracts:openapi`)
- Test: `backend/internal/contracts/contracts_test.go`

**Interfaces:**
- Consumes: `identityapi.Operations(api.New(nil, 0))` — handlers are never invoked, only metadata read.
- Produces: `contracts.Build() ([]byte, error)` returning the OpenAPI 3.1 JSON document; `task contracts:openapi` writes it to `backend/api/openapi.json`; Task 14's TS generation consumes that file.

- [ ] **Step 1: Write the failing test**

`backend/internal/contracts/contracts_test.go`:

```go
package contracts_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/contracts"
)

func TestBuild_ProducesOpenAPI31WithAllOperations(t *testing.T) {
	raw, err := contracts.Build()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		OpenAPI string `json:"openapi"`
		Info    struct {
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"info"`
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Fatalf("openapi = %q, want 3.1.x", doc.OpenAPI)
	}
	if doc.Info.Title != "caesar API" {
		t.Fatalf("title = %q", doc.Info.Title)
	}
	wantOps := map[string]map[string]string{
		"/api/v1/auth/login":    {"post": "login"},
		"/api/v1/auth/logout":   {"post": "logout"},
		"/api/v1/auth/me":       {"get": "me"},
		"/api/v1/auth/password": {"put": "changePassword"},
		"/api/v1/users":         {"post": "createUser", "get": "listUsers"},
		"/api/v1/users/{id}":    {"get": "getUser", "patch": "updateUser"},
	}
	for path, methods := range wantOps {
		got, ok := doc.Paths[path]
		if !ok {
			t.Errorf("missing path %s", path)
			continue
		}
		for method, opID := range methods {
			if got[method].OperationID != opID {
				t.Errorf("%s %s operationId = %q, want %q", method, path, got[method].OperationID, opID)
			}
		}
	}
}

func TestBuild_IsDeterministic(t *testing.T) {
	a, err := contracts.Build()
	if err != nil {
		t.Fatal(err)
	}
	b, err := contracts.Build()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("Build must be deterministic — the regen-diff gate depends on it")
	}
}
```

Run: `cd backend && go test ./internal/contracts/` — FAIL (package missing).

- [ ] **Step 2: Implement the builder**

Add the dependency: `cd backend && go get github.com/swaggest/openapi-go@latest`

`backend/internal/contracts/contracts.go`:

```go
// Package contracts turns the httpx.Operation route tables into the
// committed OpenAPI 3.1 document (spec §4: Go types are the source of
// truth; CI gates regen-diff). It is tooling, not a bounded context —
// internal/arch and txboundary both list it as nonContext.
package contracts

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/swaggest/openapi-go"
	"github.com/swaggest/openapi-go/openapi31"

	identityapi "github.com/giiorg/caesar/backend/internal/identity/api"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
)

// idParams declares the {id} path parameter for operations whose pattern
// carries one (the stdlib pattern and the OpenAPI template share syntax).
type idParams struct {
	ID string `path:"id" description:"resource id (uuid)"`
}

type idAndUpdateUser struct {
	idParams
	identityapi.UpdateUserRequest
}

// Build reflects every registered operation into an OpenAPI 3.1 document.
func Build() ([]byte, error) {
	r := openapi31.NewReflector()
	r.Spec.Info.WithTitle("caesar API").WithVersion("v1")

	ops := identityapi.Operations(identityapi.New(nil, 0)) // metadata only; handlers never run
	for _, op := range ops {
		method, path, ok := strings.Cut(op.Pattern, " ")
		if !ok {
			return nil, fmt.Errorf("contracts: pattern %q has no method", op.Pattern)
		}
		oc, err := r.NewOperationContext(method, path)
		if err != nil {
			return nil, fmt.Errorf("contracts: %s: %w", op.Pattern, err)
		}
		oc.SetID(op.ID)
		oc.SetSummary(op.Summary)
		oc.SetDescription("Errors use the RFC 9457 problem+json envelope on every non-2xx status.")

		if req := requestStructure(op, path); req != nil {
			oc.AddReqStructure(req)
		}
		if op.Response != nil {
			oc.AddRespStructure(op.Response, openapi.WithHTTPStatus(op.Status))
		} else {
			oc.AddRespStructure(nil, openapi.WithHTTPStatus(op.Status))
		}
		oc.AddRespStructure(httpx.ProblemDetails{},
			openapi.WithHTTPStatus(http.StatusBadRequest),
			openapi.WithContentType("application/problem+json"))

		if err := r.AddOperation(oc); err != nil {
			return nil, fmt.Errorf("contracts: add %s: %w", op.Pattern, err)
		}
	}

	raw, err := json.MarshalIndent(r.Spec, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("contracts: marshal: %w", err)
	}
	return append(raw, '\n'), nil
}

// requestStructure folds the {id} path parameter into the request shape.
func requestStructure(op httpx.Operation, path string) any {
	hasID := strings.Contains(path, "{id}")
	switch {
	case !hasID:
		return op.Request
	case op.Request == nil:
		return idParams{}
	default:
		// Only updateUser combines a body with {id} today; extend this
		// switch when new id-bearing bodies appear.
		if _, ok := op.Request.(identityapi.UpdateUserRequest); ok {
			return idAndUpdateUser{}
		}
		panic(fmt.Sprintf("contracts: no path-param wrapper for %s — add one", op.Pattern))
	}
}
```

`backend/internal/contracts/cmd/openapigen/main.go`:

```go
// Command openapigen writes the generated OpenAPI document. Run via
// `task contracts:openapi`; CI diffs the committed file.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/giiorg/caesar/backend/internal/contracts"
)

func main() {
	out := flag.String("out", "api/openapi.json", "output path")
	flag.Parse()
	raw, err := contracts.Build()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, raw, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

- [ ] **Step 3: Teach the arch rules about contracts**

In `backend/internal/arch/rules.go` and `backend/internal/analyzers/txboundary/txboundary.go`, extend both `nonContext` maps:

```go
var nonContext = map[string]bool{"platform": true, "app": true, "arch": true, "analyzers": true, "contracts": true}
```

(Without this, `CheckContextIsolation` reads `internal/contracts` as a bounded context importing `identity` and fails.)

- [ ] **Step 4: Run tests + generate the document**

Run: `cd backend && go test ./internal/contracts/ ./internal/arch/ -v`
Expected: PASS.

Add the Taskfile target:

```yaml
  contracts:openapi:
    desc: "Regenerate backend/api/openapi.json from the route tables"
    dir: backend
    cmds:
      - go run ./internal/contracts/cmd/openapigen -out api/openapi.json
```

Run: `task contracts:openapi` — creates `backend/api/openapi.json`. Read it once: confirm the eight operations, the `ProblemDetails` schema in components, and the `{id}` path parameter on `/api/v1/users/{id}`.

- [ ] **Step 5: Full gate**

Run: `task backend:ci`
Expected: green (deny-list untouched — swaggest is not a banned prefix; depguard needs no sync).

- [ ] **Step 6: Commit**

```bash
git add backend/internal/contracts backend/internal/arch backend/internal/analyzers backend/api Taskfile.yml backend/go.mod backend/go.sum
git commit -m "feat(backend): openapi 3.1 generated from the operation route table"
```

---

### Task 14: TypeScript API types and the contracts drift gate

`/web` gets its first (deliberately tiny) footprint: a package.json pinning `openapi-typescript` and the generated `schema.d.ts`. Plan 11 builds the real app and the `openapi-fetch` client wrapper on top of these types — this task establishes the generated artifact and its drift gate, which is what the roadmap's "TS client + regen-diff" contract requires now.

**Files:**
- Create: `web/package.json`, `web/package-lock.json` (via npm)
- Create: `web/.gitignore`
- Create: `web/src/api/schema.d.ts` (generated, committed)
- Modify: `Taskfile.yml` (`contracts:ts`, `contracts:gen`, `contracts:check`; root `ci` gains the check)

**Interfaces:**
- Consumes: `backend/api/openapi.json` (Task 13).
- Produces: `web/src/api/schema.d.ts` — the typed contract Plan 11 imports; `task contracts:check` — the single drift gate CI calls (sqlc + OpenAPI + TS in one).

- [ ] **Step 1: Scaffold the web package**

`web/package.json`:

```json
{
  "name": "caesar-web",
  "private": true,
  "engines": {
    "node": ">=20"
  },
  "scripts": {
    "api:gen": "openapi-typescript ../backend/api/openapi.json -o src/api/schema.d.ts"
  },
  "devDependencies": {
    "openapi-typescript": "7.13.0"
  }
}
```

`web/.gitignore`:

```
node_modules/
```

Run: `cd web && npm install` (creates `package-lock.json` — commit it; `npm ci` uses it from now on).

- [ ] **Step 2: Generate the types**

Run: `cd web && npm run api:gen`
Expected: `web/src/api/schema.d.ts` exists; skim it — `paths` must contain `"/api/v1/auth/login"` and `components["schemas"]` the request/response types. Commit the generated file.

- [ ] **Step 3: Wire the task targets**

Add to `Taskfile.yml`:

```yaml
  contracts:ts:
    desc: "Regenerate web/src/api/schema.d.ts from the OpenAPI document"
    dir: web
    cmds:
      - npm ci
      - npm run api:gen

  contracts:gen:
    desc: "Regenerate every generated contract artifact"
    cmds:
      - task: db:sqlc
      - task: contracts:openapi
      - task: contracts:ts

  contracts:check:
    desc: "Regenerate all contracts and fail on drift"
    cmds:
      - task: contracts:gen
      - git diff --exit-code -- backend/api/openapi.json web/src/api backend/internal/identity/store backend/internal/platform/audit
      - test -z "$(git status --porcelain -- backend/api web/src/api)"
```

And extend the root `ci` task:

```yaml
  ci:
    cmds:
      - task: backend:ci
      - task: contracts:check
      - task: secrets
      - task: lint:actions
```

- [ ] **Step 4: Verify the gate catches drift**

Run: `task contracts:check` — green on a clean tree.
Then temporarily add a junk field to `LoginRequest` in `dto.go`, run `task contracts:check` again — it must FAIL on `git diff --exit-code` (openapi.json + schema.d.ts changed). Revert the junk field, run `git checkout -- backend/api web/src/api` if the generated files were touched, and confirm green again.

- [ ] **Step 5: Commit**

```bash
git add web Taskfile.yml
git commit -m "feat(repo): typescript api types with contracts drift gate"
```

---

### Task 15: CI contracts lane, README, roadmap status, final green

**Files:**
- Modify: `.github/workflows/ci.yml` (contracts job + ci-ok needs)
- Modify: `backend/README.md`
- Modify: `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md` (status row 02)

**Interfaces:**
- Consumes: `task contracts:check` (Task 14).
- Produces: the composite gate now requires the contracts lane; docs current.

- [ ] **Step 1: Add the contracts job**

In `.github/workflows/ci.yml`, add alongside the other jobs:

```yaml
  contracts:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: backend/go.mod
          cache-dependency-path: backend/go.sum
      - uses: actions/setup-node@v4
        with:
          node-version: '20'
          cache: npm
          cache-dependency-path: web/package-lock.json
      - uses: arduino/setup-task@v2
        with:
          version: 3.x
          repo-token: ${{ secrets.GITHUB_TOKEN }}
      - run: task contracts:check
```

And update the gate: `ci-ok`'s `needs: [backend, secrets, workflow-lint, contracts]`.

Run: `task lint:actions` — clean.

- [ ] **Step 2: Update the backend README**

Extend `backend/README.md` (match its existing voice — read it first) with short sections:
- **Auth**: argon2id (pinned params), `__Host-caesar_session` cookie, idle/absolute TTLs from `auth.*` config, login rate limits (the pinned numbers), `caesar admin bootstrap -email … < password.txt` for first admin.
- **API**: `/api/v1`, problem+json envelope, the operation-table pattern (`httpx.Operation` — route and OpenAPI from one declaration), noting stdlib 405s are plain text (V1 limitation).
- **Contracts**: `task contracts:gen` / `task contracts:check`; generated artifacts are committed and never hand-edited; Node ≥ 20 required for the TS lane.
- **sqlc**: edit `schema.sql` + `queries.sql` → `task db:new -- <name>` + `task db:sqlc`.

- [ ] **Step 3: Roadmap status**

In the roadmap sequence table, set row 02's Status to `**done** — 2026-08-02-caesar-02-api-auth-rbac.md`.

- [ ] **Step 4: Final verification**

Run: `task ci`
Expected: fully green — fmt, vet, lint, analyzers, unit + integration, vulncheck, build, contracts drift, secrets, workflow lint.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/ci.yml backend/README.md docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md
git commit -m "ci(repo): contracts lane in the composite gate and plan 02 docs"
```

---

## Execution notes

- **Order is dependency order** — do not reorder. 5 needs 3/4 only conceptually; 6–10 need 5; 11 needs 4+8–10; 12 needs 11; 13 needs 11; 14 needs 13; 15 needs 14. Tasks 1–4 are independent of each other.
- **Docker must be running** for Task 5's Atlas authoring and every integration test (testcontainers).
- **sqlc output is authoritative for generated names.** Where this plan shows `store.XxxParams` fields or row struct shapes, minor naming/type differences in actual sqlc output win — adapt call sites, never hand-edit generated files. Same for `swaggest/openapi-go`: if its API differs at the resolved version (`NewOperationContext`/`AddReqStructure`/`AddRespStructure` are the v0.2.x names), adapt minimally; the committed-document test (Task 13 Step 1) is the behavior contract.
- **Session-TTL params vs config:** the service takes `identity.Params` (plain durations); config supplies them only in app wiring (Task 12) — that ordering is what keeps the no-dead-config rule satisfied per commit.
- **Timing-oracle discipline:** every credential-failure path in `Login` runs exactly one argon2 verification (real or dummy) and returns the same error value. Don't "optimize" the dummy verify away.
- **Rate-limit rows are never swept in this plan** — retention/sweepers are Plan 14 concern; the table is tiny (one row per active attacker scope).
- **`__Host-` cookies in dev:** browsers accept Secure cookies from `http://localhost`; curl/tests don't enforce cookie attributes. Nothing special needed.
- **What this plan deliberately does not build** (later plans own them): projects/grants write APIs and credentials (03), `CAESAR_MASTER_KEY` + envelope encryption (03), typed retry-effect axis on errors (05), SSE (10), the real web app + openapi-fetch client wrapper (11), security headers/CSP/HSTS (11/14), tunnel-aware client IP extraction (14), session retention sweeper (14), PATs/OIDC/TOTP (post-V1).





