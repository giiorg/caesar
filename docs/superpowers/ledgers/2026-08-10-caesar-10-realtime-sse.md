# SDD ledger — plan: docs/superpowers/plans/2026-08-10-caesar-10-realtime-sse.md

> **HANDOFF 2026-08-11 (session 57) — RESUME AT TASK 5.**
>
> Worktree `/Users/sofo/Projects/caesar/repos/caesar/.claude/worktrees/caesar-10-realtime-sse`,
> branch `caesar-10-realtime-sse`, HEAD **`1ed903a`**, clean tree. Tasks 1-4 complete,
> reviewed and GATED; Tasks 5-12 untouched. `main` is untouched at `64de023`.
> **Verify with `git log` before acting on any of this.**
>
> - Task 3 `78a4346` — `NOTIFY caesar_run_events` in the writer's tx. Review clean first pass.
>   `GATE PASS @ 78a4346`, 11 lanes unnarrowed.
> - Task 4 `1ed903a` — the tailer, plus the ruled `Snapshot` port rename and `Head`.
>   Clean after 1 fix round. `GATE PASS @ 1ed903a`, 11 lanes unnarrowed.
>
> **SOFO'S `Snapshot` RULING IS IMPLEMENTED AND CLOSED** — landed in Task 4, verified
> zero remaining `SnapshotWatermark` references tree-wide. Nothing to carry.
>
> **The three things the plan still does not know:**
>   1. **Task 5's hub `sink` MUST be non-blocking** — the tailer depends on it by contract
>      and deliberately does not enforce it. A blocking sink stalls the single per-process
>      tailer and therefore every subscriber.
>   2. **Task 7's `Since` MUST be `ORDER BY id LIMIT limit`** — now written into `ports.go`,
>      and load-bearing: the cursor advances to the highest id handed over, so a source that
>      skips lower ids before truncating strands them permanently. Task 7 also cannot use
>      `pg.WithTx` (no `TxOptions`, cannot open repeatable-read) — use `pool.BeginTx`.
>   3. **Task 11 must apply `task-11-config-section.patch`** from this directory; its brief
>      does not mention it. And Task 12's docs step says "config knobs from Task 2" — that
>      is now Task 11.
>
> **The Docker/gate blocker from the last handoff is RESOLVED** — see the section below.
> Both gates this session passed all 11 lanes. On an `image:build` registry timeout, just
> retry: Docker Hub is intermittent, not down.
>
> **Method note that earned its keep:** pre-flighting each brief against the real source
> before dispatch found 4 plan defects in Task 3 and 6 in Task 4 — including one that would
> have silently destroyed 229 lines of existing tests. That is 10 defects in 2 tasks, on top
> of the 2-in-2 the previous session found. **Do not dispatch a brief you have not read
> against the tree.**
>
> **SOFO RULED the one open question** — `SnapshotWatermark` becomes `Snapshot` returning
> both halves from one call. See the RULING block below. It is NOT open; do not re-ask it.
> Land the rename in **Task 4** (which already edits `ports.go`).
>
> **Four things the plan does not know and that MUST be carried forward:**
>   1. Task 4 lands the `Snapshot` port rename (sofo's ruling) on top of adding `Head`.
>   2. Task 7 cannot use `pg.WithTx` — it takes no `TxOptions` and cannot open
>      repeatable-read. Use `pool.BeginTx` or add a helper.
>   3. Task 11 must apply `task-11-config-section.patch` from this directory (the config
>      section deferred out of Task 2 — `arch.CheckDeadConfig` rejects unread fields).
>      Task 11's brief does not mention it. If the patch is ever lost, the content is
>      reproducible from the plan's Task 2 Steps 6-7, which carry the struct and the
>      dev-JSON block verbatim.
>   4. Task 12's docs step says "the config knobs from Task 2" — that is now Task 11.
>
> ~~**The gate has not returned GATE PASS since `4475e6d`**~~ **CLOSED 2026-08-11 at
> `78a4346`:** `GATE PASS … 11 lanes resolved unnarrowed, Commit=78a4346 stamped in the
> binary and in caesar-git:dev, contracts clean`. A clean pass at the *final* sha is still
> owed before merge, as always, but the branch is no longer carrying an unproven gate.
>
> **Durability:** this directory is GITIGNORED. It is mirrored to the tracked path
> `docs/superpowers/ledgers/2026-08-10-caesar-10-realtime-sse.md` (a SNAPSHOT — refresh it
> at each task boundary with `cp` + commit, or the tracked copy goes stale), and copied
> whole to `/Users/sofo/Projects/caesar/sdd-backups/plan10-d62a5e0/` which lives outside
> the repo entirely. Never `git clean -fdx` this worktree without re-copying first.
>
> Process note for the next controller: subagents in this session routinely went idle
> WITHOUT delivering their report. A direct `SendMessage` telling them to reply via
> SendMessage to `team-lead` retrieved it every time. Budget for that round-trip; do not
> assume silence means failure, and do not re-dispatch a duplicate agent.


Branch: caesar-10-realtime-sse
Base: 375692c (plan commit), on top of 64de023 (main)
Worktree: /Users/sofo/Projects/caesar/repos/caesar/.claude/worktrees/caesar-10-realtime-sse

## Pre-flight scan (controller, before Task 1)

- Verified `go-cmp v0.7.0` is in go.mod — the plan's Task 1 test may use it.
- Verified `appendEvents(ctx, tx pgx.Tx, ...)` in `internal/runs/events.go:59` —
  Task 3's `tx.Exec(ctx, "NOTIFY caesar_run_events")` fits.
- Verified `config.Duration` exists (`config.go:130`) and `Forge` is the last
  section — Task 2's `Realtime` section slots in after it.
- Verified `contracts.go:154-158` is the success-response block Task 1 replaces.
- **CONFLICT (controller-resolved, mechanical):** Task 1's plan snippet appends a
  test calling the unexported `contentTypeOf` to `contracts_test.go`, which is
  `package contracts_test` (external). That will not compile. Resolution: the
  `contentTypeOf` unit test goes in a NEW internal test file
  `internal/contracts/contenttype_test.go` (`package contracts`); the
  document-drift test stays in the external file calling `contracts.Build()`.
  No Global Constraint is touched.
- **Note (Task 2):** the plan's `realtime_test.go` snippet imports only
  `testing` but uses `uuid`, and disambiguates two identical table rows with a
  `if tc.name == ...` branch. Both are mechanical; the implementer fixes the
  import and should restructure the row to carry its own event rather than
  branch on the test name.
- Lane names confirmed: sqlc regeneration is `task db:sqlc` (NOT
  `backend:sqlc`, which the plan guessed at in Tasks 3/7).

## Progress

Task 1: complete (commits 375692c..4475e6d, review clean — spec ✅, no Critical/Important)
Task 1: minor (deferred): `TestBuild_UnchangedWhenNoOperationOptsIn` is named for this
  task's transition but asserts generic generated-vs-committed drift. Once Task 10 opts an
  operation into `text/event-stream` and the artifact legitimately moves, the name goes
  false. Rename to `TestBuild_MatchesCommittedDocument` when a later task next touches
  `contracts_test.go` — Task 10 does.
Task 1: minor (deferred): `contentTypeOf` is only consulted on the `op.Response != nil`
  branch, so a nil-Response operation with a set `ResponseContentType` is silently ignored.
  **Carry into Task 10:** its `streamAttention` operation must declare a non-nil `Response`
  (the plan does: `Response: StreamFrame{}`). Verify at Task 10 review.
Task 1: minor (deferred): the new external test takes `t.Parallel()` while the file's three
  pre-existing tests are serial. Harmless; no action.
Task 1: ⚠️ resolved by controller — the byte-level no-op on `backend/api/openapi.json` is
  confirmed by the artifact's ABSENCE from the diff (4 files, 65 insertions, none of them
  the generated document) plus `task contracts:check` exit 0. Nothing to check.

