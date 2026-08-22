# Ledger — Plan 12a (web critical path), execution

**Branch `caesar-12a-web-critical-path`. `main` untouched.** Started 2026-08-16 from `af915e1`
(the reviewed plan). Verify this against `git log` before acting on it — a stale handoff has cost
a full session on this project.

**All seventeen implementation tasks are complete and gate-green at `dcd9c75`.** Task 18 is not:
its Steps 1–4 are an operator checkpoint that cannot run before the branch merges, and they are
listed under **Operator TODO** below, explicitly not done. What landed here is Task 18's Step 5 —
the documentation half — pulled ahead of the merge by a sofo ruling on 2026-08-22 (see Rulings).

**Roadmap row 12a is NOT flipped**, and the reader should know one thing before looking for it:
`docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md` has **no row 12a and no row 12b**. Row 12
("Web features") is undivided and reads `pending`; it covers both halves. Splitting it, or
flipping it in part, is an operator decision at the checkpoint — flipping it whole would claim
work 12b has not started.

## Where execution stands

The gate is `scripts/gate <abs-worktree> <sha> <branch>` and it ran green on **every commit**
below, not merely at wave boundaries — 27 lanes each time. Measured at `dcd9c75`: **298 vitest
tests across 25 files**, and **4 Playwright tests across 3 spec files** in `task web:e2e`.

| Wave | Task | Commits | Fix rounds | The story in one line |
|---|---|---|---|---|
| 0 | 1 — the gate's examined artifact and review verdict, resolved in the domain | `b978415` | 0 | The brief's third subtest was vacuous; a fourth was written to kill the mutant. |
| 0 | 2 — a waiting gate's evidence and decidability on the wire | `8e197b7` `a74198c` | 1 | The `$ref`/`required` dissent that amended a global constraint for every later task. |
| 0 | 3 — one artifact's payload, grant-scoped on the run | `0246be4` `e60f54d` | 1 | Three of the brief's five subtests asserted 404 on an unregistered route and proved nothing. |
| 0 | 4 — list runs with the projection a card needs | `abc859e` `f54eff5` | 1 | Two dissents upheld, a fixture trap confirmed real, and a cursor guard nobody thought was falsifiable. |
| 0 | 5 — document schema 2 and a published payload contract | `0460617` `757914e` | 1 | A `// indirect` line survived 27 green lanes because no `go mod tidy` lane exists anywhere. |
| 0 | 6 — enforce artifact payload shape at schema 2 | `9c70f3e` `416859c` `b014ce5` | 2 | Both rounds were about a comment. The claim "asserted in five call sites" was false in every direction. |
| 0 | 7 — carry phase transitions on the attention stream | `451d198` | 0 | Widening the filter made two pre-existing fixtures stop discriminating, silently. |
| 1 | 8 — five phone tabs and a desktop rail | `f16c81a` `078dc98` `e8615ca` | 2 | Both rounds were about a comment; the assertion set was sound from the first commit. |
| 1 | 9 — route stream frames to the run they belong to | `77b1bcd` `2029656` | 1 | No pre-existing fixture had ever discriminated the widened behaviour — measured, not feared. |
| 1 | 10 — decode a patch payload and parse its files | `cfab5e9` `618748e` | 1 | A deleted line beginning `-- ` closed the hunk and pushed a phantom file. This repo's SQL is full of them. |
| 1 | 11 — the gate decision controls, all three or none | `8f7623a` `a030ee2` `5f8a5b0` | 2 | `gate_not_waiting` does not exist; the corrections file enumerated a value Task 2 had dropped. |
| 1 | 12 — the approval surface, summary-first, built once | `3dbb1a9` `7976a5f` | 1 | A hand-written fixture omitted a key the wire always sends, and a mutant found it. |
| 1 | 16 — run detail: timeline spine, gate surface inline, the PR | `ad81e34` `d74dc32` | 0 | A mutant caught a live test that had passed through RED, GREEN and a full gate on a mutated key. |
| 1 | 13 — Home, state-first, with live dots and inline decisions | `0977098` | 0 | The plan's suspending loader hangs the app; per-section degradation shipped instead, sofo-ratified. |
| 1 | 14 — Tasks: four attention groups over seven run statuses | `b8c9c12` | 0 | A four-DTO-family `required:"true"` sweep joined the task; 8 structs, 44 properties pinned. |
| 1 | 15 — the auth guard, rehomed | `083a1ae` `6ad17be` | 1 | The brief's "no loader-free route remains" was false; `/more` kept the case rather than losing it. |
| 1 | 17 — Playwright: the three mobile-critical paths | `d0955db` `dcd9c75` | 1 | Test isolation was unsolved and had to be invented; the fake's synthetic patch could not be rendered at all. |
| — | 18a — this ledger, `web/README.md`, and the plan corrections | *this commit* | — | Docs only. Task 18's Steps 1–4 remain the operator's, post-merge. |

Wave 1 ran in the order **8, 9, 10, 11, 12, 16, 13, 14, 15, 17** — Task 16 moved ahead of 13 by a
sofo ruling before Task 1 began, and 15 stayed adjacent to 14.

## What execution found that review did not

Plan 12a went through a pre-flight scan (11 agents, 5 lenses plus adversarial verification: 32
verified findings, 27 after dedupe, 5 blockers) *and* a per-task controller pre-flight against the
executed tree. Everything below survived both and was found only by running something.

### The ten numbered premise corrections

The ledger tracks these as a single sequence. Nine were wrong; **#3 is the one that was tested and
held**, recorded here so the sequence is not read as nine-plus-one-more-wrong.

1. **Task 6** — the brief asserted `TestPinnedSchemaVersionIsWhatDecidesPayloadValidation` depends
   on the fake executor's emission, so migrating the fake would break it. It does not:
   `runsports_test.go` builds the legacy payload as a hand-written literal and never references
   `fake`. The edit was a comment rewrite with table, literal and both assertions byte-identical.
2. **Task 7** — the controller's premise that `realtime_e2e_integration_test.go` has no executor
   seam, making an orchestrator-driven phase assertion unsatisfiable there. False:
   `startPendingRuns` runs on the janitor, materializes the root node and emits
   `phase.materialized` without ever touching `Executor`, and `bootServe` already runs a real
   orchestrator. The reviewer disabled the production call itself and got the FAIL.
