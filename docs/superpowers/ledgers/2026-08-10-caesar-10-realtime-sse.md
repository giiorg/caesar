# SDD ledger — plan: docs/superpowers/plans/2026-08-10-caesar-10-realtime-sse.md

> **HANDOFF 2026-08-12 (session 60) — ALL TWELVE TASKS IMPLEMENTED. RESUME AT THE TASK 12 REVIEW.**
> This block supersedes every handoff below it; they are kept only for their reasoning.
>
> Worktree `/Users/sofo/Projects/caesar/repos/caesar/.claude/worktrees/caesar-10-realtime-sse`,
> branch `caesar-10-realtime-sse`, last CODE commit **`6016635`**, clean tree.
> `main` untouched at `64de023`. **Verify with `git log` before acting on any of this.**
>
> **Tasks 1-11 complete, reviewed and GATED. Task 12 is implemented and GATED but NOT REVIEWED** —
> the session was paused on context, not on a problem.
>
> Gated shas, all 11 lanes unnarrowed: Task 3 `78a4346` · 4 `1ed903a` · 5 `8c4bfc1` · 6 `510d9bb` ·
> 7 `ceadbaf` · 8 `9108992`+`8f1a133` · 9 `3ebfb89`+`aa04f0a` · 10 `bb9e4e6`+`fc52b57`+`00030da` ·
> 11 `17e76b7`+`80579d9`+`01c4038` · 12 `6016635`.
>
> ## THE NEXT THREE THINGS, IN ORDER
>
> **1. Review Task 12** (`task-12-brief.md`, report at `task-12-report.md`, BASE **`dd1b94a`**).
> It is filed DONE_WITH_CONCERNS with four concerns, all substantive — see the Task 12 entries at
> the bottom of this file. Two of the brief's mutation rows could not redden anything and were
> replaced; my eviction estimate was out by three orders of magnitude; and one README limitation
> was narrowed. **Also weigh finding 13 (port discovery by log-line parsing), which the implementer
> accepted rather than hardened, with reasons I find fair but did not adjudicate.**
>
> **2. Then the FINAL WHOLE-BRANCH REVIEW** — most capable model, `MERGE_BASE = git merge-base main
> HEAD`, using `superpowers:requesting-code-review`'s `code-reviewer.md`. It has a real queue:
>
> - **Task 9 minors:** a false "can only be raised from inside `follow`" comment in
>   `stream_test.go`; the bound-precision wording at `stream.go:62-71`.
> - **Task 10 minors:** `stream.go:75`'s "Nothing in this tree implements `FlushError`" (falsified
>   by `deadPeerWriter` in the same commit); `ErrNotStarted`'s "every other ending" list omitting
>   the untagged flush; an "empty header map" claim that holds only because no caller sets headers.
> - **Task 11 minors:** `realtimeports.go:64-67` and `defaults_test.go:28-31` overstate why that
>   tie-back is special; and a `Warn` when the shutdown wait runs long (new behaviour, deliberately
>   not folded into a fix round).
> - **THE ONE OPEN DESIGN QUESTION:** a per-run stream whose project is revoked while the principal
>   keeps other grants can only be closed by an event it must deny, so over a QUIESCENT run it
>   stays open and idle forever. Confidentiality-safe (nothing denied reaches the client);
>   liveness only. **It is solvable** — `runScoped` (`runs/api/api.go:252-272`) already loads the
>   run's task and authorizes against `task.ProjectID`, so feeding that through `ServeRunStream`
>   would close it. I ruled it out of Tasks 10 and 12 to keep them reviewable. **The footgun if
>   you build it: a field a caller forgets is `uuid.Nil`, which no scope allows, so every per-run
>   stream would close on its first re-check.** Sofo's call.
>
> **3. Then `superpowers:finishing-a-development-branch`.**
>
> ## WHAT THE PLAN DID NOT KNOW — still live
>
> **NEVER add an `id <= watermark` predicate** to the snapshot's events query. It would make the
> isolation level irrelevant and silently un-falsify Task 7's acceptance test. Only a comment
> guards it.
>
> **The contract seam takes ONE response schema per content type.** Task 10's `altResponses` map in
> `contracts` (mirroring `requestOverrides`) is what gives `listRunEvents` both `application/json`
> and `text/event-stream`; `httpx.Operation` is deliberately untouched. A second SSE frame shape
> cannot be reflected as a type, which is why the 403-shaped terminal frame is documented in prose.
>
> **`runsapi.StreamServer`'s contract is narrower than `realtime.Serve`'s** — *error ⟹ nothing
> committed*. `realtime.ErrNotStarted` is the discriminator and is narrowed to exactly
> `http.ErrNotSupported`. An adapter that FORWARDS `Serve`'s error writes a problem document into a
> committed SSE body, and `fakeStreamServer` cannot catch it.
>
> ## METHOD — what earned its keep over twelve tasks
>
> 1. **Pre-flight every brief against the real source before dispatching.** Defects found per task:
>    6, 8, 11, 13, 21, 17, 12, 14. Several were plan defects that would have shipped.
> 2. **Demand a demonstrated mutation, never an assertion** — and hold your OWN mutants to it.
>    **Four tasks' named acceptance mutations proved nothing as written (7, 8, 9, 12).**
> 3. **Name `go test -overlay` as the mechanism** in every review dispatch, require scratch copies
>    OUTSIDE the repo, and forbid codegen explicitly. `git status` stayed empty all session.
> 4. **Never gate while a reviewer works the worktree, and never commit during a gate run.**
>    Gate every commit yourself, at a LITERAL sha — `scripts/gate` refused once this session on a
>    sha that did not exist, which is exactly what it is for. Passing `HEAD` would have hidden it.
> 5. **Give every subagent a report FILE.** Chat replies arrive late, out of order, or not at all;
>    four subagents went idle without reporting and one direct `SendMessage` retrieved each one.
> 6. **Ask questions instead of asserting answers.** The single most productive thing this session:
>    framing an untested suspicion as a question surfaced the quiescent-run residue that my own
>    framing hid.
>
> ## THE TWO PATTERNS TO DISTRUST
>
> **A confident written explanation of *why something is safe* has been exactly backwards seven
> times**, each time appearing in BOTH the source and the report. And on Tasks 10 and 11, **a
> comment written to FIX a false comment was itself false in every round** — the class does not
> converge through fix rounds, which is why the residue above is routed to a fresh whole-branch
> read instead.
>
> **AND DISTRUST THE CONTROLLER.** **Nine of my items have now been wrong and the implementer was
> right every time** — including a finding that prescribed a CI-failing arch violation, a shutdown
> bound that would have moved the hang rather than removed it, and two mutation rows that could not
> redden anything. Ask for reasons, not compliance; give every implementer an explicit licence to
> dissent.
>
> ⚠️ **The editor's language server produces FALSE compile errors on this worktree — SIX times this
> session**, while `go build`, `go vet` and the full gate were clean. Verify with the compiler.
>
> **Durability:** this directory is GITIGNORED. Mirrored to the tracked
> `docs/superpowers/ledgers/2026-08-10-caesar-10-realtime-sse.md` and copied whole to
> `/Users/sofo/Projects/caesar/sdd-backups/plan10-latest/`. Verified byte-identical at this handoff.

---

> **SUPERSEDED — session 59's handoff said RESUME AT TASK 9; Tasks 9-12 are now implemented.
> Kept only for its carry-forward reasoning.**
>
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


---

### Task 9 pre-flight (controller, 2026-08-12, session 60) — TWENTY-ONE findings, all in the brief

Read against the tree at `fc007e0` before dispatching. Recorded in full as a CONTROLLER
PRE-FLIGHT block at the top of `task-9-brief.md` that overrides the plan text. BASE for the
review package is **`fc007e0`**. Pre-flight has now found 6, 8, 11, 13 and 21 defects on the last
five tasks.

Task 9: **the plan's per-run close condition is NOT COMPUTABLE** — a new defect, not on the
  handoff's list. The plan says close when the refreshed scope "no longer covers that run's
  project", but `Filter` carries `RunID` and never the run's project (`realtime.go:74-78`), and an
  admin's per-run Filter is `Scope{All:true}` with no project id at all. `Serve` cannot learn a
  run's project until an event for it arrives. Replaced with the computable rule: close on an
  EMPTY refreshed scope (the only thing that can close an idle per-run stream, and it covers both
  shapes); on a per-run stream also close on the first event the refreshed scope denies; on an
  attention stream drop that event and keep streaming.

Task 9: **the handoff's known trap confirmed and then WIDENED.** The plan's test 3 ("a transient
  resolver error keeps the last known scope and retries") is not merely wrong about
  `ClassNotFound` — kept as written it is a FAIL-OPEN: a resolver failing indefinitely leaves the
  stream running on a scope nobody can confirm, in the one task whose purpose is failing closed.
  Ruled: `maxGrantFailures = 3` consecutive failures → terminal frame + **the wrapped resolver
  error**, not `CloseForbidden`. 90s at the 30s default.
Task 9: **deliberately NOT classifying `xerrors.ClassNotFound` as terminal**, against the
  handoff's item 2. Verified there is no user-deletion path in the codebase (no `DeleteUser`, no
  `DELETE FROM users`); deactivation returns `Scope{}` with a **nil** error
  (`realtimeports.go:288-290`), which is already the one-interval close. The failure cap closes
  the unknown-user case anyway AND covers failure modes a class test misses (a resolver stuck on
  `ClassInternal`). Importing `xerrors` into `realtime` for an error-class contract
  `GrantResolver` does not document and no fake here could verify buys nothing. **Revisit if a
  later task adds user deletion.**
Task 9: `CloseForbidden CloseReason = "not authorized"`, and it means authorization was CHECKED
  and denied — never the resolver-outage path. Putting "not authorized" in the data when the truth
  is "we could not ask" is the same false statement `closeClientGone` exists to avoid, and it
  would hand Task 10 a routine revocation and a real fault under one label. **Ruled into
  `hub.go`'s const block** — that block calls itself "the canonical sentinels" and anticipates the
  addition; `closeClientGone` stays in `stream.go` with a one-line pointer added to hub.go, which
  closes the Task 8 carry-forward.
Task 9: **must NOT mutate `Subscription.filter`** to push the new scope into the hub — `Publish`
  reads `s.filter` on the tailer goroutine holding no subscription lock (`hub.go:105`), so it is a
  race `-race` will flag. Accepted consequence, to be stated in a comment: denied events still
  occupy queue slots and can still evict the client as a slow consumer.
Task 9: a dropped event must **not advance `st.cursor`** — the cursor is the client's
  Last-Event-ID, and advancing past an event it never saw makes a later resume skip real events.
Task 9: **no resolve before the first tick** (start from `req.Filter.Scope`; the handler resolved
  it moments ago) and **re-resolve in the ticker case ONLY**, never per event — the per-event work
  is a pure in-memory `allows` test. Both pinned by tests.
Task 9: terminal frame pinned to the byte: `event: forbidden\ndata: {"error":"forbidden"}\n\n`,
  via `fmt.Fprint(st.wr.w, …)` + `st.wr.Flush()` — exactly what `sse.go:127-130` built `Flush` for,
  and still its only caller. **No `id:` line**: ids on the wire are the client's resume point and
  this is not an event. No new method on `sse.go` — the frame is stream policy, not codec.
