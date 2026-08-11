# SDD ledger — plan: docs/superpowers/plans/2026-08-10-caesar-10-realtime-sse.md

> **HANDOFF 2026-08-11 (session 59) — RESUME AT TASK 9.** This block supersedes the session-58
> and session-57 handoffs below it, which are kept only for the Docker/gate operational notes.
>
> Worktree `/Users/sofo/Projects/caesar/repos/caesar/.claude/worktrees/caesar-10-realtime-sse`,
> branch `caesar-10-realtime-sse`, last CODE commit **`8f1a133`**, clean tree.
> **Tasks 1-8 complete, reviewed and GATED; Tasks 9-12 untouched.** `main` untouched at `64de023`.
> **Verify with `git log` before acting on any of this.**
>
> Gated shas, all 11 lanes unnarrowed: Task 3 `78a4346` · Task 4 `1ed903a` · Task 5 `8c4bfc1` ·
> Task 6 `510d9bb` · Task 7 `ceadbaf` · Task 8 `9108992` and `8f1a133`. A clean pass at the FINAL
> sha is still owed before merge. **The `GATE PASS @ 0023257` in the log was discarded, not
> trusted** — see the process-hazard section.
>
> **Tasks 5, 6, 7 and 8 each took exactly one fix round**, and every finding in all four was
> demonstrated by a falsifying mutation rather than argued.
>
> **WHAT THE PLAN DOES NOT KNOW — the carry-forwards. Each is a `Task N:` line in the body below;
> read them there for the reasoning, this is only the index.**
>
> **Task 9 (grant re-check) inherits these:**
>   1. `CloseForbidden CloseReason = "…"` is the ENTIRE change — no error var, no mapping.
>      First reason wins on close.
>   2. **The plan's test 3 is WRONG as written.** It says a resolver error must "keep the last
>      known scope and retry", but `ScopeFor` returns `ClassNotFound` for a deleted or unknown
>      user, which is NOT transient — keeping the last known scope would stream to a deleted user
>      forever. Task 9 must distinguish the transient blip from the terminal answer.
>   3. Task 8 built the seam: `Streamer`/`StreamerConfig` in `stream.go`, and
>      `StreamRequest.UserID` is already carried and deliberately unread — Task 9 makes it live.
>      Add `GrantResolver` to the `Streamer` and `GrantRecheck` to `StreamerConfig`, and default a
>      non-positive `GrantRecheck` (`time.NewTicker` panics on <= 0, as `HeartbeatInterval` does).
>   4. The natural place is a second ticker case in `follow`. **Serve's contract is `nil` on
>      client disconnect** — do not let a new phase return a wrapped `context.Canceled`; the
>      single `ctx.Err()` funnel in `Serve` exists so a new phase cannot forget.
>   5. ⚠️ **The flush pin does NOT cover `follow`.** `TestServe_AddsNoFlushOfItsOwn` ends its
>      stream on a closed channel, so a `Flush()` added only in `follow`'s event case survives
>      (verified). Task 9 edits exactly that function AND legitimately calls `Writer.Flush()` for
>      the terminal frame — so extend that test (one live event before `hub.Close()`, `want` gains
>      `"write","flush"`) rather than trusting it.
>   6. Once `NewWriter` has flushed the headers the HTTP status is committed 200 forever — hence
>      "403-shaped terminal **frame**", never a status code. `Writer.Flush()` exists for exactly
>      that direct write and has no other caller today.
>   7. If you open `hub.go`, move the unexported `closeClientGone` (`stream.go:26`) into its const
>      block or leave a pointer — `hub.go` calls that block "the canonical sentinels" and the
>      reason set is now split across two files.
>
> **Task 10:** reflect `realtime.StreamFrame` — do NOT declare a parallel DTO. Its five fields and
> the deliberate absence of `project_id`/`correlation_id` are in the Task 6 block below. **It must
> reject the ambiguous `Filter` at the edge** (both selectors set, or neither): `Serve` does not
> police it, and neither-set today opens a stream that writes nothing and heartbeats forever.
> Decide how each of `Serve`'s three return shapes is logged — `nil` is a disconnect and must stay
> quiet, `ErrSlowConsumer` is expected traffic and not a fault, only write/source errors deserve
> error level. And `Serve` returns nil, not `CloseShutdown`, when a disconnect races hub close —
> do not build a shutdown-frame guarantee on the return value.
>
> **Task 11:** shutdown order is **cancel tailer → WAIT for `Run` to return → `Hub.Close()` →
> `srv.Shutdown`** (the plan omits the wait). `Tailer.Run` can return an error BEFORE it loops —
> treat that as failed startup. Apply `task-11-config-section.patch` from this directory; its brief
> does not mention it. `config.Realtime.SubscriberQueue` → `HubConfig.SubscriberQueue`;
> `HeartbeatInterval`/`ReplayBatch` → `StreamerConfig` (names already match);
> `newRealtimeEventSource(pool, cfg.Realtime.ReplayBatch)` for the snapshot cap. **Decide whether
> `stream.go`'s duplicated 20s/500 defaults get a compile-time tie-back to `config.Realtime` or
> stay a comment** — they are currently two independent literals.
>
> **Task 12:** docs step's "config knobs from Task 2" means Task 11. For the load test: none of the
> realtime query plans use the run/project indexes — `ORDER BY id … LIMIT n` makes the pkey
> cheapest and those predicates become filters. That is the shape to measure. Expect this and do
> not call it a bug: a long replay keeps the live queue filling and can evict the subscriber
> mid-catch-up; the client resumes from `Last-Event-ID` and makes progress each round, but a
> `SubscriberQueue` too small relative to `ReplayBatch` turns it into a reconnect loop.
>
> **NEVER add an `id <= watermark` predicate** to the snapshot's events query. It would make the
> isolation level irrelevant and silently un-falsify the acceptance test. Only a comment guards it.
>
> **METHOD — what has earned its keep, now over six tasks:**
>   1. **Pre-flight every brief against the real source before dispatching.** Defects found: Task 5
>      six, Task 6 eight, Task 7 eleven, Task 8 thirteen. The plan's `Publish` sketch panicked;
>      Task 7's acceptance test was unfalsifiable; **Task 8's `Serve` signature could not compile
>      and its required mutation would not have reddened its own test.**
>   2. **Demand a demonstrated mutation, never an assertion**, for any test-shaped finding — and
>      hold your own mutants to it. Task 8's implementer discarded one of its own as invalid
>      (it reddened nine tests on an unrelated assertion) and wrote a faithful one.
>   3. **Name `go test -overlay` as the mechanism in review dispatches** and forbid codegen
>      explicitly. A prohibition without a mechanism gets the obvious method — a reviewer edited
>      the repo mid-gate. **Never gate while a reviewer is working the worktree**, and never
>      commit during a gate run either: `scripts/gate` checks the sha but tests what is on disk.
>   4. **Give every subagent a named report FILE as its deliverable.** Chat replies arrive late,
>      out of order, or not at all.
>   5. **Let reviewers find what you already suspect.** Task 8's Important finding was one the
>      controller had spotted and deliberately withheld; pre-judging it would have cost the
>      independent confirmation and the root cause (the fake ignored its context).
>
> **THE PATTERN TO DISTRUST:** three times now a confident written explanation of *why something
> is safe* was exactly backwards — Task 5's `break` comment (the mutex was the guard), Task 7's
> cleanup-order comment (`t.Cleanup` is LIFO, so registering earlier runs LATER and the suite hung
> for its full timeout), and Task 8's `Serve` godoc promising `nil` on disconnect while two of its
> three phases returned a wrapped `context.Canceled`. Each appeared in the source AND the report.
> Read every "this is why it is safe" paragraph as a claim to falsify.
>
> **AND DISTRUST THE CONTROLLER'S OVERRIDES TOO.** Three implementer push-backs against my
> instructions have now been right on the merits (Task 7's query reuse, and Task 8's single-funnel
> shape and its `t.Cleanup` correction — the last settled by a counterfactual that printed
> `panic: test timed out after 15s`). Ask for reasons, not compliance.
>
> ⚠️ **The editor's language server produced FALSE compile errors on this worktree twice**, at both
> Task 8 shas. `go vet ./internal/realtime/...` exited 0 both times and the gate passed. Verify
> with the compiler, never the diagnostics.
>
> **Task 9's brief is NOT yet extracted or pre-flighted.** Run
> `scripts/task-brief <plan> 9`, then pre-flight it against the tree — that is the next
> controller's first job, and on this plan pre-flight has found 6, 8, 11 and 13 defects on the last
> four tasks.
>
> **Durability:** this directory is GITIGNORED. Mirrored to the tracked
> `docs/superpowers/ledgers/2026-08-10-caesar-10-realtime-sse.md` (refresh with `cp` + commit at
> each task boundary) and copied whole to `/Users/sofo/Projects/caesar/sdd-backups/plan10-latest/`
> outside the repo. Verified byte-identical at this handoff.