3. **Task 8 — TESTED AND HELD.** The controller feared that scoping an e2e locator to
   `getByRole('navigation')` would exclude the `display:none` rail and drop the count to 1.
   Measured: the role filter applies ARIA hiddenness to the nav element itself, and the chained
   CSS locator does not filter descendants by visibility, so the count stays 2 and the mutant kill
   survives. The fear was unfounded in the safe direction.
4. **Task 9** — the controller instructed that the `runs: null` fixture "makes deleting `?? []`
   redden every screen test the moment Task 13 lands". The implementer declined to write it: a
   green run could equally mean the future screen wrote `runs?.map`, making `?? []` redundant
   rather than the test vacuous — two causes, opposite responses. The re-reviewer then supplied a
   second independent reason neither had: a `runs.map` screen reddens in `tsc` inside `web:ci`
   before any test runs at all.
5. **Task 11** — `gate_not_waiting` does not exist. `decidability()` returns exactly three reasons
   (`not_approver`, `self_approval_forbidden`, `evidence_missing`); a gate that stopped waiting is
   the 409 conflict path, not a refusal reason. The corrections file's "four values (sofo's
   ruling)" was stale against what Task 2 actually built — the ruling's substance was *adding*
   `evidence_missing`, and the enumeration kept a value Task 2 had dropped.
6. **Task 16** — controller correction #5 claimed `attempt` restarts at 1 after a revision. It
   continues per node (`store/queries.sql`, `runs/revisions.go`). Three premise-derived comments
   were rewritten to measured truth.
7. **Task 14** — the brief's Step 7 claimed the screen test survives the default-arm deletion, so
   the pure `task-groups` test is the sole carrier. True only of the brief's own eight-test suite;
   the shipped per-status screen test reddens under that mutant.
8. **Task 15** — the controller's pre-flight fact 5 said the child loader never runs when the
   layout redirects, so the request count is 0. Measured 1, and the bound was strengthened from
   `toBeLessThanOrEqual(1)` to `toBe(1)`.
9. **Task 15** — the controller's restatement, "drop the request-count bound", was itself wrong:
   `<= 1` would be a dead assertion, and `toBe(1)` is what discriminates.
10. **Task 17** — pre-flight correction #2 said the examined artifact is a synthetic patch
    touching `CAESAR.md`, so the brief's `/CAESAR-impl\.md/` locator could never match. Right
    about the tree, wrong about the consequence — see the next section. This one corrected the
    session-80 controller's own premise.

### Twenty-eight implementer dissents, and not one overturned

Every dissent that reached adjudication was upheld, most under independent re-measurement by the
reviewer rather than on the argument. Counted by task: 2 (1), 4 (2), 6 (1), 7 (1), 8 (2), 10 (1),
11 (1), 12 (3), 13 (3), 14 (1), 15 (4), 16 (2), 17 (6). Task 5's deviation and a re-reviewer's
third-party dissent on Task 4 were upheld as well. The ones with consequences beyond their task:

11. **`required:"true"` on a `$ref` property is a claim of NON-NULLABILITY** (Task 2). The
    implementer refused the controller's correction; the reviewer regenerated in a scratchpad and
    ran `openapi-typescript` 7.13.0 itself. `jsonschema-go` gives a `$ref` property no null branch
    unless `EnvelopNullability` is set, and a `nullable:"true"` tag does not rescue it —
    **no tag combination in this toolchain yields `T | null` for a `$ref`.** A nullable `$ref`
    therefore stays optional, which is the safer of the two shapes this toolchain can express and
    not a safe one. This amended a global constraint and bound every remaining DTO decision.
12. **Labels resolve in the API layer, not in `GetRunDetail`** (Task 4). The controller's
    correction made ~24 service tests fail on an unwired port. The deviation was judged strictly
    better than the correction — it resolves once per list rather than once per row.
13. **A base64 cursor guard everyone had written off as unfalsifiable is falsifiable** (Task 4,
    third-party dissent). Go's decoder returns the bytes decoded *before* the error, so a padded
    cursor whose payload is a whole number of quanta decodes intact and passes every later guard.
    The controller and the first reviewer were both wrong; the table is now stronger than either
    specified.
14. **Home cannot suspend** (Task 13). The plan's `ensureQueryData` + `useSuspenseQuery` loader
    produces an unbounded redirect loop: `/` is `/login`'s guard target, so a 401 from one of
    Home's queries while `/auth/me` stays healthy sends the loader into `_app`'s boundary, the
    boundary navigates to `/login`, the guard proves the session and sends the visitor back. M18
    reproduces it; the observed failure is a 44-second worker OOM. Shipped as per-section
    degradation (`Promise.allSettled` plus `useQuery` per section), **sofo-ratified**.
15. **Four loader-free routes remain after 12a** (Task 15). The brief gave up the no-request case
    on the premise that none would be left. `/more`, `/admin`, `/repos` and `/workflows` are all
    loader-free, and `/more` is `PHONE_TABS`' fifth entry rather than an invented seam — so the
    "a seam with no production caller" objection does not reach it. Both guard tests were kept and
    proved non-redundant in **both** directions (each mutant kills exactly one of them),
    **sofo-ratified**.
16. **`validateSearch` structurally cannot remove a search key** (Task 13). A route's search is
    its ancestors' search with its own validated result merged over it, and nothing above these
    routes declares `validateSearch` — so `?status=42` reaches `loaderDeps` and `useSearch()` as
    the number 42 even when validation returned `{}`. Instrumented at all three sites. Narrowed at
    the read site instead, and the old comment's mechanism was corrected.
17. **The desktop two-pane run detail was not in any brief** (Task 16). decisions §7 locks
    "desktop = timeline left rail + detail right", and unlike cancel, retry and live output it had
    no deliberate-cut rationale anywhere. The implementer flagged it rather than shipping the gap;
    **sofo ruled BUILD NOW** as a scope addition before review, and the 21 prior tests needed zero
    edits.

### Task 17 had to invent test isolation, and D1 is why the lane renders anything at all