Task 9: `NewStreamer` gains the resolver as its third parameter and **PANICS on a nil one**.
  Silently never re-checking is the exact fail-open the task closes, and a nil interface would
  otherwise panic per-request in a handler goroutine rather than once at process start. Precedent:
  `contracts.go:196`, `workflows/graph.go:145`, `pg/migrate.go:25`. Three call sites, all in
  `stream_test.go` (251, 669, 699); nothing in `internal/app` builds a Streamer yet.
Task 9: `defaultGrantRecheck = 30s`, **verified against `task-11-config-section.patch`** rather
  than guessed, and defaulted in `NewStreamer` because `time.NewTicker` panics on <= 0.
Task 9: the Task 8 deferred minor with a live consumer is folded in — `TestServe_AddsNoFlushOfItsOwn`
  never reaches `follow`, the exact function this task edits, so it is extended (one live event
  before `hub.Close()`, `want` gains `"write","flush"`) BEFORE a legitimate `Writer.Flush()` caller
  is added.
Task 9: the brief carries an executable **mutation table — eight rows, one per test-shaped
  claim** — each to be run through `go test -overlay` from a scratch dir outside the repo with the
  real output pasted into the report. Same standard as Task 8: a mutant that reddens the wrong
  assertion is not evidence.
Task 9: implementer dispatched on Opus 5 at BASE `fc007e0`, with an explicit licence to dissent.

Task 9: implemented at `3ebfb89` — `stream.go` +196, `stream_test.go` +353, `hub.go` +11,
  `realtime.go` +5. **GATED BEFORE REVIEW on a confirmed-clean tree: `GATE PASS … 3ebfb89`,
  11 lanes resolved unnarrowed, contracts clean.** Review in flight.
Task 9: the implementer reports **all 21 pre-flight findings honoured**, and it ran the eight-row
  mutation table plus **three mutations of its own** for behaviours the table left unpinned — the
  `failures = 0` reset, `CloseForbidden` vs the wrapped resolver error on the outage path, and the
  absent `id:` line on the terminal frame.