### RULED BY SOFO 2026-08-11 — `SnapshotWatermark` becomes `Snapshot`, returning both halves

**RULING: the port returns the snapshot payload AND its watermark from ONE call.** The
`internal/app` adapter owns the single `pool.BeginTx(ctx, pgx.TxOptions{IsoLevel:
pgx.RepeatableRead, AccessMode: pgx.ReadOnly})` closure, so the transaction never crosses the
bounded-context boundary and `txboundary` stays satisfied. The point of the shape is that it
makes the gap **structurally unrepresentable** rather than merely documented.

```go
type Snapshot struct {
	Events    []Event   // or the run projection
	Watermark Watermark // read in the SAME tx
}

type EventSource interface {
	// Snapshot returns the caller's current state AND the high-watermark it
	// was computed at, both from ONE repeatable-read transaction. Returning
	// them together is what makes the gap unrepresentable.
	Snapshot(ctx context.Context, f Filter) (Snapshot, error)
	Replay(ctx context.Context, f Filter, after int64, limit int) ([]Event, error)
	Since(ctx context.Context, after int64, limit int) ([]Event, error)
	Oldest(ctx context.Context) (int64, error)
}
```

**Who applies it:** whichever task next touches `ports.go`. Task 4 already edits that file
(it adds `Head`), so **Task 4 is the natural place to land the rename** — do it there, and
say so in Task 4's dispatch. If Task 4's implementer pushes back, Task 7 must do it before
implementing the adapter. Task 8's `Serve` splice consumes the new shape too: it calls
`Snapshot` once and uses `.Watermark` as the replay cursor.