---

> **SUPERSEDED — session 58's handoff said RESUME AT TASK 8, which is now done and gated.
> Kept only for the carry-forward reasoning its index points at.**

---

> **SUPERSEDED — session 57's handoff. Resume instructions here are STALE (it says Task 5).
> Kept only for the Docker/gate operational notes.**
>
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
>      tailer and therefore every subscriber. **Also: Task 5's test CANNOT redeclare
>      `collector`, `eventually`, `logBuf`, `newLogger`, `assertQuiet`, `settle`, `patience`,
>      `startTailer`, `fakeSource`, `fakeWaker`** — `tailer_test.go` is `package realtime` and
>      owns them all. And any test that adds events after starting a tailer must use the
>      `seeded` channel, not a sleep. See the Task 4 carry-forward lines below.
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
Task 4: ⚠️ **CARRY-FORWARD TO TASK 5 — THE HUB TEST WILL NOT COMPILE IF IT REDECLARES THESE.**
  `tailer_test.go` is `package realtime` (internal), and already owns at package scope:
  `collector`, `eventually`, `logBuf`, `newLogger`, `assertQuiet`, `settle`, `patience`,
  `neverPolls`, `pollingCfg`, `startTailer`, `fakeSource`/`newSource`, `fakeWaker`. Task 5's
  hub test must **reuse** them, not redeclare them. Put this in Task 5's dispatch.
Task 4: ⚠️ **CARRY-FORWARD TO TASK 5 — the "adds racing the tailer's startup `Head` read"
  trap.** The implementer's first green run had three failures caused by a real bug *in the
  tests*: events added after `Run` starts can land before or after the one-time `Head` seed,
  so the test is non-deterministic. Fixed with a `seeded` channel the fake closes after the
  `Head` read. **Any future test in this package that adds events after starting a tailer has
  the same race** — reuse that channel rather than sleeping.
Task 4: ⚠️ **CARRY-FORWARD TO TASK 11 — `Tailer.Run` can return an error BEFORE it loops.**
  A `Head` failure at startup is deliberately fatal (`seed tailer cursor: %w`) rather than
  retried: falling back to cursor 0 would replay history (design constraint 1) and a silent
  retry would let the process report healthy while realtime is dead. Task 11 must treat that
  return as failed startup, not as a background goroutine exiting.
Task 4: ⚠️ **CARRY-FORWARD TO EVERY REMAINING TASK (5-12) — the goimports grouping trap.**
  Root cause of the gate failure at `0ea1a74`, found by the implementer: `.golangci.yml` sets
  `local-prefixes: github.com/giiorg/caesar/backend`, so **caesar imports need their own third
  group**, after stdlib and third-party. gofumpt does not check grouping, so
  `backend:fmt:check`, `go build` and `go vet` all pass while `backend:lint` fails. **Every
  remaining task creates fresh files that import caesar packages** — expect this, and run
  `bin/golangci-lint run ./internal/<pkg>/...` before claiming done.
Task 4: controller ruling — the implementer asked whether the sort comment should name "Task 7"
  literally, having written "the ORDER BY id in the EventSource implementation" instead.
  **Its wording stands.** A plan task number baked into a permanent source comment goes stale
  the moment Plan 10 ships; the substance (which component guarantees the ordering) is what
  the comment needed to carry. The re-review reached the same conclusion independently.
