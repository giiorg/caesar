# caesar Plan 03 — Assets & Credentials Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The assets slice of the domain (spec §2): projects, repos, envelope-encrypted credentials (`CAESAR_MASTER_KEY`, AES-256-GCM, `key_id` rotation), and the harness registry — each behind `/api/v1` with per-project RBAC — plus the grant write endpoints deferred from Plan 02 and that plan's two carry-forwards (ChangePassword throttling/audit, the `WithTx` helper).

**Architecture:** `internal/assets` is the second bounded context (peer of `identity`, per the layout law): domain types + service + sqlc store + HTTP handlers, transactions never leaving the context. To let assets authenticate and authorize without importing identity, the RBAC vocabulary (roles, grant ladder, principal, session-cookie name) is promoted to a new shared-kernel package `platform/authn` together with two ports (`SessionAuthenticator`, `Authorizer`) that `identity.Service` already satisfies; identity keeps type aliases so nothing else moves. Envelope crypto lives in `platform/secrets` (keyset parsing + Seal/Open); the master key is a bootstrap secret (`security.master_key` / `CAESAR_MASTER_KEY` env override, spec §8). Grant writes stay in identity (its tables) behind a `ProjectChecker` port that assets implements, wired in app. `platform/pg` gains `WithTx`; identity's simple transaction sites are refactored onto it (Login and ChangePassword keep explicit transactions — their failure paths *commit* rate/audit state).

**Tech Stack:** Everything from Plans 01–02. No new tools, no new Go dependencies: envelope encryption is stdlib `crypto/aes` + `crypto/cipher`; app-side ids use `uuid.NewV7()` (already a direct dep).

## Global Constraints

Everything in Plan 01's and Plan 02's Global Constraints still applies verbatim (module path, layout law, deny-list, strict JSON config, env access only in `platform/config`, stubs return `NotImplemented`, slog JSON, TDD + table-driven + behavioral names, Taskfile parity, generated code never hand-edited, commit style — one short sentence, **no AI attribution of any kind**). New pins for this plan:

- **Master key (`CAESAR_MASTER_KEY` / `security.master_key`):** a comma-separated keyset `key_id:base64key[,key_id:base64key…]`. Each `key_id` matches `[A-Za-z0-9._-]{1,64}`; each key is standard (padded) base64 of exactly 32 bytes. The **first** entry is active (encrypts everything new); the rest are decrypt-only (rotation). Env overrides the JSON field. `caesar serve` and `caesar admin rekey` require it; `migrate` and `admin bootstrap` do not. Dev keyset (committed in `backend/caesar.dev.json`, deliberately zero-entropy): `dev:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=`.
- **Envelope layout (spec §6, per credential row):** random 32-byte DEK per row; data ciphertext = AES-256-GCM under the DEK; wrapped DEK = AES-256-GCM under the active master key; both ciphertexts are `nonce(12) || gcm-output`, both use AAD = the credential id's canonical UUID string. Row records `key_id`, `wrapped_dek`, `ciphertext`. Decrypted material only in memory at use time; **every use audited** (`credential.used` with a `purpose` detail).
- **Credential kinds (pinned):** `forge_token` (referenced by repos) and `model_key` (referenced by harnesses). A referencing write validates the referenced credential's kind. Secret size 1–4096 bytes. The API **never** returns secret material — responses carry metadata only (id, name, kind, key_id, timestamps).
- **RBAC matrix (spec §6; global admin passes every check):**
  - projects: create = admin · list = admin sees all, member sees projects with any grant · get = `view` · update = `manage` · delete = `manage`, refused while repos exist (409)
  - grants (identity): list / set / delete = `manage` on the project; an actor never modifies their own grant row
  - repos: create / update / delete = `manage` on the project · list / get = `view`
  - credentials and harnesses: admin-only, all operations
  - Authorization failures are `403` (uuidv7 ids are unguessable; no 404 masking).