**Still true regardless of the ruling:** `pg.WithTx` (`internal/platform/pg/tx.go:15`) takes
no `TxOptions` and cannot open repeatable-read. Task 7 needs `pool.BeginTx` directly or a new
helper beside `WithTx`. Do not let an implementer reach for `WithTx` and call it done.

The original finding, and the three facts the controller verified, are preserved below.

---

### The finding as raised (Task 2 review) — kept for context

Task 2's `ports.go` is committed with the OLD shape; the fix is deliberately deferred per the
ruling above. Does not block Tasks 3-6.

`EventSource.SnapshotWatermark(ctx, f Filter) (Watermark, error)` (`ports.go:12-17`) carries a
doc comment — verbatim from the plan, Task 2 Step 4 — promising the watermark is "read in the
SAME repeatable-read transaction the caller's snapshot was computed in. Both values must come
from one tx or an event committing between them is lost — the gap this whole design exists to
prevent." **The signature cannot express that contract:** it neither receives a transaction
nor returns the snapshot.

Three facts the controller verified independently (not taken from the reviewer):
1. `internal/analyzers/txboundary/txboundary.go:73` reports on **interface methods**:
   "transaction type in exported bounded-context API — transactions never span contexts".
   So passing `pgx.Tx` through this port is illegal by this repo's own analyzer.
2. `pg.WithTx(ctx, pool, func(tx pgx.Tx) error)` (`internal/platform/pg/tx.go:15`) is the only
   transaction helper and it is closure-shaped. There is no tx-in-context convention anywhere
   in `identity`, `assets` or `runs`.
3. **`pg.WithTx` takes no `TxOptions`** — it cannot open `RepeatableRead`/`ReadOnly` at all.
   Plan Task 7 Step 4 requires exactly `pgx.TxOptions{IsoLevel: pgx.RepeatableRead,
   AccessMode: pgx.ReadOnly}`. So Task 7 needs a new helper or `pool.BeginTx` directly,
   regardless of how the ruling goes.

Why it matters: Task 7's implementer reading only this port has no legal way to honour the
doc. The path of least resistance — open a fresh tx, `SELECT max(id)`, return it, let the
caller fetch the snapshot separately — is *precisely* the gap the comment says the design
exists to prevent, and it passes every compile-time check in the repo.

Controller's recommendation: make the port return both halves from one call (the snapshot
payload plus its watermark), so the `internal/app` adapter owns the single `BeginTx`
closure and the transaction never crosses the context boundary. Alternative: keep the split
and move the transaction ownership statement into Task 7's brief with an explicit named
mechanism. Sofo rules.

### PLAN DEFECT — Task 2's config section moved to Task 11 (controller ruling, 2026-08-10)