Task 4: the implementer killed **three** mutants on the first pass, not the two required — the
  extra one dropped the sort and reddened `DeliversInIDOrder`, showing both the mis-ordering
  and the cursor stranded at 11. Mutant 4 came in the fix round.

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

Task 5: fix round 1/5 (3 addressed, 0 open — unfalsifiable eviction-path deregistration, a false
  `break` comment, untested `Hub.Close` idempotence; commit `143038f`..`8c4bfc1`)
Task 5: complete (commits 2d4b288..8c4bfc1, review clean after 1 fix round) and **GATED:
  `GATE PASS … 8c4bfc1`, 11 lanes resolved unnarrowed.** Gated twice — once at `143038f` before
  review, once at the fix sha.
Task 5: implemented at `143038f`; **GATED BEFORE REVIEW: `GATE PASS … 143038f`, 11 lanes
  resolved unnarrowed.** Review: spec ✅, quality approved, 0 Critical, 1 Important, 4 Minor.
Task 5: **INTERFACE PRODUCED FOR TASKS 8/9/11** — `NewHub(HubConfig{SubscriberQueue int}) *Hub`,
  `(*Hub).Subscribe(Filter) *Subscription`, `(*Hub).Publish([]Event)` (the tailer's sink),
  `(*Hub).Close()`, `(*Subscription).C() <-chan Event`, `(*Subscription).Close(CloseReason)`,
  `(*Subscription).Err() error`. `CloseReason` is a named string type that **implements `error`**,
  so `errors.Is` matches a reason by value; `CloseSlowConsumer` and `CloseShutdown` exist,
  `ErrSlowConsumer` is an alias of the former and is **the only `Err*` alias there will be**.
  All of it in `hub.go` — no `subscription.go`, contra the plan's File Structure section.
Task 5: **the re-review re-ran the mutation rather than reading the fix** — mutant A (delete
  `h.remove(s)` from the eviction path) reddens the new assertion and ONLY it, the other eight
  tests still passing, which is exactly the silent-leak profile the finding described. It also
  checked the assertion sits BEFORE the test's explicit `Close`, or it would have been satisfied
  by the path `TestHub_UnsubscribeRemovesFromRegistry` already covers.
Task 5: **finding 3's guard is deliberately unfalsifiable against today's code and that is
  correct.** No mutation of the current `Hub.Close` reddens on the second call — the re-review
  answered the right question instead ("does it catch the fault class?") with mutant C, a
  `Hub.Close` that panics when called twice: it is the only test in the file that fails, because
  every other test closes at most once. Keep this distinction for future test-shaped findings.
Task 5: controller edit to `task-5-report.md` — the report's "Anything in the brief I found
  wrong" section still ended "The `break` is load-bearing", one line away from the frame that
  made it true (the plan's sketch). Added the qualifying clause myself; the report is read by
  later implementers and that sentence contradicted the corrected section 260 lines above.
Task 5: **the reviewer ran its own mutants** (`go test -race -overlay` against an unmodified
  tree) rather than trusting the report, and found what the implementer's three did not: the
  Important finding below. It also confirmed the implementer's three mutations were honest.
Task 5: **the mutex is the panic guard, NOT the `break`.** Both the implementer's comment
  (`hub.go:108-111`) and its report claimed the post-eviction `break` prevents a send on a
  closed channel. Demonstrably false — `continue` passes all nine tests, because `offer`
  re-checks `closed` under `s.mu`. The `break` is a performance property. Corrected in the fix
  round; **Tasks 8/9 must not inherit the wrong mental model of what makes this safe.**
Task 5: controller rulings on the implementer's three questions (the reviewer concurred with
  all three independently):
  1. **Keep both `ErrSlowConsumer` and `CloseSlowConsumer`** — the plan names the sentinel.
     **Task 9's `CloseForbidden` is a const and nothing else; no `ErrForbidden`.** `CloseReason`
     implements `error`, so `errors.Is` matches a reason by value and a new reason is one line.
  2. **No `*slog.Logger` in the hub.** Eviction is a real operational signal but adding a
     logger changes a constructor Task 12 calls — **Task 11 decides**, deliberately.
  3. **`Hub.Close()` is NOT synchronous with an in-flight `Publish`** and must not be made so —
     a wait there defeats the one rule this task exists to enforce. See the Task 11
     carry-forward below.
Task 5: **CARRY-FORWARD TO TASK 11 — the shutdown order gains a step the plan does not have.**
  The plan says "cancel the tailer's context and close the hub before `srv.Shutdown`". That is
  not sufficient: `Hub.Close()` returning does not mean the tailer has stopped calling the
  sink. Correct order is **cancel tailer → WAIT for `Run` to return → `Hub.Close()` →
  `srv.Shutdown`**. `Tailer.Run` returns an error and `tailer_test.go`'s `startTailer` already
  demonstrates waiting on it. Also: `config.Realtime.SubscriberQueue` wires straight into
  `HubConfig.SubscriberQueue`, and a zero is safe (falls back to 256).
Task 5: **CARRY-FORWARD TO TASK 8** — `Err()` is meaningful only AFTER `C()` has closed; read
  it after the drain. A handler that returns without `Close` is the one leak the hub cannot
  prevent, and `Subscribe`'s doc says so. `Filter` precedence (RunID wins; neither selector set
  selects nothing) must be rejected at the HTTP edge, not relied on.
Task 5: **CARRY-FORWARD TO TASK 9** — `CloseForbidden CloseReason = "…"` is the entire change.
  **First reason wins on close**, so a subscription already evicted keeps `CloseSlowConsumer`
  even if the handler then closes it — deliberate: the client must know to resume from
  `Last-Event-ID`.
Task 5: minor (deferred): `Subscribe` is never exercised concurrently with `Publish`/`Close` —
  every test subscribes serially. Cannot deadlock today (the one `h.mu → s.mu` nesting is on a
  subscription no other goroutine can reach yet), but a future task that closed a *registered*
  subscription while holding `h.mu` would create a real cycle and no test would notice.