- **Audit vocabulary bump (append to Plan 02's):** `project.created`, `project.updated`, `project.deleted`, `grant.set`, `grant.deleted`, `repo.created`, `repo.updated`, `repo.deleted`, `credential.created`, `credential.rotated`, `credential.rekeyed`, `credential.used`, `credential.deleted`, `harness.registered`, `harness.updated`, `harness.deleted`, `auth.password_change_failed`. `credential.used` and `credential.rekeyed` record actor NULL (system).
- **ChangePassword throttling (Plan 02 carry-forward):** current-password guesses share the `login_rate` table under scope `pwchange:<user_id>` with the **account** ladder (5 consecutive failures, then lock `30s × 2^(n−5)` capped at 15 minutes; reset on success). Failures audit `auth.password_change_failed`; the transition into lock audits `auth.rate_limited` with detail `{"scope":"pwchange"}`. Oversized current passwords go through the counted+audited failure path (session-12 ruling).
- **Validation pins:** names (project, credential, harness) trimmed, 1–100 chars · project description ≤ 500 · repo slug `^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)+$`, ≤ 200 (covers GitLab nested groups) · default branch 1–200 chars, no whitespace (default `main`) · harness provider endpoint parses as a URL, scheme `https` (plain `http` allowed only for `localhost`/`127.0.0.1`) · harness `defaults` is a JSON object ≤ 4096 bytes serialized · forge kinds `github`/`gitlab` · harness kinds `claude-code`/`codex`.
- **Cross-context law, restated for this plan:** `internal/assets` never imports `internal/identity` (or vice versa) — **including test files** (the arch test normalizes `_test` packages onto their package). Assets API tests use in-test fakes of the `authn` ports; the only place real identity + assets wiring meets is `internal/app`. Audit rows FK `actor_user_id` to `users`, so assets tests seed a user row with raw SQL, never via identity.
- **Contracts drift gate grows:** `task contracts:check` diffs must add `backend/internal/assets/store`; sqlc.yaml gains a third generation block. Every task that adds `httpx.Operation`s runs `task contracts:gen` and commits the regenerated `backend/api/openapi.json` + `web/src/api/schema.d.ts` in the same commit.
- All `go` / `task` commands run from the repo root unless the step says otherwise. Integration tests: `cd backend && go test -tags integration ./...` (Docker required).

---

### Task 1: Promote the RBAC vocabulary to `platform/authn`

The assets context must authenticate sessions and check grants without importing identity (bounded contexts are peers). Promote the shared vocabulary — roles, the grant ladder, principal, session types, the session-cookie name — plus the two consumer ports into the shared kernel. Identity keeps **type aliases**, so no call site outside `identity.go` changes, and `identity.Service` satisfies both ports with its existing method signatures.

**Files:**
- Create: `backend/internal/platform/authn/authn.go`
- Create: `backend/internal/platform/authn/authn_test.go`
- Modify: `backend/internal/identity/identity.go` (replace moved declarations with aliases)
- Modify: `backend/internal/identity/identity_test.go` (ladder tests move to authn; add port-satisfaction asserts)
- Modify: `backend/internal/identity/api/api.go` (SessionCookie becomes an alias)

**Interfaces:**
- Consumes: nothing new.
- Produces: `authn.Role` (`RoleAdmin`, `RoleMember`), `authn.GrantLevel` (`GrantView` … `GrantManage`, `.Valid()`, `.AtLeast(min)`), `authn.Principal{UserID uuid.UUID; Email string; Role Role}`, `authn.AuthSession{Principal; SessionID uuid.UUID}`, `authn.Grant{ProjectID uuid.UUID; Level GrantLevel}`, `authn.SessionCookie` const, and the ports `authn.SessionAuthenticator` / `authn.Authorizer`. Identity aliases: `identity.Role = authn.Role` etc. — existing signatures (`AuthenticateSession`, `Authorize`, `GrantsForUser`) are unchanged and now satisfy the ports.

- [ ] **Step 1: Write the authn package with its tests**

`backend/internal/platform/authn/authn.go`:

```go
// Package authn is the shared-kernel RBAC vocabulary (spec §6): roles, the
// per-project grant ladder, and the requester principal that rides the
// request context into every domain write (spec §4). Promoted out of
// identity in Plan 03 so peer contexts can authorize without importing it;
// identity keeps aliases and identity.Service implements both ports.
package authn

import (
	"context"

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

// AuthSession is a principal plus the session that authenticated it.
type AuthSession struct {
	Principal
	SessionID uuid.UUID
}

// Grant is one rung of the ladder on one project.
type Grant struct {
	ProjectID uuid.UUID
	Level     GrantLevel
}

// SessionCookie is the pinned __Host- session cookie name (spec §6),
// shared by every API package that authenticates requests.
const SessionCookie = "__Host-caesar_session"

// SessionAuthenticator resolves a session token to its principal.
// identity.Service is the sole implementation.
type SessionAuthenticator interface {
	AuthenticateSession(ctx context.Context, token string) (AuthSession, error)
}

// Authorizer is the single RBAC check: global admin passes everything;
// members need a grant at or above min on the project.
// identity.Service is the sole implementation.
type Authorizer interface {
	Authorize(ctx context.Context, p Principal, projectID uuid.UUID, min GrantLevel) (bool, error)
}
```

`backend/internal/platform/authn/authn_test.go` (the ladder tests move here from `identity_test.go`, names intact):

```go
package authn_test

import (
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/authn"
)

func TestGrantLevel_LadderIsInclusive(t *testing.T) {
	cases := []struct {
		have, min authn.GrantLevel
		want      bool
	}{
		{authn.GrantView, authn.GrantView, true},
		{authn.GrantView, authn.GrantRun, false},
		{authn.GrantRun, authn.GrantView, true},
		{authn.GrantApprove, authn.GrantRun, true},
		{authn.GrantManage, authn.GrantApprove, true},
		{authn.GrantManage, authn.GrantManage, true},
		{"", authn.GrantView, false},
		{authn.GrantView, "", false},
		{"bogus", authn.GrantView, false},
	}
	for _, c := range cases {
		if got := c.have.AtLeast(c.min); got != c.want {
			t.Errorf("%q.AtLeast(%q) = %v, want %v", c.have, c.min, got, c.want)
		}
	}
}

func TestRoleAndGrantLevel_Validity(t *testing.T) {
	if !authn.RoleAdmin.Valid() || !authn.RoleMember.Valid() || authn.Role("root").Valid() {
		t.Fatal("role validity wrong")
	}
	if !authn.GrantManage.Valid() || authn.GrantLevel("owner").Valid() {
		t.Fatal("grant level validity wrong")
	}
}
```

- [ ] **Step 2: Run the authn tests**

Run: `cd backend && go test ./internal/platform/authn/`
Expected: PASS

- [ ] **Step 3: Replace identity's moved declarations with aliases**

Rewrite `backend/internal/identity/identity.go` to keep only `User` and alias the rest (delete the moved ladder tests from `identity_test.go`):

```go
// Package identity is the identity & access bounded context (spec §2, §6):
// users, sessions, the grant ladder, login. Peers never import it except
// through app wiring; it owns its transactions end to end.
package identity

import (
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/authn"
)

// The RBAC vocabulary moved to platform/authn in Plan 03 (shared kernel)
// so peer contexts can consume it. Aliases keep identity's API unchanged.
type (
	Role        = authn.Role
	GrantLevel  = authn.GrantLevel
	Principal   = authn.Principal
	AuthSession = authn.AuthSession
	Grant       = authn.Grant
)

const (
	RoleAdmin  = authn.RoleAdmin
	RoleMember = authn.RoleMember

	GrantView    = authn.GrantView
	GrantRun     = authn.GrantRun
	GrantApprove = authn.GrantApprove
	GrantManage  = authn.GrantManage
)

// User is the domain view of a user row (never exposes the hash).
type User struct {
	ID        uuid.UUID
	Email     string
	Role      Role
	Active    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}
```

In `backend/internal/identity/api/api.go`, replace the `SessionCookie` declaration with:

```go
// SessionCookie aliases the shared pinned cookie name (platform/authn).
const SessionCookie = authn.SessionCookie
```

(add the `authn` import). In `backend/internal/identity/identity_test.go`, add the port-satisfaction asserts — they live in identity (a platform test must never import a context, or the arch platform-purity check trips on the normalized test package):

```go
var (
	_ authn.SessionAuthenticator = (*Service)(nil)
	_ authn.Authorizer           = (*Service)(nil)
)
```

(If `identity_test.go` is an external `identity_test` package, use `(*identity.Service)(nil)` with the import; keep whichever package form the file already has.)

- [ ] **Step 4: Full suite + architecture tests green**

Run: `cd backend && go build ./... && go test ./... && go test -tags integration ./internal/arch/`
Expected: PASS everywhere — aliases mean zero behavioral change; platform purity holds (authn imports only stdlib + uuid).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/platform/authn backend/internal/identity
git commit -m "refactor(backend): promote rbac vocabulary to platform/authn"
```

---

### Task 2: `pg.WithTx` and the identity refactor (carry-forward)

Plan 02's final review deferred the transaction-boilerplate helper until a third call site appeared; this plan adds many. Add `pg.WithTx` and refactor identity's **simple** sites onto it. `Login` is deliberately left explicit, and `ChangePassword` becomes explicit again in Task 3 — both have failure paths that *commit* rate/audit state before returning an error, which `WithTx`'s rollback-on-error shape cannot express clearly.

**Files:**
- Create: `backend/internal/platform/pg/tx.go`
- Create: `backend/internal/platform/pg/tx_integration_test.go`
- Modify: `backend/internal/identity/service.go` (`Bootstrap`, `CreateUser`, `UpdateUser`)
- Modify: `backend/internal/identity/session.go` (`Logout`)
- Modify: `backend/internal/identity/ratelimit.go` (`purgeStaleLoginRate`)
- Modify: `backend/internal/identity/service.go` (`MintSessionForTest`)

**Interfaces:**
- Consumes: `*pgxpool.Pool` from `platform/pg`.
- Produces: `pg.WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error` — begins, rolls back unless `fn` returns nil, then commits. Assets (Tasks 6–9) uses it everywhere.

- [ ] **Step 1: Write the failing integration test**

`backend/internal/platform/pg/tx_integration_test.go`:

```go
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
```

- [ ] **Step 2: Run it to see it fail**

Run: `cd backend && go vet ./internal/platform/pg/`
Expected: FAIL — `pg.WithTx` undefined.

- [ ] **Step 3: Implement `WithTx`**

`backend/internal/platform/pg/tx.go`:

```go
package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WithTx runs fn inside one transaction: rollback unless fn returns nil,
// then commit. The fn error is returned verbatim so classified errors
// (xerrors) survive. Callers needing commit-on-failure semantics (login
// rate accounting) keep explicit transactions instead.
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg: commit: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests, verify pass**

Run: `cd backend && go test -tags integration ./internal/platform/pg/ -run TestWithTx -v`
Expected: both PASS.

- [ ] **Step 5: Refactor identity's simple transaction sites**

Six sites use the plain begin/defer-rollback/commit frame; replace the frame with `pg.WithTx`, keeping the body identical and capturing results in closure variables. `Bootstrap` shown in full as the template:

```go
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

	var user User
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", adminGuardLockID); err != nil {
			return fmt.Errorf("identity: advisory lock: %w", err)
		}
		q := store.New(tx)

		admins, err := q.CountAdmins(ctx)
		if err != nil {
			return fmt.Errorf("identity: count admins: %w", err)
		}
		if admins > 0 {
			return xerrors.E(xerrors.ClassConflict, "an admin already exists; bootstrap runs once")
		}
		row, err := q.CreateUser(ctx, store.CreateUserParams{
			Email: email, PasswordHash: hash, Role: string(RoleAdmin),
		})
		if err != nil {
			return fmt.Errorf("identity: create admin: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Action: "user.bootstrapped", SubjectKind: "user", SubjectID: row.ID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"email": email},
		}); err != nil {
			return err
		}
		user = userFromRow(row)
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return user, nil
}
```

Apply the same mechanical transformation (frame → `pg.WithTx`, body unchanged, results via closure variables) to:
- `CreateUser` and `UpdateUser` in `service.go` (both return `User`)
- `Logout` in `session.go` (no result)
- `MintSessionForTest` in `service.go` (returns the token string)
- `purgeStaleLoginRate` in `ratelimit.go` (log-and-return on `WithTx` error, exactly as it logs today; collapse its three log sites into one `slog.Default().WarnContext(ctx, "identity: purge login_rate failed", "error", err)`)

Do **not** touch `Login` or `ChangePassword` (explicit transactions stay — see task intro). Add the `pg` import to each file, drop now-unused imports.

- [ ] **Step 6: Full suite green**

Run: `cd backend && go build ./... && go test ./... && go test -tags integration ./internal/identity/...`
Expected: PASS — behavior identical.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/platform/pg backend/internal/identity
git commit -m "refactor(backend): add pg.withtx and fold identity transaction frames onto it"
```

---

### Task 3: Throttle and audit ChangePassword current-password guesses (carry-forward)

Plan 02's final review flagged that an authenticated attacker with a stolen session can brute-force the account's current password through `PUT /api/v1/auth/password` unthrottled and unaudited. Fix: the `pwchange:<user_id>` rate scope with the account ladder, failures audited.

**Files:**
- Modify: `backend/internal/identity/session.go` (`ChangePassword`)
- Create: `backend/internal/identity/pwchange_integration_test.go`

**Interfaces:**
- Consumes: `rateFail`/`rateBlocked`/`loadRate`/`saveRate` and the `login_rate` queries from Plan 02 (unchanged); `purgeStaleLoginRate` already covers stale `pwchange:` rows (same retention rule).
- Produces: unchanged signature `ChangePassword(ctx, sess AuthSession, current, newPassword string) (string, error)`; new audit actions `auth.password_change_failed` and `auth.rate_limited` with detail `{"scope":"pwchange"}`.

- [ ] **Step 1: Write the failing integration test**

`backend/internal/identity/pwchange_integration_test.go`:

```go
//go:build integration

package identity_test

import (
	"context"
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func TestChangePassword_ThrottlesAndAuditsWrongCurrentPassword(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	fake := clock.NewFake(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	svc := identity.NewService(pool, fake, audit.NewRecorder(fake), identity.Params{
		SessionIdleTTL: 14 * 24 * time.Hour, SessionAbsoluteTTL: 90 * 24 * time.Hour,
	})
	user, err := svc.Bootstrap(ctx, "root@example.com", "a long enough password")
	if err != nil {
		t.Fatal(err)
	}
	token, err := svc.MintSessionForTest(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := svc.AuthenticateSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}

	// Five wrong guesses: all unauthenticated-classed, each audited.
	for i := 0; i < 5; i++ {
		_, err := svc.ChangePassword(ctx, sess, "wrong current pw!", "a brand new password")
		if xerrors.ClassOf(err) != xerrors.ClassUnauthenticated {
			t.Fatalf("attempt %d: class = %v, want unauthenticated", i+1, xerrors.ClassOf(err))
		}
	}
	var failed int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM audit_entries WHERE action = 'auth.password_change_failed'").Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != 5 {
		t.Fatalf("failed audits = %d, want 5", failed)
	}
	var limited int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM audit_entries WHERE action = 'auth.rate_limited' AND detail->>'scope' = 'pwchange'").Scan(&limited); err != nil {
		t.Fatal(err)
	}
	if limited != 1 {
		t.Fatalf("rate_limited audits = %d, want 1 (transition only)", limited)
	}

	// Locked now — even the CORRECT current password is refused.
	if _, err := svc.ChangePassword(ctx, sess, "a long enough password", "a brand new password"); xerrors.ClassOf(err) != xerrors.ClassRateLimited {
		t.Fatalf("locked class = %v, want rate limited", xerrors.ClassOf(err))
	}

	// Lock expires (5th failure locks for 30s); success resets the scope.
	fake.Advance(31 * time.Second)
	if _, err := svc.ChangePassword(ctx, sess, "a long enough password", "a brand new password"); err != nil {
		t.Fatalf("after lock expiry: %v", err)
	}
	var rateRows int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM login_rate WHERE scope LIKE 'pwchange:%'").Scan(&rateRows); err != nil {
		t.Fatal(err)
	}
	if rateRows != 0 {
		t.Fatalf("pwchange rate rows after success = %d, want 0", rateRows)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `cd backend && go test -tags integration ./internal/identity/ -run TestChangePassword_Throttles -v`
Expected: FAIL — no throttling, no `auth.password_change_failed` rows.

- [ ] **Step 3: Rewrite ChangePassword with the rate scope**

Replace `ChangePassword` in `backend/internal/identity/session.go`:

```go
// ChangePassword verifies the current password, rewrites the hash,
// revokes every session of the user, and mints a replacement for the
// caller (rotation on privilege change, spec §6). Current-password
// guesses are throttled through login_rate scope "pwchange:<user_id>"
// with the account ladder and audited — a stolen session must not be a
// free brute-force oracle. Failure paths COMMIT their rate/audit rows,
// so this keeps an explicit transaction (not pg.WithTx).
func (s *Service) ChangePassword(ctx context.Context, sess AuthSession, current, newPassword string) (string, error) {
	if err := validatePassword(newPassword); err != nil {
		return "", err
	}
	scope := "pwchange:" + sess.UserID.String()
	now := s.clock.Now()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("identity: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	if err := q.EnsureLoginRate(ctx, store.EnsureLoginRateParams{Scope: scope, WindowStartedAt: now}); err != nil {
		return "", fmt.Errorf("identity: ensure pwchange rate: %w", err)
	}
	rate, err := loadRate(ctx, q, scope)
	if err != nil {
		return "", err
	}
	if rateBlocked(rate, now) {
		return "", errLoginLocked
	}

	row, err := q.GetUserByID(ctx, sess.UserID)
	if err != nil {
		return "", fmt.Errorf("identity: get user: %w", err)
	}
	ok := false
	if len(current) <= MaxPasswordLen {
		ok, err = VerifyPassword(row.PasswordHash, current)
		if err != nil {
			return "", fmt.Errorf("identity: verify: %w", err)
		}
	} else {
		dummyVerify(current)
	}

	if !ok {
		next := rateFail(rateAccount, rate, now)
		if err := saveRate(ctx, q, scope, next); err != nil {
			return "", err
		}
		entries := []audit.Entry{{
			Actor:  uuid.NullUUID{UUID: sess.UserID, Valid: true},
			Action: "auth.password_change_failed", SubjectKind: "user", SubjectID: sess.UserID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
		}}
		if rateBlocked(next, now) && !rateBlocked(rate, now) {
			entries = append(entries, audit.Entry{
				Actor:  uuid.NullUUID{UUID: sess.UserID, Valid: true},
				Action: "auth.rate_limited", SubjectKind: "user", SubjectID: sess.UserID.String(),
				CorrelationID: httpx.CorrelationID(ctx),
				Detail:        map[string]any{"scope": "pwchange"},
			})
		}
		for _, e := range entries {
			if err := s.audit.Record(ctx, tx, e); err != nil {
				return "", err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return "", fmt.Errorf("identity: commit: %w", err)
		}
		s.purgeStaleLoginRate(ctx)
		return "", xerrors.E(xerrors.ClassUnauthenticated, "current password is incorrect")
	}

	if err := q.DeleteLoginRate(ctx, scope); err != nil {
		return "", fmt.Errorf("identity: rate reset: %w", err)
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return "", err
	}
	if err := q.UpdateUserPassword(ctx, store.UpdateUserPasswordParams{ID: sess.UserID, PasswordHash: hash, UpdatedAt: now}); err != nil {
		return "", fmt.Errorf("identity: update password: %w", err)
	}
	if err := q.RevokeUserSessions(ctx, store.RevokeUserSessionsParams{UserID: sess.UserID, RevokedAt: &now}); err != nil {
		return "", fmt.Errorf("identity: revoke sessions: %w", err)
	}
	token, err := s.mintSession(ctx, q, sess.UserID)
	if err != nil {
		return "", err
	}
	if err := s.audit.Record(ctx, tx, audit.Entry{
		Actor:  uuid.NullUUID{UUID: sess.UserID, Valid: true},
		Action: "auth.password_changed", SubjectKind: "user", SubjectID: sess.UserID.String(),
		CorrelationID: httpx.CorrelationID(ctx),
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("identity: commit: %w", err)
	}
	return token, nil
}
```

Update the schema comment on `login_rate` in `backend/internal/platform/pg/schema.sql` to document the third scope kind (comment-only change — no migration):

```sql
-- DB-backed login rate limiting: scope is 'account:<lower(email)>',
-- 'ip:<host>', or 'pwchange:<user_id>'. Parameters pinned in the plan
-- headers (02: login scopes; 03: pwchange).
```

- [ ] **Step 4: Run the tests, verify pass**

Run: `cd backend && go test -tags integration ./internal/identity/ -run 'TestChangePassword' -v`
Expected: the new test and Plan 02's `TestAPI_ChangePasswordRotatesCookie` (api package) both PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/identity backend/internal/platform/pg/schema.sql
git commit -m "fix(backend): throttle and audit change-password current-password guesses"
```

---
### Task 4: `platform/secrets` — keyset parsing and the envelope sealer

The crypto core, pure and stdlib-only: parse the master keyset, seal/open per-row envelopes. No Postgres, no config — those wire up later.

**Files:**
- Create: `backend/internal/platform/secrets/secrets.go`
- Create: `backend/internal/platform/secrets/secrets_test.go`

**Interfaces:**
- Consumes: nothing but stdlib.
- Produces: `secrets.ParseKeyset(s string) (*Keyset, error)` · `(*Keyset).ActiveKeyID() string` · `secrets.Envelope{KeyID string; WrappedDEK []byte; Ciphertext []byte}` · `(*Keyset).Seal(plaintext, aad []byte) (Envelope, error)` · `(*Keyset).Open(e Envelope, aad []byte) ([]byte, error)`. Assets (Task 7) and config validation (Task 12) consume these.

- [ ] **Step 1: Write the failing tests**

`backend/internal/platform/secrets/secrets_test.go`:

```go
package secrets_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/secrets"
)

func testKey(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestParseKeyset_AcceptsSingleAndMultipleKeys(t *testing.T) {
	one, two := testKey(t), testKey(t)
	ks, err := secrets.ParseKeyset("prod:" + one)
	if err != nil {
		t.Fatal(err)
	}
	if ks.ActiveKeyID() != "prod" {
		t.Fatalf("active = %q, want prod", ks.ActiveKeyID())
	}
	ks, err = secrets.ParseKeyset("next:" + two + ",prod:" + one)
	if err != nil {
		t.Fatal(err)
	}
	if ks.ActiveKeyID() != "next" {
		t.Fatalf("active = %q, want next (first entry)", ks.ActiveKeyID())
	}
}

func TestParseKeyset_RejectsMalformedInput(t *testing.T) {
	good := testKey(t)
	cases := []struct{ name, in string }{
		{"empty", ""},
		{"no colon", good},
		{"empty id", ":" + good},
		{"bad id chars", "prod key:" + good},
		{"id too long", strings.Repeat("a", 65) + ":" + good},
		{"not base64", "prod:@@@@"},
		{"wrong key length", "prod:" + base64.StdEncoding.EncodeToString([]byte("short"))},
		{"duplicate id", "prod:" + good + ",prod:" + good},
	}
	for _, c := range cases {
		if _, err := secrets.ParseKeyset(c.in); err == nil {
			t.Errorf("%s: no error for %q", c.name, c.in)
		}
	}
}

func TestSealOpen_RoundTripsUnderActiveKey(t *testing.T) {
	ks, err := secrets.ParseKeyset("prod:" + testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	plain, aad := []byte("hunter2-token"), []byte("credential-id")
	env, err := ks.Seal(plain, aad)
	if err != nil {
		t.Fatal(err)
	}
	if env.KeyID != "prod" {
		t.Fatalf("key id = %q", env.KeyID)
	}
	if bytes.Contains(env.Ciphertext, plain) {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := ks.Open(env, aad)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip = %q, want %q", got, plain)
	}
}

func TestOpen_DecryptOnlyKeyStillOpens(t *testing.T) {
	old := "old:" + testKey(t)
	ksOld, err := secrets.ParseKeyset(old)
	if err != nil {
		t.Fatal(err)
	}
	env, err := ksOld.Seal([]byte("sealed under old"), []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	ksNew, err := secrets.ParseKeyset("new:" + testKey(t) + "," + old)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ksNew.Open(env, []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "sealed under old" {
		t.Fatalf("got %q", got)
	}
}

func TestOpen_FailsClosed(t *testing.T) {
	ks, err := secrets.ParseKeyset("prod:" + testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	env, err := ks.Seal([]byte("secret"), []byte("right-aad"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ks.Open(env, []byte("wrong-aad")); err == nil {
		t.Error("wrong AAD must fail")
	}
	unknown := env
	unknown.KeyID = "ghost"
	if _, err := ks.Open(unknown, []byte("right-aad")); err == nil {
		t.Error("unknown key id must fail")
	}
	tampered := env
	tampered.Ciphertext = append([]byte(nil), env.Ciphertext...)
	tampered.Ciphertext[len(tampered.Ciphertext)-1] ^= 0x01
	if _, err := ks.Open(tampered, []byte("right-aad")); err == nil {
		t.Error("tampered ciphertext must fail")
	}
	short := env
	short.WrappedDEK = short.WrappedDEK[:4]
	if _, err := ks.Open(short, []byte("right-aad")); err == nil {
		t.Error("truncated wrapped dek must fail")
	}
}
```

- [ ] **Step 2: Run to see them fail**

Run: `cd backend && go vet ./internal/platform/secrets/`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement the package**

`backend/internal/platform/secrets/secrets.go`:

```go
// Package secrets implements caesar's credential envelope encryption
// (spec §6): AES-256-GCM per-row DEKs wrapped by a master keyset from
// CAESAR_MASTER_KEY. The first keyset entry is active (encrypts new
// material); the rest are decrypt-only, which is the whole rotation
// story: add a new first key, `caesar admin rekey`, drop the old one.
// KMS/age slot in behind this same surface later.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	keyLen   = 32
	nonceLen = 12
)

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type masterKey struct {
	id   string
	aead cipher.AEAD
}

// Keyset is the parsed master keyset: one active key, any number of
// decrypt-only predecessors.
type Keyset struct {
	active masterKey
	byID   map[string]masterKey
}

// ParseKeyset parses "id:base64key[,id:base64key...]" — first entry active.
func ParseKeyset(s string) (*Keyset, error) {
	if strings.TrimSpace(s) == "" {
		return nil, errors.New("secrets: empty keyset")
	}
	ks := &Keyset{byID: map[string]masterKey{}}
	for i, part := range strings.Split(s, ",") {
		id, b64, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok {
			return nil, fmt.Errorf("secrets: entry %d: want id:base64key", i+1)
		}
		if !keyIDPattern.MatchString(id) {
			return nil, fmt.Errorf("secrets: entry %d: key id must match %s", i+1, keyIDPattern)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("secrets: entry %d: %w", i+1, err)
		}
		if len(raw) != keyLen {
			return nil, fmt.Errorf("secrets: entry %d: key is %d bytes, want %d", i+1, len(raw), keyLen)
		}
		if _, dup := ks.byID[id]; dup {
			return nil, fmt.Errorf("secrets: duplicate key id %q", id)
		}
		aead, err := newAEAD(raw)
		if err != nil {
			return nil, fmt.Errorf("secrets: entry %d: %w", i+1, err)
		}
		mk := masterKey{id: id, aead: aead}
		ks.byID[id] = mk
		if i == 0 {
			ks.active = mk
		}
	}
	return ks, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// ActiveKeyID names the key new envelopes are sealed under.
func (k *Keyset) ActiveKeyID() string { return k.active.id }

// Envelope is one sealed secret: a per-row DEK wrapped by master key
// KeyID, and the payload under the DEK. Both blobs are nonce||gcm-output
// with the same AAD (the credential id).
type Envelope struct {
	KeyID      string
	WrappedDEK []byte
	Ciphertext []byte
}

func gcmSeal(aead cipher.AEAD, plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secrets: nonce: %w", err)
	}
	return append(nonce, aead.Seal(nil, nonce, plaintext, aad)...), nil
}

func gcmOpen(aead cipher.AEAD, blob, aad []byte) ([]byte, error) {
	if len(blob) < nonceLen {
		return nil, errors.New("secrets: ciphertext too short")
	}
	return aead.Open(nil, blob[:nonceLen], blob[nonceLen:], aad)
}

// Seal envelope-encrypts plaintext under a fresh DEK wrapped by the
// active master key.
func (k *Keyset) Seal(plaintext, aad []byte) (Envelope, error) {
	dek := make([]byte, keyLen)
	if _, err := rand.Read(dek); err != nil {
		return Envelope{}, fmt.Errorf("secrets: dek: %w", err)
	}
	dekAEAD, err := newAEAD(dek)
	if err != nil {
		return Envelope{}, err
	}
	ct, err := gcmSeal(dekAEAD, plaintext, aad)
	if err != nil {
		return Envelope{}, err
	}
	wrapped, err := gcmSeal(k.active.aead, dek, aad)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{KeyID: k.active.id, WrappedDEK: wrapped, Ciphertext: ct}, nil
}

// Open reverses Seal using whichever keyset entry sealed e. Every failure
// is the same closed error shape (no tamper oracle beyond GCM's own).
func (k *Keyset) Open(e Envelope, aad []byte) ([]byte, error) {
	mk, ok := k.byID[e.KeyID]
	if !ok {
		return nil, fmt.Errorf("secrets: no master key %q in keyset (rotation dropped it too early?)", e.KeyID)
	}
	dek, err := gcmOpen(mk.aead, e.WrappedDEK, aad)
	if err != nil {
		return nil, fmt.Errorf("secrets: unwrap dek: %w", err)
	}
	dekAEAD, err := newAEAD(dek)
	if err != nil {
		return nil, err
	}
	plain, err := gcmOpen(dekAEAD, e.Ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("secrets: open: %w", err)
	}
	return plain, nil
}
```

- [ ] **Step 4: Run the tests, verify pass**

Run: `cd backend && go test ./internal/platform/secrets/ -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/platform/secrets
git commit -m "feat(backend): master-keyset envelope encryption in platform/secrets"
```

---

### Task 5: Assets schema, migration, and sqlc store scaffold

All four schema changes land in one Atlas migration; sqlc gets its third generation block; the drift gate learns the new store path. Project queries land here (the first service task needs them); later tasks append their own queries and regen.

**Files:**
- Modify: `backend/internal/platform/pg/schema.sql`
- Create: `backend/internal/platform/pg/migrations/<timestamp>_assets_foundations.sql` (generated by Atlas)
- Create: `backend/internal/assets/store/queries.sql`
- Modify: `backend/sqlc.yaml`
- Modify: `Taskfile.yml` (`contracts:check` diff paths)

**Interfaces:**
- Consumes: Plan 02's schema (projects, grants FK targets).
- Produces: tables `credentials`, `repos`, `harnesses`; `projects.description` + `projects.updated_at`; generated package `backend/internal/assets/store` with project queries (`CreateProject`, `ListProjects`, `ListProjectsByIDs`, `GetProject`, `UpdateProject`, `DeleteProject`, `ProjectExists`).

- [ ] **Step 1: Extend the desired-state schema**

In `backend/internal/platform/pg/schema.sql`, replace the minimal `projects` table with the full one and append the three new tables (keep every existing table as-is):

```sql
-- Assets (spec §2). Plan 03: projects grow their API columns; repos,
-- credentials, harnesses land.
CREATE TABLE projects (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    name        text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Encrypted-at-rest secrets (spec §6): per-row AES-256-GCM envelope —
-- wrapped_dek under master key key_id, ciphertext under the DEK, AAD =
-- the credential id. Plaintext never lands here; every use is audited.
CREATE TABLE credentials (
    id          uuid PRIMARY KEY,
    name        text NOT NULL UNIQUE,
    kind        text NOT NULL CHECK (kind IN ('forge_token', 'model_key')),
    key_id      text NOT NULL,
    wrapped_dek bytea NOT NULL,
    ciphertext  bytea NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Forge repos (spec §2): forge kind + slug + default branch + credential
-- ref. RESTRICT FKs: a project with repos and a referenced credential
-- cannot be deleted.
CREATE TABLE repos (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id     uuid NOT NULL REFERENCES projects (id) ON DELETE RESTRICT,
    forge_kind     text NOT NULL CHECK (forge_kind IN ('github', 'gitlab')),
    slug           text NOT NULL,
    default_branch text NOT NULL DEFAULT 'main',
    credential_id  uuid NOT NULL REFERENCES credentials (id) ON DELETE RESTRICT,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, forge_kind, slug)
);

CREATE INDEX repos_project_id_idx ON repos (project_id);

-- Admin-registered harness registry (spec §2): kind, image ref, provider
-- endpoint, the model-key credential the egress broker will use, and
-- free-form defaults.
CREATE TABLE harnesses (
    id                uuid PRIMARY KEY DEFAULT uuidv7(),
    name              text NOT NULL UNIQUE,
    kind              text NOT NULL CHECK (kind IN ('claude-code', 'codex')),
    image_ref         text NOT NULL,
    provider_endpoint text NOT NULL,
    credential_id     uuid NOT NULL REFERENCES credentials (id) ON DELETE RESTRICT,
    defaults          jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
```

(`credentials.id` has no DEFAULT: the id is generated app-side because it is the encryption AAD and must exist before Seal.)

- [ ] **Step 2: Author the migration**

Run: `task db:new -- assets_foundations`
Expected: a new `backend/internal/platform/pg/migrations/<ts>_assets_foundations.sql` containing `ALTER TABLE projects ADD COLUMN description/updated_at`, the three `CREATE TABLE`s, the repos index + unique constraint, and a `-- +goose Down` section reversing all of it. Review it; keep the grants FK on projects untouched.

- [ ] **Step 3: Wire sqlc and the first queries**

Append a third block to `backend/sqlc.yaml` (copy the identity block, change three lines):

```yaml
  - engine: postgresql
    schema: internal/platform/pg/schema.sql
    queries: internal/assets/store/queries.sql
    gen:
      go:
        package: store
        out: internal/assets/store
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
            go_type:
              import: time
              type: Time
          - db_type: timestamptz
            nullable: true
            go_type:
              import: time
              type: Time
              pointer: true
```

`backend/internal/assets/store/queries.sql`:

```sql
-- Assets store queries. sqlc generates the Go into this package:
-- `task db:sqlc` after any change here or in schema.sql.

-- name: CreateProject :one
INSERT INTO projects (name, description)
VALUES ($1, $2)
RETURNING id, name, description, created_at, updated_at;

-- name: ListProjects :many
SELECT id, name, description, created_at, updated_at
FROM projects ORDER BY name;

-- name: ListProjectsByIDs :many
SELECT id, name, description, created_at, updated_at
FROM projects WHERE id = ANY(sqlc.arg(ids)::uuid[]) ORDER BY name;

-- name: GetProject :one
SELECT id, name, description, created_at, updated_at
FROM projects WHERE id = $1;

-- name: UpdateProject :one
UPDATE projects SET name = $2, description = $3, updated_at = $4
WHERE id = $1
RETURNING id, name, description, created_at, updated_at;

-- name: DeleteProject :execrows
DELETE FROM projects WHERE id = $1;

-- name: ProjectExists :one
SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1);
```

Run: `task db:sqlc`
Expected: `backend/internal/assets/store/{db.go,models.go,queries.sql.go}` generated.

- [ ] **Step 4: Grow the drift gate**

In `Taskfile.yml`, extend the `contracts:check` diff path list with the new store:

```yaml
      - git diff --exit-code -- backend/api/openapi.json web/src/api backend/internal/identity/store backend/internal/platform/audit backend/internal/assets/store
```

- [ ] **Step 5: Verify migration + build green**

Run: `cd backend && go build ./... && go test -tags integration ./internal/platform/pg/ && cd .. && task contracts:check`
Expected: PASS — the migration up-down test covers the new migration via the embedded set; no contract drift (no new operations yet).

- [ ] **Step 6: Commit**

```bash
git add backend/internal/platform/pg backend/internal/assets backend/sqlc.yaml Taskfile.yml
git commit -m "feat(backend): assets schema, migration, and sqlc store"
```

---
### Task 6: Assets context bootstrap and the projects service

The second bounded context begins: domain types for the whole slice, the service struct, and the projects operations. Every write runs in `pg.WithTx` and audits inside the same transaction.

**Files:**
- Create: `backend/internal/assets/assets.go` (domain types + validation)
- Create: `backend/internal/assets/service.go` (Service, shared helpers)
- Create: `backend/internal/assets/projects.go`
- Create: `backend/internal/assets/testutil_integration_test.go`
- Create: `backend/internal/assets/projects_integration_test.go`

**Interfaces:**
- Consumes: `store` (Task 5), `pg.WithTx` (Task 2), `authn.Principal` (Task 1), `secrets.Keyset` (Task 4), `audit.Recorder`, `clock.Clock`.
- Produces: `assets.NewService(pool *pgxpool.Pool, c clock.Clock, rec *audit.Recorder, keys *secrets.Keyset) *Service` · `Project{ID uuid.UUID; Name, Description string; CreatedAt, UpdatedAt time.Time}` · methods `CreateProject(ctx, actor authn.Principal, name, description string) (Project, error)`, `ListProjects(ctx) ([]Project, error)`, `ListProjectsByIDs(ctx, ids []uuid.UUID) ([]Project, error)`, `GetProject(ctx, id uuid.UUID) (Project, error)`, `UpdateProject(ctx, actor authn.Principal, id uuid.UUID, upd ProjectUpdate) (Project, error)` with `ProjectUpdate{Name, Description *string}`, `DeleteProject(ctx, actor authn.Principal, id uuid.UUID) error`, `ProjectExists(ctx, id uuid.UUID) (bool, error)` · domain enums `ForgeKind` (`ForgeGitHub`, `ForgeGitLab`), `CredentialKind` (`CredentialForgeToken`, `CredentialModelKey`), `HarnessKind` (`HarnessClaudeCode`, `HarnessCodex`) and structs `Repo`, `Credential`, `Harness` (used by Tasks 7–9).

- [ ] **Step 1: Write the domain types and service core**

`backend/internal/assets/assets.go`:

```go
// Package assets is the assets bounded context (spec §2): projects, repos,
// encrypted credentials, and the harness registry. Peers never import it
// except through app wiring; it owns its transactions end to end.
package assets

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

type ForgeKind string

const (
	ForgeGitHub ForgeKind = "github"
	ForgeGitLab ForgeKind = "gitlab"
)

func (f ForgeKind) Valid() bool { return f == ForgeGitHub || f == ForgeGitLab }

type CredentialKind string

const (
	CredentialForgeToken CredentialKind = "forge_token"
	CredentialModelKey   CredentialKind = "model_key"
)

func (k CredentialKind) Valid() bool { return k == CredentialForgeToken || k == CredentialModelKey }

type HarnessKind string

const (
	HarnessClaudeCode HarnessKind = "claude-code"
	HarnessCodex      HarnessKind = "codex"
)

func (k HarnessKind) Valid() bool { return k == HarnessClaudeCode || k == HarnessCodex }

// Project groups repos, policies, and grants (spec §2).
type Project struct {
	ID          uuid.UUID
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Repo is one forge repository inside a project.
type Repo struct {
	ID            uuid.UUID
	ProjectID     uuid.UUID
	Forge         ForgeKind
	Slug          string
	DefaultBranch string
	CredentialID  uuid.UUID
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Credential is the METADATA view of an encrypted secret — plaintext
// never rides this struct; use-time decryption goes through UseCredential.
type Credential struct {
	ID        uuid.UUID
	Name      string
	Kind      CredentialKind
	KeyID     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Harness is one admin-registered agent harness (spec §2).
type Harness struct {
	ID               uuid.UUID
	Name             string
	Kind             HarnessKind
	ImageRef         string
	ProviderEndpoint string
	CredentialID     uuid.UUID
	Defaults         map[string]any
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Validation pins (plan header).
const (
	maxNameLen        = 100
	maxDescriptionLen = 500
	maxSlugLen        = 200
	maxBranchLen      = 200
	maxImageRefLen    = 300
	maxSecretLen      = 4096
	maxDefaultsLen    = 4096
)

var slugPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)+$`)

func validateName(field, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxNameLen {
		return "", xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: field, Message: fmt.Sprintf("must be 1-%d characters", maxNameLen),
		})
	}
	return name, nil
}

func validateDescription(desc string) error {
	if len(desc) > maxDescriptionLen {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "description", Message: fmt.Sprintf("must be at most %d characters", maxDescriptionLen),
		})
	}
	return nil
}
```

`backend/internal/assets/service.go`:

```go
package assets

import (
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
)

type Service struct {
	pool  *pgxpool.Pool
	clock clock.Clock
	audit *audit.Recorder
	keys  *secrets.Keyset
}

func NewService(pool *pgxpool.Pool, c clock.Clock, rec *audit.Recorder, keys *secrets.Keyset) *Service {
	return &Service{pool: pool, clock: c, audit: rec, keys: keys}
}

func actorID(p authn.Principal) uuid.NullUUID {
	return uuid.NullUUID{UUID: p.UserID, Valid: true}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// fkViolation returns the violated constraint name for 23503 errors, so
// callers can turn "which FK" into the right client-facing error.
func fkViolation(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return pgErr.ConstraintName, true
	}
	return "", false
}
```

- [ ] **Step 2: Write the failing projects integration tests**

`backend/internal/assets/testutil_integration_test.go` — audit rows FK to `users`, and assets must not import identity, so seed actors with raw SQL:

```go
//go:build integration

package assets_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

const testKeyset = "test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func newTestService(t *testing.T) (*assets.Service, *pgxpool.Pool, *clock.Fake) {
	t.Helper()
	pool := testdb.New(t)
	fake := clock.NewFake(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	keys, err := secrets.ParseKeyset(testKeyset)
	if err != nil {
		t.Fatal(err)
	}
	return assets.NewService(pool, fake, audit.NewRecorder(fake), keys), pool, fake
}

// seedActor inserts a user row directly (assets never imports identity)
// and returns a principal for it.
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
```

`backend/internal/assets/projects_integration_test.go`:

```go
//go:build integration

package assets_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func TestProjects_CRUDWithAudit(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	p, err := svc.CreateProject(ctx, admin, "  caesar  ", "dogfood project")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "caesar" || p.Description != "dogfood project" {
		t.Fatalf("project = %+v (name must be trimmed)", p)
	}

	if _, err := svc.CreateProject(ctx, admin, "caesar", ""); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("duplicate name class = %v, want conflict", xerrors.ClassOf(err))
	}
	if _, err := svc.CreateProject(ctx, admin, "", ""); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("empty name must be invalid")
	}

	name := "caesar-renamed"
	upd, err := svc.UpdateProject(ctx, admin, p.ID, assets.ProjectUpdate{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Name != "caesar-renamed" || upd.Description != "dogfood project" {
		t.Fatalf("update = %+v (unset fields must persist)", upd)
	}

	got, err := svc.GetProject(ctx, p.ID)
	if err != nil || got.Name != "caesar-renamed" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	all, err := svc.ListProjects(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("list = %d projects, %v", len(all), err)
	}
	some, err := svc.ListProjectsByIDs(ctx, []uuid.UUID{p.ID})
	if err != nil || len(some) != 1 {
		t.Fatalf("list by ids = %d, %v", len(some), err)
	}
	if some, err := svc.ListProjectsByIDs(ctx, nil); err != nil || len(some) != 0 {
		t.Fatalf("empty ids must list nothing, got %d, %v", len(some), err)
	}

	if err := svc.DeleteProject(ctx, admin, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteProject(ctx, admin, p.ID); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("second delete must be not found")
	}
	if _, err := svc.GetProject(ctx, p.ID); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("get after delete must be not found")
	}

	for action, want := range map[string]int{"project.created": 1, "project.updated": 1, "project.deleted": 1} {
		if got := auditCount(t, pool, action); got != want {
			t.Errorf("%s audits = %d, want %d", action, got, want)
		}
	}
}

func TestDeleteProject_RefusedWhileReposExist(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	p, err := svc.CreateProject(ctx, admin, "with-repo", "")
	if err != nil {
		t.Fatal(err)
	}
	// Raw rows: the repos/credentials services arrive in later tasks.
	credID := uuid.New()
	if _, err := pool.Exec(ctx,
		"INSERT INTO credentials (id, name, kind, key_id, wrapped_dek, ciphertext) VALUES ($1, 'gh', 'forge_token', 'test', '\\x00', '\\x00')",
		credID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO repos (project_id, forge_kind, slug, credential_id) VALUES ($1, 'github', 'giiorg/caesar', $2)",
		p.ID, credID); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteProject(ctx, admin, p.ID); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("class = %v, want conflict while repos exist", xerrors.ClassOf(err))
	}
	ok, err := svc.ProjectExists(ctx, p.ID)
	if err != nil || !ok {
		t.Fatalf("project must survive: %v %v", ok, err)
	}
}
```

- [ ] **Step 3: Run to see them fail**

Run: `cd backend && go vet ./internal/assets/`
Expected: FAIL — projects methods undefined.

- [ ] **Step 4: Implement the projects service**

`backend/internal/assets/projects.go`:

```go
package assets

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/assets/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func projectFromRow(r store.Project) Project {
	return Project{ID: r.ID, Name: r.Name, Description: r.Description, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// ProjectUpdate is a partial update; nil fields are left unchanged.
type ProjectUpdate struct {
	Name        *string
	Description *string
}

func (s *Service) CreateProject(ctx context.Context, actor authn.Principal, name, description string) (Project, error) {
	name, err := validateName("name", name)
	if err != nil {
		return Project{}, err
	}
	if err := validateDescription(description); err != nil {
		return Project{}, err
	}

	var p Project
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := store.New(tx).CreateProject(ctx, store.CreateProjectParams{Name: name, Description: description})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "project name already taken")
		}
		if err != nil {
			return fmt.Errorf("assets: create project: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "project.created", SubjectKind: "project", SubjectID: row.ID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"name": name},
		}); err != nil {
			return err
		}
		p = projectFromRow(row)
		return nil
	})
	if err != nil {
		return Project{}, err
	}
	return p, nil
}

func (s *Service) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := store.New(s.pool).ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("assets: list projects: %w", err)
	}
	out := make([]Project, 0, len(rows))
	for _, r := range rows {
		out = append(out, projectFromRow(r))
	}
	return out, nil
}

func (s *Service) ListProjectsByIDs(ctx context.Context, ids []uuid.UUID) ([]Project, error) {
	if len(ids) == 0 {
		return []Project{}, nil
	}
	rows, err := store.New(s.pool).ListProjectsByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("assets: list projects by ids: %w", err)
	}
	out := make([]Project, 0, len(rows))
	for _, r := range rows {
		out = append(out, projectFromRow(r))
	}
	return out, nil
}

func (s *Service) GetProject(ctx context.Context, id uuid.UUID) (Project, error) {
	row, err := store.New(s.pool).GetProject(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, xerrors.E(xerrors.ClassNotFound, "no such project")
	}
	if err != nil {
		return Project{}, fmt.Errorf("assets: get project: %w", err)
	}
	return projectFromRow(row), nil
}

func (s *Service) UpdateProject(ctx context.Context, actor authn.Principal, id uuid.UUID, upd ProjectUpdate) (Project, error) {
	var p Project
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		current, err := q.GetProject(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return xerrors.E(xerrors.ClassNotFound, "no such project")
		}
		if err != nil {
			return fmt.Errorf("assets: get project: %w", err)
		}
		name, description := current.Name, current.Description
		if upd.Name != nil {
			if name, err = validateName("name", *upd.Name); err != nil {
				return err
			}
		}
		if upd.Description != nil {
			if err := validateDescription(*upd.Description); err != nil {
				return err
			}
			description = *upd.Description
		}
		row, err := q.UpdateProject(ctx, store.UpdateProjectParams{
			ID: id, Name: name, Description: description, UpdatedAt: s.clock.Now(),
		})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "project name already taken")
		}
		if err != nil {
			return fmt.Errorf("assets: update project: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "project.updated", SubjectKind: "project", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"name": name},
		}); err != nil {
			return err
		}
		p = projectFromRow(row)
		return nil
	})
	if err != nil {
		return Project{}, err
	}
	return p, nil
}

func (s *Service) DeleteProject(ctx context.Context, actor authn.Principal, id uuid.UUID) error {
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).DeleteProject(ctx, id)
		if _, fk := fkViolation(err); fk {
			return xerrors.E(xerrors.ClassConflict, "project still has repos")
		}
		if err != nil {
			return fmt.Errorf("assets: delete project: %w", err)
		}
		if n == 0 {
			return xerrors.E(xerrors.ClassNotFound, "no such project")
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "project.deleted", SubjectKind: "project", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
		})
	})
}

// ProjectExists is the identity API's ProjectChecker port (grant
// endpoints 404 on unknown projects without importing assets' tables).
func (s *Service) ProjectExists(ctx context.Context, id uuid.UUID) (bool, error) {
	ok, err := store.New(s.pool).ProjectExists(ctx, id)
	if err != nil {
		return false, fmt.Errorf("assets: project exists: %w", err)
	}
	return ok, nil
}
```

Note: `grants` FKs `projects` with `ON DELETE CASCADE` (Plan 02), so deleting a project silently drops its grant rows — correct, they are meaningless without the project. Repos FK is `RESTRICT` and produces the 409.

- [ ] **Step 5: Run the tests, verify pass**

Run: `cd backend && go test -tags integration ./internal/assets/ -run TestProjects -v && go test -tags integration ./internal/assets/ -run TestDeleteProject -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/assets
git commit -m "feat(backend): assets context with projects service"
```

---

### Task 7: Credentials service — envelope encryption at rest, audited use

Secrets in, metadata out, plaintext only through `UseCredential` (audited). The credential id is generated app-side because it is the AAD binding the ciphertext to its row.

**Files:**
- Modify: `backend/internal/assets/store/queries.sql` (credential queries; regen)
- Create: `backend/internal/assets/credentials.go`
- Create: `backend/internal/assets/credentials_integration_test.go`

**Interfaces:**
- Consumes: `secrets.Keyset` (Task 4), Task 6's service core.
- Produces: `CreateCredential(ctx, actor authn.Principal, name string, kind CredentialKind, secret []byte) (Credential, error)`, `ListCredentials(ctx) ([]Credential, error)`, `GetCredential(ctx, id uuid.UUID) (Credential, error)`, `RotateCredentialSecret(ctx, actor authn.Principal, id uuid.UUID, secret []byte) (Credential, error)`, `DeleteCredential(ctx, actor authn.Principal, id uuid.UUID) error`, `UseCredential(ctx, id uuid.UUID, purpose string) ([]byte, error)`. Repos (Task 8) and harnesses (Task 9) validate referenced kinds via `GetCredential`-in-tx (`getCredentialKind` helper produced here).

- [ ] **Step 1: Add the credential queries**

Append to `backend/internal/assets/store/queries.sql`:

```sql
-- name: CreateCredential :one
INSERT INTO credentials (id, name, kind, key_id, wrapped_dek, ciphertext)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, name, kind, key_id, created_at, updated_at;

-- name: ListCredentials :many
SELECT id, name, kind, key_id, created_at, updated_at
FROM credentials ORDER BY name;

-- name: GetCredential :one
SELECT id, name, kind, key_id, created_at, updated_at
FROM credentials WHERE id = $1;

-- name: GetCredentialRow :one
SELECT id, name, kind, key_id, wrapped_dek, ciphertext, created_at, updated_at
FROM credentials WHERE id = $1;

-- name: ListCredentialRowsForUpdate :many
SELECT id, name, kind, key_id, wrapped_dek, ciphertext, created_at, updated_at
FROM credentials ORDER BY id
FOR UPDATE;

-- name: UpdateCredentialSecret :one
UPDATE credentials SET key_id = $2, wrapped_dek = $3, ciphertext = $4, updated_at = $5
WHERE id = $1
RETURNING id, name, kind, key_id, created_at, updated_at;

-- name: DeleteCredential :execrows
DELETE FROM credentials WHERE id = $1;
```

Run: `task db:sqlc`
Expected: regenerated store compiles (`cd backend && go build ./internal/assets/...`).

- [ ] **Step 2: Write the failing integration tests**

`backend/internal/assets/credentials_integration_test.go`:

```go
//go:build integration

package assets_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func TestCredentials_SecretIsEncryptedAtRestAndUseIsAudited(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)
	secret := []byte("ghp_averysecrettoken1234")

	cred, err := svc.CreateCredential(ctx, admin, "github-bot", assets.CredentialForgeToken, secret)
	if err != nil {
		t.Fatal(err)
	}
	if cred.KeyID != "test" {
		t.Fatalf("key_id = %q, want active keyset id", cred.KeyID)
	}

	// The plaintext must not appear anywhere in the row.
	var wrapped, ct []byte
	if err := pool.QueryRow(ctx, "SELECT wrapped_dek, ciphertext FROM credentials WHERE id = $1", cred.ID).Scan(&wrapped, &ct); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, secret) || bytes.Contains(wrapped, secret) {
		t.Fatal("plaintext stored")
	}

	got, err := svc.UseCredential(ctx, cred.ID, "test-clone")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("use = %q, want original secret", got)
	}
	var purpose string
	if err := pool.QueryRow(ctx,
		"SELECT detail->>'purpose' FROM audit_entries WHERE action = 'credential.used'").Scan(&purpose); err != nil {
		t.Fatal(err)
	}
	if purpose != "test-clone" {
		t.Fatalf("audited purpose = %q", purpose)
	}
}

func TestCredentials_RotateDeleteAndGuards(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	cred, err := svc.CreateCredential(ctx, admin, "anthropic", assets.CredentialModelKey, []byte("sk-ant-first"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateCredential(ctx, admin, "anthropic", assets.CredentialModelKey, []byte("x")); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatal("duplicate name must conflict")
	}
	if _, err := svc.CreateCredential(ctx, admin, "big", assets.CredentialModelKey, bytes.Repeat([]byte("a"), 4097)); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("oversized secret must be invalid")
	}
	if _, err := svc.CreateCredential(ctx, admin, "empty", assets.CredentialModelKey, nil); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("empty secret must be invalid")
	}
	if _, err := svc.CreateCredential(ctx, admin, "weird", "ssh_key", []byte("x")); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("unknown kind must be invalid")
	}

	if _, err := svc.RotateCredentialSecret(ctx, admin, cred.ID, []byte("sk-ant-second")); err != nil {
		t.Fatal(err)
	}
	got, err := svc.UseCredential(ctx, cred.ID, "post-rotate")
	if err != nil || string(got) != "sk-ant-second" {
		t.Fatalf("after rotate: %q, %v", got, err)
	}

	// A ciphertext moved onto another row must not decrypt (AAD binding).
	other, err := svc.CreateCredential(ctx, admin, "other", assets.CredentialModelKey, []byte("sk-other"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"UPDATE credentials SET wrapped_dek = (SELECT wrapped_dek FROM credentials WHERE id = $1), ciphertext = (SELECT ciphertext FROM credentials WHERE id = $1) WHERE id = $2",
		cred.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UseCredential(ctx, other.ID, "swapped"); err == nil {
		t.Fatal("swapped ciphertext must fail to open")
	}

	if err := svc.DeleteCredential(ctx, admin, cred.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UseCredential(ctx, cred.ID, "gone"); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("use after delete must be not found")
	}
	if err := svc.DeleteCredential(ctx, admin, uuid.New()); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("unknown delete must be not found")
	}

	for action, want := range map[string]int{
		"credential.created": 3, "credential.rotated": 1, "credential.deleted": 1,
	} {
		if got := auditCount(t, pool, action); got != want {
			t.Errorf("%s audits = %d, want %d", action, got, want)
		}
	}
}

func TestDeleteCredential_RefusedWhileReferenced(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	cred, err := svc.CreateCredential(ctx, admin, "gh", assets.CredentialForgeToken, []byte("tok"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.CreateProject(ctx, admin, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO repos (project_id, forge_kind, slug, credential_id) VALUES ($1, 'github', 'a/b', $2)",
		p.ID, cred.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteCredential(ctx, admin, cred.ID); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("class = %v, want conflict while referenced", xerrors.ClassOf(err))
	}
}
```

- [ ] **Step 3: Run to see them fail**

Run: `cd backend && go vet ./internal/assets/`
Expected: FAIL — credential methods undefined.

- [ ] **Step 4: Implement the credentials service**

`backend/internal/assets/credentials.go`:

```go
package assets

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/assets/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func credentialFromRow(r store.Credential) Credential {
	return Credential{
		ID: r.ID, Name: r.Name, Kind: CredentialKind(r.Kind), KeyID: r.KeyID,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func validateSecret(secret []byte) error {
	if len(secret) == 0 || len(secret) > maxSecretLen {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "secret", Message: fmt.Sprintf("must be 1-%d bytes", maxSecretLen),
		})
	}
	return nil
}

// CreateCredential seals secret under the active master key. The id is
// generated app-side: it is the AAD binding ciphertext to row.
func (s *Service) CreateCredential(ctx context.Context, actor authn.Principal, name string, kind CredentialKind, secret []byte) (Credential, error) {
	name, err := validateName("name", name)
	if err != nil {
		return Credential{}, err
	}
	if !kind.Valid() {
		return Credential{}, xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "kind", Message: "must be forge_token or model_key"})
	}
	if err := validateSecret(secret); err != nil {
		return Credential{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Credential{}, fmt.Errorf("assets: credential id: %w", err)
	}
	env, err := s.keys.Seal(secret, []byte(id.String()))
	if err != nil {
		return Credential{}, fmt.Errorf("assets: seal credential: %w", err)
	}

	var c Credential
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := store.New(tx).CreateCredential(ctx, store.CreateCredentialParams{
			ID: id, Name: name, Kind: string(kind),
			KeyID: env.KeyID, WrappedDek: env.WrappedDEK, Ciphertext: env.Ciphertext,
		})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "credential name already taken")
		}
		if err != nil {
			return fmt.Errorf("assets: create credential: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "credential.created", SubjectKind: "credential", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"name": name, "kind": string(kind), "key_id": env.KeyID},
		}); err != nil {
			return err
		}
		c = credentialFromRow(row)
		return nil
	})
	if err != nil {
		return Credential{}, err
	}
	return c, nil
}

func (s *Service) ListCredentials(ctx context.Context) ([]Credential, error) {
	rows, err := store.New(s.pool).ListCredentials(ctx)
	if err != nil {
		return nil, fmt.Errorf("assets: list credentials: %w", err)
	}
	out := make([]Credential, 0, len(rows))
	for _, r := range rows {
		out = append(out, credentialFromRow(r))
	}
	return out, nil
}

func (s *Service) GetCredential(ctx context.Context, id uuid.UUID) (Credential, error) {
	row, err := store.New(s.pool).GetCredential(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, xerrors.E(xerrors.ClassNotFound, "no such credential")
	}
	if err != nil {
		return Credential{}, fmt.Errorf("assets: get credential: %w", err)
	}
	return credentialFromRow(row), nil
}

// getCredentialKind reads a credential's kind inside a caller's tx —
// repos/harnesses validate referenced kinds with it. Kind never changes
// after creation, so this is race-free.
func getCredentialKind(ctx context.Context, q *store.Queries, id uuid.UUID) (CredentialKind, error) {
	row, err := q.GetCredential(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", xerrors.E(xerrors.ClassNotFound, "no such credential")
	}
	if err != nil {
		return "", fmt.Errorf("assets: get credential: %w", err)
	}
	return CredentialKind(row.Kind), nil
}

func (s *Service) RotateCredentialSecret(ctx context.Context, actor authn.Principal, id uuid.UUID, secret []byte) (Credential, error) {
	if err := validateSecret(secret); err != nil {
		return Credential{}, err
	}
	env, err := s.keys.Seal(secret, []byte(id.String()))
	if err != nil {
		return Credential{}, fmt.Errorf("assets: seal credential: %w", err)
	}

	var c Credential
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := store.New(tx).UpdateCredentialSecret(ctx, store.UpdateCredentialSecretParams{
			ID: id, KeyID: env.KeyID, WrappedDek: env.WrappedDEK, Ciphertext: env.Ciphertext, UpdatedAt: s.clock.Now(),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return xerrors.E(xerrors.ClassNotFound, "no such credential")
		}
		if err != nil {
			return fmt.Errorf("assets: rotate credential: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "credential.rotated", SubjectKind: "credential", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"key_id": env.KeyID},
		}); err != nil {
			return err
		}
		c = credentialFromRow(row)
		return nil
	})
	if err != nil {
		return Credential{}, err
	}
	return c, nil
}

func (s *Service) DeleteCredential(ctx context.Context, actor authn.Principal, id uuid.UUID) error {
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).DeleteCredential(ctx, id)
		if _, fk := fkViolation(err); fk {
			return xerrors.E(xerrors.ClassConflict, "credential is referenced by a repo or harness")
		}
		if err != nil {
			return fmt.Errorf("assets: delete credential: %w", err)
		}
		if n == 0 {
			return xerrors.E(xerrors.ClassNotFound, "no such credential")
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "credential.deleted", SubjectKind: "credential", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
		})
	})
}

// UseCredential decrypts a credential for a control-plane use site. Every
// call is audited (spec §6: every use audited) with the machine purpose;
// the audit row commits before decryption, so even a failed open leaves a
// trace. Plaintext lives only in the returned slice.
func (s *Service) UseCredential(ctx context.Context, id uuid.UUID, purpose string) ([]byte, error) {
	var env secrets.Envelope
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := store.New(tx).GetCredentialRow(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return xerrors.E(xerrors.ClassNotFound, "no such credential")
		}
		if err != nil {
			return fmt.Errorf("assets: get credential row: %w", err)
		}
		env = secrets.Envelope{KeyID: row.KeyID, WrappedDEK: row.WrappedDek, Ciphertext: row.Ciphertext}
		return s.audit.Record(ctx, tx, audit.Entry{
			Action: "credential.used", SubjectKind: "credential", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"purpose": purpose},
		})
	})
	if err != nil {
		return nil, err
	}
	plain, err := s.keys.Open(env, []byte(id.String()))
	if err != nil {
		return nil, fmt.Errorf("assets: open credential %s: %w", id, err)
	}
	return plain, nil
}
```

- [ ] **Step 5: Run the tests, verify pass**

Run: `cd backend && go test -tags integration ./internal/assets/ -run TestCredentials -v && go test -tags integration ./internal/assets/ -run TestDeleteCredential -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/assets
git commit -m "feat(backend): envelope-encrypted credentials with audited use"
```

---
### Task 8: Repos service

**Files:**
- Modify: `backend/internal/assets/store/queries.sql` (repo queries; regen)
- Create: `backend/internal/assets/repos.go`
- Create: `backend/internal/assets/repos_integration_test.go`

**Interfaces:**
- Consumes: Task 6 core, Task 7's `getCredentialKind`.
- Produces: `NewRepo{Forge ForgeKind; Slug, DefaultBranch string; CredentialID uuid.UUID}` · `RepoUpdate{Slug, DefaultBranch *string; CredentialID *uuid.UUID}` · methods `CreateRepo(ctx, actor authn.Principal, projectID uuid.UUID, in NewRepo) (Repo, error)`, `ListRepos(ctx, projectID uuid.UUID) ([]Repo, error)`, `GetRepo(ctx, id uuid.UUID) (Repo, error)`, `UpdateRepo(ctx, actor authn.Principal, id uuid.UUID, upd RepoUpdate) (Repo, error)`, `DeleteRepo(ctx, actor authn.Principal, id uuid.UUID) error`.

- [ ] **Step 1: Add the repo queries and regen**

Append to `backend/internal/assets/store/queries.sql`, then `task db:sqlc`:

```sql
-- name: CreateRepo :one
INSERT INTO repos (project_id, forge_kind, slug, default_branch, credential_id)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, project_id, forge_kind, slug, default_branch, credential_id, created_at, updated_at;

-- name: ListReposByProject :many
SELECT id, project_id, forge_kind, slug, default_branch, credential_id, created_at, updated_at
FROM repos WHERE project_id = $1 ORDER BY slug;

-- name: GetRepo :one
SELECT id, project_id, forge_kind, slug, default_branch, credential_id, created_at, updated_at
FROM repos WHERE id = $1;

-- name: UpdateRepo :one
UPDATE repos SET slug = $2, default_branch = $3, credential_id = $4, updated_at = $5
WHERE id = $1
RETURNING id, project_id, forge_kind, slug, default_branch, credential_id, created_at, updated_at;

-- name: DeleteRepo :execrows
DELETE FROM repos WHERE id = $1;
```

- [ ] **Step 2: Write the failing integration tests**

`backend/internal/assets/repos_integration_test.go`:

```go
//go:build integration

package assets_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func TestRepos_CRUDWithGuards(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	p, err := svc.CreateProject(ctx, admin, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	forgeCred, err := svc.CreateCredential(ctx, admin, "gh", assets.CredentialForgeToken, []byte("tok"))
	if err != nil {
		t.Fatal(err)
	}
	modelCred, err := svc.CreateCredential(ctx, admin, "ant", assets.CredentialModelKey, []byte("sk"))
	if err != nil {
		t.Fatal(err)
	}

	repo, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "giiorg/caesar", CredentialID: forgeCred.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repo.DefaultBranch != "main" {
		t.Fatalf("default branch = %q, want main default", repo.DefaultBranch)
	}

	// Guards.
	if _, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "giiorg/caesar", CredentialID: forgeCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatal("duplicate (project, forge, slug) must conflict")
	}
	if _, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "no-slash", CredentialID: forgeCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("slug without owner segment must be invalid")
	}
	if _, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: "bitbucket", Slug: "a/b", CredentialID: forgeCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("unknown forge must be invalid")
	}
	if _, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "a/b", CredentialID: modelCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("model_key credential on a repo must be invalid")
	}
	if _, err := svc.CreateRepo(ctx, admin, uuid.New(), assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "a/b", CredentialID: forgeCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("unknown project must be not found")
	}
	if _, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "a/b", CredentialID: uuid.New(),
	}); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("unknown credential must be not found")
	}

	// GitLab nested-group slug is legal.
	if _, err := svc.CreateRepo(ctx, admin, p.ID, assets.NewRepo{
		Forge: assets.ForgeGitLab, Slug: "group/sub/proj", CredentialID: forgeCred.ID,
	}); err != nil {
		t.Fatal(err)
	}

	branch := "trunk"
	upd, err := svc.UpdateRepo(ctx, admin, repo.ID, assets.RepoUpdate{DefaultBranch: &branch})
	if err != nil || upd.DefaultBranch != "trunk" || upd.Slug != "giiorg/caesar" {
		t.Fatalf("update = %+v, %v", upd, err)
	}
	if _, err := svc.UpdateRepo(ctx, admin, repo.ID, assets.RepoUpdate{CredentialID: &modelCred.ID}); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("switching to a model_key credential must be invalid")
	}

	repos, err := svc.ListRepos(ctx, p.ID)
	if err != nil || len(repos) != 2 {
		t.Fatalf("list = %d, %v", len(repos), err)
	}
	if _, err := svc.GetRepo(ctx, repo.ID); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteRepo(ctx, admin, repo.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteRepo(ctx, admin, repo.ID); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("second delete must be not found")
	}

	for action, want := range map[string]int{"repo.created": 2, "repo.updated": 1, "repo.deleted": 1} {
		if got := auditCount(t, pool, action); got != want {
			t.Errorf("%s audits = %d, want %d", action, got, want)
		}
	}
}
```

- [ ] **Step 3: Run to see them fail**

Run: `cd backend && go vet ./internal/assets/`
Expected: FAIL — repo methods undefined.

- [ ] **Step 4: Implement the repos service**

`backend/internal/assets/repos.go`:

```go
package assets

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/assets/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func repoFromRow(r store.Repo) Repo {
	return Repo{
		ID: r.ID, ProjectID: r.ProjectID, Forge: ForgeKind(r.ForgeKind), Slug: r.Slug,
		DefaultBranch: r.DefaultBranch, CredentialID: r.CredentialID,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// NewRepo is the create payload; empty DefaultBranch means "main".
type NewRepo struct {
	Forge         ForgeKind
	Slug          string
	DefaultBranch string
	CredentialID  uuid.UUID
}

// RepoUpdate is a partial update; nil fields are left unchanged.
type RepoUpdate struct {
	Slug          *string
	DefaultBranch *string
	CredentialID  *uuid.UUID
}

func validateSlug(slug string) error {
	if len(slug) > maxSlugLen || !slugPattern.MatchString(slug) {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "slug", Message: "must be owner/name (letters, digits, . _ -; gitlab groups may nest)",
		})
	}
	return nil
}

func validateBranch(branch string) error {
	if branch == "" || len(branch) > maxBranchLen || strings.ContainsAny(branch, " \t\r\n") {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "default_branch", Message: fmt.Sprintf("must be 1-%d characters without whitespace", maxBranchLen),
		})
	}
	return nil
}

// requireCredentialKind loads the referenced credential in-tx and rejects
// a kind mismatch (repos take forge_token, harnesses take model_key).
func requireCredentialKind(ctx context.Context, q *store.Queries, id uuid.UUID, want CredentialKind) error {
	kind, err := getCredentialKind(ctx, q, id)
	if err != nil {
		return err
	}
	if kind != want {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "credential_id", Message: fmt.Sprintf("credential kind must be %s", want),
		})
	}
	return nil
}

func (s *Service) CreateRepo(ctx context.Context, actor authn.Principal, projectID uuid.UUID, in NewRepo) (Repo, error) {
	if !in.Forge.Valid() {
		return Repo{}, xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "forge", Message: "must be github or gitlab"})
	}
	if err := validateSlug(in.Slug); err != nil {
		return Repo{}, err
	}
	branch := in.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	if err := validateBranch(branch); err != nil {
		return Repo{}, err
	}

	var repo Repo
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := requireCredentialKind(ctx, q, in.CredentialID, CredentialForgeToken); err != nil {
			return err
		}
		row, err := q.CreateRepo(ctx, store.CreateRepoParams{
			ProjectID: projectID, ForgeKind: string(in.Forge), Slug: in.Slug,
			DefaultBranch: branch, CredentialID: in.CredentialID,
		})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "repo already registered on this project")
		}
		if constraint, fk := fkViolation(err); fk {
			if constraint == "repos_project_id_fkey" {
				return xerrors.E(xerrors.ClassNotFound, "no such project")
			}
			return xerrors.E(xerrors.ClassNotFound, "no such credential")
		}
		if err != nil {
			return fmt.Errorf("assets: create repo: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "repo.created", SubjectKind: "repo", SubjectID: row.ID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"project_id": projectID.String(), "forge": string(in.Forge), "slug": in.Slug},
		}); err != nil {
			return err
		}
		repo = repoFromRow(row)
		return nil
	})
	if err != nil {
		return Repo{}, err
	}
	return repo, nil
}

func (s *Service) ListRepos(ctx context.Context, projectID uuid.UUID) ([]Repo, error) {
	rows, err := store.New(s.pool).ListReposByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("assets: list repos: %w", err)
	}
	out := make([]Repo, 0, len(rows))
	for _, r := range rows {
		out = append(out, repoFromRow(r))
	}
	return out, nil
}

func (s *Service) GetRepo(ctx context.Context, id uuid.UUID) (Repo, error) {
	row, err := store.New(s.pool).GetRepo(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Repo{}, xerrors.E(xerrors.ClassNotFound, "no such repo")
	}
	if err != nil {
		return Repo{}, fmt.Errorf("assets: get repo: %w", err)
	}
	return repoFromRow(row), nil
}

func (s *Service) UpdateRepo(ctx context.Context, actor authn.Principal, id uuid.UUID, upd RepoUpdate) (Repo, error) {
	var repo Repo
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		current, err := q.GetRepo(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return xerrors.E(xerrors.ClassNotFound, "no such repo")
		}
		if err != nil {
			return fmt.Errorf("assets: get repo: %w", err)
		}
		slug, branch, credID := current.Slug, current.DefaultBranch, current.CredentialID
		if upd.Slug != nil {
			if err := validateSlug(*upd.Slug); err != nil {
				return err
			}
			slug = *upd.Slug
		}
		if upd.DefaultBranch != nil {
			if err := validateBranch(*upd.DefaultBranch); err != nil {
				return err
			}
			branch = *upd.DefaultBranch
		}
		if upd.CredentialID != nil {
			if err := requireCredentialKind(ctx, q, *upd.CredentialID, CredentialForgeToken); err != nil {
				return err
			}
			credID = *upd.CredentialID
		}
		row, err := q.UpdateRepo(ctx, store.UpdateRepoParams{
			ID: id, Slug: slug, DefaultBranch: branch, CredentialID: credID, UpdatedAt: s.clock.Now(),
		})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "repo already registered on this project")
		}
		if err != nil {
			return fmt.Errorf("assets: update repo: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "repo.updated", SubjectKind: "repo", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"slug": slug},
		}); err != nil {
			return err
		}
		repo = repoFromRow(row)
		return nil
	})
	if err != nil {
		return Repo{}, err
	}
	return repo, nil
}

func (s *Service) DeleteRepo(ctx context.Context, actor authn.Principal, id uuid.UUID) error {
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).DeleteRepo(ctx, id)
		if err != nil {
			return fmt.Errorf("assets: delete repo: %w", err)
		}
		if n == 0 {
			return xerrors.E(xerrors.ClassNotFound, "no such repo")
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "repo.deleted", SubjectKind: "repo", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
		})
	})
}
```

- [ ] **Step 5: Run the tests, verify pass**

Run: `cd backend && go test -tags integration ./internal/assets/ -run TestRepos -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/assets
git commit -m "feat(backend): repos service"
```

---

### Task 9: Harness registry service

**Files:**
- Modify: `backend/internal/assets/store/queries.sql` (harness queries; regen)
- Create: `backend/internal/assets/harnesses.go`
- Create: `backend/internal/assets/harnesses_integration_test.go`

**Interfaces:**
- Consumes: Task 6 core, Task 7's kind check (`requireCredentialKind` from Task 8).
- Produces: `NewHarness{Name string; Kind HarnessKind; ImageRef, ProviderEndpoint string; CredentialID uuid.UUID; Defaults map[string]any}` · `HarnessUpdate{Name, ImageRef, ProviderEndpoint *string; CredentialID *uuid.UUID; Defaults *map[string]any}` · methods `RegisterHarness(ctx, actor authn.Principal, in NewHarness) (Harness, error)`, `ListHarnesses(ctx) ([]Harness, error)`, `GetHarness(ctx, id uuid.UUID) (Harness, error)`, `UpdateHarness(ctx, actor authn.Principal, id uuid.UUID, upd HarnessUpdate) (Harness, error)`, `DeleteHarness(ctx, actor authn.Principal, id uuid.UUID) error`.

- [ ] **Step 1: Add the harness queries and regen**

Append to `backend/internal/assets/store/queries.sql`, then `task db:sqlc`:

```sql
-- name: RegisterHarness :one
INSERT INTO harnesses (name, kind, image_ref, provider_endpoint, credential_id, defaults)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, name, kind, image_ref, provider_endpoint, credential_id, defaults, created_at, updated_at;

-- name: ListHarnesses :many
SELECT id, name, kind, image_ref, provider_endpoint, credential_id, defaults, created_at, updated_at
FROM harnesses ORDER BY name;

-- name: GetHarness :one
SELECT id, name, kind, image_ref, provider_endpoint, credential_id, defaults, created_at, updated_at
FROM harnesses WHERE id = $1;

-- name: UpdateHarness :one
UPDATE harnesses SET name = $2, image_ref = $3, provider_endpoint = $4, credential_id = $5, defaults = $6, updated_at = $7
WHERE id = $1
RETURNING id, name, kind, image_ref, provider_endpoint, credential_id, defaults, created_at, updated_at;

-- name: DeleteHarness :execrows
DELETE FROM harnesses WHERE id = $1;
```

- [ ] **Step 2: Write the failing integration tests**

`backend/internal/assets/harnesses_integration_test.go`:

```go
//go:build integration

package assets_test

import (
	"context"
	"testing"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func TestHarnesses_RegistryCRUDWithGuards(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	modelCred, err := svc.CreateCredential(ctx, admin, "ant", assets.CredentialModelKey, []byte("sk"))
	if err != nil {
		t.Fatal(err)
	}
	forgeCred, err := svc.CreateCredential(ctx, admin, "gh", assets.CredentialForgeToken, []byte("tok"))
	if err != nil {
		t.Fatal(err)
	}

	h, err := svc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "claude-main", Kind: assets.HarnessClaudeCode,
		ImageRef:         "ghcr.io/giiorg/caesar-harness-claude@sha256:abc",
		ProviderEndpoint: "https://api.anthropic.com",
		CredentialID:     modelCred.ID,
		Defaults:         map[string]any{"model": "claude-sonnet-5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.Defaults["model"] != "claude-sonnet-5" {
		t.Fatalf("defaults roundtrip = %+v", h.Defaults)
	}

	// Guards.
	if _, err := svc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "claude-main", Kind: assets.HarnessClaudeCode, ImageRef: "x", ProviderEndpoint: "https://a.example", CredentialID: modelCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatal("duplicate name must conflict")
	}
	if _, err := svc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "bad-kind", Kind: "aider", ImageRef: "x", ProviderEndpoint: "https://a.example", CredentialID: modelCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("unknown kind must be invalid")
	}
	if _, err := svc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "http-endpoint", Kind: assets.HarnessCodex, ImageRef: "x", ProviderEndpoint: "http://api.openai.com", CredentialID: modelCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("plain http on a non-localhost endpoint must be invalid")
	}
	if _, err := svc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "dev-local", Kind: assets.HarnessCodex, ImageRef: "x", ProviderEndpoint: "http://localhost:4000", CredentialID: modelCred.ID,
	}); err != nil {
		t.Fatalf("http localhost must be allowed for dev: %v", err)
	}
	if _, err := svc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "wrong-cred", Kind: assets.HarnessCodex, ImageRef: "x", ProviderEndpoint: "https://a.example", CredentialID: forgeCred.ID,
	}); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("forge_token credential on a harness must be invalid")
	}

	ref := "ghcr.io/giiorg/caesar-harness-claude@sha256:def"
	upd, err := svc.UpdateHarness(ctx, admin, h.ID, assets.HarnessUpdate{ImageRef: &ref})
	if err != nil || upd.ImageRef != ref || upd.Name != "claude-main" {
		t.Fatalf("update = %+v, %v", upd, err)
	}

	all, err := svc.ListHarnesses(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("list = %d, %v", len(all), err)
	}
	if _, err := svc.GetHarness(ctx, h.ID); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteHarness(ctx, admin, h.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteHarness(ctx, admin, h.ID); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("second delete must be not found")
	}

	for action, want := range map[string]int{"harness.registered": 2, "harness.updated": 1, "harness.deleted": 1} {
		if got := auditCount(t, pool, action); got != want {
			t.Errorf("%s audits = %d, want %d", action, got, want)
		}
	}
}
```

- [ ] **Step 3: Run to see them fail**

Run: `cd backend && go vet ./internal/assets/`
Expected: FAIL — harness methods undefined.

- [ ] **Step 4: Implement the harness service**

`backend/internal/assets/harnesses.go`:

```go
package assets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/assets/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// NewHarness is the registration payload; nil Defaults means {}.
type NewHarness struct {
	Name             string
	Kind             HarnessKind
	ImageRef         string
	ProviderEndpoint string
	CredentialID     uuid.UUID
	Defaults         map[string]any
}

// HarnessUpdate is a partial update; nil fields are left unchanged.
// Kind is immutable — register a new harness instead.
type HarnessUpdate struct {
	Name             *string
	ImageRef         *string
	ProviderEndpoint *string
	CredentialID     *uuid.UUID
	Defaults         *map[string]any
}

func harnessFromRow(r store.Harness) (Harness, error) {
	defaults := map[string]any{}
	if len(r.Defaults) > 0 {
		if err := json.Unmarshal(r.Defaults, &defaults); err != nil {
			return Harness{}, fmt.Errorf("assets: harness defaults: %w", err)
		}
	}
	return Harness{
		ID: r.ID, Name: r.Name, Kind: HarnessKind(r.Kind), ImageRef: r.ImageRef,
		ProviderEndpoint: r.ProviderEndpoint, CredentialID: r.CredentialID, Defaults: defaults,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

func validateImageRef(ref string) error {
	if ref == "" || len(ref) > maxImageRefLen {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "image_ref", Message: fmt.Sprintf("must be 1-%d characters", maxImageRefLen),
		})
	}
	return nil
}

// validateEndpoint pins provider endpoints to https; plain http passes
// only for localhost (dev). The egress gateway inherits this URL later.
func validateEndpoint(endpoint string) error {
	invalid := xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
		Field: "provider_endpoint", Message: "must be an https URL (http allowed for localhost only)",
	})
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return invalid
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if h := u.Hostname(); h == "localhost" || h == "127.0.0.1" {
			return nil
		}
	}
	return invalid
}

func marshalDefaults(d map[string]any) ([]byte, error) {
	if d == nil {
		d = map[string]any{}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, xerrors.Wrap(xerrors.ClassInvalid, "defaults must be a JSON object", err)
	}
	if len(raw) > maxDefaultsLen {
		return nil, xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "defaults", Message: fmt.Sprintf("must serialize to at most %d bytes", maxDefaultsLen),
		})
	}
	return raw, nil
}

func (s *Service) RegisterHarness(ctx context.Context, actor authn.Principal, in NewHarness) (Harness, error) {
	name, err := validateName("name", in.Name)
	if err != nil {
		return Harness{}, err
	}
	if !in.Kind.Valid() {
		return Harness{}, xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "kind", Message: "must be claude-code or codex"})
	}
	if err := validateImageRef(in.ImageRef); err != nil {
		return Harness{}, err
	}
	if err := validateEndpoint(in.ProviderEndpoint); err != nil {
		return Harness{}, err
	}
	raw, err := marshalDefaults(in.Defaults)
	if err != nil {
		return Harness{}, err
	}

	var h Harness
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := requireCredentialKind(ctx, q, in.CredentialID, CredentialModelKey); err != nil {
			return err
		}
		row, err := q.RegisterHarness(ctx, store.RegisterHarnessParams{
			Name: name, Kind: string(in.Kind), ImageRef: in.ImageRef,
			ProviderEndpoint: in.ProviderEndpoint, CredentialID: in.CredentialID, Defaults: raw,
		})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "harness name already taken")
		}
		if err != nil {
			return fmt.Errorf("assets: register harness: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "harness.registered", SubjectKind: "harness", SubjectID: row.ID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"name": name, "kind": string(in.Kind), "image_ref": in.ImageRef},
		}); err != nil {
			return err
		}
		h, err = harnessFromRow(row)
		return err
	})
	if err != nil {
		return Harness{}, err
	}
	return h, nil
}

func (s *Service) ListHarnesses(ctx context.Context) ([]Harness, error) {
	rows, err := store.New(s.pool).ListHarnesses(ctx)
	if err != nil {
		return nil, fmt.Errorf("assets: list harnesses: %w", err)
	}
	out := make([]Harness, 0, len(rows))
	for _, r := range rows {
		h, err := harnessFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

func (s *Service) GetHarness(ctx context.Context, id uuid.UUID) (Harness, error) {
	row, err := store.New(s.pool).GetHarness(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Harness{}, xerrors.E(xerrors.ClassNotFound, "no such harness")
	}
	if err != nil {
		return Harness{}, fmt.Errorf("assets: get harness: %w", err)
	}
	return harnessFromRow(row)
}

func (s *Service) UpdateHarness(ctx context.Context, actor authn.Principal, id uuid.UUID, upd HarnessUpdate) (Harness, error) {
	var h Harness
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		current, err := q.GetHarness(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return xerrors.E(xerrors.ClassNotFound, "no such harness")
		}
		if err != nil {
			return fmt.Errorf("assets: get harness: %w", err)
		}
		name, imageRef, endpoint, credID, raw :=
			current.Name, current.ImageRef, current.ProviderEndpoint, current.CredentialID, current.Defaults
		if upd.Name != nil {
			if name, err = validateName("name", *upd.Name); err != nil {
				return err
			}
		}
		if upd.ImageRef != nil {
			if err := validateImageRef(*upd.ImageRef); err != nil {
				return err
			}
			imageRef = *upd.ImageRef
		}
		if upd.ProviderEndpoint != nil {
			if err := validateEndpoint(*upd.ProviderEndpoint); err != nil {
				return err
			}
			endpoint = *upd.ProviderEndpoint
		}
		if upd.CredentialID != nil {
			if err := requireCredentialKind(ctx, q, *upd.CredentialID, CredentialModelKey); err != nil {
				return err
			}
			credID = *upd.CredentialID
		}
		if upd.Defaults != nil {
			if raw, err = marshalDefaults(*upd.Defaults); err != nil {
				return err
			}
		}
		row, err := q.UpdateHarness(ctx, store.UpdateHarnessParams{
			ID: id, Name: name, ImageRef: imageRef, ProviderEndpoint: endpoint,
			CredentialID: credID, Defaults: raw, UpdatedAt: s.clock.Now(),
		})
		if isUniqueViolation(err) {
			return xerrors.E(xerrors.ClassConflict, "harness name already taken")
		}
		if err != nil {
			return fmt.Errorf("assets: update harness: %w", err)
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "harness.updated", SubjectKind: "harness", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"name": name},
		}); err != nil {
			return err
		}
		h, err = harnessFromRow(row)
		return err
	})
	if err != nil {
		return Harness{}, err
	}
	return h, nil
}

func (s *Service) DeleteHarness(ctx context.Context, actor authn.Principal, id uuid.UUID) error {
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).DeleteHarness(ctx, id)
		if err != nil {
			return fmt.Errorf("assets: delete harness: %w", err)
		}
		if n == 0 {
			return xerrors.E(xerrors.ClassNotFound, "no such harness")
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "harness.deleted", SubjectKind: "harness", SubjectID: id.String(),
			CorrelationID: httpx.CorrelationID(ctx),
		})
	})
}
```

- [ ] **Step 5: Run the tests, verify pass**

Run: `cd backend && go test -tags integration ./internal/assets/ -run TestHarnesses -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/assets
git commit -m "feat(backend): harness registry service"
```

---
### Task 10: Assets HTTP API — auth ports, projects & repos endpoints, contracts extension

The assets API authenticates through the `authn` ports (implemented by identity, wired in app — **never** imported here, tests included: use fakes). `internal/contracts` learns to reflect multiple operation tables and grows an override map for id-bearing request bodies.

**Files:**
- Create: `backend/internal/assets/api/dto.go`
- Create: `backend/internal/assets/api/api.go`
- Create: `backend/internal/assets/api/api_integration_test.go`
- Modify: `backend/internal/contracts/contracts.go`
- Regenerate: `backend/api/openapi.json`, `web/src/api/schema.d.ts`

**Interfaces:**
- Consumes: `assets.Service` (Tasks 6–8), `authn.SessionAuthenticator`, `authn.Authorizer`, `authn.SessionCookie` (Task 1).
- Produces: `assetsapi.New(svc *assets.Service, auth authn.SessionAuthenticator, authz authn.Authorizer, grants GrantSource) *API` · `assetsapi.Operations(a *API) []httpx.Operation` · port `GrantSource{GrantsForUser(ctx, userID uuid.UUID) ([]authn.Grant, error)}` (identity.Service satisfies it) · DTOs `ProjectResponse`, `ProjectListResponse`, `CreateProjectRequest`, `UpdateProjectRequest`, `RepoResponse`, `RepoListResponse`, `CreateRepoRequest`, `UpdateRepoRequest` · contracts: `requestOverrides` map keyed by operation ID (Tasks 11 and 13 extend it).

- [ ] **Step 1: Write the DTOs**

`backend/internal/assets/api/dto.go`:

```go
// Package api is assets' HTTP surface. DTO structs here are the source
// the OpenAPI document is generated from (Go types → contract, spec §4).
package api

import (
	"time"

	"github.com/giiorg/caesar/backend/internal/assets"
)

type ProjectResponse struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ProjectListResponse struct {
	Projects []ProjectResponse `json:"projects"`
}

type CreateProjectRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type UpdateProjectRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

type RepoResponse struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	Forge         string    `json:"forge"`
	Slug          string    `json:"slug"`
	DefaultBranch string    `json:"default_branch"`
	CredentialID  string    `json:"credential_id"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type RepoListResponse struct {
	Repos []RepoResponse `json:"repos"`
}

type CreateRepoRequest struct {
	Forge         string `json:"forge"`
	Slug          string `json:"slug"`
	DefaultBranch string `json:"default_branch,omitempty"`
	CredentialID  string `json:"credential_id"`
}

type UpdateRepoRequest struct {
	Slug          *string `json:"slug,omitempty"`
	DefaultBranch *string `json:"default_branch,omitempty"`
	CredentialID  *string `json:"credential_id,omitempty"`
}

func projectResponse(p assets.Project) ProjectResponse {
	return ProjectResponse{
		ID: p.ID.String(), Name: p.Name, Description: p.Description,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func repoResponse(r assets.Repo) RepoResponse {
	return RepoResponse{
		ID: r.ID.String(), ProjectID: r.ProjectID.String(), Forge: string(r.Forge), Slug: r.Slug,
		DefaultBranch: r.DefaultBranch, CredentialID: r.CredentialID.String(),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}
```

- [ ] **Step 2: Write the API with its route table**

`backend/internal/assets/api/api.go`:

```go
package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// GrantSource lists a member's grants for project-visibility filtering.
// identity.Service satisfies it; app wiring passes it in.
type GrantSource interface {
	GrantsForUser(ctx context.Context, userID uuid.UUID) ([]authn.Grant, error)
}

type API struct {
	svc    *assets.Service
	auth   authn.SessionAuthenticator
	authz  authn.Authorizer
	grants GrantSource
}

func New(svc *assets.Service, auth authn.SessionAuthenticator, authz authn.Authorizer, grants GrantSource) *API {
	return &API{svc: svc, auth: auth, authz: authz, grants: grants}
}

// Operations is assets' route table: mux registration (httpx.Mount) and
// OpenAPI generation (internal/contracts) both consume it.
func Operations(a *API) []httpx.Operation {
	return []httpx.Operation{
		{
			Pattern: "POST /api/v1/projects", ID: "createProject", Summary: "Create a project (admin)",
			Request: CreateProjectRequest{}, Response: ProjectResponse{}, Status: http.StatusCreated,
			Handler: a.adminOnly(a.handleCreateProject),
		},
		{
			Pattern: "GET /api/v1/projects", ID: "listProjects", Summary: "List projects visible to the caller",
			Response: ProjectListResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListProjects),
		},
		{
			Pattern: "GET /api/v1/projects/{id}", ID: "getProject", Summary: "Get a project (view)",
			Response: ProjectResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleGetProject),
		},
		{
			Pattern: "PATCH /api/v1/projects/{id}", ID: "updateProject", Summary: "Update name or description (manage)",
			Request: UpdateProjectRequest{}, Response: ProjectResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleUpdateProject),
		},
		{
			Pattern: "DELETE /api/v1/projects/{id}", ID: "deleteProject", Summary: "Delete an empty project (manage)",
			Status:  http.StatusNoContent,
			Handler: a.authenticated(a.handleDeleteProject),
		},
		{
			Pattern: "GET /api/v1/projects/{id}/repos", ID: "listProjectRepos", Summary: "List a project's repos (view)",
			Response: RepoListResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListProjectRepos),
		},
		{
			Pattern: "POST /api/v1/projects/{id}/repos", ID: "createRepo", Summary: "Register a repo on a project (manage)",
			Request: CreateRepoRequest{}, Response: RepoResponse{}, Status: http.StatusCreated,
			Handler: a.authenticated(a.handleCreateRepo),
		},
		{
			Pattern: "GET /api/v1/repos/{id}", ID: "getRepo", Summary: "Get a repo (view)",
			Response: RepoResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleGetRepo),
		},
		{
			Pattern: "PATCH /api/v1/repos/{id}", ID: "updateRepo", Summary: "Update a repo (manage)",
			Request: UpdateRepoRequest{}, Response: RepoResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleUpdateRepo),
		},
		{
			Pattern: "DELETE /api/v1/repos/{id}", ID: "deleteRepo", Summary: "Delete a repo (manage)",
			Status:  http.StatusNoContent,
			Handler: a.authenticated(a.handleDeleteRepo),
		},
	}
}

type sessionHandler func(w http.ResponseWriter, r *http.Request, sess authn.AuthSession)

func (a *API) authenticated(next sessionHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(authn.SessionCookie)
		if err != nil {
			httpx.Problem(w, r, xerrors.E(xerrors.ClassUnauthenticated, "authentication required"))
			return
		}
		sess, err := a.auth.AuthenticateSession(r.Context(), c.Value)
		if err != nil {
			httpx.Problem(w, r, err)
			return
		}
		next(w, r, sess)
	})
}

func (a *API) adminOnly(next sessionHandler) http.Handler {
	return a.authenticated(func(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
		if sess.Role != authn.RoleAdmin {
			httpx.Problem(w, r, xerrors.E(xerrors.ClassForbidden, "admin role required"))
			return
		}
		next(w, r, sess)
	})
}

// authorize writes a 403 problem and returns false unless sess holds min
// on projectID (RBAC matrix in the plan header).
func (a *API) authorize(w http.ResponseWriter, r *http.Request, sess authn.AuthSession, projectID uuid.UUID, min authn.GrantLevel) bool {
	ok, err := a.authz.Authorize(r.Context(), sess.Principal, projectID, min)
	if err != nil {
		httpx.Problem(w, r, err)
		return false
	}
	if !ok {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassForbidden, "insufficient project grant"))
		return false
	}
	return true
}

// decode mirrors identity/api's strict decoder (contexts never share
// hand-rolled helpers across the boundary; ten lines beats a new seam).
func decode[T any](r *http.Request) (T, error) {
	var v T
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, xerrors.Wrap(xerrors.ClassInvalid, "invalid request body", err)
	}
	return v, nil
}

func pathUUID(r *http.Request, notFound string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.UUID{}, xerrors.E(xerrors.ClassNotFound, notFound)
	}
	return id, nil
}

func parseUUIDField(s, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.UUID{}, xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: field, Message: "must be a uuid"})
	}
	return id, nil
}