Task 2's implementer found that `arch.CheckDeadConfig` (`internal/arch/deadconfig.go:12-15`,
asserted by `TestCheckDeadConfig_LiveConfigHasNoDeadFields`) requires **every leaf field of
`config.Config` to have a read site outside the config package AND outside tests** — its own
fixture flags `OnlyTestRead` as dead. Adding `config.Realtime` with no readers reddens the
arch lane immediately, and the earliest honest read site is `internal/app` constructing the
tailer/hub, which is **Task 11**. The plan gives the tailer a `TailerConfig{PollFallback,
Batch}` of plain durations (plan line ~583), so no task 3-10 reads `config.Realtime` either.
Verified independently by the controller, not taken on the implementer's word.

Task 2 as written therefore cannot satisfy the plan's own Global Constraint "Every task ends
with `scripts/gate` green — 11 lanes, unnarrowed." **Ruling: defer the config section to
Task 11.** This is a scoping move (same code, same values, later task), not a design change.
Rejected: a token read site (dead code that defeats the guard), and weakening the check.

**TASK 11 MUST DO THIS:** apply
`.superpowers/sdd/2026-08-10-caesar-10-realtime-sse/task-11-config-section.patch`
(the finished config work: `config.go` struct + `Config` field + validation, `config_test.go`
round-trip/`"0s"`-rejected/`0`-rejected cases, and the `caesar.dev.json` block with
`heartbeat_interval` at `20s`). Task 11's brief does NOT mention it — the controller must
add it to that dispatch. Task 2's commit message drops "and config" accordingly.

Task 2: fix round 1/5 (5 addressed, 0 open — attention-scope test gap, unfalsifiable
  `!f.Attention` guard, Event immutability doc, RunID-precedence doc, false report sentence;
  commits 165348c..d62a5e0, amended)
Task 2: complete (commits 4475e6d..d62a5e0, review clean after 1 fix round)
Task 2: **GATE NOT GREEN at d62a5e0** — see the Docker Hub item below. Nine lanes passed at
  165348c including `internal/arch` (the lane this task exists to satisfy); the amended sha
  has had fmt/lint/vet re-run clean but no full gate. **Re-run must name d62a5e0, not
  165348c.**
Task 2: minor (deferred): `Filter`'s doc (`realtime.go:55-56`) still says "Exactly one of
  RunID or Attention is set", in mild tension with the both-set precedence doc four lines
  below. Pre-existing text, harmless — the new doc covers the invalid shapes explicitly.

Task 3: complete (commits 7e7947e..78a4346, review clean on the FIRST pass — spec ✅, no
  Critical/Important) and **GATED: `GATE PASS … 78a4346`, 11 lanes resolved unnarrowed.**
Task 3: the review judged the mutation evidence genuine rather than taking it on the report's
  word — the mutant (NOTIFY on a side connection via `pgx.ConnectConfig`) breaks exactly the
  property the rollback assertion pins, and only the rollback half went red under it.
Task 3: **INTERFACE PRODUCED FOR TASK 4** — channel `caesar_run_events`, payload always
  **empty**, one NOTIFY per *transaction* (not per event), global (not per project). Task 4's
  tailer must LISTEN on exactly that string and treat a wake as "something, somewhere, may
  have committed" — re-read from its own cursor, never trust the signal for content.
Task 3: **behaviour change worth knowing** — `appendEvents` now fails the caller's transaction
  if the NOTIFY fails. Deliberate (a silent notify failure would strand tailers on polling
  latency), and the review checked all 19 production call sites propagate the error, but this
  is a function every state transition in the system calls.
Task 3: ⚠️ resolved by controller — the reviewer's two "cannot verify from diff" items. (a) The
  Task 4 LISTEN-string match is a genuine cross-task interface claim, carried into Task 4's
  dispatch above. (b) The pgx claim that a `WaitForNotification` deadline leaves the connection
  healthy is corroborated behaviourally by the green run, which re-uses that connection after
  the 1s timeout. Neither is a gap.
Task 3: minor (deferred): `events_integration_test.go:262-275` — the commit half does not drain
  queued notifications before the rollback half. Harmless today (one append, one NOTIFY); a
  future second committed append before the rollback half would make the quiet-wait fail
  confusingly. A drain loop or a comment noting the assumption would future-proof it.