18. **The lane had no isolation and the brief's assertions were unsatisfiable even fully serial.**
    One server and one database serve every spec file; `playwright.config.ts` set no `workers`, so
    files ran in parallel workers by default; `approvals.spec.ts` left its gate undecided forever;
    and Path 2's run also ends `waiting_decision`. Every absolute badge assertion is a claim about
    the whole world. Shipped: `workers: 1`, plus `POST /fixture/quiesce`, which decides leftover
    gates through `runs.Service.DecideGate` and never SQL — the raw-SQL ban covers the reset as
    much as the setup. Both halves are mutation-proved (a no-op `quiesce` reddens beat 1;
    `workers: 4` gives "Expected 1, Received 3"). `open-gate` now answers with its run id so
    assertions can be scoped to one run. `task-timeline.spec.ts` deliberately does **not** quiesce,
    and its file comment says why.
19. **D1 — the fake's synthetic patch cannot be rendered, and the pre-flight was "right about the
    tree, wrong about the consequence".** With no remote registered, the fake emits a historical
    synthetic patch whose `diff` is *raw text*, while the wire form is base64 — so
    `decode-patch.ts`'s `atob` throws on `-`, `@` and `.`, returns EMPTY, and the approval surface
    rendered `0 files · +0 · −0` with no file rows for a patch that adds a line. Beat 4 was
    unwalkable. The fix registers a real bare repository with the fake, which restores the
    canonical base64 wire form and makes the brief's *original* `/CAESAR-impl\.md/` locator
    correct after all. **New lane dependency: `git` on `PATH`.** Verified end to end by the
    reviewer; **sofo ratified the harness change.**
20. **A test whose deterministic subject is already on screen proves nothing.** The brief's
    `getByText(/^\+/).first()` was vacuous — the collapsed `FileRow` header already renders `+1`
    before the click. Replaced with the added line's own text.
21. **`Reject` is the one gate decision no spec walks**, and the spec now says so truthfully
    instead of claiming "both decisions a person can reach from Home" over a control that offers
    three.

### The vacuous tests, and what found each one

Not one of these was found by a green suite. In every case the finder was a mutant.

22. **Task 1** — the brief's third subtest passed with the `!ok || Kind != KindGate` guard deleted.
    A fourth subtest was added to kill the mutant.
23. **Task 3** — three of five subtests asserted 404, and an unregistered route is also 404, so
    each passed before the handler existed *for the wrong reason*. This became a standing rule:
    any test whose failure mode is "the status the server already returns" is unproved until a
    mutant on the finished code kills it.
24. **Task 6** — subtest 1 survived all three required mutants, which is the vacuity signature.
    The implementer added a fourth (delete `summary` from the migrated fake) that kills it, so the
    producer migration is proved rather than assumed.
25. **Task 7** — adding `phase.` made **every** kind in `runs/events.go` attention-worthy, so two
    pre-existing fixtures with no non-attention kind left in them could no longer tell a correct
    filter from an over-selecting one: a `KindPatterns: ["%"]` mutant **survives** and the tests
    still pass and still look meaningful. Closed by seeding `audit.recorded` (a kind that appears
    in no production file) in three fixtures, and proved load-bearing in both directions.
26. **Task 9** — the same shape, admitted rather than defended: **no pre-existing fixture had ever
    discriminated the widened behaviour.** The re-gate mutant kills three tests, all new, and
    leaves all eight pre-existing ones green. The one old test carrying a non-attention kind had
    its discriminating assertion retired by design, and its replacement states the same protection
    as an outcome — an exact `toEqual` with no `['approvals']` in it.
27. **Task 10** — the round-0 suite never discriminated the parser's owed-content defect at all;
    mutant A is killed **only** by the new fixtures. The widening lesson in reverse.
28. **Task 12** — M11 survived its first run because a hand-written decided-run fixture *omitted*
    the `evidence` key, while `dto.go` has no `omitempty` so the wire always sends
    `evidence: null`. The fix was the fixture, not the code: an `onTheWire(run)` helper fills
    `decision: null` / `evidence: null` on every phase.
29. **Task 16 — the worst one, and it had been green through everything.** `phase()`'s default
    status of `succeeded` made the pre-frame and post-frame timelines identical, so the
    load-bearing `['run', runId]` bridge proof passed **for a mutated key** through RED, through
    GREEN, and through a full 27-lane gate. Only the mutant found it.
30. **Task 8** — mutant 8 survived its first run and read as a dead guard. The cause was not the
    code: `task web:e2e` has no `deps: [web:build]` (deliberately — a ~95MB browser download and a
    bundle build should not fire as a silent side effect), so a **hand-run** lane tests the
    previous bundle. Harmless inside `scripts/gate`, which builds first, and a standing trap
    outside it. Always `task web:build && task web:e2e`.
31. **Task 8, caught before it was recorded** — an unanchored `s/PHONE_TABS.slice(0, 4)/(0, 5)/`
    hit the *doc comment* above `RAIL`, not the code, and the green run would have read as "the
    mutant survived". The same string appears in both.

### The prose pattern, which is this plan's dominant defect

32. **Eight of the ten wave-1 tasks drew a blocking finding, and in six of them the blocking
    finding was a false sentence rather than wrong behaviour.** Tasks 8 (both fix rounds), 9, 11
    (in its own fix diff), 14, 15 and 17. The exceptions: Task 10's was a real parser defect,
    Task 11's first was a missing mutation record, and Task 12's was a genuinely missing test —
    though that one surfaced because the test's own *name* claimed two directions while the
    fixture exercised one.