Task 9: **FINDING 14 VERIFIED AS A COUNTERFACTUAL RATHER THAN ASSUMED.** It overlaid the m7
  mutant (a stray `Flush()` in `follow`'s event case) together with the PRE-Task-9 form of
  `TestServe_AddsNoFlushOfItsOwn` and the pin PASSED. Task 8's deferred minor was exactly right,
  and the extension is what makes m7 fail. That deferred minor is now closed.
Task 9: ⚠️ **MY FINDING 7's PRESCRIPTION WAS RIGHT AND ITS STATED REASON WAS WRONG — the fourth
  controller error on this plan.** I wrote that a dropped event must not advance `st.cursor`
  because "the cursor is what the client stores as Last-Event-ID". It is not: `st.cursor` is
  per-`Serve` state that dies with the stream, and the client's resume point is the last `id:`
  line that actually reached the wire. Within `follow` the cursor only gates `e.ID <= st.cursor`
  and live ids ascend strictly, so advancing it on a drop is unobservable in every direction —
  which is also why no mutation for it exists. The implementer **implemented the prescription and
  refused the justification**, writing the factual reason into the comment instead. That is the
  right call for a package whose comments are load-bearing, and it is exactly the behaviour the
  dissent licence exists to produce.
Task 9: two more comments softened after the implementer failed to falsify them — finding 6's
  "`-race` WILL flag it" (the race is real; whether the detector observes it on a given run is
  not guaranteed) and finding 12's kind-collision claim, now cited to `internal/runs/events.go:18-43`,
  which defines every kind, rather than to `realtime.go`, which lists only the three attention
  prefixes. **Pattern worth keeping: the implementer weakened three claims it could not prove
  rather than inheriting the controller's confidence.**
Task 9: **finding 19 earned its place immediately** — `task backend:lint` rejected a tree that
  `go build`, `go vet` AND `go test -race` all passed (gofumpt, one element per line in a
  multi-line composite literal, `stream_test.go:1033`). Third distinct way the lint lane has
  caught what the compiler cannot on this plan.
Task 9: ⚠️ the LSP produced FALSE compile errors a THIRD time — 11 of them at `3ebfb89`, all
  holding the pre-Task-9 `NewStreamer` signature against the new call sites. `go build ./...` and
  `go vet ./internal/realtime/...` both exited 0 and the gate passed. **This is now a reliable
  property of this worktree, not an anomaly.**

Task 9: review at `3ebfb89` — spec ❌ (one missing requirement), quality "Needs fixes": 0 Critical,
  1 Important, 4 Minor, 3 cannot-verify. All 21 pre-flight findings verified honoured against the
  CODE (file:line for each, not the report), all eight mutation rows checked out, and the reviewer
  left `git status` empty, running its own probes through `go test -overlay` from a scratch dir.
Task 9: **THE IMPORTANT FINDING IS THE CONTROLLER'S PRE-FLIGHT GAP, NOT THE IMPLEMENTER'S ERROR.**
  The re-check ticker lives only in `follow`; `prime` and `replay` never re-resolve, so the
  staleness window is `catch-up duration + GrantRecheck` rather than `GrantRecheck` — and replay
  reads up to the CURRENT HEAD, so it emits rows committed AFTER the revocation. The owner's
  locked ruling (plan pre-flight finding 8) bought exactly that bound: "on an interval and before
  each delivery batch … bounded by the re-check interval and must be configurable." A replay page
  is the most literal delivery batch in the design — `ReplayBatch`'s own doc calls it a batch.
  **My finding 9 forbade a per-EVENT poll and was silent on replay pages**, so the plan text stood
  there and the implementer built exactly what the brief said. I had considered this boundary at
  pre-flight, ruled to keep the re-check in `follow`, and then wrote NEITHER the ruling NOR the
  boundary comment into the brief — so the code shipped two docs asserting a bound it does not
  deliver. **A pre-flight ruling that stays in the controller's head is not a ruling.**
Task 9: my deferral reasoning was also weak on the merits — I told myself the window was bounded
  by catch-up, which is itself bounded by slow-consumer eviction. Eviction bounds the QUEUE, not
  the replay duration, and it only fires if live events keep arriving.
Task 9: **all three implementer dissents RATIFIED INDEPENDENTLY by the reviewer**, finding 7's
  included: `st.cursor` is per-`Serve` state that never crosses the wire, so advancing it on a
  drop cannot make a later resume skip events — a resume comes from the client's `Last-Event-ID`
  header. Four controller items have now been wrong on this plan.
Task 9: the reviewer ran a mutation nobody asked for — the **opposite direction** of the security
  branch (`if req.Filter.RunID != nil` → `if true`, i.e. over-closing attention streams). It
  reddens the attention-drop test on the right assertion. The table pinned the branch against
  under-closing only; it is now falsifiable both ways.
Task 9: ⚠️ the review's three cannot-verify items, all resolved by the controller, none a gap.
  (a) "must be configurable" — verified against `task-11-config-section.patch`:
  `GrantRecheck Duration \`json:"grant_recheck"\``, default 30s, validated positive, matching
  `defaultGrantRecheck`. (b) a deactivated principal resolves to `Scope{}` with a **nil** error
  (`realtimeports.go:289-291`), so deactivation lands on the empty-scope close. (c) documenting
  the terminal frame beside `StreamFrame` is a **Task 10** obligation, not a Task 9 gap.
Task 9: **all four Minors PROMOTED into fix round 1**, on a different basis from Task 8's two
  (which were demonstrated-green mutants). Here the basis is: every one is a one-or-two-line
  change on lines the Important fix already touches, and three of the four are the
  load-bearing-comment-that-misstates-why class this plan has been burned by. (a) a test comment
  claims "one COMPLETED re-check" when `stubGrants` increments on ENTRY — the test is sound for
  the adjacent reason, but this is the exact false-safety pattern the implementer refused three
  times in round 1. (b) `sse.go:41-44` enumerates how bytes reach the client as "connect, event or
  heartbeat" and `writeForbidden` is now a fourth way. (c) the four-line close block is verbatim
  twice and the replay-boundary close makes it three — which is what tips it. (d) `ScopeFor` is
  called synchronously inside the select, so a slow grants store stops delivery and the queue
  absorbs the backlog; the sentence belongs beside the queue-slot consequence already documented.
Task 9: fix round 1 dispatched to the ORIGINAL implementer with an open question rather than an
  assertion — does a per-run stream whose project is revoked MID-REPLAY still terminate, and by
  what mechanism? My suspicion (the hub's deliberately-stale filter keeps delivering that run's
  live events into `follow`, where the per-run denial closes it) is UNTESTED and framed as a
  question precisely because three such explanations have been backwards. Also required: re-run
  the WHOLE eight-row table after the refactor, since hoisting state out of `follow` touches every
  path and a table validated before a refactor is not evidence about the code after it.

Task 9: fix round 1/5 at `aa04f0a` (5 addressed per the implementer, 0 open — catch-up re-check,
  false determinism comment, `sse.go` under-describing itself, the duplicated close block, the
  synchronous-resolve consequence; commits `3ebfb89`..`aa04f0a`). **GATED AT THE FIX SHA on a
  confirmed-clean tree: `GATE PASS … aa04f0a`, 11 lanes resolved unnarrowed, contracts clean.**
  Gated twice — `3ebfb89` before review, `aa04f0a` after the fix round. Scoped re-review of
  `3ebfb89..aa04f0a` dispatched afterwards, never during.
Task 9: **the whole mutation table was RE-ANCHORED AND RE-RUN against the refactored code** —
  14 mutants including the reviewer's own r1 and the two new ones, none surviving. Required
  because hoisting the ticker/scope/failure state out of `follow` into `deliver` touches every
  path, and a table validated before a refactor is not evidence about the code after it.
Task 9: **the implementer deviated from my #1 shape and is right again.** I listed "per-run
  denied → close" as a replay-boundary outcome; it is not computable there — no event, and
  `Filter` carries no project id. The boundary does empty→close plus scope push-down; the per-run
  close stays in `follow`. This is the SAME defect as my own pre-flight finding 4, which I wrote
  correctly for the general case and then forgot when specifying the fix.
Task 9: **A NEW RESIDUE, FOUND BY ASKING A QUESTION INSTEAD OF ASSERTING AN ANSWER.** I asked
  whether a per-run stream revoked mid-replay still terminates and said my suspected mechanism was
  untested. Answer: **only if that run emits at least one more event.** The mechanism I suspected
  is right and is now pinned end to end (rows withheld by the narrowed Filter during catch-up,
  then one live event closes it; a separate mutant pins the SQL push-down independently) — but on
  a QUIESCENT run step 4 never happens and the connection stays open, idle, indefinitely.
  Confidentiality holds (replay's Filter and follow's scope test both refuse every denied row);
  liveness does not. **Had I asserted my suspicion instead of asking, this would have shipped as a
  confident false safety claim — the exact pattern that has bitten this plan three times.**
Task 9: the residue is UNCLOSABLE in this task's vocabulary — it needs the run's project id, which
  no `Filter` carries and which an admin's per-run Filter cannot supply. The implementer refused
  the obvious workaround (remember the last written event's project) because it fails for a stream
  that has never seen an event and would read as if it closed the whole gap. **CONTROLLER ROUTING:
  carried to TASK 10, not deferred for convenience** — Task 10 builds `StreamRequest` and is the
  only place where "the handler knows the run's project" is a fact rather than a hope. Task 10's
  pre-flight must weigh adding a run-project field against its footgun: a field Task 10 forgets to
  set is `uuid.Nil`, which no scope allows, so every per-run stream would close on its first
  re-check. Documented in three places in the code today.

Task 9: scoped re-review of `3ebfb89..aa04f0a` — **all 5 findings ADDRESSED, no Critical or
  Important breakage in the fix diff.** `git status` left empty; probes run through `go test
  -overlay` from a scratch dir.
Task 9: **the re-review did not take the re-anchored mutation table on trust — it wrote two of its
  own** and both reproduced the implementer's m12/m13 line for line (deleting the whole
  `if rc.due(){}` block leaks ids 3-6 and yields 4 Replay calls; deleting only `f.Scope = rc.scope`
  reddens the per-run test alone). That is what makes "re-anchored after the refactor" evidence
  rather than a claim.
Task 9: **the deviation was ratified independently and sharpened** — `Filter` never carries a run's
  project (`realtime.go:79-83`), so my own pre-flight finding 4 says the per-run close is not
  computable without an event, and my #1 prescription contradicted my own finding. "Nothing
  enforced is lost, only where it fires" was VERIFIED, not accepted: the per-run close is
  byte-identical and still in `follow`, and the boundary only ADDS the empty-scope close and the
  row withholding. Strictly greater enforcement, no scenario where the old code closed and the new
  one does not.
Task 9: **the residue's confidentiality claim HOLDS, traced rather than argued** — every
  post-refresh write path checked, and the push-down verified REAL IN PRODUCTION via `scopeArgs`
  (`app/realtimeports.go:135-160`), not merely in the fake. One qualification the `Serve` sentence
  does not carry: during the 1-2 tolerated resolver failures the stale scope is deliberately kept,
  so denied bytes can go out for up to `(maxGrantFailures-1) × GrantRecheck`. True by design —
  finding 10's blip tolerance — and the sentence sits in a successful-refresh paragraph.
Task 9: three deferred minors from the re-review, all documentation, no behavioural defect:
  (a) `stream_test.go:962-964` — the new comment says the call count "can only be raised from
  inside `follow`'s re-check case"; after the hoist `replay`'s boundary can raise it too. FALSE
  statement, but in a test comment, and the load-bearing reason (the queue argument) is stated
  correctly in the sibling test. → final review.
  (b) `stream.go:432-439` — revoke-then-re-grant during catch-up leaves a PERMANENT gap: the
  cursor advances past SQL-withheld rows, so a later re-grant never backfills them and
  Last-Event-ID resume cannot recover them. Inherent to a single high-water cursor and not fixable
  without breaking the strictly-ascending-ids invariant; the live path has the same shape. → TASK
  12, which owns the docs step.
  (c) `stream.go:62-71` — the true worst case is `GrantRecheck + one in-flight page`, and with
  tolerated resolver failures `maxGrantFailures × GrantRecheck + one page`. → final review.
Task 9: **I nearly opened fix round 2 over (c) and did not, because I read the actual doc text
  instead of the reviewer's summary of it.** I had inferred the doc asserted a bound 3× tighter
  than reality — an operator-facing security claim, which would have justified a round. It does
  not: it says GrantRecheck "bounds the staleness window across BOTH phases", which is true, and
  the paragraph beneath it already carves out the per-run/quiescent exception honestly. **Separate
  a reviewer's observation from its inference, and from your own** — the same lesson that cost a
  session earlier on this project.

Task 9: **complete (commits `fc007e0`..`aa04f0a`, review clean after 1 fix round) and GATED:
  `GATE PASS … aa04f0a`, 11 lanes resolved unnarrowed, on a confirmed-clean tree.**
Task 9: **the dissent licence paid for itself three times in one task** — the implementer refused
  finding 7's false justification while implementing its prescription, refused my #1 shape's
  uncomputable per-run clause, and softened two claims it could not falsify. Every refusal was
  independently ratified by a reviewer. **Four controller items have now been wrong on this plan,
  and the implementer was right every time.**
Task 9: **the method that found the most this task was asking a question instead of asserting an
  answer.** My per-run/mid-replay suspicion was correct but incomplete, and framing it as a
  question surfaced the quiescent-run residue that my own framing hid. Had I asserted it, it would
  have shipped as a confident false safety claim — the pattern that has now bitten this plan three
  times.

---

### Task 10 pre-flight (controller, 2026-08-12, session 60) — SEVENTEEN findings, all in the brief

Read against the tree at `94cd5d1` before dispatching. Recorded in full as a CONTROLLER
PRE-FLIGHT block at the top of `task-10-brief.md` that overrides the plan text. BASE for the
review package is **`94cd5d1`**. Pre-flight has now found 6, 8, 11, 13, 21 and 17 defects on the
last six tasks.

Task 10: **THE PLAN CONTAINS AN ACCEPTANCE CHECK THAT CANNOT PASS AS WRITTEN.** Its Step 5 says
  "if `listRunEvents`'s `application/json` response disappeared, the negotiation is declared wrong
  — both must be present". But `contracts.Build` calls `oc.AddRespStructure` for the success
  response exactly ONCE (`contracts.go:155-161`) with `WithContentType(contentTypeOf(op))`, and
  `contentTypeOf` returns either `application/json` or the single `ResponseContentType` override;
  `httpx.Operation` carries one `Response` and one content type. Setting the override on
  `listRunEvents` REPLACES the JSON response rather than adding to it. **The seam must be extended
  — an additional-responses list on `Operation` plus a loop in `Build` — and the implementer must
  verify EMPIRICALLY that the reflector merges two content types under one status rather than
  overwriting, and STOP if it overwrites.** Third task on this plan whose stated acceptance
  evidence was broken as written (Tasks 7 and 8 were the others), and the first where it is the
  contract rather than a test.
Task 10: **the per-run handler already holds the run's project id — Task 9's residue IS solvable
  here — and I ruled NOT to solve it in this task.** `runScoped` (`runs/api/api.go:252-272`) loads
  the run, loads its task and authorizes against `task.ProjectID`, so feeding it through
  `ServeRunStream` would close the quiescent-run liveness gap. Rejected for Task 10 because it
  needs a matching change inside `realtime`'s re-check path plus tests and mutations, and this
  task already extends the contract seam, changes a five-call-site constructor, creates a package
  and adds content negotiation. **Two unrelated risks in one task is how a task stops being
  reviewable.** The gap is confidentiality-safe and documented in three places in the code.
  **→ FINAL REVIEW must route it; it is the one open design question left by Task 9.**
Task 10: **a nil `StreamServer` must NOT panic at construction — the OPPOSITE of Task 9's nil
  ruling, and the reason is concrete**: `contracts.go:129` deliberately builds
  `runsapi.New(nil, nil, nil, nil)` for metadata-only generation and the two integration tests
  will pass nil too. The check moves to request time and must be LOUD (500), never a silent
  fall-through to the JSON poll. Same principle as Task 9, opposite mechanism — worth noticing
  that the precedent did not transfer.
Task 10: `runsapi.New` gains a parameter and has FIVE call sites, two of them integration tests
  (`contracts.go:129`, `serve.go:165`, `api_e2e_integration_test.go:188`,
  `goldenset_integration_test.go:146` and `:460`).
Task 10: **do NOT redeclare `StreamFrame`.** `realtime.StreamFrame` (`sse.go:21-27`) already has
  the five fields and its own doc says Task 10 reflects THIS type so the bytes cannot drift — a
  parallel DTO in `realtime/api/dto.go` is exactly that drift. Which probably leaves `dto.go` with
  nothing to hold: **do not create an empty file because the plan's file list names one.**
Task 10: the ambiguous-`Filter` obligation resolved against a runtime check — both handlers build
  the Filter in code, so no request can produce the ambiguous combination and a check would be
  DEAD CODE. Preferred: constructors in `realtime` that cannot express it. Minimum: a test per
  handler. The risk being guarded is a later edit, not user input.
Task 10: **there is no Accept parser in this tree** — `mime.ParseMediaType` parses one media type,
  not a list, and the only `"Accept"` hits in `internal/` are outbound forge headers. Absent,
  `*/*`, or non-matching Accept MUST take the JSON branch or the poll's contract breaks.
Task 10: **the plan's `ServeRunStream` signature carries no `Scope`, and never says who resolves
  it.** The `internal/app` adapter must call `ScopeFor` before building the `StreamRequest`;
  `realtimeGrants` already satisfies `realtime.GrantResolver` (`realtimeports.go:267-285`). Asked
  as a QUESTION rather than prescribed: what does a client get when `ScopeFor` fails at stream
  open? That is the ONE point where a real HTTP status is still available — `NewWriter` has not
  run, so the response is not committed. Everything after is stuck at 200.
Task 10: header params pinned precisely — `requestStructure` (`contracts.go:185-198`) returns
  `op.Request` unchanged for a path without `{id}`, so `/api/v1/stream` declares `Last-Event-ID`
  via its `Request:` struct with NO `requestOverrides` entry; `listRunEvents` has `{id}` so its
  header field goes in the existing `runEventsParams`, already registered at `contracts.go:118`.
Task 10: **scope boundary against Task 11 ruled explicitly** — this task creates the packages, the
  port, the negotiation and the contract entries, and touches `internal/app` only as far as it
  must to COMPILE (nil at `serve.go:165`, with a comment naming Task 11). Between the two tasks
  the SSE branch answers 500, which is honest. `realtime/api.New` wants a `*realtime.Streamer`
  that does not exist at boot until Task 11 builds it — **stop and report rather than half-doing
  Task 11.**
Task 10: also carried — document Task 9's terminal frame in prose on both stream operations (the
  seam expresses one schema per content type, so a second frame shape cannot be reflected as a
  type); five return shapes of `Serve` each need a log-level decision, matched with `errors.Is`
  against the `CloseReason` constants and never by string; `Serve` returns nil rather than
  `CloseShutdown` when a disconnect races hub close, so no shutdown guarantee may rest on the
  return value; and streaming tests must FAIL rather than HANG — this package has hung its own
  suite to the timeout twice.
Task 10: implementer dispatched on Opus 5 at BASE `94cd5d1`, with an eight-row mutation table and
  an explicit licence to dissent.

Task 10: implemented at `bb9e4e6` — 20 files, +1611/-41, spanning `realtime/api` (new),
  `runs/api`, `contracts`, `platform/httpx`, `internal/app` and — beyond the plan's file list —
  `realtime/sse.go`, `realtime/realtime.go`, `realtime/stream_test.go`. Filed
  **DONE_WITH_CONCERNS**. **GATED BEFORE REVIEW on a confirmed-clean tree: `GATE PASS … bb9e4e6`,
  11 lanes resolved unnarrowed, contracts clean.** Review in flight.