Task 3: minor (deferred): the whole-package integration evidence is a package-level `ok` line
  without `-v`, so "the 3 pre-existing tests still pass" rests on that rather than per-test
  output. Acceptable; noted.

Task 4: fix round 1/5 (2 addressed, 0 open — untestable multi-batch drain loop, and a sort
  comment claiming a gap defence that was false in the truncation case; commit 0ea1a74,
  amended to 1ed903a)
Task 4: complete (commits eacbf5c..1ed903a, review clean after 1 fix round) and **GATED:
  `GATE PASS … 1ed903a`, 11 lanes resolved unnarrowed.**
Task 4: ⚠️ **the gate caught what four review passes did not** — `0ea1a74` failed
  `backend:lint` on a goimports **grouping** violation for the `xerrors` import added during
  the fix round (`tailer_test.go:14`). Note `backend:fmt:check` (gofumpt) passed; only
  golangci-lint's goimports catches import grouping. Amended to `1ed903a`. **Lesson: gofumpt
  clean ≠ lint clean; neither an implementer's local run nor a reviewer reading a diff sees
  this — only the gate does.**
Task 4: **THE `Snapshot` PORT RENAME IS LANDED.** `EventSource.SnapshotWatermark` is gone;
  `Snapshot(ctx, Filter) (Snapshot, error)` returns `Snapshot{Events []Event; Watermark
  Watermark}`. `Head(ctx) (int64, error)` added for the tailer's startup cursor. Controller
  verified `grep -rn SnapshotWatermark` returns ZERO hits tree-wide. Sofo's ruling is DONE —
  Task 7 implements this shape; Task 8 calls `Snapshot` once and uses `.Watermark` as its
  replay cursor.
Task 4: **CARRY-FORWARD TO TASK 7 — `Since` MUST be `ORDER BY id LIMIT limit`**, never a
  truncation of some other order. This is now written into `ports.go`'s doc, and it is not
  stylistic: the tailer advances its cursor to the highest id handed over, so a source that
  skips lower ids before truncating strands them PERMANENTLY, and the tailer's sort cannot
  recover them (it runs after truncation, so it raises the cursor to max(batch)). The review
  caught the original comment asserting the exact opposite.
Task 4: **CARRY-FORWARD TO TASK 5 — the hub's `sink` MUST be non-blocking.** The tailer
  documents the dependency and deliberately does not enforce it; a blocking sink stalls the
  single per-process tailer and therefore every subscriber.
Task 4: ⚠️ resolved by controller — the reviewer's four "cannot verify from diff" items. Two I
  checked directly: `SnapshotWatermark` has zero remaining references, and
  `xerrors.NotImplemented(what string) error` matches the fake's usage. The other two are the
  Task 7 `ORDER BY id` and Task 5 non-blocking-sink carry-forwards recorded above. No gaps.
Task 4: controller ruling (the review asked for it): **the `NewTailer` defaults stay**
  (`tailer.go:14-21`, `:60-66`). They are beyond the brief, but they close two silent-deadlock
  modes — a zero `PollFallback` is a hot loop, a zero `Batch` never advances — and that is
  worth more than strict brief-minimalism.
Task 4: minor (deferred): one `Warn` per poll interval while the LISTEN connection is down
  (~1800 identical lines/hour at the 2s default) for a condition the poll fallback is already
  handling. First occurrence at `Warn`, subsequent at `Debug` until a `Wait` succeeds.
Task 4: minor (deferred): the fatal `Head`-error path (`tailer.go:83-86`) and the non-deadline
  waker-error path (`tailer.go:141-144`) are untested — the fake never errors on either. The
  seed-failure path is the one Task 11 is told to depend on.
Task 4: minor (deferred): `tailer.go` sorts the source's slice in place and hands that same
  slice to `sink`. Safe for a fresh pgx result set, hostile to any future cached/pooled slice;
  nothing documents that the hub must not retain-and-mutate it.
Task 4: minor (deferred): `NewTailer` does not guard a nil `*slog.Logger`; it would panic only
  on the two error paths, so the happy path hides the mistake until the first DB blip.

### Task 3 pre-flight (controller, 2026-08-11, session 57) — FOUR plan defects, all corrected in the brief

