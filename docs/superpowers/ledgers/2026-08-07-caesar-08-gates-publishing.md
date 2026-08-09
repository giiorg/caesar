# SDD ledger — plan: docs/superpowers/plans/2026-08-07-caesar-08-gates-publishing.md


## >>> RESUME HERE (fresh session, 2026-08-09 — written by session 53) <<<

**Tasks 1-14 COMPLETE, all gated and reviewed clean. ONE task remains (15), then the
WHOLE-BRANCH REVIEW.**
**Task 14's last code commit is `06c9afe`, gate GREEN there AND the runner lane green.
HEAD carries one docs-only commit on top (the tracked ledger snapshot). Tree clean.**
**Run `git rev-parse HEAD` and use THAT as Task 15's BASE** — not `06c9afe`. The
docs-only commit on top is exactly the trap that made session 53 re-derive Task 13's
base; the review package needs the true HEAD or it truncates the task.

Session 53 executed Tasks 13 AND 14 end to end. Task 13: zero production lines, one
fix round. Task 14: seven commits, three production files, **no fix round** — the
first task on this plan to pass its review clean on the first pass.

**NEXT ACTION: generate the Task 15 brief, then PRE-FLIGHT it before dispatching.**

```
<skill>/scripts/task-brief docs/superpowers/plans/2026-08-07-caesar-08-gates-publishing.md 15
```
where `<skill>` = `/Users/sofo/.claude/plugins/cache/claude-plugins-official/superpowers/6.2.0/skills/subagent-driven-development`

**The pre-flight has paid for itself on every single task of this plan** — 9 BLOCKER
on Task 14's brief, including a central assertion that was UNSATISFIABLE both before
and after the brief's own prescribed fix. Do not skip it for Task 15 on the grounds
that it is "only docs": Task 15 is where the plan's own text gets corrected, so a
wrong premise there is a wrong premise in the permanent record.

### >>> WHAT TASK 15 OWNS <<<

**Task 15 VERIFIES rather than creates** — the plan was already committed (`5cb627b`),
a deliberate deviation, because 8,993 lines of corrected text should not sit
untracked. Known doc items, accumulated across the plan:

1. **Plan line 45 (decision 14) is now WRONG.** It says publish failures split THREE
   ways with `publish_conflict` the only halting code. It is now FOUR, with
   `repo_unavailable` and `auth_expired` also halting.
2. Plan 05's docs table (that plan, line 92) documents the `phase.materialized`
   payload as `{node_id, attempt}`; it gained a `generation` key.
3. Add an assertion that the migrated schema equals `schema.sql` — **nothing checks
   that today**; `migrate_updown_integration_test.go` only walks down-then-up and
   checks two table names.
4. Record that `CountSettledPublications` counts EVERY intent including `pending`,
   the name being deliberately wrong.
5. Record that `CodeRevisionRoundsExhausted` is deliberately unused (the max-rounds
   refusal terminalizes nothing, so it carries no outcome code).
6. `backend/README.md` + roadmap row 08.

### >>> THEN: THE WHOLE-BRANCH REVIEW <<<

Point it at every `minor (deferred)` and `parked` line in this ledger — there are
~28 across Tasks 1-6 plus everything Tasks 7-14 added — and at `19bd487`, whose new
test never got a quality review. Dispatch it on the most capable model.

**Three things it must be told explicitly:**