Task 10: ⚠️⚠️ **THE IMPLEMENTER REPORTS A LATENT CRITICAL IN TASK 6 THAT EVERY GATE MISSED.**
  `NewWriter` type-asserted `http.Flusher`, and `httpx.AccessLog`'s response wrapper is not one —
  so **both of this task's endpoints would have answered an empty 200 with `Content-Length: 0`**
  in the real process. It says it probed this before touching anything and mutation-reverted to
  demonstrate the empty 200, then fixed it with `http.NewResponseController`. **It passed Tasks 6,
  8 and 9 and every gate because no test ever put a real middleware wrapper between the codec and
  the recorder** — the fakes went straight to an `httptest`-style recorder that IS a Flusher.
  A whole class of integration defect that unit fakes cannot see. **Unverified by the controller;
  the review is dispatched to falsify it independently, and to decide whether the fix loosened
  what Tasks 6/8/9 pinned on construction-time Flusher rejection and the exact op-log.**
Task 10: **MY PRE-FLIGHT FINDING 1's MECHANISM WAS IMPOSSIBLE — the sixth controller error, and
  this one is my own two findings colliding.** Finding 1 said to extend `httpx.Operation` with an
  additional-response list carrying a `Response any`; finding 10 said `runs` must not import
  `realtime`. Declaring the alternate response ON the `listRunEvents` operation, which lives in
  `runs/api`, requires `runs/api` to NAME `realtime.StreamFrame` — a CI-failing arch violation.
  I wrote both findings and did not connect them. The implementer demonstrated the violation by
  running the REAL arch test over an overlay of exactly what I prescribed, then put an
  `altResponses` map in `contracts` mirroring the existing `requestOverrides` and left
  `httpx.Operation` untouched. `contracts` already imports every api package, so it can name the
  type freely. **The requirement finding 1 encoded still had to be met and the reflector DOES
  merge: both content types now sit under 200 on `listRunEvents`.**
Task 10: the implementer flagged one mutation row it could NOT run — the per-run half of the
  ambiguous-`Filter` row, because the per-run `Filter` is built by Task 11's adapter. It ran the
  faithful mirror on the attention filter and **reported the gap rather than faking it**. Same
  standard as Task 8's discarded mutant: evidence that does not prove the claim is not evidence.
Task 10: findings 4 and 15 honoured — no adapter, no Streamer, two commented `nil`s in `serve.go`.
  Carry-forward the implementer raises for TASK 11: satisfy `runsapi.StreamServer` structurally
  from `realtimeapi.API` so the ScopeFor / filter-construction / log-classification logic is not
  written twice.

Task 10: review at `bb9e4e6` — **spec ✅ COMPLIANT** (all 17 findings honoured or deviated with a
  demonstrated counterfactual, no scope creep against the two "do NOT build this here" rulings),
  quality "Needs fixes": 0 Critical, 2 Important, 6 Minor. `git status` left empty.
Task 10: **THE TASK 6 FLUSH DEFECT IS CONFIRMED INDEPENDENTLY, AND THE FIX LOOSENED NOTHING.**
  `statusRecorder` embeds the ResponseWriter INTERFACE and defines only `WriteHeader`/`Unwrap`
  (`middleware.go:74-86`), so `w.(http.Flusher)` is necessarily false — the diagnosis is exact.
  Removing the new flush check reddens BOTH `TestWriter_RejectsNonFlusher` and
  `TestServe_RejectsANonFlusher` (the latter at its deadline, not hanging), and the Task 6/8/9
  op-log pins are unchanged. **A latent Critical that survived three tasks, three reviews and
  every gate, because no test put a real middleware wrapper between the codec and the recorder.**
  The lesson generalises past this bug: **a fake that is MORE capable than the real collaborator
  hides exactly the defects integration exists to find** — Task 8's fake ignored its context and
  hid a whole class; Task 10's recorder was a Flusher when the real chain is not.
Task 10: the `altResponses` substitute for my broken finding 1 is **SOUND** — `arch/rules.go:21`
  puts `contracts` in `nonContext`, both media types sit under 200, `ApiRunEventsResponse` intact.
  Leaving `httpx.Operation` untouched was right.
Task 10: **IMPORTANT I1 — the two surfaces disagree on the one failure that can still be a status.**
  `Serve` has exactly one pre-commit failure (`NewWriter`); on it the attention endpoint answers a
  bare **200 with `Content-Length: 0`** while the per-run path answers 500. Demonstrated by
  re-introducing Task 6's type assertion: `TestStreamAttention_SetsStreamHeaders` fails on
  Content-Length while the STATUS assertion still passes. The silent dead endpoint spec §4 exists
  to prevent, on one of the two surfaces — the same failure mode the `sse.go` fix just closed one
  layer down. Its comment ("by the time this returns the status has been 200") contradicts
  `stream.go:154-156`. **Non-trivial to fix: the handler cannot tell pre- from post-commit from
  the error value, and calling `httpx.Problem` over a committed 200 would append a problem
  document to the SSE body — strictly worse.** Sketched a sentinel option and explicitly invited a
  better shape rather than prescribing, my prescriptions having now been wrong six times.
Task 10: **IMPORTANT I2 — `AttentionFilter`'s doc claims a compile-time guarantee it does not
  have.** `Filter`'s fields are exported, so an ambiguous filter is still legal Go, and the per-run
  one is HAND-BUILT in `internal/app` by Task 11 — exactly finding 7's "later edit" risk. Fifth
  instance on this plan of a confident safety claim that is false.
Task 10: four Minors promoted into fix round 1 on the established basis (cheap, on lines already
  being edited, and in the classes this plan has been burned by). **M2 is the highest-value one:
  the runs-side streaming integration tests carry NO request deadline and cannot hang today only
  because `fakeStreamServer` returns immediately — the moment Task 11 puts something blocking
  behind that port they hang to the suite timeout instead of failing.** Plus M1 (the new
  `NewWriter` failure path leaves SSE headers behind for the following `httpx.Problem` to
  inherit), M3 (the Accept doc reads as exhaustive but ignores relative `q` weights), M4
  (`altResponses`'s comment explains where it lives, not what its key assumes).
Task 10: ⚠️ **M5 CARRIED TO THE TASK 11 BRIEF — a trap the implementer's own suggestion walks
  into.** `runsapi.StreamServer`'s contract is *error ⟹ nothing committed*; `realtime.Serve`'s is
  wider — it returns errors for post-commit endings too (write/source, `CloseForbidden`,
  `CloseSlowConsumer`, `CloseShutdown`). The report's §11 suggestion (satisfy `StreamServer`
  structurally from `realtimeapi.API`) **becomes a bug if the adapter forwards `Serve`'s error
  verbatim**: `streamRunEvents` would then call `httpx.Problem` over a committed 200 and append a
  problem document to the SSE body. `fakeStreamServer` writes nothing when `err` is set, so
  NOTHING IN THIS SUITE CAN CATCH IT. **Task 11 must translate, not forward.**
Task 10: M6 noted only — one summary line in the report overstates `RunFilter`'s callers; §2(b)
  discloses it plainly. Nothing to do.

Task 10: fix round 1/5 at `fc52b57` (6 addressed, 0 open — silent 200 on the attention surface,
  the false compile-time claim, leaked SSE headers, the missing test deadline, the Accept doc, the
  `altResponses` key assumption; commits `bb9e4e6`..`fc52b57`). **GATED: `GATE PASS … fc52b57`,
  11 lanes resolved unnarrowed, on a confirmed-clean tree.** Scoped re-review dispatched after.
Task 10: **the implementer chose a better shape than I sketched for I1** — a `realtime.ErrNotStarted`
  sentinel tagging `Serve`'s one pre-commit failure, with the CAUSE WRAPPED alongside it rather
  than returned bare, because the 500 carries no detail and AccessLog no reason, so the wrap is
  the only place "cannot flush" is written down anywhere. Re-review verified: no `errors.Is` site
  in the tree is broken by the wrap, and the log line really is the only surviving record.
Task 10: it re-ran **ALL ELEVEN round-1 mutation rows rebuilt from current source**, plus six new
  ones. One row's output CHANGED (m11: the reverted flush check now yields 500 instead of the
  silent 200) and it argued the change is the fix working. **I asked the re-review to check that
  rather than accept it — a mutant whose output changes after a refactor is either evidence the
  fix landed or evidence the pin moved, and the two look identical in a report.** Verdict: the fix
  landed; the mutant still reddens on the test's FIRST assertion.
Task 10: ⚠️⚠️ **THE SENTINEL WAS WRONG FOR EXACTLY THE CASE TASK 11 WILL DEPEND ON IT FOR — and
  the re-review found it by running a real server against an RST client.** `ResponseController.Flush`
  dispatches to `FlushError` first, and `(*http.response).FlushError` calls `WriteHeader(200)`
  BEFORE it can fail (`net/http/server.go:1724`); the probe produced `write: broken pipe` AND
  `superfluous response.WriteHeader call` in one run. So the fix's own new prose ("committed
  nothing" / "`w` untouched", `stream.go:155-158`, `sse.go:85-89`) is FALSE on that branch — the
  **fifth** confident safety claim on this plan to be backwards, and the first written by a fix for
  a previous one.
Task 10: **I escalated it above the re-review's Minor scoping, and the reason is the coupling, not
  today's impact.** Today it is bounded to peers already gone, so no live SSE body can be corrupted
  and the cost is log noise. But the implementer stated the sentinel "is also the discriminator
  M5's Task 11 adapter needs", and M5 is precisely *error ⟹ nothing committed*. A sentinel that
  also matches a POST-commit flush failure hands Task 11 a discriminator that lies, and
  `streamRunEvents` would then write a problem document into a committed SSE body — **the exact
  bug M5 exists to prevent, reintroduced through the mechanism meant to prevent it, and
  uncatchable by `fakeStreamServer`.** Fix round 2 ruled: tag only
  `errors.Is(err, http.ErrNotSupported)`, which is precisely "the chain cannot flush" and the only
  case where nothing was written. Keep returning the error either way — a broken pipe at connect
  time must still end the request; only the TAGGING narrows. This makes the sentence TRUE rather
  than merely softer.
Task 10: second round-2 item — `realtime.go:105` claims `RunFilter` "has no caller inside this
  package" while `stream_test.go:297` calls it. Substance holds, wording is checkably false.
Task 10: ⚠️ the LSP produced FALSE compile errors a FOURTH time, claiming `ErrNotStarted` undefined
  at `fc52b57` while `go build ./...` and `go vet` exited 0 and the full gate passed.

Task 10: fix round 2/5 at `00030da` (2 addressed, 0 open — the over-wide `ErrNotStarted` tag and
  the false `RunFilter` caller claim; commits `fc52b57`..`00030da`). **GATED: `GATE PASS …
  00030da`, 11 lanes resolved unnarrowed, on a confirmed-clean tree.**