Read against the tree at `7e7947e` before dispatching. Recorded in full at the top of
`task-3-brief.md` as a CONTROLLER PRE-FLIGHT block that overrides the plan text.

1. **`backend/internal/runs/events_integration_test.go` ALREADY EXISTS** (229 lines, three
   tests). The brief says "create". An implementer following it literally would have
   destroyed `TestEventAppendIsAtomic`, `TestEventIDsAreCommitOrdered` and
   `TestAppendRuntimeEvent_OwnTxProjectScopedAndOrdered`. Resolution: APPEND.
2. **The plan's test calls `appendEvents`, which is unexported, from `package runs_test`.**
   Would not compile — *the identical defect Task 1 hit*. The exported hook
   `runs.AppendEventsForTest` already exists (`export_integration_test.go:17`). This is now
   three tasks out of three where the plan's test snippet was not compilable as written.
3. **The plan says `testdb.New(t)` for the pool.** `run_events.run_id` is
   `NOT NULL REFERENCES runs (id)` (`schema.sql:313`), so an append against an empty
   database dies on the FK before reaching the NOTIFY. Resolution: `newFixture(t)` +
   `f.newRunRow(t, "running")`, as the file's three existing tests already do.
4. **`WaitForNotification` is not on `*pgxpool.Conn`.** It is on the underlying `*pgx.Conn` —
   `lc.Conn().WaitForNotification(ctx)`, with `LISTEN` issued on that same acquired conn.

**Verified NOT a defect:** raw `tx.Exec` for a utility statement needs no sqlc query and
breaks no arch rule — `internal/identity/service.go:103` sets the precedent with
`tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", …)`.

### RESOLVED — the Docker picture was misdiagnosed; the daemon was simply DOWN

Controller-verified 2026-08-11, session 57. **The Docker daemon was not running** (the
reboot trap: Docker Desktop does not survive a crash/restart). Started it with
`open -a Docker`; server 29.6.2 is up.

**The local image cache is intact and holds everything the gate needs** — `alpine:3.22`,
`postgres:18-alpine`, `postgres:18`, `testcontainers/ryuk:0.14.0`, `golang:alpine`, and the
two pinned harness images. So the integration lanes and plausibly `image:build` can run
after all; the previous session's "no clean 11-lane pass is obtainable" conclusion was too
pessimistic.

**Still true:** Docker Hub itself is unreachable — `curl https://auth.docker.io/token…`
times out at 20s with HTTP 000, independent of the daemon. Any lane needing a *new* pull
will hang. Whether `image:build` re-resolves the cached `alpine:3.22` manifest against the
registry is an open empirical question — the Task 3 gate run answers it.

⚠️ `timeout` does not exist on this macOS shell (`command not found`). Use
`curl --max-time` or a polling loop.

### CLOSED — the Docker gate problem, correctly diagnosed at last

Superseded by the session-57 findings above. The previous session's diagnosis ("Docker Hub is
unreachable, no 11-lane pass is obtainable") was two separate problems read as one, and the
pessimistic half was wrong:

1. **The daemon was down.** Docker Desktop does not survive a restart. `open -a Docker`.
2. **Docker Hub is INTERMITTENT, not dead.** The first gate run of this session did fail
   `image:build` on exactly this, with the precise cause now on record: buildkit resolves
   `golang:1.26-alpine` (`Dockerfile:1`) from the registry and `auth.docker.io` timed out.
   Note the local cache holds `golang:alpine`, **not** the `1.26-alpine` tag the Dockerfile
   pins, so a cached-image workaround does not exist. Minutes later the same endpoint
   answered HTTP 200 in 0.56s and **the identical gate command passed all 11 lanes.**

**The lesson for the next controller: on an `image:build` registry timeout, just retry the
gate before concluding anything.** Do not retag a local image to fake the pinned tag — that
would swap the Go toolchain version under the build and pass a gate that means nothing.

**Gate ordering note (applies from Task 2 on):** `scripts/gate` takes a sha as an argument
and verifies the named worktree is at it, so the commit must exist BEFORE the gate runs.
The plan's "Gate and commit" steps are to be executed as commit → gate → fix-and-amend if
red → re-gate.