Task 5: minor (deferred → Task 11): eviction is invisible to operators. A client losing its
  stream to a full queue is the signal that a queue is mistuned or a client is pathological,
  and only the client that lost the stream can currently observe it.

Task 7: complete (commits 7d57ab5..ceadbaf, review clean after 1 fix round) and **GATED:
  `GATE PASS … ceadbaf`, 11 lanes resolved unnarrowed, on a confirmed-clean tree.** The earlier
  `GATE PASS @ 0023257` was DISCARDED, not trusted — a reviewer's in-tree probe overlapped it; see
  the process-hazard section below.
Task 7: fix round 1/5 (4 addressed, 0 open — LIFO cleanup inversion hanging the suite, unenforced
  LIKE-metacharacter assumption, untested RunID precedence, undocumented `Replay` limit;
  commits `0023257`..`ceadbaf`)
Task 7: **THE ACCEPTANCE PROPERTY IS GENUINELY PINNED.** The reviewer re-ran
  `RepeatableRead`→`ReadCommitted` itself: it reddens the invariant **"every event in
  `Snapshot.Events` has `id <= Watermark.EventID`"** with `snapshot carries event 6 above its own
  watermark 5`. The controller's pre-flight item 3 designed that invariant precisely because the
  plan's own two assertions would have stayed green under read-committed.
Task 7: **DO NOT ADD an `id <= watermark` PREDICATE** to the snapshot's events query, ever. The
  transaction's snapshot view already implies it, and writing it out makes the isolation level
  irrelevant and the acceptance test unfalsifiable. Only a comment guards this — no test can
  detect its own defeat here. Flagged for the final review.
Task 7: read order inside the tx is **watermark first, then events** — the fail-safe direction.
  Under any weaker isolation the degradation is then a DUPLICATE (Task 8 dedups on id), never a
  gap. Postgres takes the RR snapshot at the first statement, so the watermark read is literally
  what pins the view the events read gets.
Task 7: **the SQL and `Filter.Matches` agree on every constructed input** — the reviewer built the
  disagreement cases (both selectors set, neither set, empty scope, admin scope, non-attention
  kind) and found none. Every predicate is in SQL; the only Go-side reorder is `slices.Reverse`
  over the rows the LIMIT already chose, which can neither hide nor invent a row.
Task 7: **CONTROLLER OVERRIDE ITEM 6 WAS WRONG; the implementer's deviation stands.** It added
  `ListRunEventsForRunAfter` (same shape **plus** a scope guard) rather than reusing the existing
  unguarded `ListRunEventsAfter`. Reusing it would have forced either Go-side scope filtering after
  the LIMIT — which breaks Task 8's pager — or reliance on an unstated invariant that every event
  of a run carries that run's project id. `ListRunEventsAfter` is untouched for its existing caller.
  The reviewer reached the same conclusion independently. **Both my override items that the
  implementer pushed back on turned out to be wrong in its favour.**
Task 7: **`ScopeFor` RULING — the deactivation check and the DB role read STAY.** It returns the
  empty (matches-nothing) scope for `active = false` and reads the role from the database rather
  than the session, so deactivation AND demotion both reach a live subscription. Grants survive
  deactivation, so role alone would keep a disabled admin's stream open forever. No divergence from
  `handleListApprovals`, which a deactivated user cannot reach at all.
Task 7: **CARRY-FORWARD TO TASK 9 — `ScopeFor` returns an ERROR (`ClassNotFound`) for a deleted or
  unknown user, not an empty scope.** The plan's Task 9 test 3 says a resolver error must "keep the
  last known scope and retry" so a DB blip does not look like a permissions change — but a
  `ClassNotFound` is NOT transient, and keeping the last known scope would stream to a deleted user
  forever. **Task 9 must distinguish the two.**
Task 7: **CARRY-FORWARD TO TASK 8** — every filtering predicate is in SQL, so a non-empty page never
  comes back empty and the pager cannot stop early. `Replay` with a non-positive `limit` yields an
  empty page and is a documented programming error (ruled: documented, NOT defaulted — config
  validation makes it unreachable and a surprising 500-row fallback is worse than a stated
  contract). Task 8 must pass `ReplayBatch`.
Task 7: **CARRY-FORWARD TO TASK 12 (load test)** — none of these plans use
  `run_events_run_id_idx` or `run_events_project_id_idx`: `ORDER BY id … LIMIT n` makes the pkey the
  cheapest path and the run/project predicates become filters. `ListAttentionEventsAfter` for a
  member with sparse grants and an old cursor removed 59 332 rows by filter. Bounded by the LIMIT
  and still an index scan, but it is the shape a load test should measure.
Task 7: **the second confident-but-backwards safety explanation in this plan.** The implementer's
  comment AND its report both claimed `t.Cleanup(g.Release)` registered first made a failing test
  safe; `t.Cleanup` runs **LIFO**, so registering earlier means running LATER, and `pgxpool.Close`
  ran first while the tracer held a pooled connection — `puddle.Pool.Close` then blocked on that
  connection's WaitGroup forever. A failing test burned the full 60s package timeout and buried its
  own message in a panic dump. Same pattern as Task 5's `break` comment. **Read every "this is why
  it is safe" paragraph as a claim to falsify, not to check.**
Task 7: minor (deferred): `Snapshot.Events` is a non-nil empty slice when a selector matches
  nothing but `nil` when no selector is set. Cosmetic; callers compare lengths.
Task 7: ⚠️ not re-verified by the review: the EXPLAIN transcript (the implementer's 120k-row test
  was deleted and not reconstructed) and Step 2's historical red. Both plausible, neither pinned.

### PROCESS HAZARD FOUND 2026-08-11 — a reviewer's mutation dirtied the tree DURING a gate run

Task 7's reviewer injected `t.Fatalf("REVIEW PROBE: …")` straight into
`backend/internal/app/realtimeports_integration_test.go` to test whether a failing acceptance test
can leave a goroutine parked on a pooled connection. **The question was one I asked it to ask; the
method violated its dispatch** (which said use a scratch directory and never modify a repo file).

**Why this is more than tidiness:** an 11-lane gate was running against this worktree at the same
time. `scripts/gate` verifies the worktree is at the named sha but **tests whatever is on disk**,
so a dirty tree during a gate run makes GATE PASS unfalsifiable — you cannot tell whether the
lanes saw the probe. The `0023257` gate was therefore re-run after the tree was clean.

**Rules that follow, for every future review dispatch:**
1. Name the mechanism, not just the prohibition — say "`go test -overlay`", because "do not modify
   repo files" alone leaves the reviewer to invent a way and it will pick the obvious one.
2. Forbid `task db:sqlc` in reviews explicitly: regenerating sqlc writes `queries.sql.go` into the
   repo, so an sqlc-shaped mutation cannot be run the obvious way at all.
3. **Do not run the gate while a reviewer is working the same worktree.** Gate first, then review,
   or re-gate after the tree is confirmed clean.
4. An unrun mutation reported honestly beats a run one that leaves the branch dirty — say so in the
   dispatch, so the reviewer has an honourable way out.

Task 6: fix round 1/5 (3 addressed, 0 open — untested `Heartbeat` flush, unpinned `WriteEvent`
  idle-reset, undocumented raw-`Kind` interpolation; commits `2b23b4d`..`510d9bb`)
Task 6: complete (commits c3c6fe5..510d9bb, review clean after 1 fix round) and **GATED:
  `GATE PASS … 510d9bb`, 11 lanes resolved unnarrowed.** Gated twice, at `2b23b4d` and the fix sha.
Task 6: **THE `StreamFrame` CONTRACT — TASK 10 REFLECTS THIS TYPE, DO NOT REDECLARE IT.** Defined
  in `realtime/sse.go` (controller ruling: defined here, not in Task 10, so the documented
  contract and the wire bytes cannot drift):
  `ID int64 json:"id"` · `RunID uuid.UUID json:"run_id"` · `Kind string json:"kind"` ·
  `Payload map[string]any json:"payload"` (renders `null` for a nil map — document as a nullable
  object) · `OccurredAt time.Time json:"occurred_at"` (RFC 3339). **`project_id` and
  `correlation_id` are deliberately ABSENT** — project scope is an authorization internal, not
  client data — and `TestWriter_FrameShape`'s exact-bytes assertion proves they never reach the
  wire. Task 10's `Response: StreamFrame{}` is `realtime.StreamFrame`.
Task 6: **CARRY-FORWARD TO TASKS 8/9 — `WriteEvent` and `Heartbeat` FLUSH THEMSELVES.** `Serve`
  must NOT call `Flush()` per event; `Flush()` exists only for direct writes to the underlying
  `ResponseWriter` (Task 9's terminal frame). This is a *tested* guarantee, not a note — the
  op-log assertion `["flush","write","flush"]` pins it, and it was deliberately upheld against
  the "over-pin" objection precisely so a future batching change has to renegotiate it explicitly.
Task 6: **CARRY-FORWARD TO TASK 8 — the Writer owns idleness** (controller ruling 1: the plan's
  `hb` and `clock` parameters were otherwise dead). `Heartbeat()` writes `: hb\n\n` **only when
  `clock().Sub(lastWrite) >= hb`** and is a nil-returning no-op otherwise; `lastWrite` is reset by
  connect, by every `WriteEvent`, and by every heartbeat that actually writes. So Serve just ticks
  and calls `Heartbeat()` — it must NOT track last-write itself or reset a ticker per event.
Task 6: **CARRY-FORWARD TO TASKS 9/10 — once `NewWriter` flushes the headers the HTTP status is
  committed (200) forever.** No later failure on that stream can become an HTTP error response.
  That is intended and it is *why* the plan says "403-shaped terminal **frame**" rather than a 403
  status. Documented in the source so the next reader meets it there.
Task 6: the Writer belongs to a **single goroutine** (Serve's loop) by documented contract and has
  no mutex — adding one would imply a sharing model that does not exist.
Task 6: `e.Kind` is interpolated raw into the `event:` line. **Ruling: documented, NOT sanitized.**
  Kinds are server-controlled constants (`runs/events.go`), never client input; a newline-bearing
  kind would inject arbitrary SSE fields, so any future path routing user-supplied kinds must
  sanitize upstream. A codec that silently rewrote a kind would be a worse failure than an
  obviously bad constant.
Task 6: the review's Important finding was demonstrated, not asserted — deleting `Heartbeat`'s
  `Flush()` left all five tests green, meaning the comment would sit in the `bufio` buffer and the
  idle tunnel would die silently. Both reviewer and implementer re-ran it after the fix.
Task 6: no deferred minors — all three findings were closed in the fix round.

### RULED BY SOFO 2026-08-11 — what `Snapshot.Events` contains (the other half of the Snapshot ruling)

The original `Snapshot` ruling left this open in its own comment: `Events []Event // or the run
projection`. The plan text (Task 7) says "read the run's **current projection**"; the V1 spec
(§4) says "snapshot-then-deltas **over run_events**" and "every snapshot carries the event-id
high-watermark it was computed at"; the ruled type says `Events []Event`. Two of three said
events. **RULING: events, bounded.**

`Snapshot(ctx, f)` returns **the events matching `f`, most recent N, ascending by id**, together
with the watermark — the SAME shape for both stream kinds. N defaults to 500 and is a field on
the `internal/app` adapter, wired from `config.Realtime.ReplayBatch` later; Task 7 does not read
config. Task 8 writes each returned event as a frame.

**Rejected:** per-run returning the whole unbounded history (a pathological run bursts the
client's first frame batch), and per-run returning a run projection while attention returns
events (two structurally different shapes for Task 8 to splice, and the ruled `Snapshot` type
would have had to change). The plan's "projection" wording is **superseded** — do not restore it.

### Task 5 pre-flight (controller, 2026-08-11, session 58) — SIX findings, all in the brief

Read against the tree at `1ed903a`/`2d4b288` before dispatching. Recorded in full as a
CONTROLLER PRE-FLIGHT block at the top of `task-5-brief.md` that overrides the plan text.
BASE for the review package is **`2d4b288`**.

1. **PLAN DEFECT — the `Publish` sketch panics.** After `h.evict(s, CloseSlowConsumer)`
   closes `s.ch`, the inner `for _, e := range evs` loop continues and executes
   `case s.ch <- e:` again → send on a closed channel. Plan Test 2 (queue 4, 1000 events)
   trips it on event 6. Must stop delivering to a subscriber the instant it is evicted.
2. **HAZARD the plan does not mention — `Publish` (tailer goroutine) races
   `Subscription.Close` (handler goroutine, Task 8) on the same channel.** A non-blocking
   send racing `close(ch)` is a panic, not a data race, so `-race` may not flag it. Required:
   check-and-send under the subscription's own mutex, closed under the same mutex (safe
   because the send is non-blocking). Added a test beyond the plan's five.
3. **PLAN GAP — `(*Hub).Close()` is missing from Task 5's interface list but Task 11 requires
   it** ("close the hub before `srv.Shutdown`") and Task 11's file list is `internal/app`
   only, so it cannot add it. **Ruling: land `Hub.Close()` here**, with `CloseShutdown`, plus
   the Subscribe-after-Close case — a request racing shutdown must not get a live
   subscription whose channel never closes, which is the exact hang Task 11 exists to prevent.
4. **The `CloseReason`/`Err()` contract is cross-task and underspecified.** Task 9 adds
   `CloseForbidden`; Task 8 reads the reason to pick a terminal frame. Required shape: named
   type, `Err()` nil while open then an `errors.Is`-matchable error identifying the reason,
   `ErrSlowConsumer` as a package-level sentinel by that exact name, adding a reason is one
   line. Representation left to the implementer.
5. **File-structure conflict:** the plan's File Structure section (line 55) lists a separate
   `realtime/subscription.go`; Task 5's file list does not. Ruling: everything in `hub.go`,
   per the task's own file list.
6. **`HubConfig.SubscriberQueue int`**, named to match `config.Realtime.SubscriberQueue`
   (`subscriber_queue`, default 256) which Task 11 wires straight in; same default when <= 0,
   following the `NewTailer` defaults ruling. Plus the two carry-forwards already on record:
   the `tailer_test.go` package-scope collision list, and the goimports grouping trap.

Also corrected in the brief: plan Test 5's "assert via a counter" (made concrete as a registry
check), and a note that `backend:test` already runs `-race` in the Taskfile recipe.

Task 8: implemented at `9108992` (stream.go + stream_test.go, 949 lines, no other file touched).
  **GATED BEFORE REVIEW on a confirmed-clean tree: `GATE PASS … 9108992`, 11 lanes resolved
  unnarrowed, contracts clean.** Review in flight.
Task 8: **PRE-FLIGHT FINDING 5 CONFIRMED BY MUTATION, and it is the one that mattered.** Under
  mutation 1 (`Subscribe` moved after `prime`) the duplicate event STILL arrived exactly once via
  `Replay` — so the plan's test 2, written as the plan sketched it, would have stayed green under
  the very mutation the plan names as its acceptance evidence. Only the added `liveOnly` event
  (delivered by no path but the hub) reddens it. **Second task running where the plan's stated
  acceptance test proved nothing as written** — Task 7's would have passed under read-committed.
Task 8: the implementer reports all 13 pre-flight findings held against the tree and nothing in
  the brief was wrong; two clarifications only (report §6). It added 5 tests beyond the brief's 7,
  one per otherwise-untested ruling: zero-config usable, non-flusher rejected before subscribing,
  subscription closed on a write error, and the replay spin guard.
Task 8: review at `9108992` — spec ✅ (all 13 pre-flight findings honoured, checked against the code
  and not the report), quality "Needs fixes": 0 Critical, 1 Important, 4 Minor. **The reviewer ran
  every probe through `go test -overlay` with copies in a scratch dir outside the repo, and left
  `git status` empty — the Task 7 hazard did not repeat.** It also tried to falsify the pre-flight
  block itself and could not, checking findings 11 and 12 against `internal/app/realtimeports.go`.
Task 8: **THE IMPORTANT FINDING — `Serve`'s godoc was false for the entire prime/replay window.**
  It promises "nil when ctx is done"; that held only inside `follow`. A disconnect during the
  snapshot or the catch-up returned `snapshot: context canceled`. **Root cause is the fake:
  `streamSource` ignores its context entirely, so no test in the file COULD exercise a cancelled
  read — structurally invisible, not merely uncovered.** Third instance on this plan of a
  confident doc/comment that the code did not keep.
Task 8: **controller ruling — fix the CODE, not the doc.** Catch-up is expected to be long (the
  source's own comment says a long replay can evict the subscriber), so a tab closed mid-catch-up
  is ordinary, and the report's Task 10 carry-forward would have routed it to error level — the
  exact outcome the ruling exists to prevent. Guard on `ctx.Err() != nil`, NOT
  `errors.Is(err, context.Canceled)`: a source may translate the cancellation into its own error
  class, and the question Serve asks is "is there still a client to serve?". Stated cost: a genuine
  source failure racing a disconnect reports nil. Accepted — nobody is left to serve, and a real
  outage shows on every other stream and in the tailer. **A write error is deliberately NOT
  collapsed into this**; it can be a real fault.
Task 8: **two Minors PROMOTED into the fix round, both demonstrated-green mutants.** (a) Finding
  9's "no per-event `Flush()`" had no test at all — adding `st.wr.Flush()` to `send` left the whole
  suite green. Promoted because of what it is a link in: Task 6 pinned the op-log
  `["flush","write","flush"]` deliberately against an "over-pin" objection, so a future batching
  change must renegotiate it — and the chain broke exactly here. (b)
  `TestServe_RejectsANonFlusherWithoutSubscribing` runs on `context.Background()` outside the
  cleanup guard, so a regression would HANG the package to its timeout instead of failing — the
  Task 7 failure mode again. Renamed too: moving `NewWriter` after `Subscribe` leaves the suite
  green, so the name claimed a pin it did not have (finding 10 is a preference, not an invariant,
  as the brief itself says).
Task 8: **`closeClientGone` RATIFIED** (controller + reviewer independently). Passing
  `CloseShutdown` — "hub shut down" — on a closed browser tab would put a false statement in the
  data, and `hub.go:11-14` designs for a new reason costing one const. **Carry-forward: when a
  later task next opens `hub.go`, move it into that const block or leave a pointer** — `hub.go`
  calls its own block "the canonical sentinels" and the set is now split across two files.
Task 8: ⚠️ the review's three cannot-verify items, all resolved by the controller, none a gap.
  (a) `config.Realtime`'s defaults/field names are unverifiable because the section arrives with
  Task 11 — controller-verified against `task-11-config-section.patch` at pre-flight:
  `HeartbeatInterval` 20s and `ReplayBatch` 500, names identical. **Task 11 should decide whether
  the duplicated defaults get a compile-time tie-back or stay a comment.** (b) `-race -count=20`
  and lint were not re-run by the reviewer by instruction; the 11-lane GATE PASS covers both lanes.
  (c) The ambiguous `Filter` (neither selector set) opens a stream that writes nothing and
  heartbeats forever — **Task 10 must reject it at the edge**; now a real obligation, not theory.
Task 8: minor (deferred): error-string stutter — `snapshot: app: realtime snapshot: begin: …`;
  the cursor value realtime adds is new information, the bare noun is not. Folded into fix round 1
  as the lowest-priority item since it touches the same lines.
Task 8: fix round 1/5 (4 addressed, 0 open — false ctx contract in prime/replay, untested
  no-per-event-Flush guarantee, a test that hung rather than failed, error-string stutter;
  commits `9108992`..`8f1a133`). 14 TestServe functions / 21 with subtests.
Task 8: **GATED AT THE FIX SHA on a confirmed-clean tree: `GATE PASS … 8f1a133`, 11 lanes
  resolved unnarrowed, contracts clean.** Gated twice — `9108992` before review, `8f1a133` after
  the fix round. Scoped re-review of `9108992..8f1a133` dispatched afterwards, never during.
Task 8: **BOTH OF THE IMPLEMENTER'S PUSHBACKS WERE RIGHT, AND ONE OF MY ITEMS WAS WRONG.**
  (a) It used ONE translation point around all three phases instead of my two call-site guards.
  Reason: two guards leave `follow` open, and that case is *correlated, not rare* — on a real
  disconnect the cancelled ctx and the failing write are the SAME event, so `follow`'s select is a
  coin-flip on writing a queued event to a dead socket and returning broken-pipe where the contract
  promises nil. Strictly better than what I specified; also one funnel for the phase Task 9 adds.
  **RATIFIED.** (b) **My item 3 was wrong**: `context.WithCancel` + `t.Cleanup(cancel)` does NOT
  fix a hang when the call is synchronous — a blocked test function never reaches Cleanup, so the
  package still dies on the suite timeout. It kept the cancellable ctx AND moved the call behind
  `waitServe`; falsified rather than argued (a Flusher recorder now fails in 2.00s). **That is the
  third controller override an implementer has correctly refused on this plan.**
Task 8: **THE `ctx.Err()` RULING WAS LOAD-BEARING, NOT STYLISTIC — and the evidence is accidental.**
  The replay mutant printed `stream replay from 1: streamSource: read aborted`: the fake reports a
  cancelled read as its OWN error class, wrapping nothing. An `errors.Is(err, context.Canceled)`
  guard would have left that half broken while looking fixed. Keep the "is there still a client to
  serve?" framing over "which error did the source pick?".
Task 8: **a mutant that FAILED as evidence, discarded honestly.** Closing the subscription at the
  top of `deliver` reddened nine tests on the close-reason assertion without touching the ordering
  property — so it proved nothing about what it claimed. Replaced with a faithful one that
  reproduced round 1's exact result, confirming the refactor did not weaken the ordering pins.
  **This is the standard: a mutant that reddens the wrong thing is not evidence.**
Task 8: the finding-13 lint lane bit again, in a NEW way — revive's `context-as-argument` on a test
  helper (ctx must come first). Invisible to gofumpt, `go build` and `go vet`. The trap is not only
  goimports grouping: **only `backend:lint` sees this whole class.**
Task 8: ⚠️ **the LSP produced FALSE compile errors twice, at both shas** — 10 `undefined: Streamer`
  at `9108992`, then 6 argument-order errors at `8f1a133` (it held the old `startServeOn` signature
  against the new call sites). Both times `go vet ./internal/realtime/...` exited 0 and the gate
  passed. **Verify with the compiler, never the editor diagnostics, on this worktree.**

Task 8: **complete (commits `e7053ca`..`8f1a133`, review clean after 1 fix round) and GATED:
  `GATE PASS … 8f1a133`, 11 lanes resolved unnarrowed, on a confirmed-clean tree.**
Task 8: **the re-review re-ran every mutation itself rather than reading the fix**, all through
  `go test -overlay` from a scratch dir, leaving `git status` empty. Its decisive check was one
  nobody asked for: substituting `errors.Is(err, context.Canceled)` for `ctx.Err() != nil` leaves
  the snapshot subtest GREEN and reddens only the replay subtest — proving the fake's asymmetry is
  real (`errAbortedRead` wraps nothing, `stream_test.go:72`) and that the `ctx.Err()` ruling is
  load-bearing rather than stylistic. **An `errors.Is` implementation would have looked fixed.**
Task 8: **the re-review ran the COUNTERFACTUAL on the controller's wrong instruction** — the same
  regression against the pre-fix inline shape, keeping the cancellable ctx and `t.Cleanup`:
  `panic: test timed out after 15s`. My item 3 alone would not have fixed the hang; moving the
  call behind `waitServe` is what did. Settled by experiment, not by concession.
Task 8: the single-funnel deviation was **ratified independently**, and the out-of-scope boundary
  was verified rather than trusted: under an unconditional `return nil`,
  `TestServe_ClosesTheSubscriptionOnAWriteError` reddens — so a write failure with the client
  still connected is held to an error BY A TEST, not by a comment. The re-review also re-ran round
  1's ordering mutation in the new shape: same three tests red, so the `deliver` funnel did not
  weaken the ordering pin.
Task 8: minor (deferred → **TASK 9 MUST READ THIS**): `TestServe_AddsNoFlushOfItsOwn` never
  reaches the live path — its stream ends on a closed subscription channel, so **a `Flush()` added
  only in `follow`'s event case survives** (verified green under that mutant). Task 9 edits
  `follow` and adds a terminal frame that legitimately calls `Writer.Flush()`, so this is the one
  deferred minor with a live consumer. Closing it costs one published event before `hub.Close()`
  and `"write","flush"` appended to `want`.
Task 8: minor (deferred): `stream replay from 1: app: realtime replay after 1: …` still repeats
  the verb; keeping the cursor is contractually right, the verb is noise.
Task 8: minor (deferred → Task 10): `Serve` returns nil rather than `CloseShutdown` when the
  request ctx is cancelled at the same moment the hub closes. Correct per the godoc — **do not
  build a shutdown-frame guarantee on the return value alone.**

### Task 8 pre-flight (controller, 2026-08-11, session 59) — THIRTEEN findings, all in the brief

Read against the tree at `e7053ca`/`ceadbaf` before dispatching. Recorded in full as a
CONTROLLER PRE-FLIGHT block at the top of `task-8-brief.md` that overrides the plan text.
BASE for the review package is **`e7053ca`**. Pre-flight has now found 6, 8, 11 and 13
defects on the last four tasks.

1. **`SnapshotWatermark` does not exist** — the plan's ordering list, test 1 and Step 5 all
   name it. And "write the snapshot **frame**" (singular) is superseded by sofo's events
   ruling: `Snapshot.Events` is up to 500 events ascending; write each as its own frame.
2. **PLAN DEFECT — `realtime.Serve(ctx, w, req)` as a package-level function cannot work.**
   No Hub, no EventSource, no `ReplayBatch`, no heartbeat interval. Plan Task 9 already
   contradicts it ("the resolver is a `Serve` dependency", `cfg.GrantRecheck`). **Ruled:
   `NewStreamer(hub, src, StreamerConfig{HeartbeatInterval, ReplayBatch}) *Streamer` with
   `Serve` a method**, field names matching `config.Realtime` per the Task 11 patch.
3. **`NewStreamer` must default a non-positive config** — `time.NewTicker(d)` PANICS for
   `d <= 0`, and a non-positive `ReplayBatch` yields the empty page `ports.go:20-27` says a
   pager cannot distinguish from "caught up". Same house pattern as `NewTailer`/`NewHub`/
   `newRealtimeEventSource`.
4. **The dedup floor must start at the CURSOR, not 0.** The plan's "highest id already
   written" collapses on an empty `Snapshot.Events` or a caught-up resume. Floor is
   `max(cursor, highest written)`, seeded from the watermark (or `LastEventID`). The
   invariant to state: **ids on the wire are strictly ascending** — that is what makes
   `Last-Event-ID` correct.
5. ⚠️ **PLAN DEFECT, the important one — test 2 as sketched stays GREEN under Step 5's own
   mutation.** Moving `Subscribe` after `Snapshot` causes LOSS, and loss is observable only
   for an event whose *only* path is live; an event that also returns from `Snapshot` or
   `Replay` still arrives exactly once under the mutant. Required design: TWO events, both
   published from inside the fake's `Snapshot` (the only window the mutation crosses — a
   publish inside `Replay` lands after `Subscribe` even when mutated): `dup` (returned by
   `Replay`, asserts exactly-once) and `liveOnly` (returned by nothing, asserts present).
   Three mutations required at Step 5, each with the test names it reddens.
6. **Name collisions** — `stream_test.go` is `package realtime` and 24 identifiers are
   already taken across the three existing test files. Listed in the brief. **`fakeSource`
   must not be taught `Snapshot`/`Replay`/`Oldest`** — those are deliberate
   `NotImplemented` stubs whose comment forbids exactly that; declare a new fake. REUSE
   `eventually`, `registryLen`/`registryHas`, `hubEvent`, `patience`, `settle`.
7. ⚠️ **`sseRecorder` is not concurrency-safe and Step 4 runs `-race -count=20`.** Task 6
   had one goroutine; Task 8 has `Serve` writing while the test inspects. Guaranteed race.
   Required: a mutex-guarded recorder in `stream_test.go`. Also recorded: a closed channel
   still yields its buffered values, so `Publish(...)` then `hub.Close()` drives `Serve` to
   completion deterministically without sleeping.
8. `Serve` must `defer sub.Close(...)` on every return path (the one leak the hub cannot
   prevent); `Err()` read only after `C()` closes; first close reason wins.
9. Writer owns flushing AND idleness: no per-event `Flush()` (tested op-log), no last-write
   tracking, plain ticker → `Heartbeat()`. Idle heartbeat lands in `[hb, 2*hb)` — must be
   commented, since the next person to tighten a proxy timeout needs to meet that fact.
10. **`NewWriter` first, then `Subscribe`** — it reads no events so nothing can be missed,
    and it is the one error that precedes the committed 200. The invariant is `Subscribe`
    strictly before `Snapshot`, which is what test 1 pins.
11. Pager: always pass `cfg.ReplayBatch`; may rely on Task 7's "every predicate is in SQL";
    must guard the spin (a non-empty page whose max id does not exceed the cursor).
    A long replay evicting the subscriber is correct behaviour, not a bug.
12. **`Oldest()` comparison is conservative by one** — lossless resume needs
    `LastEventID+1 >= oldest`; the plan re-snapshots one boundary case unnecessarily.
    **Ruled: the plan's form stands** (errs toward a duplicate, not a gap — same
    degradation direction as Task 7's read order), documented as deliberate. Also
    controller-verified: **nothing prunes `run_events`**, so the branch is unreachable in
    production and only a fake exercises it.
13. The goimports grouping trap, again.

Two smaller rulings: `StreamRequest.UserID` is unused by Task 8 and stays, documented as
Task 9's input; `Serve` returns **nil** on `ctx.Done()` (a disconnect is the normal end of
an SSE request and Task 10 must not log every one as an error), `sub.Err()` on close, the
write error on a write failure. One test ADDED beyond the plan's six:
`TestServe_HeartbeatsWhileIdle` — nothing in the plan exercises Serve's ticker wiring, and
a ticker never started would pass all six.

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