Task 10: **the narrowing was verified against the ACTUAL go1.26.5 GOROOT, not reasoned about** —
  `responsecontroller.go:47-62` has four exits, `:145-147` is `fmt.Errorf("%w", ErrNotSupported)`,
  `server.go:1724-1727` writes the header before `FlushError` can fail, `Flusher` cannot error and
  an unwrap-nil falls to default. So `ErrNotSupported` is exactly the nothing-written case AND the
  only error `ResponseController` can produce for an unflushable chain. Both directions are
  falsifiable: widening the tag back reddens only the new negative pin; dropping the tag through
  the real path reddens the 500 pin.
Task 10: **the implementer declared a residual IN THE CODE rather than only in the report** — a
  wrapper implementing `FlushError` could fail without writing and go untagged, costing a silent
  200. Its own framing is the valuable part: **the remaining mistake now falls on the side that
  never writes over a committed body.** That is the right direction for an error to lean.
Task 10: **it also reported a MUTATION IT LOST, as a loss.** m11's mutant invents its own error, so
  the narrowed tag correctly declines it and the surface is a silent 200 again — round 1's line
  "reintroduce the Task 6 defect and you get a 500" no longer holds under that mutant. The
  re-review rebuilt m11 independently (8 failures, silent empty 200, exactly as declared) and
  confirmed **the pin did NOT move**: a new row removes the tag through the real `ErrNotSupported`
  path and reddens the 500 pin. **A weakening pin and a working fix produce the same green suite;
  the only thing that separated them here was rebuilding the mutant.**
Task 10: the re-review SHARPENED a correct conclusion reached by a partly wrong argument — the
  implementer justified not re-running 8 rows by "arch law", which covers 7 of them
  (`go list -deps -test ./internal/runs/api/` links zero realtime packages, stronger than arch
  law), but **`./internal/contracts` DOES link both realtime and realtime/api**; that row is safe
  because `Build` never reaches `Serve` and the schema did not move, not by arch law.
Task 10: three deferred minors, all wording, no behavioural defect:
  (a) `stream.go:75` "Nothing in this tree implements `FlushError`" is falsified by
  `deadPeerWriter` at `sse_test.go:318` — **added by the same commit**. The substance holds: no
  PRODUCTION writer implements it (`httpx.statusRecorder` has only `WriteHeader`+`Unwrap`), and
  the test helper writes the header so the residual instance still does not exist.
  (b) `ErrNotStarted`'s opening "every other ending" list omits the untagged flush failure.
  (c) an "empty header map" claim that holds only because no caller sets headers before `Serve`.
  → all three to the final review.
Task 10: **PATTERN WORTH NAMING — a comment written to FIX a false comment has itself been false
  in three consecutive rounds.** Round 1's fix introduced the "committed nothing" prose that round
  2 had to narrow; round 2's fix introduced the `FlushError` claim now deferred. The class is not
  converging through fix rounds, which is the argument for routing (a)-(c) to the whole-branch
  review — a fresh read of the finished files — rather than iterating the same loop a fourth time.
Task 10: **I closed the loop here rather than opening round 3, and the distinction from round 2 is
  the test I used.** Round 2's item had a BEHAVIOURAL consequence and a downstream consumer (Task
  11's discriminator); these three have neither. Wording that overstates in a direction which does
  not mislead about safety is a final-review item, not a round.

Task 10: **complete (commits `94cd5d1`..`00030da`, review clean after 2 fix rounds) and GATED:
  `GATE PASS … 00030da`, 11 lanes resolved unnarrowed, on a confirmed-clean tree.** Gated three
  times — `bb9e4e6` before review, `fc52b57` and `00030da` after each fix round.
Task 10: **the task's biggest yield was not the endpoints — it was a latent Critical in Task 6**
  that three tasks, three reviews and every gate had missed, found because the implementer probed
  the real middleware chain before writing code.

---

### Task 11 pre-flight (controller, 2026-08-12, session 60) — TWELVE findings, all in the brief

Read against the tree at `8c4d969` before dispatching. Recorded in full as a CONTROLLER
PRE-FLIGHT block at the top of `task-11-brief.md` that overrides the plan text. BASE for the
review package is **`8c4d969`**. Pre-flight has found 6, 8, 11, 13, 21, 17 and 12 defects on the
last seven tasks.

Task 11: ⚠️ **THE WORST FAILURE SHAPE IN THIS TASK IS COMPLETELY SILENT.** The notify side is a
  bare literal (`NOTIFY caesar_run_events`, `runs/events.go:99`) and the Waker will type `LISTEN
  caesar_run_events` a second time. **If the two ever disagree, NOTHING FAILS**: the notification
  goes nowhere, the tailer falls back to polling, every test still passes, and the only symptom is
  up to `PollFallback` of extra latency that no assertion measures. Ruled: make it
  single-definition structurally (`internal/app` may legally import `runs`, so an exported
  constant consumed by both sides works) or prove a real NOTIFY wakes a real Waker — but the
  literal must not be typed twice with nothing checking.
Task 11: **`TailerConfig.Batch` deliberately gets NO config knob, and the trap is that wiring one
  would look right.** `config.Realtime` has `replay_batch`, which is one CLIENT's catch-up page;
  `TailerConfig.Batch` is the tailer's read size. **`defaultBatch` and `defaultReplayBatch` are
  both 500** (`tailer.go:21`, `stream.go:18`), so feeding one into the other tests green and
  quietly ties two unrelated knobs to one number.
Task 11: **`Tailer.Run` can fail BEFORE it loops and the shape the plan says to copy swallows it.**
  `Run` seeds its cursor from `src.Head` and its own godoc calls an unreadable head fatal — "the
  caller should treat it as a failed startup" — but `go func() { defer close(done); _ = x.Run(ctx) }()`
  (`serve.go:190`, `:201`) discards the error. A process whose tailer never started serves streams
  that get a snapshot and then silence, forever, with nothing logged. Requirement stated (must not
  be silent), shape left to the implementer, with a note that deviating from the runner's and
  orchestrator's identical shape needs a reason.
Task 11: **THE M5 TRAP CARRIED FROM TASK 10, now with its discriminator in place.**
  `runsapi.StreamServer`'s contract is *error ⟹ nothing committed* (`runs/api/api.go:24-41`);
  `Serve`'s is wider. An adapter that FORWARDS `Serve`'s error makes `streamRunEvents` call
  `httpx.Problem` over a committed 200 and append a problem document to the SSE body.
  `realtime.ErrNotStarted` is the discriminator and was narrowed in Task 10 round 2 to exactly
  `http.ErrNotSupported`. **`fakeStreamServer` CANNOT catch this — it writes nothing when its
  error is set — so a double that WRITES AND THEN ERRORS is required.**
Task 11: ruled that `realtimeapi.API` satisfies `StreamServer` structurally, reusing the existing
  `logStreamEnd` six-shape classification and the pre-commit ordering in `handleStreamAttention`
  rather than writing either twice. **`realtime/api` must NOT import `runs/api`** (peer contexts),
  so the compile-time assertion `var _ runsapi.StreamServer = (*realtimeapi.API)(nil)` goes in
  `internal/app`, the only place that may name both.
Task 11: **I flagged my OWN shutdown reasoning as unverified and asked the implementer to check it
  before repeating it in a comment.** I claim the wait matters because the tailer goroutine
  otherwise outlives `Serve` holding a dedicated connection nothing closes — and I explicitly do
  NOT claim a race with `Hub.Close()`, because reading `hub.go`, `Publish` collects under `RLock`
  and `offer` re-checks `closed` under the subscription's mutex. Also flagged: **`Hub.Close()`
  does NOT end a stream still in `prime` or `replay`** — it closes the channel, which only
  `follow` notices — so `Shutdown` can still wait on a catching-up stream, bounded by
  `ShutdownTimeout`. Named in advance so a flaky shutdown test is not "fixed" by loosening it.
Task 11: also carried — apply `task-11-config-section.patch` FIRST (the plan's brief never mentions
  it exists); use `realtime.RunFilter` rather than a hand-built literal, since Task 10's I2 pinned
  that `Filter`'s exported fields keep the ambiguous combination legal and named `internal/app` as
  the "later edit" risk; the dedicated `*pgx.Conn` must be CLOSED on shutdown, which is the other
  half of the wait; `realtimeports.go:31` credits Task 12 with the wiring and is wrong.
Task 11: implementer dispatched on Opus 5 at BASE `8c4d969`, with a seven-row mutation table, four
  open decisions to answer with reasons, and an explicit licence to dissent.

Task 11: implemented at `17e76b7` — 14 files, +1384/-35. **GATED BEFORE REVIEW on a confirmed-clean
  tree: `GATE PASS … 17e76b7`, 11 lanes resolved unnarrowed, contracts clean.**
Task 11: **THE PLAN'S ACCEPTANCE EVIDENCE ACTUALLY RAN.** The integration lane booted a real
  `postgres:18-alpine` via testcontainers under `-race` (internal/app 34s, internal/runs 65s) —
  the first time any of this executed as an assembled process rather than in isolation. All seven
  mutation rows executed and reddened the right assertion.
Task 11: review — **spec PASS and quality APPROVED**, 0 Critical, 1 Important, 5 Minor. First task
  on this plan to come back Approved on the first pass.
Task 11: **all five of the implementer's judgement calls were checked individually and all five
  stand.** (a) the sixth config knob `realtime.snapshot_events` is JUSTIFIED — finding 2 opened the
  decision and the knob is symmetric with its five neighbours; tying it to `ReplayBatch` would mean
  halving a page size silently halves everyone's backlog. (b) a fatal tailer start is CORRECT, with
  blast radius verified boot-only: `Run`'s sole non-cancel exit is the `Head` seed and `drain` logs
  and abandons thereafter, so no transient can kill a RUNNING process. (c) `runServe` binding its
  own listener is Extra but a net improvement — `srv.Addr` is read only by `ListenAndServe` and the
  empty-addr divergence is unreachable. (d) rate-limiting the waker's reconnect by the tailer's
  poll interval is EQUIVALENT to a backoff, because `tailer.go:148` hands `Wait` a
  PollFallback-bounded ctx. (e) my flagged-as-unverified publish/close reasoning is CORRECT,
  verified independently against `hub.go` rather than inherited from either of us.
Task 11: **the Important is the SIXTH false-evidence claim on this plan, and it is about a TEST.**
  `realtimeports.go:517-519` says a test "demonstrates" the LISTEN connection survives a poll
  deadline; the reviewer forced a redial after every deadline and **the test still PASSED in
  3.34s**. The underlying pgconn behaviour is fine — only the evidence claim is false. Worth
  naming because this implementer refused several such sentences about CODE today and one slipped
  in about a test.
Task 11: four Minors promoted. **M1 is the substantive one: the task created a SIXTH duplicated
  default (`defaultSnapshotEvents = 500`) and silently exempted it from the tie-back it had just
  built for finding 10** — and it is the easiest of the six to tie, because `defaults_test.go`'s
  "neither import direction is legal" argument does not apply when `internal/app` already imports
  `platform/config`. M2 and M3 are comments this commit made false (`realtime/api/api.go:106-108`
  and `runs/api/api.go:50` plus a test comment) — all three say "until Task 11 builds the
  streamer", and Task 11 is this commit. M4: **`<-rt.done` is the one UNBOUNDED wait in the
  shutdown path and it runs before `shutdownCtx` exists** (`serve.go:277-278`) — theoretical since
  pgx honours ctx, but if an in-flight `Since` ever ignored cancellation, shutdown would hang
  FOREVER with the hub open and every SSE handler parked, which is strictly worse than the
  `ShutdownTimeout` hang the whole ordering exists to prevent.