func (a *API) handleCreateProject(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	req, err := decode[CreateProjectRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	p, err := a.svc.CreateProject(r.Context(), sess.Principal, req.Name, req.Description)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, projectResponse(p))
}

func (a *API) handleListProjects(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	var (
		projects []assets.Project
		err      error
	)
	if sess.Role == authn.RoleAdmin {
		projects, err = a.svc.ListProjects(r.Context())
	} else {
		var grants []authn.Grant
		grants, err = a.grants.GrantsForUser(r.Context(), sess.UserID)
		if err == nil {
			ids := make([]uuid.UUID, 0, len(grants))
			for _, g := range grants {
				ids = append(ids, g.ProjectID)
			}
			projects, err = a.svc.ListProjectsByIDs(r.Context(), ids)
		}
	}
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := ProjectListResponse{Projects: make([]ProjectResponse, 0, len(projects))}
	for _, p := range projects {
		out.Projects = append(out.Projects, projectResponse(p))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleGetProject(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such project")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if !a.authorize(w, r, sess, id, authn.GrantView) {
		return
	}
	p, err := a.svc.GetProject(r.Context(), id)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, projectResponse(p))
}

func (a *API) handleUpdateProject(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such project")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if !a.authorize(w, r, sess, id, authn.GrantManage) {
		return
	}
	req, err := decode[UpdateProjectRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	p, err := a.svc.UpdateProject(r.Context(), sess.Principal, id, assets.ProjectUpdate{Name: req.Name, Description: req.Description})
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, projectResponse(p))
}