- **W1 — a pre-existing test is a SURVIVOR.**
  `TestPublish_CrashAfterPRCreateAdoptsTheExistingPullRequest`
  (`publish_integration_test.go:626`, Task 9's file) PASSES with `CrashAfterPR`
  turned into a no-op; its sibling push test kills the same mutation. **Mutate BOTH.**
- **`task backend:test:runner` is attested by an IMPLEMENTER, not a reviewer, and
  `scripts/gate` does not cover it.** Run it.
- **`orchestrator.go:653`'s `run.finished` audit has NO coverage** — two agents
  independently destroyed its subject and nothing failed.

### Task 13's two carry-forwards into the WHOLE-BRANCH REVIEW (not into Task 14)

- **W1 — a pre-existing test is a SURVIVOR.**
  `TestPublish_CrashAfterPRCreateAdoptsTheExistingPullRequest`
  (`publish_integration_test.go:626`, **Task 9's file**) PASSES with `CrashAfterPR`
  turned into a no-op. Its sibling push test kills the same mutation. As of `d02ce46`
  the golden set's own table pins that window, but the service-level test does not.
  **Mutate BOTH when reviewing, not just the one named here.**
- **Important 2, PARKED BY sofo's RULING:** `newGoldenEnv` duplicates ~55 lines of
  the seed test's world-staging. Deferred because Task 13's job is clean acceptance
  evidence and `TestGoldenSet_MonthOneLoopThroughRealRoutes` is the ONLY cover for the
  synthetic patch shape (`fake/executor.go:150-163`), which it gets precisely by NOT
  wiring a remote. The reviewer's remedy — a helper taking (document, wire-remote
  flag, members) — is in `task-13-review.md` finding 2. Triage it at merge.
- The double `gate.decided` emission (`gates.go:265`) is RECORDED, not fixed: the
  golden set pins 3 events against 2 audit rows with a comment saying Task 13 records
  rather than endorses it. Whether a distinct kind (`gate.authorized`?) is more honest
  is the whole-branch review's call.

### Method reminders that cost session 52 time

- **Stale gopls diagnostics looked like a broken tree TWICE MORE** (11 phantom
  compile errors at a SHA whose gate had just passed). That is five sessions
  running. `go build -gcflags=-e ./...` before believing any diagnostic.
- **Mutate only from a COMMITTED tree.** Task 12's fix round destroyed an
  uncommitted extraction with `git checkout --`, which discards real work as
  readily as a mutant.
- **A fix that widens a vocabulary widens what can be wrong.** Relabelling
  `publish.go:869` SURVIVED until the fix itself made a wrong code there possible —
  a defect that did not exist before the round and that no findings list could have
  predicted. Re-mutate every call site AFTER a fix, not just the ones a finding named.
- **The ledger is now mirrored to a TRACKED path** —
  `docs/superpowers/ledgers/2026-08-07-caesar-08-gates-publishing.md`. It is a
  SNAPSHOT: refresh it at each task boundary (`cp` + commit), or the tracked copy
  goes stale while this one moves.

### >>> THE GATE PROCEDURE CHANGED. USE THE SCRIPT. <<<

Do not hand-run `task backend:ci` any more. Run:

```
<worktree>/scripts/gate <worktree-abs-path> <expected-sha> caesar-08-gates-publishing
```

It refuses BEFORE running anything unless the tree it lands in is the one named
(toplevel, HEAD, branch, clean tree all asserted), redirects instead of piping,
asserts all NINE lanes BY NAME, requires the build lane's stamped
`buildinfo.Commit` to equal HEAD, fails on a `-dirty` stamp, and scans for
`FAIL` / `panic:` / `DATA RACE` / Docker-unavailable. Exit 0 = gate passed.

Why it exists: the old procedure was cwd-relative at every step, so a stale `cd`
gated the WRONG TREE and still emitted a real, matching sha — self-consistent
when wrong. It has already misfired once here.

**It was falsified with six red mutants before first use** (dirty tree, wrong sha,
wrong branch, a path that is not the toplevel, an injected `t.Fatal`, and Docker
genuinely stopped). Two of them earned their keep immediately — see below.

### >>> TARGETED TEST RUNS NOW ACTUALLY WORK <<<

`task backend:test:integration -- -run 'X'` **silently discarded the filter for all
ten prior tasks** — no test lane referenced `{{.CLI_ARGS}}`. Fixed in `3313958`.
Both knobs are threaded on `backend:test`, `backend:test:integration`,
`backend:test:property`, `backend:test:runner`:

```
PKG=./internal/runs/api/ task backend:test:integration -- -run 'TestGateDecision' -v
```

`PKG` matters as much as the filter: `-run` alone still BUILDS and starts every
package's test binary, so `PKG` is what stops a targeted run from spinning
testcontainers it does not need (measured: 1.5s for one package).

**This removes the reason reviewers on Tasks 4/5/6 were told not to run tests.**
Reviewers on Task 11 onward SHOULD run and mutate. Give them the PKG form.

### Session 50's tooling commits

- `3313958` CLI_ARGS + PKG through the four test lanes
- `52f673d` `scripts/gate`
- `e892288` fix to the gate's Docker-unavailable check

### The three things session 50 proved that generalize

1. **A guard tested only green asserts nothing — including the guards you write
   to enforce that rule.** `scripts/gate`'s Docker check was a grep for
   `Cannot connect to the Docker daemon` (the Docker CLI's phrasing, and the one
   this ledger records). Stopping Docker for real showed it can NEVER fire on the
   failure it was written for: the integration lane goes through testcontainers-go,
   which says `rootless Docker not found, failed to create Docker provider`.
2. **Check that your mutant is valid before trusting that it passed.**
   `DOCKER_HOST=unix:///nonexistent` and `tcp://127.0.0.1:1` BOTH left the suite
   green — testcontainers ignores `DOCKER_HOST` and falls back to socket
   discovery. The tell was `internal/app 13.7s` in the log: a real container run.
   Only quitting Docker Desktop was faithful.
3. **Pick a metric that can move.** The first measurement of the CLI_ARGS fix
   counted `=== RUN` lines and reported 0 both before and after — Go's test cache
   does not replay verbose output. The decisive evidence was Task's own echoed
   command line.

### Session 49 executed Task 10 end to end and took Task 11 to ruled-and-ready.

Task 10 commits: `304c287`, `f657465`, `6474742`, `94d0633`, `1619413`, `371e04c`,
`77e656c`, `4200bb1`, `400db32`, then fix round `5a891bc`, `002de46`.

### The two things session 49 proved that generalize

1. **Reading is not measuring.** The Task 10 implementer ARGUED a mutant would die;
   the reviewer went further and STRUCK the item as "verified by reading". I ran it:
   it survived **5 of 5**. Cause was git's one-second commit-date granularity. Never
   accept "this would fail" — demand the run, and run the decisive one yourself.
2. **Anchor mutations on LINE NUMBERS in `publish.go`.** The live push path
   (`:702`) and the settlement rebuild (`:1596`) carry BYTE-IDENTICAL literals, so a
   `sed` pattern proves nothing about which one it hit.

### (HISTORICAL — Task 10 is COMPLETE. Kept because several items still bind Tasks 12-15.)
### ⚠️ WHAT TASK 10 INHERITED

Task 9 deliberately made Task 10's job bigger, and one of those trades is now
LOAD-BEARING rather than defensive:

- **>>> A publish failure inside the backoff of the wall deadline now leaves the
  run `running` with a parked, unclaimable successor and an intent still
  `pending`/`pushed`. THAT IS SETTLEMENT'S INBOX AND IT IS THE EXPECTED END STATE,
  not an edge case. <<<** It is exactly what `ListExpiredRunIDs` hands to Task 10.
  Round 1's report called it "belt and braces"; round 2 made it the main path.
- **A terminalized run records `Class:transient`** (`orchestrator.go:1521-1525`
  sets `runOut = out` unchanged) — a class saying "retry this" on a row nothing
  will retry. Narrower now (the publish codes no longer reach that branch) but
  still reachable for `credential_busy` and `rate_limited`. Settlement should
  decide whether the terminal record keeps the attempt's class or gets a run-level
  permanent one. Left alone deliberately.
- **Guards that stay unfalsifiable until settlement is a SECOND WRITER of the
  intent:** `reload`'s typed wrap (M3), `haltPublication`'s `n == 0` ->
  `errIntentAdvanced`, `recoverBranch`'s receipt branch (G2), and
  `CountSettledPublications` on `'halted'`. Task 10 is where each becomes
  reachable — pin them as it lands them.
- **`recoverBranch` is a NO-OP in Task 9** and its receipt branch is Task 10's
  entry point: `RecordPushReceipt` is the only writer of `head_sha` and sets
  `status='pushed'` in the same statement, so `intent.HeadSha.Valid` is always
  false while `status == 'pending'`.
- **A publish node that loses its lease is now dispatched away from
  `exec.Inspect`** to the UNCHARGED `CodePublishResumable` (Task 9 BLOCKER 5).
  Do not undo that: `reconcile_lost` is chargeable, and three lost leases would
  exhaust a node whose branch is already on the forge.
- **`Publish` is not re-entrant on the same `dir` and NOTHING cleans `dir` up** —
  a full clone plus patch file per attempt. `ExpectedPublicationHead` is NOT a free
  offline call (it clones and runs `ls-remote`; what it spares is the remote's
  REFS) and needs `authEnv` against a private forge.
- **Every commit-shaping field must be replayed from the durable row** — `Date`,
  `BaseSHA`, `Patch`, `Message`/`CommitMessage`, `AuthorName`, `AuthorEmail`,
  `TreeHash`. Recomputing any of them changes the rebuilt sha, misses the adopt
  comparison, and turns a crash-after-push into a permanent `publish_conflict` on
  a branch that is genuinely ours. `TreeHash` is a HARD contract (empty = permanent
  `invalid_output` refused before the clone).

### ⚠️ FOR TASK 12, RECORD NOW SO IT IS NOT REDISCOVERED

**The `Forge` port has no `AsOutcomeError` equivalent.** `GitPublisher` is closed on
both sides; `Forge` is not. `ClassifyOutcome` is DEFAULT-DENY, so an untyped forge
error becomes a PERMANENT `internal_error` that `unchargedWhileUnfinished` does not
downgrade. It is unreachable today only because `unavailableForge.BranchHead` fails
first. **The day Plan 12's real forge adapter lands, `runPublish`'s `OpenPullRequest`
exit is the whole I1 class again, on a `pushed` intent** — a permanent answer with
the branch live on the forge. The port interface should require implementations to
return `runs.OutcomeError`, and the adapter needs what `runsports.go` gave
`PushPatch`/`ExpectedHead`. Also open: R3, R5, R6, M7, M10 (see `task-9-rereview.md`).

### ⚠️ TWO ENVIRONMENT/METHOD TRAPS THAT COST THIS SESSION TIME

1. **Docker Desktop does not survive the crash/reboot.** Every testcontainer lane
   fails with `Cannot connect to the Docker daemon` and `backend:ci` is red
   through no fault of the code. Fix: `open -a Docker`, then wait for
   `docker info` to succeed before gating.
2. **NEVER pipe `task backend:ci`.** `task backend:ci 2>&1 | tail -80` reports
   **tail's** exit status, so a FAILED gate reads as "exit 0". Redirect to a file
   and read `$?` directly, then check the contents (nine lanes, `Version=<sha>`
   stamp, `ok` count, zero FAIL/panic/DATA RACE). Exit code alone is not the gate.
3. Related, already in the standing rules but it bit again: **the shell's cwd
   persists between calls.** A `cd backend` for `go` left a later `task`/ledger
   write pointing at the wrong directory. Re-`cd` to the worktree root.

### (HISTORICAL) Task 10 — what to do next, concretely — DONE, kept for the method

**The method below is the one that keeps paying. Apply it to Tasks 12-15 verbatim;
Task 11's steps 1 and 2 are already done.**

1. Generate the brief (**NO REDIRECT** — the script writes the file ITSELF and also
   prints a status line; redirecting races its own write and yields a CORRUPTED
   brief starting mid-word):
   `<skill>/scripts/task-brief docs/superpowers/plans/2026-08-07-caesar-08-gates-publishing.md 10`
   where `<skill>` = /Users/sofo/.claude/plugins/cache/claude-plugins-official/superpowers/6.2.0/skills/subagent-driven-development
   (Task 10 = plan lines 6811-7676, "Reconciling a publication — the crash the lease
   sweep sees".)
2. **PRE-FLIGHT IT against the real source, EMPIRICALLY** (scratch copy of
   `backend/` OUTSIDE the worktree; run `sqlc`; run the brief's own tests). This has
   paid for itself on EVERY task without exception: Task 7 = 6 BLOCKER + 8
   CORRECTION; Task 8 = 4 BLOCKER + 5 CORRECTION (incl. three guards the brief
   called load-bearing that were unfalsifiable by its own suite); Task 9 = 7 BLOCKER
   + 13 CORRECTION, and it found TWO REAL DESIGN HOLES before a line was written.
   Point it at the "WHAT TASK 10 INHERITS" section above.
3. Fold every BLOCKER and CORRECTION into the dispatch as an explicit override.

### The lesson this plan keeps re-teaching — now proven SIX rounds running

**A prescribed remedy is a HYPOTHESIS, not a patch. Mutate it; never apply it on
trust.** Every round of Tasks 8 and 9 found the round's own prescribed fix was
itself a survivor, wrong, or broken:

- a `%aI` date assertion falsifiable only on a NON-UTC host — and CI is UTC, so it
  worked on the machine that did not matter;
- a one-line assertion for a value returned from TWO sites;
- a cap test asserting a class+code the uncapped path produces anyway;
- a `branch --list` assertion that failed on UNMUTATED code;
- a mutant shape that does not compile, whose to-the-letter test would have SURVIVED;
- **a review that inferred behaviour from a TIMING DELTA** (31s->61s "meaning" halted
  intents got re-probed) — disproved by showing a provably no-op mutant moved the
  same package 37s->67s. Timing deltas in this package are not signal.

And the deepest instance, worth carrying to any project: **a falsifier that FAILS
GREEN.** A test pinning a timezone by NAME (`Asia/Tokyo`) degrades silently to UTC on
any image without tzdata — git falls back wordlessly at exit 0 — restoring the exact
survivor it was added to kill, invisibly. Use a POSIX TZ string (`JST-9`). Ask of
every new guard: *on what machine, or under what environment, does this stop being a
falsifier?*

**A tally is not a coverage claim.** Task 8's honest, verified "10 mutants, 10 kills"
sat alongside TWELVE nameable survivors. Always ask a reviewer to NAME THE MUTANTS
THE SUITE WOULD NOT KILL, not merely to audit the ones that ran.

**Crossing a package boundary silently re-types outcomes.** `gitops.TypedError` is not
`runs.OutcomeError`, and `ClassifyOutcome` is DEFAULT-DENY — so every typed git
outcome became a permanent `internal_error` and a routine transient `push_rejected`
TERMINALIZED THE RUN. Neither the brief nor the pre-flight caught it; two failing
tests did. Whenever a port hands errors across a boundary, ask what the receiving
classifier does with a type it does not recognise.

### Still carried forward from Task 7 (do not lose)

> POINT THE FINAL WHOLE-BRANCH REVIEW AT **M7-5** AND **M7-6** SPECIFICALLY: an
> unquoted `rel=next` silently truncates the probe, and the off-host guard
> compares raw string prefixes rather than parsed hosts. Both are the same
> silent-truncation / credential-forwarding class as I7-1, rated Minor only
> because GitHub happens to send the quoted form today.

Commits so far: `5cb627b` plan · Task 1 `a6f8d55..affb16c` · Task 2 `affb16c..6ff1aa9`
+ fixes `135f56a`, `19bd487` · Task 3 `edcfd94` + `7099422` (atlas.sum) + `1aff70a` (fix)
· Task 4 `539b7bc` + `1ec46bf` (fix) · Task 5 `6f60140` + fixes `0048b4e`, `7472bf1`
· Task 6 `fe5128e` + fix `1e41741` · Task 7 `50f8f30` + fixes `e7ab4d6`, `5c32766`.

### A warning that cost a whole session (2026-08-08, session 47)

A session resumed from a STALE handoff — the pre-execution one that said "Plan 08
is authored, run review round 5" — and re-ran the entire round-5 artifact review
against **the untracked pre-patch copy of the plan in the MAIN CHECKOUT**
(`/Users/sofo/Projects/caesar/repos/caesar/docs/...`), not the committed plan
here. It re-derived five findings that were already fixed in `5cb627b`, and
edited that dead file. Nothing was lost, but a session was.

**Before acting on any handoff, check `git log` on this branch first.** The main
checkout is on `main` @ `38b4f75` and its copy of the plan is DEAD. The live plan
is the committed one in this worktree, 8,993 lines.

### To continue

1. `cd /Users/sofo/Projects/caesar/repos/caesar/.claude/worktrees/caesar-08-gates-publishing`
   (a git worktree on branch `caesar-08-gates-publishing`; do NOT cd to the parent repo).
2. Invoke superpowers:subagent-driven-development. Read THIS ledger first. Trust it and
   `git log` over recollection.
3. **Task 7 is CLOSED** — brief, pre-flight, implementation, two fix rounds and both re-reviews
   are all spent. Its artefacts (`task-7-brief.md`, `task-7-preflight.md`, `task-7-review.md`,
   `task-7-rereview.md`, `task-7-rereview-2.md`) are HISTORY; read them only for carry-forwards.
   Worth knowing from it: Task 7 was the first task to leave `internal/runs` for
   `internal/platform` and the first with no database, so `CheckPlatformPurity`
   (`internal/arch/rules.go`) forbids platform importing business code, no new Go dependencies
   are allowed, and no test may make a live forge call (`httptest` only) — all three still bind
   anything that touches `internal/platform` later.
4. Tasks 8-15: regenerate each brief with
   `<skill>/scripts/task-brief docs/superpowers/plans/2026-08-07-caesar-08-gates-publishing.md N`
   where `<skill>` = /Users/sofo/.claude/plugins/cache/claude-plugins-official/superpowers/6.2.0/skills/subagent-driven-development
   — and pre-flight each one the same way before dispatching. It has paid for itself every
   single time.

### Standing rules — every one of these was learned by being burned

- **Verification is `task backend:ci`, full stop** — not `go test ./... -race` (which builds
  no `//go:build` lane at all), and not a hand-enumerated lane list. Lane-picking has now
  failed three times on this plan. `task contracts:check` is a SEPARATE must; backend:ci does
  not include it. The controller re-runs BOTH itself before accepting any DONE — reading the
  implementer's table is not verification.
- **`task contracts:check` must run against a CLEAN TREE.** Its last step is
  `git diff --exit-code` over watched paths, so any uncommitted file under
  `backend/internal/runs/store` fails it and presents your own working diff as sqlc regen
  drift. Commit first, then check.
- `task ci` cannot go green from a linked worktree (its secrets lane mounts ROOT_DIR and a
  worktree's `.git` is a file pointing outside that mount). `task backend:ci` has no secrets
  lane and works.
- **READ EVERY BRIEF AGAINST THE SOURCE BEFORE DISPATCHING.** Every brief on this plan has
  conflicted with the repo. Task 3's contained four sqlc-invalid statements and a test that
  would have passed on the wrong constraint; Task 4's invented its entire test harness and
  prescribed a grep gate matching zero lines; Task 5's invents its harness too. The pre-flight
  scan run before Task 1 did not catch any of these — only a per-task read does.
- **Subagents' plain-text final messages do NOT reach the controller in this harness.** Every
  dispatch must name a report FILE and say the file is the authoritative channel.
- **Check the name in the spawn result before addressing a subagent.** A name from a previous
  session can still resolve, and the harness silently suffixes a colliding name (`review-task-3`
  -> `review-task-3-2`). Sending to the un-suffixed name woke a dead session's agent, which
  clobbered a review file. See the Task 3 section.
- Never dispatch two implementers in parallel. A reviewer alongside an implementer is fine only
  if their files do not overlap AND the reviewer is told not to run tests — concurrent
  testcontainer runs interfere.
- Subagent models: Opus 5 or Fable 5 only, never Sonnet (user's standing preference).
- Run `go` from `backend/`, `task` from the worktree root. The shell's cwd persists between
  calls — check it.
- User's commit rule: `<type>(<scope>): <description>`, one sentence, no body, and NO AI
  attribution trailer of any kind.
- Decision 17 was locked by the user to the **runtime 409**, not a validator rule.

### Carry-forwards that will be silently lost if not read

(Tasks 5 and 6 discharged the `SupersedePhasesInSliceParams`, concurrent-revision-test,
gate-generation and `generationOf` carry-forwards. What follows is what is still LIVE.)

- **>>> TASK 9 IS THE FIRST THING THAT CAN SILENTLY BREAK THE PLAN'S BIGGEST INVARIANT. <<<**
  Task 6's review established (and strengthened) this: **a gate decision and an in-flight
  attempt cannot overlap**, because `BumpRunGeneration` and `FinishGatePhase` both require the
  run to read `waiting`; a run reaches `waiting` only via `syncWaiting`/`RunIsWaitingOnAGate`,
  whose `NOT EXISTS` forbids any live phase row but the caller's own; and — the strongest leg —
  **`ClaimPhase` requires `r.status = 'running'` on the joined run row** (`queries.sql:124`), so
  an attempt cannot even be claimed on a waiting run. TWO CONSEQUENCES: (a) the same argument is
  what makes the `CountSettledPublications` refusal safe, so the refusal's *independent* value
  only appears in the post-crash shape; (b) the whole property rests on four predicates in three
  statements, none of which names the others. Task 9 adds a publish effect that writes rows
  OUTSIDE the phase lifecycle — that is the first candidate to weaken it without noticing.
- **TASK 9** also inherits Task 6's F3: a revision past an ALREADY-SUCCEEDED publish node leaves
  that node `succeeded` with an invalidated token. Harmless today (nothing consumes
  authorizations yet); once Task 9 lands, the window between a publish node succeeding and its
  intent row existing is where it bites — and the intent commits before the first forge call, so
  that window should be zero. Verify it is.
- **TASK 9/10** own `publish_intents.status = 'halted'`, which Task 6's new three-status refusal
  loop does NOT cover — narrowing `CountSettledPublications` to `status <> 'halted'` would keep
  the suite green.
- **TASK 10** must not assume an index exists for the publish-intent settlement sweep, and none
  carries `phase_runs.status` for `ListWaitingGates`. Fine at V1 scale; just do not assume.
- **TASK 11** inherits two: `GateDecisionResult.Generation` means the generation DECIDED on
  approve/reject and the NEW generation on `request_changes` (documented on the field — keep the
  API contract consistent with it); and a concurrent revision on a SIBLING gate currently reports
  "the run's wall deadline has passed" when it has not, because `BumpRunGeneration` has two
  predicates and every empty result maps to one message. Task 11 owns the surface that renders
  that lie.
- **TASK 13's golden set** is the natural home for the one path nothing exercises end to end: a
  `gate_note` reaching the runner's real staging through `lineageInto` as `1-gate_note.json`.
  Every Task 6 integration test uses the fake executor.
- **TASK 14:** split `backend/internal/runs/store/schema_integration_test.go` — it still holds
  that package's only bootstrap (`seedRun`, `seedRunWith`, `seedRunWithAuthorization`,
  `mustInsertPhase`, `mustInsertArtifactAt`) under a "schema" name while carrying behavioural
  tests. Task 6 added one more test there deliberately rather than rename mid-task.
- **TASK 15 (docs), now four items:** the `phase.materialized` payload gained a `generation` key
  that Plan 05's docs table (that plan, line 92) still documents as `{node_id, attempt}`; add an
  assertion that the migrated schema equals `schema.sql` (nothing checks that today —
  `migrate_updown_integration_test.go` only walks down-then-up and checks two table names); note
  that `CountSettledPublications` counts EVERY intent including `pending`, the name being
  deliberately wrong; and record that `CodeRevisionRoundsExhausted` is deliberately unused (the
  max-rounds refusal terminalizes nothing, so it carries no outcome code).
- **BEFORE WHOEVER GROWS IT NEXT:** `orchestrator.go` is ~1420 lines. Task 5's review asked for
  `materializeReady`/`anyRunnablePhase`/`syncWaiting`/`materializeResumedRuns` to move to
  `materialize.go`; Task 6 was ruled out of doing it (it added ~45 lines and touched none of that
  cluster). It is still worth doing as its own change.
- The final whole-branch review must be pointed at every `minor (deferred)` and `parked` line in
  this ledger — there are ~28 across Tasks 1-6 — plus `19bd487`, whose new test never got a
  quality review (its scoped re-review was skipped for an account session limit; the controller
  verified the branch instead, which is not the same thing).

### Subagents from THIS session (2026-08-08, session 45) — do not assume they carry over

`impl-task-5`, `review-task-5`, `rereview-task-5`, `rereview2-task-5`, `preflight-task-6`,
`impl-task-6`, `review-task-6`, `rereview-task-6`, `preflight-task-7`. Their outputs are all on
disk in this directory. **Dispatch fresh agents, and check the name in the spawn result** — a
name from a dead session can still resolve, and the harness silently suffixes collisions.


Branch: caesar-08-gates-publishing
Worktree: /Users/sofo/Projects/caesar/repos/caesar/.claude/worktrees/caesar-08-gates-publishing
Merge base: 38b4f75 (main, = origin/main at start)
Baseline: `go build ./...` OK, all backend unit test packages pass.

Plan: 15 tasks, 28 baked decisions. Five codex review rounds closed before execution.
Decision 17 locked to the runtime 409 (plan line 53) by the user, 2026-08-07.

## Task index
1. Gate & publish vocabulary — statuses, outcomes, events, self-approval policy (line 122)
2. Compiler — revision transitions, gate-authorizes-publish (536)
3. Schema — gate_decisions, approved_patches, publish_intents, generations (1149)
4. Gate materialization — the run stops and waits (1827)
5. The gate decision — CAS, deadline predicate, single-use authorization (2162)
6. Generations — the revision rematerialization (3165)
7. platform/forge — hand-rolled GitHub and GitLab REST clients (3974)
8. gitops.Publish — fresh trusted checkout, precondition push (4472)
9. The publish effect state machine (5144)
10. Reconciling a publication — the crash the lease sweep sees (6639)
11. The HTTP surface — decisions, approvals inbox, projections (7494)
12. App wiring — the real forge, production intake stops being refused (7880)
13. The golden set — whole conveyor, crash injection around every publish step (8276)
14. Carry-forwards from Plans 04 and 05 (8528)
15. CI, docs, roadmap, and the plan committed (8739)

## Progress

Pre-flight scan (before Task 1): NOT clean — 20 findings, all verified against source
(A=10 contradictions/constraint violations, B=3 symbol-ordering breaks, C=7 plan-mandated
review defects), plus 3 non-category corrections. See `preflight-scan.md`.
Rulings in `preflight-rulings.md`, approved by the user 2026-08-07 (option: patch the plan
first, controller makes the judgment calls).

Judgment calls made by the controller, overridable:
- C1 duplicated finishRunTx -> FIX by extracting a free function (receiver only used for
  o.svc.audit, cp only for cp.projectID; the duplicate had already drifted on correlation id)
- C2 duplicated isUniqueViolation -> KEEP (identity/workflows/assets each already carry one;
  settled repo precedent, not a fresh choice)
- C3 duplicated graph algorithms -> KEEP (arch rule forbids the import) + require Task 2 to
  test the same fixture graphs as workflows/graph_test.go, guarding dominator drift
- C4 hardcoded generation:1 in Task 3 -> ACCEPT + add a grep gate to Task 4 Step 6
- C5 source-text test -> FIX by deleting (post-Task-12 the symbol is gone, so a reference
  fails to compile; strictly stronger than grepping a renameable file)

Plan patched and VERIFIED by the controller (not taken on the agent's report):
fences even (298), 15 tasks intact, 8820 -> 8993 lines. Confirmed absent: singular
`FindPullRequest`, bare `cp.requester`, `RequesterSource`, the source-text serve test,
`CodeCredentialBusy` in Task 10's inspect block. Confirmed present: `.base.ref`/
`target_branch` -> `Base` in all four endpoint rows, `RunIsWaitingOnAGate` in Task 3 Step 7
with Task 4 as first caller, free-function `finishRunTx`, `authorizationFromIDRow` called
in Task 10, four new `task db:sqlc` steps (Tasks 5/6/9/10), the Task 2 dominator-twin
guard, the Task 4 generation-literal grep gate, and the Task 9 adapter wiring with
Task 12's text updated to say what it no longer does.

DEVIATION from the plan: the plan file is committed NOW (5cb627b), not by Task 15.
It was 8993 lines of untracked work and the corrections had to reach git history.
Task 15 verifies the plan is committed instead of committing it.

Task 1: BASE = 5cb627b
Task 1: implementer raised 2 brief-vs-code conflicts; controller verified both and endorsed.
  (a) `status_property_test.go TestPropertyTerminalStatesAreSinks` asserts terminal states
      admit no transitions, which the new `succeeded -> superseded` edge falsifies. NOT a
      plan conflict: baked decision 2 declares that edge "the one edge out of a terminal
      phase state in either transition table". Narrow the property to that exact pair,
      assert the pair is legal (so deleting the edge also fails), and add
      `PhaseWaitingDecision` to `allPhaseStatuses`. Property lane is behind
      `//go:build property` — `task backend:test:property` must be run and reported.
  (b) brief's tests touch unexported `phaseTransitions`/`snapshotFromRow` but the test
      files are `package runs_test`. Move those to in-package files; precedent is
      `retry_test.go`/`ports_test.go`/`buildrequest_test.go`.
  Also: 3 `policy_test.go` assertions break on the schema bump; use 99 not 3 as the
  "wrong schema" value so the next bump can't turn a negative test positive.

Task 1: implementer DONE, commits a6f8d55..2bbb5aa. Controller verified independently:
  build clean, `go test ./... -race` all green, `go vet` clean, property lane (3/3) green.
Task 1: review — SPEC OK, QUALITY not approved. 1 Critical + 2 Important + 4 minor.
  Critical: `task contracts:check` red — `api/dto.go:47` embeds `runs.PolicySnapshot`, so
  `AllowSelfApproval` changes generated `backend/api/openapi.json` + `web/src/api/schema.d.ts`,
  neither regenerated. Controller re-confirmed the drift by regenerating and diffing.
  Would have failed at TASK 3's verification step for a reason unrelated to Task 3.
  Important: the two new `retrySchedules` rows are exercised by nothing (retry.go:27-28) —
  and neither Task 9's nor Task 10's brief owns retry.go, so it would never be closed.
  Important: `phaseIsLive(PhaseWaitingDecision)` claim in orchestrator.go:73-81 asserted nowhere.
Task 1: minor (deferred): tautological `wantRetryable` column in outcome_test.go (brief-verbatim,
  becomes meaningful at Tasks 9/10); DefaultSnapshot test overlap (brief asked for it); no test
  pins the wire string "waiting_decision" (load-bearing for Task 3's CHECK literal).
Task 1: fix round 1/5 (3 addressed, 0 open — contracts regenerated + committed, retry
  schedules pinned, gate-holds-no-lease asserted; commits 2bbb5aa..affb16c). Re-review
  verified all three BY MUTATION (flipped the zero floor, deleted the row, made
  phaseIsLive true for gates — each fails the new test, each reverted clean).
Task 1: complete (commits a6f8d55..affb16c, review clean, 3 minors deferred)

Task 2: BASE = affb16c. Brief regenerated post-patch (task-2-brief.md, 632 lines).
Task 2: controller resolutions handed to the implementer (deviations from brief text, disclose to reviewer):
  (a) brief's Step 1 tests are written in-package but `compile_test.go` is `package runs_test`.
      TestCompile_* go there with `runs.` qualifiers; the three twin graph tests exercise
      unexported ExecPlan methods, so they go in a NEW in-package `compile_graph_test.go`.
      Same shape as Task 1's conflict (b).
  (b) brief adds `conveyorDoc()`, but `compile_test.go:13` already has `conveyor()` — the same
      five-node graph, differing only in `impl` vs `implement` and the missing Revision.
      RULING: reuse `conveyor()` + add the Revision to its gate; substitute implement->impl in
      the new tests. Avoids a fresh 25-line duplicate fixture in one file. All other brief
      values stay verbatim. (The pre-flight scan did not catch this one.)
  (c) `internal/app/runsports_test.go` does not exist; `pinnedFromVersion` is unexported, so
      the new file must be `package app`.
Task 2: pre-checked blast radius — `Compile` has ONE production caller (service.go:124) and
  one test file; `HasUnsupportedNodes` has exactly 3 references (compile.go:238, tasks.go:140,
  compile_test.go:52 — the brief names only the first two). `ExecPlan` is not referenced
  outside internal/runs, so gaining unexported fields breaks no external cmp.Diff.

Task 2: implementer DONE_WITH_CONCERNS, commit 6ff1aa9. Controller verified INDEPENDENTLY on that
  commit: `go build ./...` OK, `go vet ./...` OK, `go test ./... -race -count=1` all packages ok,
  `task backend:test:property` green (workflows + runs), `task contracts:check` green.
  Contracts genuinely unaffected: DefNode/PlanNode/ExecPlan/DefinitionDoc reach no api/dto.go —
  the wire format carries the *workflows* document, not the runs mirror.
Task 2: implementer's own deviation (1): dropped the unused `byID` param from `assignGates`; the
  brief's body never reads it. Unexported, not in the Produces contract. Controller endorses.
Task 2: implementer went BEYOND the brief in two places, both disclosed to the reviewer:
  (i) two negative-path tests covering all four assignGates error returns (brief specified none);
  (ii) made `internal/workflows/graph_test.go` symmetric with the new runs twin (test-only, +182/-…)
      — arguably mandated by the brief's twin requirement, reviewer to judge scope.
Task 2: controller resolved the implementer's open question on `Ready`. The brief's Files line
  lists `Ready :219-233` under Modify, but no step describes a change and plan line 1893 has
  Task 4 CONSUMING `ExecPlan.Ready` unchanged. NOT a gap — leaving it alone is correct.
Task 2: carry to Task 15 — `backend/README.md:140` now stale ("a definition containing gate or
  git-publish nodes is rejected at intake"). Intake is still refused, but by `unavailableForge`
  alone until Task 12, so the paragraph needs a coordinated rewrite, not a one-line edit.

Task 2: review — SPEC ✅, QUALITY APPROVED. 0 Critical, 0 Important, 7 Minor. The reviewer
  recomputed all 15 graph literals by hand and confirmed the two twin files agree with each
  other AND with the fixtures, and it verified `assignGates` reproduces
  `workflows.validatePublishes`/`validateRevisions` rule-for-rule — so the re-derivation cannot
  reject a document the validator accepted. It also closed the one input-selection divergence
  (a publish with two patch in-edges) as unreachable: validate.go:215-220 forbids it.
  Both controller-authorized deviations and both extras were judged sound; dropping `byID`
  was called dead-API removal, not a substantive deviation.
Task 2: minor (deferred), 7:
  M1 `RevisionSlice` returns the internal slice, not a copy — a caller mutating it corrupts the
     compiled plan for every holder, and under the orchestrator that is a race. LOAD-BEARING
     FOR TASK 6 (it consumes RevisionSlice) — carry this into Task 6's dispatch.
  M2 the twin graph tests build via `Compile`, so a dominator regression surfaces as
     "no dominating gate" rather than the literal mismatch that names the disagreement;
     constructing `ExecPlan{Order,Nodes}` directly would keep the diagnostic on the graph.
  M3 `assignGates` omits the validator's "revision target must be agentic" check
     (compile.go vs validate.go:314-317). Not a spec miss (brief specified existence+dominance
     only), but a corrupt version where a gate rewinds to a gate compiles. RELEVANT TO TASK 6.
  M4 `descendants` rebuilds its children index per call, inside assignGates' nested loop.
     <=50 nodes, mirrors the validator; hoisting would not diverge from the twin.
  M5 index-based fixture mutation `doc.Nodes[3].Revision` (compile_test.go:811,934).
  M6 compile.go is now 481 lines (from ~250); ~70 are general-purpose graph helpers.
     `internal/workflows` splits exactly this into graph.go — an internal/runs/graph.go would
     mirror the twin's file layout too.
  M7 stale test name `TestGraphDominators_DiamondAndMultiRoot` now covers four graphs.
Task 2: both reviewer ⚠️ items resolved by the controller. (1) `Ready` — the Files line range was
  context, Task 4 consumes it unchanged; no gap. (2) README:140 staleness — carried to Task 15
  above; confirmed it is on that task's list.
Task 2: complete (commit affb16c..6ff1aa9, review clean, 7 minors deferred)

Task 3: BASE = 6ff1aa9. Brief regenerated (task-3-brief.md, 715 lines).
Task 3: controller resolutions handed to the implementer (gaps in the brief, disclose to reviewer):
  (a) brief writes `ctx, pool := testdb.New(t)` in three tests; the real signature is
      `func New(t *testing.T) *pgxpool.Pool` (testdb.go:126) — one return value.
  (b) brief says "follow the neighbouring integration tests' bootstrap" but `internal/runs/store`
      has ZERO test files. The model is `internal/runs/testutil_integration_test.go`
      (`fixture`/`newFixture`/`newRunRow`, peer tables seeded with raw SQL); it cannot be
      imported (different package), so the bootstrap must be rewritten in `package store_test`.
  (c) NONE of the seed helpers the brief calls exist — seedRun, seedRunWithTwoGateDecisions,
      seedRunWithAuthorization, mustInsertPhase, mustFinishPhase are all to be written.
Task 3: controller pre-grepped the six call sites Step 8 breaks (queue.go:89 InsertPhase;
  orchestrator.go:910 InsertArtifact; orchestrator.go:507/:943 + tasks.go:312 SupersedeLivePhases;
  GetNodeArtifact at orchestrator.go:759/:821) and emphasized that the comment
  `// Task 4 threads the run's real generation here.` is text Task 4 Step 6 GREPS for
  (plan line 2173) — a site fixed without it silently stamps generation 1 forever.
Task 3: tooling verified before dispatch — Docker 29.6.2 up; `db:new` spins its own Atlas dev
  container (docker://postgres/18/dev); `task backend:ci` (Taskfile:186) has NO secrets lane,
  so unlike `task ci` it IS runnable from this worktree.

>>> OPEN ITEM, BLOCKS TASK 4's `task db:new` <<<
Task 3: the generated migration needed the hand-edit Step 3 anticipated, but in the DOWN
  direction: Atlas emitted `DROP COLUMN "generation"` BEFORE `DROP CONSTRAINT
  "<table>_generation_check"` for runs/phase_runs/artifacts. Dropping the column already drops
  its dependent check, so the trailing DROP CONSTRAINT fails 42704.
  `TestMigrations_DownUpRoundTrip` caught it (up passed, down failed on `runs`). The implementer
  reordered each constraint drop before its column drop, with a comment at each of the three
  sites. Controller read the down block and confirms the edit is correct.
  CONSEQUENCE: `atlas.sum` is now stale. Controller verified `atlas migrate validate` exits 1
  with "L10: 20260807125859_gates_and_publishing.sql was edited". Nothing in CI reads it —
  `atlas` appears ONLY at Taskfile.yml:125 (`db:new`), not in .github/workflows/{ci,nightly}.yml
  — so backend:ci and contracts:check are honestly green. But the NEXT `task db:new` refuses.
  FIX (needs the USER, from `backend/`; the implementer's sandbox rejects any command containing
  the bare token "hash", a lexical false positive, and the controller declined to run it on the
  subagent's behalf — that is permission laundering):
      atlas migrate hash --dir "file://internal/platform/pg/migrations?format=goose"
  Then `atlas migrate validate --dir "file://internal/platform/pg/migrations?format=goose"`
  must print nothing and exit 0. Land the corrected atlas.sum as a follow-up commit on Task 3
  BEFORE dispatching Task 4. Task 3 was told to commit without it rather than stall.

Task 3: implementer DONE_WITH_CONCERNS, commit edcfd94 (10 files + migration
  20260807125859_gates_and_publishing.sql). 4 new store integration tests RED->GREEN.
  Lane results: contracts:check PASS, unit PASS, property PASS, analyze PASS (incl. txboundary),
  vet PASS, vuln PASS, build PASS, TestMigrations PASS. `task backend:ci` FAILED in three lanes
  (fmt:check, lint, test:integration) — ALL THREE are Task 2's pre-existing regressions in files
  Task 3 does not touch. Controller verified both attributions independently.
Task 3: five implementer deviations, all disclosed to the reviewer. The two that matter:
  D4 the brief's `TestPublishIntents` SQL was INVALID (42P08: `$3` used as both a uuid FK and
     a string in `'caesar/'||$3::text`). Fixing the cast naively would have made both inserts
     collide on `publish_intents_marker_key` instead of the authorization key — the test would
     have gone GREEN while never exercising the single-use guarantee in its own name. Branch is
     now derived from the node; the assertion pins
     `pgErr.ConstraintName == "publish_intents_authorization_id_key"`.
     This is a plan-mandated defect the brief would have shipped.
  D3 four sqlc analyzer errors in the brief's SQL (ambiguous unqualified columns in
     FinishGatePhase/ConsumeAuthorization, unaliased subqueries in RunIsWaitingOnAGate,
     unbound `$2` in SupersedePhasesInSlice -> all-named args). Claim is that
     SupersedePhasesInSliceParams still has exactly RunID/NodeIds/Outcome/FinishedAt, so
     Tasks 5/6 are unaffected — reviewer asked to verify against the GENERATED code.
>>> CARRY INTO TASK 4's DISPATCH <<<
  - Step 8's premise was WRONG: the generation parameter produced NO compile errors, because
    sqlc params are named-field structs. A missed site silently inserts 0 and is caught only by
    the runtime CHECK. Task 4's grep gate is therefore the ONLY guard.
  - There are SEVEN generation-literal sites, not six. A grep restricted to non-test files finds
    only six — `export_integration_test.go:39` is the seventh, and a Task 4 agent must not read
    the discrepancy as "one site already cleaned up".
  - Task 4 authors a migration, so the atlas.sum fix must land BEFORE it is dispatched.

>>> TASK 2 REOPENED — regression found by Task 3's integration sweep <<<
Task 2: commit 6ff1aa9 landed with a RED integration test.
  `TestCreateTaskValidation/gate_rejected_in_plan_05` (runs/tasks_integration_test.go:168)
  asserts the intake refusal Step 7 deleted; `err = <nil>, want invalid/not-found`.
  Controller verified independently: `grep -rn HasUnsupportedNodes backend/` returns nothing,
  and a whole-plan search finds NO later task owning that case — it is an orphan, not something
  Task 12 was going to sweep up. Task 3's work is nowhere near intake validation.
  MISSED BY: the controller's own verification (`go test ./... -race` does not build
  `//go:build integration`), the Task 2 implementer's list, AND the Task 2 review.
  RULING: belongs to Task 2, not Task 3. Task 3 commits first (never two implementers in one
  worktree); then resume impl-task-2 for the fix as its own commit.
  FIX SCOPE: delete the stale case AND add a positive test that a gated definition is now
  ACCEPTED at intake — Task 2 removed a behaviour and left nothing asserting the replacement,
  which is the coverage hole the review would have flagged had it seen this lane.

Task 2: SECOND regression, same commit — `internal/runs/compile_graph_test.go` is not
  gofumpt-clean, which reds `task backend:fmt:check` AND `task backend:lint`. The implementer
  reported running `gofmt -w`; this repo gates on the stricter `gofumpt` (Taskfile:35-39).
  Controller reproduced: `bin/gofumpt -l .` from backend/ prints exactly that one file.
Task 2: fix round 1/5 (2 addressed, 0 open — gofumpt applied; the stale case replaced by a
  POSITIVE test that intake now accepts a gated definition, so the removed behaviour's
  replacement is asserted rather than merely un-asserted; commits 135f56a, 19bd487).
  No scoped re-review was dispatched: the account hit its session limit. Instead the controller
  verified the whole branch directly at 19bd487 — `task backend:ci` green in EVERY lane
  (fmt:check, vet, lint, analyze, unit, property, integration, vuln, build),
  `task contracts:check` green, `bin/gofumpt -l .` empty. That is stronger evidence than a
  scoped re-review of a two-file fix would have produced, but it is NOT a review of the new
  test's quality — hand the final whole-branch review a pointer to 19bd487 for that.
Task 2: complete (commits affb16c..6ff1aa9 + fixes 135f56a, 19bd487; 7 minors deferred)

>>> STANDING RULE, supersedes the per-lane lists above <<<
Every implementer from here runs `task backend:ci` — NOT `go test ./... -race`, and not a
hand-enumerated list of lanes. backend:ci covers fmt:check, vet, lint, analyze, unit, property,
integration, vuln and build in one command, and it has no secrets lane so it runs fine from
this worktree. (`task ci` still cannot — it adds secrets/lint:actions.) Enumerating lanes has
now failed three times on one commit: Task 1 missed the property lane, Task 2 missed the
integration lane AND the gofumpt/lint lanes. The controller's own independent verification
runs `task backend:ci` too. `task contracts:check` remains a separate must — backend:ci does
NOT include it.
Any change to a struct embedded in an api/dto.go DTO silently drifts the generated contracts.

NOTE (not this plan's problem, do not act mid-plan): a `caesar-07-harness-drivers`
worktree still exists on disk; the project memory claims all prior worktrees were removed.
Worth reconciling after this plan lands.

## Session 44 (2026-08-08)

BLOCKER A CLEARED: the user ran `atlas migrate hash`; controller confirmed
`atlas migrate validate` now prints nothing and exits 0, and committed the corrected
checksum as `7099422` ("chore(migrations): rehash atlas.sum ..."). Task 3's range for
review purposes is still 6ff1aa9..edcfd94 — 7099422 touches only two checksum lines.

BLOCKER B: Task 3 review dispatched (opus, `review-task-3`), writing to task-3-review.md.
Diff = the existing review-6ff1aa9..edcfd94.diff. The dispatch disclosed all five
deviations, named D3/D4 as the two to verify (D3 against the GENERATED code), carried the
three controller-resolved brief gaps so they are not misread as implementer inventions,
and forbade re-running the suite.

Task 4 brief regenerated (335 lines). Controller read it against source BEFORE dispatch;
FIVE conflicts found, all verified, all to be handed to the implementer as resolutions:
  (1) EVERY test helper Step 1 calls is fictional — `newSpineHarness`, `conveyorWorkflow`,
      `h.createTask`, `h.createTaskWithSnapshot`, `h.waitForRunStatus`, `h.phases`,
      `.byNode`, `.tryByNode`, `h.countEvents`, `h.backdateWallDeadline`, `h.run`,
      `h.pollInterval`. `grep -rnE "func .*(newSpineHarness|conveyorWorkflow|...)"` over
      internal/runs/ returns NOTHING (the only `createTask` is api/api_integration_test.go:254,
      a different package). The real bootstrap is `newFixture` (testutil_integration_test.go:27,
      type `fixture` :19, `newRunRow` :80) plus `startOrchestrator`
      (orchestrator_integration_test.go:31) and `waitForRun` (:42, already takes a
      `runs.RunStatus` so it serves RunWaiting and RunDead). Model the conveyor definition on
      `TestConveyorRunsToCompletion` (:146) and the deadline test on
      `TestWallDeadlineTerminatesTheRun` (:287). Same failure mode as Task 3's brief gap (c).
  (2) `RunIsWaitingOnAGate` returns `pgtype.Bool`, NOT `bool` (queries.sql.go:2186). Step 5's
      `if waiting {` does not compile. Must read `waiting.Bool`.
  (3) `insertArtifacts` ALREADY carries `generation int32` — at position 4, right after `ref`
      (orchestrator.go:900), not after `rawParents` as Step 6's snippet shows. Keep the
      existing order; do not churn the signature. Likewise `insertPhaseTx` (queue.go:86)
      already has the parameter in exactly the shape Step 4 prints — nothing to grow.
  (4) STEP 6's GREP GATE IS BROKEN. `rg 'generation:\s*1'` over internal/runs/ matches ZERO
      lines and would pass vacuously: the seven stub sites pass a bare POSITIONAL `1`, not a
      `Generation: 1` struct field. The real gate is the marker comment —
      `grep -rn "Task 4 threads the run's real generation here" backend/internal/runs/`
      must return ZERO after this task. The seven sites are orchestrator.go:296 (root loop,
      keeps its literal 1 but loses the marker), :834, :878, :1048, :1060, :1090, and
      export_integration_test.go:39 (the seventh, in a test export shim — Task 4 must decide
      it, not skip it as already-clean).
  (5) The brief contradicts itself on caching: the Interfaces block promises
      `cachedPlan.generation int32` "refreshed on every load", while Step 3 deliberately
      does NOT add the field and reads through `generationOf`. Step 3 governs — `cachedPlan`
      (orchestrator.go:61-66) stays a five-field struct.
Verified present and correct as the brief assumes: `ExecPlan.PublishFor` (compile.go:107),
`store.InsertGatePhaseParams` {RunID,NodeID,Attempt,MaxAttempts,Generation,CreatedAt},
`RunIsWaitingOnAGateParams` {RunID,ExcludePhase}, `CASRunStatusParams` {ID,ToStatus,FromStatus},
`store.Run.Generation`, and the Task 1 vocabulary (`RunWaiting`, `PhaseWaitingDecision`,
`EventGateWaiting`, `CodeWallDeadlineExceeded`, `PhaseSuperseded`).

BLOCKER B CLEARED. Task 3 review (`task-3-review.md`, 17.9KB): **Spec ✅ compliant, Task
quality Approved.** 0 Critical, 1 Important (plan-mandated), 5 Minor, 4 cannot-verify.
NOTE the reviewer wrote the file but its chat verdict did not reach the controller on the
first check — an `ls` raced the Write. Re-prompt before concluding a report is missing.

Both flagged deviations verified SOUND against source, not the report:
- D3: `queries.sql.go:2256-2261` — `SupersedePhasesInSliceParams{Outcome, FinishedAt,
  RunID, NodeIds}`, exactly the four fields claimed. >>> CARRY INTO TASKS 5 AND 6: the
  field ORDER changed (Outcome/FinishedAt now LEAD — sqlc numbers named args by first
  appearance in the SET clause). Keyed literals are unaffected; a POSITIONAL composite
  literal would compile wrong-side-up. Warn both briefs. <<<
- D4: both inserts share authID and repoID, but branch is node-derived so the marker key
  genuinely cannot fire, and (run_id,node_id) differs too — the only remaining unique key
  is UNIQUE(authorization_id) = `publish_intents_authorization_id_key`, the exact string
  asserted, and that constraint IS the single-use guarantee. The test fails for the reason
  its name claims.
Also confirmed independently by the reviewer: all 23 sqlc methods exist verbatim (no
`DecideGate`); both param-struct call sites (queue.go:89, orchestrator.go:913) updated with
none missed on a repo-wide grep; the down-direction hand-edit is correct for the right
reason (DROP COLUMN/DROP CONSTRAINT share AT_PASS_DROP and run in written order, while the
up direction's inverted-looking ADD pair is safe because AT_PASS_ADD_COL precedes
AT_PASS_ADD_CONSTR) and Down is a genuine inverse for all three tables.

Task 3: PARKED (the one Important) — hardcoded generation 1 at seven sites. It is
  plan-mandated and the user ALREADY ruled on exactly this at pre-flight (ruling C4:
  "ACCEPT + add a grep gate to Task 4 Step 6", approved 2026-08-07). Not re-asked; that
  would re-litigate a settled decision. RULING: stands as accepted scaffolding, BUT the
  reviewer's sharpening is adopted — the failure mode is silent (1 is a legal generation,
  so a missed site stamps generation-2 rows as 1 and `GetNodeArtifact`'s provenance-first
  ORDER BY then returns the STALE artifact, reintroducing from the other end exactly what
  that ordering exists to prevent), and the mitigation the user approved is a gate that
  DOES NOT WORK (see conflict 4 above — it matches zero lines). Controller decision, logged
  as a deviation from plan text: Task 4 gets (a) the corrected marker-comment grep and
  (b) a BEHAVIOURAL assertion — bump `runs.generation` to 2 by raw SQL mid-run, then assert
  the next materialized phase row carries 2 — which is available in Task 4 without waiting
  for Task 6's rematerialization.
Task 3: minor (deferred): `GetNodeArtifact`'s provenance-first ORDER BY has no test —
  reverting it or writing `generation ASC` breaks nothing.
Task 3: minor (deferred): `TestApprovedPatches` asserts only SQLSTATE 23505, not the
  constraint name — same lens as D4 one test to the left; pin `approved_patches_live_key`.
Task 3: minor (deferred): `artifacts_run_id_idx` is redundant with
  `artifacts_node_lookup_idx`'s leading run_id — pure write amplification; batch into Plan 15.
Task 3: minor (deferred): the 346-line `schema_integration_test.go` holds the package's only
  test bootstrap under a "schema" name. >>> SPLIT into `testutil_integration_test.go`
  BEFORE Tasks 5/6 add to it. <<<
Task 3: minor (deferred): no index carries `phase_runs.status` for `ListWaitingGates`, and
  `publish_intents` has none for Task 10's settlement sweep. Fine at V1 scale — noted so
  Task 10 does not assume one exists.
Task 3: cannot-verify (controller resolved): nothing in the repo asserts the migrated schema
  equals `schema.sql` — `migrate_updown_integration_test.go` only walks down-then-up and
  checks two table names. No drift HERE (the up direction is Atlas-generated and the
  hand-edit touched only Down, which the reviewer read line by line), but the gap is real and
  PRE-EXISTING. Not a Task 3 fix. >>> PLAN 15 ITEM. <<<
Task 3: complete (commits 6ff1aa9..edcfd94 + atlas.sum follow-up 7099422; review clean,
  1 parked, 5 minors deferred)

>>> TASK 3 GOT TWO INDEPENDENT REVIEWS, AND THEY DISAGREE. TASK 3 IS NOT CLOSED. <<<
Controller error, recorded so it is not repeated: re-prompting the fresh reviewer, I sent the
first message to the name `review-task-3` — which resolved to the PREVIOUS session's reviewer,
the one that died on a session limit. The harness had auto-renamed this session's reviewer
`review-task-3-2`. That woke the stale agent, which turned out to have completed its own full
review, and it wrote that review over `task-3-review.md` at 11:03 — after the fresh reviewer
had verified its own file there at 11:01. LESSON: check the name in the spawn result before
addressing a subagent; a name from a previous session can still resolve.
  FILES, disambiguated — there is deliberately NO `task-3-review.md` any more, because a
  canonical-looking filename holding the clobbered copy is a trap for a later session:
  - `task-3-review-stale.md` = the STALE reviewer's review (was `task-3-review.md`).
    Substantive and thorough; numbers deviations D1-D6 off the implementer's own list.
    NOT a partial. Its duplicate `task-3-review-stale-partial.md` was deleted.
  - `task-3-review-fresh.md` = this session's reviewer, re-persisted after the clobber.
    Verdict line 370: **Task quality: Needs fixes.**
Both say Spec ✅ compliant / Task quality Approved. They disagree on ONE severity:

  `GetNodeArtifact`'s provenance-first ORDER BY has NO test. Both reviewers found it.
  Fresh reviewer: Minor. Stale reviewer: IMPORTANT — "the only behavior change here that
  fails silently and produces a wrong forge effect", i.e. a later edit reverting the ordering
  (or writing `generation ASC`) silently serves the STALE artifact, the gate approves it and
  the publisher publishes it, with nothing in the suite objecting. It proposed the concrete
  test: two `patch` artifacts on one node, generation 1 with a LATER created_at and
  generation 2 with an EARLIER one, assert `GetNodeArtifact` returns generation 2 — fails
  against the old `ORDER BY attempt DESC, created_at DESC`, passes against the new one.
  CONTROLLER RULING: treat as Important. It is not plan-mandated (the brief simply never
  asked for the test), so it enters the fix loop as Task 3 fix round 1. It is the same
  silent-failure class as the generation-1 scaffolding, two reviewers independently found it,
  and the fix is ~15 lines in an existing file. Severity question put back to the fresh
  reviewer for its reasoning; the ruling stands unless it argues otherwise.
  NOT DISPATCHED YET, DELIBERATELY: `impl-task-4` is running and executing `task backend:ci`.
  Two agents running testcontainers concurrently interfere. Dispatch the Task 3 fix round the
  moment Task 4's implementer reports, before Task 4's review.
  Scope: `internal/runs/store/schema_integration_test.go` only — a different package from
  Task 4's files, so there is no file conflict, only a test-runtime one.

ADJUDICATED — BOTH REVIEWERS NOW AGREE, AND THE FRESH ONE REVISED ITS OWN VERDICT TO
"Needs fixes". Task 3's review verdict is therefore NOT clean; it has one blocking finding.
The fresh reviewer's reasoning for the revision, which is stronger than the controller's:
  1. The rubric's Important bar covers FRAGILE behaviour, not only incorrect behaviour. The
     ORDER BY is correct today (verified column-for-column against `artifacts_node_lookup_idx`);
     what makes it Important is that nothing HOLDS it there. Correct-but-unpinned + silent when
     broken + externally irreversible is the definition of fragile.
  2. >>> The plan set this standard itself and applied it to the SAFER sibling. <<<
     `ListSucceededNodes` is the other behaviour-change query in Task 3 and the brief MANDATED
     a test for it (Step 9); its silent failure mode is a node re-running. `GetNodeArtifact`'s
     is publishing stale content to a real forge. The more dangerous one shipped untested.
     That is an inconsistency IN THE BRIEF, not a coverage preference.
  3. The fresh reviewer's own Important #1 already argued the higher severity: a missed
     generation-1 literal matters *because* it "reintroduces from the other end exactly what
     the provenance-first ordering exists to prevent". It had rated the downstream half
     Important and the upstream half Minor; only one can be right.
  The counter-argument it constructed and then dismissed: the test must build generation-2 rows
  by hand because no production code can produce them yet, so it pins an ordering over an
  unreachable state. It dissolves — all four existing tests in that file already do exactly
  that (a waiting_decision gate row, two gate decisions, a valid approved_patches row; none
  reachable from production code at this commit). Synthetic-state raw-SQL tests are the file's
  established and correct pattern.
  IMPLEMENTATION NOTE for the fix round: `mustInsertArtifact` needs a generation/created_at
  parameter, or a sibling helper, before the test can be written.
  The fresh reviewer explicitly did NOT read the stale reviewer's file, so it judged that
  argument only as relayed. Its four other Minors stand unchanged.

The reviewers' OTHER disagreement, already resolved and needing no action: the seven
`generation: 1` literals. Fresh rated Important, stale rated Minor 5. Both agree it is NOT
fixable inside Task 3 and belongs to Task 4 — which is exactly how it was dispatched (corrected
grep gate + behavioural assertion). Both independently confirm the count is SEVEN, the seventh
being `export_integration_test.go:39`.

The stale reviewer additionally raised, beyond the fresh reviewer's list (all Minor, deferred):
  - `CountSettledPublications` counts EVERY intent including `pending` — the name says
    "settled" and is deliberately wrong; plan-mandated and Task 6 calls it. Note it in the plan.
  - `insertPhaseTx` now takes four adjacent positional `int32`s
    (attempt, maxAttempts, chargeableFailures, generation). Any transposition compiles and is
    caught only by a runtime CHECK or a wrong retry budget. >>> Suggested for TASK 4: consider
    a small params struct when the literals come out. <<<
  - sqlc absorbed a free-floating group comment into `RecordPushReceipt`'s doc comment and
    left a stray blank line inside the generated const. Cosmetic; generated output should be clean.
  - The DOWN migration re-adds the NARROWED CHECKs without clearing offending rows, so a
    rollback over a database holding any `waiting_decision` phase or `publication`/`gate_note`
    artifact fails 23514 mid-ALTER. Invisible to the empty-database round-trip test.
  It also confirmed the UP direction's inverted-looking ADD pair is safe (AT_PASS_ADD_COL
  precedes AT_PASS_ADD_CONSTR) and that the UP direction is byte-for-byte Atlas output.

Task 4: BASE = 7099422. Implementer `impl-task-4` dispatched on opus, report file
  `task-4-report.md`. The dispatch carried all five brief conflicts above as overrides, the
  full-gate verification rule, and the added behavioural generation assertion (raw
  `UPDATE runs SET generation = 2` mid-run, then assert the next phase AND artifact rows
  carry 2) — the corrected grep gate alone cannot catch a silent stamp.
Task 4: implementer reported via report file only (idle notification, no status message —
  same harness behaviour as before). Commit `539b7bc`, tree clean.
  VERIFICATION CLAIMED: `task backend:ci` exit 0 on ALL NINE lanes (fmt:check, vet, lint,
  analyze, unit, property, integration incl. `internal/runs` 18.0s, vuln, build), grep of the
  captured log for `fail|warn|panic|data race` returns zero lines; `task contracts:check`
  exit 0 with db:sqlc/openapi/ts all byte-identical. (`npm ci` prints two high-severity
  advisories from `web/` devDependencies — pre-existing noise, not this change.)
  CONTROLLER MUST STILL RE-RUN BOTH ITSELF before accepting DONE.
  Eight deviations declared; five are the controller's own brief overrides. The three that
  are the implementer's judgment, all disclosed to the reviewer:
  - The SEVENTH generation site (`export_integration_test.go:39`) KEPT its literal `1` rather
    than growing a parameter. Argument: its sole caller (`queue_integration_test.go:134`)
    stages a successor row to prove the fencing CAS is row-identity-scoped, on a run that
    never revises; no downstream provenance read touches those rows, so a parameter no caller
    would vary is YAGNI. The scaffolding comment was replaced with one saying why 1 is right
    there. Reviewer asked to judge the call.
  - The behavioural generation test was BROADENED from the controller's "next phase row +
    next artifact row" to a three-case table also covering the retry and dead write paths.
  - `materializeNode`'s payload map is hoisted above the gate/non-gate branch instead of built
    twice as the brief's snippet does.
  Self-review caught and fixed before commit, worth noting because it is exactly the vacuous-
  pass class this plan keeps hitting: the budget-does-not-grow assertion could have passed on
  `0 == 0`, so a positive control asserting the three agentic phases accounted usage at all
  was added.
  Implementer's own concerns: `orchestrator.go` 1127 -> 1221 lines and `evaluateSuccess` now
  ~90 lines doing five things (Task 5's `materializeReady` is meant to factor it back out —
  not pre-empted here because that helper's janitor caller does not exist yet); one extra
  unlocked `SELECT * FROM runs` per evaluation from `generationOf`; the `phase.materialized`
  event payload gained a `generation` key that Plan 05's docs table (plan 05 line 92) does not
  list >>> TASK 15 DOCS ITEM <<<; and
  `TestOrchestrator_MaterializedRowsCarryTheRunsCurrentGeneration` fakes with a
  `generationBumper` what Task 6 makes real >>> REVISIT AT TASK 6 to drive a real revision. <<<

Task 4: review dispatched (`review-task-4`, opus) over 7099422..539b7bc, report file
  `task-4-review.md`. Told NOT to run tests (fix-task-3 is running integration tests
  concurrently), given all five brief overrides so it does not misread them as implementer
  errors, pointed at the two specific vacuous-pass risks, and handed the calibration rule this
  plan learned the hard way: correct-but-unpinned + silent + externally visible is Important.

Task 3: fix round 1 dispatched (`fix-task-3`, opus, fresh implementer — Task 3's original is
  from a dead session). Scope is the ONE blocking finding: the `GetNodeArtifact` ordering test.
  Told to prove it fails by temporarily reverting the ORDER BY to `attempt DESC, created_at
  DESC` + `task db:sqlc`, then restore — a regression test for already-correct behaviour that
  cannot be made to fail is not a test. Scope-fenced to `internal/runs/store/` only, since
  `review-task-4` is reading `internal/runs` at the same time. The anticipated
  "it pins an unreachable state" objection was pre-dismissed in the dispatch.
Task 3: fix round 1 DONE, commit `1aff70a` (one file, +43/-6, nothing outside
  `internal/runs/store/`). Real RED/GREEN, not a claim: the ORDER BY was reverted to
  `attempt DESC, created_at DESC`, `task db:sqlc` re-run (confirmed reaching
  queries.sql.go:635), the new test FAILED on the generation assertion — "GetNodeArtifact
  returned the generation-1 patch: ordering on time hands the gate stale content to
  approve" — then the ordering was restored bit-for-bit and it PASSED with the file's other
  four tests. `queries.sql`/`queries.sql.go` are NOT in the commit.
  The sharp part, and the reason this test is worth having: BOTH rows carry the SAME attempt,
  deliberately. Give the newer generation a higher attempt and `attempt DESC` alone decides
  the result — the test would pass against the OLD ordering and the leading `generation DESC`
  term, the one term that stays right when the clocks disagree, would go untested.
  Deviation: took the sibling-helper option (`mustInsertArtifactAt`, with the old
  `mustInsertArtifact` delegating at generation 1) rather than widening the existing helper,
  so its two call sites stay untouched. `mustInsertArtifact` now stamps `created_at` from the
  Go clock instead of the database's `now()`; no existing test reads an artifact's
  `created_at`, and the file's other seeds already stamp from `time.Now().UTC()`.
  Claimed full gate: `task backend:ci` exit 0 on all nine lanes, `task contracts:check` exit 0.

>>> NEW STANDING RULE, learned the hard way this round <<<
`task contracts:check` MUST be run against a CLEAN TREE. Its final step is
`git diff --exit-code -- ... backend/internal/runs/store`, so ANY uncommitted change under a
watched path fails the gate and presents the working diff as if it were sqlc regen drift.
The fix implementer lost a cycle to this: it failed exit 1 with no drift, and passed once
committed. Commit first, then run contracts:check.

Task 3: fix round 1 re-review VERDICT — **all findings addressed, no new breakage.**
  (`task-3-rereview.md`.) It answered the load-bearing question from the COMMITTED TEST TEXT,
  not the implementer's RED claim: the `(attempt, generation)` args at
  `schema_integration_test.go:374-375` are `(1,1)` and `(1,2)` — both rows attempt=1 — so under
  the old `ORDER BY attempt DESC, created_at DESC` the attempt term TIES and the generation-1
  row, stamped an hour LATER, wins and trips the `t.Fatal` at :384. Only the leading
  `generation DESC` term makes it pass. The inverted timestamps are caller-supplied rather than
  DB `now()`, so there is no flake window.
  No new breakage: the `mustInsertArtifact` -> `mustInsertArtifactAt` refactor is
  behaviour-identical at both call sites (:198, :211) — explicit generation=1 matches the
  column's `DEFAULT 1`, and nothing in the file reads an artifact's `created_at`.
  Scope clean: one file under `internal/runs/store/`; no `queries.sql`/`queries.sql.go` rode
  along, confirming the RED-run revert really was restored and left uncommitted.
Task 3: minor (deferred): `mustInsertArtifact` now stamps from the Go clock while other seed
  helpers use DB `now()`. Only bites if a future test asserts cross-table timestamp order.
Task 3: fix round 1/5 (1 addressed, 0 open — the `GetNodeArtifact` provenance-ordering test;
  commit 1aff70a)
Task 3: COMPLETE (commits 6ff1aa9..edcfd94, atlas.sum follow-up 7099422, fix 1aff70a;
  two independent reviews, both Approved, all blocking findings closed; 7 minors deferred)

Task 3: fix round 1 scoped re-review dispatched (`rereview-task-3`, fable) over
  539b7bc..1aff70a, report file `task-3-rereview.md`. Its load-bearing question is stated
  explicitly: the RED evidence is a claim about a state not in the diff, so judge from the
  COMMITTED TEST TEXT whether it would really fail against the old ordering — above all
  whether both rows share an attempt, since a higher attempt on the newer generation would
  let `attempt DESC` decide and the test would pass against the old ordering too.
CONTROLLER INDEPENDENT VERIFICATION AT `1aff70a` (covers 539b7bc too): **BOTH GREEN.**
  `task backend:ci` exit 0 — all nine lanes actually invoked (fmt:check, vet, lint, analyze
  incl. the caesar-analyze vettool, unit, property with -rapid.seed=1, integration, vuln,
  build). `task contracts:check` exit 0. The log has ZERO occurrences of FAIL/panic/DATA RACE
  and 54 `ok` package lines. Both implementers' claims are confirmed, not taken on report.

Task 4: review (`task-4-review.md`) — **Spec ✅ compliant, Task quality Approved.**
  0 Critical, 1 Important (plan-mandated), 8 Minor. All five brief overrides verified carried
  out correctly, each against source rather than the report:
  - `waiting.Bool` is safe: `queries.sql:471-479` is `SELECT EXISTS(...) AND NOT EXISTS(...)`,
    neither operand nullable, so `.Valid` is always true. (Named risk checked: a NULL reading
    as "not waiting" would strand the gate forever.)
  - `gatedConveyorDoc` (`tasks_integration_test.go:85-97`) really is agentic + gate with
    Revision{impl,2} + publish, so the payload assertions test COMPILED values, not constants
    echoed back.
  - The corrected grep gate was independently re-run by the reviewer; the brief's version would
    indeed have passed vacuously.
  All three implementer judgment calls ENDORSED: the seventh site keeping its literal is "the
  right call, not a dodge" (its replacement comment also points a future caller at the
  orchestrator's path rather than leaving a trap); the broadened generation table pins 4 of the
  6 sites where the controller's minimum would have pinned 2, including both `evaluateFailure`
  paths; the hoisted payload map is equivalent (the two branches return different `Kind`s, no
  aliasing). File growth 1127->1221 judged acceptable and explicitly "noted so it is not
  re-litigated" — ~38 lines are `materializeNode`, a net de-duplication, and ~16 are the
  decision-20 comment the plan wants at that statement.

>>> USER DECISION 2026-08-08: the Important finding is FIXED NOW, in Task 4. <<<
It was plan-mandated (the brief's Step 3 prescribes `q.GetRun`), so it went to the user with
three routings; they chose fix-now over defer-to-Task-6 or park.
THE FINDING: `generationOf` (`orchestrator.go:76-82`, called `:873` and `:1131`) reads the run
row with `GetRun` — `SELECT * FROM runs WHERE id = $1`, NO `FOR UPDATE` (`queries.sql:37-38`).
Under READ COMMITTED that is the statement-start snapshot, so it does NOT deliver what its own
doc comment claims ("every write that stamps a generation must see the value its own
transaction will commit against").
  - `evaluateSuccess`: read at `:873`; the first statement locking the run row is `AddRunUsage`
    at `:899`. `insertArtifacts` (`:887`) and the successor loop (`:937`) stamp a value read
    before ANY lock was held.
  - `evaluateFailure`: `GetRunForUpdate` (`:1097`) is CONDITIONAL — `if retry && out.Code !=
    CodeCredentialBusy` (`:1087`). On the dead and credential_busy paths nothing locks the run
    before `insertArtifacts` (`:1185`) stamps it. The implementer's report said the lock
    "still comes first", which is true where it happens but reads as if it always happens.
  CONSEQUENCE: a revision commits between read and writes, rows are stamped generation N-1 on a
  generation-N run, and `GetNodeArtifact`'s provenance-first ordering serves the STALE artifact
  to the publish node. Silent, externally visible on a real forge — the same shape as Task 3's
  fix. NOT REACHABLE TODAY (nothing bumps `runs.generation` until Task 6); fixed anyway because
  it sits inside the one function whose entire purpose is getting the generation right, and
  Task 6 would inherit it silently once the stamping sites are written and passing.
Task 4: fix round 1 dispatched by RESUMING `impl-task-4` (rounds 1-3 resume the original).
  Fix: hoist `GetRunForUpdate` out of the `if retry` block in `evaluateFailure` and reuse
  `runRow.Generation` (this also deletes the redundant second read, reviewer's Minor 2, so the
  function should get SHORTER); in `evaluateSuccess` use a locked read — moving the read below
  `AddRunUsage` is NOT sufficient because `insertArtifacts` at `:887` needs it first;
  extending `AddRunUsage`'s RETURNING with `generation` is an accepted alternative. The doc
  comment must end up true of the code. Required to reason explicitly about the lock-ordering
  consequence — the dead and credential_busy paths now take a run-row lock they did not take
  before — rather than assume it is safe.

Task 4: minors (deferred, hand to the final whole-branch review):
  1. `startPendingRuns` has no read-back and its root loop can now insert a gate
     (`orchestrator.go:353`). UNREACHABLE and the reviewer proved why: `validate.go:208-211`
     requires a gate to consume exactly one plan or patch, so a gate always has >=1 input,
     while `Roots()` (`compile.go:293-302`) selects only `len(n.Inputs) == 0`. Worth one
     sentence at the call site so nobody re-derives it; adding the read-back there would be
     dead code.
  2. Redundant run read on the retry path — subsumed by the Important fix above.
  3. `phase.materialized` payload gained a `generation` key that Plan 05 documents as
     `{node_id, attempt}`. >>> TASK 15 DOCS. <<<
  4. The GATE row's generation is never asserted — the generation table uses gateless
     `agenticDoc()` and the gate tests never read the column. >>> MATTERS AT TASK 6, which
     rematerializes gates at generation N with nothing pinning that column from the writing
     end. <<<
  5. `max_rounds` is populated in the gate payload but unasserted (the test checks
     `authorizes` and `revision_target`). One line closes it.
  6. `artifactGeneration` (test helper) uses `LIMIT 1` with no `ORDER BY` — harmless today
     since every artifact of an attempt shares a generation, but nondeterministic by
     construction, so the failure message is not trustworthy.
  7. The `ExcludePhase` comment at `orchestrator.go:958-960` overstates its role: it cites
     `phaseRows`, which is a different variable (`:925`), and the phase's own row was already
     set `succeeded` in this tx by `finishPhase` (`:864`), so it could not be counted live
     regardless. The parameter earns its keep for the janitor's `uuid.Nil` caller, not here.
  8. File size / `evaluateSuccess` length — explicitly no change requested, judgement recorded
     so it is not re-litigated.

Task 4: fix round 1 DONE, commit `1ec46bf` ("fix(runs): read the run's generation under a row
  lock so the stamp survives a concurrent revision"), one file, +27/-16.
  WHAT LANDED: `generationOf` now calls `GetRunForUpdate`, and its doc comment was rewritten to
  assert only what the query provides. `evaluateFailure` hoists the locked read out of the
  conditional — ONE unconditional read now serves both the generation and the budget pre-check
  (which reuses the same `runRow`); the old second read at `:1131` is gone. The budget
  pre-check's CONDITION is untouched — credential_busy is still exempt from the breach check;
  only the read moved out from under it. `evaluateSuccess` needed no edit of its own.
  OPTION CHOSEN for `evaluateSuccess`: `GetRunForUpdate` inside `generationOf`, REJECTING the
  `AddRunUsage` RETURNING alternative because (a) it edits `queries.sql`, regenerating a path
  `contracts:check` watches and changing a statement other paths share; (b) `insertArtifacts`
  needs the generation before `AddRunUsage` runs, so it would force moving `AddRunUsage` above
  the artifact inserts — changing when usage is accounted, inside a function the review had
  just approved, for no benefit; (c) it keeps the reasoning attached to the function Tasks 5/6
  will reach for. Acknowledged cost: `generationOf` is now a thin one-caller wrapper.
  LOCK ORDERING, the substance of the round — implementer's table, to be checked by the
  re-review, not accepted here: only ONE path's order changed. `evaluateFailure`'s dead and
  credential_busy paths go from `phase -> run` to `run -> phase`, which the implementer argues
  REMOVES an inversion rather than adding one, since `run -> phase` is what the retry path and
  `terminalizeRun` already use — the dead path was previously inverted against `terminalizeRun`
  and now agrees with it. `evaluateSuccess` keeps `phase -> run` (the run lock just moves 14
  lines earlier, still after `finishPhase`), so the one surviving inversion against
  `terminalizeRun` is pre-existing and already documented there: Postgres detects it, kills one
  side, both recover (sweeper next tick / lease expiry -> reconcile). Two concurrent
  evaluations cannot cycle on phase rows because `finishPhase` names one row id — its own
  claimed attempt — so their only shared resource is the run row, where the loser blocks.
  Claimed bonus: the review's noted serialisation property (two concurrent `evaluateSuccess`
  txs serialise on the run row before either reaches the `RunIsWaitingOnAGate` read-back) now
  holds a fortiori and no longer depends on `AddRunUsage` staying unconditional, which the
  review had flagged as a fragile dependency.
  HONEST COST, self-flagged rather than buried: both changed paths hold the run row for a wider
  window, and a FENCED failure evaluation now takes the run lock before discovering it lost the
  CAS. This makes the pre-existing `evaluateSuccess`/`terminalizeRun` deadlock marginally more
  likely to be HIT; it makes nothing newly possible, and both sides already recover.
  NO TEST CHANGED THIS ROUND, disclosed plainly: a test distinguishing a locked from an
  unlocked read would have to interleave a real `UPDATE runs SET generation` from another
  transaction against a precise statement boundary mid-evaluation, which is Task 6's territory
  where a genuine revision exists to race against. Verified by reasoning plus existing
  regression coverage. The re-review was asked to accept or reject that explicitly and to
  propose a concrete test if it rejects.
CONTROLLER INDEPENDENT VERIFICATION AT `1ec46bf`: **BOTH GREEN.** `task backend:ci` exit 0,
  `task contracts:check` exit 0, zero occurrences of FAIL/panic/DATA RACE/deadlock in the log,
  54 `ok` package lines.
Task 4: fix round 1 scoped re-review dispatched (`rereview-task-4`, opus) over 1aff70a..1ec46bf
  — deliberately NOT 539b7bc..1ec46bf, which would have dragged Task 3's fix into the scope.
  Report file `task-4-rereview.md`. Told a wrong lock-ordering claim is Critical.

Task 4: fix round 1 re-review VERDICT — **all findings addressed, no new breakage.**
  (`task-4-rereview.md`.) Every path that stamps a generation now takes the run row's
  `FOR UPDATE` before its first stamp; the doc comment asserts only what the query delivers.
  Verified unchanged by the hoist: the budget pre-check's condition is byte-identical
  (`credential_busy` still exempt), `budgetBreached`'s zero value and first use are the same,
  `runRow`'s widened scope shadows nothing and no later read gets a stale copy, and
  `ExecutionUsedMs`/`ExecutionBudgetMs` are still read before any `AddRunUsage`. Only cosmetic
  loss: the dead/credential_busy paths now return the raw pgx error instead of one wrapped
  `"runs: read generation: %w"`.
  >>> ONE IMPLEMENTER CLAIM REFUTED — the code is right, the justification was not. <<<
  Claim 6 ("two concurrent evaluations cannot cycle on phase rows; `finishPhase` names one row
  id, so their only shared resource is the run row") is FALSE: `finishRunTx` calls
  `SupersedeLivePhases` (`:1050`), which is `UPDATE phase_runs ... WHERE run_id = $1 AND status
  IN (...)` (`queries.sql:168-171`) — EVERY live phase row of the run, not one id. A terminal
  evaluation therefore does take other evaluations' phase rows, and a fan-out cycle exists
  (Tx1 evaluateSuccess holds A waits R; Tx2 evaluateFailure-dead holds R takes B then wants A).
  It is NOT new — the same cycle exists pre-fix with the locks in the other order.
  THE CORRECT ARGUMENT, worth keeping because it is the one that generalises: after the fix the
  set of lock orders in play is a SUBSET of the pre-fix set. `phase -> run` (evaluateSuccess)
  and `run -> phase` (retry, terminalizeRun) both already existed; the dead path merely moved
  from the first class into the second. No order was introduced, so no pair of orders that
  could not already meet can now meet.
  Claim 7 VERIFIED: the `FOR UPDATE` at `:885` sits above both `RunIsWaitingOnAGate :973` and
  `CASRunStatus :985`, so two concurrent `evaluateSuccess` txs still serialise before either
  reads back the waiting state — and that no longer rests on `AddRunUsage` staying unconditional.
  NO-NEW-TEST DECISION: ACCEPTED, but the implementer's REASON was overstated and the
  re-reviewer corrected it. Two tests ARE constructible today (hold-and-observe with a second
  connection holding `SELECT ... FOR UPDATE`; or interleaving via the injected clock, which
  `insertPhaseTx` calls at `queue.go:92` after the generation read). They were rejected on
  merit, not feasibility: both assert WHERE A LOCK SITS via a negative timing assertion on
  statement boundaries that are implementation detail — flaky under `-race` and a brake on any
  legitimate reordering. Neither asserts the property that matters, because with the lock held
  the concurrent bump cannot commit mid-transaction, so the STAMPED VALUE IS IDENTICAL in both
  worlds; only the blocking differs.
  Regression evidence accepted as adequate: every branch the hoist newly locks under has an
  integration test — `TestDelayedRetry_CredentialBusyChargesNothing` (credential_busy retry),
  `TestExhaustionKillsTheRun` / `TestOrchestrator_DeadAttemptArtifactsPersist` /
  `TestBudgetBreachFailsTheRun` (dead), `TestHangingAttemptReconcilesAndExhausts` (concurrent).

>>> THE TEST TASK 6 MUST CARRY — written out so it cannot be lost <<<
  With a run at generation 1 and an attempt in flight, commit a revision that bumps the run to
  generation 2 CONCURRENTLY with the evaluation, then assert (a) no artifact or phase row exists
  whose `generation` is below the run's committed `generation`, and (b) `GetNodeArtifact` for
  the downstream node returns the generation-2 artifact, never the generation-1 one.
  It fails on the pre-fix code and pins the INVARIANT rather than the lock's address. This is
  the honest behavioural test for the fix that landed in `1ec46bf`; Task 4 could not write it
  because no revision machinery exists yet.

Task 4: minor (deferred): `terminalizeRun`'s lock-order comment (`orchestrator.go:545-552`) is
  now the only record of the deadlock analysis and documents only the
  `evaluateSuccess` x `terminalizeRun` inversion. The `evaluateSuccess` x terminal-
  `evaluateFailure` cycle through `SupersedeLivePhases` is pre-existing and undocumented; one
  sentence there would complete it. Fold in opportunistically.
Task 4: minor (deferred): `generationOf` is now a two-line wrapper with ONE caller, kept for
  Tasks 5/6. >>> IF TASKS 5/6 DO NOT CALL IT, inline it at `:885` with its comment. <<<
Task 4: fix round 1/5 (1 addressed, 0 open — the unlocked generation read; commit 1ec46bf)
Task 4: COMPLETE (commits 7099422..539b7bc + fix 1ec46bf; review Approved, re-review clean,
  10 minors deferred)

Task 5: brief GENERATED (`task-5-brief.md`, 1000 lines) and a PRE-FLIGHT dispatched against it
  (`preflight-task-5`, opus) writing `task-5-preflight.md`. This is for the NEXT session — the
  brief is NOT yet dispatched to an implementer. The pre-flight was given the full failure
  pattern from Tasks 3 and 4 (fictional helpers, sqlc-invalid SQL, a test that would pass on
  the wrong constraint, a grep gate matching zero lines, stale signatures, self-contradiction)
  and the `SupersedePhasesInSliceParams` field-order trap, and told to grade BLOCKER /
  CORRECTION / NOTE with a paste-ready resolution for each. READ IT BEFORE DISPATCHING TASK 5.


Task 5: BASE = 1ec46bf. Implementer `impl-task-5` dispatched on opus, report file
  `task-5-report.md`. Session 45 read the ledger + `task-5-preflight.md` before dispatching.
  The dispatch carried, as binding overrides: all 5 BLOCKERS (B1 `waiting.Bool` unwrap at the
  read site; B2 `%v` not `%s` on `xerrors.Class`, which would fail vet and break the whole
  `runs_test` build; B3 write the tests against the REAL `newFixture`/`newService`/
  `startOrchestrator`/`waitForRun`/`principal(f)`/`phaseByNode` scaffolding using the
  pre-flight's mapping table, invent no harness type, build `checkpointWorkflow` as
  `agenticDoc()` + a gate with no publish; B4 the strict-policy half needs its OWN service
  since the snapshot is per-Service not per-task; B5 add `startStoppableOrchestrator` or the
  expired-run test races the sweeper) and all 7 CORRECTIONS (C1 pgconn import; C2 the
  `-run` filter is silently dropped by the task lane; C3 `BaseSha` on the store row only;
  C4 IGNORE the finishRunTx correlation-id drift narrative — it is backwards and following it
  changes behaviour; C5 see ruling below; C6 `impl` not `implement`; C7 drop the
  `authn.GrantApprove` arg, use `principal(f)`, INSERT a real second user where a distinct
  approver is needed).
  CONTROLLER RULING on C5 (the pre-flight offered two options): implement the Step-4 `DBNow`
  predicate as the brief prescribes AND give the expired-run test a discriminator — assert the
  error message names the WALL DEADLINE, not the gate, since only the Step-4 branch produces
  that. Softening the comment alone would leave the Step-4 read removable with the suite green.
  Notes N3/N6 (two doc comments that describe something other than the code beneath them),
  N7 (missing `dropPlan` after `materializeResumedRuns`' terminal commit — add unless wrong),
  N8 (`waitForRun` FATALS on the wrong terminal status, so polling for a transient `running`
  after an approve can lose the race to `succeeded`), N4/N5 (don't call `CountRequestChanges`,
  don't touch service.go/ports.go), N10 (reuse `s.definitionByVersionID`) were passed as
  act-on items; N1/N2 as line-citation corrections.
  Carry-forwards handed over: the `SupersedePhasesInSliceParams` field-order trap (pre-flight
  verified it is unreachable here, passed as insurance), `generationOf` is now a LOCKED read
  after `1ec46bf` and must be called rather than re-adding an unlocked `GetRun`, and Task 4's
  deferred minor that the gate row's `generation` is unasserted from the writing end.
  Also carried: the full-gate rule (`task backend:ci` + `task contracts:check`, the latter on a
  CLEAN TREE), the report-file-is-the-only-channel rule, and the two vacuous-pass failure modes
  this plan keeps hitting.
Task 5: implementer DONE, commit `6f60140` ("feat(runs): decide gates under a CAS with the
  wall-deadline predicate and mint approved patches"), single commit, tree clean. Reported via
  the report file only (idle notification, no status message — same harness behaviour as
  Tasks 3 and 4).
CONTROLLER INDEPENDENT VERIFICATION AT `6f60140`: **BOTH GREEN.** `task backend:ci` exit 0 with
  all nine lanes actually invoked (fmt:check, vet, lint, analyze incl. the caesar-analyze
  vettool, test, property with -rapid.seed=1, integration, vuln, build — the build lane stamped
  `Version=6f60140`, so the gate ran at the right commit); 54 `ok` package lines; ZERO
  occurrences of FAIL/panic/DATA RACE/deadlock. `task contracts:check` exit 0 against the clean
  tree, both final gates (the sqlc/openapi regen-diff and the porcelain check) passing.
Task 5: all five blockers and seven corrections reported carried out; the C5 discriminator is
  claimed to work, with a MUTATION as evidence rather than an assertion — the Step-4 predicate
  was replaced with `if false` and the expired-run test went red on the message
  ("the gate stopped waiting while this decision was being made", want the deadline branch),
  and deleting the `materializeResumedRuns` call from the janitor took two more tests red.
Task 5: implementer's own declared deviations (all disclosed to the reviewer, none are mine):
  (1) N10 DECLINED — `s.definitionByVersionID` returns `(PinnedDefinition, error)` and discards
      the compiled plan, but `DecideGate` needs the `ExecPlan` for `ExaminedInput`/`PublishFor`,
      so routing through it would compile the document twice per decision; `loadPlan`
      (`orchestrator.go:264-271`) already inlines the same pair for the same reason. The note
      said prefer-the-helper-if-the-signature-fits; it does not.
  (2) TWO TESTS BEYOND THE BRIEF: `TestDecideGate_ValidationAndTheRequestChangesStub` (nothing
      in the brief exercised `validateDecision` or the `openRevision` stub — and it carries the
      only assertion in the suite that would catch a Task 6 stub silently returning nil) and
      `TestListWaitingGatesAndGateProject` (`ListWaitingGates`/`waitingGateFrom`/`GateProject`
      are in Produces but untested; `waitingGateFrom` takes ten positional args, four of them
      `uuid.UUID`, which compiles fine while transposing any two).
  (3) EXTRA ASSERTIONS inside the brief's own tests where the brief's version could pass against
      a wrong build (persisted run status vs the returned value, one-authorization-survives on
      the rollback path, `count(*) FROM gate_decisions == 1`, `gate.decided == 2`, and a premise
      guard so the `TreeHash` comparison cannot become `"" == ""`).
  (4) `anyRunnablePhase`'s doc comment rewritten: the brief's claim (that the exclusion is
      load-bearing because `phaseRows` predates the finish) is false in the current code, since
      `ListPhasesForRun` runs after `finishPhase` in the same tx.
Task 5: implementer's self-flagged substance, all handed to the reviewer as named risks:
  `syncWaiting` is now BIDIRECTIONAL in `evaluateSuccess` (a genuine behaviour change, not a
  refactor — it can now CAS `waiting -> running`, argued as the correct repair of a stranded
  run); `materializeResumedRuns` races a worker's `evaluateSuccess` and is argued to serialise
  on `GetRunForUpdate`; and it uses `runRow.Generation` rather than `generationOf` because it
  already holds the locked row.
Task 5: open concerns carried forward — Task 4's gate-generation minor is still open (Task 6);
  `revisionContext`'s fields are write-only until Task 6; `ListRunsAwaitingMaterialization`
  scans `runs` each janitor tick with no index (fine at V1 scale, flagged for later);
  and the publish node "succeeds" through the fake executor, so the approve test pins that the
  node materialized and was claimable, not that anything was published (Task 9/13).
Task 5: review dispatched (`review-task-5`, opus) over 1ec46bf..6f60140, report file
  `task-5-review.md`. Told NOT to run tests (the controller's own `backend:ci` was running
  concurrently), given all twelve overrides so it does not misread them as implementer errors,
  pointed at six named risks (tx ordering, bidirectional `syncWaiting`, the janitor/evaluate
  race and its generation read, the `finishRunTx` extraction's four existing call sites,
  vacuous assertions, the ten-positional-arg `waitingGateFrom`), and handed this plan's
  calibration rule plus the lesson from Tasks 3 and 4: a WRONG ARGUMENT FOR A CORRECT BEHAVIOUR
  is itself a finding, because the next task inherits the argument.

Task 5: review (`task-5-review.md`) — **Spec ✅ compliant, Task quality: NEEDS FIXES.**
  0 Critical, 2 Important, 10 Minor. All twelve controller overrides verified carried out
  correctly, each against source rather than the report (the reviewer tabulated them one by one).
  The independently-verified good parts, worth keeping because they close questions rather than
  restating claims:
  - THE C5 DISCRIMINATOR IS REAL, checked without the report: the up-front branch is the only
    producer of "the run's wall deadline has passed" (`gates.go` diff:172); the only other branch
    reachable from that state emits "the gate stopped waiting while this decision was being made"
    (diff:320). Delete the Step-4 predicate and the test goes red. The brief's original could not
    do that.
  - The `finishRunTx` extraction is behaviour-preserving: exactly two changed lines inside the
    body, and `CorrelationID: "caesar-run-" + runID.String()` appears as a CONTEXT line — i.e.
    untouched. All four pre-existing call sites still route through the shim.
  - The janitor's generation read HONOURS Task 4's carry-forward: `generationOf` is literally
    `GetRunForUpdate` -> `.Generation`, and `materializeResumedRuns` takes its own
    `GetRunForUpdate` and reads the same field from it — the same read under the same lock.
    Calling `generationOf` would only have taken the lock twice.
  - The `attempted` -> `live` narrowing cannot double-materialize a node awaiting retry:
    `evaluateFailure` inserts the next attempt row IN THE SAME TRANSACTION as the failure
    (`orchestrator.go:1319-1321`), so a retryable node never has a window with only terminal rows.
  - Reject ordering holds FOR THE REASON CLAIMED: `SupersedeLivePhases` touches only
    `('pending','running','waiting_decision','reconciling')`, so the already-`failed` gate is out
    of its scope — the test's assertion is correct rather than accidentally correct.
Task 5: THE TWO IMPORTANT FINDINGS (both small, neither disputes the code's behaviour):
  I1. `orchestrator.go` diff:1281-1286 — the comment justifying the new `waiting -> running`
      direction describes a state the guard three lines above makes IMPOSSIBLE. The run row is
      locked and proven `running`; `ListRunsAwaitingMaterialization` also filters
      `r.status = 'running'`. So `syncWaiting(false)` there is always a zero-row CAS. The
      reviewer also could not construct a reachable un-park state at the `evaluateSuccess` site
      (`ClaimPhase` requires `running`; `RunIsWaitingOnAGate`'s NOT EXISTS covers
      pending|running|reconciling) — labelled "could not construct", with the
      `materializeResumedRuns` half proven. The PREMISES of the repair argument are sound; the
      defect is the comment, and the report ESCALATED it, citing the green suite as evidence for
      a branch the suite cannot reach. Third instance on this plan of correct-code-false-
      justification, and Task 6 rewrites this function and would inherit the claim as settled.
  I2. `gates_integration_test.go` diff:1008 — `attempt`/`generation` are adjacent `int32` params
      of the ten-positional `waitingGateFrom`; the test asserts `got.Attempt` and never
      `got.Generation`, and BOTH VALUES ARE 1 in the fixture (attempt 1 at first materialization,
      both generation columns `NOT NULL DEFAULT 1`). Transpose them at either call site and every
      assertion still passes — the one transposition the anti-transposition test cannot see, on a
      field Task 11's approvals inbox renders. Silent + externally visible = Important here.
Task 5: fix round 1 dispatched by RESUMING `impl-task-5` (rounds 1-3 resume the original).
  Scope: I1 (keep the CAS, rewrite the comment AND `syncWaiting`'s doc to say the direction is
  defensive and unreachable behind the guard today, and correct the report claim), I2 (structural
  keyed-params fix or test-only fix at the implementer's choice, but ACCEPTANCE IS A MUTATION —
  transpose the args, show red, revert, show green), and one item of the controller's own:
  the reviewer's Minor 3 is really a PARTIAL MISS OF OVERRIDE C5 — the expired-run test's comment
  still claims the DATABASE clock, but swapping `q.DBNow` for `s.clock.Now()` keeps it green, so
  the comment must be softened to what it proves or given a clock-source discriminator.
  Explicitly told NOT to fix the ten Minors: they go to the final whole-branch review, and
  widening the diff makes the re-review harder.
Task 5: minors (deferred, hand to the final whole-branch review), 10:
  1. Expired-run test pins "an up-front check exists", not "it reads the DB clock" — folded INTO
     fix round 1 as the controller's item 3, so this one should close.
  2. `gates.go` diff:272-277 — the approve path ignores `CASRunStatus`'s row count while
     reporting `res.RunStatus = RunRunning` unconditionally; `finishGate` fences on `n == 0` but
     this CAS does not. Window is narrow (the `EXISTS` proved `waiting` moments earlier in the
     same tx) but a zero-row CAS would make the returned status a lie. Check it or write down why
     zero rows is impossible.
  3. `mintAuthorization`'s two corruption guards are ASYMMETRIC and unexplained: a missing
     `tree_hash` is a hard refusal, a missing `base_sha` passes. Brief-mandated verbatim and Task
     8 re-validates at consumption. Neither refusal branch has a test, though the file has the
     machinery.
  4. `finishGate` and `mintAuthorization` are `*Service` methods that never touch the receiver —
     the same argument C4/Step 6 used to make `finishRunTx` a free function, applied
     inconsistently three functions apart.
  5. `materializeReady` returns two adjacent bare `bool`s; named results or a small struct would
     make `waiting`/`complete` untransposable for TASK 6's caller.
  6. `orchestrator.go` is now ~1406 lines (1221 before, +185 here). The four added functions
     (`materializeReady`, `anyRunnablePhase`, `syncWaiting`, `materializeResumedRuns`) are a
     coherent cluster that wants to be `materialize.go`. >>> WORTH DOING BEFORE TASK 6 GROWS THE
     SAME CLUSTER. <<<
  7. `ListRunsAwaitingMaterialization` has no dampening: a permanently-matching run would cost one
     no-op transaction PER TICK forever, not the "one" its comment claims. Believed unreachable
     today (a dead node terminalizes the run in the same tx).
  8. The test helper `nodeArtifact` DUPLICATES `GetNodeArtifact`'s four-term ORDER BY rather than
     checking it, so the "binds the examined artifact" assertion would not catch an ordering bug
     the two share. Reading the id off the `phase.succeeded` event payload would break the mirror.
  9. `mintAuthorization` stamps `gate.Generation` (the PHASE row's) while `GetLiveGateForUpdate`
     also selects `r.generation AS run_generation`, which the code ignores. Equal in every state
     reachable today and the phase row is locked, so Task 4's carry-forward is satisfied in
     substance — but this SECOND generation-stamping site went unmentioned in the report's
     carry-forward audit. >>> TASK 6 must invalidate live authorizations when a sibling gate opens
     a revision; `approved_patches.generation` is what schema.sql:391's "a newer patch generation
     invalidates the prior one" hangs off. <<<
 10. Coverage: `DecideGate` on an unknown `RunID` (the `notFound` mapping) is untested though
     `GateProject`'s is; `ListWaitingGates` is exercised with a single row only, so ordering and
     multi-row fan-in are unpinned.
Task 5: fix round 1 DONE, commit `0048b4e` ("fix(runs): pin the waiting-gate attempt/generation
  pair and correct the syncWaiting rationale"), two files, comments + tests only
  (`orchestrator.go` comments-only, `gates_integration_test.go`). No watched path touched.
  I1: CAS kept, both comments rewritten. I2: the implementer took the TEST-ONLY option and gave
  a reason worth keeping — a keyed params struct makes a positional transposition inexpressible
  but does NOT make the test catch a VALUE-level swap (`Attempt: r.Generation` still compiles),
  so a mutation would come back green and the gap would not be demonstrably closed; since the
  acceptance was a mutation, only the test fix satisfies it. The fixture now raw-`UPDATE`s both
  `runs.generation` and the gate row's generation to 3 so attempt(1) != generation(3),
  `Generation` is asserted against the pre-existing `phaseGeneration` reader, a guard fatals if
  the two values ever coincide again, and the assertions moved into a closure applied to BOTH
  `waitingGateFrom` call sites (previously only the admin row was inspected).
  MUTATION EVIDENCE: transposed at both call sites -> FAIL "attempt/generation = 3/1, want 1/3"
  on both rows; reverted (`grep -c` = 2) -> ok. With the old fixture that same mutation produced
  `1/1 == 1/1` and stayed green.
  Item 3: comment softened to "refused UP FRONT, before the transaction writes anything", plus a
  paragraph stating what the test does NOT pin. Three earlier report claims corrected in place as
  marked blocks rather than deleted.
CONTROLLER INDEPENDENT VERIFICATION AT `0048b4e`: **BOTH GREEN.** `task backend:ci` exit 0
  (54 `ok` packages, zero FAIL/panic/DATA RACE/deadlock, build lane stamped `Version=0048b4e`);
  `task contracts:check` exit 0 — run even though no watched path changed, because lane-picking
  has failed three times on this plan.
Task 5: fix round 1 scoped re-review (`rereview-task-5`, fable) over 6f60140..0048b4e —
  **ALL THREE FINDINGS ADDRESSED**, and it judged from the COMMITTED TEXT rather than the RED
  claim. It cross-checked the committed files against the diff's `+` side, verified both inbox
  call sites really dispatch to the two different queries, confirmed `r.Attempt, r.Generation`
  appears exactly twice in committed `gates.go` (so the temporary transposition did not ride
  along, and `gates.go` is not in the diff at all), verified all four of Item 3's comment claims
  against source, and verified the three report corrections were ACCURATE rather than merely
  present. It also corroborated the mutation without re-running it: `assertWaitingGate` is a
  `t.Helper()` closure, so its failures attribute to lines 583/589 — exactly the report's FAIL
  line numbers — and `3/1, want 1/3` is what that fixture yields under that mutation. The raw
  `UPDATE ... generation = 3` was checked for collateral: neither inbox query filters on
  generation, `GateProject` never reads it, the run is `waiting` so the janitor (running-only)
  and `ClaimPhase` (running-only) cannot race it, and no other assertion involves generation.
>>> ONE NEW IMPORTANT FROM THE FIX DIFF, comment-only, one line. <<<
  `orchestrator.go:1061` — the rewritten `syncWaiting` doc says the un-park direction is
  "unreachable at both call sites today", upgrading the original review's DELIBERATE hedge
  ("could not construct") into a flat claim, which is the exact modality this round existed to
  fix. The premise (`ClaimPhase` requires `running`) is true but covers the CLAIM INSTANT, not
  the interval between claim and evaluation where lease expiry, requeue and reconciling adoption
  live. The re-reviewer tried to build a counterexample and could not — so the claim is very
  likely true, but by an argument the comment does not make: the `finishPhase` fence
  (`orchestrator.go:879-886`, `n == 0` drops the result) means the phase row was live
  continuously up to this transaction, and no park can commit while a live
  `pending|running|reconciling` row exists (`RunIsWaitingOnAGate`'s NOT EXISTS), so the run
  cannot read `waiting` at `:943`. Not a false rationale — a true-as-far-as-anyone-can-determine
  claim stated with more confidence than its cited premises earn.
Task 5: fix round 2 dispatched by RESUMING `impl-task-5`. Scope is that ONE line plus the same
  phrase where it recurs in the report. CONTROLLER RULING: either restore the hedge or state the
  re-reviewer's closing argument — but state it ONLY after verifying each step personally
  against the code and SQL; asserting it on the re-reviewer's authority would be the same defect
  one level up. The `materializeResumedRuns` half stays flat; that one its own guard proves.
Task 5: fix round 1/5 (3 addressed, 1 new — the flat unreachability claim; commit 0048b4e)
Task 5: fix round 2 DONE, commit `7472bf1` ("docs(runs): argue syncWaiting's un-park
  unreachability from the finishPhase fence instead of asserting it"), one file, comment-only
  (+18/-8, all `//` lines). The implementer took the ARGUMENT option over the hedge and showed
  its five steps in the order it read them, as ruled.
CONTROLLER INDEPENDENT VERIFICATION AT `7472bf1`: `task backend:ci` exit 0 (54 `ok`, zero
  FAIL/panic/DATA RACE/deadlock, build lane stamped `Version=7472bf1`). `contracts:check` not
  re-run: comment-only in `orchestrator.go`, no watched path, and it passed at `0048b4e`.
Task 5: fix round 2 scoped re-review (`rereview2-task-5`, fable) — **ADDRESSED, no new
  findings** (`task-5-rereview-2.md`). It re-derived all five steps independently rather than
  checking them off the report, and went past the report on the two that matter:
  - STEP 2 (the one that actually closes the requeue hole) held under the hardest reading: it
    enumerated every SQL phase-status writer rather than trusting the Go `phaseTransitions`
    table — the only entry into `running` is `ClaimPhase` from `pending`, into `reconciling`
    from `running`, into `pending` only at INSERT, and the sole terminal-out edge is
    `succeeded -> superseded`. A requeue is a NEW ROW (`InsertPhase`; no edges out of `failed`)
    and the fence names one row id, so requeue/adoption is not representable.
  - STEP 4's exhaustive negative held: `syncWaiting`'s CAS really is the only writer of run
    status `'waiting'`, checked across every `CASRunStatus`/`FinishRun`/`StartRun`/`InsertRun`
    call site and every `'waiting'` occurrence in SQL (all the rest are predicates).
  - READ COMMITTED confirmed at the source: `pg.WithTx` uses a bare `pool.Begin` with no
    IsoLevel override anywhere.
  - Its own counterexample attempt (a waiting gate plus two siblings, one parking while the
    other materializes) fails exactly where the comment says it must.
  Modality judged correct: the flat claim is now EARNED by the proof that follows, and the
  `ClaimPhase` premise is demoted rather than deleted.
Task 5: minor (deferred): the round-2 report says both parkers hold the run row `FOR UPDATE`;
  `evaluateSuccess`'s lock actually arrives via `AddRunUsage`'s UPDATE. Immaterial — the
  COMMITTED COMMENT does not rely on it — but the report text is loose.
Task 5: fix round 2/5 (1 addressed, 0 open — the flat unreachability claim; commit 7472bf1)
Task 5: COMPLETE (commits 1ec46bf..7472bf1 = `6f60140` + fixes `0048b4e`, `7472bf1`;
  Spec ✅, quality Approved after two fix rounds, both re-reviews clean, 11 minors deferred)

Task 6: brief GENERATED (`task-6-brief.md`, 820 lines) and PRE-FLIGHTED before dispatch
  (`preflight-task-6`, opus, read-only, writing `task-6-preflight.md`). BASE will be `7472bf1`.
  The pre-flight was handed the full failure pattern from Tasks 3/4/5 (fictional harnesses three
  briefs running, sqlc-invalid SQL, `pgtype.Bool` returned as `bool`, `%s` on an int-typed
  `xerrors.Class` breaking the whole package build via vet, a per-task policy snapshot that does
  not exist, a factually backwards `finishRunTx` narrative that would have changed behaviour,
  a grep gate matching zero lines, and a test whose stated purpose was defeated by a second SQL
  predicate producing the identical observable state) and told to grade
  BLOCKER / CORRECTION / NOTE with a paste-ready resolution for each, plus a "Verified clean"
  section. It was pointed at `task-5-preflight.md` as the model and the standard.
  ELEVEN INHERITED ITEMS were written into the dispatch so they cannot be lost: the
  `SupersedePhasesInSliceParams` field-order trap; `RevisionSlice` returning the internal slice
  (Task 6 is its FIRST consumer); `CountSettledPublications` counting every intent including
  `pending`; `CountRequestChanges` being Task 6's; **the concurrent-revision test spelled out
  verbatim**; the unasserted gate-row generation plus Task 4's `generationBumper` fake that
  should now drive a real revision; `mintAuthorization` stamping the PHASE row's generation and
  the consequent requirement that Task 6 invalidate live authorizations when a sibling gate
  opens a revision; every symbol Task 5 added including the `openRevision` stub Task 6 replaces
  and the free-function `finishRunTx`; the `materialize.go` split recommended BEFORE Task 6
  grows that cluster (report the line delta so the controller can decide); the store-package
  test bootstrap split; and Decision 17 = the runtime 409.
Task 6: pre-flight DONE (`task-6-preflight.md`, 1171 lines) — **6 BLOCKER, 11 CORRECTION,
  15 NOTE.** Implementation half sound (keyed params throughout, the field-order trap avoided,
  `RevisionSlice` never mutated, `CountSettledPublications` used per its real semantics, the new
  SQL run through sqlc verbatim). Test half weak again, and B4 is a NEW KIND of defect for this
  plan: **a test that can never pass against the brief's own correct implementation** — it
  compares the seeded COPY's id against the original's, and `seedRevisionTarget` deliberately
  mints a new id with the original as parent. Same family as a test that cannot fail, seen from
  the other side. Also: the harness is invented for the FOURTH brief running; `%s` on
  `xerrors.Class` again; and five tests read a `Generation` field that exists on none of the
  runs projections (they gain it at Task 11, so Task 6 must read raw).
  The two pre-flight findings that changed the shape of the task:
  - B5: THE MANDATED CONCURRENT-REVISION TEST IS ABSENT from the brief. The pre-flight wrote it,
    with a `revisionRacer` executor that parks one attempt so a real `DecideGate` transaction
    contends for the same row lock — unlike Task 4's raw `UPDATE runs SET generation`, which
    never contends at all.
  - B6: the clock-skew test needs an injectable clock `newService` cannot supply (it hard-wires
    `clock.System{}`), AND it targets the wrong term: both rows it must separate are generation
    2, so the discriminator is `attempt DESC`, not `generation DESC` — which
    `TestGetNodeArtifact_PrefersTheNewerGenerationOverTheNewerTimestamp` already pins. What Task
    6 newly introduces (attempt-0 seeds sharing a generation with produced rows) is pinned
    nowhere. Replaced with a deterministic store-level test next to that sibling.
Task 6: CONTROLLER RULINGS on the pre-flight's open questions:
  - N5: do NOT convert Task 4's `TestOrchestrator_MaterializedRowsCarryTheRunsCurrentGeneration`
    to a real revision — it drives gateless `agenticDoc()`, so conversion means a different
    document, different node ids and three rewritten cases. B5 gives the same coverage far
    cheaper. **This overrides the carry-forward I wrote before seeing the cost.**
  - N6: do NOT fold the `materialize.go` split in — Task 6 adds ~45 lines to `orchestrator.go`
    and touches none of the materialization cluster. Split stays deferred as its own change.
  - N7: B6's store test goes in `store/schema_integration_test.go` next to its sibling; the
    helpers->`testutil` rename stays Task 14's.
  - N8: `openRevision` returns the NEW generation while approve/reject return the generation
    decided — keep both, and comment `GateDecisionResult.Generation` so Task 11 inherits the
    contract instead of an argument.
  - N12: leave the max-rounds refusal without an outcome code (nothing is terminalized);
    `CodeRevisionRoundsExhausted` stays unused. >>> TASK 15 ITEM. <<<
  - N13: keep the budget/deadline test as a cheap fence but do not count it as spec §3 coverage.
Task 6: BASE = 7472bf1. Implementer `impl-task-6` dispatched on opus, report file
  `task-6-report.md`. Dispatch carried all 6 blockers, all 11 corrections, the act-on notes
  (N14 DELETE THE STUB or the package will not compile; N2 the two new docs with a hand-traced
  `assignGates` justification; N3 `executionCount` as an artifact count; N4 the one line that
  finally asserts the gate row's generation from the writing end, closing a Task 4
  carry-forward; N11 renumber `openRevision`'s doc; N9 reword `syncWaiting`'s now-stale forward
  reference WITHOUT disturbing the hard-won argument around it; N15 the comment recording that
  every `waitForRun(RunWaiting)` is safe only because `openRevision` CASes inside the decision
  transaction), the five carry-forwards, the full gate, and the three failure modes with
  MUTATION required as proof wherever a test exists to catch a specific defect.
Task 6: implementer DONE_WITH_CONCERNS, commit `fe5128e` ("feat(runs): rematerialize the
  revision slice into a new generation on request_changes"), single commit, tree clean.
CONTROLLER INDEPENDENT VERIFICATION AT `fe5128e`: **BOTH GREEN.** `task backend:ci` exit 0
  (54 `ok`, zero FAIL/panic/DATA RACE/deadlock, build lane stamped `Version=fe5128e`);
  `task contracts:check` exit 0 including the sqlc regen-diff over `internal/runs/store` — the
  gate that matters here, since this task added `ListSeedArtifacts` and regenerated.
Task 6: FIVE MUTATION PROOFS reported, each introducing the specific defect its test names:
  M1 `collapsePatchInputs` before the seed loop; M2 supersede narrowed to the target alone;
  M3 seed ids dropped from `evaluateSuccess`'s parentIDs; M4a/M4b the two `GetNodeArtifact`
  ordering terms separately; M5 `ListSeedArtifacts`' generation filter removed.
  >>> M1 IS THE ONE TO REMEMBER: it did NOT go red on the first attempt. Against
  `gatedConveyorDoc` the mutated code stayed GREEN, because that document's revision target
  (`impl`) has only a PLAN as structural input — the two-patch collision C7 warns about cannot
  arise there at all. The implementer added `patchTargetDoc` (plan -> impl -> polish -> gate,
  so the target binds a patch through an edge AND dominates the gate) specifically to make the
  guard falsifiable. Its first attempt at that document used `review` as the target and the
  COMPILER correctly rejected it, since `impl -> gate` bypasses `review`. <<<
Task 6: nine declared deviations (D1-D9), the two that need a controller/reviewer judgment:
  D1 — `harnessBinding` EXTRACTED OUT OF `buildRequest`, code Task 6 was not asked to touch.
  Forced: `buildRequest` now queries the pool for seeds, and two existing in-package unit tests
  build an Orchestrator with a NIL `*pgxpool.Pool`; the first run panicked in
  `pgxpool.(*Pool).Acquire`. `svc.pool` is a concrete type, so no stub injects without a new
  seam. Controller's view, handed to the reviewer as disagreeable: forced, and the least
  invasive option. D2 — Task 5's `TestDecideGate_ValidationAndTheRequestChangesStub` asserted
  `ErrNotImplemented` on a well-formed `request_changes`, which THIS TASK DELETES; that half was
  removed and the test renamed. Neither brief nor pre-flight mentioned it; it would have failed
  the gate.
  D3-D9: row counts moved after the second `waitForRun` (counting right after `DecideGate` is
  racy in both directions because `openRevision` materializes ONLY the target and the rest of
  the slice arrives through the janitor); `patchTargetDoc` + its test (D4, see M1);
  `..._ASecondRoundSeesOnlyItsOwnNote` (D5, closes the `ListSeedArtifacts` generation-filter gap
  the pre-flight missed); B5 renamed/rescoped (D6, see F1) with `decisionExaminedArtifact`
  dropped because the generation-2 gate is UNDECIDED at that point so no row carries it;
  `..._SeedIsNeverReadAs…` strengthened to first assert the window it guards EXISTS (D7);
  `runGeneration == 3` added to the max-rounds test (D8); helper renames (D9).
>>> F1 — THE IMPLEMENTER CHALLENGES THE MANDATED B5 TEST'S PREMISE, and asked to be pushed. <<<
  Claim: a `request_changes` decision CANNOT commit while another attempt is being evaluated, so
  the contention B5 was commissioned to create does not exist. Argument: `BumpRunGeneration` and
  `FinishGatePhase` both require the run to read `waiting`; a run reaches `waiting` only through
  `syncWaiting`/`RunIsWaitingOnAGate`, whose `NOT EXISTS` forbids any live phase row except the
  caller's own `exclude_phase`; and that exception opens no window for a third party because the
  excluded row's `finishPhase` CAS is uncommitted, so every other transaction still sees it live,
  and the move to `waiting` commits in the same transaction that terminalizes the row — the two
  become visible together. Consequence if sound: the decision and any evaluation serialize on the
  run's STATUS COLUMN, not the row lock, and Task 4's `generationBumper` remains the only probe
  for the lock. B5 as shipped pins something real but different — that a REAL `DecideGate`
  transaction stamps every row of the new generation and leaves the old ones alone.
  Handed to the reviewer as a named risk with "the third bullet is where to push", because an
  unsound argument here would undermine the reachability picture Task 4's fix rests on.
Task 6: F2-F5 (implementer's own): B4's content assertion is weaker than it reads (the fake
  executor derives the diff from inputs that do not change across generations, so it
  discriminates only against a wholly wrong payload); a revision past an ALREADY-SUCCEEDED
  publish node leaves that node `succeeded` with an invalidated token — harmless until Task 9,
  >>> REVISIT AT TASK 9 <<<; `insertPhaseTx` stamps `created_at` from the APP clock while
  everything else `openRevision` writes uses the DB clock (pre-existing, feeds only
  `ClaimPhase` ordering); `ArtifactKindPublication` (Task 9) and `CodeRevisionRoundsExhausted`
  (Task 15) are both deliberately unused and neither trips lint.
Task 6: review dispatched (`review-task-6`, opus) over 7472bf1..fe5128e, report file
  `task-6-review.md`. Told not to run tests, given all overrides and rulings, pointed at six
  named risks and asked explicitly to adjudicate F1 (with the invitation to push on the third
  bullet) and D1.
Task 6: review (`task-6-review.md`) — **Spec ✅ compliant, Task quality APPROVED.** 0 Critical,
  2 Important, 4 Minor. Every blocker, correction and ruling verified carried out, and the
  transaction ORDER + PREDICATES were checked against the real SQL rather than the brief's prose:
  nil-revision refusal -> CountSettledPublications -> CountRequestChanges -> InsertGateDecision ->
  finishGate -> BumpRunGeneration -> InvalidateAuthorizations -> SupersedePhasesInSlice -> seed ->
  insertPhaseTx -> CAS(waiting->running) -> events -> audit. `finishGate` and `BumpRunGeneration`
  both precede the CAS (both predicate `status = 'waiting'`), supersede follows `finishGate` so
  the decided row is spared, and `insertPhaseTx` follows supersede so `phase_runs_live_key` is
  free. `rc.now` is `q.DBNow`, so every deciding predicate reads the DB clock.
>>> F1 ADJUDICATED: THE IMPLEMENTER IS RIGHT, and the reviewer made the argument STRONGER. <<<
  It pushed the third bullet as invited and could not break it: `syncWaiting` really is the only
  writer of `status = 'waiting'` in the package, every other `CASRunStatus` goes
  `waiting -> running`. AND IT FOUND A FOURTH LEG NOBODY HAD CITED, which makes the conclusion
  independent of the visibility reasoning: **`ClaimPhase` requires `r.status = 'running'` on the
  joined run row**, so an attempt cannot be CLAIMED on a waiting run at all — the property holds
  across the whole interval, not just at the transition instant. The converse closes too: while
  `openRevision` is uncommitted others still read the run as `waiting`, so the target's new
  pending row cannot be claimed until the decision commits.
  CONSEQUENCES TO CARRY: (a) the mandated B5 test CANNOT pin lock contention; keeping it rescoped
  was correct, and Task 4's `generationBumper` remains the only probe for the `GetRunForUpdate`
  lock. (b) THE SAME ARGUMENT IS WHAT MAKES THE `CountSettledPublications` REFUSAL SAFE — a live
  publish phase row keeps the run out of `waiting`, so the refusal and the status column defend
  the same property from two sides, and the refusal's INDEPENDENT value only appears in the
  post-crash shape F3 names. (c) The whole property rests on four predicates in three statements,
  none of which names the others. >>> TASK 9 IS THE FIRST CANDIDATE TO WEAKEN IT — it adds a
  publish effect that writes rows outside the phase lifecycle. <<<
Task 6: D1 (the `harnessBinding` extraction) ENDORSED by the reviewer independently: it verified
  the necessity from the diff (the old test ran `buildRequest` on an input-less node, so it never
  touched the pool), diffed the moved switch body line by line, and confirmed the mitigation test
  exists where the comment says. No change requested.
Task 6: THE TWO IMPORTANTS, both missing coverage over correct code:
  I1. `CountSettledPublications`' real predicate (EVERY status, `pending` included) is pinned by
      NOTHING anywhere in the repo — the only case seeded is `'published'`, and `grep` across
      every `*_test.go` returns nothing else. Narrow the SQL to `status = 'published'` and the
      whole suite stays green. `pending` is the dangerous one (the intent commits BEFORE the
      first forge call, so it can mean a branch is already on the forge) and F3's post-crash
      shape is where the count is the only defence — arriving with Task 9.
  I2. Nothing asserts the revision path's EVENTS or AUDIT entry. Delete either event or the whole
      `appendEvents` call and all twelve tests still pass. Departs from the standard the sibling
      path set (Task 5 pinned exactly this for approve) and the helpers already exist. The run
      timeline is the operator's only view of a revision, and Task 11 builds an API on it.
Task 6: fix round 1 dispatched by RESUMING `impl-task-6`. Scope: I1, I2, and ONE Minor folded in
  by controller ruling — M1, where `evaluateSuccess` records a parent the attempt was never given
  (`collapsePatchInputs` dropped the structural patch from the request, but the structural loop
  still records it), contradicting the very rule C8 was added under. RULING: AMEND THE COMMENT,
  do not change the behaviour — say `parent_ids` records declared lineage plus seeds. Dropping
  collapsed inputs is a real lineage change that nothing pins and that does not belong in a fix
  round. Folded in because it is the accuracy-of-justification class this plan polices, and it
  keeps the round's code diff to one comment line.
Task 6: minors (deferred to the final whole-branch review), 3:
  M2. A concurrent revision on a SIBLING gate reports "the run's wall deadline has passed" when
      it has not — `BumpRunGeneration` has two predicates and every empty result maps to that one
      message. Newly reachable (no other caller). Rolls back cleanly; only the message lies.
      >>> ALSO FLAG AT TASK 11, which owns the surface that renders it. <<<
  M3. The `generation > run's generation` half of B5 is vacuous — nothing in the design can stamp
      a row AHEAD of the run, so that count is 0 under every implementation. The test's weight is
      carried by the three specific assertions. The racer's mid-window read is also no different
      from reading at the end, since nothing updates a phase row's generation after insert. If a
      later task wants the file smaller, this is the first thing that can go.
  M4. F2 confirmed: the fake executor makes the two compared payload values identical by
      construction, so that line discriminates only against a wholly wrong payload kind.
Task 6: fix round 1 DONE, commit `1e41741` ("test(runs): cover every publish-intent status in
  the revision refusal and pin the revision timeline"), two files — `orchestrator.go`
  comment-only, the rest tests. Four mutation proofs, each red-then-reverted.
  Two judgment calls inside the fixes, both endorsed by the re-review:
  - I1: the seeded receipt columns now FOLLOW the status (a `pending` row carries no `pr_url`),
    because a pending row with receipts is a state Task 9's machine cannot produce and seeding
    one would assert against a shape that never occurs. Mutation A (narrowing the SQL to
    `status = 'published'` in both `queries.sql` and the generated const) came back RED on
    `/pending` and `/pushed` with `/published` still green — exactly the predicted split.
  - I2: `countEvents(EventGateDecided) == 1` added ALONGSIDE the revision-event count, because
    both events come from one `appendEvents` call and an assertion on only the new one would
    survive losing the first element. The audit detail reads `generation` rather than
    `invalidated_authorizations`, since this fixture's gate has no prior approval so that list
    is empty and an assertion on it would be satisfied by a missing key.
CONTROLLER INDEPENDENT VERIFICATION AT `1e41741`: **BOTH GREEN.** `task backend:ci` exit 0
  (54 `ok`, zero FAIL/panic/DATA RACE/deadlock, build lane stamped `Version=1e41741`);
  `task contracts:check` exit 0. I also confirmed the mutated SQL was genuinely reverted:
  `CountSettledPublications` carries no status predicate in `queries.sql`.
Task 6: fix round 1 scoped re-review (`rereview-task-6`, fable) — **ALL THREE ADDRESSED, no new
  breakage** (`task-6-rereview.md`), every claim checked against the committed tree rather than
  the report's prose:
  - I1: the seed was checked COLUMN BY COLUMN against `schema.sql:401-441` — all NOT NULLs
    supplied, receipts nullable, the status CHECK admits all three, both unique constraints hold
    (fresh authorization and fresh run per subtest). It also closed the vacuity question: the
    seed CANNOT fail silently, because a SQL error `Fatalf`s and a 0-row `INSERT..SELECT` would
    let `DecideGate` succeed and fail the `ClassConflict` assertion.
  - I2: THE CAREFUL CHECK HELD. The audit detail's `generation` is `BumpRunGeneration`'s return
    (the OPENED 2) while the decided `rc.gate.Generation` (1) goes only into the event payload —
    so `== 2` genuinely discriminates the SOURCE, not just the key's presence; absence scans
    NULL and fatals, zero fails. One refinement to the implementer's reasoning:
    `invalidated_authorizations` would be present-but-EMPTY rather than missing — but an
    emptiness assertion still discriminates nothing here, so rejecting it was right anyway.
  - M1: the `orchestrator.go` hunk is mechanically comment-only, and the new text is true
    clause by clause. NO OVER-CLAIMING THIS ROUND — the first round on this plan where a
    rewritten comment needed no correction.
  - The F1 `ClaimPhase` leg was verified before being written down (`queries.sql:124` really
    carries `AND r.status = 'running'`), and the comment states the interval argument the
    review supplied and no more.
Task 6: minor (deferred): the `publish_intents` status CHECK admits a FOURTH value, `'halted'`,
  which I1's loop does not cover — a `status <> 'halted'` narrowing of
  `CountSettledPublications` would stay green. The finding asked for exactly three, so this is
  deferrable. >>> NATURAL HOME TASK 9/10, which own the halted state. <<<
Task 6: fix round 1/5 (3 addressed, 0 open — I1, I2, M1; commit 1e41741)
Task 6: COMPLETE (commits 7472bf1..1e41741 = `fe5128e` + fix `1e41741`; Spec ✅, quality
  Approved, re-review clean, 4 minors deferred)

Task 7: brief GENERATED (`task-7-brief.md`, 508 lines; byte-identical to plan lines 4038-4545,
  so no transcription drift) and PRE-FLIGHTED (`preflight-task-7`, opus, read-only, worktree
  never touched) — **6 BLOCKER, 8 CORRECTION, 20 NOTE** in `task-7-preflight.md` (814 lines).
  THE PRE-FLIGHT WENT EMPIRICAL: it transcribed the brief's `forge.go` verbatim into a scratch
  module (`scratchpad/runprobe/`, never the repo), wrote the minimum faithful `github.go` around
  it, and RAN the brief's own six GitHub tests. The package did not compile (B1); once it did,
  THREE of the six failed against the brief's own correct-as-written implementation (B2, B3,
  B4) — the Task 6 failure mode, three times over. It then verified its own resolutions turn
  the suite green.
  Good news first: the two things that killed Tasks 4-6 are absent — no invented harness (every
  test stands up its own `httptest` server) and no `xerrors` usage at all, so the `%s`-on-`Class`
  vet break cannot fire. Platform purity is safe: every prescribed import is stdlib and no new
  module dependency appears.
  The two blockers worth knowing before dispatch: the mandated "paginated to exhaustion" has NO
  MECHANISM AND NO TEST, and a plausible implementation HANGS against the brief's own handler
  (B5); and the single GitLab test is written so a client that never maps `Base` ships green
  (B6) — the exact defect the brief spends a paragraph arguing is fatal.
  >>> NEXT SESSION: read `task-7-preflight.md` in full and fold every BLOCKER and CORRECTION
  into the dispatch as an override, exactly as Tasks 4, 5 and 6 were dispatched. <<<

Task 7: BASE = 1e41741. Implementer `impl-task-7` dispatched (Opus 5, session 46), with all
  6 BLOCKERs and 8 CORRECTIONs from `task-7-preflight.md` folded in as binding overrides plus
  N13/N15/N16 made mandatory. Agent name verified un-suffixed in the spawn result.
Task 7: implementer DONE, commit 50f8f30 (5 new files, all under platform/forge; nothing else
  touched, in particular NOT ports.go per N1). Reports 21 mutants / 0 survivors and applies all
  6 blockers + 8 corrections + N13/N15/N16; self-declares 4 deviations (a shared `roundTrip`
  under the two `do` methods; two ADDED tests — a GitLab pagination test and an off-host `Link`
  guard test with its three-line production guard; RED 1's wording; a named `maxMessageBytes`).
Task 7: CONTROLLER-VERIFIED on 50f8f30, not on the report: `task backend:ci` exit 0 — all nine
  lanes invoked, build lane stamped `Version=50f8f30`, 56 `ok` package lines, zero
  FAIL/panic/DATA RACE — and `task contracts:check` exit 0 on a clean tree (no-op, as predicted).
Task 7: review — SPEC ✅ PASS, QUALITY Approved. 0 Critical, 4 Important, 8 Minor. The reviewer
  audited all 21 mutants by reading the assertion each is attributed to: every attribution holds,
  the claim is "not inflated" — the gap is mutant SELECTION, four wrong implementations survive
  the whole 15-test suite. Its three "cannot verify from diff" items are all the house gate,
  already settled by the controller's own run on 50f8f30.
Task 7: Important — I7-1 the `maxPages` cap's BEHAVIOUR is untested on both forges (`return refs,
  nil` at the cap survives all 15 tests; that is the truncated-probe -> second PR on a single-use
  approval failure); I7-2 GitLab's branch escaping unpinned (every fixture branch lacks a `/`, so
  dropping url.PathEscape survives — and unescaped, `caesar/abc` 404s, i.e. BranchHead would deny
  every marker branch caesar itself pushes); I7-3 `RetryAfter`'s instant asserted for presence
  only (epoch-as-millis, `now` instead of `now.Add`, and swapped arms all survive); I7-4
  `rateLimited`'s doc comment is false about its own GitLab path — brief-verbatim, gates on the
  `X-`-prefixed remaining header while GitLab emits the un-prefixed draft-spec ones, so GitLab's
  stated reset is always discarded. CONTROLLER RULING on I7-4: fix the code, not just the comment
  (symmetric `remaining` fallback), and pin the newly-live branch with one GitLab 429 test.
Task 7: minor (deferred, 8): M7-1 `redact` runs AFTER truncation so a token straddling the
  200-byte boundary leaves a prefix in a durable string; M7-2 GitHub's request-body spelling only
  half-pinned (`title`/`body` mutants survive); M7-3 401 -> `ErrUnauthorized` mapped but pinned by
  nothing; M7-4 `Repo.Slug` interpolated unescaped/unvalidated on 3 of 4 GitHub calls; M7-5
  `nextLink` accepts only the quoted `rel="next"`, an unquoted `rel=next` silently truncates the
  probe; M7-6 the off-host guard compares raw string prefixes rather than parsed hosts; M7-7
  `maxBodyBytes` truncation is indistinguishable from a malformed response; M7-8
  `TestGitHub_TransportFailuresAreRetryable` is theoretically racy (implementer's own Concern 3,
  confirmed). >>> POINT THE FINAL WHOLE-BRANCH REVIEW AT M7-5 AND M7-6 SPECIFICALLY: both are the
  same silent-truncation / credential-forwarding class as I7-1, rated Minor only because GitHub
  happens to send the quoted form today. <<<
Task 7: fix round 1/5 (4 addressed, 0 open — I7-1 cap-refuses-rather-than-truncates pinned on
  BOTH forges asserting the error not len(got), I7-2 GitLab branch escaping pinned with the real
  `caesar/abc` marker shape, I7-3 `wantAt` column on the absolute-epoch row, I7-4 symmetric
  `remaining` fallback in forge.go + its own GitLab 429 test; commit e7ab4d6). Implementer proved
  each by red-then-revert: 7 mutants, 7 killed (cumulative 28/28). It also disclosed that one
  round-1 mutant had failed to BUILD and that its round-1 harness would have scored a build
  failure as a kill — an honest disclosure worth more than the tally.
Task 7: CONTROLLER-VERIFIED on e7ab4d6: `task backend:ci` exit 0 (nine lanes, stamped
  `Version=e7ab4d6`, 56 `ok`, zero FAIL/panic/DATA RACE) + `task contracts:check` exit 0 clean.
Task 7: re-review — all four ADDRESSED, no Critical/Important breakage in the fix diff; the
  re-reviewer re-derived each of the 7 kills from the assertions rather than trusting the
  transcript. It found I7-3's THIRD named survivor still live: `now.Add(secs)` -> `now` on the
  SECONDARY-limit arm (forge.go:95), which keeps `wantAt` zero and is checked for presence only,
  so the mutant passes both assertions; the arms-swapped mutant survives on the same row. Round
  1's tally was honest — I7-3b mutated the epoch arm, so this mutant was never in the set.
Task 7: CONTROLLER RULING — the re-reviewer recommended carrying that survivor forward rather
  than reopening; OVERRIDDEN into a round 2. It is ~6 lines with a bound that holds by
  construction (monotonic readings, `classify`'s now taken strictly between the brackets), and a
  named live survivor on a guard this task exists to provide is what the plan's mutation
  criterion is for. Folded in re-review 6.1 (the rewritten comment dropped the reset's UNITS and
  mislabels GitLab's epoch-seconds `RateLimit-Reset` as the IETF draft's delta-seconds) — same
  comment the I7-4 ruling just touched. 6.2 deferred with the round-1 minors.
Task 7: fix round 2/5 COMMITTED as `5c32766` (forge.go + github_test.go, +35/-10, subject:
  "pin the stated retry delay as a bracketed window on the secondary-limit arm") — then the
  MACHINE CRASHED at ~15:45, before the controller gate ran and before the re-review verdict
  was read. The re-review diff `review-e7ab4d6..5c32766.diff` was captured at 15:44; its result
  is lost. NOTHING HAS VERIFIED THIS COMMIT. Resume by (1) running `task backend:ci` +
  `task contracts:check` against 5c32766 yourself, (2) re-running the round-2 re-review and
  demanding red-then-revert on the two named survivors (secondary-arm `now.Add(secs)` -> `now`,
  and the arms-swapped mutant on the same row), plus confirming re-review 6.1 (the comment's
  dropped UNITS and the GitLab epoch-seconds-vs-delta-seconds mislabel) actually landed.
  6.2 remains deferred with the round-1 minors.
Task 7: CONTROLLER-VERIFIED on `5c32766` (session 48, after the crash): `task backend:ci` exit 0
  — all nine lanes (analyze, build, fmt:check, lint, test, test:integration, test:property, vet,
  vuln), 56 `ok`, zero FAIL/panic/DATA RACE, build lane stamped `Version=5c32766` confirming the
  gate ran against the right code — plus `task contracts:check` exit 0 against a clean tree.
  ⚠️ METHOD NOTE WORTH KEEPING: the first attempt ran `task backend:ci 2>&1 | tail -80`, so `$?`
  reported TAIL's status, not the gate's. It came back "exit 0" while the gate had actually
  FAILED — the crash had taken Docker Desktop down with it and every testcontainer lane errored
  with `Cannot connect to the Docker daemon`. A piped gate cannot report its own exit code.
  Never pipe `task backend:ci`; redirect to a file and read `$?` directly. Docker was restarted
  (`open -a Docker`, daemon 29.6.2) before the real run.
Task 7: re-review round 2 — **PASSED, EXECUTED**; Task 7 CLOSES, no round 3. The reviewer was
  required to prove kills by red-then-revert rather than by reading, precisely because reasoning
  is what let the survivor through in round 1. Results: (1) the round-1 named survivor
  `forge.go:99` `now.Add(secs)` -> `now` is DEAD — observed instant `m=+0.0066` against a window
  opening at `m=+60.0062`, i.e. it misses by ~60s against a ~343us window, not a marginal kill;
  (2) the arms-swapped mutant (`Retry-After` read as an epoch) is DEAD, landing at
  1970-01-01T00:01:00Z; (3) BONUS — the reviewer also ran the FULL two-arm swap, which round 1
  predicted would half-survive, and it now dies on BOTH rows (the epoch-as-delta direction lands
  in year 2086 and `wantAt` catches it). Both arms are now individually pinned to their own unit
  semantics and neither can be silently substituted for the other.
Task 7: the bracket was audited as SOUND BY CONSTRUCTION, not merely passing — `before <= now <=
  after` follows from same-goroutine program order (`classify`'s `time.Now()` at `forge.go:329`
  is taken synchronously inside the bracketed call), the monotonic readings are present on all
  three operands (visible as Go's own `m=+...` in the failure output, and nothing on the path from
  `forge.go:99` -> `retryable{at}` -> `RetryAfter` strips them), the bounds are inclusive so a
  degenerate coarse-clock window still passes, and no table row is wrongly skipped by
  `if c.wantDelta > 0`.
Task 7: re-review item 6.1 LANDED and verified true claim-by-claim against the code beneath it
  (`forge.go:81-89`) — units restored and capitalised (EPOCH SECONDS, the form `time.Unix` reads),
  and the dangerous "draft-spec pair" mislabel replaced with a correct contrast caveat. One
  two-word residual: `RateLimit-ResetTime` is a DATE (RFC 7231), not "delta/date" — and that
  wording was inherited VERBATIM from round 1's own suggested patch, so the implementer followed
  the prescription exactly; the imprecision is the reviewer's. Not worth a round. If ever touched,
  "date-formatted sibling" is the right phrase.
Task 7: no Critical or Important breakage in the round-2 diff; `forge.go`'s function body is
  byte-identical to `e7ab4d6` (the only production edit is inside a doc comment). Scope held —
  M7-1's redact-after-truncate (`forge.go:265`) and M7-5's quoted `rel="next"` (`github.go:176`)
  both still untouched, as ordered. Package re-verified green at the reverted state
  (`-race -count=3` ok, `go vet` clean); controller independently re-confirmed clean tree,
  HEAD `5c32766`, package `ok`.
Task 7: DEFERRED CARRY (new, all Minor, none worth a round): (a) `wantDelta > 0` cannot express a
  legitimate `Retry-After: 0`, conflating "unset" with "zero" — same idiom as `wantAt.IsZero()`
  above it; fix with `*time.Duration` or a `hasDelta bool` WHEN such a row is added. (b) The
  monotonic guarantee is asserted by comment and enforced by nothing: adding `.UTC()` to the
  `Retry-After` arm for symmetry with `forge.go:112` would strip the reading, silently degrade
  the comparison to wall-clock, and the test would keep passing. (c) The `RateLimit-ResetTime`
  wording above. (d) PRE-EXISTING, not this diff's: no row asserts the ABSENCE of a retry instant,
  so a mutant handing bare 429s a bogus instant would survive.
Task 7: COMPLETE — commits `50f8f30` + fixes `e7ab4d6`, `5c32766`. 6.2 and M7-1..M7-8 remain
  the controller's deferred set. >>> M7-5 AND M7-6 STILL GO TO THE FINAL WHOLE-BRANCH REVIEW. <<<
Task 8: brief GENERATED (`task-8-brief.md`, 669 lines; faithful extraction of plan lines
  4546-5214, confirmed by the pre-flight against the plan source — nothing dropped or added).
  ⚠️ GENERATOR GOTCHA: `scripts/task-brief` WRITES THE FILE ITSELF and also prints a status line
  to stdout. Redirecting it (`task-brief ... > task-8-brief.md`) races its own write and yields a
  CORRUPTED brief (668 lines, starting mid-word). Run it with NO redirect.
Task 8: PRE-FLIGHTED (`task-8-preflight.md`) — **4 BLOCKER, 5 CORRECTION, 9 NOTE**, each with
  paste-ready code. It went empirical again: transcribed the brief's test file into a full copy of
  `backend/` OUTSIDE the worktree, wrote a faithful `publish.go` to the brief's own Step 3, ran
  everything, then mutation-tested every guard the brief claims. Verdict: the brief's LOGIC is
  sound — all four sharp invariants (empty-lease create, date pinning, rebuilt-sha ownership,
  observed-sha-is-not-permission) are real and falsifiable, and `--force-with-lease=<ref>:` does
  exactly what the brief says (verified on git 2.39.3 against both absent and existing refs).
  What is broken is the TEST FILE: it does not compile and six helpers do not exist.
Task 8: the four BLOCKERs — (1) `grepTree` ALREADY EXISTS (`stage_test.go:336`) returning
  `[]string`, so the brief's `found != ""` is a type error and NOTHING in the file compiles until
  fixed; (2) Step 1 mandates `var runGitFn = runGit` but Step 3 — the block an implementer
  actually pastes — calls plain `runGit` at all ten call sites, which turns two tests red;
  (3) SIX invented, undefined helpers (`initRemote`, `addFilePatch`, `gitOut`,
  `pushForeignCommit`, `createBranchAfterPrepare`, `recordGitInvocations`) and NO import block at
  all — the Task 4 failure mode again; note `initRemote` duplicates the package's existing
  `stageFixture` (`stage_test.go:19`), which the brief did not notice; (4) Step 3's body is
  `Publish`'s but the brief says it moves "verbatim" into `publishPrepare`, which returns a
  DIFFERENT type, and `preparedPublication.Dir` is declared but never set — without it step 8's
  push is unreachable.
Task 8: >>> THE FINDING THAT MATTERS MOST — CORRECTION 2. <<< Three refusals the brief calls
  load-bearing are UNFALSIFIABLE BY THE BRIEF'S OWN SUITE: the zero-`Date` refusal (the test
  file's opening convention paragraph is BUILT on it), the branch-name guard, and
  `decodePublishPatch` classifying an undecodable patch as `CodeInvalidOutput` rather than
  `CodeStagingFailed` (the stated reason the function exists separately). The pre-flight DELETED
  all three and all 11 of the brief's tests still passed. Resolution folded into the dispatch:
  move the date guard UP to the top of `publishPrepare` (the brief put it at step 6, AFTER a full
  network clone, against its own precedent that cheap refusals precede git), plus two new tests
  proven red-without/green-with. This is [[caesar-mutation-acceptance]] catching a plan defect
  BEFORE any code was written, which is the earliest it has ever fired on this plan.
Task 8: corrections 1/3/4/5 — goimports grouping (`uuid` in its own group before the local one,
  per `goimports.local-prefixes`); `context.Background()` -> `t.Context()` throughout (11 sites,
  matching every existing test in the package, and it kills wedged git processes at test end);
  the brief's Go is NOT gofumpt-clean so `backend:fmt:check` would red it (run `task backend:fmt`,
  do not hand-align); and Step 6's `go test ./internal/runs/runner/... -race` is narrower than the
  house rule, replaced with the full gate.
Task 8: notes worth carrying — NOTE 4: `ExpectedPublicationHeadRebuilds...` does NOT on its own
  prove reproducibility (with the commit-date env removed it still passes; only the idempotency
  test's 1100ms sleep catches it), so THE SLEEP IS THE ENTIRE REPRODUCIBILITY GUARD and trimming
  it for speed silently deletes the invariant. NOTE 6: `ExpectedPublicationHead` DOES touch the
  remote (clone + ls-remote); what it leaves alone is the remote's REFS — Task 9 must not assume
  it is a free/offline call. NOTE 7: `Publish` is NOT re-entrant on the same `dir` (`git clone`
  fails if `<dir>/publish` exists), so a Task 9 caller running `ExpectedPublicationHead` then
  `Publish` must hand each a FRESH directory. NOTE 8: arch/deps/network all clear — `uuid v1.6.0`
  already in go.mod, no DenyList/depguard hit, no cross-context import, no network in any test.
Task 8: no live carry-forward from the ledger lands on Task 8 (all are addressed to Tasks 9-15),
  and the brief contradicts none of them — verified by the pre-flight against progress.md:125-180.
Task 8: implementer `impl-task-8-s48` dispatched (Opus 5) on BASE `5c32766`, with all four
  BLOCKERs and all five CORRECTIONs folded in as explicit overrides, TDD ordering, red-then-revert
  demanded on all seven named guards, and the un-piped full gate as the acceptance bar.
Task 8: implementer DONE, commit `a8543fc` ("feat(gitops): publish an approved patch from a fresh
  checkout under a remote precondition"). 4 files: publish.go (350), publish_test.go (366),
  publishhelpers_test.go (132), gitops.go package doc (+11/-4). All four BLOCKERs and all five
  CORRECTIONs applied; TDD red-first confirmed (the only undefined symbols in the initial failure
  were `Publish`/`PublishInput`). 10 mutants, 10 kills, each confirmed to BUILD (no compile-error
  false kills), with a post-mutation diff against a backup proving nothing leaked into the commit.
Task 8: CONTROLLER-VERIFIED on `a8543fc`: `task backend:ci` exit 0 — nine lanes, 56 `ok`, zero
  FAIL/panic/DATA RACE, zero docker errors, build lane stamped `Version=a8543fc` — plus
  `task contracts:check` exit 0 on a clean tree. Both run unpiped with `$?` captured directly.
Task 8: review — **SPEC PASS, QUALITY Changes-requested.** 0 Critical, 3 Important, 5 Minor. The
  reviewer audited all 10 mutant kills BY READING rather than trusting the tally: all 10 are real,
  with two framing corrections (M2's kill is DETERMINISTIC — 1100ms > 1000ms guarantees the
  wall-clock second turns over, not timing luck; and M4's "exactly one test" is a property of where
  the mutant sits). It then named **TWELVE survivors the suite would not kill**. It also walked
  NINE crash/interleaving scenarios incl. two-runners-one-branch and found NO case where `Publish`
  does the wrong thing — the leased push is the only ref-writing command, its refspec is explicit,
  and the empty lease is a server-side CAS on the zero oid, so the `ls-remote` probe is never
  authoritative for a write.
Task 8: >>> THE RECURRING DEFECT, AGAIN: guards that exist and are correct but that NO TEST CAN
  FALSIFY. <<< Same class the pre-flight's CORRECTION 2 caught in the brief, now in the code.
  I-1: dropping `.UTC()` at publish.go:253 (or the zone from the date layout) survives the WHOLE
  suite — a commit stores `<epoch> <tz-offset>` and the offset is HASHED, so the rebuilt sha
  silently becomes a function of the caller's `time.Location` / the host's /etc/localtime. This
  settles the reproducibility question: the 1100ms sleep IS genuine and load-bearing and must not
  be trimmed, but it is NOT SUFFICIENT — both calls run in one process, so it structurally cannot
  see the timezone half. I-2: deleting either `checkCloneURL` (publish.go:170) or
  `assertNoCredentialConfig` (:199) kills no test, and A TOKEN CAN REACH DISK — a userinfo clone
  URL lands in `.git/config` as `remote.origin.url`, which the scan's key list
  {extraheader,askpass,credential} does not match and `redact` does not know about, and
  publish.go:194 formats CloneURL VERBATIM into an outcome message Task 9 persists as a run event.
  I-3: `TreeHash: ""` silently disables the approval-tree check via INPUT with no signal anywhere.
  Controller independently verified all three against source before ruling, plus the zero-
  `t.Parallel()` claim.
Task 8: CONTROLLER RULING on I-3 — adopt the HARD REFUSAL (review's option 1), a deliberate
  behaviour change beyond the brief. Justification: `approved_patches.tree_hash` is `text NOT NULL`
  (`backend/internal/platform/pg/schema.sql:372`) and Task 9 threads it as `intent.TreeHash`, so a
  tree hash is ALWAYS available and an empty one is a caller bug, never a legitimate input. This
  makes `TreeHash` symmetric with `Date`, which is already a hard refusal.
Task 8: CONTROLLER RULING on the Minors — ALL FIVE FOLDED IN rather than deferred (a departure
  from Task 7, which deferred its minors). Reason: M-2/M-3/M-4 are three more UNFALSIFIABLE guards
  and each has an existing mirror in stage_test.go to copy, so closing them costs a few lines and
  retires the entire defect class in this file; M-4 in particular is the same permanent-condition-
  retried-as-transient misclassification the pre-flight caught for the branch-name guard. M-1 is a
  latent credential leak (three error paths skip `redact`). M-5 is COMMENT-ONLY — do not drop the
  `%s`. Also ordered: a one-sentence comment recording that `runGitFn`'s -race safety rests on
  there being zero `t.Parallel()` in the package (verified zero) — otherwise an undocumented
  invariant.
Task 8: DEFERRED / left alone deliberately — `checkCloneURL` returning
  `CodeStagingVerificationFailed` on a publish (pre-existing, shared with Stage; the new test
  asserts it AS-IS rather than changing it), `remoteBranchHead`'s dead `sha == ""` fallback, and
  Task 7's 6.2 + M7-1..M7-8.
Task 8: honesty audit of the implementer — all three of its disclosures are honest, but #2 stops
  one step short: the guard it nominates as "the primary credential guard"
  (`assertNoCredentialConfig`) is ITSELF unfalsifiable, and the one realistic disk-leak path is
  covered by neither mechanism it names. Eight of the twelve survivors were not disclosed anywhere,
  so §3's "10 mutants, 10 kills" reads as a coverage claim the suite does not support. A true
  tally is not a coverage claim — worth remembering when reading any mutation report.
Task 8: fix round 1/5 dispatched (`fix-task-8-round1-s48`, Opus 5) on BASE `a8543fc` with I-1,
  I-2, I-3 (hard refusal), M-1..M-5 and the runGitFn comment, red-then-revert demanded on all
  EIGHT named mutants, and the reviewer's own caveat passed through: its expected `%aI` literal
  `2026-08-07T12:00:00+00:00` may render as `Z` on some git versions and must be confirmed against
  the local git rather than pasted blind.
Task 8: fix round 1/5 COMMITTED as `048b50c` ("test(gitops): make publish's date, credential and
  cap guards falsifiable and require the approved tree hash"), 3 files, all eight findings applied.
  CONTROLLER-VERIFIED: `task backend:ci` exit 0 — nine lanes, 56 `ok`, zero FAIL/panic/DATA RACE,
  zero docker errors, stamped `Version=048b50c` — plus `task contracts:check` exit 0, clean tree.
  Every mutant died, none failed to build, and `diff` proved `publish.go` restored byte-identical
  before the gate ran.
Task 8: >>> THIS ROUND FOUND THREE HOLES IN THE REVIEW'S OWN REMEDIES. Worth remembering: a
  review's prescribed fix can itself be a survivor, so a fix round must MUTATE the remedy, not
  just apply it. <<<
  (1) I-1's `%aI` literal assertion is HOST-DEPENDENT. The "drop the zone from the date layout"
  mutant makes git read the stamp in the host's local time; this dev box is +0400 so the literal
  killed it here, but ON A UTC HOST — WHICH IS WHERE CI RUNS — the mutant renders `+00:00` and
  SURVIVES. The assertion would have been falsifiable on the machine that does not matter and
  unfalsifiable on the one that does. Closed with `pinGitLocalZone(t, "Asia/Tokyo")` through the
  `runGitFn` seam: correct code still emits `+00:00` (the RFC3339 stamp carries its own zone), the
  mutant now emits `+09:00` on EVERY host.
  (2) M-2's remedy killed only the CREATE arm — `PublishReport.TreeHash` is returned from two
  sites and the ADOPT arm's `TreeHash: ""` mutant came back green (transcript captured). Closed
  with a second assertion in the idempotency test.
  (3) M-3's proposed class+code-only assertion WOULD ITSELF HAVE BEEN A SURVIVOR: with a cap
  raised, an oversize patch still fails as a permanent `invalid_output` from `git apply` — same
  class, same code. Only the wording distinguishes "I enforced a bound" from "git could not apply
  this", so the cap tests assert the MESSAGE NAMES THE BOUND.
Task 8: I-3 knock-on the ruling did not anticipate (flagged by the fixer, on record rather than
  silent): requiring `TreeHash` made the step-5 `in.TreeHash != ""` half DEAD (now unconditional),
  and broke EVERY pre-existing test since none set one. Each valid-patch call site now computes the
  real value via a new `approvedTree` helper that builds the tree with plain git in a throwaway
  clone — deliberately NOT through `publishPrepare`, so the expectation is independent of the code
  under test rather than circular. Refusal-before-any-tree sites use a named `unreachedTree`
  constant instead of a plausible-looking fake. Cost ~10 extra clones of a one-commit bare repo.
Task 8: the fixer confirmed the I-1 caveat against the local git before pasting: git 2.39.3 renders
  `%aI`/`%cI` as `2026-08-07T12:00:00+00:00`, NOT `Z`, so the review's literal was correct as
  written on this toolchain.
Task 8: incidental but worth knowing — with the `checkCloneURL` guard MUTATED AWAY, the suite
  reaches the REAL NETWORK (the userinfo URL test targets github.com). That is a property of the
  mutant only; with the guard present nothing leaves the machine, and the mutant's own failure
  output demonstrates I-2's claim first-hand by carrying the token verbatim into an outcome message.
Task 8: re-review of fix round 1 dispatched (`rereview-task-8-round1-s48`, Opus 5) on
  `a8543fc..048b50c`, pointed HARDEST at independently verifying the fixer's three
  "the-review-was-wrong" claims (a claim that the reviewer erred is exactly the kind that must be
  verified, not accepted), plus whether `approvedTree` is genuinely non-circular, whether the TZ
  pin really makes the kill host-independent, and which of the previous review's TWELVE named
  survivors are now closed vs still live.
Task 8: re-review of fix round 1 — **ALL EIGHT FINDINGS ADDRESSED** (I-1, I-2 core, I-3, M-1..M-5)
  plus the `runGitFn`/`t.Parallel` comment; **no new breakage**; verdict "Task 8 can CLOSE, no
  round 2 required". Every kill was proven red-then-revert against a backed-up copy, not by
  reading. It INDEPENDENTLY REPRODUCED all three of the fixer's "the review's own remedy was
  wrong" corrections and all three HOLD — including running the S2 mutant on a SIMULATED UTC HOST
  (injecting `TZ=UTC` into `baseEnv`) to confirm both that the TZ pin still kills there and that
  the review's bare `%aI` remedy would have SURVIVED there. The pin wins over an earlier `TZ=UTC`
  in the same env because `os/exec` de-duplicates the environment keeping the LAST occurrence.
Task 8: survivor ledger after round 1 — 8 of the 12 named survivors are DEAD by executed
  red-then-revert. The 4 still live are the ones already classified safe-by-design or untestable
  by construction and are NOT to be chased: S9 (`remoteBranchHead`'s unknown-error arm — safe
  because the probe is never authoritative for a write; only the leased push moves a ref), S10
  (`baseEnv(dir)` -> `baseEnv(repo)`, low consequence), S11 (dropping `--no-verify` — untestable
  by construction, a fresh clone carries no hooks), S12 (the credential test's negative half is a
  name allow-list, still blind to `assertNoCredentialConfig`'s own `config` call which uses
  `runGit` not `runGitFn`).
Task 8: TWO NEW MINORS from the re-review, both verified by execution.
  R-1 — the I-2 test does NOT pin the guard's PLACEMENT, only "before the push". Moving
  `assertNoCredentialConfig` to the last statement of `publishPrepare` — after the checkout, the
  patch write, the apply and the commit — SURVIVES the whole package (exit 0), so a full commit
  gets built inside a checkout never proven credential-free. The review's remedy claimed this was
  pinned; its second sentence was not delivered, and the test's NAME overclaims what it proves.
  R-2 — >>> THE SHARPEST FINDING OF THE SESSION: A FALSIFIER THAT FAILS GREEN. <<<
  `pinGitLocalZone(t, "Asia/Tokyo")` needs a zoneinfo database. On an image WITHOUT tzdata git
  falls back to UTC **without erroring**, so correct code still renders `+00:00`, the test stays
  GREEN, and the S2 mutant becomes a survivor again — the exact failure the pin was added to
  eliminate, restored by the environment and INVISIBLE because it fails green. Same
  "unfalsifiable on the machine that matters" defect as I-1 itself, one level deeper. Fix is one
  word: a POSIX TZ string (`JST-9`) needs no zoneinfo files (verified: `XXX-9`/`JST-9` -> +09:00,
  `Not/AZone` -> silent +00:00).
Task 8: CONTROLLER RULING — the re-reviewer recommended closing and carrying R-1/R-2 forward;
  OVERRIDDEN into a small round 2. Five lines total, test-only, no production change, and each
  retires a NAMED LIVE SURVIVOR. R-2 especially: carrying a guard that silently evaporates on the
  CI image is exactly the debt this plan keeps paying for, and it protects the very fix that made
  round 1 worth running. Round 2 must DEMONSTRATE that the old `Asia/Tokyo` value would have
  survived under a simulated tzdata-less host, not merely assert it.
Task 8: fix round 2/5 dispatched (`fix-task-8-round2-s48`, Opus 5) on BASE `048b50c`, scoped to
  `publish_test.go` + `publishhelpers_test.go` ONLY, with S9-S12 explicitly OFF LIMITS.
Task 8: fix round 2/5 COMMITTED as `2963819` ("test(gitops): make the credential guard's placement
  falsifiable and pin the zone without zoneinfo"), TEST-ONLY (+23/-4 across publish_test.go and
  publishhelpers_test.go; `git diff 048b50c..2963819` contains NO production file at all, which is
  what proves every deferred item untouched). CONTROLLER-VERIFIED: `task backend:ci` exit 0 — nine
  lanes, 56 `ok`, zero FAIL/panic/DATA RACE, zero docker errors, stamped `Version=2963819` — plus
  `task contracts:check` exit 0, clean tree.
Task 8: R-2 CLOSED. `pinGitLocalZone(t, "JST-9")` (POSIX TZ string, no zoneinfo needed), with a
  constraint sentence on the helper's doc so the next caller cannot reintroduce a zone NAME. The
  survival demonstration is a clean controlled experiment: same code, same mutant, same host TZ,
  the ONLY variable being whether the zone name resolves — resolvable name -> mutant dies,
  unresolvable name -> mutant SURVIVES silently at exit 0. HONEST LIMIT, disclosed by the fixer:
  it could not produce a genuinely tzdata-less host (`TZDIR` is a glibc extension macOS libc
  IGNORES — verified as a no-op), so the survival case substitutes an unresolvable zone name as a
  stand-in. Faithful to the mechanism, not the literal image.
Task 8: R-1 CLOSED, and >>> THE RE-REVIEW'S OWN REMEDY WAS WRONG AS WRITTEN. <<< Its second
  assertion used an UNPATTERNED `branch --list`, which is never empty on a fresh clone (the
  default branch is checked out), so it fails on UNMUTATED code — `the checkout already carries
  "* main"`. Committed form scopes it to `caesar/*`. Same falsifier, correct baseline. This is the
  THIRD consecutive round in which the round's own prescribed remedy turned out to be a survivor
  or simply broken; treat every prescribed fix as a hypothesis to be mutated, never as a patch to
  be applied on trust.
Task 8: the two new assertions are proven non-redundant by a discriminating mutant — guard moved
  to the END of publishPrepare fires BOTH (the patch-file stat and the branch list); guard moved
  to just past the checkout but BEFORE the apply fires ONLY the branch list, because at that
  placement the refusal still precedes the write. The test's name now matches what it proves.
Task 8: CONTROLLER'S OWN MUTATION CHECK (not delegated) — applied the no-zone mutant to
  `publish.go:274` directly, ran the date test, watched it FAIL rendering `+09:00` on a host whose
  local zone is `+0400` (so the JST-9 pin, not the host, produces the kill), restored from backup,
  confirmed byte-identical and green, tree clean at `2963819`. Direct evidence, not a report.
Task 8: ✅ COMPLETE — commits `a8543fc` (implementation) + `048b50c` (fix round 1) + `2963819`
  (fix round 2). Review was SPEC PASS / QUALITY Changes-requested; both rounds re-verified. Still
  deliberately open and NOT to be chased: survivors S9 (safe by design — the ls-remote probe is
  never authoritative for a write), S10 (low consequence), S11 (untestable by construction), S12
  (name allow-list, blind to `assertNoCredentialConfig`'s own `config` call which uses `runGit`
  not `runGitFn`); plus Task 7's 6.2 and M7-1..M7-8, `checkCloneURL`'s
  `CodeStagingVerificationFailed` on a publish, and `remoteBranchHead`'s dead `sha == ""` fallback.
  >>> M7-5 AND M7-6 STILL GO TO THE FINAL WHOLE-BRANCH REVIEW. <<<
Task 9: brief GENERATED (`task-9-brief.md`, 1,596 lines = plan 5215-6810, the largest on the plan)
  and PRE-FLIGHTED (`task-9-preflight.md`, 807 lines) — **7 BLOCKER, 13 CORRECTION, 9 NOTE**, the
  biggest pre-flight yet. It went empirical: scratch copy of `backend/`, ran `sqlc`, built the
  analyzers, ran the brief's unit test.
Task 9: >>> THE PRE-FLIGHT ANSWERED THE INVARIANT QUESTION AND FOUND TWO REAL DESIGN HOLES. <<<
  All four predicates re-read at HEAD and confirmed (`BumpRunGeneration` queries.sql:391-397,
  `FinishGatePhase` :314-331, `RunIsWaitingOnAGate` :482-511, `ClaimPhase`'s `r.status='running'`
  at :124). Findings: (a) no new phase row, BUT a live publish *effect* the predicate structurally
  cannot see — and the plan survives only because `CountSettledPublications` counts `'pending'`,
  which makes carry-forward #3 NOT a naming curiosity but THE SOLE ENFORCEMENT of the invariant
  across the publish boundary (and it is unfalsifiable by the whole suite). Task 9 does not weaken
  it; it makes it load-bearing. (b) No — Task 9's only status writes go through the same
  `syncWaiting`/`finishRunTx` the agentic path uses. (c) >>> A REAL HOLE <<<
  `ConsumeAuthorization` (queries.sql:375-381) carries `wall_deadline > now()` and NO run-status
  predicate, and `authorizePublish` reads no run row — so a run terminalized to `failed` by a
  SIBLING node between the claim and the authorize still lets the intent commit and the push land,
  and `ListExpiredRunIDs` matches only ('pending','running','waiting') so that orphan intent and
  its branch are reconciled by NOTHING, EVER. Controller independently verified all three SQL
  facts before ruling. Fix: `GetRunForUpdate` + status check at the top of the authorize tx
  (`GetRunForUpdate` exists at queries.sql:46; `CodePublishNotStarted` exists and is UNCHARGED).
Task 9: BLOCKER 5 — >>> THE BRIEF'S OWN STATED INVARIANT WAS FALSE. <<< It promises every error
  leaving `runPublish` is transient and uncharged while an intent is live, but `reconcileAttempt`
  (orchestrator.go:693-697) hands EVERY lost lease to `o.exec.Inspect` with NO node-kind dispatch.
  A publish attempt ran no container, so it lands on `reconcile_lost` — which IS CHARGEABLE
  (outcome.go:82-91 exempts only rate_limited, credential_busy, publish_not_started,
  publish_resumable). Three lost leases exhaust the node -> permanent `attempts_exhausted` ->
  run failed -> every phase superseded -> a branch sits on the forge and nothing ever looks again.
  Reached by the one door `runPublish` does not guard, and the brief's own race test drives it.
  Fix: dispatch `KindGitPublish` to the UNCHARGED `CodePublishResumable`. All three facts verified
  by the controller at HEAD.
Task 9: G1 — the worst unfalsifiable guard on the plan so far: `revalidateAuthorization`, ~60
  lines and SIX checks, can have its ENTIRE BODY replaced with `return nil` and every prescribed
  test still passes. The brief's most argued-for code was its least tested. G2-G8 name seven more.
Task 9: implementer DONE — seven commits `8c8c6a0` (feat) + `b6d0895` (fix) + five test-hardening
  commits, HEAD `dec913c`, +3558/-119 across 32 files. CONTROLLER-VERIFIED: `task backend:ci`
  exit 0 — nine lanes, 56 `ok`, zero FAIL/panic/DATA RACE, zero docker errors, stamped
  `Version=dec913c` — plus `task contracts:check` exit 0 on a clean tree (which also proves the
  regenerated store, openapi.json and schema.d.ts are all committed).
Task 9: >>> THE HIGHEST-CONSEQUENCE FIND OF THE SESSION, AND NEITHER DOCUMENT CAUGHT IT: <<<
  `gitops.TypedError` is NOT `runs.OutcomeError`, and `runs.ClassifyOutcome` is DEFAULT-DENY. The
  brief's `gitPublisher.PushPatch` returned `gitops.Publish`'s error verbatim, so EVERY typed git
  outcome became a permanent `internal_error`: a conflicted marker never reached `maybeHalt` (the
  run died instead of halting for a human) and a routine transient `push_rejected` TERMINALIZED
  THE RUN. Found by two failing tests, not by reading. Fixed with `gitops.AsOutcomeError` (+10
  lines in gitops.go) called by BOTH adapters. Lesson: crossing a package boundary silently
  re-types outcomes, and a default-deny classifier turns that into a terminal failure.
Task 9: the implementer corrected BOTH documents three more times — (2) the pre-flight's N2 said
  `fake` importing `gitops` is legal; it is an IMPORT CYCLE via `gitops/stage_test.go`, fixed at
  the cause by having `stageFixture` build its own bare repo; (3) BLOCKER 7 named the author
  fields but MISSED THE COMMIT MESSAGE, which has the identical mutable-source defect
  (`auth.ApproverEmail` joined from the mutable `users` row and hashed into the sha) — found
  because the test written to falsify the author fix went red for the MESSAGE; fixed with two new
  NOT NULL columns + two atlas/goose migrations so `pushPublication` replays only immutable
  snapshot data; (4) the pre-flight's "G-good" claim that `Date: intent.CreatedAt` was already
  falsifiable was WRONG — RFC3339 has SECOND resolution, so a crash-and-retry inside the same
  second rebuilds the same sha and the mutant SURVIVED until the test was made to cross a second
  boundary (`2642024`).
Task 9: that is now FOUR CONSECUTIVE ROUNDS in which a prescribed remedy was itself wrong. The
  rule stands and is getting stronger: a prescribed remedy is a HYPOTHESIS TO BE MUTATED, never a
  patch to be applied on trust — and that now demonstrably applies to PRE-FLIGHT findings too, not
  just review findings.
Task 9: review dispatched (`review-task-9-s48`, Opus 5) on `2963819..dec913c`, pointed at the
  invariant, both design-hole fixes, independent verification of the four "the documents were
  wrong" claims (especially whether `AsOutcomeError`'s conversion is COMPLETE for every outcome
  gitops can produce), and naming the guards the suite still would not kill.
Task 9: review — **SPEC PASS, QUALITY Approved-with-findings**, 3 Important + 10 Minor, 0 Critical.
  Reviewer's own summary: "the task is done, and done well" — all 15 brief steps landed, all 9
  prescribed tests exist, six more added, both pre-flight design holes closed by code a mutant
  genuinely kills. All three Importants were the SAME defect: `runPublish`'s doc comment states an
  absolute invariant ("while an intent is pending/pushed, every error leaving this function is
  TRANSIENT and UNCHARGED") and there were FOUR doors violating it. The implementer invented the
  typing discipline that enforces it and then missed exits. I1: three untyped exits (default-deny
  -> permanent internal_error -> run terminalized), worst being `haltPublication` returning its tx
  error raw while the intent is `pushed` (branch live on the forge). I2: budget exhaustion is a
  fourth door. I3: `unchargedWhileUnfinished` unfalsifiable at the PUSH call site — the exact leg
  BLOCKER 5 was about.
Task 9: CONTROLLER RULING on I2 — took the one-line exemption over widening Task 10's settlement.
  The precedent sits on the same line (`CodeCredentialBusy` is already exempt) and its own comment
  gives the principle: "it charges nothing, so demoting its requeue to budget_exhausted would kill
  a run for time it never spent". The overrun permitted is bounded by the wall deadline, which
  settles properly; the alternative orphans branches. Minors M1-M9 folded in; M10 explicitly
  BARRED (the migrations are hashed in atlas.sum — editing them breaks contracts:check) and
  recorded as a Task 12 carry-forward instead.
Task 9: fix round 1 COMMITTED as `903235f` ("fix(runs): type every exit that leaves a publication
  unfinished"), +818/-64 across 12 files. CONTROLLER-VERIFIED: backend:ci exit 0, nine lanes, 56
  `ok`, zero FAIL/panic/DATA RACE, stamped `Version=903235f`; contracts:check exit 0 clean.
  24 mutants: 21 killed, 2 deliberate survivors (each argued STRUCTURALLY UNREACHABLE, not merely
  untested), 1 failed to build and was rewritten.
Task 9: >>> THE ROUND'S REAL CONTRIBUTION — IT SOLVED THE ROOT PROBLEM, NOT THE THREE SYMPTOMS.
  <<< I1a, I1b, M6 and crash windows 8 and 10 were all one unanswerable question — what happens
  when a statement fails AFTER its effect landed on the forge — and the package had no way to ask
  it. It built a DATABASE-FAULT SEAM: `queryFaults`, a `pgx.QueryTracer` on the fixture pool that
  arms a failure BY SQLC QUERY NAME (`data.SQL` carries the `-- name: X` comment) and returns an
  already-cancelled context, so pgconn refuses the statement before writing anything, the
  connection survives and the tx rolls back normally. ~50 lines. The review had listed this as the
  highest-value thing TASK 10 could build. It makes I1a/I1b/M6 falsifiable AT THEIR CALL SITES
  rather than only in function bodies.
Task 9: fix round 1 found the review WRONG TWICE, and one is methodologically important.
  >>> THE REVIEW INFERRED BEHAVIOUR FROM A TIMING DELTA <<< (a package run going 31s->61s
  "meaning" halted intents got re-probed). The fixer showed a PROVABLY NO-OP mutant moved the same
  package 37s->67s and concluded "timing deltas in this package are not signal" — then proved the
  block was semantically redundant with the check at publish.go:359, DELETED it, and pinned the
  surviving check with a real test. Net: one guard fewer AND one guard newly falsifiable, where
  the review's remedy would have left two guards and zero tests. (Also: M4's prescribed mutant
  shape does not compile, and a test written to the review's letter would have SURVIVED.)
Task 9: re-review of fix round 1 — every finding closed, BOTH "the review was wrong" claims
  independently CONFIRMED, and the fault seam verified as real (incl. an 8-property `-race` probe
  of the tracer itself). But it blocked closure on ONE thing: >>> R1, A FOURTH EXTERNAL DOOR OF
  EXACTLY THE I2 SHAPE, REPRODUCED IN 239ms. <<< `evaluateFailure`'s park-past-deadline branch
  refuses to park a retry that would wake past the wall deadline and terminalizes instead; with
  `publish_resumable`'s backoff (floor 5s, base 15s, cap 5min) ANY uncharged publish failure
  within ~15s-5min of the deadline kills the run `failed` with the intent `pushed` while
  `wall_deadline > now()`, so `ListExpiredRunIDs` never matches it again and the branch is
  orphaned. EASIER TO HIT THAN I2: that needed a nearly-spent budget; this needs only a forge
  hiccup near the end of a run. The commit's central artefact — the invariant comment — asserted
  three doors and shut; the counter-example took 239ms.
Task 9: CONTROLLER RULING on R1 — took the behaviour change, not the comment-only option, for the
  same reason as I2. Safety argument verified by the controller in source: `ClaimPhase` already
  carries `AND r.wall_deadline > now()` (queries.sql:125), so a row parked past the deadline is
  ALREADY UNCLAIMABLE — the guard's stated reason is belt-and-braces the claim predicate makes
  redundant. Parking routes the run to the expiry sweep and Task 10's settlement instead of a
  `failed` run nothing revisits.
Task 9: fix round 2 COMMITTED as `8bdc7ee` ("fix(runs): park an unfinished publication past the
  wall deadline instead of terminalizing the run"), 5 files. CONTROLLER-VERIFIED: backend:ci exit
  0, nine lanes, 56 `ok`, zero FAIL/panic/DATA RACE, stamped `Version=8bdc7ee`; contracts:check
  exit 0 clean; no scratch files left. R1 closed (2 mutants, incl. one ON THE REMEDY ITSELF), the
  doc comment corrected, and R2 (crash window 10 — the last one the pre-flight enumerated) closed.
  GOOD JUDGEMENT WORTH KEEPING: it did NOT reuse `exemptFromBudgetPreCheck`. The two predicates
  ask different questions — the budget one asks "did this outcome spend the time the budget
  protects?", the new `exemptFromParkPastDeadline` asks "would terminalizing here strand an effect
  no sweep revisits?" — so `credential_busy` is in the first and deliberately NOT the second ("a
  run that runs out of wall clock waiting for someone else's lease is honestly dead"), pinned in
  both directions.
Task 9: CONTROLLER'S OWN MUTATION CHECK (not delegated) — neutered the R1 exemption at
  orchestrator.go:1452, ran `TestPublish_ADeadlineInsideTheBackoffParksRatherThanTerminalizing`
  under `-tags integration`, watched it FAIL ("the run died (failed) before publish reached
  attempt 3: its failures were charged"), restored from backup byte-identical, re-ran green, tree
  clean at `8bdc7ee`. Direct evidence, not a report.
Task 9: COMPLETE — commits `8c8c6a0` + `b6d0895` + five test-hardening commits (through `dec913c`)
  + `903235f` (fix round 1) + `8bdc7ee` (fix round 2). Ten of ten crash windows now enumerated,
  with 3, 5, 8 and 10 exercised.

## Task 10 — Reconciling a publication (BASE `8bdc7ee`)

Task 10: brief generated (`task-10-brief.md`, 866 lines).
Task 10: PRE-FLIGHT NOT CLEAN — **12 BLOCKER, 14 CORRECTION, 6 DESIGN QUESTION**, the worst on
  this plan, every finding backed by a command actually run (`task-10-preflight.md`). Method:
  `backend/` copied to scratch OUTSIDE the worktree, brief's Go pasted into a scratch file and
  compiled, brief's four SQL statements run through real `sqlc generate` (exit 0) and exercised
  against a real testcontainer Postgres. Worktree never written to; tree clean at `8bdc7ee`.
  Highlights: the entire test harness (`newSpineHarness` + ~22 `h.*`) does not exist and the
  package refuses to grow one; `commitMessageFor` has a different signature AND was made
  unnecessary by Task 9's stored `commit_message`; every nullable intent column is `pgtype`, not
  a pointer; Step 3's insertion point was already occupied by Task 9's early-returning publish
  branch; and TWO REAL DESIGN DEFECTS found before a line was written — a production wedge (B8)
  and a proven grace-skip (B9).
Task 10: NOT a defect, verified — the brief needs NO migration; `publish_intents.absent_since`
  already exists from Task 3, and all four new statements generate and behave as claimed.
Task 10: all rulings written to `task-10-overrides.md`, which SUPERSEDES the brief. Dispatches
  carry brief + overrides + preflight.
Task 10: **USER RULING (sofo) on D1** — BOUND the unverifiable case, against the plan's text. The
  plan mandates an unbounded defer when the forge cannot be reached; the pre-flight showed that
  means an immortal run (a long outage, or a credential revoked after the intent was created,
  which `gitPublisher.prepare` re-opens on every probe). Production wires `unavailableForge`,
  whose `BranchHead` errors unconditionally (`runsports.go:135`), so this arms the moment Plan 12
  lands a real forge. New constant `publishSettleUnverifiableAfter = 30m`, measured in SQL against
  `runs.wall_deadline` — NOT `updated_at` (the absent-stamp statements bump it) and NOT
  `created_at` (would halt on the first blip for a long-deadline run).
Task 10: CONTROLLER RULINGS, overridable — B8 fix by hoisting `fenceExpiredPhases` out of
  `settleRunCredentials` so both sweeps fence (one implementation) rather than making settlement
  fence separately; D2 probe cadence NOT throttled in this task (single-user scale; the expensive
  clone arm needs BranchHead to succeed AND the clone to fail) — DEFERRED to Plan 12's real forge;
  D5 run-outcome naming (`Class: transient` on a terminalized run) DEFERRED to Task 11, which owns
  the surface that renders it; C3's `head_sha` comparison for `pushed` intents NOT added (sentence
  deleted instead) — deferred minor.
Task 10: LEDGER CORRECTIONS from the pre-flight — the Task 9 handoff's claim that Task 10 makes
  `recoverBranch`'s receipt branch (G2) reachable is **FALSE**: settlement never calls
  `recoverBranch`, and `RecordPushReceipt` sets `status='pushed'` in the same statement as
  `head_sha`, so a driver reaching it always holds `HeadSha.Valid == false`. The comment at
  `publish.go:766-767` is wrong and this task corrects it. Likewise `CountSettledPublications` has
  NO status filter (`queries.sql:452`), so "on 'halted'" was never a distinct branch.
Task 10: IMPLEMENTED — 9 commits `8bdc7ee..400db32` (`304c287` statements, `f657465` fakes,
  `6474742` the state machine, `94d0633` the two sweeps' tests, `1619413`, `371e04c`, `77e656c`,
  `4200bb1`, `400db32`). CONTROLLER-VERIFIED DIRECTLY, not on the report: `task backend:ci` exit 0
  (nine lanes, 56 `ok`, zero FAIL/panic/DATA RACE, stamped `Version=400db32` = HEAD),
  `task contracts:check` exit 0, tree clean, no scratch files. NOTE: mid-run LSP diagnostics showed
  compile errors and three `zzprobe*` scratch files — both were a STALE snapshot of the agent's
  working state; the committed tree has neither. Verify, do not panic on diagnostics.
Task 10: ACCEPTED DEVIATION from override A8 — the implementer did NOT hoist
  `fenceExpiredPhases` into a whole-run pass, because doing so broke
  `TestOrchestrator_ExpiredRunWaitsOutASweeperThatWonTheFence` (`run status = dead, want running`):
  fencing everything up front takes custody of a phase a concurrent sweeper is mid-settlement on,
  reintroducing the exact ordering violation the sweep prevents. Shipped instead:
  `settleRunCredentials` → `settleExpiredPhases`, one per-phase loop kept, `o.credentials == nil`
  moved onto the CREDENTIAL CALL alone so fencing is unconditional in production, and `fencePhase`
  extracted as the single implementation with a TRI-STATE return (a phase that moved because it is
  over owes nothing; one another sweeper moved to `reconciling` must defer — a two-valued return
  reconciled a terminal phase's credential). The reviewer judged this independently and confirmed
  it delivers A8's substance. Mutant M13 pins it.
Task 10: review round 1 — **spec ✅ (A1-A12, C1-C14, D1-D6 applied), quality NOT APPROVED**, 2
  Important + 5 Minor (`task-10-review.md`). Independently confirmed: A8 deviation correct; lock
  ordering adds NO new deadlock surface (settlement never locks `runs`/`phase_runs`, and uses the
  same intent→counter→audit order as every existing publish transaction).
Task 10: >>> CONTROLLER'S OWN MUTATION CHECK, and it REFUTED BOTH THE REPORT AND THE REVIEW. <<<
  The report's survivor #4 said per-field mutants of `pushRequestFor` were unrun but "would die";
  the reviewer went further and STRUCK the item as "verified by reading that every field is covered
  indirectly". I measured it: mutating `Date: intent.CreatedAt` → `o.svc.clock.Now()` SURVIVES
  `TestExpiredRun_APushThatLandedIsRecordedBeforeTheRunDies` **5 runs out of 5** (ok 3.0-3.1s each);
  the only kill was a slower `-v` run. Restored byte-identical, re-ran green, tree clean. Cause:
  git's ONE-SECOND commit-date granularity — in the fixture, settlement rebuilds inside the same
  second the intent was created, so both timestamps yield the identical sha. In production they
  differ by minutes, so the PROPERTY is real; the GUARD only falsifies when the timing happens to
  cross a second boundary. This is the plan's recurring sin — *a falsifier that fails green* — and
  it sits on the most load-bearing publish invariant there is.
  **THE RULE THIS PROVES, AGAIN AND HARDER: reading is not measuring. A reviewer who "verified by
  reading" that a mutant would die has verified nothing.** Demand the run.
Task 10: fix round 1 dispatched to the original implementer (resumed) with FOUR findings: F1
  (`inspectPublication`'s error return charges the budget — a chargeable `reconcile_lost` on a DB
  read failure, the one spend this task exists to forbid), F2 (the headline test cannot see
  settlement PUSHING — swapping `ExpectedHead` for `PushPatch` adopts the branch and every
  assertion still holds; only `PushCount()` moves and nothing asserts it), F3 (the controller's
  Date-replay finding above), F4-cleanup (dead `ambiguous ||` at `publish.go:1363`).
  DELIBERATELY DEFERRED, do not let the loop grow: F3-review (the two late-delivery tests can stage
  nothing under a ~20ms interleaving — poll `absent_since`, which settlement alone writes, instead
  of a probe counter; PROMOTED into the round as it degrades toward a false pass), F4's grace half
  (the live twin behaves the same way; consistency beats a one-sided change), F5 (2-minute probe
  budget × 16 runs stalls the single janitor goroutine), F6 (settlement's receipts emit no
  event/audit/artifact unlike its halts), F7 (YAGNI notes). Point the final whole-branch review at
  F5 and F6 specifically.
Task 10: CORRECTION to the line above — fix round 1 carries **FIVE** findings, not four. The
  review's F3 (the two late-delivery tests can stage nothing) was promoted into the round and sent
  as an addendum; it is NOT deferred. Promoted above its Minor rating because it fails GREEN — it
  does not flake into red, it quietly stops testing the race it exists for, on the headline
  invariant. Remedy: poll `absent_since` (settlement is its only writer) instead of a probe
  counter, then re-run mutants M7 and M11 to confirm both still die.
Task 10: fix round 1/5 — **5 addressed, 0 open** (commits `5a891bc` F1 production fix, `002de46`
  F2/F3/F5 tests + F4 cleanup). Scoped re-review verdicted all five ADDRESSED with file:line
  evidence and found NO new Critical/Important breakage (`task-10-rereview.md`).
  CONTROLLER-VERIFIED DIRECTLY at `002de46`: `task backend:ci` exit 0 (nine lanes, 56 `ok`, zero
  FAIL/panic/DATA RACE, stamped `Version=002de46` = HEAD), `task contracts:check` exit 0, tree clean.
Task 10: >>> CONTROLLER'S OWN MUTATION CHECKS AT `002de46`, both decisive, neither delegated. <<<
  (1) The Date-replay mutant that SURVIVED 5/5 before the fix now DIES 3/3
  (`1597s/Date: intent.CreatedAt/Date: o.svc.clock.Now()/`). The repair is structural, not lucky:
  the fixture puts a 1500 ms gap between the DB-stamped `created_at` and the rebuild, and `gitops`
  formats commit dates at RFC3339 SECOND resolution (`runner/gitops/publish.go:274`), so any gap
  over one second changes the truncated second.
  (2) The re-review flagged that the `AuthorEmail` mutant could NOT be shown to have hit the
  rebuild, because `publish.go:702` (live push) and `:1596` (settlement rebuild) are BYTE-IDENTICAL
  and a live-path hit yields the identical red. I closed it by line number: mutating ONLY line 1596
  to a foreign author kills 2/2, line 702 untouched. Restored byte-identical, re-ran green.
  LESSON FOR ANY FUTURE STRING-MATCH MUTATION IN THIS FILE: the live path and the settlement
  rebuild carry identical literals, so a `sed` pattern proves nothing about which one it hit —
  ANCHOR ON THE LINE NUMBER.
Task 10: minor (deferred): the three new constants' VALUES have no falsifier — `publishSettleGrace`
  (a 1-second grace, shorter than an HTTP request, survives), `publishSettleUnverifiableAfter` (a
  5-minute bound, which would halt over ordinary outages, survives), and `publishSettleProbeBudget`.
Task 10: minor (deferred): `probeCtx`'s timeout — the A12 janitor-blocking fix — is untested;
  nothing wedges `ExpectedHead`, so deleting the `context.WithTimeout` survives.
Task 10: minor (deferred): `BaseSHA`/`TreeHash`/`Patch` rebuild mutants kill only through a ~93s
  TIMEOUT ("run never reached dead"), not a wrong-record assertion, because those mutations make
  `ExpectedHead` ERROR rather than mismatch. Real kills, blunt evidence. The re-review's judgment,
  which I accept: the recompute mutants for those three fields are genuinely EQUIVALENT
  (`row.BaseSha`/`row.TreeHash` are immutable copies; `Patch` comes from the artifact on both
  paths), so the falsifiable surface really is `Date`/`Message`/`AuthorEmail` — and all three now
  die through the assertion.
Task 10: minor (deferred): settlement's foreign-PR halt arm is an untested survivor; its live twin
  is covered (`TestPublish_AForeignPullRequestOnOurMarkerHaltsForAHuman`). Also F5's fix leaves the
  F2 test racing a `publish_resumable`-parked successor with ~3.4s slack (false-RED only, never a
  false green), and F3's second-boundary guarantee spends 500ms of its 1500ms on DB-vs-host clock
  skew (testcontainers Postgres vs `clock.System{}`).
Task 10: COMPLETE (commits `8bdc7ee..002de46`, 11 commits, review clean after one fix round).
  WHAT TASK 11 INHERITS: (a) D5 was deferred TO TASK 11 — a run that dies over a settled publication
  still records `Class: transient` (`orchestrator.go:1520-1522`), which reads as "come back later"
  on a run nothing will revisit; Task 11 owns the surface that renders run outcomes, and Task 10
  made this death routine. (b) Task 6's carry-forward still stands: `GateDecisionResult.Generation`
  means the generation DECIDED on approve/reject and the NEW generation on `request_changes`, and a
  concurrent revision on a SIBLING gate currently reports "the run's wall deadline has passed" when
  it has not, because `BumpRunGeneration` has two predicates and every empty result maps to one
  message — Task 11 owns the surface that renders that lie. (c) The ledger's Task-9 claim that
  Task 10 makes `recoverBranch`'s receipt branch (G2) reachable was FALSE and is now corrected in
  source; `CountSettledPublications` has no status filter, so "on 'halted'" was never a branch.

## Task 12 — App wiring: the real forge (BASE `3b0cabf` — pre-flight IN FLIGHT, session 51)

Task 12: brief generated (`task-12-brief.md`, 386 lines). Plan lines 7880-8276. Deletes
  `unavailableForge`, puts a real `forgeAdapter` over `platform/forge` + `assets.UseCredential`
  in its place, rewrites `cloneURL` to read configured clone bases, adds a `config.Forge` block,
  grows the `runs` constructors.
Task 12: pre-flight DISPATCHED — `preflight-task-12-s51` (Opus 5), told the worktree is
  READ-ONLY (scratch copy outside it; Tasks 11 and 12 are separated by nothing but this
  pre-flight and the tree must stay at `3b0cabf`). Pointed explicitly at five carry-forwards:
  (1) the `Forge`-port/`AsOutcomeError` hole — **this task is what makes it reachable**;
  (2) Task 7's two live forge-client defects (unquoted `rel=next` truncation, off-host guard
  comparing raw string prefixes not parsed hosts) which this task puts on the production path
  against possibly self-hosted base URLs; (3) the three `internal/platform` arch rules;
  (4) Task 11's new `Errors []int` seam, not to be reinvented; (5) Task 9's still-open R3, R5,
  R6, M7, M10. Told to run `go build -gcflags=-e` for any blast radius, since plain `go vet`
  hides all but one error per package — the exact trap that made Task 11's brief say "three call
  sites" when there were 28.

Task 12: pre-flight COMPLETE — **8 BLOCKER, 11 CORRECTION, 5 DESIGN QUESTION**
  (`task-12-preflight.md`). All rulings written to `task-12-overrides.md`, incl. sofo's
  **D4** (halt on proven-permanent, retry the rest; the brief's shape REJECTED because it
  can strand a pushed branch) and **A/B1** (the brief invents its fourth harness in a row —
  new integration-tagged file instead).
Task 12: IMPLEMENTER DISPATCHED (session 52, Opus 5, BASE `3b0cabf`) with all three
  absolute paths, overrides declared as superseding the brief, and `task-12-report.md`
  named as the authoritative channel.
Task 12: implementer returned DONE_WITH_CONCERNS at `1399bbd` — 6 commits
  (`6bf346f` rel=next, `d6b72e7` halt + port contract, `1d17aa4` app wiring,
  `f7fc814` + `9ed25fd` tests, `1399bbd` comment). Four concerns, incl. one decision
  taken BEYOND the rulings: D4 named no outcome code for `ErrNotFound`, so it added
  `runs.CodeRepoUnavailable` ("repo_unavailable").
Task 12: CONTROLLER VERIFICATION at `1399bbd` (not the implementer's table):
  - `scripts/gate` **PASS** — 9 lanes, `Commit=1399bbd` stamped, contracts clean.
  - `go build -gcflags=-e ./...` exit 0 and `go vet -tags 'integration
    runnerintegration property' ./...` exit 0. **The editor showed 11 compile errors
    at this SHA and every one was a stale gopls index — FIFTH time on this plan.**
  - **Decisive mutation run by the controller, not argued:** narrowing
    `haltsPublication` (`publish.go:995`) back to `CodePublishConflict` alone —
    i.e. the pre-D4 shape sofo rejected — makes
    `TestPublish_APermanentForgeRefusalHaltsRatherThanStrandingThePushedBranch` go
    RED **on the halt assertion**, printing the rejected behaviour verbatim:
    `outcome = {Class:permanent Code:auth_expired ...}` with the branch pushed.
    Reverted; GREEN; tree clean. D4 is genuinely pinned, not merely asserted.
  - The new code's justification was checked rather than taken on trust:
    `publish.go:876` really does branch on `CodePublishConflict` to RE-PROBE (a
    racing driver may have opened the PR), so reusing that code for a deleted repo
    would route a permanent failure into a recovery path. Outcome codes carry no
    contract surface — no DB CHECK, no TS union, no OpenAPI enum — so the addition
    is Go-local. **Still a vocabulary addition the plan did not authorize: surfaced
    to sofo, not buried.**
Task 12: >>> **USER RULING (sofo, session 52): KEEP `runs.CodeRepoUnavailable`.** <<<
  D4 named no outcome code for `ErrNotFound`; the implementer added
  `repo_unavailable` and flagged it as its one step beyond the rulings. Ruled IN,
  on the controller's recommendation. Grounds, all checked rather than argued:
  reusing `publish_conflict` would route a deleted repo into `publish.go:876`'s
  RE-PROBE branch (which exists for a racing driver, not a dead repo);
  `internal_error` and `auth_expired` both mislead the human who reads the halt;
  and the vocabulary has no contract surface (no DB CHECK, no TS union, no OpenAPI
  enum), which is why `contracts:check` stayed green. It is permanent-only
  (`runsports.go:346` is its sole producer), so it needs no `retrySchedules` row —
  those bind transients.
Task 12: **CARRY-FORWARD FOR TASK 15 (docs) — two lines, both now wrong in the plan:**
  1. Plan **line 45, decision 14** says "Publish failures split THREE ways" and
     names `publish_conflict` as the only permanent/halting one. It is now FOUR,
     with `repo_unavailable` as the second halting code and `auth_expired` (a Plan
     07 code) reused as a third — D4 widened `haltsPublication` past what decision
     14 describes.
  2. `outcome.go`'s Plan 08 block gained a code the plan's Task 1 list does not
     have. Task 15 verifies docs against code; this is the diff it must find.
Task 12: REVIEWER DISPATCHED (Opus 5) with brief + overrides + report + the
  `3b0cabf..1399bbd` package, told it MAY run targeted tests and mutants (the first
  reviewer on this plan cleared to do so was Task 11's) and must leave the tree clean.
Task 12: REVIEW — **spec ✅ COMPLIANT** (both load-bearing rulings landed), quality
  "Approved with fixes". **0 Critical, 2 Important, 5 Minor** (`task-12-review.md`).
  The reviewer ran its own mutants and left the tree clean. Its cross-cutting checks:
  forge sentinels are plain `errors.New` so `intakeError`'s `*xerrors.Error`
  short-circuit cannot swallow them; gitops emits neither `auth_expired` nor
  `repo_unavailable`, so widening `haltsPublication` changes nothing at the push call
  site; and `CodeRepoUnavailable` introduces no contract drift.
Task 12: **the two Important findings, both real (I verified #1 in the source myself):**
  1. `publish.go:1106-1108` — `haltPublication` stamps `Code: CodePublishConflict` on
     EVERY halt. `maybeHalt` passes `out.Message` and DROPS `out.Code`, so a halt for a
     revoked token or a deleted repo reports `publish_conflict` through the API
     (`api/dto.go:246-250`) and `auth_expired` never reaches durable state — only the
     prose reason carries it. Undoes D5's own rationale. **The e2e test at
     `publish_integration_test.go:1120-1123` currently PINS the wrong behaviour** and
     must flip to asserting `auth_expired`, keeping a test that the conflict paths
     still report `publish_conflict`.
  2. `serve.go:107-110` — the `config.Forge` → `forgeEndpoints` mapping has ZERO
     coverage, proven by a mutant: swapping the API bases into the clone slots left the
     whole `internal/app` integration package green. That is the silent self-hosted
     failure three doc comments describe.
Task 12: fix round 1/5 DISPATCHED — resumed the original implementer with both
  Importants plus ONE upgraded Minor: `repo_unavailable`'s halt is pinned only by
  `TestMaybeHalt_…`'s positive loop, which asserts `haltsPublication(code)` for the same
  three constants the switch names and never calls `maybeHalt` — unfalsifiable in its own
  fixture, and sofo's D4 says "Pin this". Minors 4-7 deferred, named explicitly as
  out-of-round.
Task 12: minor (deferred): `open`'s credential leg rides `publish_not_started`'s
  `{floor:0, base:0, cap:0}` schedule, so a credential deleted mid-run — a DETERMINISTIC
  failure — re-drives at poll cadence with no backoff and no charge until the wall
  deadline. Task 9 chose that schedule for a cause that does not repeat. Remedy: give the
  credential leg `resumable`'s 5s/15s/5min curve, or `publish_not_started` a nonzero floor.
Task 12: minor (deferred): `platform/forge/forge.go:274` folds 422 and 409 alike onto
  `ErrConflict`, so a NON-duplicate GitHub 422 ("base is invalid") takes `publish.go:876`'s
  adopt branch, finds nothing, and retries to the wall deadline for a condition no retry
  fixes. Inherited from Task 7; Task 12 is what makes it reachable — the same argument
  that put C8 in scope. Belongs in the platform client.
Task 12: minor (deferred): `runsports.go` is now 495 lines (from ~236) holding five
  adapters and two error vocabularies; the forge adapter + `intakeError`/`publishError`
  (`:113-357`) is a self-contained ~240-line unit that wants to be
  `internal/app/forgeadapter.go` — the test file is already named that way.
Task 12: minor (deferred): `intakeError` answers 429 for a failed dial
  (`runsports.go:288-293`) — checked, and COMPLIANT with D1 (`problemShapes` has no 503).
Task 12: fix round 1/5 (3 addressed, 0 open — halt code per cause; config→endpoints
  mapping covered; `repo_unavailable` halt made falsifiable; commits `1399bbd`..`00bbd94`
  = `f6324c4`, `a80a36a`, `00bbd94`).
Task 12: CONTROLLER GATE at `00bbd94` — **PASS**, 9 lanes, `Commit=00bbd94` stamped,
  contracts clean. `haltPublication` now takes the code: `:869`, `:877`, `:1060` pass
  `CodePublishConflict` explicitly and `maybeHalt:1024` passes `out.Code`.
Task 12: >>> **THE METHOD EARNED ITS KEEP AGAIN, and this one generalizes.** <<< The
  per-call-site mutation rule found a gap the PRESCRIPTION did not name: relabelling
  `publish.go:869` (the ambiguous-candidates halt) **survived** the first run, because
  while every halt stamped one constant nothing COULD distinguish `:869` from `:877`.
  The fix itself made a wrong code there newly possible — a defect that did not exist
  before the round and that the findings list could not have predicted. Closed with
  `TestPublish_AnUnprovableRequestOnOurMarkerHaltsForAHuman`. **A fix that widens a
  vocabulary widens what can be wrong; re-mutate every call site AFTER the fix, not
  just the ones the finding named.**
Task 12: implementer process note worth keeping: its first mutation run destroyed an
  UNCOMMITTED extraction (`git checkout --` discards real work as readily as a mutant).
  It re-applied, committed, and re-ran every mutation from a committed baseline.
  **Mutate only from a committed tree.**
Task 12: SCOPED RE-REVIEW DISPATCHED (Opus 5) over `1399bbd..00bbd94` with the three
  findings and both self-graded claims called out for independent judgement:
  the four-call-site falsifiability claim, and `haltedOutcome` deliberately keeping
  `publish_conflict` for an attempt ARRIVING at an already-halted intent.
Task 12: RE-REVIEW — **all three ADDRESSED, no new breakage, tree clean.** It did NOT
  take the report on trust: it re-ran round 1's surviving `serve.go` mutant (now dies on
  three assertions) and mutated each `haltPublication` call site SEPARATELY against the
  same four-test set — `:869` kills only the ambiguous-marker test, `:877` only the
  foreign-PR test, `:1060` only the create-conflict test, `:1024` only both legs of the
  permanent-refusal table. Every other test stayed green in each run. That is what
  "independently falsifiable" means, and it was measured, not argued.
Task 12: `haltedOutcome` keeping `publish_conflict` — RULED SOUND by the re-review (one
  of the two options the original review itself offered). Both its call sites answer an
  attempt ARRIVING at an already-halted intent; the causing code reaches the run through
  the halting attempt's own outcome, and `publish_intents` carries only free-text
  `halt_reason`.
Task 12: minor (deferred): if the halting attempt dies BEFORE recording its outcome, the
  reconcile path reports `publish_conflict` for what was an `auth_expired` halt. Named by
  the re-review, non-blocking. Closing it needs a column the row does not have.
Task 12: **complete (commits `3b0cabf`..`00bbd94`, 9 commits, review clean after one fix
  round).** Controller-gated at BOTH heads (`1399bbd` and `00bbd94`): 9 lanes,
  `Commit=<sha>` stamped, contracts clean, tree clean.

## Task 13 — The golden set (BASE `00bbd94` — pre-flighted session 52)

Task 13: brief generated (`task-13-brief.md`, 252 lines; plan lines 8276-8528).
Task 13: PRE-FLIGHT NOT CLEAN — **5 BLOCKER, 14 CORRECTION, 3 DESIGN QUESTION**
  (`task-13-preflight.md`). It did not merely read: it BUILT the alternative seam and
  MEASURED the brief's numbers in a scratch copy. Worktree verified clean at `00bbd94`.
  1. **Step 3's whole fault-injection seam is unnecessary and its justification is
     factually FALSE.** The brief says a pool wrapper is impossible because "the pool
     type is concrete throughout" — but the seam is a `pgx.QueryTracer` on
     `pgxpool.Config.ConnConfig.Tracer`, it ALREADY EXISTS as `queryFaults`
     (`internal/runs/testutil_integration_test.go:21-94`), and six tests use it. The
     pre-flight lifted it into `internal/app` and drove all five crash cases with
     **ZERO production lines**. Both of the brief's analyzer arguments are aimed at
     nothing: `stubfake` lives in `internal/analyzers/stubfake/` (not `internal/arch/`)
     and only flags string literals containing "not implemented"; `deadconfig` only ever
     checks `platform/config.Config`, never `runs.Params`.
  2. **`wantPushes: 1` is measurably wrong in two of five cases** — measured 8
     (post-push) and 2 (push-receipt). `CrashAfterPush` is STICKY and `p.pushes++` fires
     before the crash flag is read. `CreatedCount()` is 1 in all five: that is the real
     property. `g.disarm()` also cannot have a zero-argument signature across three
     different arming mechanisms.
  3. **The event sequence is 29 events, not 12**, and an approve that authorizes a
     publish emits `gate.decided` TWICE (`gates.go:265`). The brief's list is right as a
     SUBSEQUENCE — every element, correct relative order — but "in order and complete"
     is unassertable.
  4. `countAuthorizations(valid) == 1` in the intent-crash test is UNOBSERVABLE —
     measured 0, because `publish_not_started`'s `{0,0,0}` schedule requeues before any
     poller can read. (Same schedule as Task 12's deferred minor 4.)
  5. **Task 11's moved run-projection tests (C13/C14) are entirely absent from Step 2.**
     The fixture DOES drive the identical arc (verified: two gate phases, one per
     generation, different examined artifacts), so they drop in verbatim.
Task 13: pre-flight CONFIRMED CORRECT (do not second-guess): artifact counts
  `{plan:1, patch:3, review_verdict:2, gate_note:1, publication:1}` measured exactly;
  every event-kind constant; `gate.decided`/`gate_decision` AUDIT = 2; every DTO field;
  the rejected-gate test in every particular; `countIntents`/`consumed` = 1 in all five
  crash cases; `-run TestGoldenSet` matches today. Corrected command:
  `PKG=./internal/app/ task backend:test:integration -- -run 'TestGoldenSet' -v`
  (the brief's `-race` is doubled by the lane; the missing `PKG` spins a Postgres per
  package). Expect NINE `--- PASS` lines.
Task 13: IMPLEMENTER DISPATCHED (session 53, Opus 5, agent `impl-task-13` — name came
  back un-suffixed, no collision). **BASE = `f5dab73`** (real HEAD, not `00bbd94`: the
  ledger-snapshot commit sits on top and the review package needs the true base).
  Dispatched with all three absolute paths, overrides named as BINDING over the brief,
  and `task-13-report.md` named as the authoritative channel.
Task 13: implementer DONE — 4 commits `f5dab73`..`f400152`, tree clean.
  `81c098d` moves `queryFaults` → `internal/platform/testdb/faults.go` (exported
  `testdb.QueryFaults`, `//go:build integration || runnerintegration`), `c5a873d` the
  golden set, `fd9a6b1` a gofumpt fix, `f400152` self-review additions.
  **ZERO production Go lines** — the diff is `_test.go` files plus that one new platform
  test-only file. 920 insertions / 107 deletions across 5 files.
Task 13: **GATE PASS at `f400152`** — controller-run `./scripts/gate`, 9 lanes,
  `buildinfo.Commit=f400152` stamped, contracts clean, tree clean. Not taken on the
  implementer's report.
Task 13: the implementer's own first `backend:ci` came back **exit 201 on
  `backend:fmt:check`** (gofumpt wanted the crash case table exploded) — fixed in
  `fd9a6b1`. Recording it: that is exactly the lane a hand-picked test list misses.
Task 13: eight mutations reported (M1-M8), including **mutant S14 discharged** —
  `orchestrator.go:1182` generation → 1 kills `FullConveyor` via the approval inbox never
  showing a generation-2 gate. The S14 ledger line is `progress.md:2697`, NOT the `:2632`
  the brief cites (that is a different Task 11 entry).
Task 13: **the pre-flight's DESIGN QUESTION 3 remedy was itself a survivor** — the one
  block in that document that was recommended rather than measured. Asserting
  `publish.pr_opened == 1` measures **0** in the two PR-adjacent crash cases, because
  `publish.go:915-917` emits `publish.adopted` on a recovered attempt. Replaced with a
  count of events carrying `pr_number`. The standing lesson, landing again.
Task 13: REVIEWER DISPATCHED (Opus 5, agent `review-task-13`, un-suffixed) with the
  overrides named as binding, the `PKG=` targeted-run form, and an explicit mandate to
  RUN and MUTATE rather than verify by reading.
Task 13: REVIEW = spec ✅ / quality **NEEDS FIXES** (`task-13-review.md`). 0 Critical,
  2 Important, 5 Minor. The reviewer independently reproduced M1, M2 and M3 from the
  implementer's table (same failure text, same line numbers) and confirmed §4's
  survivor finding. It left the tree clean at `f400152`.
  **Important 1 — the one that matters. Two of the five crash cases pass with the crash
  injection DISABLED**, measured: turning `fake/forge.go:230` and `fake/publisher.go:162`
  into no-ops leaves `post-push` and `post-pr-create` GREEN. The `fired` predicate exists
  by its own comment to prevent exactly this, and it does the job for the three
  query-fault cases (`Fired()` is a true counter) but NOT for the two sticky-switch cases:
  `PushCount() >= 1` / `CreateAttemptCount() >= 1` count the EFFECT, and both increment
  before the crash flag is read. Nothing else in the case body separates the two worlds.
  Remedy is MEASURED not prescribed: a per-case `wantAdopted` asserting `publish.adopted`
  (>=1 in the four cases that reach the forge, 0 for `pre-db-commit-on-intent`).
  **Important 2 — PARKED BY sofo's RULING (2026-08-09).** `newGoldenEnv` duplicates ~55
  lines of the seed test's world-staging. Ruling: DEFER. Task 13's job is clean acceptance
  evidence, and `TestGoldenSet_MonthOneLoopThroughRealRoutes` is the ONLY cover for the
  synthetic patch shape (`fake/executor.go:150-163`), which it gets precisely by NOT
  wiring a remote. The reviewer's remedy (a helper taking document + wire-remote flag +
  members) is recorded for the whole-branch review to triage. Both agents split on this
  and both routed it upward; the ruling went the implementer's way.
Task 13: minor (deferred): `wantCreates`/`wantPRs` are `1,1` in all five rows — constants
  pretending to be data; folded into the fix dispatch as optional.
Task 13: minor (deferred): the uniform `time.Sleep(1100ms)` is load-bearing only in
  `post-push` (M2 reproduces there alone) and costs ~4.4s across the other four. Kept
  deliberately as insurance if another case becomes sticky. Cost is now a known one.
Task 13: >>> **W1 — ROUTED TO THE WHOLE-BRANCH REVIEW, NOT TASK 13's TO FIX.** <<<
  The pre-existing `TestPublish_CrashAfterPRCreateAdoptsTheExistingPullRequest`
  (`publish_integration_test.go:626`, **Task 9's file**) SURVIVES the same no-op mutation —
  it passes with `CrashAfterPR` doing nothing. Its sibling push test kills it. So the
  post-PR-create window is currently pinned by **NOTHING IN THE REPOSITORY**: neither the
  service-level test nor the golden set's case notices if the crash stops happening.
  This is a live example of the plan's own lesson — a guard tested only green asserts
  nothing. Whoever runs the final review: mutate BOTH, not just the one named here.
Task 13: FIX ROUND 1/5 DISPATCHED — resumed `impl-task-13` (rounds 1-3 resume the
  original implementer; its context is intact). Sent Important 1 + three one-line minors,
  with the parked item and W1 explicitly marked do-not-touch. Acceptance criterion stated
  as the mutation, not the green suite: all five cases must go RED when the injection is
  disabled.
Task 13: fix round 1/5 — 1 Important addressed + 4 minors, commit `d02ce46` (still ZERO
  production lines). `wantCreates`/`wantPRs` dropped for literals; the struct's earning
  field is now `wantAdopted int` = `1,1,1,1,0`. The `fired` comment now says out loud
  that it is a PROXY for the two sticky switches and names `wantAdopted` as the real
  discriminator. Minors 4/5/6 all fixed in code (`delete(q.fired, name)` on both arm
  paths; `countAudit` documented as database-wide with what would break it;
  `OpenPRCount() == 1` added to the full-conveyor test).
Task 13: **the implementer improved on the prescribed remedy in two ways.** (a) It
  strengthened the reviewer's `bool` (">= 1") to an exact `int`, measured stable over
  three consecutive runs — an exact count is a strictly better falsifier. (b) **It noticed
  the reviewer's mutation only exercises TWO of the five rows** and added M10 (disabling
  the `QueryFaults` seam in `testdb/faults.go`) so the other three are shown to die on
  `fired` too. M9 → `post-push` + `post-pr-create` FAIL on `publish.adopted = 0, want 1`;
  M10 → the three query-fault cases FAIL on the firing guard. Every row now dies when its
  OWN injection is disabled — which is the property the round was opened for, and it took
  two mutations to cover, not one.
Task 13: **GATE PASS at `d02ce46`** — controller-run, 9 lanes, `Commit=d02ce46` stamped,
  contracts clean. Second gate of this task; both taken by the controller, neither on a
  report.
Task 13: SCOPED RE-REVIEW DISPATCHED (Fable 5 — one-commit 11.6KB fix diff, mid tier per
  model selection; agent `rereview-task-13`, un-suffixed) over `f400152`..`d02ce46`, with
  the parked finding explicitly OUT of scope, M9 named as the check to re-run, and the
  bool→int strengthening flagged for an explicit soundness verdict.
Task 13: RE-REVIEW = **all findings addressed, no new Critical/Important breakage**
  (`task-13-rereview.md`). It re-ran M9 itself rather than reading the claim — 2 FAIL on
  the new assertion, 3 PASS, matching the report line-for-line — verified both lines
  matched before editing, reverted, tree clean. All three minors verdicted ADDRESSED.
Task 13: the re-reviewer upgraded the exact-count justification from empirical to
  **structural**: both adopted events are written in the same transaction as their
  stage's receipt (`publish.go:728-755`, `:919-940`), so a crashed attempt rolls its
  event back with the receipt; the receipt is fenced (`n == 0` → `errIntentAdvanced`), so
  each stage records at most once; sticky retries error out before the receipt tx and
  cannot inflate the count. The only path to a different number is a lease takeover, which
  this fixture does not stage. So exact-1 has no flake surface AND catches a double-emit
  regression that `>= 1` would not. Worth keeping as a method note: "measured stable three
  times" is weaker evidence than an argument from the transaction boundary.
Task 13: >>> **COMPLETE** (commits `f5dab73`..`d02ce46`, 5 commits, review clean after one
  fix round, 1 Important PARKED BY USER RULING). <<< Gated by the controller at BOTH heads
  it passed through (`f400152`, `d02ce46`), never on an implementer's report. ZERO
  production lines. The parked item and W1 both carry forward to the whole-branch review.

## Task 14 — Carry-forwards from Plans 04 and 05 (BASE `1a3da79` — session 53)

Task 14: brief generated (`task-14-brief.md`, 211 lines; plan lines 8529-8739).
Task 14: PRE-FLIGHT NOT CLEAN — **9 BLOCKER, 8 CORRECTION, 3 DESIGN QUESTION**
  (`task-14-preflight.md`, 976 lines). It reproduced ALL FOUR defects and FIXED all four
  in a scratch copy OUTSIDE the worktree, red-then-green, with the `runs`+`workflows`+`app`
  integration lanes green and `golangci-lint` clean after. Worktree verified clean at
  `1a3da79` before and after. **All four of the brief's diagnoses are sound; its Step 1
  test code and its Step 6 are what do not survive contact with the source.**
Task 14: >>> **`task backend:test:runner` IS GREEN AT `1a3da79`** <<< — measured, all four
  packages `ok`, exit 0, ~100s after `task image:build`. The lane that had not been run
  for several tasks and that `scripts/gate` does not assert has **no pre-existing red**.
  Task 14 inherits a green lane and adds a test to it.
  1. **`if won != 1` is UNSATISFIABLE — the brief's premise is wrong.** Measured `won=2`
     ten runs of ten at HEAD, and `2,2,3,2,2` WITH the brief's own Step 3+4 fix applied.
     Under READ COMMITTED a blocked `FOR UPDATE` re-evaluates the quals of the row it
     LOCKED (EPQ); it does NOT re-run `ORDER BY … LIMIT 1`. So a caller whose SELECT begins
     after the winner commits legitimately locks the winner's NEW pending run and supersedes
     it — a second winner, and correct. "Exactly one rerun wins" is not an invariant of this
     design. RULED: assert (a) no loser is an internal fault, (b) `runs_task_live_key` leaves
     exactly one live run. The Step 3 classification fix itself is right and stays.
  2. **Step 1's harness does not exist — ten identifiers, ZERO hits.** The real harness is
     `tasks_integration_test.go` + `testutil_integration_test.go`. No orchestrator is wired
     by `newService`, so waiting for `RunSucceeded` is unreachable — and unnecessary: both
     carry-forwards reproduce with a PENDING latest run (measured).
  3. **`TestValidate_DuplicateEdgeIsReportedOnceNotTwice` PASSES at HEAD** — twice over:
     `validateShape` already `continue`s after one duplicate violation, AND the second
     assertion greps `"at most 1 patch"`, which is the IMPLEMENT message; the fixture's
     review node emits `"review consumes exactly 1 patch"`. The loop matches nothing and
     the test is vacuous.
  4. `interface{ Fields() []xerrors.FieldError }` NEVER matches — `Fields` is a struct
     field, not a method, so `TestWorkflowName_LengthErrorSaysBytes` `t.Fatal`s
     unconditionally, before AND after the fix.
  5. `%s` on `xerrors.ClassOf(...)` is a BUILD failure (`type Class int`, no `String()`);
     `go test` runs a vet subset by default. Use `%d`.
  6. **`fake.Forge.Heads` does not exist** — the field is unexported, ALREADY mutex-guarded,
     and the setter already exists as `Head`. Every clause of that brief sentence is wrong.
  7. **`countAuditBySubject` duplicates Task 13's `countAudit` AND does not scope to a run**
     — `subject_kind` is a TYPE, not an identity. The brief's own comment claims the
     opposite of what its SQL does. Real fix: scope by `subject_id`.
  8. Replacing `countAudit`'s two call sites makes it dead → `unused` fires → **measured
     `task backend:lint` failure**, and lint is in `backend:ci`. Delete it in the same edit.
  9. The workflows tests are written for the wrong package (`package workflows` is an
     INTERNAL test package; qualified `workflows.X` references do not compile there).
Task 14: RULINGS — **D1 (a)** bare `isUniqueViolation`, consistent with four existing call
  sites; a constraint-name check would be the only one in the codebase. **D2 (b)** the
  `gate_note` test is the runner-lane end-to-end ALONE — the carry-forward's point is REAL
  staging, which a unit test cannot show, and a unit test would be a third assertion of a
  format string two tests already pin. **D3 (a)** scope AND tighten `n == 0` → `n == 1`; a
  scoped count that only asks "more than zero" throws away the precision scoping just bought.
Task 14: both ledger carry-forwards RULED IN SCOPE — the `gate_note`/`lineageInto` real-
  staging test (~45 lines, `//go:build runnerintegration`, near-clone of
  `lineage_runner_test.go:19-96`; **the ONE item the pre-flight sized but did NOT build**,
  so its shape is a proposal, not a measurement) and the `schema_integration_test.go` split
  (555 lines; boundary EXACT at line 228, the first `func Test`).
Task 14: IMPLEMENTER DISPATCHED (session 53, Opus 5, agent `impl-task-14`, un-suffixed).
  BASE `1a3da79`, all three absolute paths, overrides named BINDING, `task-14-report.md`
  named as the authoritative channel, and the runner lane named as the implementer's own
  gate since `scripts/gate` will not catch a regression there.
Task 14: implementer DONE — 7 commits `1a3da79`..`06c9afe`, tree clean, 12 files,
  930 insertions / 594 deletions. Production touched in only THREE files
  (`runs/tasks.go`, `workflows/validate.go`, `workflows/workflows.go`).
Task 14: **GATE PASS at `06c9afe`** — controller-run, 9 lanes, `Commit=06c9afe` stamped,
  contracts clean. **AND `task backend:test:runner` exit 0** with the new gate_note test
  inside the lane (all four packages `ok`) — the lane `scripts/gate` does not assert.
Task 14: 18 MUTATIONS, per call site. The two that matter most for method:
  **M3/M4 kill exactly ONE call site's test each** (`checkHarnesses` in `Rerun` vs in
  `resolveIntake`), proving the extraction did not collapse two guards into one covered
  path; M5 then shows the shared body is reached from both. And **G3 is Task 13's lesson
  landing on the implementer**: duplicating `run.finished`'s audit at
  `orchestrator.go:652` changed NOTHING, because the golden set finishes through
  `orchestrator.go:1419` — the mutation looked like it should kill the row and could not
  reach it. G4 at the other site is what actually falsified the assertion.
Task 14: **A1b found a real coverage gap the review had not asked about.** With
  `wellFormed` admitting malformed edges the ENTIRE existing suite stayed green — nothing
  pinned that an edge already rejected for an unknown endpoint must not also inflate its
  target's consumption count. The implementer added
  `TestValidate_AMalformedEdgeIsNotCountedTowardsConsumption` (`0c54a27`) and re-ran A1b
  to show it now dies. An unfalsifiable predicate found by mutating a refactor.
Task 14: **M6 is a FIXTURE-level falsification, recorded as such.** The `liveRuns`
  assertion cannot be killed by any application-level mutation because it asserts what the
  INDEX promises; it was falsified by `DROP INDEX runs_task_live_key`. Not vacuous, but
  not code-level either — the distinction is the honest one.
Task 14: minor (deferred): **`characters`-for-bytes survives in five other messages**
  (`workflows.go:67`, `identity/service.go:76`, `assets/assets.go:138` and `:147`,
  `assets/harnesses.go:68`, `assets/repos.go:56`) — all count with `len()`, all say
  "characters", none pinned by `openapi.json`. CONTROLLER RULING: **not widened here.**
  The carry-forward names the workflow *name* message only, and widening scope mid-task
  is how a bounded task stops being reviewable. Routed to the whole-branch review as a
  one-line-each consistency sweep.
Task 14: minor (deferred): `orchestrator.go:652`'s `run.finished` audit is exercised by
  NOTHING in the golden set (discovered by G3). Not this task's defect; real coverage gap.
Task 14: minor (deferred): `isUniqueViolation` in `Rerun` is a bare 23505 check per ruling
  D1(a). `insertRun` also runs `appendEvents`, so a future unique index on `run_events`
  would report as "another re-run is already pending". Exposure is real, matches four
  existing call sites, worth a line wherever error classification is revisited next.
Task 14: REVIEWER DISPATCHED (Opus 5, agent `review-task-14`, un-suffixed) with the three
  requirement files in precedence order, the extraction/signature-change/file-split named
  as the risks that justify looking outside the diff, and a mandate to run and mutate.
Task 14: REVIEW = spec ✅ / quality **APPROVED** (`task-14-review.md`). 0 Critical,
  0 Important, 5 Minor — **no fix round**. The reviewer ran 12 mutations of its own, four
  of which the implementer never ran, and left the tree clean at `06c9afe`.
Task 14: the reviewer's **G5 is the most valuable mutation of the round and nobody had run
  it**: `gates.go:294` `SubjectID: decision.ID.String()` → a fresh uuid →
  `gate.decided audit entries for this run = 1, want 2`. That pins
  `countAuditGateDecisions`'s join (`d.id::text = a.subject_id`) to the production
  convention that a `gate.decided` entry's subject IS the decision row — the single most
  novel thing in the new helper, and the implementer's own G-series never tested it.
Task 14: the reviewer **independently reproduced the negative result** (its Ga = the
  report's G3): destroying `orchestrator.go:653`'s audit subject moves NOTHING. Two agents
  now agree the golden set never reaches the terminalization path's `run.finished` audit.
Task 14: the reviewer closed the one thing that could have made the `gate_note` test
  vacuous, and the report had not stated it: if the staged `.caesar/in/` files landed in
  the produced diff, every content assertion would pass without the agent reading
  anything. They cannot — `gitops/extract.go:26` (`:(exclude).caesar`) plus the index drop
  at `:110-118` remove `.caesar` from the patch, so the evidence can only reach the diff
  through the agent's own `cp`.
Task 14: ⚠️ **the reviewer did NOT run `task backend:test:runner`** (~100s + image build,
  no specific doubt to justify it). It verified the new file builds and vets under the
  lane's tag and that `internal/app` still vets there. **The runner lane is attested by
  the IMPLEMENTER's pasted `RUNNER_EXIT=0`, not by the reviewer** — and `scripts/gate`
  does not cover it either. Whoever runs the whole-branch review should run that lane.
Task 14: minor (deferred): `gatenote_runner_test.go:318-329` — two assertion messages
  describe something other than what they check (`note-seen.json` / `inputs-seen.json` are
  the SCRIPT's copy targets, so a failure reading "inputs.json did not name the gate note"
  sends a debugger to `lineageInto` when the cause is the container). Same class of defect
  as Task 13's override C. Hoist into their own "the agent did not run" check.
Task 14: minor (deferred): `rerun_carryforward_integration_test.go` names a process, not a
  subject; every neighbour is named for what it tests. `rerun_intake_integration_test.go`
  would age better — "carryforward" is a fact about this ledger, meaningless in six months.
Task 14: minor (deferred): the split put ~2 files of distance between two near-identical
  14-column `approved_patches` INSERTs (`constraints_integration_test.go:358-364` and
  `testutil_integration_test.go`'s `seedRunWithAuthorization`). PRE-EXISTING duplication,
  explicitly not counted against this task, but the next column added has two homes.
Task 14: >>> **COMPLETE** (commits `1a3da79`..`06c9afe`, 7 commits, review clean, NO fix
  round, 5 minors deferred). <<< Gated by the controller at `06c9afe` (9 lanes, stamped),
  plus the runner lane green by the implementer's attestation.

## Task 11 — The HTTP surface (BASE `e892288` — COMPLETE, session 51)

Task 11: brief generated (`task-11-brief.md`, 386 lines).
Task 11: PRE-FLIGHT NOT CLEAN — **11 BLOCKER, 16 CORRECTION, 6 DESIGN QUESTION**
  (`task-11-preflight.md`, 1126 lines), every finding backed by a real command. Highlights:
  `task contracts:gen` PANICS on `decideGate` (a `{id}` pattern with a non-nil Request and no
  `requestOverrides` entry hits `contracts.go:180`'s panic arm — reproduced, fix verified);
  there is NO mechanism to declare a 409 though Step 7 requires one; the whole e2e harness is
  invented AGAIN (Task 10's failure mode verbatim); `httpx.ProblemDocument` is really
  `ProblemDetails`; the `GetRun` refactor has **28 call sites in 11 files across 3 lanes**, not
  the "three" the brief claims (found by renaming and compiling with `-gcflags=-e`; `go vet`
  hides 26 behind one-error-per-package); and `api_test.go` does not exist, so Step 8's second
  command is a guaranteed no-op green.
Task 11: >>> REPO-WIDE GOTCHA, worth more than the task itself: **`task backend:test:integration
  -- -run '…'` SILENTLY DROPS THE FILTER.** The Taskfile never references `{{.CLI_ARGS}}`, and
  go-task does not append post-`--` args to commands that do not reference them (proved against
  go-task 3.48.0). EVERY "Run:" line in EVERY brief on this plan has been running the entire
  integration suite with `-run` and `-v` discarded. For a targeted run call `go` directly from
  `backend/`. The full-gate rule is unchanged. <<<
Task 11: **USER RULING (sofo) — SPLIT THE TESTS, do not build the e2e harness.** Four tests (403
  grant enforcement, 404/409, body validation, inbox scoping) go into the EXISTING
  `internal/runs/api/api_integration_test.go` harness, which already has per-user grant maps
  (`:200-202`), four seeded users incl. an admin (`:192-198`) and the real middleware chain
  mounted (`:208-213`). Task 11's app-e2e file keeps only a thin route-reachability proof.
  **`TestAPI_RunProjectionCarriesGenerationsDecisionsAndPublications` MOVES TO TASK 13**, whose
  `TestGoldenSet_FullConveyorThroughTheAPI` already drives the identical arc on a fixture already
  scheduled. Saves ~200 lines of harness Task 13 would have duplicated.
Task 11: CONTROLLER RULINGS, overridable — B6 take option 1 (keep `GetRun` as a thin wrapper
  delegating to a new `GetRunDetail`; change only `api.go`, so one call site and no test churn);
  B2/D4 add `Errors []int` to `httpx.Operation` and teach `contracts.Build` to emit it, which puts
  `platform/httpx/operation.go` and `internal/contracts/contracts.go` IN SCOPE; D2 carry BOTH
  `ActedByID` and `ActedByEmail` (id alone makes the JOIN dead weight; email alone makes the field
  name lie AND leaks the approver's address to every view-only member); C9/D3 keep ONE `generation`
  wire field but document both meanings on the DTO and in the operation Summary; no `absent_since`
  in the projection (sweep bookkeeping that changes every tick); no inbox pagination, just a
  comment saying unbounded-by-design at V1.
Task 11: **C10 RULED IN SCOPE — fix the "wall deadline has passed" LIE.** `BumpRunGeneration` has
  three predicates and every empty result maps to one message, so a concurrent decision on a
  SIBLING gate (which flips `runs.status` waiting→running) tells a human their deadline expired
  when it has not. Fix at the source — re-read the run row on the empty result and choose the
  message from what is actually false — and pin it with a test that stages the sibling-gate race
  and asserts the message is NOT the deadline one (a test asserting only "409" passes on the lie).
Task 11: **C11 RE-ROUTED — Task 10's deferred D5 moves from Task 11 to TASK 14.** A run parked past
  its deadline over a settled publication already surfaces as `{"class":"transient"}` on
  `GET /runs/{id}` TODAY (`dto.go:50,62-67,136-141` + `orchestrator.go:1565-1571`). Task 11 neither
  worsens nor fixes it. It is an orchestrator CLASSIFICATION bug, not a projection bug, so Task 14
  ("Carry-forwards") is its right home. Task 11 must state this in a comment rather than be silent.
Task 11: >>> CARRY-FORWARDS TO TASK 13 — DO NOT LOSE. <<< (a) It inherits
  `TestAPI_RunProjectionCarriesGenerationsDecisionsAndPublications` (run generation == 2 after one
  request_changes then one approve; one decision per generation; a `published` publication with a
  PR URL and branch). (b) With it come two pre-flight findings: **C13** — `decided != 2` cannot
  distinguish a correct `phase_run_id` join from a `node_id` join, so assert the mapping, not the
  count; and **C14** — the test ignores four HTTP status codes it should be asserting.
Task 11: all rulings are in `task-11-overrides.md`, which SUPERSEDES the brief. The implementer
  dispatch must carry brief + overrides + preflight.
Task 11: DISPATCHED 2026-08-09 (session 51) — implementer `impl-task-11-s51`, Opus 5, **BASE
  `e892288`** (verified against `git log` before dispatch: HEAD = e892288, tree clean, branch
  caesar-08-gates-publishing, 47 ahead of main; Docker up, all 5 caesar images present). Dispatch
  carried all three ABSOLUTE paths (brief + overrides + preflight), the user's split-the-tests
  ruling, the C10 sibling-gate-lie fix as in-scope, C11 as explicitly not-his, the B2/D4 scope
  expansion into `platform/httpx/operation.go` + `contracts/contracts.go`, the PKG= targeted-test
  form, the no-pipe rule, the gate script invocation, and the commit rule. Report file
  `task-11-report.md` named as the AUTHORITATIVE channel. Spawn name resolved with no collision
  suffix. Nothing dispatched in parallel with it.
Task 11: implementer returned **DONE_WITH_CONCERNS**, commits `53aca37` (feat) + `5000ed8` (test
  strengthening), 15 files, +1885/-94. Its final message never arrived — only an idle
  notification — exactly the harness behaviour the standing rules predict. THE REPORT FILE WAS
  THE RECORD, as designed. (Note: gopls diagnostics fired mid-edit showing `finishGate` arity
  errors and a missing sqlc row type; they were STALE — captured before the regen landed.
  `go build ./...` and `go vet -tags integration` are both clean at HEAD. Do not act on
  diagnostics without building.)
Task 11: **CONTROLLER-VERIFIED, not taken on the report.** `scripts/gate <wt> 5000ed8
  caesar-08-gates-publishing` → **exit 0, GATE PASS, 9 lanes, `Commit=5000ed8` stamped,
  contracts clean.** First use of the script on a product-code commit; it behaved.
Task 11: **DECISIVE MUTATION RUN BY THE CONTROLLER.** Mutant: `decisionRefused`'s three-arm
  switch collapsed to `return xerrors.E(ClassConflict, fallback)` (i.e. the old constant
  message). FIRST ATTEMPT WAS AN INVALID MUTANT — it did not compile (`declared and not used:
  run`), which is the plan's own recorded failure mode; repaired with `_, _ = now, run` and
  re-run. Result: `TestDecideGate_ARunResumedBySiblingIsNotReportedAsAnExpiredDeadline` FAILED
  on BOTH subtests with the right diagnostic ("want it to name the run's actual status"), then
  PASSED after `git checkout --`. Tree clean at `5000ed8`. Red-then-revert satisfied.
Task 11: **RULING 1 — C10's premise was WRONG; the scope extension STANDS.** The override aimed
  C10 at `BumpRunGeneration`'s empty arm. The implementer found `finishGate` runs one statement
  earlier carrying the SAME two run predicates, so the named site is nearly unreachable and the
  sentence a human actually gets was a different, equally false one. **I verified this myself
  rather than by reading the report:** `FinishGatePhase`'s `EXISTS` clause is
  `r.status = 'waiting' AND r.wall_deadline > now()` (queries.sql:336-341), byte-identical to
  `BumpRunGeneration`'s (queries.sql:425). Both sites now route through `decisionRefused`, which
  re-reads the run row and the DB clock and picks the message from what is actually false.
  Confining the fix to the site C10 named would have left the REACHABLE lie in place and left
  the fix unpinnable by any deterministic test. This is the plan's own lesson landing correctly:
  **a prescribed remedy is a hypothesis, and this implementer falsified one.**
Task 11: **RULING 2 — the unrequested projection test STAYS.** The user ruling moved the
  CONVEYOR-ARC test to Task 13 to avoid duplicating ~200 lines of harness; it did not rule that
  six new wire fields ship unfalsifiable. `TestAPI_RunProjectionCarriesTheGateFields` asserts
  projection SHAPE over already-staged rows, adds no harness, costs ~90 lines, and explicitly
  does not replay the arc. Decisive argument: mutants M7-M12, M21, M22, M25 and M26 — TEN — are
  killed by that test alone; deleting it resurrects all ten. Task 13's carry-forward (the arc
  test + C13/C14) is UNCHANGED and still owed.
Task 11: implementer self-caught a REGRESSION IT INTRODUCED — `runScoped` returning the full
  detail made `GET /runs/{id}/events` run five queries per poll tick. Added `GetRunSummary`,
  narrowed `runScoped` to it, left `handleGetRun` fetching the detail after authorizing. Worth
  noting because the poll path is now CHEAPER than before this task.
Task 11: reviewer `review-task-11-s51` DISPATCHED (Opus 5) with brief + overrides + report +
  review package `review-e892288..5000ed8.diff`. **Told it may run focused tests AND mutants**
  (the first reviewer on this plan allowed to — the Tasks 4/5/6 prohibition existed only because
  targeted runs were secretly full-suite runs; `3313958` fixed that), with a hard requirement to
  revert any mutation and prove `git status --porcelain` empty at `5000ed8`. Both controller
  rulings were disclosed to it as CHALLENGEABLE, not as out of bounds.
Task 11: REVIEW BACK — **Spec ✅ (every override item landed, verified line by line), Task
  quality: NEEDS FIXES.** 0 Critical, 2 Important, 6 Minor. Tree verified clean at `5000ed8`
  after its three mutants. It also independently upheld BOTH controller rulings, having
  re-derived the `FinishGatePhase`/`BumpRunGeneration` predicate identity itself.
  >>> **LETTING THE REVIEWER RUN MUTANTS PAID FOR ITSELF ON ITS FIRST USE.** Both Importants
  are VERIFIED SURVIVORS produced by running the mutant, not by reading. This retires the
  Tasks 4/5/6 no-tests-for-reviewers rule for good. <<<
Task 11: **IMPORTANT 1 — my own decisive mutation was not as decisive as it looked.** Reverting
  `decisionRefused`'s BODY (my M13) kills through the `finishGate` call site, which is the one
  the test reaches; the `revisions.go:129-136` arm — **the site C10 actually named** — survives
  reversion to the old constant (`ok 4.495s`). It is reachable only when a sibling commits
  BETWEEN `finishGate` and `BumpRunGeneration` inside one transaction, so it is unpinnable
  without fault injection. RULING: accept it as unfalsifiable, require it DOCUMENTED at the
  site, and record it here as a named survivor. **The generalizable lesson: a mutant that kills
  proves the guard covers THE PATH THE TEST TAKES, not every call site of the mutated function.
  When one helper serves two call sites, mutate at each SITE, not in the shared body.**
Task 11: **IMPORTANT 2 — the `phase_run_id` join survives being re-keyed on `node_id`**
  (`tasks.go:539-551`, mutant ran `ok 2.637s`). Both the SQL comment and the test's own comment
  claim it catches node-id smearing after a revision, but the fixture stages exactly ONE gate
  attempt. **This is the M6a shape the implementer already found and fixed once in this same
  test** — a guard whose comment claims a property its fixture cannot exercise. Fix: stage the
  superseded earlier attempt + its own `request_changes` decision, assert the two gate phases
  carry DIFFERENT verdicts, and demonstrate the re-keyed mutant now dies.
Task 11: fix round 1 DISPATCHED to `impl-task-11-s51` (resumed, context intact). Sent both
  Importants plus FOUR bundled Minors — a deliberate deviation from "minors never enter the
  loop", on the grounds that all four are one-to-five-line repairs to load-bearing COMMENTS or
  to the falsification net itself, in functions already being touched: M3 (the deadline arm's
  stated justification is FALSE on Postgres — `DBNow` is `transaction_timestamp()`, frozen for
  the transaction, and `wall_deadline` is never UPDATEd, so the two reads cannot disagree);
  M4 (`decisionRefused`'s `GetRun` takes a NEW snapshot under READ COMMITTED, so the janitor
  re-parking the run restores both predicates and drops it into the fallback — the old lie, for
  the very race C10 was about; document as best-effort, and pass the already-held `now` in);
  M5 (`WaitingSince`/`ActedAt` asserted self-referentially — `want` is built from `got` — so
  only `IsZero` is real; and every staged row has attempt = generation = 1, so struct equality
  proves nothing about either — Important 2's second row supplies differing values for free);
  M6 (the 403-before-200 subtest ordering is load-bearing and undocumented). FIX_BASE for the
  scoped re-review = `5000ed8`.
Task 11: minor (deferred): `GateDecisionResponse.Decision` echoes the REQUEST BODY rather than
  the persisted value (`api.go:352`). Harmless today — validation admits only three literals —
  but the wire value never passes through the domain, so any future normalisation desyncs
  silently. Brief-specified; recorded, not fixed.
Task 11: minor (deferred): `handleGetRun` reads the run row TWICE (`GetRunSummary` inside
  `runScoped`, then `GetRunDetail`'s own `GetRun`). The trade is right — the poll path lost
  three queries and the page gained one row read — recorded so it is not re-discovered as a
  regression.
Task 11: >>> UNKILLED MUTANTS, NAMED — POINT THE FINAL WHOLE-BRANCH REVIEW HERE. <<< Beyond S1
  and S2 (now in the fix round): S3 `decisionRefused` arm order swapped; S4 its `DBNow` swapped
  for `time.Now()`/the app clock (clock source pinned by NO test); S5 its boundary `.After` →
  `.Before`; S8 `Attempt`/`Generation` sourced from a different column (every fixture row is
  1); S9 `ListPublishIntentsForRun` gains `LIMIT 1` or reverses its `ORDER BY` (every fixture
  stages ONE intent, yet chained gates each authorize their own publish, so multi-publication
  runs are real); S10 `publicationFromRow` reads `BaseSHA` from the run instead of the intent
  (the fixture makes them equal); S11 `runScoped` re-widened to `GetRunDetail` — the poll
  regression just fixed, with NO query-count assertion anywhere to notice; S12 a wrong status
  VALUE in `Errors` on a future operation; S13 the `node == ""` guard deleted (unreachable via
  the mux); S14 the orchestrator materializing a gate phase row in a different shape — every
  gate in `internal/runs/api` is staged SQL, so **Task 13's golden set is now LOAD-BEARING for
  "the orchestrator produces the rows this surface reads"**.
Task 11: fix round 1 LANDED — one commit `3b0cabf` (`test(api): stage a spent revision round so
  the gate projections have a wrong answer to give`), +296/-87 across three files. **CONTROLLER
  GATE at `3b0cabf`: exit 0, GATE PASS, 9 lanes, `Commit=3b0cabf` stamped, contracts clean.**
  (Build/vet verified separately first — gopls diagnostics again showed a stale `decisionRefused`
  arity error from the Minor 4 signature change. `go build ./...` exit 0. THIRD time this session
  stale diagnostics looked like a broken tree; build before believing them.)
Task 11: **the S2 fix is entirely IN THE FIXTURE — `tasks.go` is BYTE-IDENTICAL to `5000ed8`.**
  The production join was always right; only the test could not prove it. `stageGate` now stages
  a spent revision round (gen 1: impl attempt 1 failed, impl attempt 2 superseded + patch, gate
  attempt 1 superseded + a `request_changes` decision by the REQUESTER with `self_approval=true`;
  gen 2: impl attempt 3 succeeded + patch, gate attempt 2 waiting). The two decisions differ in
  EVERY projected field, not just the verdict, so a smear is caught even by a reader inspecting
  only one. S2/M36 now KILLED, M6b (run-id keying) still KILLED — the count assertion was kept
  precisely because the verdict comparison alone would not catch it.
Task 11: >>> **THE FIX FOUND TWO MORE SURVIVORS THAN THE REVIEW DID.** <<< The implementer
  noticed that "attempt and generation are the same number on every row" was the same class of
  defect and tested it: M33 (artifact attempt/generation swapped) and M34 (phase
  attempt/generation swapped) BOTH SURVIVED. Adding the FAILED impl attempt 1 breaks the
  coincidence — rows become `(attempt 2, gen 1)` and `(attempt 3, gen 2)` — and both now die.
  This also closes the review's S8. **Generalizable: a fixture where two independent ordinals
  are always equal cannot distinguish them; break the coincidence deliberately.**
Task 11: >>> CARRY-FORWARD TO TASK 13, from the implementer's own concern. <<< `stageGate` is
  now a HAND-WRITTEN revision history (5 phase rows, 2 artifacts, 1 decision, 2 generations) and
  it deliberately omits the two seed artifacts a real revision writes (the gate note and the
  carried-forward patch, both at attempt 0) because nothing on this surface reads them. Survivor
  6 GREW this round, not shrank: more fiction to keep in sync with the orchestrator. **If Task
  13's golden set covers the two-attempt projection through the REAL conveyor, shrink this
  fixture rather than maintaining both.**
Task 11: scoped re-review DISPATCHED — `rereview-task-11-s51`, **Fable 5** (bounded: verdict six
  findings against a 296-line diff), FIX_BASE `5000ed8` → `3b0cabf`, package
  `review-5000ed8..3b0cabf.diff`. Told to run Important 2's mutant ITSELF and revert, since that
  finding already survived once and argument is not acceptable evidence for it.
Task 11: re-review CLEAN — **all six findings ADDRESSED**, no new Critical/Important breakage.
  Important 2 was verified BY MUTATION, not by reading: the re-reviewer re-keyed `tasks.go` on
  `d.NodeID` at `3b0cabf` itself and the test FAILED with the smear signature (live "approve" /
  prior "approve", want approve / request_changes), then reverted — tree clean, HEAD `3b0cabf`.
  It also confirmed `tasks.go` byte-identical to `5000ed8` via a real `git diff --stat`, and
  cross-checked the enlarged fixture against the ORCHESTRATOR (supersede statuses,
  `MaxAttemptForNode` continuation, decision shape, `MaxRounds` headroom) — internally
  consistent, which was the specific risk in staging a hand-written revision history.
Task 11: minor (deferred): `stageGate`'s comment OVERSTATES timestamp distinctness — it claims
  every staged timestamp is far from every other, but four pairs collide. The assertions do not
  depend on the overstatement; the comment does.
Task 11: minor (deferred): the INBOX test's `attempt`/`generation` are both 2, so swapping those
  two columns is still unfalsifiable THERE — the same class M33/M34 closed in the projection
  test, one test over, and not admitted in the updated §5. Cheap to close whenever the inbox
  fixture is next touched.
Task 11: **COMPLETE** (commits `e892288..3b0cabf` = `53aca37`, `5000ed8`, `3b0cabf`; review
  clean after one fix round). Controller-verified at every step, never on a report: gate PASS at
  `5000ed8` AND at `3b0cabf` (9 lanes each, commit stamped, contracts clean), plus my own
  red-then-revert mutation. Branch now 50 ahead of main. **Four tasks remain: 12, 13, 14, 15.**
  WHAT TASK 12 INHERITS: (a) the `Forge` port carry-forward recorded under Task 9 below —
  `Forge` has no `AsOutcomeError` equivalent and `ClassifyOutcome` is DEFAULT-DENY, so the day
  the real adapter lands, `runPublish`'s `OpenPullRequest` exit is the whole I1 class again on a
  `pushed` intent; (b) `Errors []int` now exists on `httpx.Operation` and `contracts.Build`
  emits it — the seam Plan 02's global error-status fix should use, and Task 12 should not
  reinvent it; (c) survivor S12 — a wrong status VALUE in `Errors` is caught only by the
  committed `openapi.json`.