Task 11: ⚠️ **M5 CARRIED TO THE TASK 12 BRIEF** — `awaitListening` discovers the port by parsing a
  production log line's exact message text and JSON key. Bounded at 60s and fails loudly, but
  renaming that message or the `addr` key breaks four integration tests for a reason unrelated to
  realtime. **Task 12 builds its load-test fixture on this**, which is where returning the address
  from a seam earns its cost.
Task 11: also carried to Task 12 — **`runs.AppendEventsForTest` is NOT reachable from
  `internal/app`** (it lives in a `_test.go` file), so the e2e test writes via
  `runs.Service.AppendRuntimeEvent`. The load test will hit the same wall.
Task 11: three ⚠️ cannot-verify items, none a gap: the lanes the controller and implementer ran;
  `TailerConfig.Batch` being left defaulted has no observable behaviour and no test can pin it
  (comments at both ends are the honest guard, and the implementer said so rather than claiming
  coverage); and shutdown with a stream still in `prime`/`replay` is documented but untested —
  correctly characterised, not covered.
Task 11: ⚠️ the LSP produced FALSE compile errors a FIFTH time — a dozen undefined `store.Queries`
  methods at `17e76b7` — while `go build ./...`, `go vet` and the full gate were all clean.

Task 11: fix round 1/5 at `80579d9` (5 addressed — the false test-evidence claim, the sixth
  duplicated default, three stale comments, the unbounded shutdown wait; commits
  `17e76b7`..`80579d9`). **GATED: `GATE PASS … 80579d9`, 11 lanes resolved unnarrowed.**
Task 11: ⚠️ **THE IMPLEMENTER REPORTED A SHA THAT DOES NOT EXIST — and the gate caught it by
  REFUSING TO RUN.** It reported `3a9dcfa`; `git cat-file -t 3a9dcfa` says "Not a valid object
  name" and the reflog shows exactly one commit with that subject and NO amend (`80579d9`). The
  content was right; only the reference was wrong. **`scripts/gate` refused because it verifies the
  named worktree is at the named sha** — precisely the failure it was written for, and the first
  time on this plan it has fired. Had I passed `HEAD` instead of a literal, it would have gated
  green and the report would still name a commit nobody could find. **A subagent-reported sha is
  not evidence; only `git log` is** — the same lesson as verifying handoffs against git, one scale
  down. The implementer was asked to correct the reference in the report and to touch nothing else.
Task 11: **the implementer discarded its own first fix for I1 before shipping it.** Capturing the
  backend PID only after the warm-up `Wait` PASSES under an eager-redial mutant, because the redial
  happens inside that Wait. The shipped shape holds the waker across TWO deadlines and requires one
  PID throughout; two mutants now redden it — dropping on deadline gives "the waker holds no
  connection", dropping AND redialling eagerly gives "a poll deadline moved the listener from
  backend 76 to 77".
Task 11: **M1 answered by DELETION rather than by tie-back** — `defaultSnapshotEvents` was
  unreachable (`Load` validates `>= 1`, every test call site passes an explicit positive), so the
  duplication was removed instead of monitored. The right instinct: a guard that cannot fire is not
  a guard.
Task 11: it also fixed a stale comment **the review did not flag** — `realtime/api.New`'s own doc
  made the same "mid-way through wiring" claim one paragraph above the branch that was flagged.
Task 11: ⚠️ **M4 REFUSED, WITH A REASON THAT KILLS MY SUGGESTION OUTRIGHT.** I asked for the
  unbounded `<-rt.done` to be bounded or documented. It documented and refused to bound, because a
  bound is *dangerous in one case and false in the other*: abandoning the wait closes a pgx `Conn`
  a live goroutine may still be reading (documented unsafe for concurrent use, `conn.go:65`) and
  `Stop` cannot tell which state the tailer is in; and **a tailer stuck in `Wait` holds
  `pgWaker.mu`, which the next step's `Close` takes — so the hang simply moves one line down.** My
  bound would have fixed nothing while looking like it had. A real bound needs the waker
  interruptible independently of its mutex, i.e. a `pgWaker` redesign, offered as a separate item.
  **Seventh controller item wrong on this plan.**

Task 11: **CORRECTION TO THE ENTRY ABOVE — the bad sha was NOT a transcription error, and the real
  cause is better than my guess.** The implementer diagnosed it: after committing it ran
  `git log -1 --format='%an <%ae>%n%n%B'`, which prints the author and the message and **no sha at
  all**. It verified authorship and the absent AI trailer, then reported an identifier it had
  never read. Nothing produced the string `3a9dcfa`. **That is I1 one level up — an evidence claim
  with no evidence behind it, committed in the very round whose Important finding was exactly
  that.** The right command was `git log -1 --format='%H %s'`. Also corrected: the REPORT never
  contained the bad sha (it said only "on top of `17e76b7`"); the error lived solely in the chat
  message, so the fix was an addition, not a substitution. **I inferred "mis-transcribed" from the
  symptom and was wrong — separate a subagent's observation from your own inference too.**

Task 11: scoped re-review of `17e76b7..80579d9` — I1, M1, M2, M3 **ADDRESSED**; M4
  **REFUSED-AND-JUSTIFIED**. One Important and one Minor of new breakage, both in round 1's own
  justifications. `git status` left empty.
Task 11: **THE RE-REVIEW FALSIFIED HALF OF EACH OF ROUND 1's TWO ARGUMENTS — by probe, not by
  argument.**
  (a) **M4's refusal: lock half DEMONSTRATED, hazard half FALSE.** A probe parked a `Wait` on a
  real database and `Close` blocked 3.30s until it returned — so "the hang moves one line down" is
  real and my suggested bound would indeed have fixed nothing. But "abandoning closes a pgx `Conn`
  a live goroutine may be reading" is impossible: every conn access is under `w.mu` and `Close`
  takes it, **as `realtimeports.go:478-481` says in the same file**. Abandoning would cost another
  hang (`pool.Close`), not a race. Conclusion right, stated mechanism wrong — the **seventh** false
  safety claim on this plan and the **second written by a fix for a previous one**.
  (b) **M1's "unreachable" was PARTLY WRONG.** `Load` validates `>= 1`, but `runServe` is called
  with a hand-built `config.Config{}` (`serve_integration_test.go:39` → `serve.go:177`) and reaches
  `newRealtimeEventSource` with 0. Probe: **0 events under watermark 3, versus 3 at 500.** Deleting
  the guard left `SnapshotEvents` as the ONE realtime knob whose zero is broken rather than
  defaulted — `NewHub`, `NewTailer` and `NewStreamer` all default a non-positive value with a
  comment on why the zero fails SILENTLY. "No test sees it today" is the problem, not the
  reassurance. **I endorsed the deletion; the endorsement was as wrong as the reasoning.**
Task 11: I1's fix was verified BOTH WAYS by the re-reviewer's own mutant — it reddens the shipped
  two-deadline PID test (backend 77→78) and **PASSES under the shape the implementer discarded**,
  so the self-discard was correct on the merits.
Task 11: **the `pgWaker` redesign is NOT warranted, and the reason is better than the offer.** Every
  blocking call in `Tailer.Run` takes a context descended from the one `Stop` cancels
  (`tailer.go:115,148-150,158`), so the wait is already bounded by cancellation. A `Warn` when the
  wait runs long is the only thing worth adding — **carried to the FINAL REVIEW as new behaviour,
  deliberately not folded into a fix round.**
Task 11: fix round 2/5 dispatched — the false hazard sentence, and a zero `SnapshotEvents` that must
  not silently yield empty snapshots. Also asked the implementer to make
  `git log -1 --format='%H %s'` an explicit habit rather than a resolution, since its own diagnosis
  of the phantom sha was that it reported an identifier it had never read.

Task 11: fix round 2/5 at `01c4038` (2 addressed, 0 open — the false concurrent-use hazard and the
  silently-broken zero `SnapshotEvents`; commits `80579d9`..`01c4038`). **GATED: `GATE PASS …
  01c4038`, 11 lanes resolved unnarrowed.** Sha verified by the controller with
  `git log -1 --format='%H %s'` before gating, after last round's phantom.
Task 11: **the third attempt at the shutdown paragraph is the one that holds, and the re-review
  checked BOTH hang shapes rather than the one cited.** Shape A: `Wait` holds `w.mu` across the
  blocking read and `Close` takes it. Shape B: a drain reads via the pool
  (`tailer.go:114` → `realtimeports.go:199`) and the deferred `pool.Close` at `serve.go:97` runs
  LAST, LIFO after `rt.Stop` — citation verified verbatim at `pgxpool/pool.go:454-455` AND in the
  implementation (`puddle pool.go:180` `destructWG.Wait`). **No third stuck state exists.** The
  implementer's own diagnosis of its error: it reached for pgx's general "not safe for concurrent
  usage" line instead of reading the mutual exclusion it had documented eleven lines above.
Task 11: **the tie-back was verified in BOTH directions** — the re-reviewer ran the implementer's
  mutant (app const → 250) AND wrote its own (`config.go:226` → 750); both RED. A tie-back tested
  from one side only proves half of what it claims.
Task 11: claim 3 verified by running the mutant rather than reasoning about it — deleting the guard
  **compiles** (an unused const is legal Go) and fails at `realtimeports_test.go:297` with
  "LIMIT 0, want 500". Asserting the RESULT rather than the constant is what makes it redden
  instead of failing to build; a test that fails to compile under a mutation proves nothing.
Task 11: the deliberately-omitted integration test is SOUND for what was asked —
  `realtimeports_integration_test.go:464` pins constructor-arg→LIMIT against a real DB and the unit
  test pins zero→500. The residual uncovered link (knob identity at `realtimeports.go:369`) cannot
  reopen the finding, because every field is zero on that path so the guard fires regardless.
Task 11: ⚠️ **the report's reason for not re-running m2-m9 was WRONG while its conclusion held** —
  `bootServe` is called at `:293`, `:337` and `:369`, so m2 and m5's e2e half DO run through the
  constructor. Inert at 500, so the conclusion stands. **Exactly the hazard named in the dispatch:
  an anchor that still matches is not the same as a mutation that still proves its claim.**
Task 11: two deferred minors, both comment/report level, no behavioural defect:
  (a) `realtimeports.go:64-67` and `defaults_test.go:28-31` overstate why THIS tie-back is special
  — the five in `internal/realtime` do have a running tie-back, and `defaults_test.go:8` imports
  config itself, so "free to import config" separates production code only.
  (b) the m2-m9 rationale above. → both to the final review.
