# Plan 07: Harness Drivers & Subscription Auth — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Real Claude Code and Codex CLI agents execute phases on the operator's own subscriptions — credentials materialized under the approved custody protocol, cap-hits waiting and resuming unattended, auth-death failing fast and typed — and a real agent produces a patch through the conveyor.

**Architecture:** Three layers extend in lockstep. The **spine** gains the delayed-retry contract (`not_before` + chargeable-failure counter) and an immutable credential binding on `ExecutionRequest`. **assets** gains two credential kinds, `secret_version`, and the whole mutable-credential aggregate (single-flight lease with takeover epoch, idempotent sync receipts) behind acquire/heartbeat/takeover/sync-commit/release ports. The **runner** gains a per-kind driver contract (custody mode, env delivery, FQDN allowlist, exit→outcome mapping), the host-backed auth-directory mount with guarded identity-pinned sync-back, the redaction scan over canonical bytes with quarantined logs, and lineage materialization into the staged workspace.

**Tech Stack:** Go 1.26, Postgres 18 (sqlc v1.31.1, Atlas v0.34.1 + goose), Docker Engine API v1.43 via `internal/platform/dockerapi`, `pgregory.net/rapid` where properties fit. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-05-caesar-subscription-auth-design.md` (USER-APPROVED 2026-08-05; §-references below point there) amending `docs/superpowers/specs/2026-08-02-caesar-v1-design.md` §3. Roadmap row 07 in `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md`.

## Baked decisions (flag these if the user pushes back)

1. **Generations / revision rematerialization moves to Plan 08.** The roadmap row lists it under 07, but a generation opens only on a gate's `request_changes` decision — gate decisions, and intake acceptance of gate nodes, are Plan 08. Plan 07 delivers linear lineage materialization (single cumulative-patch staging + context-file inputs), which is everything a gateless run can exercise. **The patch contract is cumulative**: extraction diffs the run's `BaseSHA` against the final tree (extract.go:130), and the staged workspace already contains the input patch — so every runner-produced patch subsumes its ancestors, the latest patch alone reproduces the whole linear chain, and staging applies exactly ONE patch input (two patch-typed inputs on one node ⇒ typed permanent `staging_failed`; applying a chain of cumulative patches would double-apply). Multi-patch / parallel-branch merge is Plan 08 (generations). The roadmap row is annotated by Task 14.
2. **Custody is driver logic, not schema.** `harnesses` gains no custody column: custody derives from (harness kind, credential kind) inside the driver table — `subscription_token`/`agent_auth_file` ⇒ `materialized`, `model_key` ⇒ `brokered` (§3). Reversible by editing one table in code.
3. **The credential binding pins at `buildRequest`** (per attempt), not at phase-row insertion — no schema on `phase_runs` for it. Every credential operation downstream (lease, open, materialize, manifest) uses the pinned binding; the runner never dereferences `harness.credential_id` again (§3 "immutable credential binding").
4. **`rate_limited` charges execution usage but not the failure counter; `credential_busy` charges neither** (§2.2, §4.3). The capped attempt really ran; the busy-wait never started a container.
5. **Failure-counter semantics:** `max_attempts` bounds *charged* failures; `attempt` stays the execution ordinal and may legitimately exceed `max_attempts` (§4.3). The claim CTE swaps its `attempt <= max_attempts` guard for `chargeable_failures < max_attempts`.
6. **Retry scheduling pinned** (§4.4): floor/base/cap per cause — `rate_limited` {floor 2m, base 15m, cap 1h}, `credential_busy` {floor 30s, base 30s, cap 10m}; exponent = prior uncharged waits on the node (`attempt − 1 − chargeable_failures`); jitter ±10% via an injected seam; provider-surfaced reset times clamped to [now+floor, wall_deadline]; eligibility past the wall deadline terminalizes immediately with the typed outcome.
7. **Lease tables:** `credential_leases` keyed by `credential_id` (single-flight *is* the primary key), `attempt_ref text` + index for the recovery-authority lookup, `epoch bigint` for the takeover fence; `credential_sync_receipts` PK `(credential_id, attempt_ref)` makes the sync commit idempotent (§2.1, §2.3, §2.6).
8. **New outcome codes:** `rate_limited` (transient), `auth_expired` (permanent, names the credential), `credential_busy` (transient), `credential_quarantined` (permanent — identity-pinned read-back caught substitution, §2.5), `secret_in_artifact` (class `policy_violation` — redaction fail-closed, §3). `Outcome` gains `RetryAt *time.Time`.
9. **Auth directory at `<DataRoot>/auth/<ref>/`, manifest at `<DataRoot>/manifests/<ref>.json`** — both outside `attempts/<ref>` so blanket teardown never touches them; removal only under the lease-epoch cleanup fence (§2.4). Codex mounts the auth dir at `/caesar-auth` with `CODEX_HOME=/caesar-auth` (inside the container's namespace, outside the workspace bind).
10. **Quarantined logs:** streams write to `<DataRoot>/quarantine/<ref>/`; only the post-read-back redacted copy is atomically published to `<DataRoot>/logs/<ref>/` (§3). Unscannable read-back (§2.7 window) ⇒ logs and workspace-derived artifacts permanently withheld.
11. **`.caesar/` is invisible to the diff** via a `/.caesar/` line in `base.git/info/exclude`, written at stage time — base.git is already not agent-writable, so the agent cannot re-include it.
12. **FQDN allowlists pinned** (verified 2026-08-05 against vendor docs + the Codex CLI source; drift is caught by `phase.egress_denied` events): claude-code `api.anthropic.com`, `claude.ai`, `platform.claude.com`; codex `chatgpt.com`, `auth.openai.com`, `api.openai.com`. Telemetry hosts (Datadog intakes, `ab.chatgpt.com`, Statsig/Sentry) deliberately absent = default-denied (§3).
13. **Exit→outcome mapping is an ordered marker table per driver** over the agent log tail plus the exit code — recorded CLI behavior, updated by the smoke run; unmatched nonzero exit stays transient `harness_error`. No live subscription calls in CI, ever (§6).
14. **Harness images** `caesar-harness-claude:dev` / `caesar-harness-codex:dev` built from `docker/harness/*/Dockerfile`, CLI versions pinned by ARG (resolved at authoring time via `npm view`), entry script reads `/workspace/.caesar/in/instructions.md`. CI drivers run against scripted fake-CLI images only.
15. **Codex sandbox spike** (§2.5) is a bounded Task 14 step with a written verdict — adopt-or-reject evidence, not implementation.

## Global Constraints

- Go 1.26; module `github.com/giiorg/caesar/backend`; stdlib-first, no new Go dependencies.
- Postgres 18; migrations via `task db:new -- <name>` (Atlas diff from `internal/platform/pg/schema.sql`, goose format); `task db:sqlc` after any queries/schema change; `task contracts:check` must stay green (regen-diff gate).
- Bounded contexts never import each other (arch tests, `internal/arch/rules.go`); runs↔assets meet only through ports declared in `internal/runs`/`internal/runs/runner` and adapted in `internal/app`. No exported context function mentions a pgx tx type (`txboundary` analyzer) — mutating assets paths use `pg.WithTx` internally.
- Deciding time predicates use the **database clock** (`now()`), never an app timestamp.
- The pinned Env allowlist for agent containers stays an allowlist — every addition is explicit in the driver table.
- All tests: `-race`; integration behind `//go:build integration`; runner suite behind `//go:build runnerintegration` (needs Docker + `task image:build`).
- Commit style: `<type>(<scope>): <description>`, one sentence, no body, no AI attribution.
- Full `task ci` **plus the runner lane** (`task image:build && task backend:test:runner` — `task ci` does not include it) green at plan end (worktree secrets-lane skip is the one known exception).

## File Structure (created / modified)

**Spine (runs):**
- `backend/internal/runs/outcome.go` — new codes, `RetryAt`, `Chargeable()`
- `backend/internal/runs/ports.go` — `CredentialBinding`, `HarnessRef` growth, `ExecutionRequest.Credential`, `CredentialReconciler`
- `backend/internal/runs/retry.go` (new) — `retrySchedule`, per-cause params
- `backend/internal/runs/queue.go`, `orchestrator.go` — claim/insert/evaluate changes, lease-obligation sweep hooks
- `backend/internal/runs/store/queries.sql` — `ClaimPhase`, `InsertPhase` growth
- `backend/internal/platform/pg/schema.sql` + migration — `phase_runs.not_before`, `phase_runs.chargeable_failures`
- `backend/internal/runs/api/dto.go` — `OutcomeResponse.RetryAt`

**assets:**
- `backend/internal/platform/pg/schema.sql` + migrations — credentials kinds/`secret_version`/`fingerprint`/freshness columns, `credential_leases`, `credential_sync_receipts`
- `backend/internal/assets/assets.go`, `credentials.go` — kinds, per-kind caps, `agent_auth_file` shape validation + fingerprint
- `backend/internal/assets/lease.go` (new) — the aggregate: Acquire/Heartbeat/Takeover/CommitSync/Release/LeaseByAttempt
- `backend/internal/assets/store/queries.sql` — lease/receipt/version queries
- `backend/internal/assets/api/dto.go`, `api.go` — freshness fields, per-kind body handling
- `backend/internal/assets/harnesses.go`, `repos.go` — `requireCredentialKind` per-kind sets

**runner:**
- `backend/internal/runs/runner/driver.go` (new) — driver table: custody, env, allowlists, ops, exit mapping, sensitive-field extraction
- `backend/internal/runs/runner/authdir.go` (new) — materialize/read-back/cleanup-fence for the auth directory
- `backend/internal/runs/runner/manifest.go` (new) — attempt manifest write/read
- `backend/internal/runs/runner/syncback.go` (new) — guarded, identity-pinned sync-back on every exit path
- `backend/internal/runs/runner/redact.go` (new) — canonical-bytes scan, quarantine publish, withhold
- `backend/internal/runs/runner/runner.go`, `spec.go`, `execute.go`, `collect.go`, `inspect.go`, `sweep.go`, `logs.go` — wiring the above
- `backend/internal/runs/runner/gitops/stage.go` + `cmd stage` — patch-chain apply, `.caesar/in` contract, info/exclude

**app / images / CI:**
- `backend/internal/app/runnerports.go`, `runsports.go`, `serve.go` — lease adapter, binding-aware resolvers
- `docker/harness/claude-code/Dockerfile`, `docker/harness/codex/Dockerfile`, entry scripts (new)
- `Taskfile.yml` — `image:harness` target; `.github/workflows/ci.yml` — runner-lane fixture images
- `backend/README.md`, roadmap row, this plan committed (Task 14)

---

### Task 1: Outcome vocabulary & the immutable credential binding

**Files:**
- Modify: `backend/internal/runs/outcome.go` (codes at :21-43, `Outcome` at :46-50, `Map()` at :55-57)
- Modify: `backend/internal/runs/ports.go` (`HarnessRef` :45-48, `ExecutionRequest` :85-95)
- Modify: `backend/internal/runs/orchestrator.go` (`buildRequest` :514-544)
- Modify: `backend/internal/app/runsports.go` (`runsAssetSource.HarnessByName` :71-77)
- Modify: `backend/internal/runs/runner/runner.go` (`HarnessSpec` :58-65 — drop `CredentialID`), `backend/internal/runs/runner/execute.go` (:73-91), `backend/internal/app/runnerports.go` (`appHarnessResolver` :70-83)
- Modify: `backend/internal/runs/api/dto.go` (:62-66, :135-140) + regen `backend/api/openapi.json`, `web/src/api/schema.d.ts`
- Test: `backend/internal/runs/outcome_test.go`, `backend/internal/runs/orchestrator_integration_test.go` (binding pin), runner unit tests

**Interfaces:**
- Consumes: existing `Outcome`, `OutcomeError`, `HarnessSource`, `assets.Service.GetHarnessByName` + `GetCredential`.
- Produces (later tasks rely on these exact names):
  - `runs.CodeRateLimited = "rate_limited"`, `runs.CodeAuthExpired = "auth_expired"`, `runs.CodeCredentialBusy = "credential_busy"`, `runs.CodeCredentialQuarantined = "credential_quarantined"`, `runs.CodeSecretInArtifact = "secret_in_artifact"`
  - `Outcome.RetryAt *time.Time` and `func (o Outcome) Chargeable() bool`
  - `runs.CredentialBinding{ID uuid.UUID; Kind string}` and `ExecutionRequest.Credential CredentialBinding`
  - `runs.HarnessRef{ID uuid.UUID; Kind string; CredentialID uuid.UUID; CredentialKind string}`

- [ ] **Step 1: Write failing unit tests for the new vocabulary**

In `outcome_test.go`:

```go
func TestOutcome_ChargeableExemptsCapAndContentionWaits(t *testing.T) {
	cases := []struct {
		out  Outcome
		want bool
	}{
		{Outcome{Class: OutcomeTransient, Code: CodeHarnessError}, true},
		{Outcome{Class: OutcomeTransient, Code: CodeRateLimited}, false},
		{Outcome{Class: OutcomeTransient, Code: CodeCredentialBusy}, false},
		{Outcome{Class: OutcomePermanent, Code: CodeAuthExpired}, false}, // permanent: never retried, charge is moot
	}
	for _, c := range cases {
		if got := c.out.Chargeable(); got != c.want {
			t.Errorf("Chargeable(%s/%s) = %v, want %v", c.out.Class, c.out.Code, got, c.want)
		}
	}
}

func TestOutcome_MapCarriesRetryAt(t *testing.T) {
	at := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	m := (Outcome{Class: OutcomeTransient, Code: CodeRateLimited, Message: "cap", RetryAt: &at}).Map()
	if m["retry_at"] != at.Format(time.RFC3339) {
		t.Fatalf("retry_at = %v", m["retry_at"])
	}
	if _, present := (Outcome{Code: CodeHarnessError}).Map()["retry_at"]; present {
		t.Fatal("retry_at must be absent when unset")
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/runs/ -run TestOutcome_ -v` → FAIL (undefined: `CodeRateLimited`, no `RetryAt`).

- [ ] **Step 3: Implement the vocabulary**

In `outcome.go`, extend the const block (subscription-auth group, after the runner block):

```go
// Plan 07: subscription-auth outcomes (spec 2026-08-05 §2, §4).
CodeRateLimited           = "rate_limited"           // cap window exhausted — waiting, not failing
CodeAuthExpired           = "auth_expired"           // credential dead; message names it
CodeCredentialBusy        = "credential_busy"        // single-flight lease held elsewhere; charges nothing
CodeCredentialQuarantined = "credential_quarantined" // read-back identity mismatch (§2.5)
CodeSecretInArtifact      = "secret_in_artifact"     // redaction scan hit; artifact withheld (§3)
```

`Outcome` gains `RetryAt *time.Time \`json:"retry_at,omitempty"\``. `Map()` adds `retry_at` (RFC3339) only when set. Add:

```go
// Chargeable reports whether this failure spends the phase's failure budget.
// Cap waits and lease contention are facts of subscription life, not errors:
// they requeue without charging (spec §2.2, §4.3).
func (o Outcome) Chargeable() bool {
	return o.Class == OutcomeTransient && o.Code != CodeRateLimited && o.Code != CodeCredentialBusy
}
```

- [ ] **Step 4: Run** — `go test ./internal/runs/ -run TestOutcome_ -v` → PASS.

- [ ] **Step 5: Extend the binding types**

`ports.go`:

```go
// CredentialBinding is the phase's pinned credential identity (spec §3):
// resolved once at request build, it travels intact to the runner, which must
// never re-resolve the credential off the mutable harness row.
type CredentialBinding struct {
	ID   uuid.UUID
	Kind string // assets credential kind vocabulary, opaque here
}
```

`HarnessRef` grows `CredentialID uuid.UUID` and `CredentialKind string`. `ExecutionRequest` grows `Credential CredentialBinding` (zero value = harnessless node, e.g. fake-executed kinds). In `buildRequest` (orchestrator.go:527), after `req.HarnessID = h.ID` add `req.Credential = CredentialBinding{ID: h.CredentialID, Kind: h.CredentialKind}`.

- [ ] **Step 6: Adapter + runner side**

`runsports.go` `HarnessByName`: fetch the harness, then its credential kind — two reads, both against assets:

```go
func (s runsAssetSource) HarnessByName(ctx context.Context, name string) (runs.HarnessRef, error) {
	h, err := s.svc.GetHarnessByName(ctx, name)
	if err != nil {
		return runs.HarnessRef{}, err
	}
	c, err := s.svc.GetCredential(ctx, h.CredentialID)
	if err != nil {
		return runs.HarnessRef{}, err
	}
	return runs.HarnessRef{ID: h.ID, Kind: string(h.Kind), CredentialID: h.CredentialID, CredentialKind: string(c.Kind)}, nil
}
```

Runner: delete `HarnessSpec.CredentialID`; `appHarnessResolver.HarnessSpec` stops mapping it; `execute.go:87` becomes `r.creds.OpenCredential(ctx, req.Credential.ID, "attempt:"+ref.String())`. Any `hs.CredentialID` reference is now a compile error — fix all.

- [ ] **Step 7: Binding-race test (spec §6 "binding race")**

In the runner suite (fake resolver seam, no Docker needed — unit-style with fakes in `internal/runs/runner`): resolver whose `HarnessSpec` result is mutated between `buildRequest`-time binding and `Execute`; assert `OpenCredential` was called with the *bound* id, not the resolver's current one. (The full assets-backed race test lands in Task 5's integration coverage.)

- [ ] **Step 8: API surface** — `OutcomeResponse` gains `RetryAt *time.Time \`json:"retry_at,omitempty"\``; `outcomeResponse()` maps it. Run `task contracts:gen`, commit regenerated `openapi.json` + `schema.d.ts`.

- [ ] **Step 9: Full gate** — `task backend:ci contracts:check` → green.

- [ ] **Step 10: Commit** — `git commit -m "feat(backend): subscription outcome vocabulary and pinned credential binding"`

---

### Task 2: The delayed-retry contract — `not_before` + chargeable-failure counter

**Files:**
- Modify: `backend/internal/platform/pg/schema.sql` (`phase_runs` :195-214) + new migration via `task db:new -- delayed_retry`
- Modify: `backend/internal/runs/store/queries.sql` (`ClaimPhase` :82-103, `InsertPhase` :77-80) + `task db:sqlc`
- Create: `backend/internal/runs/retry.go`
- Modify: `backend/internal/runs/queue.go` (`insertPhaseTx` :78-86, `claimed` :19-27, `claimNext` :43-63), `backend/internal/runs/orchestrator.go` (`evaluateFailure` :742-843, `startPendingRuns` root-phase insertion), `backend/internal/runs/export_integration_test.go` (:33)
- Test: `backend/internal/runs/retry_test.go`, `backend/internal/runs/delayedretry_integration_test.go` (new)

**Interfaces:**
- Consumes: Task 1's codes, `Chargeable()`, `RetryAt`.
- Produces: `phase_runs.not_before` (nullable timestamptz), `phase_runs.chargeable_failures` (int, default 0); `insertPhaseTx(ctx, tx, runID, nodeID uuid/string, attempt, maxAttempts, chargeableFailures int32, notBefore *time.Time)`; `claimed.ChargeableFailures int32`; `func retryDelay(out Outcome, now time.Time, waits int32, jitter func() float64) time.Duration` (the caller adds it to the database clock — `DBNow` store query); orchestrator seam `jitter func() float64` (defaults `rand.Float64`).

- [ ] **Step 1: Schema + migration**

`schema.sql` `phase_runs` gains:

```sql
    chargeable_failures integer NOT NULL DEFAULT 0 CHECK (chargeable_failures >= 0),
    not_before   timestamptz,
```

and below the existing indexes:

```sql
CREATE INDEX phase_runs_not_before_idx ON phase_runs (not_before)
    WHERE status = 'pending' AND not_before IS NOT NULL;
```

Run `task db:new -- delayed_retry`; verify the goose file adds exactly these; `task db:sqlc`. The migration round-trip test (`migrate_updown_integration_test.go`) must stay green.

- [ ] **Step 2: Claim predicate + insert**

`ClaimPhase` candidate WHERE: replace `AND pr.attempt <= pr.max_attempts` with:

```sql
      AND pr.chargeable_failures < pr.max_attempts
      AND (pr.not_before IS NULL OR pr.not_before <= now())
```

(db clock, same as the deadline predicates — spec §4.1). `InsertPhase` gains `chargeable_failures` and `not_before` columns/params. `task db:sqlc`; `claimNext` maps `ChargeableFailures` onto `claimed`.

- [ ] **Step 3: Failing scheduler unit tests**

`retry_test.go`:

```go
func TestRetryDelay_ProviderResetClampedToFloorAndDeadlineless(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	noJitter := func() float64 { return 0.5 } // multiplier 1.0
	past := now.Add(-time.Hour)
	// malformed/past reset falls back to base backoff, never before floor
	got := retryDelay(Outcome{Code: CodeRateLimited, RetryAt: &past}, now, 0, noJitter)
	if got < 2*time.Minute {
		t.Fatalf("past reset must hold the floor, got %v", got)
	}
	// a sane provider reset is used, floored
	soon := now.Add(30 * time.Second)
	got = retryDelay(Outcome{Code: CodeRateLimited, RetryAt: &soon}, now, 0, noJitter)
	if got < 2*time.Minute || got > 3*time.Minute {
		t.Fatalf("reset below floor must clamp to floor, got %v", got)
	}
	// a provider reset BEYOND the fallback cap is honored, not capped —
	// waking at cap would retry while the window is still shut; the wall
	// deadline (compared by the caller) is its only ceiling
	late := now.Add(5 * time.Hour)
	got = retryDelay(Outcome{Code: CodeRateLimited, RetryAt: &late}, now, 0, noJitter)
	if got < 4*time.Hour {
		t.Fatalf("provider reset must not be capped at %v, got %v", time.Hour, got)
	}
}

func TestRetryDelay_BackoffGrowsAndCapsUnderAnyJitter(t *testing.T) {
	now := time.Now()
	// [floor, cap] is a hard bound: assert it at both jitter extremes, not
	// just the neutral multiplier — the clamp runs after the jitter.
	for _, jit := range []float64{0, 0.5, 1} {
		jitter := func() float64 { return jit }
		prev := time.Duration(0)
		for waits := int32(0); waits < 8; waits++ {
			d := retryDelay(Outcome{Code: CodeCredentialBusy}, now, waits, jitter)
			if d < 30*time.Second || d > 10*time.Minute {
				t.Fatalf("jit=%v waits=%d delay %v outside [floor,cap]", jit, waits, d)
			}
			if jit == 0.5 && d < prev { // monotonicity is only meaningful un-jittered
				t.Fatalf("backoff shrank: %v -> %v", prev, d)
			}
			prev = d
		}
	}
}
```

- [ ] **Step 4: Run** — FAIL (undefined `retryDelay`).

- [ ] **Step 5: Implement `retry.go`**

```go
// Package-internal delayed-retry scheduling (spec §4.4). Two regimes, per
// baked decision 6: a FALLBACK delay (no usable provider reset) is our own
// guessing and sits on [floor, cap] — a hard bound, jitter included; a
// PROVIDER-STATED reset is truth and is only floor-clamped — capping it
// would wake us before the provider unlocks, and its real ceiling is the
// wall deadline, which the caller compares against the resulting time
// (past-deadline eligibility terminalizes instead of parking).
type retryParams struct{ floor, base, cap time.Duration }

var retrySchedules = map[string]retryParams{
	CodeRateLimited:    {floor: 2 * time.Minute, base: 15 * time.Minute, cap: time.Hour},
	CodeCredentialBusy: {floor: 30 * time.Second, base: 30 * time.Second, cap: 10 * time.Minute},
}

// retryDelay returns a duration, not an absolute time: the caller adds it to
// the DATABASE clock (the same clock the claim CTE's `not_before <= now()`
// predicate reads — the global DB-clock constraint), never to an app-side
// timestamp. `now` is that database now, used only to convert a
// provider-surfaced absolute RetryAt into a delay.
func retryDelay(out Outcome, now time.Time, waits int32, jitter func() float64) time.Duration {
	p, ok := retrySchedules[out.Code]
	if !ok {
		p = retrySchedules[CodeRateLimited] // conservative default for future wait-class codes
	}
	providerReset := out.RetryAt != nil && out.RetryAt.After(now)
	var delay time.Duration
	if providerReset {
		// The provider's word is never jittered: negative jitter would wake
		// us before the window opens — the exact failure the no-cap rule
		// exists to prevent. (User ruling 2026-08-05, task review round 1.)
		delay = out.RetryAt.Sub(now)
	} else {
		delay = p.base << min(uint(waits), 6)
		frac := 1 + 0.1*(2*jitter()-1) // ±10%, then clamp: the bounds are hard
		delay = time.Duration(float64(delay) * frac)
		delay = min(delay, p.cap) // the cap bounds only our own backoff, never the provider's word
	}
	return max(p.floor, delay)
}
```

- [ ] **Step 6: Run** — PASS. (Jitter is applied before the clamp, so `[floor, cap]` holds for every jitter value — assert it at jitter extremes 0.0 and 1.0, not only with `noJitter`.)

- [ ] **Step 7: Rewire `evaluateFailure`**

The exact deltas, in order (current line refs in parentheses):

1. Counter (`:755`): replace `retry := out.Retryable() && c.Ref.Attempt < c.MaxAttempts` with:
```go
newCount := c.ChargeableFailures
if out.Chargeable() {
	newCount++
}
retry := out.Retryable() && newCount < c.MaxAttempts
```
2. Budget pre-check (`:764-771`): skip entirely when `out.Code == CodeCredentialBusy` (charges nothing, and a zero-charge requeue must never be demoted to `budget_exhausted`). `rate_limited` keeps the pre-check — the capped attempt ran and its time counts.
3. Eligibility: when `retry && !out.Chargeable()`:
```go
dbNow, err := q.DBNow(ctx) // deciding time predicates use the database clock (global constraint)
...
delay := retryDelay(out, dbNow, c.Ref.Attempt-1-c.ChargeableFailures, o.jitter)
nb := dbNow.Add(delay)
if nb.After(c.RunWallDeadline) { // never park a row that can't legally run (spec §4.4)
	retry = false
	// terminal branch will stamp the run with `out` itself — see step 4 below
}
```
`DBNow` is a new one-liner store query (`-- name: DBNow :one` / `SELECT now()::timestamptz`) run inside the evaluate transaction, so `nb` and the `not_before <= now()` claim predicate rest on the same clock, and the `RunWallDeadline` comparison is DB-timestamp against DB-timestamp — app-clock skew cannot move the park-vs-terminalize boundary. `c.RunWallDeadline` is zero on the reconcile path (`claimedFromRow`) at THIS task's point in the sequence — reconcile outcomes are all chargeable until Task 7 — so guard `!c.RunWallDeadline.IsZero()`. Task 7 Step 6 then makes reconciled `rate_limited` outcomes real, fills `RunWallDeadline` on the reconcile claims, and deletes any unreachability comment: from there the branch must behave identically on both paths.
4. Exhaustion stamp (`:824-833`): the `out.Retryable()` case message becomes `"node %s exhausted %d chargeable failures"`; add a case above it: when the deadline-boundary demotion fired (track with a local `parkedPastDeadline bool`), `runOut = out` — the run fails with the typed `rate_limited`/`credential_busy` outcome.
5. Usage (`:786-790`, `:819-823`): wrap both `AddRunUsage` calls with `if out.Code != CodeCredentialBusy` (rate_limited still charges).
6. Insertion (`:791`): `insertPhaseTx(ctx, tx, c.Ref.RunID, c.Ref.NodeID, c.Ref.Attempt+1, c.MaxAttempts, newCount, notBeforePtr)` where `notBeforePtr` is nil for charged failures.
7. `EventPhaseRetried` payload additionally carries `"chargeable_failures": newCount` and, when set, `"not_before": nb.UTC().Format(time.RFC3339)`.
8. Root-phase insertion (`startPendingRuns`) and every other `insertPhaseTx` caller passes `0, nil`.
9. `Orchestrator` gains `jitter func() float64` initialized to `rand.Float64` in `NewOrchestrator`; integration tests overwrite it in-package.

- [ ] **Step 8: Failing integration tests, then green**

`delayedretry_integration_test.go` (build tag `integration`, fake executor scripted via `caesar-fake:` directives — extend `parseDirective` to accept `code=rate_limited`/`code=credential_busy`, which it already does generically via `code=`):

1. **Invisible until eligible:** run a node that fails `rate_limited` once; assert the retry row has `not_before` ≥ floor in the future and a `claimNext`-driven worker does not pick it up; SQL-backdate `not_before` (the Plan 05 lease-expiry precedent) and assert it then runs and succeeds.
2. **Caps never exhaust:** `max_attempts=2`, script 4 consecutive `rate_limited` failures then success (backdating between each); assert final run `succeeded`, final attempt ordinal 5 > 2, `chargeable_failures` stayed 0.
3. **Deadline boundary:** wall deadline near-now; `rate_limited` with far `RetryAt`; assert run fails immediately with outcome code `rate_limited` and no parked row exists.
4. **Restart survival:** stop the orchestrator (cancel), start a fresh one against the same pool; the backdated row is claimed — `not_before` is durable state, nothing in-process.
5. **Fallback floor:** `rate_limited` without `RetryAt` — `not_before - now ∈ [floor, cap]` (hard bounds — jitter is clamped); total row count for the node stays bounded (one new row per wait).
6. **Zero charge:** `credential_busy` failure — `execution_used_ms` delta 0 and `chargeable_failures` 0 on the successor; a concurrent claimant during the wait sees nothing claimable.
7. **Charged path regression:** ordinary `harness_error` ×`max_attempts` still ends `attempts_exhausted`.

- [ ] **Step 9: Full gate** — `task backend:ci contracts:check` → green (drift gate covers the regenerated store).

- [ ] **Step 10: Commit** — `git commit -m "feat(backend): delayed-retry contract with not_before and chargeable-failure counter"`

---

### Task 3: assets — new credential kinds, per-kind caps, `secret_version`, fingerprint dedup, freshness

**Files:**
- Modify: `backend/internal/platform/pg/schema.sql` (`credentials` :80-89) + migration `task db:new -- subscription_credentials`
- Modify: `backend/internal/assets/assets.go` (kinds :26-33, pins :90-98, `Credential` :67-74), `credentials.go` (whole credential surface), `harnesses.go` (:124, :226), `repos.go` (`requireCredentialKind` :61-74)
- Modify: `backend/internal/assets/store/queries.sql` (:32-60) + `task db:sqlc`
- Modify: `backend/internal/assets/api/dto.go` (:76-97, mapper :132), `api.go`, `backend/internal/contracts/contracts.go` if request shapes change (they don't — same `CreateCredentialRequest`)
- Test: `backend/internal/assets/credentials_test.go`, `credentials_integration_test.go`, `api/api_integration_test.go`

**Interfaces:**
- Consumes: existing envelope (`secrets.Keyset`, AAD = credential id), audit recorder pattern.
- Produces:
  - `assets.CredentialSubscriptionToken CredentialKind = "subscription_token"`, `assets.CredentialAgentAuthFile CredentialKind = "agent_auth_file"`
  - `maxAuthFileLen = 64 << 10`; `validateSecret(kind CredentialKind, secret []byte) error`
  - `func authFileFingerprint(secret []byte) (string, error)` — hex SHA-256 of `tokens.refresh_token`
  - `Credential` gains `SecretVersion int64`, `LastUsedAt *time.Time`, `LastAuthExpiredAt *time.Time`
  - `func (s *Service) MarkCredentialAuthExpired(ctx context.Context, id uuid.UUID) error`
  - `func (s *Service) UseCredentialVersioned(ctx context.Context, id uuid.UUID, purpose string) ([]byte, int64, error)` — `UseCredential` returning `row.SecretVersion` alongside (same audited path; `UseCredential` delegates to it). Tasks 10/11 need the version to prove a crash-adopted attempt's redaction set is complete.
  - `requireCredentialKindIn(ctx, q, id, wants ...CredentialKind) error`; harness kind→allowed set: `claude-code → {subscription_token, model_key}`, `codex → {agent_auth_file, model_key}`

- [ ] **Step 1: Schema + migration**

`credentials` in `schema.sql`:

```sql
    kind text NOT NULL CHECK (kind IN ('forge_token', 'model_key', 'subscription_token', 'agent_auth_file')),
    secret_version bigint NOT NULL DEFAULT 1 CHECK (secret_version >= 1),
    fingerprint text,
    last_used_at timestamptz,
    last_auth_expired_at timestamptz,
```

plus, after the table:

```sql
-- Two active agent_auth_file rows sharing one refresh token would hold
-- independent single-flight leases against the same underlying credential
-- (spec §2 "import is exclusive custody transfer").
CREATE UNIQUE INDEX credentials_fingerprint_key ON credentials (fingerprint)
    WHERE fingerprint IS NOT NULL;
```

`task db:new -- subscription_credentials`, `task db:sqlc` (all four credential RETURNING lists in `queries.sql` gain the new columns; `newCredential` constructor grows accordingly).

- [ ] **Step 2: Failing kind/cap/fingerprint unit-level tests**

In `credentials_integration_test.go` (service-level, real Postgres — the assets suite convention):

```go
func TestCreateCredential_SubscriptionKinds(t *testing.T) {
	svc, _, _ := newTestService(t)
	actor := seedActor(t, ...)
	// subscription_token: plain string, 4 KiB cap
	_, err := svc.CreateCredential(ctx, actor, "claude-max", assets.CredentialSubscriptionToken,
		[]byte("sk-ant-oat01-...token..."))
	// agent_auth_file: must be JSON with tokens.refresh_token
	authJSON := []byte(`{"auth_mode":"chatgpt","tokens":{"id_token":"eyJ...","access_token":"eyJ...","refresh_token":"rt-abc","account_id":"acc_1"},"last_refresh":"2026-08-05T00:00:00Z"}`)
	c, err := svc.CreateCredential(ctx, actor, "codex-sub", assets.CredentialAgentAuthFile, authJSON)
	// second credential with the same refresh token → conflict
	_, err = svc.CreateCredential(ctx, actor, "codex-sub-2", assets.CredentialAgentAuthFile, authJSON)
	// assert xerrors.ClassConflict
}

func TestCreateCredential_AgentAuthFileCaps(t *testing.T) {
	// 64 KiB passes (well-formed JSON padded with an ignored field), 64 KiB + 1 fails,
	// non-JSON fails, JSON without tokens.refresh_token fails.
}

func TestRotateCredentialSecret_BumpsVersionAndFingerprint(t *testing.T) {
	// rotate an agent_auth_file with a new refresh_token: secret_version 1 → 2,
	// fingerprint changes, updated_at advances.
}
```

- [ ] **Step 3: Run** — FAIL (kinds undefined).

- [ ] **Step 4: Implement**

`assets.go`: add the two kind consts; `Valid()` accepts them; add `maxAuthFileLen = 64 << 10` to the pins block. `Credential` gains the three fields; `newCredential` maps them.

`credentials.go`:

```go
// validateSecret enforces the per-kind size and shape rules (spec §5). The
// flat cap fits single tokens; auth.json is a multi-token file with its own
// bound. agent_auth_file must parse: caesar reads pinned paths from it
// (fingerprint here, sensitive fields + identity in the runner) and a file
// that cannot yield them is unusable, better refused at the door.
func validateSecret(kind CredentialKind, secret []byte) error {
	limit := maxSecretLen
	if kind == CredentialAgentAuthFile {
		limit = maxAuthFileLen
	}
	if len(secret) == 0 || len(secret) > limit {
		return xerrors.E(xerrors.ClassInvalid, "invalid credential").WithFields(
			xerrors.FieldError{Field: "secret", Message: fmt.Sprintf("must be 1-%d bytes", limit)})
	}
	if kind == CredentialAgentAuthFile {
		if _, err := authFileFingerprint(secret); err != nil {
			return err
		}
	}
	return nil
}

// authFile is the pinned subset of Codex's auth.json caesar reads. Unknown
// fields are deliberately tolerated — the CLI owns the schema (spec §2).
type authFile struct {
	Tokens struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
	OpenAIAPIKey string `json:"OPENAI_API_KEY"`
}

func authFileFingerprint(secret []byte) (string, error) {
	var f authFile
	if err := json.Unmarshal(secret, &f); err != nil || f.Tokens.RefreshToken == "" {
		return "", xerrors.E(xerrors.ClassInvalid, "invalid credential").WithFields(
			xerrors.FieldError{Field: "secret", Message: "must be a Codex auth.json with tokens.refresh_token"})
	}
	sum := sha256.Sum256([]byte(f.Tokens.RefreshToken))
	return hex.EncodeToString(sum[:]), nil
}
```

`CreateCredential`/`RotateCredentialSecret`: call `validateSecret(kind, secret)`; compute + store the fingerprint for `agent_auth_file` (NULL otherwise); rotation sets `secret_version = secret_version + 1`; `isUniqueViolation` on insert/rotate maps to `ClassConflict` with message `"an active credential already holds this refresh token — import transfers custody (spec: exclusive custody transfer)"`. Kind-invalid message at :49 updated to name all four kinds. `UseCredential`'s tx additionally sets `last_used_at = now()`. New:

```go
// MarkCredentialAuthExpired records auth death for the freshness view (§5).
// Called by the runner (through its port) when an attempt maps to auth_expired.
func (s *Service) MarkCredentialAuthExpired(ctx context.Context, id uuid.UUID) error
```
— `pg.WithTx`: update `last_auth_expired_at = now()`, audit `credential.auth_expired` (no actor — machine path).

- [ ] **Step 5: Harness-kind gating**

`repos.go`: generalize to `requireCredentialKindIn(ctx, q, id, wants ...CredentialKind)`; repos call it with just `CredentialForgeToken`. `harnesses.go` :124/:226 pass the per-kind set:

```go
var harnessCredentialKinds = map[HarnessKind][]CredentialKind{
	HarnessClaudeCode: {CredentialSubscriptionToken, CredentialModelKey},
	HarnessCodex:      {CredentialAgentAuthFile, CredentialModelKey},
}
```

Integration test: registering a claude-code harness with an `agent_auth_file` credential → `ClassInvalid` naming the allowed kinds; with `subscription_token` → OK.

- [ ] **Step 6: API surface**

`CredentialResponse` gains `SecretVersion int64 \`json:"secret_version"\``, `ProvidedAt time.Time \`json:"provided_at"\`` (= `UpdatedAt` — when the operator last supplied the secret), `LastUsedAt *time.Time \`json:"last_used_at,omitempty"\``, `LastAuthExpiredAt *time.Time \`json:"last_auth_expired_at,omitempty"\``. Request DTOs unchanged (`Secret string` carries auth.json contents verbatim). `task contracts:gen` + commit regenerated artifacts. API test: created `agent_auth_file` surfaces the fields; 64 KiB secret round-trips through the HTTP layer (1 MiB body limit already accommodates it — assert, don't assume).

- [ ] **Step 7: Run everything** — `task backend:ci contracts:check` → green.

- [ ] **Step 8: Commit** — `git commit -m "feat(backend): subscription credential kinds with per-kind caps, versioning, and freshness"`

---

### Task 4: assets — the mutable-credential aggregate (lease, epoch, receipts)

> **Amendments (user rulings 2026-08-05, task review round 1 — these govern over the pinned sketches below):**
> 1. **Receipts are epoch-aware.** A sync receipt for `(credential_id, attempt_ref)` with a *different* epoch is NOT "already applied": the CAS adjudicates, and `InsertSyncReceipt` is an upsert. Only a same-epoch receipt short-circuits to `SyncAlreadyApplied`. (Epoch-blind receipts silently swallow the reconciler's post-takeover sync-back.)
> 2. **Acquire is fenced after takeover.** `AcquireCredentialLease` returns `ErrLeaseBusy` when the existing lease's `epoch > 1`, even for the owning `attempt_ref` — takeover transfers custody to recovery, and reconcilers use Takeover, never Acquire. Idempotent re-acquire remains only while no takeover has occurred.
> 3. **Seal stays outside the tx.** `CommitCredentialSync` runs `validateSecret` + fingerprint + `Seal` before `pg.WithTx` (kind read from the pool first), per the global crypto-outside-tx constraint; step 4's in-tx placement is superseded.
> 4. `credential_leases.attempt_ref` gains a UNIQUE index (replacing the plain one): `CredentialLeaseByAttempt`'s singular contract is enforced by schema, not assumption.

**Files:**
- Modify: `backend/internal/platform/pg/schema.sql` + migration `task db:new -- credential_leases`
- Create: `backend/internal/assets/lease.go`
- Modify: `backend/internal/assets/store/queries.sql` + `task db:sqlc`
- Test: `backend/internal/assets/lease_integration_test.go` (new)

**Interfaces:**
- Consumes: Task 3's `secret_version`, envelope seal/open, audit pattern (`UseCredential` ordering: mutation + audit in one `pg.WithTx`, crypto outside).
- Produces (exact names Task 5's port adapter binds to):
  - `assets.CredentialLease{CredentialID uuid.UUID; AttemptRef string; Epoch, SecretVersion int64; AcquiredAt, HeartbeatAt time.Time}`
  - `assets.ErrLeaseBusy` (sentinel, wrapped in `ClassConflict`)
  - `AcquireCredentialLease(ctx, credentialID uuid.UUID, attemptRef string) (CredentialLease, []byte, error)` — idempotent for the same `attemptRef`; the `[]byte` is the atomic secret snapshot at the lease-granted version (see Step 4). The acquire tx also stamps `last_used_at = now()` on the credential (fresh AND idempotent re-acquire): leased kinds never pass through `UseCredential`, and the §5 freshness view must still see them.
  - `HeartbeatCredentialLease(ctx, credentialID uuid.UUID, attemptRef string, epoch int64) error` — 0 rows ⇒ `ClassConflict` (fenced)
  - `TakeoverCredentialLease(ctx, credentialID uuid.UUID, attemptRef string) (CredentialLease, error)` — atomically `epoch = epoch + 1`
  - `type SyncStatus string` — `SyncApplied`, `SyncAlreadyApplied`, `SyncCASLost`, `SyncFenced`
  - `CommitCredentialSync(ctx, credentialID uuid.UUID, attemptRef string, epoch, expectVersion int64, newSecret []byte) (SyncStatus, error)`
  - `ReleaseCredentialLease(ctx, credentialID uuid.UUID, attemptRef string, epoch int64) error` — epoch mismatch is a silent no-op (the old worker relinquishes)
  - `CredentialLeaseByAttempt(ctx, attemptRef string) (CredentialLease, bool, error)`
  - `RecordCredentialQuarantine(ctx, credentialID uuid.UUID, attemptRef string) error` — audit-only

- [ ] **Step 1: Schema**

```sql
-- The single-flight lease: the primary key IS the mechanism (one row per
-- credential). The epoch fences a paused owner from its own reconciler —
-- identity alone cannot, because they are the same phase run (spec §2.1).
CREATE TABLE credential_leases (
    credential_id  uuid PRIMARY KEY REFERENCES credentials (id) ON DELETE RESTRICT,
    attempt_ref    text NOT NULL,
    epoch          bigint NOT NULL DEFAULT 1 CHECK (epoch >= 1),
    secret_version bigint NOT NULL,
    acquired_at    timestamptz NOT NULL DEFAULT now(),
    heartbeat_at   timestamptz NOT NULL DEFAULT now()
);
-- The lease row, indexed by owning attempt, is the recovery authority:
-- reconciliation consults it even when no manifest and no container exist
-- (spec §2.3).
CREATE INDEX credential_leases_attempt_idx ON credential_leases (attempt_ref);

-- One receipt per (credential, attempt): the commit-then-crash retry is
-- recognizable as already applied and can never clobber an operator
-- rotation (spec §2.6).
CREATE TABLE credential_sync_receipts (
    credential_id uuid NOT NULL REFERENCES credentials (id) ON DELETE CASCADE,
    attempt_ref   text NOT NULL,
    epoch         bigint NOT NULL,
    from_version  bigint NOT NULL,
    to_version    bigint NOT NULL,
    committed_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (credential_id, attempt_ref)
);
```

`task db:new -- credential_leases`, then queries (`queries.sql` additions): `AcquireLease :one` (`INSERT ... ON CONFLICT (credential_id) DO NOTHING RETURNING *`), `GetLease :one`, `GetLeaseByAttempt :one`, `HeartbeatLease :execrows` (`WHERE credential_id=$1 AND attempt_ref=$2 AND epoch=$3`), `TakeoverLease :one` (`UPDATE ... SET epoch = epoch + 1 WHERE credential_id=$1 AND attempt_ref=$2 RETURNING *`), `ReleaseLease :execrows` (epoch-matched DELETE), `GetSyncReceipt :one`, `InsertSyncReceipt :exec`, `UpdateCredentialSecretCAS :execrows` (`UPDATE credentials SET key_id=$2, wrapped_dek=$3, ciphertext=$4, fingerprint=$5, secret_version = secret_version + 1, updated_at = now() WHERE id = $1 AND secret_version = $6`), `ListStaleLeases :many` (`WHERE heartbeat_at < now() - make_interval(secs => $1)`). `task db:sqlc`.

- [ ] **Step 2: Failing integration tests — the crash/race matrix core (spec §6)**

`lease_integration_test.go`, all against real Postgres:

1. **Acquire/busy/idempotent:** A acquires; B's acquire → `ErrLeaseBusy` (and `ClassConflict`); A's second acquire with the same ref returns the same lease (same epoch, same version). Both successful acquires advanced `last_used_at` (§5 freshness — the leased path never touches `UseCredential`).
2. **Heartbeat fence:** heartbeat with a stale epoch → `ClassConflict`; with the current epoch → `heartbeat_at` advances (assert via re-read).
3. **Takeover fences the old owner:** A acquires (epoch 1); reconciler `Takeover` → epoch 2; A's `CommitCredentialSync(..., epoch=1, ...)` → `SyncFenced`, store untouched; reconciler's commit at epoch 2 → `SyncApplied`, `secret_version` +1.
4. **Receipt idempotency:** commit twice with identical args — first `SyncApplied`, second `SyncAlreadyApplied`; `secret_version` bumped exactly once; decrypting the stored secret yields the new bytes.
5. **Operator rotation wins:** A acquires at v1; operator `RotateCredentialSecret` → v2; A's commit expecting v1 → `SyncCASLost`; stored secret is the operator's; audit `credential.sync_discarded` present.
6. **Expiry alone never breaks single-flight:** SQL-backdate `heartbeat_at` beyond any TTL; B's acquire still `ErrLeaseBusy` (spec §2.1 — only conclusion releases).
7. **Release semantics:** stale-epoch release is a no-op returning nil (lease intact); current-epoch release deletes; `CredentialLeaseByAttempt` then reports absence.
8. **Fingerprint collision discards:** two `agent_auth_file` credentials; A's holder commits a refresh whose new refresh token equals B's fingerprint → `SyncCASLost`, audit `credential.sync_discarded` present (in the SAME committed tx — the pre-check, not a 23505 abort), A's ciphertext/version unchanged, and A's lease still releasable afterwards.

- [ ] **Step 3: Run** — FAIL (nothing exists).

- [ ] **Step 4: Implement `lease.go`**

Shape rules (all methods `pg.WithTx` internally, txboundary-clean, every mutation audited in-tx):

```go
var ErrLeaseBusy = errors.New("assets: credential lease held")

// AcquireCredentialLease acquires the single-flight lease AND snapshots the
// credential it grants in one transaction: the returned plaintext is the
// bytes of exactly the secret_version recorded on the lease row (spec §3 —
// the acquired version must correspond to the materialized credential). The
// runner materializes THESE bytes; it never performs a separate open for a
// leased credential, so an operator rotation between acquire and delivery
// cannot make sync-back's CAS discard a legitimate refresh.
func (s *Service) AcquireCredentialLease(ctx context.Context, credentialID uuid.UUID, attemptRef string) (CredentialLease, []byte, error) {
	var lease CredentialLease
	var env secrets.Envelope
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		// Record the version the lease actually grants (spec §3: the
		// expected secret_version binds at lease acquisition, not earlier) —
		// and capture the envelope from the SAME row read, atomically.
		cred, err := q.GetCredentialRow(ctx, credentialID)
		...
		env = secrets.Envelope{KeyID: cred.KeyID, WrappedDEK: cred.WrappedDek, Ciphertext: cred.Ciphertext}
		row, err := q.AcquireLease(ctx, store.AcquireLeaseParams{
			CredentialID: credentialID, AttemptRef: attemptRef, SecretVersion: cred.SecretVersion,
		})
		if errors.Is(err, pgx.ErrNoRows) { // conflict: held — by us (idempotent) or another attempt (busy)
			existing, err := q.GetLease(ctx, credentialID)
			...
			if existing.AttemptRef == attemptRef {
				// Idempotent re-acquire (a retried attempt): the store may have
				// rotated since the first acquire, so re-bind the lease to the
				// CURRENT version — the snapshot below is what materializes,
				// and lease.secret_version must match it.
				existing, err = q.RebindLeaseVersion(ctx, store.RebindLeaseVersionParams{
					CredentialID: credentialID, AttemptRef: attemptRef, SecretVersion: cred.SecretVersion})
				...
				lease = leaseFromRow(existing)
				return nil
			}
			return xerrors.Wrap(xerrors.ClassConflict, "credential lease held", ErrLeaseBusy)
		}
		...
		lease = leaseFromRow(row)
		return s.rec.Record(ctx, tx, audit.Entry{Action: "credential.lease_acquired",
			SubjectKind: "credential", SubjectID: credentialID.String(),
			Detail: map[string]any{"attempt": attemptRef, "secret_version": lease.SecretVersion}})
	})
	if err != nil {
		return CredentialLease{}, nil, err
	}
	// Unseal outside the tx — the UseCredential precedent (credentials.go:187).
	// The lease-acquired audit row IS the audited use for this open.
	plain, err := s.keys.Open(env, []byte(credentialID.String()))
	...
	return lease, plain, nil
}
```

(`RebindLeaseVersion` is one more `queries.sql` row: `UPDATE credential_leases SET secret_version = $3 WHERE credential_id = $1 AND attempt_ref = $2 RETURNING *` — safe: the caller just proved it is the holder, inside the same tx as the row read.)

`CommitCredentialSync` ordering inside its tx: (1) receipt lookup → `SyncAlreadyApplied`; (2) `GetLease` + match attempt/epoch → else `SyncFenced`; (3) validate `newSecret`'s shape and compute its NEW fingerprint (`validateSecret` + `authFileFingerprint` — before the update, because the update carries it); a shape failure returns a `ClassInvalid` error (permanent — the runner releases on it, never retries); (4) fingerprint pre-check: `SELECT 1 FROM credentials WHERE fingerprint = $new AND id <> $1` — a hit means the refreshed token now belongs to another registered credential → `SyncCASLost` + audit `credential.sync_discarded`, store untouched. The pre-check exists because the alternative — letting the unique index raise 23505 — ABORTS the transaction, and no audit row can be written after that without savepoints; the residual concurrent-commit race still surfaces as 23505 (tx rolls back, error returns, the janitor's retry meets the pre-check next round); (5) seal `newSecret` (envelope, AAD = id — sealing is pure, acceptable in-tx, matching `RotateCredentialSecret`'s precedent); (6) `UpdateCredentialSecretCAS` with `expectVersion` and the step-3 fingerprint — 0 rows → `SyncCASLost` + audit `credential.sync_discarded`; (7) `InsertSyncReceipt{from: expectVersion, to: expectVersion + 1}`; (8) audit `credential.synced`. Audit actions: `credential.lease_acquired`, `credential.lease_released`, `credential.lease_takeover`, `credential.synced`, `credential.sync_discarded`, `credential.sync_quarantined` — all actor-NULL machine paths.

- [ ] **Step 5: Run the matrix** — all 7 green. `task backend:ci` → green.

- [ ] **Step 6: Commit** — `git commit -m "feat(backend): single-flight credential lease aggregate with epoch fencing and sync receipts"`

---

### Task 5: Lease wiring into the runner — acquisition, heartbeat, `credential_busy`, the manifest

**Files:**
- Modify: `backend/internal/runs/runner/runner.go` (ports + `New` signature), `execute.go`
- Create: `backend/internal/runs/runner/manifest.go`
- Modify: `backend/internal/app/runnerports.go` (new `appCredentialLeaser`), `serve.go` (unchanged call, `buildRunner` grows the adapter)
- Test: `backend/internal/runs/runner/lease_test.go` (fake leaser), `backend/internal/app/runner_e2e_runnerintegration_test.go` additions

**Interfaces:**
- Consumes: Task 4's service methods, Task 1's `ExecutionRequest.Credential`.
- Produces:
  - runner ports (assets vocabulary never crosses — plain values only):
    ```go
    type Lease struct{ Epoch, SecretVersion int64 }
    type SyncStatus string // mirrors assets by value
    const (
        SyncStatusApplied        SyncStatus = "applied"
        SyncStatusAlreadyApplied SyncStatus = "already_applied"
        SyncStatusCASLost        SyncStatus = "cas_lost"
        SyncStatusFenced         SyncStatus = "fenced"
    )
    var ErrLeaseBusy = errors.New("runner: credential lease busy")
    type CredentialLeaser interface {
        // Acquire returns the lease AND the secret snapshot it grants — one
        // atomic assets-side read (Task 4): the bytes always correspond to
        // Lease.SecretVersion. Leased kinds never do a separate open.
        Acquire(ctx context.Context, credentialID uuid.UUID, ref string) (Lease, []byte, error) // ErrLeaseBusy when held elsewhere
        Heartbeat(ctx context.Context, credentialID uuid.UUID, ref string, epoch int64) error
        CommitSync(ctx context.Context, credentialID uuid.UUID, ref string, epoch, expectVersion int64, secret []byte) (SyncStatus, error)
        Release(ctx context.Context, credentialID uuid.UUID, ref string, epoch int64) error
        Takeover(ctx context.Context, credentialID uuid.UUID, ref string) (Lease, error)
        LeaseByAttempt(ctx context.Context, ref string) (uuid.UUID, Lease, bool, error)
        MarkAuthExpired(ctx context.Context, credentialID uuid.UUID) error
        RecordQuarantine(ctx context.Context, credentialID uuid.UUID, ref string) error
    }
    ```
  - `runner.New(engine, harnesses, creds, leaser, git, events, opts, log)` — leaser after creds
  - `attemptManifest` + `writeManifest`/`readManifest` (`<DataRoot>/manifests/<ref>.json`, `writeContract`/`readContract` helpers, 128 KiB read bound)
  - const `leaseHeartbeatInterval = 30 * time.Second`

- [ ] **Step 1: Manifest**

`manifest.go`:

```go
// attemptManifest records what reconciliation and adoption need to know
// about the attempt's credential material without resolving anything through
// agent-writable state (spec §2.3). Convenience, not authority — the lease
// row is the recovery authority; this file may be missing after a crash.
// Written for EVERY attempt with a credential binding (all kinds, both
// custodies), not only leased ones: crash adoption needs the credential id,
// kind, and custody to rebuild the redaction set (Task 11) — without a
// manifest it must withhold the attempt's logs and artifacts.
type attemptManifest struct {
	CredentialID  string `json:"credential_id"`
	Kind          string `json:"credential_kind"`
	Custody       string `json:"custody"`
	SecretVersion int64  `json:"secret_version"`        // the version actually delivered — the lease grant (leased) or the versioned open (every other kind). Adoption refuses to publish unless the store still holds THIS version: a mid-attempt operator rotation of ANY kind makes the delivered bytes unrecoverable.
	Epoch         int64  `json:"epoch"`                 // leased kinds only; 0 otherwise
	AuthDir       string `json:"auth_dir,omitempty"`    // relPath under DataRoot
	Identity      string `json:"identity,omitempty"`    // pinned account-identity fingerprint (§2.5)
	BrokerToken   string `json:"broker_token,omitempty"` // brokered custody only — see below
}
```

`BrokerToken` is the one secret the manifest carries, and only for brokered attempts: the per-attempt gateway token is generated in-process and exists nowhere else, so a crashed worker's replacement could never redact it from quarantined logs otherwise (it rides the agent's env, so it can appear there). Persisting it is acceptable where persisting provider credentials is not: the file is 0600, host-only, outside every bind (the agent cannot reach it), and the token is dead the moment the attempt's gateway is gone — it is redaction material, not a live credential. Provider credentials are deliberately NOT serialized — adoption re-opens them from the store (Task 11).

`r.manifestPath(ref) = <DataRoot>/manifests/<ref>.json`; written 0600 — for leased kinds after lease acquisition, otherwise right after `buildSpec` (broker token known) — always before any container exists; deleted only when the credential obligation concludes (Task 9/10). Lives outside `attempts/<ref>` so blanket teardown never touches it (naming.go gets `manifestsDir` beside `logsDir`).

- [ ] **Step 2: Failing fake-leaser tests**

`lease_test.go` (no Docker — a `fakeLeaser` recording calls, plugged into a Runner whose engine is the httptest fake daemon pattern from `dockerapi`; where full Execute is impractical without Docker, test the extracted `acquireCredential(ctx, ref, spec)` helper directly):

1. `agent_auth_file` binding → `Acquire` called with the bound credential id before any engine call; busy → `Execute` returns `OutcomeError{transient, credential_busy}` whose message names the credential id; **no container was created and no usage-bearing work ran**.
2. `subscription_token` binding → `Acquire` never called (static kind, shareable — spec §2 end); the manifest is still written (credential id + kind + custody — adoption's redaction anchor, Task 11).
3. Successful acquire → manifest exists on disk with the granted `SecretVersion`/`Epoch` before the stage container starts, and the materialized bytes are the snapshot `Acquire` returned (no separate open occurred — assert the fake opener was never called for the leased credential).
4. Heartbeats observed at `leaseHeartbeatInterval` while the (scripted) agent runs; heartbeat `ClassConflict` (fence) cancels the attempt's context (the reconciler owns it now — same shape as the phase-lease fenced heartbeat at orchestrator.go:483-501).

- [ ] **Step 3: Implement in `Execute`**

After `buildSpec` (driver known — Task 6 adds custody; until then key off `req.Credential.Kind == "agent_auth_file"`), before `CloneSpec`:

```go
var lease Lease
var leasedSecret []byte
if needsLease(req.Credential) {
	lease, leasedSecret, err = r.leaser.Acquire(ctx, req.Credential.ID, ref.String())
	if errors.Is(err, ErrLeaseBusy) {
		return runs.ExecutionResult{}, transientf(runs.CodeCredentialBusy,
			"credential %s lease held by another attempt", req.Credential.ID)
	}
	if err != nil {
		return runs.ExecutionResult{}, transientf(runs.CodeStagingFailed, "acquire credential lease: %v", err)
	}
	stopHeartbeat := r.startLeaseHeartbeat(ctx, req.Credential.ID, ref, lease.Epoch, cancelAttempt)
	defer stopHeartbeat()
}
if err := r.writeManifest(ref, manifestFor(req, spec, lease)); err != nil { ... } // every credential-bearing attempt
```

`leasedSecret` is the ONLY provider-credential source for a leased kind — the atomic acquire+snapshot is what guarantees the bytes match `lease.SecretVersion` (an operator rotation between acquire and delivery can therefore never make sync-back's CAS discard a legitimate CLI refresh). Non-leased kinds keep the existing `OpenCredential` path.

Lease conclusion (release + manifest/auth cleanup) is NOT deferred here — it belongs to the sync-back sequence (Task 9), which runs on every exit path and must precede release. Until Task 9 lands, conclude with a plain `Release` at the same point Task 9 will replace.

- [ ] **Step 4: App adapter**

`runnerports.go`:

```go
type appCredentialLeaser struct{ svc *assets.Service }

func (a appCredentialLeaser) Acquire(ctx context.Context, id uuid.UUID, ref string) (runner.Lease, []byte, error) {
	l, secret, err := a.svc.AcquireCredentialLease(ctx, id, ref)
	if errors.Is(err, assets.ErrLeaseBusy) {
		return runner.Lease{}, nil, runner.ErrLeaseBusy
	}
	...
	return runner.Lease{Epoch: l.Epoch, SecretVersion: l.SecretVersion}, secret, nil
}
// Heartbeat/CommitSync/Release/Takeover/LeaseByAttempt/MarkAuthExpired/RecordQuarantine: direct translations.
```

`buildRunner` passes it; `runner.New` signature updated everywhere (test constructors included).

- [ ] **Step 5: Spine-level contention proof**

Integration test (runs package, fake executor is not enough — this one uses the real assets service via the app adapters, so it lives in `internal/app` as an integration test): two tasks sharing one `agent_auth_file` harness; hold the lease (acquire directly via assets); intake + run both; assert the runs wait with `credential_busy` retries under the pinned backoff (bounded row creation), `execution_used_ms` stays 0, `chargeable_failures` stays 0; release the lease; both runs complete serially (spec §6 contention test).

- [ ] **Step 6: Gates + commit** — `task backend:ci` green; `git commit -m "feat(backend): credential lease acquisition, heartbeat, and credential_busy in the runner"`

---

### Task 6: The harness driver contract

> **Amendment (user ruling 2026-08-05, task review round 1 — governs over the steps below):** the driver FQDN allowlist joins the gateway `ConnectAllow` for **materialized custody only**. Brokered attempts keep the operator-configured ConnectAllow alone — a brokered agent has no functional need to reach provider hosts directly (its CLI points at the gateway), and the tunnel would be an unmetered exfiltration path to upload-accepting web surfaces.

**Files:**
- Create: `backend/internal/runs/runner/driver.go`, `backend/internal/runs/runner/driver_test.go`
- Modify: `backend/internal/runs/runner/spec.go` (`RunSpec` :30-49, `buildSpec` :54-91; **delete** `providerProfile` :124-144 — the driver table absorbs it), `backend/internal/runs/runner/execute.go` (`Execute` credential block :85-91, `startGateway` :359-400)

**Interfaces:**
- Consumes: Task 1's `req.Credential CredentialBinding`, Task 5's `needsLease`, existing `egress.Config`/`egress.Op`/`brokerURL`, `newBrokerToken`, `r.opts.ConnectAllow`.
- Produces (every later task keys off these exact names):

```go
type custody string

const (
	custodyMaterialized custody = "materialized"
	custodyBrokered     custody = "brokered"
)

type exitRule struct {
	pattern           *regexp.Regexp
	code              string
	class             runs.OutcomeClass
	resetEpochCapture bool // capture group 1 is a unix epoch → Outcome.RetryAt
}

type authMount struct{ containerPath, envVar, file string } // "/caesar-auth", "CODEX_HOME", "auth.json"

type envInput struct {
	custody     custody
	secret      string // materialized only: the opened credential
	brokerToken string // brokered only
}

type brokeredProfile struct {
	header, prefix string
	ops            []egress.Op
}

// driver is the whole per-harness contract in one row (spec §3): custody
// derivation, env delivery, the FQDN allowlist, the broker profile, exit
// mapping, and the sensitive-field/identity extractors for its credential.
type driver struct {
	kind         string
	connectAllow []string
	custodyFor   map[string]custody // credential kind → custody mode
	brokered     brokeredProfile
	env          func(envInput) []string
	exitMap      []exitRule // rows land in Task 7
	sensitive    func(secret []byte) ([][]byte, error)
	identity     func(secret []byte) (string, error) // nil = no identity pin (static kinds)
	authMount    *authMount                          // nil = env-delivered credential
}

func driverFor(kind string) (driver, error)
func (d driver) custodyOf(credentialKind string) (custody, error)
```

**Per-kind applicability rule (pinned):** `sensitive`, `identity`, and `authMount` describe the harness's MATERIALIZED credential kind only; every consumer gates on `custodyOf(credentialKind)` first. A brokered binding (any harness + `model_key`) never routes through them — its delivery is the broker profile, its redaction entry is the raw credential bytes, and it materializes nothing (Task 8's custody gate, Task 11's set construction). Feeding a raw `model_key` to `codexSensitive`/`codexIdentity` is a bug the four-cell driver test exists to catch.

- `RunSpec` gains `Custody custody`; **a materialized spec has `BrokerToken`/`BrokerTokenSHA` empty** (nothing to authenticate — the gateway runs tokenless) and its Env carries no `CAESAR_BROKER_URL`/`CAESAR_BROKER_TOKEN`. `RunSpec` **loses** `ProviderOps`/`CredentialHeader`/`CredentialPrefix` (the gateway reads them off `d.brokered` directly). `ProviderCredential` stays — filled by Execute for both custodies (redaction needs it) but handed to the gateway only when brokered.
- `func (r *Runner) gatewayConfig(spec RunSpec, d driver) egress.Config` — extracted from `startGateway` so the two shapes are unit-testable without Docker.

- [ ] **Step 1: Failing unit tests** (`driver_test.go`, pure unit)

```go
func TestDriver_CustodyDerivation(t *testing.T) {
	cases := []struct {
		harness, credKind string
		want              custody
		wantErr           bool
	}{
		{"claude-code", "subscription_token", custodyMaterialized, false},
		{"claude-code", "model_key", custodyBrokered, false},
		{"codex", "agent_auth_file", custodyMaterialized, false},
		{"codex", "model_key", custodyBrokered, false},
		{"claude-code", "agent_auth_file", "", true}, // wrong pairing, refused
		{"codex", "subscription_token", "", true},
	}
	for _, c := range cases {
		d, err := driverFor(c.harness)
		if err != nil {
			t.Fatalf("driverFor(%s): %v", c.harness, err)
		}
		got, err := d.custodyOf(c.credKind)
		if (err != nil) != c.wantErr || got != c.want { ... }
	}
	if _, err := driverFor("gemini"); err == nil {
		t.Fatal("unknown harness kind must be refused, not defaulted")
	}
}

func TestDriver_EnvComposition(t *testing.T) {
	// claude-code materialized: token + both kill-switches, nothing else
	d, _ := driverFor("claude-code")
	got := d.env(envInput{custody: custodyMaterialized, secret: "sk-ant-oat01-x"})
	want := []string{
		"CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-x",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"DISABLE_AUTOUPDATER=1",
	}
	// claude-code brokered: base URL at the gateway, broker token as the api key
	got = d.env(envInput{custody: custodyBrokered, brokerToken: "cbt1.abc"})
	want = []string{"ANTHROPIC_BASE_URL=http://egress:8080", "ANTHROPIC_API_KEY=cbt1.abc"}
	// codex materialized: only the home redirect — the secret travels via the auth dir
	// codex brokered: OPENAI_BASE_URL=http://egress:8080/v1 + OPENAI_API_KEY=cbt1.abc
}

func TestGatewayConfig_Shapes(t *testing.T) {
	// materialized → open-CONNECT: TokenSHA256 "", zero Provider, ConnectAllow =
	// driver list ∪ opts list. brokered → provider populated from d.brokered +
	// spec.Harness.ProviderEndpoint + spec.ProviderCredential, token digest set.
	// Both assert ConnectAllow contents exactly (order-insensitive).
}
```

- [ ] **Step 2: Run** — `go test ./internal/runs/runner/ -run 'TestDriver_|TestGatewayConfig_' -v` → FAIL (nothing defined).

- [ ] **Step 3: Implement `driver.go`**

```go
var drivers = map[string]driver{
	"claude-code": {
		kind: "claude-code",
		// Verified 2026-08-05 against code.claude.com/docs/en/network-config:
		// api.anthropic.com is the API; claude.ai and platform.claude.com are
		// OAuth token exchange/refresh. Telemetry (Datadog intakes) and the
		// updater are deliberately absent — default-denied, and the env kill-
		// switches below stop the CLI from trying (spec §3).
		connectAllow: []string{"api.anthropic.com", "claude.ai", "platform.claude.com"},
		custodyFor: map[string]custody{
			"subscription_token": custodyMaterialized,
			"model_key":          custodyBrokered,
		},
		brokered: brokeredProfile{header: "x-api-key", ops: []egress.Op{
			{Method: "POST", PathPrefix: "/v1/messages"},
			{Method: "POST", PathPrefix: "/v1/messages/count_tokens"},
			{Method: "GET", PathPrefix: "/v1/models"},
		}},
		env: func(in envInput) []string {
			if in.custody == custodyBrokered {
				return []string{"ANTHROPIC_BASE_URL=" + brokerURL, "ANTHROPIC_API_KEY=" + in.brokerToken}
			}
			return []string{
				"CLAUDE_CODE_OAUTH_TOKEN=" + in.secret,
				"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
				"DISABLE_AUTOUPDATER=1",
			}
		},
		sensitive: func(secret []byte) ([][]byte, error) { return [][]byte{secret}, nil },
		// identity nil: a static token has no read-back and no identity pin.
	},
	"codex": {
		kind: "codex",
		// Verified 2026-08-05 against the Codex CLI source: chatgpt.com is the
		// ChatGPT-auth backend (WebSocket upgrades land there too);
		// auth.openai.com is token refresh — without it a long run dies
		// opaquely mid-refresh (spec §3); api.openai.com covers apiKey-mode
		// auth.json files. ab.chatgpt.com (Statsig telemetry) deliberately absent.
		connectAllow: []string{"chatgpt.com", "auth.openai.com", "api.openai.com"},
		custodyFor: map[string]custody{
			"agent_auth_file": custodyMaterialized,
			"model_key":       custodyBrokered,
		},
		brokered: brokeredProfile{header: "authorization", prefix: "Bearer ", ops: []egress.Op{
			{Method: "POST", PathPrefix: "/v1/responses"},
			{Method: "POST", PathPrefix: "/v1/chat/completions"},
			{Method: "GET", PathPrefix: "/v1/models"},
		}},
		env: func(in envInput) []string {
			if in.custody == custodyBrokered {
				return []string{"OPENAI_BASE_URL=" + brokerURL + "/v1", "OPENAI_API_KEY=" + in.brokerToken}
			}
			return []string{"CODEX_HOME=/caesar-auth"}
		},
		sensitive: codexSensitive,
		identity:  codexIdentity,
		authMount: &authMount{containerPath: "/caesar-auth", envVar: "CODEX_HOME", file: "auth.json"},
	},
}
```

`driverFor` returns a plain error for unknown kinds (callers wrap it `permanentf(runs.CodeHarnessError, ...)` — same posture as the old `providerProfile`: no default, ever). `custodyOf` map-misses likewise.

`codexSensitive`/`codexIdentity` share a runner-local pinned view of `auth.json` (mirrors the assets validator — contexts cannot share code):

```go
// codexAuthFile is the runner's pinned subset of Codex's auth.json. Unknown
// fields are tolerated; the CLI owns the schema (spec §2).
type codexAuthFile struct {
	Tokens struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
	OpenAIAPIKey string `json:"OPENAI_API_KEY"`
}

// codexSensitive returns the file's token fields — the values the redaction
// scan must know (spec §3): id/access/refresh tokens and the API key, empties
// dropped.
func codexSensitive(secret []byte) ([][]byte, error)

// codexIdentity fingerprints the stable account identity (spec §2.5):
// sha256 hex over account_id + "|" + sub + "|" + email, the last two decoded
// from the id_token's payload segment base64url-unverified — this is a
// fingerprint for comparing a file against itself later, not authentication.
// No usable identity material at all is an error (the caller treats the file
// as unscannable).
func codexIdentity(secret []byte) (string, error)
```

- [ ] **Step 4: Run** — the driver tests PASS.

- [ ] **Step 5: Rewire `buildSpec`, `Execute`, `startGateway`**

`buildSpec` (spec.go:54-91): resolve `d, err := driverFor(hs.Kind)` and `cust, err := d.custodyOf(req.Credential.Kind)` first (both errors → `permanentf(runs.CodeHarnessError, ...)`); the env starts from the common allowlist (`CAESAR_ATTEMPT`, `CAESAR_WORKSPACE`, `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`, `HOME`, `PATH`); only the brokered branch mints a broker token and appends `CAESAR_BROKER_URL`, `CAESAR_BROKER_TOKEN`, and `d.env(envInput{custody: custodyBrokered, brokerToken: token})`. Set `Custody: cust`. Update the allowlist comment at spec.go:72 — every addition is explicit in the driver table (Global Constraints).

`Execute` (execute.go:85-91): after `OpenCredential`, keep `spec.ProviderCredential = string(cred)` for both custodies, and append the materialized driver env — it needs the secret, which buildSpec never touches:

```go
if spec.Custody == custodyMaterialized {
	spec.Env = append(spec.Env, d.env(envInput{custody: custodyMaterialized, secret: string(cred)})...)
}
```

(`d` is re-derived in Execute via `driverFor(hs.Kind)` — RunSpec stays plain data.) Task 5's `needsLease` keeps keying off `req.Credential.Kind == "agent_auth_file"` — custody says how a credential is delivered, the kind says whether it is mutable.

`startGateway` (execute.go:359-400): replace the inline `egress.Config` literal with `r.gatewayConfig(spec, d)`:

```go
// gatewayConfig picks the gateway's shape from custody (spec §3). Materialized
// is the open-CONNECT configuration the validation gateway already runs
// (validate.go:139-172): empty token digest, zero Provider — the gateway holds
// nothing worth stealing, and the allowlist is the sole barrier. Brokered
// keeps Plan 06's token-authenticated header-injection unchanged. Both get the
// driver's FQDNs ∪ the operator's config list.
func (r *Runner) gatewayConfig(spec RunSpec, d driver) egress.Config {
	allow := append(slices.Clone(d.connectAllow), r.opts.ConnectAllow...)
	cfg := egress.Config{Attempt: spec.Ref.String(), ConnectAllow: allow}
	if spec.Custody == custodyBrokered {
		cfg.TokenSHA256 = spec.BrokerTokenSHA
		cfg.Provider = egress.Provider{
			Endpoint:         spec.Harness.ProviderEndpoint,
			Credential:       spec.ProviderCredential,
			CredentialHeader: d.brokered.header,
			CredentialPrefix: d.brokered.prefix,
			Ops:              d.brokered.ops,
		}
	}
	return cfg
}
```

(For brokered the driver FQDNs are reachable over CONNECT too — harmless, the agent holds no provider credential to use there, and one composition point beats two shapes.) Delete `providerProfile`; fix every compile error it leaves.

- [ ] **Step 6: Extend the buildSpec tests** — in the existing spec tests: a materialized spec has no `CAESAR_BROKER_TOKEN` in Env and empty `BrokerTokenSHA`; a brokered spec keeps today's exact env. Run `go test ./internal/runs/runner/...` → PASS.

- [ ] **Step 7: Full gate** — `task backend:ci` green; runner lane locally if Docker is up: `go test -race -tags runnerintegration -count=1 -p 1 ./internal/runs/runner/...`.

- [ ] **Step 8: Commit** — `git commit -m "feat(backend): per-harness driver contract with custody modes and pinned allowlists"`

---

### Task 7: The exit→outcome engine and the Claude Code marker table

> **Amendments (user rulings 2026-08-05, task review round 1 — these govern over the pinned snippets below):**
> 1. The **provisional codex rows are narrowed** (still provisional; Task 14's smoke run replaces them with recorded ones): the plain cap row requires the completing phrase — `(?i)(?:usage|rate) limit (?:reached|exceeded)` — and the auth row drops the bare `codex login` alternative. Rationale: the tail is the agent's own stdout; the broad rows can false-match an agent that merely printed those words, and a false `auth_expired` stamps a live credential dead (`MarkAuthExpired`) from untrusted output. The epoch-bearing row Task 13's fakecli consumes is unchanged.
> 2. Step 6.1's pinned `InspectResult{Completed: true, Outcome: &oe.Outcome}` **gains artifacts**: the typed-failure adoption branch populates `Result` with at least the log-ref artifact (worker-path parity), so reconciliation-discovered terminal failures stay diagnosable.

**Files:**
- Modify: `backend/internal/runs/runner/driver.go` (fill `exitMap` for both drivers, add `mapExit`), `backend/internal/runs/runner/collect.go` (exit gate :56-64, `collection` :43-49), `backend/internal/runs/runner/execute.go` (`collectResult` call :103-106), `backend/internal/runs/runner/inspect.go` (adopt path :102-124), `backend/internal/runs/runner/manifest.go` (`attemptManifest` gains `HarnessKind`), `backend/internal/runs/ports.go` (`InspectResult` gains `Outcome`), `backend/internal/runs/orchestrator.go` (`reconcileAttempt` :405-432, `claimedFromRow` :434-445), `backend/internal/runs/store/queries.sql` (the reconciling-claim queries join `runs`)
- Test: `driver_test.go` (marker table), orchestrator reconciliation integration cases, new runnerintegration case in the runner suite

**Interfaces:**
- Consumes: Task 1's outcome codes + `RetryAt`, Task 5's `CredentialLeaser.MarkAuthExpired` and `fakeLeaser`, existing `readTail` (collect.go), `snapshotTailBytes`.
- Produces:
  - `func mapExit(d driver, exitCode int64, tail []byte) runs.Outcome` — exit 0 is never passed in (the caller gates); first matching rule wins; unmatched nonzero → transient `harness_error` (today's collect.go:61-64 semantics as the fallback).
  - `collection` gains `Kind string` (harness kind; empty on adoption without a manifest) and `CredentialID uuid.UUID`.
  - `attemptManifest` gains `HarnessKind string \`json:"harness_kind"\`` (adoption's route back to the driver).
  - `runs.InspectResult` gains `Outcome *Outcome` — an adopted TYPED failure (`rate_limited`, `auth_expired`, …) rides reconciliation instead of degrading to `reconcile_lost`.
  - Claude marker rows (recorded 2026-08-05 from Claude Code v2.1.222; the Task 14 smoke run re-verifies) and **provisional** Codex rows (no recorded corpus yet — the fakecli fixture speaks them, the smoke run replaces them with recorded ones).

- [ ] **Step 1: Failing marker tests**

```go
func TestMapExit_ClaudeMarkers(t *testing.T) {
	d, _ := driverFor("claude-code")
	cases := []struct {
		tail     string
		code     string
		class    runs.OutcomeClass
		retryAt  int64 // 0 = must be nil
	}{
		// recorded 2026-08-05, v2.1.222; re-verified by the Task 14 smoke run
		{"Claude AI usage limit reached|1754398800", runs.CodeRateLimited, runs.OutcomeTransient, 1754398800},
		{"5-hour limit reached ∙ resets 6pm", runs.CodeRateLimited, runs.OutcomeTransient, 0},
		{"Invalid API key · Please run /login", runs.CodeAuthExpired, runs.OutcomePermanent, 0},
		{"OAuth token revoked", runs.CodeAuthExpired, runs.OutcomePermanent, 0},
		{"panic: something else entirely", runs.CodeHarnessError, runs.OutcomeTransient, 0},
	}
	for _, c := range cases {
		out := mapExit(d, 1, []byte(c.tail))
		// assert code, class; RetryAt equals time.Unix(c.retryAt,0) or nil
	}
}

func TestMapExit_OrderingAndFallback(t *testing.T) {
	// a tail matching both the epoch rule and the plain rate-limit rule takes
	// the epoch (first row wins); an unmatched tail with exit 137 reports
	// harness_error mentioning the exit code.
}
```

- [ ] **Step 2: Run** — FAIL (`mapExit` undefined).

- [ ] **Step 3: Implement**

`mapExit` in driver.go:

```go
// mapExit reads meaning into a nonzero exit the only way a harness offers it:
// recorded output markers over the log tail (spec §4). Rules are ordered —
// first match wins — and anything unmatched stays a transient harness_error,
// exactly what collect did before drivers existed.
func mapExit(d driver, exitCode int64, tail []byte) runs.Outcome {
	for _, rule := range d.exitMap {
		m := rule.pattern.FindSubmatch(tail)
		if m == nil {
			continue
		}
		out := runs.Outcome{Class: rule.class, Code: rule.code,
			Message: fmt.Sprintf("harness exited %d: %s", exitCode, rule.pattern.String())}
		if rule.resetEpochCapture && len(m) > 1 {
			if sec, err := strconv.ParseInt(string(m[1]), 10, 64); err == nil {
				at := time.Unix(sec, 0).UTC()
				out.RetryAt = &at // sanity clamping is retryDelay's job (Task 2)
			}
		}
		return out
	}
	return runs.Outcome{Class: runs.OutcomeTransient, Code: runs.CodeHarnessError,
		Message: fmt.Sprintf("harness exited %d", exitCode)}
}
```

The rows:

```go
// claude-code (recorded 2026-08-05, v2.1.222):
exitMap: []exitRule{
	{pattern: regexp.MustCompile(`(?i)usage limit reached\|(\d{9,12})`),
		code: runs.CodeRateLimited, class: runs.OutcomeTransient, resetEpochCapture: true},
	{pattern: regexp.MustCompile(`(?i)(?:5-hour|usage) limit reached`),
		code: runs.CodeRateLimited, class: runs.OutcomeTransient},
	{pattern: regexp.MustCompile(`Invalid API key|OAuth token (?:revoked|expired)|Please run /login`),
		code: runs.CodeAuthExpired, class: runs.OutcomePermanent},
},

// codex (PROVISIONAL — no recorded corpus; the Task 13 fakecli speaks these
// exact markers and the Task 14 smoke run replaces them with recorded ones):
exitMap: []exitRule{
	// Epoch-bearing row FIRST (ordered table): the Task 13 fakecli emits
	// `usage limit reached|<epoch>` and the cap-resume e2e asserts RetryAt
	// from it — mapExit only sets RetryAt when resetEpochCapture is true.
	{pattern: regexp.MustCompile(`(?i)usage limit reached\|(\d{9,12})`),
		code: runs.CodeRateLimited, class: runs.OutcomeTransient, resetEpochCapture: true},
	{pattern: regexp.MustCompile(`(?i)(?:usage|rate) limit`),
		code: runs.CodeRateLimited, class: runs.OutcomeTransient},
	{pattern: regexp.MustCompile(`(?i)401 unauthorized|token .*(?:expired|invalid)|codex login`),
		code: runs.CodeAuthExpired, class: runs.OutcomePermanent},
},
```

The marker unit tests (Step 2) cover the epoch-capture row for BOTH drivers: `usage limit reached|1791234567` under codex must yield `rate_limited` with `RetryAt == time.Unix(1791234567, 0)` — Task 13's cap-resume e2e consumes exactly this rule.

- [ ] **Step 4: Run** — marker tests PASS.

- [ ] **Step 5: Wire the collect gate**

collect.go:56-64 becomes:

```go
if c.ExitCode != 0 {
	tail := readTail(filepath.Join(r.logsDir(c.Ref), agentLogFile), snapshotTailBytes)
	d, derr := driverFor(c.Kind)
	if derr != nil { // adoption without a manifest: no driver knowledge, keep the old answer
		return runs.ExecutionResult{}, transientf(runs.CodeHarnessError,
			"harness exited %d (oom_killed=%v)", c.ExitCode, c.OOM)
	}
	out := mapExit(d, c.ExitCode, tail)
	if out.Code == runs.CodeAuthExpired {
		// Fail fast, naming the credential (spec §4, §5) — and stamp the
		// freshness signal. Best-effort: the outcome is the authority.
		out.Message = fmt.Sprintf("credential %s: %s", c.CredentialID, out.Message)
		if err := r.leaser.MarkAuthExpired(ctx, c.CredentialID); err != nil {
			r.log.Warn("runner.mark_auth_expired", "attempt", c.Ref.String(), "error", err)
		}
	}
	return runs.ExecutionResult{}, runs.OutcomeError{Outcome: out}
}
```

Execute's `collection` literal (:103-106) gains `Kind: spec.Harness.Kind, CredentialID: req.Credential.ID`. The adopt path (inspect.go:102-124) uses the manifest copy `Inspect` captured in memory BEFORE the credential obligation concluded (Task 10 — conclusion deletes the file): present → `Kind: m.HarnessKind, CredentialID: uuid.MustParse-safe m.CredentialID`; absent → zero values (fallback branch above). `manifestFor` (Task 5) now also fills `HarnessKind`.

Inspect's adoption gate (inspect.go:48) drops the `|| insp.State.ExitCode != 0` clause: a finished nonzero-exit attempt with an intact attempt dir IS adoptable — its exit flows through this very collect gate, so a crash after a completed `rate_limited`/`auth_expired` exit maps to its typed outcome instead of degrading to `reconcile_lost` (which would silently charge the failure counter for a cap-hit). The exited-status and attempt-dir conditions stay.

- [ ] **Step 6: Typed outcomes survive reconciliation** (ports.go + inspect.go + orchestrator.go + queries.sql)

Dropping the adoption gate is worthless unless the typed outcome can actually travel: today `InspectResult` carries only completion + artifacts (ports.go:114-120), `adopt` swallows every `collectResult` error (inspect.go:118-121), and `reconcileAttempt` converts anything not adopted into `reconcile_lost` (orchestrator.go:424-428). The plumbing, in order:

1. `InspectResult` gains `Outcome *Outcome`. In `adopt`, a `collectResult` error that `errors.As`-unwraps to `runs.OutcomeError` becomes `InspectResult{Completed: true, Outcome: &oe.Outcome}` — the collect gate's typed exits (`rate_limited` with `RetryAt`, `auth_expired`, quarantine, policy violations) are ADOPTED failures, not lost attempts. Non-outcome errors keep today's empty-result → `reconcile_lost` path.
2. `reconcileAttempt`: when `ins.Completed && ins.Outcome != nil`, evaluate with `OutcomeError{Outcome: *ins.Outcome}` instead of the success arm; `reconcile_lost` remains only for genuinely unadoptable attempts.
3. `claimedFromRow` grows `RunWallDeadline` and `ChargeableFailures`: `MarkPhaseReconciling`, `MarkPhaseReconcilingPastDeadline` (Task 10), and `ClaimStuckReconciling` each gain a scalar `(SELECT r.wall_deadline FROM runs r WHERE r.id = phase_runs.run_id) AS run_wall_deadline` in RETURNING (`task db:sqlc`); `ChargeableFailures` is already on the row. Task 2's step-7 note ("the deadline branch is unreachable on the reconcile path") is now obsolete — a reconciled `rate_limited` is a real uncharged retry and MUST park or terminalize exactly like the worker path; delete the unreachability comment when this step lands.
4. Integration cases (orchestrator suite, fake executor): (a) `Inspect` returns an adopted `rate_limited` outcome with a far `RetryAt` and the run near its wall deadline → the run terminalizes with `rate_limited`, no parked row; (b) adopted `rate_limited` with headroom → parked row with `not_before`, `chargeable_failures` unchanged; (c) adopted `auth_expired` → permanent, run fails naming the credential.

- [ ] **Step 7: Runnerintegration proof** — new case in the runner suite (build tag `runnerintegration`, the caesar-git scripted-agent pattern from testutil): a scripted agent that prints `Claude AI usage limit reached|<epoch>` and exits 1 under a claude-code harness → `Execute` returns an `OutcomeError` with code `rate_limited` and `RetryAt` equal to the epoch; a second script printing `Please run /login` → `auth_expired`, message contains the bound credential id, and the fakeLeaser recorded `MarkAuthExpired` with that id.

- [ ] **Step 8: Gates + commit** — `task backend:ci` green (plus the local runner lane); `git commit -m "feat(backend): exit-to-outcome mapping with claude-code markers and provisional codex rows"`

---

### Task 8: The Codex auth directory — materialize, mount, cleanup fence

**Files:**
- Create: `backend/internal/runs/runner/authdir.go`
- Modify: `backend/internal/runs/runner/naming.go` (`authDir` beside `logsDir`/`manifestsDir`), `backend/internal/runs/runner/spec.go` (`RunSpec` gains `AuthBind string`), `backend/internal/runs/runner/execute.go` (materialization order, `startAgent` :405-426 bind)
- Test: `authdir_test.go` (unit, fakeLeaser), new runnerintegration case

**Interfaces:**
- Consumes: Task 5's lease/manifest wiring, Task 6's `driver.authMount` + `driver.identity`, `r.hostPath` (naming.go — translates DataRoot→DataHostRoot for binds).
- Produces:
  - `func (r *Runner) authDir(ref runs.AttemptRef) string` — `<DataRoot>/auth/<ref>` (outside `attempts/<ref>`: blanket teardown at execute.go:498-508 never touches it — spec §2.4)
  - `func (r *Runner) materializeAuthDir(ref runs.AttemptRef, d driver, secret []byte) error` — dir 0700, `<authDir>/auth.json` 0600
  - `func (r *Runner) removeAuthDirFenced(ctx context.Context, ref runs.AttemptRef, credentialID uuid.UUID, epoch int64) bool` — heartbeat-under-epoch immediately before removal; fenced → skip, return false
  - `RunSpec.AuthBind` — `"<hostPath(authDir)>:/caesar-auth"`, empty for non-authMount drivers

- [ ] **Step 1: Failing unit tests**

```go
func TestRemoveAuthDirFenced_FencedWorkerRelinquishes(t *testing.T) {
	// fakeLeaser whose Heartbeat returns a conflict (epoch advanced by a
	// reconciler): removeAuthDirFenced returns false and the directory plus
	// manifest still exist — the paused worker's deferred cleanup must not
	// destroy the recovery file (spec §2.4, §6 cleanup-fence race).
}

func TestMaterializeAuthDir_Permissions(t *testing.T) {
	// dir is 0700, auth.json is 0600, contents equal the secret bytes.
}
```

- [ ] **Step 2: Run** — FAIL. Implement `authdir.go`:

```go
// removeAuthDirFenced removes the attempt's auth directory and manifest, but
// only after proving this worker still owns the lease at its epoch — a
// heartbeat immediately before removal (spec §2.4). The database fence alone
// cannot stop a paused worker's deferred cleanup: by the time it resumes, a
// reconciler may own the directory. Fenced → relinquish, reconciliation
// cleans up under its own epoch.
func (r *Runner) removeAuthDirFenced(ctx context.Context, ref runs.AttemptRef, credentialID uuid.UUID, epoch int64) bool {
	if err := r.leaser.Heartbeat(ctx, credentialID, ref.String(), epoch); err != nil {
		r.log.Warn("runner.auth_cleanup_fenced", "attempt", ref.String())
		return false
	}
	if err := os.RemoveAll(r.authDir(ref)); err != nil {
		r.log.Warn("runner.auth_dir_remove", "attempt", ref.String(), "error", err)
	}
	if err := os.Remove(r.manifestPath(ref)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		r.log.Warn("runner.manifest_remove", "attempt", ref.String(), "error", err)
	}
	return true
}
```

- [ ] **Step 3: Wire Execute's materialization order**

The credential block reorders to: `buildSpec` → lease acquisition (Task 5 — for leased kinds the acquire's snapshot IS the credential; `OpenCredential` runs only for non-leased kinds, moving up from :87, its error staying transient `staging_failed`) → manifest write, now with `SecretVersion` for EVERY kind (the lease grant, or the versioned open's return — crash adoption's proof obligation), `Identity` (from `d.identity(cred)` when the driver pins one and custody is materialized), `AuthDir: r.relPath(r.authDir(ref))`, and the secret bytes stashed on the in-memory struct (`m.secretBytes`, unexported, never serialized — Task 9's unchanged-check reads it) → `materializeAuthDir` → `spec.AuthBind = r.hostPath(r.authDir(ref)) + ":" + d.authMount.containerPath` — all before `CloneSpec`/networks/stage.

**The auth-dir/identity machinery is gated on CUSTODY, not on the driver having an `authMount`**: the gate is `custodyOf(req.Credential.Kind) == custodyMaterialized && d.authMount != nil`. Codex with a `model_key` credential is brokered — no auth dir, no `CODEX_HOME` bind, no identity pin, no auth-file parsing (a raw API key is not auth.json; `d.sensitive`/`d.identity` must never see it — its redaction entry is the raw credential bytes themselves, added whole in Task 11). "Non-authMount drivers (claude-code): no dir, no bind" remains true, but so does "brokered codex: no dir, no bind". Driver tests (Task 6) cover all four supported cells: claude+subscription_token (materialized, env), claude+model_key (brokered), codex+agent_auth_file (materialized, mount), codex+model_key (brokered, no mount, raw-value sensitive). `startAgent` appends `spec.AuthBind` to its binds when non-empty (dockerapi HostConfig has only `Binds` — the auth dir goes through the same mechanism as the workspace). Non-authMount drivers (claude-code): no dir, no bind, manifest `AuthDir`/`Identity` empty.

- [ ] **Step 4: Runnerintegration proof** — scripted codex-kind agent (caesar-git image, script entrypoint) that (1) writes `sha256sum "$CODEX_HOME/auth.json"` into a workspace file, (2) rewrites `auth.json` with new token values, (3) exits 0. Assert: the workspace file's hash equals the credential bytes' hash (delivery — the value itself never appears in an assertion); after Execute returns, the host-side `<DataRoot>/auth/<ref>/auth.json` carries the mutation (survives container teardown — it feeds Task 9); the run's patch contains the workspace file but no `auth.json` (outside the workspace bind by construction).

- [ ] **Step 5: Gates + commit** — `task backend:ci` + local runner lane green; `git commit -m "feat(backend): host-backed codex auth directory with epoch-fenced cleanup"`

---

### Task 9: Guarded, identity-pinned sync-back on every exit path

**Files:**
- Create: `backend/internal/runs/runner/syncback.go`, `backend/internal/runs/runner/syncback_test.go`
- Modify: `backend/internal/runs/runner/execute.go` (conclusion wiring — replaces Task 5's placeholder `Release`)
- Test: unit crash matrix (fakeLeaser + real files), runnerintegration real-file case

**Interfaces:**
- Consumes: Task 4/5's `CommitSync` statuses, Task 6's `codexSensitive`/`codexIdentity`, Task 8's `removeAuthDirFenced`, the manifest.
- Produces:

```go
const authFileReadLimit = 128 << 10

// syncConclusion is what concluding the credential obligation left behind.
// Status vocabulary (pinned): "clean" (nothing to sync), "synced" (committed),
// "discarded" (CAS lost to an operator rotation), "unscannable" (read-back
// unusable — Task 11 withholds this attempt's logs and artifacts),
// "quarantined" (identity mismatch), "fenced" (a reconciler owns the lease now).
type syncConclusion struct {
	Status             string
	RefreshedSensitive [][]byte      // token fields recovered from the read-back file
	Outcome            *runs.Outcome // set only on quarantine: permanent credential_quarantined
}

func (r *Runner) concludeCredential(ctx context.Context, ref runs.AttemptRef, m attemptManifest, credentialID uuid.UUID) syncConclusion
```

- [ ] **Step 1: Failing crash-matrix unit tests** (`syncback_test.go`, fakeLeaser + a temp DataRoot; spec §6 matrix, file half)

One table, one behavior per row — for each: build the auth dir + manifest fixture, run `concludeCredential`, assert (leaser calls, files, conclusion):

1. **No auth mount** (`m.AuthDir == ""`, subscription_token path): status `clean`, `Release` called once, no `CommitSync`, no read.
2. **Missing file** (dir exists, no auth.json — crash before the CLI ever wrote): status `unscannable`, store untouched (no `CommitSync`), `Release` called, **auth dir + manifest retained** (evidence; the Task 10 orphan sweep reaps them once the lease is gone).
3. **Malformed file** (`{"tokens": {"acc` — the §2.7 torn-write window): same as 2.
4. **Oversize or non-regular file** (> `authFileReadLimit`, or a fifo): same as 2 — a bounded read of a regular file is the only read there is.
5. **Unchanged bytes** (file equals the materialized secret): status `clean`, no `CommitSync`, `removeAuthDirFenced` ran (dir gone), `Release` called.
6. **Refreshed, same identity**: `CommitSync(credentialID, ref, m.Epoch, m.SecretVersion, newBytes)` called; fake returns `applied` → status `synced`, cleanup + `Release`. Fake returns `already_applied` (commit-then-crash retry) → same conclusion, no double anything.
7. **CAS lost** (operator rotated mid-attempt): status `discarded`, cleanup + `Release` — the stale snapshot dies quietly (audited assets-side, Task 4).
8. **Fenced** (`CommitSync` → `fenced`): status `fenced`, **no Release, no auth cleanup** — the reconciler owns both now (spec §2.1/§2.4).
9. **Identity mismatch** (well-formed file, different `account_id`/`sub`): `RecordQuarantine(credentialID, ref)` called, **no CommitSync**, status `quarantined`, `Outcome` = permanent `credential_quarantined` naming the credential, `RefreshedSensitive` carries the *poisoned* file's tokens too (they may be live secrets of some account — redact them anyway), auth dir retained, `Release` called.
10. **Epoch-fence race** (spec §6): conclusion built with `m.Epoch = 1` while the fake's current epoch is 2 → every path lands in 8's behavior; the paused worker can commit nothing and deletes nothing.
11. **Dead parent context** (the wall-deadline exit): call `concludeCredential` with an already-cancelled/expired ctx over row 6's refreshed-file fixture → the fake still records `CommitSync` and `Release` (the bounded `WithoutCancel` derivation is load-bearing); status `synced`.
12. **Shape-invalid but well-formed** (the lease-livelock trap): valid JSON, SAME identity, but `tokens.refresh_token` empty/missing → NO `CommitSync` attempted (the read gate refuses it), status `unscannable`, store untouched, **`Release` called** — and a follow-up `Acquire` on the fake succeeds (no eternal `credential_busy`). Same terminal behavior when the fake's `CommitSync` returns a `ClassInvalid` error directly (defense in depth for validation drift between runner and assets).

- [ ] **Step 2: Run** — FAIL. Implement `syncback.go` in exactly that order:

```go
// concludeCredential is the single choke point where an attempt's credential
// obligation ends (spec §2.5, §2.6). Every exit path — success, failure,
// enforcement kill, reconciliation, adoption — funnels through here; nothing
// else releases the lease or touches the auth directory.
//
// It runs under its own bounded, UNCANCELLED context: the most important
// caller is exactly the one whose ctx is already dead (the wall-deadline
// exit — orchestrator.go passes a deadline-bound execCtx into Execute), and
// a conclusion that fails on `context deadline exceeded` would strand every
// deadline-exit lease for the janitor. Same pattern as teardown
// (execute.go:498-500).
func (r *Runner) concludeCredential(ctx context.Context, ref runs.AttemptRef, m attemptManifest, credentialID uuid.UUID) syncConclusion {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), credentialConcludeTimeout) // pinned: 30s
	defer cancel()
	release := func() {
		if err := r.leaser.Release(ctx, credentialID, ref.String(), m.Epoch); err != nil {
			r.log.Warn("runner.lease_release", "attempt", ref.String(), "error", err)
		}
	}
	if m.AuthDir == "" { // env-delivered credential: nothing mutable to guard
		release()
		return syncConclusion{Status: "clean"}
	}
	raw, ok := r.readAuthFile(ref) // Lstat regular + size ≤ authFileReadLimit + json.Valid + the FULL pinned commit shape (non-empty tokens.refresh_token — assets' validateSecret would refuse it, and a commit that is doomed to a permanent validation error must never be attempted: the janitor would deterministically retry the same invalid file forever, holding the lease as an eternal credential_busy)
	if !ok {
		release() // store preserved; the credential may be dead (§2.7 accepted loss window)
		return syncConclusion{Status: "unscannable"}
	}
	sens, _ := codexSensitive(raw) // best-effort even before identity: these bytes must never publish
	if bytes.Equal(raw, m.secretBytes) { ... clean: fenced-remove + release ... }
	id, err := codexIdentity(raw)
	if err != nil { ... unscannable path (identity unreadable = no complete scan) ... }
	if id != m.Identity {
		// Agent-authored substitution (spec §2.5): preserve the store, type the
		// outcome, audit — never commit.
		_ = r.leaser.RecordQuarantine(ctx, credentialID, ref.String())
		release()
		return syncConclusion{Status: "quarantined", RefreshedSensitive: sens, Outcome: &runs.Outcome{
			Class: runs.OutcomePermanent, Code: runs.CodeCredentialQuarantined,
			Message: fmt.Sprintf("credential %s: read-back identity mismatch; stored credential preserved", credentialID)}}
	}
	status, err := r.leaser.CommitSync(ctx, credentialID, ref.String(), m.Epoch, m.SecretVersion, raw)
	switch {
	case xerrors.IsClass(err, xerrors.ClassInvalid): // permanent: retrying the same bytes can never succeed
		release() // store preserved, evidence retained — the no-release path is ONLY for fencing/infra uncertainty
		return syncConclusion{Status: "unscannable", RefreshedSensitive: sens}
	case err != nil: ... log; treat as fenced-shaped: no release, no cleanup (retryable by reconciliation) ...
	case status == SyncStatusFenced:
		return syncConclusion{Status: "fenced", RefreshedSensitive: sens}
	}
	r.removeAuthDirFenced(ctx, ref, credentialID, m.Epoch)
	release()
	...
}
```

Detail pinned here: the materialized secret's bytes needed for the unchanged-check ride the manifest write path in memory (`m.secretBytes`, unexported, never serialized — the manifest file carries only the version and identity); reconciliation paths that lack them (Task 10) skip the unchanged-shortcut and go straight to identity+CAS, which is correct — `already_applied`/CAS handle it.

- [ ] **Step 3: Wire Execute** — replace Task 5's placeholder `Release`. Concretely: after `runAgentToCompletion` returns (both arms), and before any result/snapshot assembly, run `conc := r.concludeCredential(...)` when a lease was acquired; if `conc.Outcome != nil`, the attempt's result becomes `r.failureResult(...)` with `runs.OutcomeError{Outcome: *conc.Outcome}` — quarantine outranks whatever the agent exited with. An UNRESOLVED conclusion — status `fenced`, or the commit erred with infra uncertainty — also overrides: the attempt returns transient `credential_busy` naming the credential (nothing charged), because a success may not be reported while its sync obligation is pending (the spec's receipt-before-terminal-result ordering); the lease stays held, the stale-lease janitor settles it, and the retried attempt re-acquires idempotently. Priority: quarantine > unresolved-sync > the agent's own outcome. Early exits between lease acquisition and agent start (clone/stage/network failures) conclude through a small deferred once-guard so no path leaks a lease:

```go
concluded := false
conclude := func() syncConclusion {
	concluded = true
	return r.concludeCredential(ctx, ref, manifest, req.Credential.ID)
}
defer func() {
	if leased && !concluded {
		conclude() // early failure: agent never ran, auth.json unchanged → clean/release
	}
}()
```

- [ ] **Step 4: Run the matrix** — all rows green.

- [ ] **Step 5: Runnerintegration real-file proof** — Task 8's mutating scripted agent, fakeLeaser: after Execute, the fake recorded `CommitSync` with the mutated bytes, `m.Epoch`, and the acquired `SecretVersion`; the auth dir is gone; exactly one `Release`. Second case: fake advances its epoch mid-run (simulated takeover) → `CommitSync` answers `fenced` → auth dir still on disk, no `Release` recorded.

- [ ] **Step 6: Gates + commit** — `task backend:ci` + runner lane green; `git commit -m "feat(backend): guarded identity-pinned credential sync-back on every exit path"`

---

### Task 10: Reconciliation and terminalization never skip the credential obligation

**Files:**
- Modify: `backend/internal/runs/ports.go` (`ParseAttemptRef`, `CredentialReconciler`, `StaleLease` + orchestrator port), `backend/internal/runs/orchestrator.go` (deadline sweep ordering, stale-lease janitor tick), `backend/internal/runs/runner/inspect.go` (:22-53, adopt :102-124), `backend/internal/runs/runner/sweep.go` (`sweepAttemptDirs` :111-134), `backend/internal/runs/runner/runner.go` (Runner implements `ReconcileCredential`), `backend/internal/assets/lease.go` (`ListStaleCredentialLeases`), `backend/internal/app/runnerports.go` + `runsports.go` + `serve.go` (adapters + wiring)
- Test: `backend/internal/runs/ports_test.go` (ref parsing), `backend/internal/app/credential_lifecycle_runnerintegration_test.go` (new)

**Interfaces:**
- Consumes: Task 5's `LeaseByAttempt`/`Takeover`, Task 9's `concludeCredential`, Task 8's `removeAuthDirFenced`, assets' `ListStaleLeases` query (Task 4).
- Produces:
  - `func runs.ParseAttemptRef(s string) (AttemptRef, error)` — inverse of `AttemptRef.String()`; the run id is a fixed-width uuid and the attempt is the digits after the last dash, so node ids containing dashes parse correctly.
  - `runs.CredentialReconciler interface { ReconcileCredential(ctx context.Context, ref AttemptRef) error }` — implemented by the Runner; `fake` gets a no-op.
  - `runs.StaleLease{CredentialID uuid.UUID; AttemptRef string}` and the orchestrator port `StaleLeaseSource interface { StaleCredentialLeases(ctx context.Context, olderThan time.Duration) ([]StaleLease, error) }` (app adapts it over assets).
  - `assets.ListStaleCredentialLeases(ctx context.Context, olderThan time.Duration) ([]CredentialLease, error)`.
  - Pinned: `staleLeaseAfter = 5 * time.Minute` (10× the heartbeat interval) in the orchestrator.

- [ ] **Step 1: `ParseAttemptRef` — failing test, implement, pass**

```go
func TestParseAttemptRef_RoundTrip(t *testing.T) {
	ref := AttemptRef{RunID: uuid.New(), NodeID: "review-and-fix", Attempt: 3} // dashes in the node id
	got, err := ParseAttemptRef(ref.String())
	// got == ref; also: garbage, missing prefix, non-numeric attempt → error
}
```

- [ ] **Step 2: Inspect learns the credential obligation** (inspect.go)

Restructure `Inspect` around one shared helper so the janitor path (below) reuses it:

```go
// credentialReconciliation is everything settling the obligation learned —
// adoption (Task 11) rebuilds its redaction set from this, so it is captured
// BEFORE conclusion deletes the on-disk manifest or bumps the store version.
type credentialReconciliation struct {
	Manifest   attemptManifest // in-memory copy; zero-valued when absent
	HasManifest bool
	Stored     []byte // stored credential bytes, opened pre-commit; nil if open failed
	StoredOK   bool   // open succeeded AND (for leased kinds) at the lease-granted version
	Concluded  bool   // a lease existed and concludeCredential ran
	Conc       syncConclusion
}

// takeoverCredential is stage 1: fence the paused owner (spec §2.1, §2.3 —
// takeover FIRST, before that owner can race its own commit or cleanup).
// Fencing the WORKER is all it does: the epoch stops the old process's
// database writes, not the container's file writes — the runtime must be
// provably stopped by the caller before settleCredential may read auth.json.
func (r *Runner) takeoverCredential(ctx context.Context, ref runs.AttemptRef) (credID uuid.UUID, lease Lease, held bool, err error)

// settleCredential is stage 3: capture the redaction inputs, then conclude.
// PRECONDITION (each caller's to prove, tested): the attempt's containers are
// gone or exited — a still-running agent could write auth.json between the
// read-back and the commit, and a released lease would let a second attempt
// acquire while the first still runs.
func (r *Runner) settleCredential(ctx context.Context, ref runs.AttemptRef, credID uuid.UUID, lease Lease, held bool) credentialReconciliation {
	var rec credentialReconciliation
	// Manifest read is NOT gated on the lease: static kinds (subscription_token)
	// write one with no lease behind it, and adoption needs it either way.
	rec.Manifest, rec.HasManifest = r.readManifest(ref)
	if rec.HasManifest {
		// Open the stored credential BEFORE any CommitSync bumps the version:
		// these are the only recoverable copy of the initially-materialized
		// bytes, and adoption may not publish logs it cannot scan for them.
		if b, v, oerr := r.creds.OpenCredential(ctx, mustID(rec.Manifest.CredentialID), "reconcile:"+ref.String()); oerr == nil {
			// The manifest's version is what was DELIVERED (every kind records
			// it — Task 5); the store must still hold it, or the delivered
			// bytes are unrecoverable and adoption must withhold. `!held` is
			// no exemption: a rotated subscription_token's old value entered
			// the crashed attempt's env just the same.
			rec.Stored, rec.StoredOK = b, v == rec.Manifest.SecretVersion
		}
	}
	if !held {
		return rec // no lease: nothing to conclude (auth-dir leftovers age into the orphan sweep)
	}
	m := rec.Manifest // conclusion works on a copy; a missing manifest concludes as "clean"
	// (crash between lease commit and manifest write, spec §2.3 — the lease
	// row is the recovery authority; release, never loop on credential_busy)
	m.Epoch = lease.Epoch                 // conclude under the taken-over epoch
	m.SecretVersion = lease.SecretVersion // the version the lease actually granted
	rec.Conc = r.concludeCredential(ctx, ref, m, credID)
	rec.Concluded = true
	r.log.Info("runner.credential_reconciled", "attempt", ref.String(), "status", rec.Conc.Status)
	return rec
}
```

(`OpenCredential` on the runner's existing `CredentialOpener` port grows a version return — `(secret []byte, version int64, err error)` — backed by a new `assets.UseCredentialVersioned` that is `UseCredential` returning `row.SecretVersion` alongside; the two existing call sites adapt trivially.)

The two callers sequence the stages around their own runtime handling — the kill always sits between takeover and settlement:

- **`Inspect`** (:22-53): (1) `takeoverCredential`; (2) the EXISTING inspect/kill flow unchanged — `ContainerInspect`, `killThenInspect` + corpse wait when running (this preserves the exit code adoption needs; a force-remove here would destroy it); (3) `settleCredential`; (4) adopt using `rec.Manifest` (the pre-conclusion in-memory copy — the file may already be deleted) for `collection.Kind`/`CredentialID` (Task 7), with `rec.Stored`/`rec.Conc.RefreshedSensitive` feeding the redaction set (Task 11); (5) teardown as today.
- **`ReconcileCredential(ctx, ref)`** — the `runs.CredentialReconciler` implementation (janitor entry, no adoption): `takeoverCredential` → `removeByName(agent, gateway)` (force-remove kills before returning; nothing to adopt, so losing the corpse is fine) → `settleCredential`, discarding the reconciliation value.

Unit test the precondition, not just the happy path: with a fakeLeaser and a fake engine whose agent container reports `running`, `Inspect` must record `Takeover` BEFORE any kill, and `CommitSync`/`Release` only AFTER the engine observed the kill + wait; and an already-exited nonzero (typed `rate_limited`) attempt stays adoptable after settlement.

- [ ] **Step 3: Deadline sweep reconciles before it terminalizes** (orchestrator.go + queries.sql)

In the janitor's expired-run sweep (`sweepExpiredRuns`), before `terminalizeRun`: for each of the run's live phases, (1) `MarkPhaseReconcilingPastDeadline` (below — fences the phase so a still-live worker's own evaluate loses its `FinishPhase` CAS), then (2) `executor.(CredentialReconciler).ReconcileCredential(ctx, ref)` — the CLEANUP-ONLY entry from Step 2: takeover, stop runtime, sync-back with its receipt, release. **Never `reconcileAttempt` here**: full reconciliation ADOPTS a completed attempt and can evaluate it as success — `FinishRun` carries no deadline predicate, so an exit-0 attempt reconciled after the wall deadline would mark the run succeeded and the terminalization would silently lose its CAS. Past the deadline the run's outcome is already decided (`wall_deadline_exceeded`); only the credential obligation is still owed (spec §2.8: sync, release, and only then terminalize). Only then terminalize. A reconciliation error logs and **skips terminalizing that run this tick** — the run stays matched by the expired-run sweep's predicate and is retried next tick, terminalizing only once every live phase's credential obligation has concluded. Terminalize-anyway would make the spec's ordering violation durable (sync receipt and lease release BEFORE the terminal event — spec §2.8; `terminalizeRun` finishes the run and supersedes all live phases immediately, and `FinishRun` has no deadline predicate to catch it later). A run stuck this way is visible (repeated `runner.credential_reconciled` errors in logs) rather than silently mis-ordered.

The existing `MarkPhaseReconciling` CANNOT serve this path: its guard is `leased_until <= now()` (queries.sql:116-118), and a run's wall deadline can expire while the phase's worker still heartbeats a future-dated lease (the worker's own deadline exit and the sweep race). Add a deadline-specific fenced transition, `task db:sqlc` after:

```sql
-- name: MarkPhaseReconcilingPastDeadline :one
-- The deadline sweep's variant of MarkPhaseReconciling: fenced by the RUN's
-- expired wall deadline (db clock) instead of the phase lease — a live lease
-- must not shield a phase whose run is already past its deadline (spec §2.8).
UPDATE phase_runs SET status = 'reconciling'
WHERE id = $1 AND status = 'running'
  AND EXISTS (SELECT 1 FROM runs r WHERE r.id = phase_runs.run_id AND r.wall_deadline <= now())
RETURNING *;
```

- [ ] **Step 4: Stale-lease janitor** (orchestrator.go + adapters)

On the janitor tick: `StaleCredentialLeases(ctx, staleLeaseAfter)`; for each, `ParseAttemptRef`; look up the phase — if it is **not** live (terminal row, or no claim), call `executor.(CredentialReconciler).ReconcileCredential(ctx, ref)` (type-asserted once at construction; the fake executor's is a no-op). A live phase is skipped: its own heartbeat/reconciliation machinery owns it. assets side:

```go
// ListStaleCredentialLeases surfaces leases whose owner stopped heartbeating
// (spec §2.8): expiry alone never breaks single-flight — these are handed to
// reconciliation, never to a new acquirer.
func (s *Service) ListStaleCredentialLeases(ctx context.Context, olderThan time.Duration) ([]CredentialLease, error)
```

app: `runsAssetSource` (or a sibling) adapts it to `[]runs.StaleLease`; `serve.go` wires the port.

- [ ] **Step 5: Orphan sweep covers auth dirs and manifests** (sweep.go)

`sweepAttemptDirs` grows two siblings: entries under `<DataRoot>/auth/` and `<DataRoot>/manifests/` older than `orphanMaxAge` (25h) are removed **only when** `LeaseByAttempt` reports no lease for the ref — a retained-as-evidence dir from an unscannable/quarantined conclusion outlives its lease and ages out here; anything lease-held belongs to reconciliation, however old.

- [ ] **Step 6: Lifecycle proof** (`credential_lifecycle_runnerintegration_test.go`, app-level, real Postgres + Docker + scripted caesar-git agents)

1. **Restart after deadline mid-outage** (spec §6 lifecycle): an `agent_auth_file` run whose scripted agent mutates auth.json then hangs; short wall deadline; cancel the orchestrator mid-run; restart a fresh one after the deadline passed. Assert the order: agent container gone, `credential_sync_receipts` row exists (the mutation landed), lease released, and the run's terminal event is stamped **after** the `credential.synced` audit row. `secret_version` is 2.
2. **Crash between lease and manifest** (spec §6 crash injection): acquire a lease directly via assets under an attempt ref whose phase is already terminal; no manifest, no container. Run the janitor tick. The lease is released; a new run on the same credential proceeds immediately (no `credential_busy` loop).
3. **Stale-lease + live phase**: a lease whose heartbeat is backdated but whose phase is mid-run → the janitor leaves it alone.
4. **Deadline beats a healthy phase lease**: SQL-backdate the run's `wall_deadline` to the past while the phase row's `leased_until` is still future-dated → the sweep still moves the phase to `reconciling` (`MarkPhaseReconcilingPastDeadline`), reconciles the credential obligation, and terminalizes — the lease guard on plain `MarkPhaseReconciling` must not shield it.
5. **Completed exit-0 after the deadline is NOT adopted as success**: agent finishes cleanly (container exited 0, auth.json mutated) but the run's `wall_deadline` passes before any reconciliation; the deadline sweep runs. Assert: the sync receipt exists and the lease is released (the credential obligation landed), but NO success event and NO artifact row was persisted for the phase, and the run terminal outcome is `wall_deadline_exceeded` — the sweep's cleanup-only path never routes through adoption.

- [ ] **Step 7: Gates + commit** — `task backend:ci` + runner lane green; `git commit -m "feat(backend): credential-aware reconciliation ordering and stale-lease janitor"`

---

### Task 11: Redaction — canonical-bytes scans, quarantined logs, withhold-on-unscannable

> **Amendments (user rulings + implementation deviations 2026-08-05, task review round 1 — these govern over the pinned snippets below):**
> 1. **Adoption mirrors Execute's outcome priority: quarantine > unresolved sync > agent outcome.** A permanent `credential_quarantined` in the reconciliation's conclusion outranks the exit-derived outcome on the adoption path, exactly as it does on the live path. One vocabulary, one semantics — and a quarantined credential must never be reported as a transient `harness_error`, which would schedule a retry doomed to meet the same broken credential. Pinned by `TestAdopt_AQuarantinedCredentialOutranksTheExitDerivedOutcome`.
> 2. **The 16-byte floor applies to driver-extracted fields only.** The raw provider credential and the per-attempt broker token enter the redaction set at any length (`sensitiveSet.addRaw`); the floor exists for values parsed OUT of an auth file (`"auth_mode":"chatgpt"`, an account slug), never for the credential itself — which also restores the unconditional snapshot redaction the pre-Task-11 `attemptSecrets` gave. A raw value short enough that the floor would have dropped it is logged (`runner.short_credential`): a 4-byte credential redacting everywhere is a real patch-poisoning risk, but it is operator-caused and must be visible rather than silent.
> 3. **A `d.sensitive` failure withholds on the live path too.** Extraction failure is an INCOMPLETE scan, not a coarser one: the set holds the whole auth file, so a log line printing only the access token is not a substring match. Execute now sets the withhold flag on that error, matching `adoptionSensitive` — the two paths may not disagree about what counts as a complete scan. Pinned by `TestExecute_AFieldListTheDriverCannotExtractWithholdsEverything`.
> 4. **Docker's `State.Error` stays in the withheld failure snapshot**, against the step's literal four-field enumeration: the withheld snapshot is the operator's only evidence on that path, and the daemon's error text about caesar-authored container config carries no agent-chosen bytes.
> 5. **Deviation — `agentTail` prefers the quarantined copy and falls back to the published one.** The steps pin both "`mapExit` reads the quarantine" and "publish before collect", but publication deletes the quarantine; the fallback resolves the contradiction. It cannot read unscanned bytes: `publishLogs` is the only writer of `<logsDir>/agent.log`, so anything the fallback finds was already redacted against a complete set.
> 6. **Two off-brief additions.** (a) `sweepAttemptDirs` became `sweepStaleDirs(root, cutoff)` and now sweeps `quarantine/` as well as `attempts/` — a withheld attempt leaves a quarantine dir that can never be published later, so nothing else would ever reclaim it. (b) The `syncConclusion.Status` string literals became the `conc*` constants, because the withhold rule now branches on two of them from a second file.

**Files:**
- Create: `backend/internal/runs/runner/redact.go`, `backend/internal/runs/runner/redact_test.go`
- Modify: `backend/internal/runs/runner/naming.go` (`quarantineDir`), `backend/internal/runs/runner/logs.go` (stream targets), `backend/internal/runs/runner/execute.go` (ordering: read-back → publish → snapshot → collect; `attemptSecrets` :147-149 grows), `backend/internal/runs/runner/collect.go` (patch + declared-artifact scans, withhold, log_ref), `backend/internal/runs/runner/inspect.go` (adoption publishes too)
- Test: precision suite (spec §6), one runnerintegration case

**Interfaces:**
- Consumes: Task 6's `driver.sensitive`, Task 9's `syncConclusion.RefreshedSensitive`/`Status`, existing `redactAll` (snapshot.go:158-166), `logRefArtifact`, `egressUsage`.
- Produces:

```go
const (
	minSecretLen   = 16 // exact-match floor (spec §3): benign short values in auth.json must not poison legitimate patches
	redactedMarker = "[caesar:redacted]"
)

// sensitiveSet is the attempt's exact-bytes redaction list: broker token,
// provider credential, the driver's sensitive fields — initial AND refreshed
// (a mid-run-refreshed token is unknown until read-back, spec §3).
type sensitiveSet struct{ values [][]byte }

func newSensitiveSet(values ...[]byte) *sensitiveSet // drops values shorter than minSecretLen
func (s *sensitiveSet) add(values ...[]byte)
func (s *sensitiveSet) scan(b []byte) bool
func (s *sensitiveSet) redact(b []byte) []byte

// publishLogs redacts the quarantined streams and atomically publishes them
// (temp file + rename) into logsDir; the quarantine dir is removed after.
// Idempotent: an empty or missing quarantine publishes nothing.
func (r *Runner) publishLogs(ref runs.AttemptRef, set *sensitiveSet) error
```

- `collection` gains `Sensitive *sensitiveSet` and `Unscannable bool`.
- **Pinned decision (flag if pushed back):** an unscannable read-back on an otherwise-successful attempt with a changed workspace fails the attempt permanently with `invalid_output` — the alternative is a "success" whose patch and logs silently never appear. Kill/crash paths (the common §2.7 case) already carry their own outcome.

- [ ] **Step 1: Failing precision tests** (`redact_test.go` — spec §6's list, verbatim)

1. **The literal `chatgpt` persists**: a set built from realistic tokens; a patch diff containing `auth_mode: "chatgpt"` and a timestamp → `scan` false. (The per-kind sensitive-field list plus the length floor is the whole precision mechanism.)
2. **Floor**: an 8-byte "secret" is never added; `redact` leaves it alone.
3. **Mid-run-refreshed token in logs**: quarantine file contains `rt-refreshed-<40 bytes>`; the set is extended from `RefreshedSensitive` after read-back; `publishLogs` → published file carries `redactedMarker`, quarantine dir gone; byte-identical second call is a no-op.
4. **Canonical bytes** (the regression the spec calls out): plant a raw token in `rep.Diff` bytes; assert `scan(rep.Diff)` hits — and assert `scan([]byte(base64.StdEncoding.EncodeToString(rep.Diff)))` does NOT, proving the scan must run pre-encoding, where collect.go:87 encodes.
5. **Withhold-on-unscannable**: conclusion status `unscannable` → no file lands in `logsDir`, quarantine retained, and collect (step 3 below) emits no patch, no declared artifacts, no log_ref. The failure snapshot carries container metadata only: seed the workspace with a file whose NAME is the refreshed token (which the test knows but the runner, post-garble, cannot) → that byte-sequence appears nowhere in the snapshot map or any persisted artifact.
6. **Crash adoption rebuilds the set or withholds** (unit, against the adoption set-reconstruction helper — one case per custody combo): (a) claude `subscription_token`: quarantine contains the token; reconstruction from manifest + stored bytes → published log carries `redactedMarker`, not the token; (b) codex `agent_auth_file`: initial token in quarantine, refreshed token in `RefreshedSensitive` → both redacted; (c) brokered `model_key`: broker token from `rec.Manifest.BrokerToken` redacted; (d) manifest missing on a credential-bearing attempt → withheld; (e) `StoredOK == false` (operator rotated mid-attempt — EVERY kind, including a static `subscription_token`: the old value entered the crashed attempt's env and only version equality proves the store still holds it) → withheld.

- [ ] **Step 2: Implement `redact.go`** — exact-bytes `bytes.Contains`/`bytes.ReplaceAll` over the value list; `publishLogs` reads each `<quarantineDir>/<file>` (they are already capped by `LogCapBytes`), redacts, writes `<logsDir>/.tmp-<file>`, renames, then `os.RemoveAll(quarantineDir)`.

- [ ] **Step 3: Rewire the pipeline**

- `logs.go`: `streamLogs` call sites target `r.quarantineDir(ref)` (naming.go sibling of `logsDir`) for **every** attempt — publication is what moves bytes into `logsDir`, for materialized and brokered alike. The gateway stream is included (it should hold no secrets; scanned anyway — the broker token transits it).
- Task 7's collect gate re-targets its tail: `mapExit` now reads the QUARANTINE copy (`readTail(quarantineDir, agentLogFile)`), not `logsDir` — classification and publication are different questions. The raw tail is consumed in memory for outcome typing only and never persisted or surfaced (`Outcome.Message` carries marker names, not tail bytes). Withholding suppresses logs and artifacts, NEVER the typed outcome: a torn read-back coinciding with a cap-hit or auth-death exit (the routine §2.7 window) must still classify `rate_limited`/`auth_expired`, not fall back to `harness_error` off an empty published dir.
- `execute.go`: build `set := newSensitiveSet(spec.BrokerToken bytes, spec.ProviderCredential bytes, d.sensitive(cred)...)` once the credential bytes are in hand (the acquire snapshot for leased kinds, `OpenCredential` otherwise — Task 5/8; for a brokered binding `d.sensitive` is not called — the raw credential bytes enter the set whole, Task 6's per-kind rule); `attemptSecrets` for the snapshot's string-level `redactAll` is derived from the same set — one source of truth. Ordering after the agent finishes: **conclude (Task 9) → `set.add(conc.RefreshedSensitive...)` → publish or withhold → snapshot/collect**. Concretely: `failureResult` and `collectResult` are only reached after `publishLogs`; on `unscannable`, skip `publishLogs`, and `failureResult`'s snapshot is stripped to CONTAINER METADATA ONLY — exit code, oom, status, timestamps; no `log_tail`, no `workspace` listing, no log_ref. The workspace listing is agent-controlled bytes (snapshot.go:53 includes filenames), and unscannable means the refreshed value is UNKNOWN — `redactAll` cannot redact a value nobody holds, so a filename embedding the refreshed token would ride the snapshot into the store. Workspace-derived means every byte the agent could influence, not just the patch (baked decision 10).
- `collect.go`: between `extractPatch` (:66) and the payload build (:87):

```go
if c.Unscannable {
	// §2.7: the refreshed sensitive values are unknowable — no complete scan
	// exists, so nothing workspace-derived may publish.
	if rep.Changed {
		return runs.ExecutionResult{}, permanentf(runs.CodeInvalidOutput,
			"auth read-back unscannable: this attempt's logs and artifacts are withheld (spec §2.7)")
	}
	return runs.ExecutionResult{Artifacts: nil}, nil
}
if c.Sensitive.scan(rep.Diff) {
	return runs.ExecutionResult{}, runs.OutcomeError{Outcome: runs.Outcome{
		Class: runs.OutcomePolicyViolation, Code: runs.CodeSecretInArtifact,
		Message: "a sensitive value reached the extracted patch; the artifact was not persisted (spec §3)"}}
}
```

  and each declared artifact's payload is scanned as its marshalled bytes before acceptance (same outcome on a hit). `logRefArtifact` is unchanged — it already reads `logsDir`, which now only ever holds published (redacted) bytes; `egressUsage` likewise.
- `inspect.go`: adoption may only publish what it can PROVE it scanned completely — the dead worker's in-memory set is gone, so it is rebuilt from trusted state or the logs stay withheld. The reconstruction, per credential-bearing attempt (all inputs come from Task 10's `credentialReconciliation`, captured before conclusion):
  - `rec.Manifest` names the credential (id, kind, custody) — no manifest on a credential-bearing attempt ⇒ **withhold**;
  - initially-materialized bytes: `rec.Stored` with `rec.StoredOK` (opened pre-commit; for leased kinds at the lease-granted version — a mid-attempt operator rotation makes the materialized bytes unrecoverable) — `!StoredOK` ⇒ **withhold**;
  - driver-extracted fields: `d.sensitive(rec.Stored)`;
  - refreshed values: `rec.Conc.RefreshedSensitive` (the read-back, leased kinds);
  - the broker token: `rec.Manifest.BrokerToken` (brokered custody — Task 5 persisted it for exactly this path).
  **Withhold** means the unscannable shape: nothing lands in `logsDir`, quarantine retained, no log_ref, and collect's `Unscannable` branch governs artifacts. With the set complete, adoption follows Execute's ordering — conclude (Task 10 already did) → extend set → publish → collect. Attempts with no credential binding publish with the broker-token-only set as today.

- [ ] **Step 4: Run** — precision suite green; the whole runner unit suite green (the log-path move touches many tests — update fixtures to read published paths).

- [ ] **Step 5: Runnerintegration proof** — scripted agent under a codex-kind harness prints its own `$CODEX_HOME/auth.json` access token to stdout, copies it into a workspace file, then exits 0. Assert: Execute returns the `secret_in_artifact` policy-violation outcome (patch scan hit, fail closed); the published `agent.log` contains `redactedMarker` and not the token; the quarantine dir is gone. Second case — **withhold never untypes the exit**: script garbles `auth.json` (torn write) AND prints an auth-death marker, exit 1 → outcome is `auth_expired` (classified from the quarantine tail), logs withheld (`logsDir` empty, quarantine retained); same shape with the cap-hit marker → `rate_limited` with `RetryAt` intact.

- [ ] **Step 6: Gates + commit** — `task backend:ci` + runner lane green; `git commit -m "feat(backend): exact-value redaction with quarantined logs and fail-closed artifact scans"`

---

### Task 12: Lineage materialization and the `.caesar` filesystem contract

> **Amendments (user ruling + implementation deviations 2026-08-05, task review round 1 — these govern over the pinned snippets below):**
> 1. **The `stageAttempt` snippet is amended: a patch input whose `payload["diff"]` is missing, non-string, or empty is a permanent `invalid_output`** — not the discarded assertion the snippet's `in.Patch, _ = a.Payload["diff"].(string)` writes. Two failure modes, either fatal. (a) Staging no lineage is *silent*: the phase opens at the run base, and because extraction diffs from that base, its own cumulative patch then reads as a **revert** of everything its predecessor did — with no signal anywhere. (b) The discarded assertion makes the multi-patch guard **fail open**: a first patch input carrying a non-string diff leaves `in.Patch == ""`, so the `in.Patch != ""` check does not fire on a second one, and two cumulative patches are staged — defeating the exact contract a codex artifact-review round was spent establishing (baked decision 1). Sofo ruled 2026-08-05 that the deviation governs and the plan is amended to match. Pinned by `TestLineageInto_PatchWithoutTheWireFormIsRefused` (3 subtests).
> 2. **Step 3's `git add -A` pathspec is replaced by a following `git rm -r -q --force --cached --ignore-unmatch -- .caesar`** — mechanically forced, not preferred. With `/.caesar/` in `base.git/info/exclude` — the normal case, both mechanisms live — `git add -A -- . ':(exclude).caesar'` reports the excluded directory as an ignored path and **exits 1**, which `runGit` would turn into a permanent `extraction_failed` on every single attempt. Confirmed twice by independent scratch-repo probe (implementer and reviewer): `exit=1` with the exclude line present, `exit=0` without it. The `diff-tree` half of Step 3 is implemented exactly as pinned, so the two independent mechanisms survive intact — each is isolated by its own test (`TestStage_MirrorExcludeHidesCaesarFromTheTrustedIndex` for the mirror exclude; `TestExtract_PathspecHidesCaesarWithoutTheMirrorExclude`, whose first assertion requires `!Changed` on a workspace holding only the staged contract, for the index drop, which the `diff-tree` pathspec cannot cover because `Changed` is decided by tree comparison before any diff runs).
> 3. **Step 4's mapping is factored out of `stageAttempt` as `lineageInto(*gitops.StageInput, runs.ExecutionRequest) error`**, and `stageAttempt` gains a `req` parameter. The step's "inline" was incidental rather than load-bearing; the seam buys five unit tests that need no Docker daemon for logic that would otherwise only be reachable through the full four-container pipeline.

**Files:**
- Modify: `backend/internal/runs/runner/gitops/stage.go` (`StageInput` :26-31, `Stage` :49-108), `backend/internal/runs/runner/gitops/extract.go` (belt-and-braces pathspec :88-93, :130), `backend/internal/runs/runner/execute.go` (`stageAttempt` :186-193 maps the request)
- Test: gitops stage/extract tests (follow the package's existing build-tag convention), one runnerintegration case
- Docs: `backend/README.md` gains the "harness filesystem contract" section (written here, where the contract lands)

**Interfaces:**
- Consumes: `ExecutionRequest.Instructions/Inputs/TaskInputs` (already on the port, currently unread by the runner), collect.go's base64 patch wire form (`payload["diff"]`).
- Produces:

```go
// gitops additions
type StageContext struct {
	Name string          `json:"name"` // "<idx>-<type>.json"
	JSON json.RawMessage `json:"json"`
}
type InputRef struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name,omitempty"` // context filename; empty for applied patches
}
// StageInput gains:
Instructions string           `json:"instructions,omitempty"`
TaskInputs   map[string]any   `json:"task_inputs,omitempty"`
Patch        string           `json:"patch,omitempty"` // base64(std) canonical diff — SINGULAR: patches are cumulative from the run base (baked decision 1), one subsumes the chain
Contexts     []StageContext   `json:"contexts,omitempty"`
InputRefs    []InputRef       `json:"input_refs,omitempty"`

// pinned caps (validated in Stage; breach → permanent staging_failed)
maxInputPatchBytes   = 4 << 20   // decoded — mirrors maxPatchBytes
maxInstructionsBytes = 256 << 10
maxContextBytes      = 1 << 20   // per context file

caesarDir = ".caesar" // workspace-relative; in/ and out/ under it
```

- The workspace contract produced: `workspace/.caesar/in/instructions.md`, `workspace/.caesar/in/inputs.json` (`{"task_inputs": {...}, "inputs": [{"id","type","name"}]}`), `workspace/.caesar/in/<idx>-<type>.json` per context artifact, `workspace/.caesar/out/` created empty (harnesses declare artifacts there, Plan 06 contract). **`.caesar/` is invisible to the diff**: a `/.caesar/` line written into `base.git/info/exclude` at stage time (baked decision 11 — extraction runs `git add -A` under `GIT_DIR=base.git`, so this exclude governs the patch; base.git is outside the agent's bind, so the agent cannot re-include it), plus the same line in `workspace/.git/info/exclude` (agent-side `git status` hygiene only).

- [ ] **Step 1: Failing gitops tests**

1. **Cumulative round-trip** (the contract's core): stage a patch, simulate an agent edit touching one of the patched lines, `Extract` → the new patch contains BOTH changes (it diffs from `BaseSHA`, extract.go:130); staging that new patch alone into a fresh workspace reproduces the full composition. This is why staging takes ONE patch: the latest subsumes its ancestors.
2. **Conflicting patch** against the base → permanent `staging_failed` (typed, never a raw git error).
3. **Caps**: a 4MiB+1-decoded patch, oversize instructions, oversize context → each a typed permanent `staging_failed` naming the bound.
4. **Contract files**: instructions/inputs.json/context files land with the exact shapes above; `.caesar/out` exists and is empty.
5. **Invisibility**: after staging with inputs, append a file under `workspace/.caesar/in/` and run `Extract` → `Changed` is false; a real workspace change alongside it → the patch contains only the real change.

- [ ] **Step 2: Implement `Stage`** — after the checkout/verify steps (stage.go:77-96), in order: decode + bound-check the patch (when present), write it to `<dir>/input.patch` (outside the workspace), `git -C <ws> apply --whitespace=nowarn <abs path>` (failure → `typed(runs.OutcomePermanent, runs.CodeStagingFailed, "input patch does not apply")`); write both `info/exclude` lines; `MkdirAll` `.caesar/in` + `.caesar/out`, write `instructions.md`, `inputs.json`, and each context file 0644. The `.caesar` writes happen **after** the patch apply so a patch cannot touch them, and before the report so a failure is typed. `verifyStaging`'s credential-file scan already walks the whole dir — unchanged.

- [ ] **Step 3: Belt-and-braces in `Extract`** — `git add -A` (extract.go:91) and the `diff-tree` invocation (:185) both gain the explicit pathspec `"--", ".", ":(exclude)" + caesarDir` — redundancy on top of info/exclude, per the file-structure decision: two independent mechanisms, either alone suffices.

- [ ] **Step 4: Map the request** (`stageAttempt`, execute.go:186-193)

```go
in := gitops.StageInput{ /* existing fields */, Instructions: req.Instructions, TaskInputs: req.TaskInputs}
for i, a := range req.Inputs {
	if a.Type == runs.ArtifactKindPatch {
		if in.Patch != "" {
			// Cumulative patches cannot be chained (each contains its
			// ancestors — applying two double-applies); a node with two
			// patch inputs is a Plan 08 (generations/merge) shape.
			return ..., permanentf(runs.CodeStagingFailed,
				"node declares more than one patch input; multi-patch staging lands with generations (Plan 08)")
		}
		in.Patch, _ = a.Payload["diff"].(string) // already the base64 wire form (collect.go:87)
		in.InputRefs = append(in.InputRefs, gitops.InputRef{ID: a.ID.String(), Type: a.Type})
		continue
	}
	name := fmt.Sprintf("%d-%s.json", i, a.Type)
	raw, err := json.Marshal(a.Payload) // error → permanent invalid_output
	in.Contexts = append(in.Contexts, gitops.StageContext{Name: name, JSON: raw})
	in.InputRefs = append(in.InputRefs, gitops.InputRef{ID: a.ID.String(), Type: a.Type, Name: name})
}
```

- [ ] **Step 5: Runnerintegration proof — a real two-phase conveyor** (the contract's end-to-end shape, not a handcrafted patch): phase 1's scripted agent writes `a.txt`; phase 2's node declares phase 1's patch as input and its agent appends to `a.txt` and copies `.caesar/in/instructions.md` into a workspace file. Assert: phase 2's staged workspace already contained phase 1's `a.txt` (the cumulative patch applied); phase 2's extracted patch contains BOTH phases' changes and nothing under `.caesar`; the existing declared-artifacts test still passes (`.caesar/out` remains readable by collect — it is excluded from the *diff*, not from the filesystem).

- [ ] **Step 6: README** — add the "Harness filesystem contract" section: the `/workspace` mount, the `.caesar/in` inputs (instructions.md, inputs.json shape, context files), `.caesar/out/artifacts.json` (existing declarable allowlist), the invisibility rule and both mechanisms, the pinned caps, and the **cumulative patch contract** (every patch diffs from the run base and subsumes its ancestors; one patch input per node; multi-patch is Plan 08).

- [ ] **Step 7: Gates + commit** — `task backend:ci` + runner lane green; `git commit -m "feat(backend): cumulative-patch lineage staging and the .caesar filesystem contract"`

---

### Task 13: Harness images, the fakecli fixture, and the materialized-pipeline e2e

> **Amendments (controller rulings + a refuted review premise, 2026-08-06 — these govern over the pinned snippets below):**
> 1. **The real harness images read the CLI-native env pair ONLY** — settling Task 6's open NOTE (brokered agents receive both the CLI-native pair and `CAESAR_BROKER_*`, and something had to decide which the images consume). Claude Code reads `ANTHROPIC_BASE_URL`/`ANTHROPIC_API_KEY`, Codex reads `OPENAI_BASE_URL`(+`/v1`)/`OPENAI_API_KEY` (`driver.go:105`, `:153`); the entrypoints must never reference or translate `CAESAR_BROKER_*`, because each CLI already consumes its own variables and a translation shim in the image would be a second source of truth for the broker contract. `CAESAR_BROKER_URL`/`CAESAR_BROKER_TOKEN` stay in the brokered spec (`spec.go:110-111`) as the harness-agnostic contract for Plan 06's isolation probes and any future non-CLI harness. No change to `spec.go` or `driver.go`. This is also what makes the fixture's `fakecli:assert-no-broker` directive a real regression signal rather than a tautology: materialized custody never appends the broker pair at all.
> 2. **The CLI version pins are re-resolved, superseding the ones written into the step below.** Re-checked 2026-08-06: `@anthropic-ai/claude-code` = **2.1.223** (the step pinned 2.1.222) and `@openai/codex` = **0.146.1** (the step pinned 0.146.0). The Dockerfile `ARG`s carry the re-checked values. Note the consequence for Task 7's marker table, which is *not* re-pinned by this: the claude rows remain recorded against 2.1.222, so the smoke run re-verifies them against 2.1.223.
> 3. **No amendment for the claude image's `HOME` — the review premise was refuted, recorded here so it is not re-raised.** The review round called the claude image plan-mandated-broken for having no writable `HOME` on a read-only rootfs. It is not: `spec.go:23` sets `agentHome = "/tmp"` and `spec.go:87-95` seeds `HOME=`+`agentHome` into the agent env unconditionally, before custody is consulted; `containers.go:26,50` mount `/tmp` as `rw,size=268435456` alongside `ReadonlyRootfs`. The runner delivers a writable `HOME`, so the Dockerfile needs no `ENV HOME` — and a per-container env value wins over an image `Config.Env` entry on a duplicate key, so adding one would have been dead weight. Verified empirically against the real image under the runner's exact posture: Claude Code 2.1.223 starts, writes `~/.claude`, `~/.claude.json` and `~/claude-501` to the tmpfs as a non-root uid, and reaches the auth check. (The reviewer's failure mode is real *without* the runner's `HOME`: uid 501 has no `/etc/passwd` entry, so Docker would default `HOME=/` on the read-only rootfs.)

**Files:**
- Create: `docker/harness/claude-code/Dockerfile` + `entrypoint.sh`, `docker/harness/codex/Dockerfile` + `entrypoint.sh`, `docker/fakecli.sh`
- Modify: `Dockerfile` (new `fakecli` stage), `Taskfile.yml` (`image:harness` target; `image:build` also tags `caesar-fakecli:dev`), `backend/internal/app/runnerports.go` (`buildRunner` :33-63 — `MkdirAll` the five DataRoot subdirs)
- Test: `backend/internal/app/materialized_e2e_runnerintegration_test.go` (new)

**Interfaces:**
- Consumes: everything Tasks 1–12 built; the provisional codex marker rows (Task 7 — fakecli speaks them verbatim).
- Produces: `caesar-harness-claude:dev`, `caesar-harness-codex:dev` (operator-built, never CI), `caesar-fakecli:dev` (CI fixture, built by `image:build`). No new config fields (deadconfig gate: only fields that get read exist).

- [ ] **Step 1: Real harness images** (data for the smoke run, not CI)

`docker/harness/claude-code/Dockerfile`:

```dockerfile
FROM node:22-bookworm-slim
# Pinned at plan authoring (2026-08-05): npm view @anthropic-ai/claude-code version
ARG CLAUDE_CODE_VERSION=2.1.222
RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}
COPY entrypoint.sh /usr/local/bin/harness-entry
RUN chmod 0755 /usr/local/bin/harness-entry
ENTRYPOINT ["/usr/local/bin/harness-entry"]
```

`entrypoint.sh` (the container is the permission boundary — the sandbox flags are safe *because* of Plan 06's isolation, record that in a comment):

```sh
#!/bin/sh
set -eu
exec claude -p "$(cat /workspace/.caesar/in/instructions.md)" --dangerously-skip-permissions
```

codex mirrors it: `ARG CODEX_VERSION=0.146.0` (`npm view @openai/codex version`, same date), `npm install -g @openai/codex@${CODEX_VERSION}`, entry `exec codex exec --skip-git-repo-check --sandbox workspace-write "$(cat /workspace/.caesar/in/instructions.md)"`. Taskfile:

```yaml
image:harness:
    desc: Build the real harness agent images (operator machine only — never CI)
    cmds:
      - docker build -t caesar-harness-claude:dev docker/harness/claude-code
      - docker build -t caesar-harness-codex:dev docker/harness/codex
