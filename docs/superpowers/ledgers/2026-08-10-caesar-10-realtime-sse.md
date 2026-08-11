# SDD ledger — plan: docs/superpowers/plans/2026-08-10-caesar-10-realtime-sse.md

> **HANDOFF 2026-08-11 — paused after Task 2 at sofo's request. RESUME AT TASK 3.**
>
> Worktree `/Users/sofo/Projects/caesar/repos/caesar/.claude/worktrees/caesar-10-realtime-sse`,
> branch `caesar-10-realtime-sse`, clean tree. Tasks 1-2 complete and reviewed;
> Tasks 3-12 untouched. **Verify with `git log` before acting on any of this.**
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
> **The gate has not returned GATE PASS since `4475e6d`** because Docker Hub is
> unreachable (`docker pull alpine:3.22` times out — controller-verified, not inferred).
> `image:build` and `backend:test:runner` fail environmentally; the other nine lanes pass,
> including `internal/arch`. A clean 11-lane pass at the final sha is still owed before
> merge. Do not record a "gated" claim for Tasks 2+ until then.
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

### OPEN — the gate has not passed on this branch since 4475e6d (Docker Hub outage)

`scripts/gate` returns GATE FAIL on `image:build` because Docker Hub is unreachable from this
machine. **Controller-verified, not taken on the implementer's word:** `docker pull
alpine:3.22` — the exact base image `Dockerfile:19` needs — times out against
`auth.docker.io`. `backend:test:runner` fails downstream because it needs that image.

Nine of eleven lanes pass, including `internal/arch`, `contracts:check` and all of
`backend:ci`. This is an environment outage, not a code defect — but it means **no clean
11-lane GATE PASS exists on this branch after 4475e6d**, and one must be obtained at the
final sha before merge. Do not record a "gated" claim for Tasks 2+ until then.

**Gate ordering note (applies from Task 2 on):** `scripts/gate` takes a sha as an argument
and verifies the named worktree is at it, so the commit must exist BEFORE the gate runs.
The plan's "Gate and commit" steps are to be executed as commit → gate → fix-and-amend if
red → re-gate.