Task 11: **loop closed rather than a round 3, on the same test used at Task 10** — neither residual
  has a behavioural consequence or a downstream consumer. And the pattern is now confirmed across
  two tasks: **a justification written to correct a justification has been wrong in every round of
  both**, so the class does not converge through fix rounds. A fresh read of the finished files at
  the whole-branch review is the right instrument, not a fourth iteration.

Task 11: **complete (commits `8c4d969`..`01c4038`, review Approved on the FIRST pass, clean after 2
  fix rounds) and GATED: `GATE PASS … 01c4038`, 11 lanes resolved unnarrowed.** Gated three times —
  `17e76b7` before review, `80579d9` and `01c4038` after each round.
Task 11: **the realtime stack now runs as one process against a real database.** NOTIFY → tailer →
  hub → splice → codec → socket is exercised end to end under `-race` on `postgres:18-alpine`.

---

### Task 12 pre-flight (controller, 2026-08-12, session 60) — FOURTEEN findings, all in the brief

Read against the tree at `dd1b94a` before dispatching. Recorded in full as a CONTROLLER
PRE-FLIGHT block at the top of `task-12-brief.md`. BASE for the review package is **`dd1b94a`**.
Pre-flight found 6, 8, 11, 13, 21, 17, 12 and 14 defects across the eight tasks it ran on.

Task 12: **"no holes" MUST NOT mean contiguous ids** — `run_events.id` is a single GLOBAL counter,
  so one run's ids are a subsequence with arbitrary gaps, and with 8 writers over 4 runs those gaps
  are the normal case. The assertion is SET EQUALITY between ids received for run R and ids
  committed for run R, plus strictly ascending and no duplicates. **A contiguity check would fail
  on correct behaviour** — the most likely way to write this test wrong.
Task 12: `SubscriberQueue` must be raised for test 1 via `bootServe`'s `tune` hook, or eviction
  silently becomes the thing under test instead of gaplessness. Eviction-and-resume is test 2's
  job and wants the opposite.
Task 12: the eviction MECHANISM spelled out so the implementer does not fight it — a client that
  stops reading fills the socket buffer, so `Serve` blocks inside `WriteEvent` while the hub queue
  fills and evicts with `CloseSlowConsumer`; **`Serve` only notices when the write unblocks**, i.e.
  when the client reads again. Nothing prunes `run_events`, so `Oldest` never forces a re-snapshot
  and the resume is lossless.
Task 12: test 3 must commit the project-B events BEFORE the stream opens or "including during
  replay" is not exercised at all — one path tested, three reported.
Task 12: **the docs are SIX knobs, not the plan's "from Task 2"** (which means Task 11).
  `snapshot_events` was added during Task 11 and appears in no earlier list.
Task 12: **THE README MUST CARRY EVERY LIMITATION THE PLAN DISCOVERED**, not just the
  process-local one the plan names. All five are real, all confidentiality-safe, all already
  documented in code: process-local fan-out; the per-run/quiescent-run stream that cannot close on
  partial revocation (**the plan's one open design question — document, do not fix**); the
  revoke-then-re-grant permanent hole during catch-up; the true staleness window
  (`grant_recheck` + one in-flight page, and up to 3× while the resolver is failing); what a client
  sees on eviction and that reconnecting is correct; and the 403-shaped terminal frame.
  **Writing only the good news would be the same failure as a false safety comment, at
  documentation scale.**
Task 12: row 10 gets `**done** — 2026-08-10-caesar-10-realtime-sse.md` matching rows 02-05 (no
  backticks); **NO operator-smoke dependency** unlike rows 07 and 08, and no manual checklist is to
  be invented for it.
Task 12: finding 1 is the collision-and-reuse list — Task 11's fixture (`bootServe` with its
  `tune` hook, `openStream`, `sseStream.await/rest/awaitLive`, `appendRuntimeEvent`,
  `seedRealtimeRun`, `realtimeTestConfig`, `login`, `shutdown`, `testPassword`) is exactly what
  this task needs and must be REUSED, not rebuilt. `runs.AppendEventsForTest` is unreachable from
  `internal/app`, which is why `appendRuntimeEvent` exists.
Task 12: **finding 13 is the last chance on the port-discovery fragility** — `awaitListening`
  parses a production log line's exact message text and `addr` key; Task 11 accepted it with four
  dependents and this task doubles that to eight. Harden via a seam or accept with a reason;
  silence is not an option.
Task 12: `-count=10 -race` required, and **a flake is a real bug** — not to be fixed by loosening
  an assertion, adding a sleep or widening a timeout.
Task 12: implementer dispatched on Opus 5 at BASE `dd1b94a`, with a seven-row mutation table and an
  explicit licence to dissent.

Task 12: implemented at `6016635` — 4 tests in `internal/app/realtime_e2e_integration_test.go`,
  plus the README section and the roadmap row. Filed **DONE_WITH_CONCERNS**. **GATED BEFORE REVIEW
  on a confirmed-clean tree: `GATE PASS … 6016635`, 11 lanes resolved unnarrowed, contracts clean.**
  **`-count=10 -race` on the four tests: ok, 46.3s, NO FLAKE.** Integration lanes really ran
  (Docker Desktop 29.6.2, `postgres:18-alpine`, real socket); full `backend:test:integration` ok
  (`internal/app 52.098s`). **Review NOT dispatched — session paused here at the user's request.**
Task 12: ⚠️⚠️ **TWO OF MY SEVEN MUTATION ROWS COULD NOT REDDEN ANYTHING, both demonstrated green
  rather than argued.** The eighth and ninth controller errors on this plan.
  (a) Row 2 ("deliver each live event twice") is absorbed by `send`'s cursor guard — doubling
  delivery in `Hub.Publish` left it green 3/3. Replaced with a faithful wire-level duplicate, which
  reddens.
  (b) Row 3 ("remove `send`'s `e.ID <= st.cursor` guard") does not redden the test the plan
  describes, because **`prime` jumps a fresh open's cursor to the snapshot watermark, so a
  fresh-open stream cannot falsify the dedup guard at all** — demonstrated green. Test 1 now runs
  TWO subscribers (one fresh, one resuming) with `ReplayBatch: 1` and the load started before the
  opens; the mutant is then red 5/5. **This is the fourth task whose named acceptance mutation
  proved nothing as written (7, 8, 9, 12) — and the first where the pre-flight author was the one
  who wrote the useless mutation.**
Task 12: **my finding 5 understated the eviction burst by ~3 orders of magnitude.** A stopped
  client absorbs ~700-830 KiB before `Serve` blocks — not one `SubscriberQueue` of events. Test 2
  pins `SO_RCVBUF` and uses an 8 MiB burst, and logs the measured margin every run so a future
  kernel or buffer change shows up as a number rather than a flake.
Task 12: **finding 10's third limitation was NARROWED by the implementer, and the narrowing is the
  interesting part.** The revoke-then-re-grant permanent hole is the ATTENTION stream's, in catch-up
  AND live; **a per-run stream cannot reach it** — denied catch-up does not advance the cursor, and
  a denied live event closes the stream outright. The README states the narrowed version. I had
  written the broad claim into the brief from Task 10's re-review without checking which stream
  shape it applies to.
Task 12: finding 13 ACCEPTED rather than hardened, with reasons: the log parse is in ONE function
  so this task did not double the exposure as I claimed; hardening needs a test-only seam on
  `runServe` (the shape `queryGate` already refused on an earlier plan); and the log line is
  production behaviour an operator depends on. Reasonable — **the review should still weigh it.**
Task 12: REVIEWED (Opus 5, session 61, package `review-dd1b94a..6016635.diff`), report at
  `task-12-review.md`. **Spec ✅ compliant — all fourteen pre-flight findings verified against
  source, not against the report. Task quality APPROVED.** One Important, six Minor.
  The reviewer independently CONFIRMED all three of the implementer's accepted concerns from the
  code (not the report): row 2 is absorbed by `send`'s cursor guard at `stream.go:409-419`; row 3
  is unfalsifiable on a fresh open because `prime` sets `st.cursor = max(cursor, watermark)`,
  while the resume branch sets `st.cursor = req.LastEventID` and skips the snapshot — so the
  two-subscriber `ReplayBatch: 1` fix is *the only* shape that reddens it; and the per-run
  narrowing is load-bearing (`follow` closes a per-run stream outright at `stream.go:608`, and a
  denied per-run replay page never advances the cursor, so a re-grant DOES backfill).
  **The README was checked claim by claim against the producing code and holds** — all six knobs
  and defaults against `config.go` `defaults()`, all six limitations, both quoted log strings,
  and the `[hb, 2×hb)` heartbeat window arithmetic. That is the first time on this plan that a
  load-bearing prose block survived a line-by-line read intact.