func (a *API) handleDeleteProject(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such project")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if !a.authorize(w, r, sess, id, authn.GrantManage) {
		return
	}
	if err := a.svc.DeleteProject(r.Context(), sess.Principal, id); err != nil {
		httpx.Problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleListProjectRepos(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such project")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if !a.authorize(w, r, sess, id, authn.GrantView) {
		return
	}
	repos, err := a.svc.ListRepos(r.Context(), id)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := RepoListResponse{Repos: make([]RepoResponse, 0, len(repos))}
	for _, repo := range repos {
		out.Repos = append(out.Repos, repoResponse(repo))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleCreateRepo(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such project")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if !a.authorize(w, r, sess, id, authn.GrantManage) {
		return
	}
	req, err := decode[CreateRepoRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	credID, err := parseUUIDField(req.CredentialID, "credential_id")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	repo, err := a.svc.CreateRepo(r.Context(), sess.Principal, id, assets.NewRepo{
		Forge: assets.ForgeKind(req.Forge), Slug: req.Slug, DefaultBranch: req.DefaultBranch, CredentialID: credID,
	})
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, repoResponse(repo))
}

// repoScoped resolves {id} to a repo and enforces min on its project.
func (a *API) repoScoped(w http.ResponseWriter, r *http.Request, sess authn.AuthSession, min authn.GrantLevel) (assets.Repo, bool) {
	id, err := pathUUID(r, "no such repo")
	if err != nil {
		httpx.Problem(w, r, err)
		return assets.Repo{}, false
	}
	repo, err := a.svc.GetRepo(r.Context(), id)
	if err != nil {
		httpx.Problem(w, r, err)
		return assets.Repo{}, false
	}
	if !a.authorize(w, r, sess, repo.ProjectID, min) {
		return assets.Repo{}, false
	}
	return repo, true
}

func (a *API) handleGetRepo(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	repo, ok := a.repoScoped(w, r, sess, authn.GrantView)
	if !ok {
		return
	}
	httpx.JSON(w, http.StatusOK, repoResponse(repo))
}

func (a *API) handleUpdateRepo(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	repo, ok := a.repoScoped(w, r, sess, authn.GrantManage)
	if !ok {
		return
	}
	req, err := decode[UpdateRepoRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	upd := assets.RepoUpdate{Slug: req.Slug, DefaultBranch: req.DefaultBranch}
	if req.CredentialID != nil {
		credID, err := parseUUIDField(*req.CredentialID, "credential_id")
		if err != nil {
			httpx.Problem(w, r, err)
			return
		}
		upd.CredentialID = &credID
	}
	updated, err := a.svc.UpdateRepo(r.Context(), sess.Principal, repo.ID, upd)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, repoResponse(updated))
}

func (a *API) handleDeleteRepo(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	repo, ok := a.repoScoped(w, r, sess, authn.GrantManage)
	if !ok {
		return
	}
	if err := a.svc.DeleteRepo(r.Context(), sess.Principal, repo.ID); err != nil {
		httpx.Problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 3: Write the failing integration tests (fakes, not identity)**

`backend/internal/assets/api/api_integration_test.go`:

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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/assets"
	assetsapi "github.com/giiorg/caesar/backend/internal/assets/api"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// fakePorts implements the authn ports and GrantSource without identity:
// assets tests may never import the identity context (arch law). Real
// wiring is exercised by the app e2e (Task 12).
type fakePorts struct {
	sessions map[string]authn.AuthSession
	grants   map[uuid.UUID][]authn.Grant
}

func (f *fakePorts) AuthenticateSession(_ context.Context, token string) (authn.AuthSession, error) {
	s, ok := f.sessions[token]
	if !ok {
		return authn.AuthSession{}, xerrors.E(xerrors.ClassUnauthenticated, "invalid or expired session")
	}
	return s, nil
}

func (f *fakePorts) Authorize(_ context.Context, p authn.Principal, projectID uuid.UUID, min authn.GrantLevel) (bool, error) {
	if p.Role == authn.RoleAdmin {
		return true, nil
	}
	for _, g := range f.grants[p.UserID] {
		if g.ProjectID == projectID && g.Level.AtLeast(min) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakePorts) GrantsForUser(_ context.Context, userID uuid.UUID) ([]authn.Grant, error) {
	return f.grants[userID], nil
}

func seedUser(t *testing.T, pool *pgxpool.Pool, email string, role authn.Role) authn.Principal {
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

func newTestServer(t *testing.T) (*httptest.Server, *assets.Service, *pgxpool.Pool, *fakePorts) {
	t.Helper()
	pool := testdb.New(t)
	fake := clock.NewFake(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	keys, err := secrets.ParseKeyset("test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	svc := assets.NewService(pool, fake, audit.NewRecorder(fake), keys)
	ports := &fakePorts{sessions: map[string]authn.AuthSession{}, grants: map[uuid.UUID][]authn.Grant{}}

	mux := http.NewServeMux()
	httpx.Mount(mux, assetsapi.Operations(assetsapi.New(svc, ports, ports, ports)))
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := httptest.NewServer(httpx.Chain(mux, httpx.WithCorrelationID, httpx.AccessLog(log), httpx.CheckOrigin("http://caesar.test")))
	t.Cleanup(srv.Close)
	return srv, svc, pool, ports
}

func login(ports *fakePorts, token string, p authn.Principal) {
	ports.sessions[token] = authn.AuthSession{Principal: p, SessionID: uuid.New()}
}

func do(t *testing.T, method, url, token string, body any) *http.Response {
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
	if token != "" {
		req.Header.Set("Cookie", authn.SessionCookie+"="+token)
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

func TestAssetsAPI_ProjectVisibilityFollowsGrants(t *testing.T) {
	srv, _, pool, ports := newTestServer(t)
	admin := seedUser(t, pool, "root@example.com", authn.RoleAdmin)
	member := seedUser(t, pool, "m@example.com", authn.RoleMember)
	login(ports, "admin-tok", admin)
	login(ports, "member-tok", member)

	// Admin creates two projects over the API; member creation is 403.
	resp := do(t, http.MethodPost, srv.URL+"/api/v1/projects", "admin-tok", assetsapi.CreateProjectRequest{Name: "alpha"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	alpha := decodeInto[assetsapi.ProjectResponse](t, resp)
	if resp := do(t, http.MethodPost, srv.URL+"/api/v1/projects", "admin-tok", assetsapi.CreateProjectRequest{Name: "beta"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create beta = %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodPost, srv.URL+"/api/v1/projects", "member-tok", assetsapi.CreateProjectRequest{Name: "gamma"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member create = %d, want 403", resp.StatusCode)
	}

	// No grant: empty list, 403 on get.
	if got := decodeInto[assetsapi.ProjectListResponse](t, do(t, http.MethodGet, srv.URL+"/api/v1/projects", "member-tok", nil)); len(got.Projects) != 0 {
		t.Fatalf("ungranted member sees %d projects", len(got.Projects))
	}
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/projects/"+alpha.ID, "member-tok", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("ungranted get = %d, want 403", resp.StatusCode)
	}

	// View grant on alpha: visible, readable, still not writable.
	alphaID := uuid.MustParse(alpha.ID)
	ports.grants[member.UserID] = []authn.Grant{{ProjectID: alphaID, Level: authn.GrantView}}
	if got := decodeInto[assetsapi.ProjectListResponse](t, do(t, http.MethodGet, srv.URL+"/api/v1/projects", "member-tok", nil)); len(got.Projects) != 1 || got.Projects[0].Name != "alpha" {
		t.Fatalf("granted member list = %+v", got.Projects)
	}
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/projects/"+alpha.ID, "member-tok", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("granted get = %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodPatch, srv.URL+"/api/v1/projects/"+alpha.ID, "member-tok", assetsapi.UpdateProjectRequest{}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("view-level patch = %d, want 403", resp.StatusCode)
	}

	// Admin lists everything; no cookie is 401.
	if got := decodeInto[assetsapi.ProjectListResponse](t, do(t, http.MethodGet, srv.URL+"/api/v1/projects", "admin-tok", nil)); len(got.Projects) != 2 {
		t.Fatalf("admin list = %d", len(got.Projects))
	}
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/projects", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d, want 401", resp.StatusCode)
	}
}

func TestAssetsAPI_RepoEndpointsEnforceManage(t *testing.T) {
	ctx := context.Background()
	srv, svc, pool, ports := newTestServer(t)
	admin := seedUser(t, pool, "root@example.com", authn.RoleAdmin)
	member := seedUser(t, pool, "m@example.com", authn.RoleMember)
	login(ports, "admin-tok", admin)
	login(ports, "member-tok", member)

	p, err := svc.CreateProject(ctx, admin, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	cred, err := svc.CreateCredential(ctx, admin, "gh", assets.CredentialForgeToken, []byte("tok"))
	if err != nil {
		t.Fatal(err)
	}

	// manage grant → member can register a repo.
	ports.grants[member.UserID] = []authn.Grant{{ProjectID: p.ID, Level: authn.GrantManage}}
	resp := do(t, http.MethodPost, srv.URL+"/api/v1/projects/"+p.ID.String()+"/repos", "member-tok", assetsapi.CreateRepoRequest{
		Forge: "github", Slug: "giiorg/caesar", CredentialID: cred.ID.String(),
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create repo = %d", resp.StatusCode)
	}
	repo := decodeInto[assetsapi.RepoResponse](t, resp)

	// Downgrade to run: reads still work, writes are 403.
	ports.grants[member.UserID] = []authn.Grant{{ProjectID: p.ID, Level: authn.GrantRun}}
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/repos/"+repo.ID, "member-tok", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("get repo = %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/projects/"+p.ID.String()+"/repos", "member-tok", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("list repos = %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodDelete, srv.URL+"/api/v1/repos/"+repo.ID, "member-tok", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("run-level delete = %d, want 403", resp.StatusCode)
	}

	// Bad path uuid → 404 problem, unknown body field → 400.
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/repos/not-a-uuid", "admin-tok", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bad uuid = %d", resp.StatusCode)
	}
	raw, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/projects", bytes.NewReader([]byte(`{"name":"x","surprise":1}`)))
	raw.Header.Set("Cookie", authn.SessionCookie+"=admin-tok")
	rawResp, err := http.DefaultClient.Do(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer rawResp.Body.Close()
	if rawResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", rawResp.StatusCode)
	}
}
```

- [ ] **Step 4: Run to see them fail, then pass**

Run: `cd backend && go vet ./internal/assets/api/` — fix until it builds, then:
Run: `cd backend && go test -tags integration ./internal/assets/api/ -v`
Expected: PASS.

- [ ] **Step 5: Teach contracts about multiple tables and overrides**

In `backend/internal/contracts/contracts.go`, replace the ops collection and `requestStructure`:

```go
// operationTables collects every context's route table with nil
// dependencies — metadata only; handlers never run during generation.
func operationTables() [][]httpx.Operation {
	return [][]httpx.Operation{
		identityapi.Operations(identityapi.New(nil, 0)),
		assetsapi.Operations(assetsapi.New(nil, nil, nil, nil)),
	}
}
```

(in `Build`, iterate `for _, ops := range operationTables() { for _, op := range ops { … } }` — body unchanged), and:

```go
type idAndUpdateUser struct {
	idParams
	identityapi.UpdateUserRequest
}

type idAndUpdateProject struct {
	idParams
	assetsapi.UpdateProjectRequest
}

type idAndCreateRepo struct {
	idParams
	assetsapi.CreateRepoRequest
}

type idAndUpdateRepo struct {
	idParams
	assetsapi.UpdateRepoRequest
}

// requestOverrides maps operation IDs whose request combines a body with
// path parameters onto their wrapper structs. Every new id-bearing body
// gets an entry; requestStructure panics loudly otherwise.
var requestOverrides = map[string]any{
	"updateUser":    idAndUpdateUser{},
	"updateProject": idAndUpdateProject{},
	"createRepo":    idAndCreateRepo{},
	"updateRepo":    idAndUpdateRepo{},
}

func requestStructure(op httpx.Operation, path string) any {
	if o, ok := requestOverrides[op.ID]; ok {
		return o
	}
	hasID := strings.Contains(path, "{id}")
	switch {
	case !hasID:
		return op.Request
	case op.Request == nil:
		return idParams{}
	default:
		panic(fmt.Sprintf("contracts: no path-param wrapper for %s — add one to requestOverrides", op.Pattern))
	}
}
```

(add the `assetsapi` import: `assetsapi "github.com/giiorg/caesar/backend/internal/assets/api"`).

- [ ] **Step 6: Regenerate contracts and verify the gate**

Run: `task contracts:gen && task contracts:check`
Expected: `backend/api/openapi.json` gains the ten assets operations, `web/src/api/schema.d.ts` regenerates, check passes with no residual drift. Also run the contracts unit tests if any exist: `cd backend && go test ./internal/contracts/`.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/assets/api backend/internal/contracts backend/api/openapi.json web/src/api
git commit -m "feat(backend): assets api for projects and repos with grant-scoped rbac"
```

---

### Task 11: Assets API — credentials and harness endpoints (admin-only)

**Files:**
- Modify: `backend/internal/assets/api/dto.go`
- Modify: `backend/internal/assets/api/api.go` (append operations + handlers)
- Create: `backend/internal/assets/api/admin_api_integration_test.go`
- Modify: `backend/internal/contracts/contracts.go` (two more overrides)
- Regenerate: `backend/api/openapi.json`, `web/src/api/schema.d.ts`

**Interfaces:**
- Consumes: Tasks 7/9 service methods, Task 10 scaffolding.
- Produces: DTOs `CredentialResponse`, `CredentialListResponse`, `CreateCredentialRequest`, `RotateCredentialSecretRequest`, `HarnessResponse`, `HarnessListResponse`, `RegisterHarnessRequest`, `UpdateHarnessRequest`; operations `createCredential`, `listCredentials`, `getCredential`, `rotateCredentialSecret`, `deleteCredential`, `registerHarness`, `listHarnesses`, `getHarness`, `updateHarness`, `deleteHarness`.

- [ ] **Step 1: Add the DTOs**

Append to `backend/internal/assets/api/dto.go`:

```go
type CredentialResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	KeyID     string    `json:"key_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type CredentialListResponse struct {
	Credentials []CredentialResponse `json:"credentials"`
}

type CreateCredentialRequest struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Secret string `json:"secret"`
}

type RotateCredentialSecretRequest struct {
	Secret string `json:"secret"`
}

type HarnessResponse struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Kind             string         `json:"kind"`
	ImageRef         string         `json:"image_ref"`
	ProviderEndpoint string         `json:"provider_endpoint"`
	CredentialID     string         `json:"credential_id"`
	Defaults         map[string]any `json:"defaults"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

type HarnessListResponse struct {
	Harnesses []HarnessResponse `json:"harnesses"`
}

type RegisterHarnessRequest struct {
	Name             string         `json:"name"`
	Kind             string         `json:"kind"`
	ImageRef         string         `json:"image_ref"`
	ProviderEndpoint string         `json:"provider_endpoint"`
	CredentialID     string         `json:"credential_id"`
	Defaults         map[string]any `json:"defaults,omitempty"`
}

type UpdateHarnessRequest struct {
	Name             *string         `json:"name,omitempty"`
	ImageRef         *string         `json:"image_ref,omitempty"`
	ProviderEndpoint *string         `json:"provider_endpoint,omitempty"`
	CredentialID     *string         `json:"credential_id,omitempty"`
	Defaults         *map[string]any `json:"defaults,omitempty"`
}

func credentialResponse(c assets.Credential) CredentialResponse {
	return CredentialResponse{
		ID: c.ID.String(), Name: c.Name, Kind: string(c.Kind), KeyID: c.KeyID,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

func harnessResponse(h assets.Harness) HarnessResponse {
	return HarnessResponse{
		ID: h.ID.String(), Name: h.Name, Kind: string(h.Kind), ImageRef: h.ImageRef,
		ProviderEndpoint: h.ProviderEndpoint, CredentialID: h.CredentialID.String(), Defaults: h.Defaults,
		CreatedAt: h.CreatedAt, UpdatedAt: h.UpdatedAt,
	}
}
```

- [ ] **Step 2: Append operations and handlers**

Append to `Operations` in `backend/internal/assets/api/api.go`:

```go
		{
			Pattern: "POST /api/v1/credentials", ID: "createCredential", Summary: "Store an encrypted credential (admin)",
			Request: CreateCredentialRequest{}, Response: CredentialResponse{}, Status: http.StatusCreated,
			Handler: a.adminOnly(a.handleCreateCredential),
		},
		{
			Pattern: "GET /api/v1/credentials", ID: "listCredentials", Summary: "List credential metadata (admin)",
			Response: CredentialListResponse{}, Status: http.StatusOK,
			Handler: a.adminOnly(a.handleListCredentials),
		},
		{
			Pattern: "GET /api/v1/credentials/{id}", ID: "getCredential", Summary: "Get credential metadata (admin)",
			Response: CredentialResponse{}, Status: http.StatusOK,
			Handler: a.adminOnly(a.handleGetCredential),
		},
		{
			Pattern: "PUT /api/v1/credentials/{id}/secret", ID: "rotateCredentialSecret", Summary: "Replace the secret value (admin)",
			Request: RotateCredentialSecretRequest{}, Response: CredentialResponse{}, Status: http.StatusOK,
			Handler: a.adminOnly(a.handleRotateCredentialSecret),
		},
		{
			Pattern: "DELETE /api/v1/credentials/{id}", ID: "deleteCredential", Summary: "Delete an unreferenced credential (admin)",
			Status:  http.StatusNoContent,
			Handler: a.adminOnly(a.handleDeleteCredential),
		},
		{
			Pattern: "POST /api/v1/harnesses", ID: "registerHarness", Summary: "Register a harness (admin)",
			Request: RegisterHarnessRequest{}, Response: HarnessResponse{}, Status: http.StatusCreated,
			Handler: a.adminOnly(a.handleRegisterHarness),
		},
		{
			Pattern: "GET /api/v1/harnesses", ID: "listHarnesses", Summary: "List harnesses (admin)",
			Response: HarnessListResponse{}, Status: http.StatusOK,
			Handler: a.adminOnly(a.handleListHarnesses),
		},
		{
			Pattern: "GET /api/v1/harnesses/{id}", ID: "getHarness", Summary: "Get a harness (admin)",
			Response: HarnessResponse{}, Status: http.StatusOK,
			Handler: a.adminOnly(a.handleGetHarness),
		},
		{
			Pattern: "PATCH /api/v1/harnesses/{id}", ID: "updateHarness", Summary: "Update a harness (admin)",
			Request: UpdateHarnessRequest{}, Response: HarnessResponse{}, Status: http.StatusOK,
			Handler: a.adminOnly(a.handleUpdateHarness),
		},
		{
			Pattern: "DELETE /api/v1/harnesses/{id}", ID: "deleteHarness", Summary: "Delete a harness (admin)",
			Status:  http.StatusNoContent,
			Handler: a.adminOnly(a.handleDeleteHarness),
		},
```

and the handlers:

```go
func (a *API) handleCreateCredential(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	req, err := decode[CreateCredentialRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	c, err := a.svc.CreateCredential(r.Context(), sess.Principal, req.Name, assets.CredentialKind(req.Kind), []byte(req.Secret))
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, credentialResponse(c))
}

func (a *API) handleListCredentials(w http.ResponseWriter, r *http.Request, _ authn.AuthSession) {
	creds, err := a.svc.ListCredentials(r.Context())
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := CredentialListResponse{Credentials: make([]CredentialResponse, 0, len(creds))}
	for _, c := range creds {
		out.Credentials = append(out.Credentials, credentialResponse(c))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleGetCredential(w http.ResponseWriter, r *http.Request, _ authn.AuthSession) {
	id, err := pathUUID(r, "no such credential")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	c, err := a.svc.GetCredential(r.Context(), id)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, credentialResponse(c))
}

func (a *API) handleRotateCredentialSecret(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such credential")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	req, err := decode[RotateCredentialSecretRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	c, err := a.svc.RotateCredentialSecret(r.Context(), sess.Principal, id, []byte(req.Secret))
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, credentialResponse(c))
}

func (a *API) handleDeleteCredential(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such credential")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if err := a.svc.DeleteCredential(r.Context(), sess.Principal, id); err != nil {
		httpx.Problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleRegisterHarness(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	req, err := decode[RegisterHarnessRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	credID, err := parseUUIDField(req.CredentialID, "credential_id")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	h, err := a.svc.RegisterHarness(r.Context(), sess.Principal, assets.NewHarness{
		Name: req.Name, Kind: assets.HarnessKind(req.Kind), ImageRef: req.ImageRef,
		ProviderEndpoint: req.ProviderEndpoint, CredentialID: credID, Defaults: req.Defaults,
	})
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, harnessResponse(h))
}

func (a *API) handleListHarnesses(w http.ResponseWriter, r *http.Request, _ authn.AuthSession) {
	all, err := a.svc.ListHarnesses(r.Context())
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := HarnessListResponse{Harnesses: make([]HarnessResponse, 0, len(all))}
	for _, h := range all {
		out.Harnesses = append(out.Harnesses, harnessResponse(h))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleGetHarness(w http.ResponseWriter, r *http.Request, _ authn.AuthSession) {
	id, err := pathUUID(r, "no such harness")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	h, err := a.svc.GetHarness(r.Context(), id)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, harnessResponse(h))
}

func (a *API) handleUpdateHarness(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such harness")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	req, err := decode[UpdateHarnessRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	upd := assets.HarnessUpdate{
		Name: req.Name, ImageRef: req.ImageRef, ProviderEndpoint: req.ProviderEndpoint, Defaults: req.Defaults,
	}
	if req.CredentialID != nil {
		credID, err := parseUUIDField(*req.CredentialID, "credential_id")
		if err != nil {
			httpx.Problem(w, r, err)
			return
		}
		upd.CredentialID = &credID
	}
	h, err := a.svc.UpdateHarness(r.Context(), sess.Principal, id, upd)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, harnessResponse(h))
}

func (a *API) handleDeleteHarness(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	id, err := pathUUID(r, "no such harness")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if err := a.svc.DeleteHarness(r.Context(), sess.Principal, id); err != nil {
		httpx.Problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 3: Write the failing integration tests**

`backend/internal/assets/api/admin_api_integration_test.go` (reuses Task 10's helpers — same package):

```go
//go:build integration

package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	assetsapi "github.com/giiorg/caesar/backend/internal/assets/api"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
)

func TestAssetsAPI_CredentialsAdminOnlyAndSecretNeverEchoed(t *testing.T) {
	srv, _, pool, ports := newTestServer(t)
	admin := seedUser(t, pool, "root@example.com", authn.RoleAdmin)
	member := seedUser(t, pool, "m@example.com", authn.RoleMember)
	login(ports, "admin-tok", admin)
	login(ports, "member-tok", member)
	const secret = "ghp_supersecrettokenvalue"

	// Member: 403 on every credentials operation.
	if resp := do(t, http.MethodPost, srv.URL+"/api/v1/credentials", "member-tok", assetsapi.CreateCredentialRequest{
		Name: "x", Kind: "forge_token", Secret: secret,
	}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member create credential = %d, want 403", resp.StatusCode)
	}
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/credentials", "member-tok", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member list credentials = %d, want 403", resp.StatusCode)
	}

	// Admin roundtrip; the secret never appears in any response body.
	resp := do(t, http.MethodPost, srv.URL+"/api/v1/credentials", "admin-tok", assetsapi.CreateCredentialRequest{
		Name: "github-bot", Kind: "forge_token", Secret: secret,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), secret) {
		t.Fatal("create response echoes the secret")
	}
	var created assetsapi.CredentialResponse
	if err := jsonUnmarshal(body, &created); err != nil {
		t.Fatal(err)
	}

	resp = do(t, http.MethodPut, srv.URL+"/api/v1/credentials/"+created.ID+"/secret", "admin-tok",
		assetsapi.RotateCredentialSecretRequest{Secret: "rotated-secret-value"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate = %d", resp.StatusCode)
	}
	body, _ = io.ReadAll(resp.Body)
	if strings.Contains(string(body), "rotated-secret-value") {
		t.Fatal("rotate response echoes the secret")
	}

	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/credentials/"+created.ID, "admin-tok", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("get = %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodDelete, srv.URL+"/api/v1/credentials/"+created.ID, "admin-tok", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
}

func TestAssetsAPI_HarnessRegistryAdminOnly(t *testing.T) {
	srv, svc, pool, ports := newTestServer(t)
	admin := seedUser(t, pool, "root@example.com", authn.RoleAdmin)
	member := seedUser(t, pool, "m@example.com", authn.RoleMember)
	login(ports, "admin-tok", admin)
	login(ports, "member-tok", member)

	cred, err := svc.CreateCredential(t.Context(), admin, "ant", "model_key", []byte("sk"))
	if err != nil {
		t.Fatal(err)
	}

	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/harnesses", "member-tok", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member list harnesses = %d, want 403", resp.StatusCode)
	}

	resp := do(t, http.MethodPost, srv.URL+"/api/v1/harnesses", "admin-tok", assetsapi.RegisterHarnessRequest{
		Name: "claude-main", Kind: "claude-code",
		ImageRef: "ghcr.io/giiorg/caesar-harness-claude@sha256:abc", ProviderEndpoint: "https://api.anthropic.com",
		CredentialID: cred.ID.String(), Defaults: map[string]any{"model": "claude-sonnet-5"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d", resp.StatusCode)
	}
	h := decodeInto[assetsapi.HarnessResponse](t, resp)
	if h.Defaults["model"] != "claude-sonnet-5" {
		t.Fatalf("defaults = %+v", h.Defaults)
	}

	name := "claude-renamed"
	if resp := do(t, http.MethodPatch, srv.URL+"/api/v1/harnesses/"+h.ID, "admin-tok", assetsapi.UpdateHarnessRequest{Name: &name}); resp.StatusCode != http.StatusOK {
		t.Fatalf("update = %d", resp.StatusCode)
	}
	if resp := do(t, http.MethodDelete, srv.URL+"/api/v1/harnesses/"+h.ID, "admin-tok", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
}
```

(Add a tiny `jsonUnmarshal = json.Unmarshal` alias or inline `json.Unmarshal` with the import — keep whichever reads cleaner. If the Go version in use predates `t.Context()`, use `context.Background()`.)

- [ ] **Step 4: Run to see them pass**

Run: `cd backend && go test -tags integration ./internal/assets/api/ -v`
Expected: all Task 10 + Task 11 tests PASS.

- [ ] **Step 5: Extend contracts overrides and regenerate**

In `backend/internal/contracts/contracts.go` add:

```go
type idAndRotateSecret struct {
	idParams
	assetsapi.RotateCredentialSecretRequest
}

type idAndUpdateHarness struct {
	idParams
	assetsapi.UpdateHarnessRequest
}
```

and the entries `"rotateCredentialSecret": idAndRotateSecret{}`, `"updateHarness": idAndUpdateHarness{}` in `requestOverrides`.

Run: `task contracts:gen && task contracts:check`
Expected: ten more operations in the OpenAPI document; gate clean.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/assets/api backend/internal/contracts backend/api/openapi.json web/src/api
git commit -m "feat(backend): credentials and harness registry api"
```

---
### Task 12: Master-key config and app wiring

`security.master_key` joins the config (env `CAESAR_MASTER_KEY` overrides — the spec §8 bootstrap-secret pair with `DATABASE_URL`), `serve` refuses to boot without it, and the assets service + API mount into the real `Routes`. The deadconfig analyzer is satisfied by the serve read site in this same task.

**Files:**
- Modify: `backend/internal/platform/config/config.go`
- Modify: `backend/internal/platform/config/config_test.go` (add cases in the file's existing style)
- Modify: `backend/internal/app/serve.go`
- Modify: `backend/caesar.dev.json`
- Modify: `backend/internal/app/serve_integration_test.go` and `backend/internal/app/api_e2e_integration_test.go`

**Interfaces:**
- Consumes: `secrets.ParseKeyset` (Task 4), `assets.NewService` (Task 6), `assetsapi.New`/`Operations` (Task 10) — note the wiring: `identity.Service` is passed as **both** `authn.SessionAuthenticator`/`authn.Authorizer` and `assetsapi.GrantSource`.
- Produces: `cfg.Security.MasterKey string`; `serve` boots with assets endpoints live.

- [ ] **Step 1: Add failing config tests**

Extend `backend/internal/platform/config/config_test.go` (internal `config` test package; `writeFile(t, content)` is its existing temp-file helper) with three cases:

```go
func TestLoad_MasterKeyFromFileAndEnvOverride(t *testing.T) {
	path := writeFile(t, `{
	  "database": {"url": "postgres://x"},
	  "security": {"master_key": "file:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.MasterKey != "file:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" {
		t.Fatalf("master key = %q", cfg.Security.MasterKey)
	}

	t.Setenv("CAESAR_MASTER_KEY", "env:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.MasterKey != "env:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" {
		t.Fatalf("env must override file, got %q", cfg.Security.MasterKey)
	}
}

func TestLoad_RejectsMalformedMasterKey(t *testing.T) {
	path := writeFile(t, `{
	  "database": {"url": "postgres://x"},
	  "security": {"master_key": "not-a-keyset"}
	}`)
	if _, err := Load(path); err == nil {
		t.Fatal("malformed keyset must fail validation")
	}
}

func TestLoad_MasterKeyIsOptionalAtLoadTime(t *testing.T) {
	// migrate/bootstrap run without it; serve enforces presence itself.
	path := writeFile(t, `{"database": {"url": "postgres://x"}}`)
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}
```

Run: `cd backend && go test ./internal/platform/config/` — FAIL (unknown field `security`).

- [ ] **Step 2: Implement the config field**

In `backend/internal/platform/config/config.go`: add to `Config` — `Security Security \`json:"security"\`` — and:

```go
// Security carries the credential master keyset (spec §8 bootstrap
// secret): "id:base64key[,id:base64key...]", first entry active. The
// CAESAR_MASTER_KEY env var overrides; serve and admin rekey require it,
// migrate and bootstrap do not.
type Security struct {
	MasterKey string `json:"master_key"`
}
```

In `Load`, after the `DATABASE_URL` override:

```go
	if key := os.Getenv("CAESAR_MASTER_KEY"); key != "" {
		cfg.Security.MasterKey = key
	}
```

In `validate` (import `platform/secrets`):

```go
	if c.Security.MasterKey != "" {
		if _, err := secrets.ParseKeyset(c.Security.MasterKey); err != nil {
			problems = append(problems, fmt.Sprintf("security.master_key: %v", err))
		}
	}
```

Run: `cd backend && go test ./internal/platform/config/` — PASS.

- [ ] **Step 3: Wire assets into serve**

In `backend/internal/app/serve.go` (`runServe`), after the identity wiring:

```go
	if cfg.Security.MasterKey == "" {
		return errors.New("serve: security.master_key (or CAESAR_MASTER_KEY) is required")
	}
	keys, err := secrets.ParseKeyset(cfg.Security.MasterKey)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	assetsSvc := assets.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), keys)
	// identity.Service serves as authenticator, authorizer, and grant
	// source for the assets API (the authn ports, wired here only).
	ops = append(ops, assetsapi.Operations(assetsapi.New(assetsSvc, svc, svc, svc))...)
```

(imports: `assets`, `assetsapi "…/internal/assets/api"`, `secrets`.) Add the dev keyset to `backend/caesar.dev.json`:

```json
{
  "server": {"addr": ":8080"},
  "database": {"url": "postgres://caesar:caesar@localhost:5432/caesar_dev?sslmode=disable"},
  "log": {"level": "debug"},
  "security": {"master_key": "dev:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
}
```

(The dev key is deliberately zero-entropy base64 — worthless and unflaggable. If gitleaks ever flags this file, allowlist the path in the gitleaks config; never weaken the rule set.)

- [ ] **Step 4: Extend serve + e2e integration tests**

- `serve_integration_test.go`: add `"security": {"master_key": "dev:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}` to its config fixture (serve now refuses to boot without it). Add one assertion: booting with the key absent returns an error mentioning `security.master_key`.
- `api_e2e_integration_test.go`: build `ops` the way serve now does (identity ops + assets ops with the real `identity.Service` as all three ports) and extend the flow: after login, `POST /api/v1/projects` as the bootstrapped admin → 201; `GET /api/v1/projects` → the created project. This is the only place real identity↔assets wiring is exercised (assets' own tests use fakes).

Run: `cd backend && go test -tags integration ./internal/app/ -v`
Expected: PASS.

- [ ] **Step 5: Full gate, then commit**

Run: `task ci` (or the composite subset available locally — see execution notes for the worktree gitleaks caveat)
Expected: green, including deadconfig (the new field has its serve read site) and arch tests.

```bash
git add backend/internal/platform/config backend/internal/app backend/caesar.dev.json
git commit -m "feat(backend): master-key config and assets api wiring"
```

---

### Task 13: Grant administration endpoints (identity)

The Plan 02 deferral: grant writes land with the projects API. They stay in identity (its tables); project existence rides the `ProjectChecker` port implemented by `assets.Service.ProjectExists` and wired in app — the FK stays as backstop.

**Files:**
- Modify: `backend/internal/identity/store/queries.sql` (grant write queries; regen)
- Create: `backend/internal/identity/grants.go`
- Create: `backend/internal/identity/grants_integration_test.go`
- Modify: `backend/internal/identity/api/api.go` (ProjectChecker, New signature, operations, handlers)
- Modify: `backend/internal/identity/api/dto.go`
- Modify: `backend/internal/identity/api/api_integration_test.go` (New call sites + grant flow test)
- Modify: `backend/internal/contracts/contracts.go` (New call site + overrides)
- Modify: `backend/internal/app/serve.go` (pass `assetsSvc` as ProjectChecker)
- Regenerate: `backend/api/openapi.json`, `web/src/api/schema.d.ts`

**Interfaces:**
- Consumes: `assets.Service.ProjectExists` (Task 6) — via the port only, wired in app.
- Produces: `identity.ProjectGrant{UserID uuid.UUID; Email string; Level GrantLevel}` · service methods `SetGrant(ctx, actor Principal, projectID, userID uuid.UUID, level GrantLevel) error`, `DeleteGrant(ctx, actor Principal, projectID, userID uuid.UUID) error`, `ListGrants(ctx, projectID uuid.UUID) ([]ProjectGrant, error)` · `identityapi.ProjectChecker{ProjectExists(ctx, id uuid.UUID) (bool, error)}` · **changed signature** `identityapi.New(svc *identity.Service, sessionTTL time.Duration, projects ProjectChecker) *API` (update all call sites: serve.go, contracts, identity api tests) · operations `listGrants` (GET `/api/v1/projects/{id}/grants`, 200), `setGrant` (PUT `/api/v1/projects/{id}/grants`, 204), `deleteGrant` (DELETE `/api/v1/projects/{id}/grants/{user_id}`, 204) · DTOs `ProjectGrantResponse{UserID, Email, Level string}`, `GrantListResponse{Grants []ProjectGrantResponse}`, `SetGrantRequest{UserID string \`json:"user_id"\`; Level string \`json:"level"\`}`.

- [ ] **Step 1: Add the queries and regen**

Append to `backend/internal/identity/store/queries.sql`, then `task db:sqlc`:

```sql
-- name: UpsertGrant :exec
INSERT INTO grants (user_id, project_id, level)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, project_id) DO UPDATE SET level = excluded.level;

-- name: DeleteGrant :execrows
DELETE FROM grants WHERE user_id = $1 AND project_id = $2;

-- name: ListProjectGrants :many
SELECT g.user_id, u.email, g.level
FROM grants g JOIN users u ON u.id = g.user_id
WHERE g.project_id = $1
ORDER BY u.email;
```

- [ ] **Step 2: Write the failing service tests**

`backend/internal/identity/grants_integration_test.go`:

```go
//go:build integration

package identity_test

import (
	"context"
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

// seedProject inserts a project row directly — identity never imports the
// assets context, and grants only need the FK target to exist.
func seedProject(t *testing.T, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		"INSERT INTO projects (name) VALUES ($1) RETURNING id", name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestGrants_SetListDeleteWithGuards(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	fake := clock.NewFake(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	svc := identity.NewService(pool, fake, audit.NewRecorder(fake), identity.Params{
		SessionIdleTTL: time.Hour, SessionAbsoluteTTL: 2 * time.Hour,
	})
	admin, err := svc.Bootstrap(ctx, "root@example.com", "a long enough password")
	if err != nil {
		t.Fatal(err)
	}
	member, err := svc.CreateUser(ctx, identity.Principal{UserID: admin.ID, Role: identity.RoleAdmin}, "m@example.com", "a long enough password", identity.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Principal{UserID: admin.ID, Email: admin.Email, Role: identity.RoleAdmin}
	project := seedProject(t, pool, "p")

	if err := svc.SetGrant(ctx, actor, project, member.ID, identity.GrantRun); err != nil {
		t.Fatal(err)
	}
	// Upsert raises in place.
	if err := svc.SetGrant(ctx, actor, project, member.ID, identity.GrantManage); err != nil {
		t.Fatal(err)
	}
	grants, err := svc.ListGrants(ctx, project)
	if err != nil || len(grants) != 1 || grants[0].Level != identity.GrantManage || grants[0].Email != "m@example.com" {
		t.Fatalf("grants = %+v, %v", grants, err)
	}
	// Authorize sees it.
	if ok, err := svc.Authorize(ctx, identity.Principal{UserID: member.ID, Role: identity.RoleMember}, project, identity.GrantManage); err != nil || !ok {
		t.Fatalf("authorize = %v, %v", ok, err)
	}

	// Guards.
	if err := svc.SetGrant(ctx, actor, project, member.ID, "owner"); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatal("bad level must be invalid")
	}
	if err := svc.SetGrant(ctx, actor, project, admin.ID, identity.GrantView); xerrors.ClassOf(err) != xerrors.ClassForbidden {
		t.Fatal("changing own grant must be forbidden")
	}
	if err := svc.SetGrant(ctx, actor, project, uuid.New(), identity.GrantView); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("unknown user must be not found")
	}
	if err := svc.SetGrant(ctx, actor, uuid.New(), member.ID, identity.GrantView); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("unknown project must be not found (fk backstop)")
	}

	if err := svc.DeleteGrant(ctx, actor, project, member.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteGrant(ctx, actor, project, member.ID); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatal("second delete must be not found")
	}

	for _, action := range []string{"grant.set", "grant.deleted"} {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_entries WHERE action = $1", action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if (action == "grant.set" && n != 2) || (action == "grant.deleted" && n != 1) {
			t.Errorf("%s audits = %d", action, n)
		}
	}
}
```

- [ ] **Step 3: Implement the service methods**

`backend/internal/identity/grants.go`:

```go
package identity

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"errors"

	"github.com/giiorg/caesar/backend/internal/identity/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// ProjectGrant is one grant row on a project, joined with the user email
// for the admin surface.
type ProjectGrant struct {
	UserID uuid.UUID
	Email  string
	Level  GrantLevel
}

func grantFKTarget(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return pgErr.ConstraintName, true
	}
	return "", false
}

// SetGrant upserts (user, project) → level. Actors never modify their own
// row (mirrors UpdateUser's self-guard: no self-escalation, no
// accidental self-lockout). Project/user existence errors surface as
// not-found via the FK constraint names.
func (s *Service) SetGrant(ctx context.Context, actor Principal, projectID, userID uuid.UUID, level GrantLevel) error {
	if !level.Valid() {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "level", Message: "must be view, run, approve, or manage"})
	}
	if actor.UserID == userID {
		return xerrors.E(xerrors.ClassForbidden, "cannot change own grant")
	}
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := store.New(tx).UpsertGrant(ctx, store.UpsertGrantParams{
			UserID: userID, ProjectID: projectID, Level: string(level),
		})
		if constraint, fk := grantFKTarget(err); fk {
			if constraint == "grants_user_id_fkey" {
				return xerrors.E(xerrors.ClassNotFound, "no such user")
			}
			return xerrors.E(xerrors.ClassNotFound, "no such project")
		}
		if err != nil {
			return fmt.Errorf("identity: set grant: %w", err)
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor:  uuid.NullUUID{UUID: actor.UserID, Valid: true},
			Action: "grant.set", SubjectKind: "grant", SubjectID: projectID.String() + ":" + userID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"level": string(level)},
		})
	})
}

func (s *Service) DeleteGrant(ctx context.Context, actor Principal, projectID, userID uuid.UUID) error {
	if actor.UserID == userID {
		return xerrors.E(xerrors.ClassForbidden, "cannot change own grant")
	}
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).DeleteGrant(ctx, store.DeleteGrantParams{UserID: userID, ProjectID: projectID})
		if err != nil {
			return fmt.Errorf("identity: delete grant: %w", err)
		}
		if n == 0 {
			return xerrors.E(xerrors.ClassNotFound, "no such grant")
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor:  uuid.NullUUID{UUID: actor.UserID, Valid: true},
			Action: "grant.deleted", SubjectKind: "grant", SubjectID: projectID.String() + ":" + userID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
		})
	})
}

func (s *Service) ListGrants(ctx context.Context, projectID uuid.UUID) ([]ProjectGrant, error) {
	rows, err := store.New(s.pool).ListProjectGrants(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("identity: list grants: %w", err)
	}
	out := make([]ProjectGrant, 0, len(rows))
	for _, r := range rows {
		out = append(out, ProjectGrant{UserID: r.UserID, Email: r.Email, Level: GrantLevel(r.Level)})
	}
	return out, nil
}
```

Run: `cd backend && go test -tags integration ./internal/identity/ -run TestGrants -v` — PASS.

- [ ] **Step 4: API endpoints, ProjectChecker, and the changed constructor**

In `backend/internal/identity/api/api.go`:

```go
// ProjectChecker reports whether a project exists — implemented by the
// assets context's service, wired in app. Grant endpoints 404 on unknown
// projects without identity touching assets' tables.
type ProjectChecker interface {
	ProjectExists(ctx context.Context, id uuid.UUID) (bool, error)
}

type API struct {
	svc        *identity.Service
	sessionTTL time.Duration
	projects   ProjectChecker
}

func New(svc *identity.Service, sessionTTL time.Duration, projects ProjectChecker) *API {
	return &API{svc: svc, sessionTTL: sessionTTL, projects: projects}
}
```

DTOs in `dto.go`:

```go
type ProjectGrantResponse struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Level  string `json:"level"`
}

type GrantListResponse struct {
	Grants []ProjectGrantResponse `json:"grants"`
}

type SetGrantRequest struct {
	UserID string `json:"user_id"`
	Level  string `json:"level"`
}
```

Operations (append to the table):

```go
		{
			Pattern: "GET /api/v1/projects/{id}/grants", ID: "listGrants", Summary: "List a project's grants (manage)",
			Response: GrantListResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListGrants),
		},
		{
			Pattern: "PUT /api/v1/projects/{id}/grants", ID: "setGrant", Summary: "Set a user's grant on a project (manage)",
			Request: SetGrantRequest{}, Status: http.StatusNoContent,
			Handler: a.authenticated(a.handleSetGrant),
		},
		{
			Pattern: "DELETE /api/v1/projects/{id}/grants/{user_id}", ID: "deleteGrant", Summary: "Remove a user's grant (manage)",
			Status:  http.StatusNoContent,
			Handler: a.authenticated(a.handleDeleteGrant),
		},
```

Handlers:

```go
// grantScoped resolves {id}, 404s unknown projects via the ProjectChecker
// port, and enforces manage (global admins pass inside Authorize).
func (a *API) grantScoped(w http.ResponseWriter, r *http.Request, sess identity.AuthSession) (uuid.UUID, bool) {
	projectID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassNotFound, "no such project"))
		return uuid.UUID{}, false
	}
	exists, err := a.projects.ProjectExists(r.Context(), projectID)
	if err != nil {
		httpx.Problem(w, r, err)
		return uuid.UUID{}, false
	}
	if !exists {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassNotFound, "no such project"))
		return uuid.UUID{}, false
	}
	ok, err := a.svc.Authorize(r.Context(), sess.Principal, projectID, identity.GrantManage)
	if err != nil {
		httpx.Problem(w, r, err)
		return uuid.UUID{}, false
	}
	if !ok {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassForbidden, "insufficient project grant"))
		return uuid.UUID{}, false
	}
	return projectID, true
}

func (a *API) handleListGrants(w http.ResponseWriter, r *http.Request, sess identity.AuthSession) {
	projectID, ok := a.grantScoped(w, r, sess)
	if !ok {
		return
	}
	grants, err := a.svc.ListGrants(r.Context(), projectID)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := GrantListResponse{Grants: make([]ProjectGrantResponse, 0, len(grants))}
	for _, g := range grants {
		out.Grants = append(out.Grants, ProjectGrantResponse{UserID: g.UserID.String(), Email: g.Email, Level: string(g.Level)})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleSetGrant(w http.ResponseWriter, r *http.Request, sess identity.AuthSession) {
	projectID, ok := a.grantScoped(w, r, sess)
	if !ok {
		return
	}
	req, err := decode[SetGrantRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "user_id", Message: "must be a uuid"}))
		return
	}
	if err := a.svc.SetGrant(r.Context(), sess.Principal, projectID, userID, identity.GrantLevel(req.Level)); err != nil {
		httpx.Problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleDeleteGrant(w http.ResponseWriter, r *http.Request, sess identity.AuthSession) {
	projectID, ok := a.grantScoped(w, r, sess)
	if !ok {
		return
	}
	userID, err := uuid.Parse(r.PathValue("user_id"))
	if err != nil {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassNotFound, "no such grant"))
		return
	}
	if err := a.svc.DeleteGrant(r.Context(), sess.Principal, projectID, userID); err != nil {
		httpx.Problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

Update every `identityapi.New` call site:
- `serve.go`: `identityapi.New(svc, cfg.Auth.SessionAbsoluteTTL.Std(), assetsSvc)`
- `contracts.go`: `identityapi.New(nil, 0, nil)`
- identity api tests: add a test-local checker that queries the DB (identity tests may not import assets):

```go
type dbProjects struct{ pool *pgxpool.Pool }

func (d dbProjects) ProjectExists(ctx context.Context, id uuid.UUID) (bool, error) {
	var ok bool
	err := d.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1)", id).Scan(&ok)
	return ok, err
}
```

and in `newTestServer`: `identityapi.New(svc, 90*24*time.Hour, dbProjects{pool})` (have the helper also return the pool).

- [ ] **Step 5: API-level test**

Append to `backend/internal/identity/api/api_integration_test.go`:

```go
func TestAPI_GrantAdministration(t *testing.T) {
	srv, svc, pool := newTestServer(t) // adjust to the helper's new return
	admin := bootstrapAndLogin(t, srv, svc)

	resp := do(t, http.MethodPost, srv.URL+"/api/v1/users", admin, identityapi.CreateUserRequest{
		Email: "m@example.com", Password: testPassword, Role: "member",
	})
	member := decodeInto[identityapi.UserResponse](t, resp)
	var projectID uuid.UUID
	if err := pool.QueryRow(context.Background(), "INSERT INTO projects (name) VALUES ('p') RETURNING id").Scan(&projectID); err != nil {
		t.Fatal(err)
	}

	// Unknown project 404s before anything else.
	if resp := do(t, http.MethodPut, srv.URL+"/api/v1/projects/"+uuid.NewString()+"/grants", admin,
		identityapi.SetGrantRequest{UserID: member.ID, Level: "view"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown project = %d, want 404", resp.StatusCode)
	}

	// Admin grants approve; /auth/me reflects it.
	if resp := do(t, http.MethodPut, srv.URL+"/api/v1/projects/"+projectID.String()+"/grants", admin,
		identityapi.SetGrantRequest{UserID: member.ID, Level: "approve"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set grant = %d", resp.StatusCode)
	}
	resp = do(t, http.MethodPost, srv.URL+"/api/v1/auth/login", "", identityapi.LoginRequest{Email: "m@example.com", Password: testPassword})
	memberTok := sessionCookie(t, resp)
	me := decodeInto[identityapi.MeResponse](t, do(t, http.MethodGet, srv.URL+"/api/v1/auth/me", memberTok, nil))
	if len(me.Grants) != 1 || me.Grants[0].Level != "approve" {
		t.Fatalf("me grants = %+v", me.Grants)
	}

	// approve < manage: the member cannot administer grants.
	if resp := do(t, http.MethodGet, srv.URL+"/api/v1/projects/"+projectID.String()+"/grants", memberTok, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("approve-level list grants = %d, want 403", resp.StatusCode)
	}

	// List then delete as admin.
	got := decodeInto[identityapi.GrantListResponse](t, do(t, http.MethodGet, srv.URL+"/api/v1/projects/"+projectID.String()+"/grants", admin, nil))
	if len(got.Grants) != 1 || got.Grants[0].Email != "m@example.com" {
		t.Fatalf("grants = %+v", got.Grants)
	}
	if resp := do(t, http.MethodDelete, srv.URL+"/api/v1/projects/"+projectID.String()+"/grants/"+member.ID, admin, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete grant = %d", resp.StatusCode)
	}
}
```

- [ ] **Step 6: Contracts wrappers and regen**

In `contracts.go` add the wrapper for the two-path-parameter delete and the set body:

```go
type idAndSetGrant struct {
	idParams
	identityapi.SetGrantRequest
}

// grantMemberParams covers DELETE …/{id}/grants/{user_id} — two path
// parameters, no body.
type grantMemberParams struct {
	idParams
	UserID string `path:"user_id" description:"user id (uuid)"`
}
```

with overrides `"setGrant": idAndSetGrant{}`, `"deleteGrant": grantMemberParams{}` (`listGrants` falls through to `idParams` automatically).

Run: `task contracts:gen && task contracts:check && cd backend && go test -tags integration ./internal/identity/... ./internal/app/`
Expected: green.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/identity backend/internal/contracts backend/internal/app/serve.go backend/api/openapi.json web/src/api
git commit -m "feat(backend): grant administration endpoints"
```

---

### Task 14: `caesar admin rekey`

Completes the rotation story `key_id` exists for: point the keyset's first entry at a new key (old key decrypt-only behind it), run rekey, drop the old entry.

**Files:**
- Create: `backend/internal/assets/rekey.go`
- Create: `backend/internal/assets/rekey_integration_test.go`
- Modify: `backend/internal/app/admin.go` (subcommand + usage)
- Modify: `backend/internal/app/main.go` (usage line: `admin` gains `(bootstrap|rekey)`)
- Create: `backend/internal/app/rekey_integration_test.go`

**Interfaces:**
- Consumes: Task 7's credential queries (`ListCredentialRowsForUpdate`, `UpdateCredentialSecret`), Task 4's keyset.
- Produces: `(*assets.Service).RekeyCredentials(ctx context.Context) (int, error)` — re-wraps every row not already under the active key; `caesar admin rekey [-config path]`.

- [ ] **Step 1: Write the failing service test**

`backend/internal/assets/rekey_integration_test.go`:

```go
//go:build integration

package assets_test

import (
	"context"
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
)

const rotatedKeyset = "next:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzA=,test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func TestRekeyCredentials_RewrapsUnderActiveKey(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t) // active key id "test"
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)

	one, err := svc.CreateCredential(ctx, admin, "one", assets.CredentialForgeToken, []byte("secret-one"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateCredential(ctx, admin, "two", assets.CredentialModelKey, []byte("secret-two")); err != nil {
		t.Fatal(err)
	}

	// New service on the SAME database with a rotated keyset: "next"
	// active, "test" decrypt-only.
	keys, err := secrets.ParseKeyset(rotatedKeyset)
	if err != nil {
		t.Fatal(err)
	}
	fake := clock.NewFake(time.Date(2026, 8, 3, 13, 0, 0, 0, time.UTC))
	rotated := assets.NewService(pool, fake, audit.NewRecorder(fake), keys)

	n, err := rotated.RekeyCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("rekeyed = %d, want 2", n)
	}
	// Idempotent: second run touches nothing.
	if n, err := rotated.RekeyCredentials(ctx); err != nil || n != 0 {
		t.Fatalf("second rekey = %d, %v; want 0", n, err)
	}

	// Rows now carry the new key id and still decrypt.
	var keyID string
	if err := pool.QueryRow(ctx, "SELECT key_id FROM credentials WHERE id = $1", one.ID).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if keyID != "next" {
		t.Fatalf("key_id = %q, want next", keyID)
	}
	plain, err := rotated.UseCredential(ctx, one.ID, "post-rekey")
	if err != nil || string(plain) != "secret-one" {
		t.Fatalf("use after rekey = %q, %v", plain, err)
	}
	if got := auditCount(t, pool, "credential.rekeyed"); got != 2 {
		t.Fatalf("rekeyed audits = %d, want 2", got)
	}
}
```

(`zzzz…A=` is valid base64 for 32 bytes — any fixed 44-char base64 string decoding to 32 bytes works; verify with `ParseKeyset` in the test itself, which already fails loudly if not.)

- [ ] **Step 2: Implement RekeyCredentials**

`backend/internal/assets/rekey.go`:

```go
package assets

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/assets/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
)

// RekeyCredentials re-wraps every credential row not already under the
// active master key (`caesar admin rekey`). One transaction, rows locked
// FOR UPDATE: concurrent rotations serialize, and a crash mid-run leaves
// every row either old-key-readable or fully rewrapped. Returns the
// number of rows re-wrapped. Actor is NULL (system); each row audits
// credential.rekeyed.
func (s *Service) RekeyCredentials(ctx context.Context) (int, error) {
	count := 0
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		rows, err := q.ListCredentialRowsForUpdate(ctx)
		if err != nil {
			return fmt.Errorf("assets: list credentials for rekey: %w", err)
		}
		for _, row := range rows {
			if row.KeyID == s.keys.ActiveKeyID() {
				continue
			}
			aad := []byte(row.ID.String())
			plain, err := s.keys.Open(secrets.Envelope{KeyID: row.KeyID, WrappedDEK: row.WrappedDek, Ciphertext: row.Ciphertext}, aad)
			if err != nil {
				return fmt.Errorf("assets: rekey %s (%s): %w", row.Name, row.ID, err)
			}
			env, err := s.keys.Seal(plain, aad)
			if err != nil {
				return fmt.Errorf("assets: rekey seal %s: %w", row.ID, err)
			}
			if _, err := q.UpdateCredentialSecret(ctx, store.UpdateCredentialSecretParams{
				ID: row.ID, KeyID: env.KeyID, WrappedDek: env.WrappedDEK, Ciphertext: env.Ciphertext, UpdatedAt: s.clock.Now(),
			}); err != nil {
				return fmt.Errorf("assets: rekey update %s: %w", row.ID, err)
			}
			if err := s.audit.Record(ctx, tx, audit.Entry{
				Action: "credential.rekeyed", SubjectKind: "credential", SubjectID: row.ID.String(),
				CorrelationID: httpx.CorrelationID(ctx),
				Detail:        map[string]any{"from": row.KeyID, "to": env.KeyID},
			}); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
```

Run: `cd backend && go test -tags integration ./internal/assets/ -run TestRekey -v` — PASS.

- [ ] **Step 3: The admin subcommand**

In `backend/internal/app/admin.go`: change `adminUsage` to

```go
const adminUsage = `usage: caesar admin <bootstrap|rekey>

  bootstrap -email <email> [-config path]   create the first admin
      (password from stdin; refuses once any admin exists)
  rekey [-config path]                      re-wrap credentials under the
      active master key (first CAESAR_MASTER_KEY entry)
`
```

dispatch on `args[0]` (`bootstrap` keeps its current body; unknown → usage + exit 2), and add:

```go
func runAdminRekey(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("admin rekey", flag.ContinueOnError)
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
	if cfg.Security.MasterKey == "" {
		fmt.Fprintln(stderr, "rekey: security.master_key (or CAESAR_MASTER_KEY) is required")
		return 1
	}
	keys, err := secrets.ParseKeyset(cfg.Security.MasterKey)
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
	svc := assets.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), keys)
	n, err := svc.RekeyCredentials(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "rekey: %d credential(s) re-wrapped under key %q\n", n, keys.ActiveKeyID())
	return 0
}
```

Update `main.go`'s usage text (`admin     administrative commands (bootstrap, rekey)`); check `main_test.go`/`admin_integration_test.go` for usage-string assertions and adjust.

- [ ] **Step 4: CLI integration test**

`backend/internal/app/rekey_integration_test.go` (mirror `admin_integration_test.go`'s config-file pattern):

```go
//go:build integration

package app_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/app"
	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
)

func TestAdminRekey_EndToEnd(t *testing.T) {
	ctx := context.Background()
	url := testdb.NewURL(t)
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// Seed one credential under the old key.
	oldKeys, err := secrets.ParseKeyset("old:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	fake := clock.NewFake(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	svc := assets.NewService(pool, fake, audit.NewRecorder(fake), oldKeys)
	actor := authn.Principal{Role: authn.RoleAdmin}
	if err := pool.QueryRow(ctx, "INSERT INTO users (email, password_hash, role) VALUES ('root@example.com', 'x', 'admin') RETURNING id").Scan(&actor.UserID); err != nil {
		t.Fatal(err)
	}
	cred, err := svc.CreateCredential(ctx, actor, "gh", assets.CredentialForgeToken, []byte("tok"))
	if err != nil {
		t.Fatal(err)
	}

	// Rotated keyset via config file: "new" active, "old" decrypt-only.
	cfgPath := filepath.Join(t.TempDir(), "caesar.json")
	cfg := fmt.Sprintf(`{"database": {"url": %q}, "security": {"master_key": "new:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzA=,old:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}}`, url)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := app.Main([]string{"admin", "rekey", "-config", cfgPath}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "1 credential(s) re-wrapped") {
		t.Fatalf("stdout = %q", out.String())
	}
	var keyID string
	if err := pool.QueryRow(ctx, "SELECT key_id FROM credentials WHERE id = $1", cred.ID).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if keyID != "new" {
		t.Fatalf("key_id = %q, want new", keyID)
	}
}
```

(If a `DATABASE_URL` env override could leak into `config.Load` here, clear it with `t.Setenv("DATABASE_URL", "")`-style handling consistent with the existing admin test.)

Run: `cd backend && go test -tags integration ./internal/app/ -run TestAdminRekey -v` — PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/assets backend/internal/app
git commit -m "feat(backend): caesar admin rekey re-wraps credentials under the active master key"
```

---

### Task 15: Docs, roadmap, final green

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md`
- Commit: `docs/superpowers/plans/2026-08-03-caesar-03-assets-credentials.md` (this document)

- [ ] **Step 1: README**

In the README's setup/config section: document `CAESAR_MASTER_KEY` next to `DATABASE_URL` (format, "first entry active", generation via `openssl rand -base64 32`, dev keyset already in `caesar.dev.json`), and add `caesar admin rekey` under the admin commands with the three-step rotation recipe (prepend new key → rekey → drop old key). Add the new resources to any endpoint overview the README carries.

- [ ] **Step 2: Roadmap status**

In `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md`, flip row 03 to `**done** — 2026-08-03-caesar-03-assets-credentials.md`.

- [ ] **Step 3: Full gate**

Run: `task ci`
Expected: every lane green — backend (build, fmt, vet, lint, race, integration, arch), contracts (openapi/ts/sqlc regen-diff, migration up-down), secrets (see worktree caveat below — CI is authoritative).

- [ ] **Step 4: Commit**

```bash
git add README.md docs/superpowers/plans
git commit -m "docs(repo): plan 03 recorded, readme covers master key and rekey"
```

---

## Execution notes

- **Worktree:** execute in an isolated worktree per superpowers:using-git-worktrees (Plan 02 used `.claude/worktrees/caesar-02-api-auth-rbac`; name this one `caesar-03-assets-credentials`). Branch from current `main` (`ba440ad` or later).
- **Working pattern that held for Plan 02:** implementers haiku/sonnet, reviewers sonnet, final whole-branch review on the most capable model; **one implementer at a time** — tasks are sequential, most later tasks consume earlier interfaces.
- **Integration tests** need Docker (testcontainers boots its own postgres:18-alpine; the dev compose project is only for running the server by hand: `docker compose -f compose.dev.yaml -p caesar-03-assets-credentials up -d`; take it down at cleanup).
- **`task secrets` cannot run inside a linked worktree** (gitleaks' Docker mount can't resolve the external gitdir pointer — session-12 finding). Rely on CI for the secrets lane, or run it from the main checkout.
- **`task db:new` needs Docker** (Atlas dev database) and network access for the Atlas image on first use.
- **Session-11/12 rulings still bind:** txboundary skips generated files; oversized passwords go through counted+audited failure paths; nolint only with an in-code invariant comment.
- **Codex review:** per the roadmap's rule 4, implementation-stage `code_diff` reviews run within this plan's execution (per-task reviewer + final whole-branch review), not as artifact rounds.