33. **The forms it took, in order of arrival.** First code comments (Tasks 6, 7, 8, 9), then test
    titles (Tasks 14 and 15 — `_app.test.tsx`'s "STUB route" premise, and a dictated "exactly
    once" that names a redirect count no assertion separates from what it does prove), then a
    dictated rationale carried verbatim from a brief onto a path a dissent in the same task had
    just proved the subject was not on (Task 17's `task-timeline.spec.ts` comment, framed around
    `PhaseDots` when D2 had shown the dots are not on the run-detail path at all).
34. **The mechanism, diagnosed by an implementer about its own work:** it argued *for* the
    unconditional `['runs']` invalidation in §6 of its own report and then wrote a doc comment
    denying it existed, in the same commit. Intent and prose diverged inside one head, in one
    sitting. Nothing typechecks prose.
35. **The rule that would have caught all of it**, adopted mid-plan in Task 8 and worth carrying
    forward: **a claim stays in a test comment only if a mutant has killed the assertion it
    describes.** Everything else — ordering narration, meta-commentary about what is and is not
    guarded, restatements of the mechanism — is surface area to be wrong on, and it will be.
36. **The corollary, learned the hard way twice:** adopting the rule in a report does not apply it.
    A violating sentence survived in the same file whose own report had just adopted it, and was
    found by the next reviewer. Re-read the whole file against the rule, not just the lines you
    touched.
37. **A "MEASURED" claim whose probe was deleted is worse than the reasoned claim it replaced**,
    because it stops the next reader from checking. Task 9 rewrote a comment from reasoned to
    MEASURED after probing react-query's prefix matching, then deleted the probe. The re-reviewer
    re-ran it and it reproduced exactly — but the shape is the point: the comment now asserts an
    act, and the act left no artifact in the tree.
38. **A note about accuracy can itself be inaccurate.** Task 9's report carried a correction saying
    every line number in §3 "reads 1–3 low, add 1–3". That was false for 3 of 10 citations and two
    ran the opposite way; a reader applying it to one citation lands inside a different test. The
    stated cause was incomplete too — the amendment deleted a three-line assertion as well as
    adding comment lines. There is no usable numeric rule; assertion text is the only reliable
    anchor.

### The environment, the tooling, and things that look like bugs and are not

39. **Stale IDE diagnostics described a dead tree eight separate times, and not one was real.**
    Recorded at Tasks 8, 8 (round 2), 9, 9 (fix round), 10, 12, 17 and 17 (fix round), across
    sessions 77 and 80. The recurring shapes were "`ApiRunSummaryResponse` does not exist",
    "`/api/v1/runs` is not a valid path", "/more is absent from the route union", and phantom
    diagnostics for probe files that `git status` proves are not in the tree. Every one was
    settled the same way: run `tsc --noEmit` (exit 0) and `git status --porcelain
    --untracked-files=all` (empty). **Do not act on a mid-task editor diagnostic without running
    the real typechecker.**
40. **`exactOptionalPropertyTypes` produced six distinct consequences on this plan**, none of them
    anticipated: a literal `params.query` object that will not compile (Task 9), an unwritable
    `body: { decision, note: v.note }` where `note?: string` (Task 11), `if (phase.evidence)`
    narrowing correctly while `if (phase.evidence !== undefined)` narrows to the object over a
    runtime `null` (Task 16's brief), `TS2375` on optional-prop passthrough (Task 11), a
    search-updater that must return the whole next object so closing a phase *drops* the key
    rather than setting it undefined (Task 16), and typed fixtures forcing the honest wire shape
    (Task 13). Build by assignment; do not fight it with literals.
41. **The scratchpad is shared across agents and sessions, and a reviewer's mutant copy outlives
    the reviewer.** By Task 9 there were three stale `cp -a` copies of `web/` sitting there — one
    344M, one pinned at a superseded commit, and one with a **live mutant still applied**. Task 9's
    re-reviewer copied into a path that landed *inside* one and briefly measured a false 183/186
    before tracing the cause. Copy to a uniquely-named path, delete it when done, confirm what
    commit a copy is at before trusting any measurement from it, and sweep at task close.
42. **`TestDelayedRetry_ParkedRowSurvivesRestart` failed under gate load for the second time on a
    second plan**, on a 15s wall-clock deadline. The implementer reported it rather than burying it
    and argued unreachability; the argument was upheld on two independent grounds, the decisive one
    being that the commit's entire diff was one comment hunk and one test-file edit — zero
    production-behaviour change from a sha that had already gated green. A green re-run alone is
    **not** evidence a flake is unrelated. The suite's seven 5–20s wall-clock deadlines are the
    standing cause.
43. **There is no `go mod tidy` lane anywhere** — not in `Taskfile.yml`, not in `scripts/gate`, not
    in `.github/workflows`. That is why 27 green lanes missed a `// indirect` marker on a module
    `contracts.go` imports directly. Still open.
44. **No index serves `ORDER BY (created_at DESC, id DESC)` on `runs`**, so every page of a
    deliberately-paginated endpoint is a scan-and-sort. Task 9 made this reachable in practice:
    after it, every frame arms a debounce window, so `['runs']` is invalidated once per 250ms
    window during any active run. Intended design, unindexed table.
45. **The gate refuses a dirty tree**, so any step ordering that gates before committing cannot run
    as written — commit, then gate, then amend if it reddens. And never mutate a file with
    uncommitted work in it and revert with `git checkout -- <file>`; it wipes the implementation
    along with the mutant.
46. **Scope a mutation regex to the function you mean to break.** `ExaminedInput`'s and
    `SummaryInput`'s guards are character-identical, so a non-anchored substitution hits the first
    one and mutates a function the test does not cover — a green run that reads as "the mutant
    survived" when it was never made. Verify with `git diff` which function you actually changed.
47. **Two hiddenness models live inside one tool.** Playwright's `toBeHidden` is bounding-box
    visibility; the role engine uses ARIA hiddenness. A zero-size element (`w-0 h-0
    overflow-hidden`) is Playwright-hidden and ARIA-present at once — which is how Task 8's
    "this assertion has no mutant of its own" claim was measured false, by ten lines of probe.
    `toBeHidden` also passes on a locator resolving to **no node at all**, so it cannot
    distinguish "hidden by CSS" from "deleted outright".
48. **`payload: null` really is reachable** (Task 3). `json.Unmarshal` of the literal `null` nils a
    map, and `jsonb NOT NULL` permits `'null'::jsonb`. Guarded at the service boundary, with a
    subtest that compares **raw bytes** — the only way to tell `{}` from `null`. The artifact path
    was the anomaly: `run_events.payload` and `tasks.inputs` both normalize on their sole
    non-test write path.
49. **A Postgres fixture trap, confirmed real** (Task 4): `now()` is `transaction_timestamp()`, so
    seven separate `UPDATE`s put a "same timestamp" pair microseconds apart and the keyset mutant
    survives. Staged in one transaction with a permanent DB-querying fixture guard, and proved
    load-bearing both ways.
50. **A grep for a key name finds readers of a key with that name, which is not the same set as
    consumers of the thing under test** (Task 6). The claim "`content` is asserted in five call
    sites" travelled from a reviewer, through the controller, into an implementer's code comment,
    and was false in every direction: the fake's own suite compared two fake emissions to each
    other (so a deleted key degraded to `nil == nil` and passed silently), and every other
    "reader" scripted its own payload and never touched the fake. Deleting the key and running the
    suite is what settled it. **Check a claim about the test suite regardless of who it came
    from — it is one command away.**

## Deferred minors, for the final whole-branch review to triage

Every `minor (deferred)` this plan produced, one per line, un-rolled-up on purpose: a roll-up
nobody reads is a silent discard. Items already fixed in a fix round are not here.

**Task 1**
- `compile_internal_test.go:110-114` header claims read-path helper tests that did not exist yet (they arrived with Task 4) — recheck whether the sentence is now true.
- `compile.go:143` cites `workflows/validate.go:207` (the `case NodeGate:` line) rather than `:208`/`:211` where the checks are.
- `compile.go:143` "At most one can exist" is an upstream invariant `Compile` does not re-check; two verdict edges on a corrupt definition silently yield the sort-first one.
- `compile_internal_test.go:159` discards `ok` from `ExaminedInput` (plan-mandated, harmless on this fixture).
- `SummaryInput` duplicates `ExaminedInput`'s guard verbatim (~5 of 11 lines); the reviewer recommends leaving it — the two have deliberately different false semantics.
- `task-1-report.md`'s mutation records are abridged rather than transcripts (the reviewer reproduced all four independently).

**Task 2**
- `gateevidence_integration_test.go:123-127`'s new oracle accepts any non-200 where every other oracle in the file pins an exact status.
- `api_integration_test.go` is now 1743 lines; a `harness_test.go` split was deliberately deferred out of the fix round.
- Fix-round mutation records elide the mutating command; a legibility regression, folded into the constraints for later tasks.
- **Roadmap item for sofo:** nothing repairs or sweeps a gate stuck in the `evidence_missing` state. It sits in the inbox forever, undecidable. Not 12a's scope.

**Task 3**
- **Deferred to final review:** `unmarshalMap`'s doc comment ("tolerating NULL/empty") overpromises for the `null` literal — either fix the helper or document that every write path keeps its own guard.
- `task-3-report.md:254`'s wave-1 handoff still carried the false mechanism after the implementer reported it corrected in place (workspace doc only).

**Task 4**
- **Deferred to final review:** no index serves `ORDER BY (created_at DESC, id DESC)` on `runs` — every page of a deliberately-paginated endpoint is a scan-and-sort. Needs a migration, outside the task's file list.
- Adapter `RepoSlugs`/`WorkflowNames` are per-id loops (up to 200+400 round trips at `limit=200`); the brief's own code, with the batch ceiling documented behind the same port.
- `api_integration_test.go:115-131` — a doc comment is orphaned onto `labelsUnavailable`, leaving the three port methods undocumented.
- `spinePorts.Repo` returns `"spine/repo"` for **every** repo id including `repoB` (real row `"spine/repo-b"`), so a future cross-project label assertion cannot use this double as-is.

**Task 5**
- A `nullableRefKeys` entry naming a key `RequiredPayloadKeys` does not contain is inert but **armed**: if that key is later added, a stale exemption activates unre-justified.
- **Deferred to sofo/roadmap:** there is still no `go mod tidy` lane in `Taskfile.yml`, `scripts/gate` or `.github/workflows`. The gap that let `// indirect` through is open.

**Task 6**
- `fake/executor.go:248-249` — the comment's closing sentence points at "the first sentence" as what the pin makes true going forward, but that sentence's content-specific clause is exactly what the pin falsifies; the intended referent was deleted in the same commit.
- **PROMOTE:** `TestDelayedRetry_ParkedRowSurvivesRestart` is now twice-observed failing under gate load on two different plans; the suite's seven 5–20s wall-clock deadlines are the standing cause.
- **Deferred to final review (a):** `fake/executor.go:204` keys `lastInstructions` on `req.Ref.NodeID` while `nodeKey(ref)` exists ten lines above and keys `execCount` by run+node — two runs sharing one executor collide silently.
- **Deferred to final review (b):** the "was not stored (%d artifacts)" message fires on `n != 1`, so a transient mutant printed "was not stored (3 artifacts)".
- **Deferred to final review (c):** `payload.go` keys the output-shape instruction on **node kind**, so an implement node declaring a plan artifact at schema 2 is validated against a shape it was never told — `collect.go`'s allowlist permits that declaration, so a previously-succeeding attempt becomes a permanent failure. The last residue of the problem Task 6 exists to eliminate.
- **Deferred to final review (d):** the review block's example is not valid JSON (`"approve" | "request_changes" | "reject"`), and that text ships to real models.

**Task 7**
- `realtimeports_integration_test.go:176-178` states a false claim — "no orchestrator ever loads these runs". It does; the accurate sentence is also what makes the isolation argument above it true.
- A RED run of `TestStream_CarriesAnOrchestratedPhaseTransition` costs the full 60s deadline (measured 61.9s) where the frame arrives in under 1s; 15–20s would still be ~300x headroom and fail 3x faster under gate load.
- `ApiArtifactPayloadResponse` is missing from `slice_test.go`'s `wantRequired` map (16 schemas named, it is not one).

**Task 8**
- `nav.tsx:73-105` — the two `<Link>` blocks are near-verbatim 7-liners and the badge condition is duplicated character-for-character; a third presentation added later can silently omit it, which is the exact bug the badge mutant exists to catch. A local `NavLink` removes it without abstracting anything speculative.
- `app-shell.test.tsx:131` — `keeps one navigation landmark` sits inside `describe('the phone tab bar')` while the claim is about `Nav` as a whole.
- `routes/_app/more.tsx` renders a bare `<ul>` with no page heading, so the fifth tab lands on an unlabelled screen — and `/more` is a real page, not a stub.
- `e2e/approvals.spec.ts:39`'s badge `toHaveCount(0)` shares an earlier assertion's shape and also passes if phone-tabs disappears; another assertion covers that, so no separate fix.
- `e2e/approvals.spec.ts:31` — "so it must follow (1)" is a necessity claim with zero execution weight; "so it proves nothing without (1)" would be exact.
- `e2e/approvals.spec.ts:28` — "a CSS selector, which reads the DOM and asks nothing else" stopped being literally true in the same commit that scoped the locator to the nav role.
- `e2e/approvals.spec.ts:20-21` — "the rail is `display:none`" is code-true and test-unenforced (a `visibility:hidden` rail passes all three assertions). One word: "hidden".

**Task 9**
- `run-query.ts:15` — "`['runs']` goes out on every frame" is imprecise rather than false: it goes out once per **window**, and only windows armed by routable frames exist. "on every window, whatever the kind" would be exact.
- **Noted, no action:** `cache-bridge.ts:101` drops an unroutable frame *before* the kind check, so a `gate.waiting` frame with no `run_id` would no longer move the inbox. A narrowing of Plan 11 behaviour on a case the contract says cannot occur; deliberate and documented.
- **Note, no rating:** `render-app.tsx:79-80`'s "the empty case" means the `runs: null` shape, but on today's wire the empty case is `[]` — referent loose, logic sound. And `run-query.ts:19` cites `approvals-query.ts` by bare name from a different directory.

**Task 10**
- `decode-patch.ts:17` — `EMPTY` is a shared module-level object returned by identity from two paths, so every empty or undecodable patch aliases one `files` array. Safe under today's read-only render; `Object.freeze` or a fresh literal removes the hazard.
- Mutant 1's parenthetical in the report is inference rather than transcript (the run stops at the added assertion); the reviewer reproduced the numbers, so the substance is right and the record should have been a measured line.
- An empty line arriving while content is owed (producers emitting bare `''` instead of `' '` for empty context) zeroes the debt via the fallthrough and degrades to the round-0 classification — same net kind, one fewer count consumed than git intends. Untested, unclaimed by any comment, outside the ruled fix.

**Task 11**
- Deadline-first suppression makes the client's clock a suppressor with no override — brief-mandated, and `GateEvidenceResponse`'s own doc comment argues the opposite design for `can_decide` itself (computed server-side, in `decideGate`'s order, so it names the refusal the caller would actually receive). **Parked for the final review / sofo.** Its supporting comment also cites `gates.go` where the `not_approver` caller is in fact refused earlier, in `api.go`, before `DecideGate` is entered.
- `ConflictPanel`'s `isPending` arm renders text no assertion names.
- The full vitest run prints "Not implemented: Window's scrollTo()" several times — pre-existing; suite output should be pristine.
- **Follow-up for a later task:** the 8192-byte gate note renders in a single-line input; a `TextAreaField` registry entry is wanted.

**Task 12**
- The typo'd-deep-link branch and the evidence-null/decision-null branch are honest but untested — coverage, not correctness. Only the summary/facts pair is position-pinned; accordion and controls are presence-only.
- **UX call for sofo / final review:** there is no back-link from the full-screen approval surface to `/approvals`, and Task 16's host does not supply one either.
- The renamed verdict-404 test proves "keeps the diff" only via hard-facts "2 files"; no assertion pins the file accordion surviving that state. One `getByTestId` would close it.

**Task 16**
- **Plan-level, sofo's call at final review:** nothing proves the desktop rail paints **left**. jsdom loads no Tailwind and the tests pin structure rather than geometry; the only falsifier is a desktop Playwright project, which `playwright.config.ts` deliberately refuses ("would double the plan's slowest lane"). Three geometry assertions across the plan wait on this decision.
- `RunHeader`'s raw-list-versus-spine argument is a design claim with **no mutant** — the reviewer measured the swap passing 25/25. A retried-earlier-node fixture plus one header assertion closes it.
- The `pushed` and published-without-PR arms render untested prose (compile-checked exhaustive, so they cannot vanish silently; the one place a rendered sentence has no assertion).

**Task 13**
- `StatusLine` renders "could not be read" for `undefined`, which also means in-flight — unreachable today, but `Unread` and `StatusLine` disagree about what `undefined` means.
- Asymmetric degradation: a run-list failure or empty state removes the "Running" and "Recent" headings entirely, and the state-first order claim is only tested on the populated path.
- `run-card.tsx:19` — "the only card Home, the runs list and Tasks use" was present tense about Task 14's then-unbuilt Tasks screen; true now, worth confirming.
- `phase-dots.tsx:59-67` carries the meta-claim "nothing here is asserted by a test" — true today, goes false silently the moment anyone asserts a class.
- `run-card.tsx:14` cites `dto.go:97-102`; the real range is `:99-103`.
- Home's non-suspension is guarded only by a test whose failure mode is a 44s worker OOM; a direct render-through-failure assertion would be the cheaper pin.
- The status line counts the whole inbox as "waiting on you", including rows the session cannot decide (any-grant listing versus `GrantApprove`).
- `?status=<unknown>` 400s and the `_app` boundary replaces the whole list including the filter chips; and `runs.index.tsx` still suspends, which inherits the redirect loop **iff** `/runs` ever becomes a redirect target.

**Task 14**
- The "both are needed" sentence in `tasks.test.tsx` is hypothetical; naming the mutant that kills only the screen test would make it measured.
- `task-groups.ts:53-54`'s "on four of the seven" is prose about the mapping — the transcript aborts at the first orphaned status, one failure per test rather than four.
- The `scrollTo` noise: "pre-existing" now understates the volume. `vaul` is a second producer, ~9 lines across five drawer tests. Not stubbed deliberately — jsdom defines-and-throws, so suppression means an unguarded global overwrite.
- `setup.ts:76`'s polyfill guard tests one of the three methods it installs; a jsdom shipping `setPointerCapture` alone leaves two absent.
- Drawer open state is React-only against the URL-as-state constraint. The constraint names filters, tabs and the open gate, and a transient creation sheet is arguably none of them — flagged so the choice is deliberate.
- The FAB's 56px / `bottom-20` visual claims are unguarded (no desktop Playwright; e2e never opens this screen), and whether "New task" fits inside a 56px circle only a screenshot settles.
- `tasks.tsx` is 203 lines (route + `Tasks` + `TaskSection` + `TaskRow` + `NewTaskFab`); `NewTaskFab` could sit in `tasks/` beside `new-task-form.tsx`. Taste; the plan specified no home.

**Task 15**
- The `/more` guard test never asserts `pathname === '/login'` though its name says so; the sign-in button carries the destination in practice. Cosmetic.
- **Report-file only:** `task-15-report.md` §5.2 credits a controller citation correction to a brief header section that does not exist. The correction was real but arrived in the dispatch prompt; no tree change needed.

**Task 17**
- **Triage question:** `Reject` is the one gate decision no e2e walks. The spec now says so truthfully; whether the third decision deserves a spec is a call for this review.
- M2b's transcript shows only the `phase-impl` half failing (Playwright aborts at the first failed await); the `phase-gate` kill is entailed rather than transcribed.
- "gate row not in the DOM when this starts" is true in every observed run but rides the loader-fetch-versus-orchestrator race, not a scheduler guarantee — the same character as the lane's acknowledged live-advance raciness.
- The `webe2e` forge stub answers **every** request with the head SHA, ignoring method and path. The header's "the one outbound call" scoping is accurate today and fragile if the harness gains a second outbound call.

## Sofo's rulings

Each with the one-line reason it was decided that way.

- **2026-08-16, Task 6's inverted assertion** — a payload-rejected artifact **is** persisted, and Step 1's assertion is inverted; no suppression filter. *The artifact is evidence of what the harness produced, and hiding it hides the diagnosis.*
- **2026-08-16, wave 1 order** — Task 16 moves ahead of Task 13: `8, 9, 10, 11, 12, 16, 13, 14, 15, 17`, with 15 staying adjacent to 14. *Home's cards need the run-detail destination to exist before they link to it.*
- **2026-08-16, Task 8's badge** — the badge renders in **both** presentations, queries scope by presentation, and the two affected suites are fixed in the same commit. *Which presentation a person sees is a viewport's decision, so a badge in only one is a badge missing on one of the two surfaces.*
- **2026-08-16, Task 4's DTO** — `RunResponse` gains `task_title`, `repo_slug` and `workflow_name`, plus the `required:"true"` markup sweep. *The run header has no other source for them, and discovering that in the web slice would reopen a closed contract.*
- **2026-08-16, Task 2's third Important** — add a fourth `cannot_decide_reason`, `evidence_missing`, server-side before wave 0 closes. *`can_decide` was true on a gate where `decideGate` answers 500, which is a refusal the client could not explain.*
- **2026-08-22, Task 10's parser** — **fix it**; the reviewer's count-tracking parser governs the plan text. *A deleted line beginning `-- ` closes the hunk and pushes a phantom file, and this repo's own migrations and `queries.sql` are full of `-- ` comments.*
- **2026-08-22, Task 16's desktop two-pane** — **build now**, as a scope addition before review. *decisions §7 locks it and, unlike cancel, retry and live output, it had no deliberate-cut rationale anywhere; the implementer still held the context.*
- **2026-08-22, Task 13's loader** — the deviation is **ratified**: per-section degradation stands over the brief's suspending loader. *Both the mutant and the independent review show the plan's shape hangs the app in an unbounded redirect loop.*
- **2026-08-22, Task 14's stale test prose** — **accept the deferral** for exactly one commit. *The plan's own sequencing puts Task 15's rehoming next with nothing between, and the test stays falsifiable meanwhile.*
- **2026-08-22, Task 15** — (1) the `/more` deviation is **ratified**, both guard tests stay; (2) one fix round, to rename the first test truthfully plus a ride-along count correction. *The dictated "exactly once" loses to the measurement, and four loader-free routes remain, so the case was never lost.*
- **2026-08-22, Task 17** — (1) D1's harness change is **ratified**: the real bare repo and the new `git`-on-`PATH` dependency stand; (2) one fix round for the brief-dictated false comment, with ride-alongs. *A fake that differs from the real thing in the field under test is not a fake, it is a different subject.*
- **2026-08-22, Task 18's ordering** — the plan's Task-18-before-final-review order is **impossible as written** and is reordered: 18a (this documentation) now, then the final whole-branch review, then the merge, then the operator checkpoint against `main`'s published digest. *`image`, `image-serves-ui` and `publish` run only on push to `main` (`ci.yml:185`) and the branch has never been pushed, so Step 1's tested digest cannot exist pre-merge.*

## Plan divergences, corrected in the plan text

The rule this project follows: when execution proves a plan claim wrong, **correct the plan**,
because the next person to execute it will otherwise reintroduce exactly what was just removed.
Each site below carries a `(corrected in execution — see ledger)` marker in
`docs/superpowers/plans/2026-08-16-caesar-12a-web-critical-path.md`; none of the original text was
silently rewritten.

- **Task 13's Home loader.** The `Promise.all` + suspense shape is marked as what hangs the app,
  with the redirect-loop mechanism, the measured 44s OOM, and the sofo ratification of per-section
  degradation.
- **Task 15's premise, in three places.** The section header's "plausibly no loader-free route
  remains"; the dictated test title's "exactly once" (a redirect count no assertion separates from
  what the test does prove); and `toBeLessThanOrEqual(1)`, which is a dead bound because the child
  loader really does run — measured at exactly 1. The `/more` second test is now written out, and
  the "update the file's leading comment" instruction is corrected to point at the right comment.
- **Task 15's Proof 3.** Marked unsatisfiable in every form, with the router-source reason
  (`followRedirect` hard-sets `replace: true` over a redirect's options), and Proof 1's stderr
  attribution corrected from `/tasks`' loader to `AppLayout`'s `useSuspenseQuery`.
- **Task 16's Files and Interfaces.** The real runs list moved to Task 13 under the reorder, and
  the desktop two-pane — locked in decisions §7, absent from every brief — was built here under a
  sofo ruling. The `Consumes: PhaseDots / RunCard` line is corrected: run detail consumes neither,
  and that line is the origin of the false framing that reached Task 17's spec comment.
- **Task 17's `PhaseDots` framing, in two places.** Step 3's timeline comment (the watched row is
  not a dot; `ApiPhaseResponse.status` is non-nullable; the row renders its status as visible
  text) and Step 6's Path-2 mutant, which **survives** because `PhaseDots` is not on that path at
  all.
- **Task 17's fixture.** The task intro's "`runs.Executor` faked and nothing else" now names the
  forge stub and the real bare repository; Step 1 carries the D1 mechanism, the `git`-on-`PATH`
  dependency, and the isolation work (`quiesce` + `workers: 1`) the step assumed it already had.
  Step 2's beat-4 `getByText(/^\+/).first()` is marked vacuous, with the reason.
- **Home's "Recent, newest first".** True of nothing the server sends: the page arrives ordered by
  `created_at` (when a run was *requested*), and the shipped section re-sorts one page by the
  timestamp the card itself shows.

**One claim in this task's own brief did not survive checking, and the plan was left alone.** The
brief listed "§6.3's newest-first event log" as a plan claim to correct. It is not one. The plan
says "flat chronological log" in every place it describes the event log (Task 16's prose, its test
name, and its Step-6 mutant), and its three "newest first" occurrences are all about the **run
list**, which really is `ORDER BY created_at DESC, id DESC`. The false sentence lived in Task 16's
*report* §6.3 and was already controller-corrected there on 2026-08-22. Verified against
`store/queries.sql`: `ListRunEventsAfter` and `ListRunEventsForRunAfter` are both `ORDER BY id`
ascending, consistent with the `after` resume cursor.

## Operator TODO — NOT DONE

**Everything in this section is Task 18 Steps 1–4 and none of it has happened.** It is sofo's, at
a keyboard and then at a phone, **after the branch merges**. A checkpoint conducted against mocked
data cannot produce the taste feedback it exists for, and nothing an agent can run substitutes for
it.

### 1. The deployed digest — NOT RECORDED

Use the release path Plan 11 built and measured: build once, test **that** digest with
`image-serves-ui`, promote **that** digest. Do not build a second image for the deploy.

```
gh run list --branch main --limit 5
# read the publish job log for the digest that image-serves-ui tested
```

`gh api user/packages/...` needs a `read:packages` scope the local token lacks — read the publish
job log rather than trying to list GHCR versions.

> **Digest:** _not recorded._
> **Deployed at:** _not recorded._

### 2. The seven-step phone walk — NOT WALKED

Against real data, on the real device. In order, timing each step. The timings are the
deliverable, not a formality: they are the only evidence about whether the flow is usable on a
phone.

| # | Step | Time | Notes |
|---|---|---|---|
| 1 | Create a task from the FAB | _not walked_ | |
| 2 | Watch its run advance without touching the screen | _not walked_ | |
| 3 | Open the waiting gate from Home | _not walked_ | |
| 4 | Read the examined artifact | _not walked_ | |
| 5 | Request changes with a note; confirm the new generation appears | _not walked_ | |
| 6 | Approve the next gate | _not walked_ | |
| 7 | Open the PR from the publish phase | _not walked_ | |

### 3. The checkpoint questions — UNANSWERED

Each is a decision the plan deliberately deferred to evidence rather than argument. The answers go
here, in this file.

- **Does Home's inline approve get used, or is the run opened first every time?** (§10.2, §11.) It
  ships because it is locked and because this is the right instrument for the question. If the
  answer is "always opened first", the card's buttons come off **with evidence behind the
  removal**. — _unanswered._
- **What should the sticky header offer?** (§10.3, §11.) `rerunTask` ships today, so whole-task
  retry is a call rather than a feature; it stayed out so the checkpoint could decide the header as
  a whole. If the answer is "obviously retry", the cost of adding it was never the reason to wait.
  — _unanswered._
- **Is `phase.` on the global stream too chatty in practice?** (§11.) **Watch the network panel
  during step 2 and write down what you saw, not what you expect.** A run in flight emits far more
  phase transitions than gate or publish events, and the bridge's 250ms debounce is what keeps that
  from becoming a refetch storm. — _unmeasured._
- **Do the empty states read as empty or as broken?** Particularly "Up next", which is empty most
  of the time by construction on a single-operator system. — _unanswered._
- **What is missing that the mockups promised?** §10's twelve entries are the known list. Anything
  else found here is a thirteenth, and it belongs in 12b's spec. — _unanswered._

### 4. The roadmap row — NOT FLIPPED

**Only if steps 1–3 completed on a real device against real data.** If the deploy could not be
reached from a phone, the row is not done and this ledger says which step blocked it.

Read the note at the top of this file first: **the roadmap has no row 12a.** Row 12 is undivided
and covers 12b as well, so "flip row 12a" has no literal target today. Whether to split row 12 into
12a/12b, or to annotate row 12 as partially done, is a decision for the operator at this
checkpoint — flipping row 12 whole would claim work that has not started.

## What 12a did **not** close

Stated explicitly, because §1 of the design claims nothing is dropped silently.

- **Row 08 does not close here.** Paths 1 and 3 are the interface the checklist is walked on, not
  the event. Row 08 closes when every item of `backend/README.md`'s checklist is recorded against
  **both** forges — GitHub *and* GitLab, because the two adapters' response shapes and error
  mappings are invented until a live call proves them. That stays an operator event.
- **Row 12b is unstarted**: the Workflows outline editor, Repos, Admin, the Approvals history
  endpoint (§10.7), the task-board lifecycle problem (§10.8), and desktop's ⌘K (§10.9). **One
  correction to that list as the plan states it:** §10.9 defers *two* desktop behaviours, and 12a
  shipped one of them — run detail's split view landed in Task 16 (`d74dc32`) under a sofo ruling.
  Only ⌘K remains from §10.9.
- **Plan 11's one open operator item is still open** and unrelated: proving the `__Host-` session
  cookie survives the Vite dev proxy, and is *dropped* without it. `web/vite.config.ts` carries the
  procedure as **seven** numbered steps (the plan text says six), headed with the standing warning
  that the cookie claim is reasoned and not measured. If step 6 does **not** drop the cookie, the
  block's justification is wrong and should be rewritten rather than kept "just in case".
- **Cancel is row 09's.** `runs/tasks.go` refuses a rerun of a live run with "run is in flight;
  cancel arrives in Plan 09", which is why the sticky header carries neither cancel nor retry.
- **Live agent output needs its own design pass** (§10.4). No log-serving route exists; logs are
  quarantined and redacted precisely because secrets reach them, and serving them is a safety
  decision. A live *phase-status* spine is buildable and 12a builds it; the terminal box is not.
- **`GET /tasks` and `GET /approvals` remain unpaginated** — correct at month-one volume, a cliff
  later, named so the next person meets it deliberately.