Task 12: ⚠️ the reviewer could not run the Docker-backed tests, so the seven mutation OUTPUTS are
  unreproduced — it verified each mutant's MECHANISM against source instead and found every
  reported outcome consistent. Also flagged: the 700-830 KiB socket-chain margin is a macOS
  loopback figure and the gate's integration lane is Linux; test 2 logs `the evicted client
  received N of 256 frames` every run so it re-measures. **Read that N on the first CI run** —
  if it approaches 256 the margin is gone and the test goes red over CORRECT production code.
Task 12: finding 13 verdict — **"accept, no seam" STANDS** (the `runServe` hook is the shape
  `queryGate` already refused, and the log line is production behaviour `awaitListening`'s
  `:0`-suffix refusal deliberately guards). But the implementer's argument surveyed one
  alternative and concluded the space was exhausted: `serve.go:248`'s message and
  `awaitListening` are BOTH in package `app`, so an unexported const dedupes an existing literal
  with no test-only production seam — the same pattern this plan already chose for
  `AttentionKindPatterns` and `sseHeaders`. Closes the rename half only; the `addr` key is a
  struct tag and cannot be a const. Folded into fix round 1 as Minor.
Task 12: fix round 1/5 — dispatched FRESH (Opus 5); `task12-impl` died with session 60, so the
  brief + report file carried the memory as the skill prescribes. Folded ALL SIX Minors and the
  finding-13 const into the one round rather than leaving them for the final review.
  Committed `44332db` (1 commit, 4 files, +148/-74). Every item addressed, none refused:
  Important 1 (preburst comment) rewritten — the watched run's backlog IS contiguous and the gaps
  come from the writer phase; Minor 2 now a loud guard on the `snapshot_events` coupling; Minor 3
  `openStream` collapsed to a delegate of `openStreamFrom` (eight tests now share ONE open-time
  contract); Minor 4 shared service construction; Minor 5 writer count COUNTED not divided (at
  `writers = 9` the division expected 2 where 3 commit — a false failure); Minor 6 three slice
  indexes fail with a reason instead of panicking; Minor 7 the `q` clause now names three refusing
  cases (zero, negative, unparseable) because `accept.go:59-63` is `err != nil || weight <= 0` —
  the review said two. Finding 13 TAKEN: `serve.go` gains `const msgListening`, the fixture
  matches it quoted; the `addr` half cannot follow (struct tag) and the const's doc says so.
Task 12: fix round 1 evidence — mutants were COPIES outside the repo via `-overlay`, applied by a
  script that refuses unless each anchor matches exactly once; `git status --porcelain` empty
  throughout. Whole `internal/app` integration package ok 37.2s; `-count=10 -race` over the seven
  tests that reach the wire through the collapsed opener ok 67.1s, NO FLAKE; `backend:test`,
  `backend:test:integration` and `contracts:check` all clean. Output pristine.
Task 12: **the eviction margin now has a Linux-vs-macOS data point: `the evicted client received
  25 of 256 frames`** — ~10x margin, consistent with the reported 700-830 KiB. Still worth reading
  on the first CI run, but the alarm is much less likely than the review feared.
Task 12: **`GATE PASS  caesar-10-realtime-sse @ 44332db` — 11 lanes resolved unnarrowed**, commit
  stamped in the binary and in `caesar-git:dev`, contracts clean. Gated at the LITERAL sha on a
  confirmed-clean tree with no reviewer in the worktree. Logs `/tmp/caesar-gate.ZvkcBb`.
Task 12: re-review of fix round 1 (Opus 5, `review-e6ac0ec..44332db.diff`), report at
  `task-12-rereview.md`. **ALL SEVEN FINDINGS + finding 13 ADDRESSED. No new Critical/Important
  breakage.** The re-reviewer traced finding 1's replacement comment clause by clause against two
  independent id measurements rather than accepting it — the "comment that fixes a false comment"
  class finally converged, and it converged because the implementer DROPPED one of the reviewer's
  suggested clauses ("the other three runs get a snapshot backlog of their own") as itself false:
  no stream in the test opens on those runs. The reviewer withdrew the suggestion.
Task 12: **the re-reviewer's own two prescriptions were BOTH weaker than what the implementer
  built, and it said so.** (a) Finding 2's guard bound: the review proposed
  `backlog + writers*perWriter + 1` = 361, which counts all eight writers against a run that
  receives two — it would have refused a config band where the test is still correct, i.e. THE SAME
  false-failure shape as finding 5, inside the finding that complained about it. The implementer
  used `wantCount` (241), which also cannot drift from the final count check. (b) Finding 3's
  direction of collapse: the base moved INTO the fixture file, not the reverse, so the fixture does
  not depend on the newer file that depends on it. Finding 13's const verified against all four
  disqualifying seam shapes — `runServe`'s signature unchanged, message byte-identical, no
  test-only production element, and the test still merely OBSERVES the log line.
  **That is the tenth and eleventh controller/reviewer item to be wrong with the implementer right.**
Task 12: minor (deferred): `serve_realtime_integration_test.go:255-257` — `sseStream`'s doc still
  says the deadline is the one "`openStream` set"; after the collapse `openStreamFrom` sets it and
  `openStream` is one of two entry points. Introduced BY the fix; incomplete, not false.
Task 12: minor (deferred, out of scope of the fix): `runs/api/accept.go:32` — the production doc
  comment says "Only q=0 is read", the same narrowness finding 7 corrected in the README. The
  README is now the MORE accurate of the two documents; `:60` also refuses a negative and an
  unparseable `q`.
Task 12: complete (commits dd1b94a..44332db, review clean after one fix round) — GATED at
  `44332db`, 11 lanes unnarrowed. **ALL TWELVE TASKS NOW COMPLETE, REVIEWED AND GATED.**

### FINAL WHOLE-BRANCH REVIEW (Opus 5, session 61) — `64de023..44332db`, 37 commits

Report at `final-review.md`. **VERDICT: READY TO MERGE. Nothing Critical, nothing Important.**
Eight Minors (M1-M8). Triage of the 26-item deferred queue: **0 must-fix, 22 fine to carry,
3 mistaken as stated, 3 already resolved.**

It verified rather than assumed, and this is what it checked outside the diff: `errors.Is(err,
http.ErrNotSupported)` against `go1.26.5/src/net/http/responsecontroller.go:145-147`;
`FlushError` writing the header first against `server.go:1724-1727`; context isolation via
`go list -deps ./internal/realtime/...` (reaches only `platform/{authn,httpx,xerrors}`); every
`h.mu`/`s.mu` pairing (no cycle exists); idempotency of all four `Close` paths; goroutine joins;
`pgxpool.Close` not hanging a catching-up replay; and the README claim by claim a SECOND time.
**Task 12: minor (deferred → NOW FIXED, see fix wave): M1-M8.**

**M1 and M2 are new behavioural findings nobody saw in twelve tasks, both DEMONSTRATED under
`go test -overlay` from a scratchpad outside the repo, both fail-closed:**
  **M1** — a grant *ADDED* after a stream opens never reaches it. `Hub.Publish` filters on the
  scope resolved at open time and `follow` deliberately never rewrites it (rewriting would race
  the tailer). Revocation is acted on; ACQUISITION IS NOT, for the life of the connection. The
  README's limitation 3 attributed the hole to the single cursor, which invites the reader to
  conclude new events on a newly granted project flow — for a re-grant they do, for a FIRST-time
  grant they never do.
  **M2** — a `Last-Event-ID` ABOVE the head silences the stream permanently. `prime` validates
  only against `Oldest()`, so the cursor sits above every id that will ever exist: 200, headers,
  heartbeats, no data, nothing logged. Not reachable from a browser (`EventSource` echoes back an
  id this server wrote) but reachable from a hand-rolled client and after any restore-from-backup
  or re-seed of `run_event_counter` while clients hold higher ids.

### FINAL-REVIEW FIX WAVE — M1-M8, one dispatch, two commits (session 61)

Owner chose to land all eight Minors before merge rather than defer. Report at
`final-fix-report.md`. **Status DONE, no refusals.** `77ead72` (code: M2 + M3 with tests) and
`608db93` (prose: M1, M4a-d, M5, M6, M7, M8). The agent died on an API 529 AFTER writing its
report — commits 17:12/17:15, report 17:18, failure 17:22 — so nothing was lost. Verified by
compiler, not the language server: `BUILD_OK`, `go vet` clean (plain, tagged, and under the repo's
own `-vettool`), whole unit suite `-race` clean, `git status` empty.
**M2's shape: the re-snapshot, with `Head` read only inside the `>= oldest` branch** — the only
branch whose outcome a head can change — so a too-old resume still costs one query and an ordinary
resume costs two, once per stream OPENED, never per event. `Head` failure is a plain error, not
tagged `ErrNotStarted`, so `StreamServer`'s narrow contract is untouched.

**⚠️ M2's mutation 2 found a real gap in the FIRST version of its own test.** The test initially
asserted only the above-head case, and a `LastEventID < head` mutant passed the WHOLE package —
nothing pinned "exactly at the head resumes", which is the COMMON case (a caught-up client
reconnects with exactly the head) and where re-snapshotting would re-send the entire backlog on
every reconnect. The existing suite could not see it because
`TestServe_DiscardsQueuedEventsAtOrBelowTheCursor` has an empty snapshot at watermark 10, so both
paths leave the cursor at 10 and the outcomes coincide. Now a two-case table; both mutants redden.
**This is the value of demanding the mutation on the fixer's OWN new test, not just on the code.**

**THREE of the final review's prescriptions were adjusted because the prescribed wording was
itself false — items 13, 14 and 15 of the reviewer/controller-wrong tally:**
  1. **M1's suggested sentence "does not reach it at all" is false of CATCH-UP**, where `replay`
     re-queries with the refreshed scope. The README now attributes the gap to live fan-out and
     names catch-up as the exception.
  2. **M3's reset condition "until a `Wait` succeeds" was too narrow** — on a quiet database a
     HEALTHY waker only ever returns the poll deadline, so a nil-only reset would have silenced
     every outage after the first. Reset is on any non-fault return. A real bug, avoided.
  3. M5's "cannot be a compile-time reference in either direction" does not transfer to
     `internal/app`, which does import `config`; the clause was dropped rather than restated.

Fix wave: minor (deferred): `internal/app/realtimeports_test.go`'s `recordingSource.Head` returns
  `(0, nil)` unconditionally, so a future test there that resumes would silently take the
  re-snapshot path. Left alone deliberately (no test there resumes; direction is safe) but it is
  **exactly the fake-more-capable-than-reality shape this branch has paid for** — the two fakes
  that matter carry an explicit `head` whose zero fails loudly instead.
Fix wave: NOT done, as directed: the open design question, and the review's recommendation 3
  (write the hub's lock invariant into `hub.go`) — a real suggestion, outside M1-M8.

### RE-REVIEW OF THE FIX WAVE (Opus 5) — `44332db..608db93`, report `final-fix-rereview.md`

**ALL EIGHT ADDRESSED (M1-M8). No new Critical/Important breakage. READY TO MERGE. Nothing open.**
`GATE PASS  caesar-10-realtime-sse @ 608db93` — 11 lanes resolved unnarrowed, contracts clean,
logs `/tmp/caesar-gate.7pvybu`. Worktree untouched by the review; every mutation ran via
`go test -overlay` against scratchpad copies.

**The implementer was right on all three adjusted prescriptions, and the reviewer verified the
hard one against PRODUCTION rather than the fake:** pgx v5.10.0's `normalizeTimeoutError` returns
`&errTimeout{err: context.DeadlineExceeded}` with an `Unwrap` (`pgconn/errors.go:151-165`,
`:191-207`), so a real poll deadline through `pgWaker.Wait` DOES match and DOES reset the flag.
The review's prescribed nil-only reset would have wedged the flag on any quiet deployment and
downgraded every later outage to `Debug` — **strictly worse than the finding it was fixing.**

**M2's boundary independently mutated by the reviewer, both directions:** `<=`→`<` reddens only
"exactly the head, resumed"; the bound removed reddens only "above the head, re-snapshotted". Both
adjacent contracts intact — the single `ctx.Err()` funnel (`stream.go:273-275`), and `ErrNotStarted`
has exactly one attachment site guarded on `http.ErrNotSupported`, unreachable from `prime`.

**The fake question, settled with a demonstration:** `streamSource.head` and `fakeSource.head` are
faithful because the real `Head` is `coalesce(max(id),0)` — zero-means-empty-table MATCHES
production. An overlay forcing both to 0 reddens four resume tests. The fifth
(`DiscardsQueuedEventsAtOrBelowTheCursor/a_caught-up_resume`) is insensitive by construction and the
implementer disclosed that himself.
**The reviewer DISAGREED with the implementer's own self-flag on `recordingSource.Head` and closed
it rather than parking it:** `Oldest 0 / Head 0 / Snapshot empty / Replay nil` is a self-consistent
EMPTY TABLE, and re-snapshotting a resume there is the CORRECT behaviour under this fix. That
supersedes the "minor (deferred)" line recorded for it in the fix-wave block above.

**Bonus the final review did not anticipate:** on an empty or restored `run_events` both `Oldest`
and `Head` return 0, so M2's fix also un-silences that case — which is the restore-from-backup
scenario that made M2 reachable in the first place.

Fix wave: minor (deferred): `realtime/api/api.go:199-203` carries the same short ending list M4b
  just fixed. Harmless — `logStreamEnd` below it is complete.
Fix wave: minor (deferred, pre-existing): `defaults_test.go:17-22` says "legal" where no
  import-boundary analyzer actually exists.