```

- [ ] **Step 2: The fakecli fixture** — a `fakecli` stage in the main `Dockerfile` (alpine + `jq`, `COPY docker/fakecli.sh`), tagged `caesar-fakecli:dev` by `image:build` (Taskfile:165-175 gains `--target fakecli -t caesar-fakecli:dev`), so the existing runner CI lane (ci.yml:26-40) builds it with no workflow change. The script reads `/workspace/.caesar/in/instructions.md` and executes one directive per line:

```
fakecli:assert-no-broker            # exit 42 if CAESAR_BROKER_TOKEN is set
fakecli:print-auth-sha              # sha256 of $CODEX_HOME/auth.json → stdout + workspace file
fakecli:mutate-auth                 # jq-rewrite tokens (new access/refresh values, SAME identity), truncate+rewrite like the real CLI
fakecli:poison-auth                 # rewrite with a different account_id/id_token (identity mismatch)
fakecli:garble-auth                 # write malformed bytes (the §2.7 torn write)
fakecli:print-secret                # cat the access_token to stdout (redaction bait)
fakecli:write <file> <content>     # a real workspace change
fakecli:rate-limit-until-refreshed <epoch>  # auth.json lacks "rt-refreshed" → print "usage limit reached|<epoch>", mutate-auth, exit 1; else no-op
fakecli:auth-death                  # print "401 Unauthorized: token expired", exit 1
fakecli:exit <n>
```

`rate-limit-until-refreshed` is the cross-attempt state trick: attempt 1 mutates the credential through real sync-back, so attempt 2 materializes the refreshed file and takes the success branch — state lives in the credential store, nothing in-process.

- [ ] **Step 3: `buildRunner` subdirs** — `MkdirAll` `attempts`, `logs`, `auth`, `manifests`, `quarantine` under DataRoot (runnerports.go:33-63 currently makes only the root).

- [ ] **Step 4: The golden e2e** (`materialized_e2e_runnerintegration_test.go` — full stack: real Postgres, real orchestrator, real runner, real assets adapters, fakecli agents; this is the CI-provable half of spec §6)

1. **Materialized pipeline**: codex harness on `caesar-fakecli:dev` + `agent_auth_file` credential; instructions: `assert-no-broker`, `print-auth-sha`, `mutate-auth`, `print-secret`, `write out.txt done`, `exit 0`. Assert: run succeeded; the patch contains `out.txt` and no `auth.json`; the logged auth sha equals the v1 secret's sha (env + mount delivery); `secret_version` is 2 with a sync receipt (sync-back through the whole stack); the published agent.log contains `[caesar:redacted]` and not the token; the gateway log's `egress.start` line shows the codex FQDN allowlist and no token digest (open-CONNECT).
2. **Cap-hit wait and resume**: instructions `rate-limit-until-refreshed <epoch≈now+1h>` + `write out.txt done`. Assert attempt 1's outcome is `rate_limited` with `RetryAt` from the marker; the retry row carries `not_before`; SQL-backdate it (the Plan 05 precedent); attempt 2 succeeds; `chargeable_failures` 0; run succeeded.
3. **Auth death names the credential**: `fakecli:auth-death` → run fails fast, outcome `auth_expired`, message contains the credential id, `last_auth_expired_at` set.
4. **Poisoned identity quarantines**: `fakecli:poison-auth` + `exit 0` → outcome `credential_quarantined`; stored secret still decrypts to v1 bytes; audit `credential.sync_quarantined` present.
5. **Torn write withholds**: `fakecli:garble-auth` + `write out.txt x` + `exit 0` → permanent `invalid_output`; no log_ref artifact, quarantine dir retained on host; credential preserved at v1.

- [ ] **Step 5: Gates + commit** — `task ci` does NOT include the runner lane (Taskfile: `ci` → `backend:ci` + contracts + secrets + actionlint; `backend:test:runner` is separate, invoked only by the CI runner job), so the gate here is explicit: `task image:build && task backend:test:runner && task ci` — the e2e must actually execute locally before this task may commit. `git commit -m "feat(backend): harness images, fakecli fixture, and the materialized-pipeline e2e"`

---

### Task 14: CI audit, docs, the sandbox spike, the smoke checklist, roadmap

**Files:**
- Modify: `Taskfile.yml`/`.github/workflows/ci.yml` (audit only — change only if the package audit finds a gap), `backend/README.md`, `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md` (row 07)
- Commit: this plan document (untracked until now, per the 03–06 convention)

- [ ] **Step 1: CI package audit** — `backend:test:runner` already runs `./internal/runs/runner/... ./internal/runs/egress/... ./internal/app/` (gitops rides `runner/...`); confirm every new test file's package is covered and `nightly.yml`'s twin job matches. New lanes are none; `ci-ok`'s needs-list (ci.yml:85) is untouched. Fix anything the audit finds.

- [ ] **Step 2: README — Plan 07 section**: the driver contract (custody table: harness kind × credential kind → custody; env delivery per cell; the FQDN allowlists with the verified-2026-08-05 caveat and the denial-event drift signal); the lease protocol in five sentences (single-flight, epoch fence, receipts, recovery authority, accepted loss window §2.7); delayed retry (`not_before`, chargeable failures, **`attempt` may legitimately exceed `max_attempts`** — the §4.3 documentation consequence, stated for events/API readers); redaction rules and the withhold; the images (`image:harness` operator-only, fakecli in CI); credential freshness signals (§5).

- [ ] **Step 3: Codex sandbox spike** (spec §2.5 — bounded: half a day, verdict either way)

Probe whether the Codex CLI's native command sandbox denies model-executed commands read access to `/caesar-auth` inside our container: run `caesar-harness-codex:dev` locally with a scratch auth dir and use the CLI's sandbox debugging entry points (`codex debug` subcommands in v0.146.0) to execute `cat /caesar-auth/auth.json` under the `workspace-write` policy on Linux (Landlock) — the deploy target. Record in the README, beside the driver contract: the command transcript, whether reads were denied, and the verdict — **adopt** (wire the flag into the codex driver's entrypoint as a follow-up line item) or **reject** (state that the §2.5 residual stands as user-accepted). Evidence, not implementation.

- [ ] **Step 4: Manual smoke checklist** (README, operator-run, real subscriptions — the spec §6 boundary line; no CI involvement)

1. `claude setup-token` on any machine → create a `subscription_token` credential → register a claude-code harness on `caesar-harness-claude:dev` (`task image:harness` first) → run a small real task → a patch lands. If a cap window is hit: observe `rate_limited`, the parked `not_before`, and the unattended resume.
2. `codex login --device-auth` on the caesar box (exclusive-custody note: this copy becomes the only live copy) → paste `auth.json` as an `agent_auth_file` credential → run a real task → verify the patch **and** `secret_version` bumped if the CLI refreshed.
3. Revoke the Codex session from another device → run → `auth_expired` fails fast naming the credential; `last_auth_expired_at` visible in the credentials view.
4. **Record the real CLI marker lines** observed for cap-hit and auth-death (both CLIs) and update the Task 7 marker tables + test fixtures — promoting the provisional codex rows to recorded. Commit that as its own change.

**The smoke run is the plan's acceptance evidence, not an optional appendix** (spec §6: the operator-run half is the only place real CLI behavior is ever observed; the codex marker rows are INVENTED until it happens — if the real CLI speaks differently, cap hits charge as `harness_error` and auth death stops failing fast). Roadmap row 07 may flip to **done** only after checklist items 1–4 are executed and their evidence (transcripts + recorded markers) is committed. If the operator cannot run them at plan end, the row is annotated **"implementation complete — smoke pending, provisional codex markers"** and Plan 07 stays open on that single item; it never silently closes.

- [ ] **Step 5: Roadmap + plan** — roadmap row 07 per the gate above (done only with smoke evidence; otherwise the smoke-pending annotation), plus: "generations/revision rematerialization moved to Plan 08 (needs gate decisions)". `git add docs/superpowers/plans/2026-08-05-caesar-07-harness-drivers.md` and the roadmap/README changes.

- [ ] **Step 6: Full gate** — `task image:build && task backend:test:runner && task ci` green end to end (`task ci` alone never invokes the runner lane; worktree secrets-lane skip is the known exception).

- [ ] **Step 7: Commit** — `git commit -m "docs(backend): plan 07 driver contract, smoke checklist, and roadmap close-out"`

---
