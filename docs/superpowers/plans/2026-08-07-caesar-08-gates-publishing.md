# Plan 08: Gates & Publishing — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A run reaches a human gate and stops; the human approves, requests changes, or rejects; an approval mints a single-use ApprovedPatch that a `git-publish` node CAS-consumes to push a branch and open a pull request on GitHub or GitLab — crash-safe at every step, and a request-changes opens a bounded revision generation that rewinds the slice and re-decides.

**Architecture:** Four layers extend in lockstep. The **compiler** learns the two things the runtime needs from the already-validated document — which gate authorizes which publish, and what a gate's revision slice contains. The **spine** gains a non-executing phase state (`waiting_decision`), the gate-decision transaction (CAS + database-clock deadline predicate), the `approved_patches` single-use authorization, generation rematerialization, and the `publish_intents` effect state machine whose every effect commits its receipt before the next call. **platform/forge** gains hand-rolled GitHub and GitLab REST clients (no SDKs); `gitops` gains `Publish` — a fresh trusted checkout, canonical-patch apply, pinned attribution, expected-remote-SHA push. **app** wires the real forge, so production task intake stops being refused.

**Tech Stack:** Go 1.26, Postgres 18 (sqlc v1.31.1, Atlas v0.34.1 + goose), stdlib `net/http` for the forge clients, `pgregory.net/rapid` where properties fit. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-02-caesar-v1-design.md` §2 (Definitions, Gate, Work products), §3 (Artifact flow, Publishing, Budgets), §4 (deciding mutations are CAS end-to-end), §6 (requester principal, `allow_self_approval`), §7 (golden set with crash injection). Roadmap row 08 in `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md`. **Milestone: month-one loop complete via API.**

## Baked decisions (flag these if the user pushes back)

1. **A gate is a phase that is never claimed.** Gate rows are inserted directly as `waiting_decision`, a status the claim CTE's `status = 'pending'` predicate cannot see. No worker, no lease, no heartbeat, no timeout. This also makes "the execution budget pauses at gates" (spec §3 Budgets) **fall out for free** — usage accrues only from `claimed_at` to evaluation, and a gate has no `claimed_at`. The wall clock keeps running, which is exactly what the spec asks for. No pause bookkeeping is added.

2. **Readiness becomes latest-attempt-wise, not any-attempt-wise.** `ListSucceededNodes` today returns every node with *a* succeeded row. After a revision rematerializes a node, that is wrong. It becomes "the node's newest attempt row is `succeeded`". This is the single change that makes generations work without generation-scoping every read, and it is a strict generalization: with no revisions, the newest row is the only terminal row.

   **This only works if a revision retires the succeeded rows in its slice.** Latest-attempt readiness forgets a node when a *newer* row exists that is not succeeded — so leaving a slice node's generation-1 success as its newest row keeps it readable as done forever. The consequence is not a stalled node but a silently wrong one: the revision target regenerates its patch, the intermediate node never re-runs, and the gate re-decides against the **previous generation's** review verdict. `SupersedePhasesInSlice` therefore covers `succeeded` as well as the live statuses, and `succeeded → superseded` is the one edge out of a terminal phase state in either transition table.

3. **`generation` is recorded, not load-bearing for dispatch.** `runs.generation`, `phase_runs.generation`, `artifacts.generation` are written and read (deterministic markers, approval invalidation, `max_rounds` accounting, the API projection, golden-set assertions) but readiness comes from decision 2, not from generation filters. One mechanism decides dispatch; the counter is provenance.

4. **The deterministic publish marker is `caesar/<approved-artifact-id>`.** Spec §3 says markers derive from the approved artifact id and target; the artifact id is a UUIDv7, unique per node per generation, so the branch name alone is a total key — recovery reconstructs it from the authorization row with zero ambiguity, and a new generation necessarily gets a new branch. Human readability lives in the PR title, not the ref.

5. **Single-use is enforced by a unique index, not only by CAS.** `publish_intents.authorization_id` is `UNIQUE NOT NULL`, and `approved_patches` carries a partial unique index on `(run_id, publish_node_id) WHERE status = 'valid'`. A second consumption is a constraint violation, not a lost race — the database is the authority (invariant 2).

6. **One intent per `(run, publish node)`, resumed across attempts — never one per attempt.** Retrying a publish must not mint a second authorization or a second branch. The intent row records the attempt that created it and is resumed by every later attempt; `attempt` is provenance, not identity.

7. **Attribution is author=human, committer=caesar, and both dates are pinned.** `GIT_AUTHOR_NAME` = the requester's email local-part, `GIT_AUTHOR_EMAIL` = the requester's email; `GIT_COMMITTER_NAME=caesar`, `GIT_COMMITTER_EMAIL=caesar@invalid`. Git's own author/committer split says "this human's intent, executed by this machine" natively. The message carries `caesar-run` / `caesar-node` / `caesar-artifact` / `caesar-approved-by` trailers — provenance, not attribution. **No `Co-authored-by` machine trailer is emitted**, deliberately.

   `GIT_AUTHOR_DATE` and `GIT_COMMITTER_DATE` are both pinned to the publish intent's `created_at`, and that is a correctness requirement rather than a tidiness one. A commit's sha hashes its identities *including their timestamps*, so an unpinned date makes rebuilding the same publication produce a different sha every second — which silently breaks decision 8's adopt path: the recovery comparison never matches and the retry force-pushes a duplicate commit over the one already on the remote. Pinning makes the sha a pure function of the request, which is what "recovery adopts rather than re-pushes" actually requires.

8. **`gitops.Publish` runs IN-PROCESS, not in the `caesar-git` container.** Extraction is containerized because it reads a `.git` the agent controlled (spec §3: "extraction trusts nothing the agent could write"). Publish has no such input: it clones the *remote* fresh at the pinned base SHA into a private temp dir under a constructed env (no ambient config, no helpers, no prompt — `gitops.baseEnv`), applies a canonical patch extraction already validated (gitlinks rejected, traversal bounded), commits, and pushes. Repo code is never executed. In exchange, publishing works with `runner.enabled=false` (the default posture) and the golden set exercises **the same function production does** rather than a parallel path — which is what "crash injection around every publish step" (spec §7) is worth. The function is shaped so a future `caesar publish` subcommand can wrap it unchanged if the threat model changes.

9. **`gitops.Publish` lives in `internal/runs/runner/gitops`** despite publishing not being a runner concern. That package is the one that drives git for this context and already owns `baseEnv`/`runGit`/`redact`; moving it would churn every Plan 06/07 import for no behavioural gain. The path is an accident of birth, noted in the package doc.

10. **Forge clients are hand-rolled `net/http` against pinned API versions** in `internal/platform/forge` (the `dockerapi` precedent): GitHub REST `2022-11-28` with `Accept: application/vnd.github+json`, GitLab REST `v4`. The package is pure infrastructure — it imports no bounded context (`CheckPlatformPurity`), takes a token per call, and `app` adapts it onto `runs.Forge`.

11. **Self-approval is a snapshot field, and the snapshot schema goes to 2.** `PolicySnapshot.AllowSelfApproval bool` defaults `true` (spec §6: solo-friendly shipped baseline). `snapshotFromRow` accepts a stored `schema: 1` document by defaulting the field to `true` — one explicit upgrade branch, tested — so Plan 07-era runs still load. Plan 09 replaces the *evaluation*, never the pinning.

12. **Exhausting `max_rounds` refuses the decision (409), it does not kill the run.** The human is told the revision loop is spent and must approve or reject. Killing the run on the Nth request-changes would destroy work over a click; leaving the gate waiting is bounded by the wall deadline anyway.

13. **A gate decision is recorded once per gate *attempt* row** (`gate_decisions.phase_run_id UNIQUE`). A rematerialized gate is a new attempt row and therefore decidable again; the same row is never decided twice, which is the CAS the API needs.

14. **Five outcome codes classify a forge answer, and three of them halt.** `push_rejected` (transient — the remote moved or the network blinked; retries within `max_attempts`) and `forge_unavailable` (transient — the API is down or throttling) are ordinary retries. Three are **permanent AND halting**, because each leaves something of ours half-done on the forge that no retry can finish: `publish_conflict` (recovery found a marker at a state we cannot prove is ours), `auth_expired` (the forge rejected the repository's credential — somebody must issue a new token), and `repo_unavailable` (the forge will not show the repository at all — deleted, renamed, or no longer visible to this token). In all three the intent is `halted` and a human reconciles. Recovery never guesses.

    **Amended during execution.** As written this decision named three codes with `publish_conflict` the only halting one, and framed them as "publish failures" generally. Both were wrong. The count is five and the scoping is narrower: a publish attempt can also end as `authorization_stale`, `publish_not_started` or `publish_resumable` (`internal/runs/outcome.go`), and none of those classifies anything the forge said — the first is decided before any forge call, the other two are crash-and-resume types. `repo_unavailable` was added in Task 12: decision 4 named no code for the forge's `ErrNotFound`, and mapping a vanished repository onto `forge_unavailable` would have retried it until the wall deadline while the pushed branch sat orphaned. It halts for the same reason `auth_expired` does. `haltsPublication` in `internal/runs/publish.go` is the single authority on the halting set, and `internal/app/runsports.go` is where a forge answer becomes one of these codes.

    "Ours" is checked on every field the intent pinned, not just the head sha. A pull request found on our marker must also target the branch the approval named — a head branch is not a unique description of a publication, and a second request opened from our marker to a different base would otherwise be adopted as ours and reported as the publication.

15. **Task intake stops refusing gates and publishes**, and `unavailableForge` is deleted. `ExecPlan.HasUnsupportedNodes` and its call site go with it — the roadmap's promise for this plan.

16. **Ownership of a ref is proved by rebuilding its commit, never by observing its sha.** A publication decides adopt-vs-halt by reconstructing the commit it would push and comparing shas — which only works because the date is pinned (decision 7). An observed remote head is *evidence*, never a lease value: passing one to `--force-with-lease` converts "somebody else's branch" into "a branch I may replace". Creating a marker uses an atomic **create-only** lease (`--force-with-lease=refs/heads/<b>:` with an empty expected value), because a plain push fast-forwards a branch that appeared between the probe and the push and reports success. The same rebuild, minus the push, is what lets expired-run settlement identify a receiptless branch while holding no authority to write.

17. **A revision may not rewind a run that has already published.** Chained gates each authorize their own publish, so an earlier publication can be live on the forge while a later gate still waits. Authorization invalidation cannot reach it — that token was *consumed*, and consumed rows are deliberately left alone — and nothing in a transaction can un-push a commit. So `request_changes` refuses (409) once any intent has reached `pushed`, `published` or `halted`, exactly as an exhausted `max_rounds` does: the human keeps approve and reject, and no work is destroyed. Refusing structurally in the validator was the alternative and is worse, since it would outlaw definitions that are fine whenever the earlier publish has not actually run.

18. **Artifact selection orders by provenance, not by time.** `GetNodeArtifact` orders by `generation DESC, attempt DESC` before any timestamp. Revision seeds carry the *database* clock and produced artifacts carry the *application* clock, so ordering on `created_at` means a host clock behind the database sorts a new patch behind its own seed — and the gate approves, and the publisher publishes, stale content. uuidv7 does not rescue it; the ids come from the same skewed clock.

19. **A publication that may have landed is a terminalization obligation.** The wall-deadline sweep may not terminalize a run holding a `pending` or `pushed` intent until it has asked the forge what exists and written the answer down. A cancelled push cannot prove the remote refused it, and `terminalizeRun` supersedes every live phase — so terminalizing first makes an orphaned branch permanent, with nothing that will ever look again. This is the same shape as Plan 07's credential obligation, and it fails the same way: an unreachable forge defers the run to the next tick rather than guessing, because this is the one path with no later corrector.

20. **Run status is read back from the database, never inferred from what a pass just did.** `materializeReady` asks `RunIsWaitingOnAGate` AFTER its inserts. "Did I insert a gate" gets a fan-out wrong in both directions: a gate materialized beside a runnable sibling parks the run early, and a sibling finishing beside an already-waiting gate parks it never — and since `FinishGatePhase` requires the run to be waiting, that second case leaves the gate undecidable until the wall deadline. `syncWaiting` moves the run in both directions so the two callers cannot drift.

21. **Repository coordinates are pinned into the authorization, not re-resolved.** `UpdateRepo` can change a repo's slug, default branch and credential mid-run, so `repo_id` is not an identity. `approved_patches` and `publish_intents` carry `forge_kind` and `repo_slug` as approved, revalidation compares every one of them, and a rename becomes a refusal instead of a patch delivered to a repository nobody approved.

22. **Receipt writes are monotonic and their row counts are checked.** A lease takeover cannot retract a forge request already in flight, so two drivers can be inside one publication. `status` only moves forward along pending → pushed → published, `published` is not haltable, and a lost CAS reloads and adopts the winner's row rather than failing the run.

22a. **A create-PR conflict is a recovery condition, not a verdict.** "Already exists" describes the world, not who is entitled to it — and the usual claimant is our own other driver. Excluding `published` from `HaltPublishIntent` does *not* close this: the dangerous window is before the winner's receipt commits, when the intent still says `pushed` and a halt would succeed, telling a human to reconcile a publication that worked. So the loser re-queries and adopts an exact match, halts only on a request provably not ours, and treats "the forge says it exists but will not list it" as an uncharged resumable.

23. **A pull request is adopted only on an exact three-way match.** `FindPullRequests` returns every candidate on the head branch, and `matchPublication` requires head branch, target base and head sha all to agree. A candidate missing a field is *unproved*, not *undisputed* — treating an empty base as "no disagreement" would make absent evidence into permission — so missing fields and multiple matches both halt.

24. **After its intent exists, a publication addresses only the intent's own coordinates.** `cp.repo` comes from the mutable assets row, so every probe, push and pull-request call goes through `intentRepo(intent)` instead — forge kind, slug, target branch and credential as pinned at authorization time. `authorizePublish` is the one place that compares `cp.repo` against the approval and refuses a rename; after that, `cp.repo` is never consulted again. Otherwise a restart mid-publication addresses whatever the repos table says now, and recovery probes the wrong repository and concludes the real branch does not exist.

25. **Absence never settles an intent on a single probe, and the marker it probes follows the intent's status.** A request issued just before the wall deadline can still be in flight when settlement looks. Halting on that first negative terminalizes the run, and then the effect lands — the orphan decision 19 exists to prevent, reintroduced through the negative case. Settlement requires **quiescence** *and* a **stable negative**: the first absent probe records `absent_since` and defers, only a later tick past `publishSettleGrace` may halt, and any probe that finds the marker clears it. Two details are easy to get wrong and both deadlock or orphan:
    - Quiescence means **no phase still `running`**, i.e. fenced — *not* "no live phase row". `settleRunCredentials` runs first and leaves phases in `reconciling`, which is non-terminal, so the obvious phrasing would defer forever on exactly the runs this exists for.
    - A `pending` intent's marker is the **branch**; a `pushed` intent's is the **pull request**. Probing only the branch settles a `pushed` intent while its create call is still on the wire.

26. **An unfinished publication is uncharged — and that is an invariant, not a tendency.** While an intent is `pending` or `pushed`, *every* error leaving `runPublish` is transient and uncharged. `ClassifyOutcome` is default-deny, so a raw error is permanent; `evaluate` terminalizes on a permanent outcome or an exhausted budget; and **no sweep revisits a failed run** (`ListExpiredRunIDs` matches only `pending`/`running`/`waiting`). So any breach makes an orphan permanent. Four places enforce it: `inspectPublication` returns `publish_resumable` rather than `Completed:false` (which `reconcileAttempt` would charge as `reconcile_lost`), `receiptFailure` types the transaction that commits an effect's receipt, `reload` types the position re-read, and `unchargedWhileUnfinished` stops a forge outage from spending the budget. With it, the wall deadline is the only path that can terminalize over an unfinished publication — and that path settles first.

27. **A 403 is not a synonym for "forbidden".** GitHub answers both primary and secondary rate limits with 403 *or* 429, so classifying every 403 permanent turns minutes of throttling into a failed run with a branch already pushed — and, on a secondary limit, re-requesting immediately extends the lockout. Retryability is decided by the rate-limit signals (`Retry-After`, `X-RateLimit-Remaining: 0` + `X-RateLimit-Reset`, the documented secondary-limit message), and a stated reset is carried through to `Outcome.RetryAt`, which `retryDelay` takes verbatim over its own curve.

## Global Constraints

- Go 1.26; module `github.com/giiorg/caesar/backend`; stdlib-first, **no new Go dependencies**.
- Postgres 18; migrations via `task db:new -- <name>` (Atlas diff from `internal/platform/pg/schema.sql`, goose format); `task db:sqlc` after any queries/schema change; `task contracts:check` must stay green (OpenAPI + TS client + sqlc regen-diff gates).
- Bounded contexts never import each other (`internal/arch/rules.go`); `runs` ↔ `assets` ↔ `workflows` meet only through ports declared in `internal/runs` and adapted in `internal/app`. `internal/platform/**` imports no business code (`CheckPlatformPurity`) — the forge clients therefore speak their own types.
- No exported context function mentions a pgx tx type (`txboundary` analyzer); mutating paths use `pg.WithTx` internally. The analyzer skips `_test.go`.
- **Every deciding predicate reads the database clock** (`now()`), never an app timestamp: the gate decision, the authorization consumption, and the publish-intent creation all carry the run's `wall_deadline > now()` predicate in the same statement that mutates (spec §3 Budgets).
- Stubs return typed `NotImplemented`, never fake success (`stubfake` analyzer, invariant 13). No dead config (`deadconfig`): every new config field has a read site.
- `credential.lease_released` **already exists** as an audit action since Plan 07 Task 4 — do not implement a duplicate.
- Plan 07's parked "Group C" residuals (the driver table not being ready for a third row) are **out of scope**: this plan adds no harness driver.
- All tests `-race`; integration behind `//go:build integration` (real Postgres via testcontainers); no test may make a live forge call — the forge clients are tested against `httptest` servers, and the golden set against local bare repos.
- Commit style: `<type>(<scope>): <description>`, one sentence, no body, no AI attribution.
- Full `task ci` green at plan end. `task backend:test:runner` (Plan 06/07's Docker lane) must stay green — this plan does not touch it, and a regression there is a real one. `task ci` cannot go green from a linked worktree (its `secrets` lane mounts `ROOT_DIR` and a worktree's `.git` is a file pointing outside that mount); run gitleaks via the parent-repo absolute mount instead.

## File Structure (created / modified)

**Spine (`internal/runs`):**
- `status.go` — `PhaseWaitingDecision`, widened `runTransitions`/`phaseTransitions`
- `outcome.go` — five publish/gate outcome codes
- `events.go` — `gate.*`, `publish.*`, `run.revision_opened` kinds
- `policy.go` — `AllowSelfApproval`, snapshot schema 2 + v1 upgrade branch
- `compile.go` — `PlanNode.Revision`, `ExecPlan.GateFor`, `ExecPlan.RevisionSlice`, deterministic `passThrough`, `HasUnsupportedNodes` **deleted**
- `gates.go` (new) — the decision transaction, `max_rounds`, self-approval, the ApprovedPatch mint
- `revisions.go` (new) — generation rematerialization
- `publish.go` (new) — authorize → push → PR, receipts, recovery, halt
- `ports.go` — `Forge` growth, `GitPublisher`, `PrincipalSource`
- `tasks.go` — intake accepts gates/publishes; `Rerun` 23505 classification
- `orchestrator.go` — gate materialization, publish dispatch, `waiting` run status
- `queue.go`, `store/queries.sql` — latest-attempt readiness, gate insert, decision CAS
- `api/api.go`, `api/dto.go` — decision endpoint, approvals list, projections
- `fake/forge.go` — scriptable forge with crash injection; `fake/publisher.go` (new)

**Schema:** `internal/platform/pg/schema.sql` + one migration — `gate_decisions`, `approved_patches`, `publish_intents`, `runs.generation`, `phase_runs.generation`, `artifacts.generation`, widened status/type CHECKs.

**platform:** `internal/platform/forge/forge.go`, `github.go`, `gitlab.go` (new).

**gitops:** `internal/runs/runner/gitops/publish.go` (new).

**app:** `runsports.go` (real forge adapter, git publisher, principal source, `unavailableForge` deleted), `runnerports.go` (`cloneURL` reads the configured clone bases), `serve.go` (wiring), `config.go` (publish workdir).

**Docs/CI:** `backend/README.md`, roadmap row 08, this plan committed (Task 15).

---
### Task 1: Gate & publish vocabulary — statuses, outcomes, events, self-approval policy

Everything later tasks name. Pure data plus two transition tables; no I/O, no schema yet.

**Files:**
- Modify: `backend/internal/runs/status.go` (`RunStatus` :11-43, `PhaseStatus` :48-80)
- Modify: `backend/internal/runs/outcome.go` (codes :22-52)
- Modify: `backend/internal/runs/events.go` (kinds :17-32)
- Modify: `backend/internal/runs/policy.go` (`snapshotSchemaVersion` :11, `PolicySnapshot` :19-24, `DefaultSnapshot` :27-34, `Validate` :36-51)
- Modify: `backend/internal/runs/service.go` (`snapshotFromRow` :90-96)
- Test: `backend/internal/runs/status_test.go`, `outcome_test.go`, `policy_test.go`

**Interfaces:**
- Consumes: the existing `RunStatus` / `PhaseStatus` / `Outcome` / `PolicySnapshot` types.
- Produces (later tasks rely on these exact names):
  - `runs.PhaseWaitingDecision PhaseStatus = "waiting_decision"`
  - `runs.CodeGateRejected`, `CodeRevisionRoundsExhausted`, `CodeAuthorizationStale`, `CodePublishConflict`, `CodePushRejected`, `CodeForgeUnavailable`
  - `runs.EventGateWaiting`, `EventGateDecided`, `EventRunRevisionOpened`, `EventPublishIntentCreated`, `EventPublishPushed`, `EventPublishPROpened`, `EventPublishAdopted`, `EventPublishHalted`
  - `runs.PolicySnapshot.AllowSelfApproval bool` and `snapshotSchemaVersion = 2`

- [ ] **Step 1: Write the failing status tests**

In `backend/internal/runs/status_test.go`:

```go
func TestPhaseStatus_WaitingDecisionIsLiveAndDecidable(t *testing.T) {
	if !PhaseWaitingDecision.Valid() {
		t.Fatal("waiting_decision must be a valid phase status")
	}
	if PhaseWaitingDecision.Terminal() {
		t.Fatal("waiting_decision is live: a human has not decided yet")
	}
	for _, to := range []PhaseStatus{PhaseSucceeded, PhaseFailed, PhaseDead, PhaseSuperseded} {
		if !PhaseWaitingDecision.CanTransition(to) {
			t.Errorf("waiting_decision -> %s must be legal", to)
		}
	}
	// A gate is never claimed: no worker ever moves it to running, and
	// nothing reconciles a row that holds no lease.
	for _, to := range []PhaseStatus{PhaseRunning, PhaseReconciling, PhasePending} {
		if PhaseWaitingDecision.CanTransition(to) {
			t.Errorf("waiting_decision -> %s must be illegal", to)
		}
	}
	// Nothing transitions INTO waiting_decision either: the row is inserted
	// in that state by gate materialization.
	for from, tos := range phaseTransitions {
		for _, to := range tos {
			if to == PhaseWaitingDecision {
				t.Errorf("%s -> waiting_decision must be illegal; gates are inserted waiting", from)
			}
		}
	}
}

func TestPhaseStatus_ARevisionMaySupersedeASucceededAttempt(t *testing.T) {
	// Readiness is the node's NEWEST attempt row (decision 2). A revision
	// that rewinds a slice must therefore be able to retire a row that
	// SUCCEEDED in the prior generation — otherwise that row stays newest
	// and forever readable as done, and the slice's downstream nodes never
	// re-run. This is the one edge out of a terminal phase state.
	if !PhaseSucceeded.CanTransition(PhaseSuperseded) {
		t.Fatal("succeeded -> superseded must be legal; a revision retires the whole slice")
	}
	for _, to := range []PhaseStatus{PhasePending, PhaseRunning, PhaseReconciling, PhaseFailed, PhaseDead} {
		if PhaseSucceeded.CanTransition(to) {
			t.Errorf("succeeded -> %s must stay illegal; only a revision may retire a success", to)
		}
	}
}

func TestRunStatus_WaitingTerminalizesOnRejectAndCompletesOnApprove(t *testing.T) {
	for _, to := range []RunStatus{RunRunning, RunSucceeded, RunFailed, RunDead} {
		if !RunWaiting.CanTransition(to) {
			t.Errorf("waiting -> %s must be legal", to)
		}
	}
	if RunWaiting.CanTransition(RunSuperseded) {
		t.Error("waiting -> superseded must be illegal: supersede-before-work only touches pending runs")
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/runs/ -run 'TestPhaseStatus_WaitingDecision|TestRunStatus_Waiting' -v`
Expected: FAIL — `undefined: PhaseWaitingDecision`.

- [ ] **Step 3: Add the statuses and widen both transition tables**

In `status.go`, add the constant and extend the tables:

```go
const (
	PhasePending PhaseStatus = "pending"
	PhaseRunning PhaseStatus = "running"
	// PhaseWaitingDecision is a gate: a phase whose completion is a human
	// decision (spec §2). Rows are INSERTED in this state — the claim CTE
	// predicates on 'pending', so no worker can ever pick one up, and with
	// no worker there is no lease, no heartbeat and no timeout. The wall
	// deadline is the only clock that still applies.
	PhaseWaitingDecision PhaseStatus = "waiting_decision"
	PhaseReconciling     PhaseStatus = "reconciling"
	PhaseSucceeded       PhaseStatus = "succeeded"
	PhaseFailed          PhaseStatus = "failed"
	PhaseDead            PhaseStatus = "dead"
	PhaseSuperseded      PhaseStatus = "superseded"
)

var phaseTransitions = map[PhaseStatus][]PhaseStatus{
	PhasePending:         {PhaseRunning, PhaseSuperseded},
	PhaseRunning:         {PhaseSucceeded, PhaseFailed, PhaseDead, PhaseReconciling, PhaseSuperseded},
	PhaseReconciling:     {PhaseSucceeded, PhaseFailed, PhaseDead, PhaseSuperseded},
	PhaseWaitingDecision: {PhaseSucceeded, PhaseDead, PhaseSuperseded, PhaseFailed},
	// A revision supersedes the WHOLE slice, succeeded rows included. This
	// is the only edge out of a terminal state in either table, and it is
	// what makes latest-attempt readiness (decision 2) actually forget a
	// node the new generation must re-run: leaving a succeeded row as the
	// node's newest row would keep it readable as done forever.
	PhaseSucceeded: {PhaseSuperseded},
}
```

Add `PhaseWaitingDecision` to the `Valid()` switch. Leave `Terminal()` alone — it lists the terminal four, and waiting is not one of them. `PhaseSucceeded` stays terminal: `Terminal()` answers "is this attempt over", which it is; the new edge only says a later generation may retire it.

For runs, waiting must be able to end the run directly: a gate can be the last node (approve → succeeded) and a reject terminalizes from waiting (→ failed).

```go
var runTransitions = map[RunStatus][]RunStatus{
	RunPending: {RunRunning, RunSuperseded, RunDead},
	RunRunning: {RunWaiting, RunSucceeded, RunFailed, RunDead},
	RunWaiting: {RunRunning, RunSucceeded, RunFailed, RunDead},
}
```

- [ ] **Step 4: Run the status tests**

Run: `go test ./internal/runs/ -run 'TestPhaseStatus|TestRunStatus' -v`
Expected: PASS.

- [ ] **Step 5: Extend `phaseIsLive` so the credential janitor keeps ignoring gates**

`orchestrator.go:469` answers "does the attempt's own machinery still own it". A gate has no machinery — but it is also not finished, and the stale-credential janitor must not reconcile a credential against a phase that never held one. A gate never holds a credential (it runs no container), so the honest answer is that `phaseIsLive` is about *lease holders* and gates are not lease holders. Add the comment; the function body is unchanged:

```go
// phaseIsLive reports whether the attempt's own machinery still owns it: a
// running phase heartbeats, a reconciling one is mid-recovery. Everything else
// — terminal, never claimed, or waiting on a human — has nobody left to
// conclude its credential. A gate is deliberately NOT live here: it never ran
// a container, so it never took a credential lease, and treating it as live
// would strand any lease whose ref collided with a gate node's.
func phaseIsLive(s PhaseStatus) bool {
	return s == PhaseRunning || s == PhaseReconciling
}
```

- [ ] **Step 6: Write the failing outcome test**

In `backend/internal/runs/outcome_test.go`:

```go
func TestPublishOutcomes_RetryabilityMatchesTheDecisionTable(t *testing.T) {
	cases := []struct {
		out           Outcome
		wantRetryable bool
		wantChargeable bool
	}{
		// A rejected push and an unreachable forge are worth another attempt.
		// They are chargeable only while NOTHING has landed: once an intent
		// is unfinished, Task 9's state machine reports publish_resumable
		// instead, and that is what keeps a forge outage from spending the
		// budget of a run whose branch is already pushed.
		{Outcome{Class: OutcomeTransient, Code: CodePushRejected}, true, true},
		{Outcome{Class: OutcomeTransient, Code: CodeForgeUnavailable}, true, true},
		// The two publish waits requeue WITHOUT charging: one publication
		// never began, the other is merely unfinished. Charging either would
		// let a lease hiccup or a forge outage exhaust max_attempts on a
		// publication that in fact succeeded.
		{Outcome{Class: OutcomeTransient, Code: CodePublishNotStarted}, true, false},
		{Outcome{Class: OutcomeTransient, Code: CodePublishResumable}, true, false},
		// A stale authorization, a halted publication and a human's reject
		// are decisions, not hiccups: retrying reaches the same answer.
		{Outcome{Class: OutcomePermanent, Code: CodeAuthorizationStale}, false, false},
		{Outcome{Class: OutcomePermanent, Code: CodePublishConflict}, false, false},
		{Outcome{Class: OutcomePermanent, Code: CodeGateRejected}, false, false},
		{Outcome{Class: OutcomePermanent, Code: CodeRevisionRoundsExhausted}, false, false},
	}
	for _, c := range cases {
		if got := c.out.Retryable(); got != c.wantRetryable {
			t.Errorf("Retryable(%s) = %v, want %v", c.out.Code, got, c.wantRetryable)
		}
		if got := c.out.Chargeable(); got != c.wantChargeable {
			t.Errorf("Chargeable(%s) = %v, want %v", c.out.Code, got, c.wantChargeable)
		}
	}
}
```

- [ ] **Step 7: Add the outcome codes**

In `outcome.go`, after the Plan 07 block:

```go
	// Plan 08: gate and publish outcomes (spec §2 Gate, §3 Publishing).
	CodeGateRejected            = "gate_rejected"             // a human ended the run at a gate
	CodeRevisionRoundsExhausted = "revision_rounds_exhausted"  // max_rounds spent; only approve/reject remain
	CodeAuthorizationStale      = "authorization_stale"        // the ApprovedPatch no longer matches what it authorized
	CodePublishConflict         = "publish_conflict"           // a marker exists in a state we cannot prove is ours
	CodePushRejected            = "push_rejected"              // the expected-remote-SHA precondition failed
	CodeForgeUnavailable        = "forge_unavailable"          // the forge API could not be reached or refused transiently
	CodePublishNotStarted       = "publish_not_started"        // transient, UNCHARGED: the worker died before authorizing
	CodePublishResumable        = "publish_resumable"          // transient, UNCHARGED: an unfinished publication resumes from its intent
```

The last two codes live here, in the vocabulary task, rather than beside the
code that first returns them (Tasks 9 and 10). That is not tidiness: Task 9's
fake publisher and receipt wrappers both reference `CodePublishResumable`, so
defining it in Task 10 would leave Task 9 unable to compile, breaking the
one-task-at-a-time rule this plan is built on.

`Chargeable()` DOES change, and the change is load-bearing:

```go
// Chargeable reports whether this failure spends the phase's failure budget.
// Cap waits, lease contention, a publication that never began and one that is
// merely unfinished are facts of life rather than errors: they requeue without
// charging (spec §2.2, §4.3). The two publish codes matter most — charging
// them would let a lease hiccup or a forge outage exhaust a node whose branch
// is already on the forge, terminalizing the run over work that succeeded.
func (o Outcome) Chargeable() bool {
	if o.Class != OutcomeTransient {
		return false
	}
	switch o.Code {
	case CodeRateLimited, CodeCredentialBusy, CodePublishNotStarted, CodePublishResumable:
		return false
	}
	return true
}
```

Add the same two rows to `TestOutcome_ChargeableExemptsCapAndContentionWaits`
so the exemption is asserted rather than assumed.

- [ ] **Step 7b: Give both codes an explicit retry schedule**

`retryDelay` (`retry.go`) does **not** requeue an unknown code immediately — it
falls through to `retrySchedules[CodeRateLimited]`, whose 2-minute floor would
park every resumable publication for two minutes. Both codes therefore need
their own row:

```go
	CodePublishNotStarted: {floor: 0, base: 0, cap: 0},
	CodePublishResumable:  {floor: 5 * time.Second, base: 15 * time.Second, cap: 5 * time.Minute},
```

`publish_not_started` waits for nothing — the worker died before it caused
anything — so it requeues at once. `publish_resumable` is different: the retry
re-drives the state machine, which re-probes the forge, and that is the path a
forge outage now takes (Task 9 Step 7). Backing it off means an unreachable
forge is retried on a curve instead of hot-looping until the wall deadline,
while a receipt that failed to commit still resumes within seconds. A provider
that states its own reset still wins — `retryDelay` takes `Outcome.RetryAt`
verbatim over the computed delay.

- [ ] **Step 8: Run the outcome test**

Run: `go test ./internal/runs/ -run 'TestPublishOutcomes|TestOutcome_Chargeable' -v`
Expected: PASS.

- [ ] **Step 9: Add the event kinds**

In `events.go`, after the phase kinds:

```go
	// Plan 08. Gates and publications get their own namespaces rather than
	// living under phase.*: they are the run's attention items, and Plan 10's
	// global stream filters on the kind prefix to build badges and cards.
	EventGateWaiting          = "gate.waiting"
	EventGateDecided          = "gate.decided"
	EventRunRevisionOpened    = "run.revision_opened"
	EventPublishIntentCreated = "publish.intent_created"
	EventPublishPushed        = "publish.pushed"
	EventPublishPROpened      = "publish.pr_opened"
	EventPublishAdopted       = "publish.adopted"
	EventPublishHalted        = "publish.halted"
```

- [ ] **Step 10: Write the failing snapshot tests**

In `backend/internal/runs/policy_test.go`:

```go
func TestDefaultSnapshot_AllowsSelfApprovalOnTheShippedBaseline(t *testing.T) {
	s := DefaultSnapshot()
	if s.SchemaVersion != 2 {
		t.Fatalf("schema = %d, want 2", s.SchemaVersion)
	}
	if !s.AllowSelfApproval {
		t.Error("the shipped baseline is solo-friendly (spec §6): self-approval defaults on")
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("the shipped baseline must validate: %v", err)
	}
}

func TestSnapshotFromRow_UpgradesASchema1DocumentToSelfApprovalOn(t *testing.T) {
	// Runs minted before Plan 08 pinned a schema-1 snapshot with no such
	// field. Refusing to load them would strand every in-flight run at the
	// upgrade; defaulting to the shipped baseline value is the only answer
	// that changes no behaviour for them.
	got, err := snapshotFromRow([]byte(`{"schema":1,"max_attempts":3,"execution_budget_ms":1000,"wall_clock_ms":2000}`))
	if err != nil {
		t.Fatalf("schema 1 must still load: %v", err)
	}
	if !got.AllowSelfApproval {
		t.Error("an upgraded schema-1 snapshot must allow self approval")
	}
	if got.SchemaVersion != 2 {
		t.Errorf("schema = %d, want the document upgraded to 2", got.SchemaVersion)
	}
	if got.MaxAttempts != 3 || got.ExecutionBudgetMS != 1000 || got.WallClockMS != 2000 {
		t.Errorf("upgrade must not touch the pinned values: %+v", got)
	}
}

func TestSnapshotFromRow_RefusesAnUnknownSchema(t *testing.T) {
	if _, err := snapshotFromRow([]byte(`{"schema":99,"max_attempts":3}`)); err == nil {
		t.Fatal("an unknown snapshot schema is corruption, not a default")
	}
}
```

- [ ] **Step 11: Run them and watch them fail**

Run: `go test ./internal/runs/ -run 'TestDefaultSnapshot|TestSnapshotFromRow' -v`
Expected: FAIL — `s.AllowSelfApproval undefined`.

- [ ] **Step 12: Add the field and the upgrade branch**

In `policy.go`:

```go
// snapshotSchemaVersion is the only snapshot shape this binary WRITES.
// Readers additionally accept version 1 (see snapshotFromRow), which is
// the Plan 05 shape without AllowSelfApproval.
const snapshotSchemaVersion = 2

type PolicySnapshot struct {
	SchemaVersion     int   `json:"schema"`
	MaxAttempts       int32 `json:"max_attempts"`
	ExecutionBudgetMS int64 `json:"execution_budget_ms"`
	WallClockMS       int64 `json:"wall_clock_ms"`
	// AllowSelfApproval lets the requester decide their own gate (spec §6).
	// True in the shipped baseline — teams flip it in the versioned baseline
	// Plan 09 introduces. Self-approvals are still marked distinctly in audit.
	AllowSelfApproval bool `json:"allow_self_approval"`
}

func DefaultSnapshot() PolicySnapshot {
	return PolicySnapshot{
		SchemaVersion:     snapshotSchemaVersion,
		MaxAttempts:       3,
		ExecutionBudgetMS: 2 * 60 * 60 * 1000,
		WallClockMS:       24 * 60 * 60 * 1000,
		AllowSelfApproval: true,
	}
}
```

`Validate()` keeps checking `s.SchemaVersion != snapshotSchemaVersion`: what a caller hands `insertRun` must be the current shape. The tolerance lives on the read path only.

In `service.go`:

```go
// snapshotFromRow re-parses the pinned snapshot; corruption is internal.
// A stored schema-1 document (Plan 05..07) is upgraded in place: it predates
// AllowSelfApproval, and the shipped baseline value is what those runs were
// created under, so defaulting to true changes nothing for them.
func snapshotFromRow(raw []byte) (PolicySnapshot, error) {
	var s PolicySnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return PolicySnapshot{}, fmt.Errorf("runs: pinned snapshot corrupt: %w", err)
	}
	switch s.SchemaVersion {
	case 1:
		s.SchemaVersion = snapshotSchemaVersion
		s.AllowSelfApproval = true
	case snapshotSchemaVersion:
	default:
		return PolicySnapshot{}, fmt.Errorf("runs: pinned snapshot schema %d is not readable by this binary", s.SchemaVersion)
	}
	return s, nil
}
```

- [ ] **Step 13: Run the whole unit suite**

Run: `go test ./internal/runs/... -race`
Expected: PASS. (Integration tests are behind a build tag and are not in this run.)

- [ ] **Step 14: Commit**

```bash
git add backend/internal/runs/status.go backend/internal/runs/status_test.go \
        backend/internal/runs/outcome.go backend/internal/runs/outcome_test.go \
        backend/internal/runs/events.go backend/internal/runs/policy.go \
        backend/internal/runs/policy_test.go backend/internal/runs/service.go \
        backend/internal/runs/orchestrator.go
git commit -m "feat(runs): add the gate and publish vocabulary with a self-approval snapshot field"
```

---

### Task 2: Compiler — revision transitions and the gate that authorizes each publish

The document validator already proved every structural invariant (`internal/workflows/validate.go`). The runtime needs two answers it does not currently carry across the mirror: *which* gate authorizes a given publish node, and *what* a gate's revision loop rewinds. Re-derive both here with the same rule the validator used, so a document that passed validation compiles to exactly one assignment.

**Files:**
- Modify: `backend/internal/runs/compile.go` (`DefNode` :44-49, `PlanNode` :66-72, `Compile` :84-139, `passThrough` :191-203, `Ready` :219-233, `HasUnsupportedNodes` :238-247 **deleted**)
- Modify: `backend/internal/app/runsports.go` (`pinnedFromVersion` :38-56)
- Test: `backend/internal/runs/compile_test.go`, `backend/internal/app/runsports_test.go`

**Interfaces:**
- Consumes: `runs.DefinitionDoc`, `workflows.Document`, `workflows.RevisionTransition`.
- Produces:
  - `runs.DefRevision{Target string; MaxRounds int}` and `DefNode.Revision *DefRevision`
  - `PlanNode.Revision *DefRevision`
  - `func (p ExecPlan) GateFor(publishNodeID string) (string, bool)`
  - `func (p ExecPlan) PublishFor(gateNodeID string) (string, bool)`
  - `func (p ExecPlan) RevisionSlice(gateNodeID string) ([]string, bool)`
  - `func (p ExecPlan) ExaminedInput(gateNodeID string) (PlanInput, bool)`

- [ ] **Step 1: Write the failing compiler tests**

In `backend/internal/runs/compile_test.go`. The fixture is the canonical shape this whole plan exercises: plan → implement → review → gate → publish, with the gate rewinding to implement.

```go
// conveyorDoc is the plan's reference definition: the full month-one loop.
func conveyorDoc() DefinitionDoc {
	return DefinitionDoc{
		Nodes: []DefNode{
			{ID: "plan", Kind: KindPlan, Harness: "h", Instructions: "plan it"},
			{ID: "implement", Kind: KindImplement, Harness: "h", Instructions: "do it"},
			{ID: "review", Kind: KindReview, Harness: "h", Instructions: "review it"},
			{ID: "gate", Kind: KindGate, Revision: &DefRevision{Target: "implement", MaxRounds: 2}},
			{ID: "publish", Kind: KindGitPublish},
		},
		Edges: []DefEdge{
			{From: "plan", To: "implement", Artifact: ArtifactKindPlan},
			{From: "implement", To: "review", Artifact: ArtifactKindPatch},
			{From: "implement", To: "gate", Artifact: ArtifactKindPatch},
			{From: "review", To: "gate", Artifact: ArtifactKindReviewVerdict},
			{From: "gate", To: "publish", Artifact: ArtifactKindPatch},
		},
	}
}

func TestCompile_AssignsTheDominatingGateToItsPublish(t *testing.T) {
	p, err := Compile(conveyorDoc())
	if err != nil {
		t.Fatal(err)
	}
	gate, ok := p.GateFor("publish")
	if !ok || gate != "gate" {
		t.Fatalf("GateFor(publish) = %q,%v; want gate,true", gate, ok)
	}
	pub, ok := p.PublishFor("gate")
	if !ok || pub != "publish" {
		t.Fatalf("PublishFor(gate) = %q,%v; want publish,true", pub, ok)
	}
	if _, ok := p.GateFor("review"); ok {
		t.Error("only git-publish nodes have an authorizing gate")
	}
}

func TestCompile_RevisionSliceIsTheTargetToGateSpan(t *testing.T) {
	p, err := Compile(conveyorDoc())
	if err != nil {
		t.Fatal(err)
	}
	slice, ok := p.RevisionSlice("gate")
	if !ok {
		t.Fatal("the gate declares a revision transition")
	}
	want := []string{"gate", "implement", "review"}
	if diff := cmp.Diff(want, slice); diff != "" {
		t.Errorf("slice mismatch (-want +got):\n%s", diff)
	}
	// "plan" dominates the target and is NOT rewound: a revision re-does the
	// work between the target and the gate, never the inputs that fed it.
	for _, id := range slice {
		if id == "plan" {
			t.Error("the revision slice must stop at its target")
		}
	}
}

func TestCompile_GateWithoutARevisionHasNoSlice(t *testing.T) {
	doc := conveyorDoc()
	doc.Nodes[3].Revision = nil
	p, err := Compile(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.RevisionSlice("gate"); ok {
		t.Error("a gate with no on_request_changes has no slice")
	}
}

func TestCompile_ExaminedInputIsThePatchTheGateSees(t *testing.T) {
	p, err := Compile(conveyorDoc())
	if err != nil {
		t.Fatal(err)
	}
	in, ok := p.ExaminedInput("gate")
	if !ok {
		t.Fatal("the gate examines exactly one plan or patch")
	}
	if in.Artifact != ArtifactKindPatch || in.Origin != "implement" {
		t.Fatalf("examined = %+v; want the implement patch", in)
	}
}

func TestCompile_InputOrderBreaksTiesOnFromSoTwoSameTypeInputsAreDeterministic(t *testing.T) {
	// Carried forward from Plan 05's final review: sorting on (Origin,
	// Artifact) alone left two edges from DIFFERENT direct sources through
	// the same origin in an order the map iteration decided.
	doc := DefinitionDoc{
		Nodes: []DefNode{
			{ID: "implement", Kind: KindImplement, Harness: "h", Instructions: "x"},
			{ID: "gate_b", Kind: KindGate},
			{ID: "gate_a", Kind: KindGate},
			{ID: "sink", Kind: KindReview, Harness: "h", Instructions: "x"},
		},
		Edges: []DefEdge{
			{From: "implement", To: "gate_b", Artifact: ArtifactKindPatch},
			{From: "implement", To: "gate_a", Artifact: ArtifactKindPatch},
			{From: "gate_b", To: "sink", Artifact: ArtifactKindPatch},
			{From: "gate_a", To: "sink", Artifact: ArtifactKindPatch},
		},
	}
	first, err := Compile(doc)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := Compile(doc)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(first.Nodes["sink"].Inputs, again.Nodes["sink"].Inputs); diff != "" {
			t.Fatalf("compile %d produced a different input order (-first +again):\n%s", i, diff)
		}
	}
	got := []string{first.Nodes["sink"].Inputs[0].From, first.Nodes["sink"].Inputs[1].From}
	if diff := cmp.Diff([]string{"gate_a", "gate_b"}, got); diff != "" {
		t.Errorf("From tiebreaker mismatch (-want +got):\n%s", diff)
	}
}

// The three graph helpers Step 6 introduces get direct unit tests, and the
// cases are not free to differ from the definitions context's.
//
// ancestors, descendants and dominators are a SECOND implementation of what
// internal/workflows/graph.go already computes. internal/arch/rules.go forbids
// the import, so the copy is forced — but a second dominator computation that
// disagrees with the validator's is exactly the "corruption of a stored
// version" assignGates refuses to paper over, and nothing in the build catches
// the disagreement.
//
// So the two test files are twins: cover the SAME fixture graphs as
// backend/internal/workflows/graph_test.go, write the expectations as literals
// in both places rather than deriving them from either implementation, and
// leave a comment at the top of each file naming the other as its twin. A
// change to one that is not made to the other must fail.
func TestAncestors_MatchesTheDefinitionsValidatorOnTheSameGraphs(t *testing.T) { /* … */ }
func TestDescendants_MatchesTheDefinitionsValidatorOnTheSameGraphs(t *testing.T) { /* … */ }
func TestDominators_MatchesTheDefinitionsValidatorOnTheSameGraphs(t *testing.T) { /* … */ }

func TestCompile_PassThroughPrefersTheTypedInputDeterministically(t *testing.T) {
	// A gate examining a patch and consuming a review verdict passes the
	// PATCH through, whatever order the edges arrive in.
	p, err := Compile(conveyorDoc())
	if err != nil {
		t.Fatal(err)
	}
	in := p.Nodes["publish"].Inputs
	if len(in) != 1 || in[0].Origin != "implement" || in[0].Artifact != ArtifactKindPatch {
		t.Fatalf("publish inputs = %+v; want the implement patch through the gate", in)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/runs/ -run TestCompile -v`
Expected: FAIL — `unknown field Revision in struct literal of type DefNode`.

- [ ] **Step 3: Carry the revision transition across the mirror**

In `compile.go`:

```go
// DefRevision is the runs-owned mirror of a gate's bounded
// on_request_changes loop (spec §2). It is not a graph edge — the dataflow
// DAG stays acyclic; the loop is replayed by rematerializing a slice.
type DefRevision struct {
	Target    string
	MaxRounds int
}

type DefNode struct {
	ID           string
	Kind         string
	Harness      string
	Instructions string
	// Revision is set on gates that declare on_request_changes, nil otherwise.
	Revision *DefRevision
}
```

and carry it onto `PlanNode`:

```go
type PlanNode struct {
	ID           string
	Kind         string
	Harness      string
	Instructions string
	Inputs       []PlanInput
	Revision     *DefRevision
}
```

In `Compile`, set it where the `PlanNode` is built:

```go
pn := PlanNode{ID: n.ID, Kind: n.Kind, Harness: n.Harness, Instructions: n.Instructions, Revision: n.Revision}
```

- [ ] **Step 4: Make the input sort total**

Still in `Compile`, the tiebreaker gains `From` — the last field that can distinguish two inputs, so the order is a total one and two compiles of the same document always agree:

```go
			sort.Slice(pn.Inputs, func(i, j int) bool {
				a, b := pn.Inputs[i], pn.Inputs[j]
				if a.Origin != b.Origin {
					return a.Origin < b.Origin
				}
				if a.Artifact != b.Artifact {
					return a.Artifact < b.Artifact
				}
				// Two edges may share an origin AND a type and still differ in
				// their direct source — one patch reaching a node through two
				// gates. From is the last discriminator, so this comparison is
				// a total order and compilation is deterministic.
				return a.From < b.From
			})
```

- [ ] **Step 5: Make `passThrough` deterministic**

```go
// passThrough picks the gate input that carries the outgoing artifact:
// the same artifact type where the gate examines one, else the single
// examined work product. Matching on type first keeps a gate that sees
// both a plan and a patch from binding the wrong producer.
//
// Candidates are compared in a fixed order rather than taken as they come:
// `in` is built by ranging over doc.Edges, so its order is the document's,
// and a caller that reorders two equivalent edges must still get the same
// origin. Ties break on (From, To, Artifact) — the whole edge.
func passThrough(inputs []DefEdge, artifact string) (DefEdge, bool) {
	best, found := DefEdge{}, false
	consider := func(e DefEdge) {
		if !found || edgeLess(e, best) {
			best, found = e, true
		}
	}
	for _, ge := range inputs {
		if ge.Artifact == artifact {
			consider(ge)
		}
	}
	if found {
		return best, true
	}
	for _, ge := range inputs {
		if ge.Artifact == ArtifactKindPlan || ge.Artifact == ArtifactKindPatch {
			consider(ge)
		}
	}
	return best, found
}

func edgeLess(a, b DefEdge) bool {
	if a.From != b.From {
		return a.From < b.From
	}
	if a.To != b.To {
		return a.To < b.To
	}
	return a.Artifact < b.Artifact
}
```

- [ ] **Step 6: Compute the gate↔publish assignment and the revision slices**

Add to `compile.go`. `ExecPlan` grows three maps, all built once during `Compile`:

```go
type ExecPlan struct {
	Order []string
	Nodes map[string]PlanNode
	// gateFor maps a git-publish node to the gate whose approval authorizes
	// it, and publishFor is its inverse. The document validator already
	// proved the assignment exists and is one-to-one (workflows §2
	// invariants 6 and 7); this re-derives it with the same nearest-eligible
	// rule so the runtime can name the gate a publish must wait for.
	gateFor    map[string]string
	publishFor map[string]string
	// revisionSlice maps a gate declaring on_request_changes to the node ids
	// its loop rewinds, sorted.
	revisionSlice map[string][]string
}

func (p ExecPlan) GateFor(publishNodeID string) (string, bool) {
	g, ok := p.gateFor[publishNodeID]
	return g, ok
}

func (p ExecPlan) PublishFor(gateNodeID string) (string, bool) {
	pub, ok := p.publishFor[gateNodeID]
	return pub, ok
}

func (p ExecPlan) RevisionSlice(gateNodeID string) ([]string, bool) {
	s, ok := p.revisionSlice[gateNodeID]
	return s, ok
}

// ExaminedInput is the single plan-or-patch a gate decides on (spec §2:
// "a gate examines exactly one plan or patch"). Approval binds this
// artifact id and nothing newer.
func (p ExecPlan) ExaminedInput(gateNodeID string) (PlanInput, bool) {
	n, ok := p.Nodes[gateNodeID]
	if !ok || n.Kind != KindGate {
		return PlanInput{}, false
	}
	for _, in := range n.Inputs {
		if in.Artifact == ArtifactKindPlan || in.Artifact == ArtifactKindPatch {
			return in, true
		}
	}
	return PlanInput{}, false
}
```

The derivation runs at the end of `Compile`, after `nodes` is populated:

```go
	plan := ExecPlan{Order: order, Nodes: nodes}
	if err := plan.assignGates(byID, in); err != nil {
		return ExecPlan{}, err
	}
	return plan, nil
```

```go
// assignGates re-derives the two graph facts the runtime needs from an
// already-validated document. It is deliberately a re-derivation and not a
// second validation: reaching a state the validator rejects is corruption of
// a stored version, so it returns an error the caller surfaces as
// internal_error rather than a user-facing violation.
func (p *ExecPlan) assignGates(byID map[string]DefNode, in map[string][]DefEdge) error {
	anc := map[string]map[string]bool{}
	for id := range p.Nodes {
		anc[id] = p.ancestors(id, in)
	}
	dom := p.dominators(in)

	p.gateFor = map[string]string{}
	p.publishFor = map[string]string{}
	for _, pubID := range p.Order {
		if p.Nodes[pubID].Kind != KindGitPublish {
			continue
		}
		var patch PlanInput
		for _, pin := range p.Nodes[pubID].Inputs {
			if pin.Artifact == ArtifactKindPatch {
				patch = pin
				break
			}
		}
		nearest, nearestDepth := "", -1
		for _, gateID := range p.Order {
			if p.Nodes[gateID].Kind != KindGate || !dom[pubID][gateID] {
				continue
			}
			ex, ok := p.ExaminedInput(gateID)
			if !ok || ex.Artifact != ArtifactKindPatch || ex.Origin != patch.Origin {
				continue
			}
			// No patch producer may sit between the gate and the publish, or
			// the approved artifact and the published artifact could diverge.
			divergent := false
			for id := range p.descendants(gateID) {
				if anc[pubID][id] && p.Nodes[id].Kind == KindImplement {
					divergent = true
					break
				}
			}
			if divergent {
				continue
			}
			// dom(publish) is a chain, so |dom(gate)| orders the candidates
			// and the deepest eligible gate is the nearest one.
			if d := len(dom[gateID]); d > nearestDepth {
				nearest, nearestDepth = gateID, d
			}
		}
		if nearest == "" {
			return fmt.Errorf("runs: compile: publish %q has no dominating gate examining its patch", pubID)
		}
		if other, taken := p.publishFor[nearest]; taken {
			return fmt.Errorf("runs: compile: gate %q authorizes both %q and %q", nearest, other, pubID)
		}
		p.gateFor[pubID] = nearest
		p.publishFor[nearest] = pubID
	}

	p.revisionSlice = map[string][]string{}
	for _, gateID := range p.Order {
		n := p.Nodes[gateID]
		if n.Kind != KindGate || n.Revision == nil {
			continue
		}
		target := n.Revision.Target
		if _, ok := p.Nodes[target]; !ok {
			return fmt.Errorf("runs: compile: gate %q rewinds to unknown node %q", gateID, target)
		}
		if !dom[gateID][target] {
			return fmt.Errorf("runs: compile: revision target %q does not dominate gate %q", target, gateID)
		}
		slice := map[string]bool{target: true, gateID: true}
		desc := p.descendants(target)
		for a := range anc[gateID] {
			if desc[a] {
				slice[a] = true
			}
		}
		ids := make([]string, 0, len(slice))
		for id := range slice {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		p.revisionSlice[gateID] = ids
	}
	return nil
}
```

`ancestors`, `descendants` and `dominators` are three small helpers on `ExecPlan`, re-owned here because contexts never share code across the boundary (the definitions context has its own copies in `internal/workflows/graph.go`):

```go
// ancestors is the strict ancestor set of id.
func (p ExecPlan) ancestors(id string, in map[string][]DefEdge) map[string]bool {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(cur string) {
		for _, e := range in[cur] {
			if !seen[e.From] {
				seen[e.From] = true
				walk(e.From)
			}
		}
	}
	walk(id)
	return seen
}

// descendants is the strict descendant set of id, walked over the plan's
// resolved inputs so it needs no edge index of its own.
func (p ExecPlan) descendants(id string) map[string]bool {
	children := map[string][]string{}
	for _, n := range p.Nodes {
		for _, pin := range n.Inputs {
			children[pin.From] = append(children[pin.From], n.ID)
		}
	}
	seen := map[string]bool{}
	var walk func(string)
	walk = func(cur string) {
		for _, c := range children[cur] {
			if !seen[c] {
				seen[c] = true
				walk(c)
			}
		}
	}
	walk(id)
	return seen
}

// dominators is the classic iterative fixed point over the topological
// order: dom(n) = {n} ∪ ⋂ dom(pred). Order is already topological, so one
// forward pass suffices.
func (p ExecPlan) dominators(in map[string][]DefEdge) map[string]map[string]bool {
	dom := make(map[string]map[string]bool, len(p.Order))
	for _, id := range p.Order {
		cur := map[string]bool{id: true}
		first := true
		for _, e := range in[id] {
			pd := dom[e.From]
			if first {
				for k := range pd {
					cur[k] = true
				}
				first = false
				continue
			}
			for k := range cur {
				if k != id && !pd[k] {
					delete(cur, k)
				}
			}
		}
		dom[id] = cur
	}
	return dom
}
```

Note `dominators` uses `dom[n][n] = true` (reflexive), matching the definitions validator: `dom[pubID][gateID]` therefore reads "gateID dominates pubID", and a publish is trivially its own dominator, which the `Kind != KindGate` guard already excludes.

- [ ] **Step 7: Delete `HasUnsupportedNodes`**

Remove the function from `compile.go` entirely. Its only call site is `resolveIntake` (`tasks.go:140-143`), removed in Task 11 — leave the call site alone for now and take the compile error as the reminder. Do the deletion and the call-site removal in the same step so the tree builds:

In `tasks.go`, drop the block:

```go
	if unsupported := plan.HasUnsupportedNodes(); len(unsupported) > 0 {
		return RepoRef{}, PinnedDefinition{}, "", xerrors.E(...)
	}
```

and update `resolveIntake`'s doc comment: `// resolveIntake pins everything a run needs before any row exists: repo, definition (compiled — gates and publishes are executable from Plan 08 on), per-node harness refs, snapshot, base SHA.`

- [ ] **Step 8: Run the compiler tests**

Run: `go test ./internal/runs/ -run TestCompile -race -v`
Expected: PASS, including the 20-iteration determinism loop.

- [ ] **Step 9: Write the failing adapter test**

In `backend/internal/app/runsports_test.go`:

```go
func TestPinnedFromVersion_CarriesTheRevisionTransition(t *testing.T) {
	doc := workflows.Document{
		Schema: 1,
		Nodes: []workflows.Node{
			{ID: "implement", Type: workflows.NodeImplement, Harness: "h", Instructions: "x"},
			{ID: "gate", Type: workflows.NodeGate, OnRequestChanges: &workflows.RevisionTransition{Target: "implement", MaxRounds: 3}},
		},
		Edges: []workflows.Edge{{From: "implement", To: "gate", Artifact: workflows.ArtifactPatch}},
	}
	got, err := pinnedFromVersion(workflows.Version{ID: uuid.New(), Document: doc.Canonical()})
	if err != nil {
		t.Fatal(err)
	}
	var gate runs.DefNode
	for _, n := range got.Doc.Nodes {
		if n.ID == "gate" {
			gate = n
		}
	}
	if gate.Revision == nil {
		t.Fatal("the mirror must carry on_request_changes; without it no revision can ever open")
	}
	if gate.Revision.Target != "implement" || gate.Revision.MaxRounds != 3 {
		t.Errorf("revision = %+v; want implement/3", *gate.Revision)
	}
}

func TestPinnedFromVersion_LeavesNonGatesWithoutARevision(t *testing.T) {
	doc := workflows.Document{
		Schema: 1,
		Nodes:  []workflows.Node{{ID: "implement", Type: workflows.NodeImplement, Harness: "h", Instructions: "x"}},
	}
	got, err := pinnedFromVersion(workflows.Version{ID: uuid.New(), Document: doc.Canonical()})
	if err != nil {
		t.Fatal(err)
	}
	if got.Doc.Nodes[0].Revision != nil {
		t.Error("only gates carry a revision transition")
	}
}
```

- [ ] **Step 10: Run it and watch it fail**

Run: `go test ./internal/app/ -run TestPinnedFromVersion -v`
Expected: FAIL — `gate.Revision` is nil.

- [ ] **Step 11: Map the transition in the adapter**

In `runsports.go`, inside `pinnedFromVersion`:

```go
	for _, n := range doc.Nodes {
		mn := runs.DefNode{
			ID: n.ID, Kind: string(n.Type), Harness: n.Harness, Instructions: n.Instructions,
		}
		if n.OnRequestChanges != nil {
			// Only app sees both vocabularies (composition root): the mapping
			// is mechanical, and the runs mirror owns its own type so the
			// contexts stay peers.
			mn.Revision = &runs.DefRevision{
				Target:    n.OnRequestChanges.Target,
				MaxRounds: n.OnRequestChanges.MaxRounds,
			}
		}
		mirror.Nodes = append(mirror.Nodes, mn)
	}
```

- [ ] **Step 12: Run the adapter and compiler tests together**

Run: `go test ./internal/runs/... ./internal/app/... -race`
Expected: PASS.

- [ ] **Step 13: Commit**

```bash
git add backend/internal/runs/compile.go backend/internal/runs/compile_test.go \
        backend/internal/runs/tasks.go \
        backend/internal/app/runsports.go backend/internal/app/runsports_test.go
git commit -m "feat(runs): compile the gate-to-publish assignment and revision slices from the pinned definition"
```

---

### Task 3: Schema — gate decisions, approved patches, publish intents, generations

One migration. Everything downstream reads these tables, so they land before any behaviour does.

**Files:**
- Modify: `backend/internal/platform/pg/schema.sql` (runs block :193-313)
- Create: `backend/internal/platform/pg/migrations/<timestamp>_gates_and_publishing.sql` (generated)
- Modify: `backend/internal/runs/store/queries.sql`
- Regenerate: `backend/internal/runs/store/*.sql.go` (`task db:sqlc`)
- Test: `backend/internal/runs/store/schema_integration_test.go` (new)

**Interfaces:**
- Produces: tables `gate_decisions`, `approved_patches`, `publish_intents`; columns `runs.generation`, `phase_runs.generation`, `artifacts.generation`; widened `phase_runs.status` and `artifacts.type` CHECKs; sqlc methods `InsertGatePhase`, `GetLiveGateForUpdate`, `FinishGatePhase`, `InsertGateDecision`, `CountRequestChanges`, `InsertApprovedPatch`, `GetValidAuthorizationForUpdate`, `GetAuthorizationByID`, `ConsumeAuthorization`, `InvalidateAuthorizations`, `CreatePublishIntent`, `GetPublishIntent`, `RecordPushReceipt`, `RecordPRReceipt`, `HaltPublishIntent`, `CountSettledPublications`, `RunIsWaitingOnAGate`, `MaxAttemptForNode`, `SupersedePhasesInSlice`, `ListSucceededNodes` (rewritten), `BumpRunGeneration`, `ListWaitingGates`, `ListWaitingGatesByProjects`.

  Step 7 is authoritative on these names — Tasks 5, 6 and 9 call them verbatim, so a subagent handed this task in isolation must write exactly these and not near-synonyms. One collision is intentional and worth naming so nobody "fixes" it: the sqlc method that CASes a gate row to its terminal status is `FinishGatePhase`, while `DecideGate` is the **service** method Task 5 introduces at a different layer. They are different things with adjacent names, and the store never gets a `DecideGate`.

- [ ] **Step 1: Widen the two CHECK constraints and add the generation columns**

In `schema.sql`, `runs`:

```sql
CREATE TABLE runs (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    task_id             uuid NOT NULL REFERENCES tasks (id) ON DELETE RESTRICT,
    status              text NOT NULL CHECK (status IN ('pending', 'running', 'waiting', 'succeeded', 'failed', 'superseded', 'dead')),
    definition_hash     text NOT NULL,
    base_sha            text NOT NULL,
    policy_snapshot     jsonb NOT NULL,
    -- Generations (spec §3): a gate's request_changes rewinds a slice and
    -- opens the next one. Dispatch never reads this — readiness is the
    -- node's NEWEST attempt row — but markers, approval invalidation and
    -- round accounting all do.
    generation          integer NOT NULL DEFAULT 1 CHECK (generation >= 1),
    execution_budget_ms bigint NOT NULL CHECK (execution_budget_ms > 0),
    execution_used_ms   bigint NOT NULL DEFAULT 0 CHECK (execution_used_ms >= 0),
    wall_deadline       timestamptz NOT NULL,
    outcome             jsonb,
    created_at          timestamptz NOT NULL,
    started_at          timestamptz,
    finished_at         timestamptz
);
```

`phase_runs`: add `waiting_decision` to the status CHECK, add `generation`, and add it to the live partial unique index — a gate awaiting a decision is as live as a running attempt and must block a second row for its node.

```sql
    status       text NOT NULL CHECK (status IN ('pending', 'running', 'waiting_decision', 'reconciling', 'succeeded', 'failed', 'dead', 'superseded')),
    generation   integer NOT NULL DEFAULT 1 CHECK (generation >= 1),
```

```sql
CREATE UNIQUE INDEX phase_runs_live_key ON phase_runs (run_id, node_id)
    WHERE status IN ('pending', 'running', 'waiting_decision', 'reconciling');
```

`artifacts`: add `publication` and `gate_note` to the type CHECK, plus `generation`.

```sql
CREATE TABLE artifacts (
    id         uuid PRIMARY KEY,
    run_id     uuid NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    node_id    text NOT NULL,
    attempt    integer NOT NULL,
    generation integer NOT NULL DEFAULT 1 CHECK (generation >= 1),
    -- publication and gate_note join log_ref and failure_snapshot as RUNTIME
    -- outputs: they are never edge bindings (the definitions validator only
    -- knows plan/patch/review_verdict), they are produced by the control
    -- plane rather than by a harness, and they are read by the API and by the
    -- revision seed respectively.
    type       text NOT NULL CHECK (type IN ('plan', 'patch', 'review_verdict', 'log_ref', 'failure_snapshot', 'publication', 'gate_note')),
    payload    jsonb NOT NULL,
    parent_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL
);

CREATE INDEX artifacts_run_id_idx ON artifacts (run_id);
-- buildRequest and the lineage reader both want "the newest artifact of this
-- type for this node", which is this index read backwards. The column order
-- mirrors GetNodeArtifact's ORDER BY exactly (Step 7): selection is by
-- PROVENANCE first — generation, then attempt — and only then by time, because
-- seeds and produced artifacts are stamped from two independent clocks. An
-- index ordered on created_at would no longer serve the query that decision 18
-- depends on.
CREATE INDEX artifacts_node_lookup_idx ON artifacts (run_id, node_id, type, generation DESC, attempt DESC, created_at DESC, id DESC);
```

- [ ] **Step 2: Add the three new tables**

Append to the runs block in `schema.sql`:

```sql
-- A gate's decision (spec §2 Gate). One row per gate ATTEMPT: a revision
-- rematerializes the gate as a new attempt row, which is decidable again,
-- while the unique key makes deciding the SAME row twice impossible — that
-- uniqueness IS the API's concurrency answer, not a convention.
CREATE TABLE gate_decisions (
    id                   uuid PRIMARY KEY DEFAULT uuidv7(),
    phase_run_id         uuid NOT NULL UNIQUE REFERENCES phase_runs (id) ON DELETE RESTRICT,
    run_id               uuid NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    node_id              text NOT NULL,
    generation           integer NOT NULL CHECK (generation >= 1),
    decision             text NOT NULL CHECK (decision IN ('approve', 'request_changes', 'reject')),
    -- The immutable artifact the human examined. Approval authorizes exactly
    -- this id and nothing newer (spec §3 artifact flow).
    examined_artifact_id uuid NOT NULL REFERENCES artifacts (id) ON DELETE RESTRICT,
    note                 text NOT NULL DEFAULT '',
    acted_by             uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    -- Marked distinctly because the policy permits it, not because it is
    -- ordinary (spec §6).
    self_approval        boolean NOT NULL DEFAULT false,
    acted_at             timestamptz NOT NULL
);

CREATE INDEX gate_decisions_run_node_idx ON gate_decisions (run_id, node_id);

-- The single-use ApprovedPatch authorization (spec §2): the only token
-- git-publish can consume, scoped to the one publish node its gate
-- dominates. Every field here is re-validated at consumption; a mismatch
-- aborts before any forge call.
CREATE TABLE approved_patches (
    id               uuid PRIMARY KEY DEFAULT uuidv7(),
    run_id           uuid NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    gate_node_id     text NOT NULL,
    publish_node_id  text NOT NULL,
    gate_decision_id uuid NOT NULL UNIQUE REFERENCES gate_decisions (id) ON DELETE RESTRICT,
    artifact_id      uuid NOT NULL REFERENCES artifacts (id) ON DELETE RESTRICT,
    base_sha         text NOT NULL,
    tree_hash        text NOT NULL,
    repo_id          uuid NOT NULL REFERENCES repos (id) ON DELETE RESTRICT,
    -- The repo's coordinates AS APPROVED. repo_id alone is not an identity:
    -- UpdateRepo can change a repo's slug, default branch and credential
    -- mid-run (assets/repos.go), so the row the id points at is not
    -- necessarily the repository the human approved publishing to. Pinning
    -- them here means the publish path can prove the destination has not
    -- moved, and a rename becomes a refusal instead of a patch delivered to
    -- the wrong repository.
    forge_kind       text NOT NULL,
    repo_slug        text NOT NULL,
    target_branch    text NOT NULL,
    generation       integer NOT NULL CHECK (generation >= 1),
    status           text NOT NULL CHECK (status IN ('valid', 'consumed', 'invalidated')),
    created_at       timestamptz NOT NULL,
    settled_at       timestamptz
);

-- At most one LIVE authorization per publish node per run. A newer patch
-- generation invalidates the prior one (spec §2), and this index is what
-- makes "invalidate then mint" impossible to get half-done.
CREATE UNIQUE INDEX approved_patches_live_key ON approved_patches (run_id, publish_node_id)
    WHERE status = 'valid';

-- The git-publish effect state machine (spec §3 Publishing). The row commits
-- BEFORE the first forge call; every effect records its receipt as it lands,
-- so a retry always distinguishes "never happened" from "succeeded, receipt
-- lost". One intent per (run, publish node) — resumed across attempts, never
-- re-minted, because a second intent would mean a second branch.
CREATE TABLE publish_intents (
    id               uuid PRIMARY KEY DEFAULT uuidv7(),
    run_id           uuid NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    node_id          text NOT NULL,
    -- The attempt that created the intent: provenance, not identity.
    attempt          integer NOT NULL CHECK (attempt >= 1),
    -- UNIQUE is the single-use guarantee in its strongest form: a second
    -- consumption of the same authorization is a constraint violation, not a
    -- lost CAS (invariant 2, Postgres is the authority).
    authorization_id uuid NOT NULL UNIQUE REFERENCES approved_patches (id) ON DELETE RESTRICT,
    repo_id          uuid NOT NULL REFERENCES repos (id) ON DELETE RESTRICT,
    -- Copied from the authorization at creation, so every later attempt and
    -- every recovery probe addresses the repository the approval named — not
    -- whatever the repos table says today. The credential rides along for the
    -- same reason: UpdateRepo can repoint a repo at a different one, and a
    -- resumed publication must present the credential its approval implied.
    forge_kind       text NOT NULL,
    repo_slug        text NOT NULL,
    credential_id    uuid NOT NULL REFERENCES credentials (id) ON DELETE RESTRICT,
    -- The deterministic markers recovery reconstructs from the authorization.
    branch           text NOT NULL,
    target_branch    text NOT NULL,
    base_sha         text NOT NULL,
    tree_hash        text NOT NULL,
    title            text NOT NULL,
    body             text NOT NULL,
    status           text NOT NULL CHECK (status IN ('pending', 'pushed', 'published', 'halted')),
    -- Receipts, each written in the transaction after its effect landed.
    head_sha         text,
    pr_number        bigint,
    pr_url           text,
    halt_reason      text,
    -- When settlement first found the marker missing. A single absent probe
    -- cannot settle an intent — a request issued just before the deadline may
    -- still be in flight — so absence must persist across ticks before it
    -- means "nothing landed". Cleared the moment a probe finds the marker.
    absent_since     timestamptz,
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL,
    UNIQUE (run_id, node_id)
);

-- Two publications can never target the same branch on the same repo: the
-- marker is derived from the approved artifact id, so a collision would mean
-- two authorizations over one artifact, which approved_patches_live_key
-- already forbids. Belt and braces, because the consequence is a silent
-- overwrite of somebody else's ref.
CREATE UNIQUE INDEX publish_intents_marker_key ON publish_intents (repo_id, branch);
```

- [ ] **Step 3: Generate the migration and prove it round-trips**

```bash
task db:up
task db:new -- gates_and_publishing
```

Read the generated file. It must contain: three `CREATE TABLE`s, three `ALTER TABLE ... ADD COLUMN generation`, the two CHECK replacements (Atlas emits `DROP CONSTRAINT` + `ADD CONSTRAINT`), the `phase_runs_live_key` recreation, and the two new indexes. Hand-edit only if Atlas ordered a constraint before the column it references.

Run: `task backend:test:integration -- -run TestMigrations`
Expected: PASS — migration up-down is already covered by the Plan 01 harness.

- [ ] **Step 4: Write the failing schema integration test**

In `backend/internal/runs/store/schema_integration_test.go`:

```go
//go:build integration

package store_test

// (Follow the neighbouring integration tests' testdb bootstrap.)

func TestApprovedPatches_OneLiveAuthorizationPerPublishNode(t *testing.T) {
	ctx, pool := testdb.New(t)
	runID, artifactID, decisionA, decisionB, repoID := seedRunWithTwoGateDecisions(t, ctx, pool)

	insert := func(decisionID uuid.UUID) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO approved_patches (run_id, gate_node_id, publish_node_id, gate_decision_id,
			                              artifact_id, base_sha, tree_hash, repo_id,
			                              forge_kind, repo_slug, target_branch,
			                              generation, status, created_at)
			VALUES ($1,'gate','publish',$2,$3,'abc','def',$4,'github','acme/widgets','main',1,'valid',now())`,
			runID, decisionID, artifactID, repoID)
		return err
	}
	if err := insert(decisionA); err != nil {
		t.Fatalf("first authorization: %v", err)
	}
	err := insert(decisionB)
	if err == nil {
		t.Fatal("a second LIVE authorization for the same publish node must be refused: the token is single-use")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("want unique violation, got %v", err)
	}

	// Invalidating the first makes room for the next generation's.
	if _, err := pool.Exec(ctx, `UPDATE approved_patches SET status='invalidated', settled_at=now() WHERE run_id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if err := insert(decisionB); err != nil {
		t.Fatalf("after invalidation a new authorization must fit: %v", err)
	}
}

func TestPublishIntents_AnAuthorizationIsConsumedByAtMostOneIntent(t *testing.T) {
	ctx, pool := testdb.New(t)
	// The seed returns the credential too: publish_intents pins forge_kind,
	// repo_slug and credential_id at creation (they are the coordinates the
	// approval named), and all three are NOT NULL.
	runID, authID, repoID, credID := seedRunWithAuthorization(t, ctx, pool)

	insert := func(node string) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO publish_intents (run_id, node_id, attempt, authorization_id, repo_id,
			                             forge_kind, repo_slug, credential_id,
			                             branch, target_branch, base_sha, tree_hash, title, body,
			                             status, created_at, updated_at)
			VALUES ($1,$2,1,$3,$4,'github','acme/widgets',$5,
			        'caesar/'||$3::text,'main','abc','def','t','b','pending',now(),now())`,
			runID, node, authID, repoID, credID)
		return err
	}
	if err := insert("publish"); err != nil {
		t.Fatal(err)
	}
	if err := insert("publish_two"); err == nil {
		t.Fatal("one authorization must not fund two publications")
	}
}

func TestPhaseRuns_AWaitingGateBlocksASecondLiveRowForItsNode(t *testing.T) {
	ctx, pool := testdb.New(t)
	runID := seedRun(t, ctx, pool)
	insert := func(attempt int, status string) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, status, created_at)
			VALUES ($1,'gate',$2,3,$3,now())`, runID, attempt, status)
		return err
	}
	if err := insert(1, "waiting_decision"); err != nil {
		t.Fatal(err)
	}
	if err := insert(2, "pending"); err == nil {
		t.Fatal("waiting_decision is live: a second live row for the node must be refused")
	}
	if _, err := pool.Exec(ctx, `UPDATE phase_runs SET status='succeeded', finished_at=now() WHERE run_id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if err := insert(2, "pending"); err != nil {
		t.Fatalf("once the gate is decided a successor row must fit: %v", err)
	}
}
```

- [ ] **Step 5: Run the schema tests**

Run: `task backend:test:integration -- -run 'TestApprovedPatches|TestPublishIntents|TestPhaseRuns_AWaitingGate' -v`
Expected: PASS.

- [ ] **Step 6: Rewrite `ListSucceededNodes` to be latest-attempt-wise**

In `internal/runs/store/queries.sql`, replace the existing statement:

```sql
-- name: ListSucceededNodes :many
-- Readiness is decided by each node's NEWEST attempt row, not by whether the
-- node ever succeeded. A revision rematerializes nodes that already succeeded
-- in the prior generation; with an any-attempt predicate those nodes would
-- read as done forever and their successors would run on stale artifacts.
-- With no revisions in play the newest row is the only terminal row, so this
-- is a strict generalization of the Plan 05 behaviour.
SELECT node_id FROM (
    SELECT DISTINCT ON (node_id) node_id, status
    FROM phase_runs
    WHERE run_id = $1
    ORDER BY node_id, attempt DESC
) latest
WHERE status = 'succeeded';
```

- [ ] **Step 7: Add the gate, authorization and intent statements**

Append to `queries.sql`:

```sql
-- name: InsertGatePhase :one
-- A gate row is INSERTED waiting: the claim CTE predicates on 'pending', so
-- no worker can ever lease it and the execution budget never accrues against
-- a human's thinking time (spec §3 Budgets).
INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, chargeable_failures,
                        generation, status, created_at)
VALUES ($1, $2, $3, $4, 0, $5, 'waiting_decision', $6)
RETURNING *;

-- name: GetLiveGateForUpdate :one
-- The decision's CAS target, locked for the whole decision transaction so a
-- second decider blocks rather than racing. The run columns ride along:
-- every deciding statement carries the wall-deadline predicate (spec §3).
SELECT pr.*, r.wall_deadline AS run_wall_deadline, r.status AS run_status,
       r.generation AS run_generation, r.policy_snapshot, t.requester_id, t.project_id
FROM phase_runs pr
JOIN runs r ON r.id = pr.run_id
JOIN tasks t ON t.id = r.task_id
WHERE pr.run_id = $1 AND pr.node_id = $2 AND pr.status = 'waiting_decision'
FOR UPDATE OF pr;

-- name: FinishGatePhase :execrows
-- The gate's own CAS. Unlike FinishPhase there is no worker identity to
-- match — a gate has no worker — and the from-status is fixed, so the only
-- way to win is to be the transaction that found the row still waiting.
--
-- The run's wall-deadline predicate rides IN this statement, evaluated against
-- the database clock (global constraint: every deciding predicate reads now(),
-- and it reads it in the statement that mutates). An earlier SELECT now() in
-- the same transaction is not a substitute: it would let a decision that read
-- the clock before the sweeper's commit still land its own mutation.
UPDATE phase_runs SET status = sqlc.arg(to_status), outcome = $2, finished_at = $3
WHERE id = $1 AND status = 'waiting_decision'
  AND EXISTS (
      SELECT 1 FROM runs r
      WHERE r.id = phase_runs.run_id
        AND r.status = 'waiting'
        AND r.wall_deadline > now()
  );

-- name: InsertGateDecision :one
INSERT INTO gate_decisions (phase_run_id, run_id, node_id, generation, decision,
                            examined_artifact_id, note, acted_by, self_approval, acted_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: CountRequestChanges :one
-- Rounds spent on THIS gate in THIS run — max_rounds is per gate, not per
-- run, and generations are global, so the count cannot be read off either.
SELECT count(*) FROM gate_decisions
WHERE run_id = $1 AND node_id = $2 AND decision = 'request_changes';

-- name: InsertApprovedPatch :one
INSERT INTO approved_patches (run_id, gate_node_id, publish_node_id, gate_decision_id,
                              artifact_id, base_sha, tree_hash, repo_id, forge_kind,
                              repo_slug, target_branch, generation, status, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 'valid', $13)
RETURNING *;

-- name: GetValidAuthorizationForUpdate :one
-- The approver's email rides along because a publication renders it into the
-- commit trailers and the PR body, and joining here saves the publish path a
-- second round trip on its hottest transaction.
--
-- FOR UPDATE OF ap: the decision and user rows are read-only here, and locking
-- them would serialize unrelated decisions by the same approver.
SELECT ap.*, u.email AS approver_email
FROM approved_patches ap
JOIN gate_decisions gd ON gd.id = ap.gate_decision_id
JOIN users u ON u.id = gd.acted_by
WHERE ap.run_id = $1 AND ap.publish_node_id = $2 AND ap.status = 'valid'
FOR UPDATE OF ap;

-- name: GetAuthorizationByID :one
-- The resume read: an intent that already exists names its authorization, and
-- the publish path needs the same joined shape the fresh read returns.
SELECT ap.*, u.email AS approver_email
FROM approved_patches ap
JOIN gate_decisions gd ON gd.id = ap.gate_decision_id
JOIN users u ON u.id = gd.acted_by
WHERE ap.id = $1;

-- name: ConsumeAuthorization :execrows
-- Carries the run's wall-deadline predicate: an expired run may not authorize
-- a forge effect, and evaluating it here means the sweeper cannot lose the
-- race (spec §3 Budgets).
UPDATE approved_patches SET status = 'consumed', settled_at = $2
WHERE id = $1 AND status = 'valid'
  AND EXISTS (SELECT 1 FROM runs r WHERE r.id = approved_patches.run_id AND r.wall_deadline > now());

-- name: InvalidateAuthorizations :many
-- A newer patch generation invalidates prior authorizations (spec §2). Only
-- 'valid' rows are touched: a consumed one already funded a publication and
-- rewriting its status would erase that fact.
UPDATE approved_patches SET status = 'invalidated', settled_at = $2
WHERE run_id = $1 AND status = 'valid'
RETURNING id, publish_node_id;

-- name: BumpRunGeneration :one
-- Opening a generation is a deciding mutation, so it carries the same
-- predicate the gate's own CAS does. A run that expired between the decision
-- arriving and this statement running returns no row, and openRevision turns
-- that into the same conflict a late decision gets.
UPDATE runs SET generation = generation + 1
WHERE id = $1 AND status = 'waiting' AND wall_deadline > now()
RETURNING generation;

-- name: CreatePublishIntent :one
INSERT INTO publish_intents (run_id, node_id, attempt, authorization_id, repo_id,
                             forge_kind, repo_slug, credential_id, branch, target_branch,
                             base_sha, tree_hash, title, body, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 'pending', $15, $15)
RETURNING *;

-- name: GetPublishIntent :one
SELECT * FROM publish_intents WHERE run_id = $1 AND node_id = $2;

-- The three receipt writes are MONOTONIC. status only ever moves forward
-- along pending -> pushed -> published, plus a one-way exit to halted from
-- the two unfinished states. That matters because a worker whose lease was
-- taken over can still be mid-flight inside a forge call: its request may be
-- accepted remotely long after the run stopped listening, and its late write
-- must not be able to undo the successor's. Each returns :execrows and every
-- caller checks the count — a zero means "somebody else already moved this
-- further", which is reloaded and adopted, never treated as failure.

-- name: RecordPushReceipt :execrows
-- The push landed. Recording head_sha before the PR call is what lets a
-- crash between the two resume instead of re-pushing blind. head_sha is
-- written once and never rewritten: a second push receipt for a different
-- commit would mean two commits on one marker, which cannot be true.
UPDATE publish_intents SET status = 'pushed', head_sha = $2, updated_at = $3
WHERE id = $1 AND status = 'pending';

-- name: RecordPRReceipt :execrows
UPDATE publish_intents SET status = 'published', pr_number = $2, pr_url = $3, updated_at = $4
WHERE id = $1 AND status = 'pushed';

-- name: CountSettledPublications :one
-- Publications this run has begun. EVERY status counts, 'pending' included.
--
-- The tempting reading is that a pending intent has pushed nothing yet, so a
-- revision could still take it back. It cannot: the intent row commits BEFORE
-- the first forge call, so 'pending' is precisely the window in which a push
-- may be in flight — and the intent's authorization is already consumed, which
-- InvalidateAuthorizations deliberately will not touch. A revision opened
-- against a pending intent races a push it has no way to cancel.
--
-- It lives in this task, not beside the publish state machine that owns the
-- rest of these rows, because Task 6's revision path is its only caller and
-- Task 6 must compile on its own.
SELECT count(*) FROM publish_intents WHERE run_id = $1;

-- name: HaltPublishIntent :execrows
-- Terminal by design: a halted publication is reconciled by a human, never
-- by another attempt.
--
-- 'published' is NOT haltable, and that exclusion is load-bearing rather than
-- tidy: a publication that reached 'published' has both its receipts and is
-- done, so no late arrival may overwrite it with a call for human help.
--
-- It is NOT, however, sufficient on its own, and reading it as the fence
-- against the create-PR race would be a mistake. The dangerous window is the
-- one BEFORE the receipt commits: the winner has opened the request, the
-- intent still says 'pushed', and this statement would happily halt it. What
-- closes that window is in the code, not here — adoptConflictingPublication
-- re-queries on conflict and only halts on a request provably not ours, so a
-- loser of the race never reaches this statement at all.
UPDATE publish_intents SET status = 'halted', halt_reason = $2, updated_at = $3
WHERE id = $1 AND status IN ('pending', 'pushed');

-- name: ListWaitingGates :many
SELECT pr.id, pr.run_id, pr.node_id, pr.attempt, pr.generation, pr.created_at,
       t.id AS task_id, t.project_id, t.title, t.requester_id
FROM phase_runs pr
JOIN runs r ON r.id = pr.run_id
JOIN tasks t ON t.id = r.task_id
WHERE pr.status = 'waiting_decision'
ORDER BY pr.created_at;

-- name: ListWaitingGatesByProjects :many
SELECT pr.id, pr.run_id, pr.node_id, pr.attempt, pr.generation, pr.created_at,
       t.id AS task_id, t.project_id, t.title, t.requester_id
FROM phase_runs pr
JOIN runs r ON r.id = pr.run_id
JOIN tasks t ON t.id = r.task_id
WHERE pr.status = 'waiting_decision' AND t.project_id = ANY(sqlc.arg(project_ids)::uuid[])
ORDER BY pr.created_at;

-- name: RunIsWaitingOnAGate :one
-- True when every live phase of the run is a gate awaiting a decision — at
-- least one waiting_decision row, and nothing pending, running or reconciling.
--
-- This is what makes "run status is read back from the database, never inferred
-- from what a pass just did" (decision 20) implementable. It is read AFTER a
-- materialization pass's inserts, so it sees the successors that were just
-- created. Deciding from a pre-insertion snapshot gets a fan-out wrong in both
-- directions, and one of those directions strands the gate permanently: a
-- sibling finishing beside an already-waiting gate would leave the run
-- 'running', and FinishGatePhase refuses to decide a run that is not 'waiting'.
--
-- exclude_phase is the phase whose own completion is being evaluated in the
-- calling transaction: its row may not have committed its terminal status yet,
-- and counting it as live would keep the run running forever. Pass uuid.Nil
-- when there is no such phase.
--
-- It lives in this task, not beside the janitor pass that shares it, because
-- Task 4's evaluateSuccess is its first caller and Task 4 must compile on its
-- own.
SELECT EXISTS (
           SELECT 1 FROM phase_runs
           WHERE run_id = @run_id AND status = 'waiting_decision'
       ) AND NOT EXISTS (
           SELECT 1 FROM phase_runs
           WHERE run_id = @run_id
             AND id <> @exclude_phase
             AND status IN ('pending', 'running', 'reconciling')
       );

-- name: MaxAttemptForNode :one
-- A rematerialized node continues the attempt ordinal rather than restarting
-- it: (run_id, node_id, attempt) is unique, and the ordinal stays what it has
-- always been — the execution ordinal (Plan 07 decision 5).
SELECT COALESCE(max(attempt), 0)::integer FROM phase_runs WHERE run_id = $1 AND node_id = $2;

-- name: SupersedePhasesInSlice :many
-- Revision rewind: every row for a node in the slice steps aside so a fresh
-- attempt can take its place.
--
-- 'succeeded' is in this list and that is the load-bearing part. Readiness is
-- each node's NEWEST attempt row (decision 2), so a slice node that succeeded
-- in the prior generation would otherwise keep that row as its newest and stay
-- readable as done forever — the new generation would re-run only the revision
-- target, and the gate would then re-decide against the OLD review verdict.
-- Superseding the success is also the honest record: that attempt was retired
-- by a new generation, and its artifacts and decisions remain untouched.
--
-- 'failed' and 'dead' are NOT in this list: a run holding either is already
-- terminal, so no revision can be opened against it.
UPDATE phase_runs SET status = 'superseded', outcome = $3, finished_at = $4
WHERE run_id = $1 AND node_id = ANY(sqlc.arg(node_ids)::text[])
  AND status IN ('pending', 'running', 'waiting_decision', 'reconciling', 'succeeded')
RETURNING id, node_id, attempt;
```

Also update `SupersedeLivePhases` (which terminalizes a whole run) to include the new live status:

```sql
-- name: SupersedeLivePhases :many
UPDATE phase_runs SET status = 'superseded', outcome = $2, finished_at = $3
WHERE run_id = $1 AND status IN ('pending', 'running', 'waiting_decision', 'reconciling')
RETURNING id, node_id, attempt;
```

and `InsertPhase` and `InsertArtifact` gain the generation parameter:

```sql
-- name: InsertPhase :one
INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, chargeable_failures,
                        not_before, generation, status, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8)
RETURNING *;

-- name: InsertArtifact :one
INSERT INTO artifacts (id, run_id, node_id, attempt, generation, type, payload, parent_ids, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;
```

`GetNodeArtifact` must pick the newest, which is what makes a rematerialized node's output win without any generation filter:

```sql
-- name: GetNodeArtifact :one
-- The CURRENT artifact of this type for this node. After a revision the node
-- has one per generation, and the successor must read the newest.
--
-- Ordering is by PROVENANCE first, not by time. The rows this has to separate
-- are stamped from two different clocks: a revision seed carries the database
-- clock (it is written inside the decision transaction), while a produced
-- artifact carries the injected application clock, which is time.Now() in
-- production. Those clocks are independent, so ordering on created_at means a
-- host clock even slightly behind the database silently sorts a generation-2
-- patch BEHIND its own generation-1 seed — and the gate then approves, and the
-- publisher then publishes, the stale content. uuidv7 does not save this: the
-- ids are minted from the same skewed clock.
--
-- generation and attempt are integers this plan already maintains and neither
-- can go backwards, so they decide. created_at and id stay only as tiebreaks
-- within one (generation, attempt) — where a single clock produced both rows
-- and comparing them is meaningful.
SELECT * FROM artifacts
WHERE run_id = $1 AND node_id = $2 AND type = $3
ORDER BY generation DESC, attempt DESC, created_at DESC, id DESC
LIMIT 1;
```

- [ ] **Step 8: Regenerate and build**

```bash
task db:sqlc
go build ./...
```

Expected: compile errors at every `InsertPhase` / `InsertArtifact` / `SupersedeLivePhases` call site (the generation parameter). Fix them by threading the run's generation through — `insertPhaseTx` and `insertArtifacts` gain a `generation int32` parameter, and their callers read it from `cachedPlan` (added in Task 4). For this task, pass a literal `1` at every site and leave a `// Task 4 threads the run's real generation here.` comment; the tests below still pass and Task 4 removes the literals.

- [ ] **Step 9: Prove the readiness rewrite with an integration test**

In `backend/internal/runs/store/schema_integration_test.go`:

```go
func TestListSucceededNodes_ForgetsANodeThatWasRematerialized(t *testing.T) {
	ctx, pool := testdb.New(t)
	runID := seedRun(t, ctx, pool)
	q := store.New(pool)

	mustInsertPhase(t, ctx, pool, runID, "implement", 1, "succeeded")
	got, err := q.ListSucceededNodes(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"implement"}, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}

	// A revision rewinds the node: a NEWER pending attempt row appears.
	mustInsertPhase(t, ctx, pool, runID, "implement", 2, "pending")
	got, err = q.ListSucceededNodes(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a rematerialized node must stop counting as succeeded, got %v", got)
	}

	// And counts again once the new attempt lands.
	mustFinishPhase(t, ctx, pool, runID, "implement", 2, "succeeded")
	got, err = q.ListSucceededNodes(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"implement"}, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}
```

- [ ] **Step 10: Run the store suite and the existing spine integration suite**

Run: `task backend:test:integration -- -run 'TestListSucceededNodes|TestApprovedPatches|TestPublishIntents|TestPhaseRuns' -v`
Then the regression sweep: `task backend:test:integration`
Expected: PASS. Plan 05/06/07 integration tests must be untouched by this — if any fails, the readiness rewrite or a CHECK is wrong, not the old test.

- [ ] **Step 11: Contracts and full gate**

```bash
task contracts:check
task backend:ci
```

Expected: green. (`contracts:check` includes the sqlc regen-diff, which is what proves Step 8's generated code is committed.)

- [ ] **Step 12: Commit**

```bash
git add backend/internal/platform/pg/schema.sql backend/internal/platform/pg/migrations/ \
        backend/internal/runs/store/ backend/internal/runs/orchestrator.go backend/internal/runs/queue.go
git commit -m "feat(runs): add gate decisions, approved patches, publish intents and run generations"
```

---
### Task 4: Gate materialization — the run stops and waits

When a gate becomes ready the orchestrator inserts a `waiting_decision` row instead of a `pending` one, and moves the run to `waiting`. Nothing claims it; nothing times it out but the wall deadline.

**Files:**
- Modify: `backend/internal/runs/orchestrator.go` (`cachedPlan` :61-67, `loadPlan` :216-257, `startPendingRuns` :261-312, `evaluateSuccess` :801-888, `insertArtifacts` :894-916, `finishRunTx` :920-960)
- Modify: `backend/internal/runs/queue.go` (`insertPhaseTx` :86-97)
- Test: `backend/internal/runs/orchestrator_gates_integration_test.go` (new)

**Interfaces:**
- Consumes: `ExecPlan.Ready`, `insertPhaseTx`, `store.InsertGatePhase`, `store.CASRunStatus`, `store.RunIsWaitingOnAGate` (Task 3 Step 7).
- Produces:
  - `cachedPlan.generation int32` (refreshed on every load, see below)
  - `func (o *Orchestrator) materializeNode(ctx, tx, cp, runID, nodeID, attempt, maxAttempts, generation int32) (Event, error)` — the one place a phase row is born, gate or not. The `generation` parameter is not optional: both call sites in Step 5 pass it, and so does Task 5's `materializeReady`.

- [ ] **Step 1: Write the failing integration test**

In `backend/internal/runs/orchestrator_gates_integration_test.go`. Follow the neighbouring `orchestrator_integration_test.go` bootstrap (testdb pool, fake executor, fake forge, short poll interval).

```go
//go:build integration

package runs_test

func TestOrchestrator_GateMaterializesWaitingAndStopsTheRun(t *testing.T) {
	h := newSpineHarness(t) // testdb + fake executor + fake forge + orchestrator
	_, run := h.createTask(t, conveyorWorkflow(t, h)) // plan → implement → review → gate → publish

	// The orchestrator drives everything up to the gate and then stops.
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	phases := h.phases(t, run.ID)
	gate := phases.byNode(t, "gate")
	if gate.Status != runs.PhaseWaitingDecision {
		t.Fatalf("gate status = %s, want waiting_decision", gate.Status)
	}
	if gate.WorkerID != "" {
		t.Errorf("a gate must never be claimed; worker = %q", gate.WorkerID)
	}
	if _, ok := phases.tryByNode("publish"); ok {
		t.Error("the publish node must not materialize before its gate is decided")
	}

	// The gate consumed no execution budget: the run's usage is whatever the
	// three agentic phases spent, and it does not grow while the gate waits.
	before := h.run(t, run.ID).ExecutionUsedMS
	time.Sleep(3 * h.pollInterval)
	if after := h.run(t, run.ID).ExecutionUsedMS; after != before {
		t.Errorf("execution usage grew from %d to %d while a human was thinking; the budget must pause at gates", before, after)
	}

	// And the event stream announced it exactly once.
	if n := h.countEvents(t, run.ID, runs.EventGateWaiting); n != 1 {
		t.Errorf("gate.waiting events = %d, want 1", n)
	}
}

func TestOrchestrator_WallDeadlineStillKillsAWaitingRun(t *testing.T) {
	// The execution budget pauses at gates; the wall clock never does
	// (spec §3 Budgets). A gate is not a place to hide from the deadline.
	h := newSpineHarness(t)
	_, run := h.createTaskWithSnapshot(t, conveyorWorkflow(t, h), runs.PolicySnapshot{
		SchemaVersion: 2, MaxAttempts: 3, ExecutionBudgetMS: 60_000, WallClockMS: 60_000,
		AllowSelfApproval: true,
	})
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	h.backdateWallDeadline(t, run.ID) // SQL: wall_deadline = now() - 1s
	h.waitForRunStatus(t, run.ID, runs.RunDead, 20*time.Second)

	got := h.run(t, run.ID)
	if got.Outcome == nil || got.Outcome.Code != runs.CodeWallDeadlineExceeded {
		t.Fatalf("outcome = %+v, want wall_deadline_exceeded", got.Outcome)
	}
	if gate := h.phases(t, run.ID).byNode(t, "gate"); gate.Status != runs.PhaseSuperseded {
		t.Errorf("the waiting gate must be superseded with the run, got %s", gate.Status)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `task backend:test:integration -- -run 'TestOrchestrator_Gate|TestOrchestrator_WallDeadlineStillKills' -v`
Expected: FAIL — the run never reaches `waiting`; the gate materializes `pending` and the fake executor is asked to run it.

- [ ] **Step 3: Carry the generation on the cached plan**

The generation is per-run mutable state, so caching it needs care: `loadPlan` caches for the process lifetime and a revision bumps the counter. Keep the compiled plan cached and read the generation fresh.

In `orchestrator.go`:

```go
type cachedPlan struct {
	plan       ExecPlan
	projectID  uuid.UUID
	taskInputs map[string]any
	baseSHA    string
	repo       RepoRef
	// generation is NOT cached: it changes when a gate opens a revision, and
	// a stale value would stamp the wrong marker on an artifact. It is read
	// per use through generationOf.
}

// generationOf reads the run's current generation. Deliberately not cached:
// a revision bumps it under a different transaction than the one that
// compiled the plan, and every write that stamps a generation must see the
// value its own transaction will commit against.
func (o *Orchestrator) generationOf(ctx context.Context, q *store.Queries, runID uuid.UUID) (int32, error) {
	row, err := q.GetRun(ctx, runID)
	if err != nil {
		return 0, fmt.Errorf("runs: read generation: %w", err)
	}
	return row.Generation, nil
}
```

The requester's user id is deliberately **not** cached here. Nothing in this task reads it, and Task 9 — which is the first task that needs the human's identity at all — needs the *email*, which it resolves through `PrincipalSource` from `taskRow.RequesterID` at load time. A field whose only justification is a task five ahead is a field a reviewer of this commit has to take on faith.

- [ ] **Step 4: Add the one place a phase row is born**

Both `startPendingRuns` and `evaluateSuccess` materialize nodes, and both must branch on gate-ness identically. Extract it:

```go
// materializeNode inserts one node's next attempt row inside the caller's tx
// and returns the event announcing it. Gates are inserted 'waiting_decision'
// — the claim CTE predicates on 'pending', so this single branch is the whole
// reason a human's thinking time never burns a lease or the execution budget.
func (o *Orchestrator) materializeNode(ctx context.Context, tx pgx.Tx, cp cachedPlan,
	runID uuid.UUID, nodeID string, attempt, maxAttempts, generation int32,
) (Event, error) {
	ref := AttemptRef{RunID: runID, NodeID: nodeID, Attempt: attempt}
	if cp.plan.Nodes[nodeID].Kind != KindGate {
		if _, err := o.svc.insertPhaseTx(ctx, tx, runID, nodeID, attempt, maxAttempts, 0, generation, nil); err != nil {
			return Event{}, err
		}
		return Event{
			RunID: runID, ProjectID: cp.projectID, Kind: EventPhaseMaterialized,
			CorrelationID: ref.String(),
			Payload:       map[string]any{"node_id": nodeID, "attempt": attempt, "generation": generation},
		}, nil
	}
	if _, err := store.New(tx).InsertGatePhase(ctx, store.InsertGatePhaseParams{
		RunID: runID, NodeID: nodeID, Attempt: attempt, MaxAttempts: maxAttempts,
		Generation: generation, CreatedAt: o.svc.clock.Now(),
	}); err != nil {
		return Event{}, fmt.Errorf("runs: insert gate %s/%s#%d: %w", runID, nodeID, attempt, err)
	}
	payload := map[string]any{"node_id": nodeID, "attempt": attempt, "generation": generation}
	if rev := cp.plan.Nodes[nodeID].Revision; rev != nil {
		payload["revision_target"] = rev.Target
		payload["max_rounds"] = rev.MaxRounds
	}
	if pub, ok := cp.plan.PublishFor(nodeID); ok {
		// The attention item says what an approval will authorize. A gate
		// that authorizes nothing is a checkpoint; one that authorizes a
		// publish is a release decision, and the UI must not conflate them.
		payload["authorizes"] = pub
	}
	return Event{
		RunID: runID, ProjectID: cp.projectID, Kind: EventGateWaiting,
		CorrelationID: ref.String(), Payload: payload,
	}, nil
}
```

`insertPhaseTx` grows the generation parameter:

```go
func (s *Service) insertPhaseTx(ctx context.Context, tx pgx.Tx, runID uuid.UUID, nodeID string,
	attempt, maxAttempts, chargeableFailures, generation int32, notBefore *time.Time,
) (uuid.UUID, error) {
	row, err := store.New(tx).InsertPhase(ctx, store.InsertPhaseParams{
		RunID: runID, NodeID: nodeID, Attempt: attempt, MaxAttempts: maxAttempts,
		ChargeableFailures: chargeableFailures, NotBefore: notBefore,
		Generation: generation, CreatedAt: s.clock.Now(),
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("runs: insert phase %s/%s#%d: %w", runID, nodeID, attempt, err)
	}
	return row.ID, nil
}
```

- [ ] **Step 5: Route both materialization sites through it**

In `startPendingRuns`, replace the root loop:

```go
			for _, root := range cp.plan.Roots() {
				ev, err := o.materializeNode(ctx, tx, cp, runID, root, 1, maxAttempts, 1)
				if err != nil {
					return err
				}
				evs = append(evs, ev)
			}
```

(The generation is literally 1: a run that has not started cannot have revised.)

In `evaluateSuccess`, replace the successor loop and follow it with the waiting transition:

```go
	gen, err := o.generationOf(ctx, q, c.Ref.RunID)
	if err != nil {
		return false, err
	}
	for _, nodeID := range cp.plan.Order {
		if attempted[nodeID] || !cp.plan.Ready(nodeID, succeeded) {
			continue
		}
		ev, err := o.materializeNode(ctx, tx, cp, c.Ref.RunID, nodeID, 1, c.MaxAttempts, gen)
		if err != nil {
			return false, err
		}
		evs = append(evs, ev)
	}
```

and before the completion check:

```go
	// The run's status is READ BACK from the database after the inserts, never
	// inferred from what this pass happened to do (baked decision 20).
	//
	// "Did I just insert a gate" is the wrong question twice over, and a
	// fan-out DAG asks it both ways. Insert a gate beside a runnable sibling
	// and it says waiting while the sibling is still pending — the run claims
	// to be waiting on a human who is not the blocker. Let a sibling finish
	// while a gate is ALREADY waiting and it says nothing, so the run stays
	// running — and because FinishGatePhase predicates on the run being
	// waiting (Task 3), that gate is then undecidable until the wall deadline
	// kills it. Nothing in the validator forbids either shape: it checks form,
	// cycles, lineage, revisions and publication dominance, not "one branch at
	// a time".
	//
	// The post-insertion state answers it exactly, in one statement that also
	// excludes this phase's own row — phaseRows was read before this
	// transaction finished it, and counting it as live would keep the run
	// running forever.
	waiting, err := q.RunIsWaitingOnAGate(ctx, store.RunIsWaitingOnAGateParams{
		RunID: c.Ref.RunID, ExcludePhase: c.PhaseID,
	})
	if err != nil {
		return false, err
	}
	if waiting {
		// A run whose only remaining work is a human's decision is 'waiting',
		// not 'running': the status is what the attention stream and the task
		// list render, and it is what tells a human the machine is done for
		// now. The CAS is deliberately allowed to find zero rows — the run may
		// have been terminalized under us, which is not a conflict.
		if _, err := q.CASRunStatus(ctx, store.CASRunStatusParams{
			ID: c.Ref.RunID, FromStatus: string(RunRunning), ToStatus: string(RunWaiting),
		}); err != nil {
			return false, err
		}
	}
```

Task 5 factors this loop and this read out into `materializeReady`, which the janitor calls too. Landing the read here rather than the inference is not a stylistic preference: the inference is one-directional (it never *leaves* `waiting`), so between a Task 4 commit and a Task 5 commit a gate materialized beside a runnable sibling parks the run — a real defect in the interval, and one the plan's own decisions call a bug.

- [ ] **Step 6: Thread the generation into artifacts**

`insertArtifacts` gains the parameter and stamps it:

```go
func insertArtifacts(ctx context.Context, q *store.Queries, ref AttemptRef,
	artifacts []NewArtifact, rawParents []byte, generation int32, now time.Time,
) ([]string, error) {
	...
		if _, err := q.InsertArtifact(ctx, store.InsertArtifactParams{
			ID: id, RunID: ref.RunID, NodeID: ref.NodeID, Attempt: ref.Attempt,
			Generation: generation, Type: a.Type, Payload: rawPayload,
			ParentIds: rawParents, CreatedAt: now,
		}); err != nil {
	...
}
```

Both `evaluateSuccess` and `evaluateFailure` pass the generation they read. In `evaluateFailure`, read it once before the branch:

```go
	gen, err := o.generationOf(ctx, q, c.Ref.RunID)
	if err != nil {
		return false, err
	}
```

and pass `gen` to both `insertArtifacts` calls and to the retry's `insertPhaseTx`.

Then prove no scaffolding literal survived. Task 3 Step 8 passed a literal `1` at every generation site behind a `// Task 4 threads the run's real generation here.` comment; a site this step misses does not fail — it silently stamps generation 1 on a revised run's rows, and every provenance read downstream is wrong in a way nothing surfaces.

Run: `rg 'generation:\s*1' backend/internal/runs/`
Expected: only the root-loop site in `startPendingRuns` (Step 5), where a run that has not started legitimately keeps its `1`. Anything else is a site Task 3 stubbed and this step forgot.

- [ ] **Step 7: Let a waiting run be terminalized**

`finishRunTx` and `terminalizeRun` both call `FinishRun`, whose SQL already accepts `status IN ('pending','running','waiting')`. Nothing changes there. `SupersedeLivePhases` was widened in Task 3 to include `waiting_decision`, so a wall-deadline death supersedes the waiting gate — which is what the second test asserts.

One real change: `settleRunCredentials` iterates the run's phases and must keep skipping gates. Its `switch` already handles only `PhaseRunning` and `PhaseReconciling`, with `default: continue`. Add the reason:

```go
		switch PhaseStatus(p.Status) {
		case PhaseRunning:
			...
		case PhaseReconciling:
			...
		default:
			// Terminal, never claimed, or waiting on a human. A gate in
			// particular ran no container and therefore holds no credential;
			// there is nothing here to settle.
			continue
		}
```

- [ ] **Step 8: Run the gate tests**

Run: `task backend:test:integration -- -run 'TestOrchestrator_Gate|TestOrchestrator_WallDeadlineStillKills' -race -v`
Expected: PASS.

- [ ] **Step 9: Run the whole spine integration suite**

Run: `task backend:test:integration`
Expected: PASS — Plan 05's gateless scenarios must be untouched.

- [ ] **Step 10: Commit**

```bash
git add backend/internal/runs/orchestrator.go backend/internal/runs/queue.go \
        backend/internal/runs/orchestrator_gates_integration_test.go
git commit -m "feat(runs): materialize gates as waiting phases and move the run to waiting"
```

---

### Task 5: The gate decision — CAS, deadline predicate, and the single-use authorization

The deciding transaction. Approve mints the ApprovedPatch; reject terminalizes the run; request-changes is Task 6's job and is stubbed here with a typed `NotImplemented` so this task's tests cannot accidentally pass through it (stubs never fake success).

**Files:**
- Create: `backend/internal/runs/gates.go`
- Modify: `backend/internal/runs/service.go` (`Service` :27-45 — no principal source is needed here; the requester rides on the task row, and Task 9 adds `PrincipalSource` only because a publication has to render an email)
- Modify: `backend/internal/runs/orchestrator.go` (`materializeReady`/`syncWaiting`/`anyRunnablePhase`, the janitor's materialization tick, and `finishRunTx` :920-960 extracted to a free function so the decision path can share it)
- Modify: `backend/internal/runs/ports.go` (nothing yet — Task 7 grows `Forge`)
- Test: `backend/internal/runs/gates_integration_test.go` (new)

**Interfaces:**
- Consumes: `store.GetLiveGateForUpdate`, `FinishGatePhase`, `InsertGateDecision`, `CountRequestChanges`, `InsertApprovedPatch`, `GetNodeArtifact`, `CASRunStatus`, `ExecPlan.ExaminedInput`, `ExecPlan.PublishFor`, `audit.Recorder`.
- Produces:
  - `runs.Decision string` with `DecisionApprove`, `DecisionRequestChanges`, `DecisionReject`
  - `runs.GateDecisionInput{RunID uuid.UUID; NodeID string; Decision Decision; Note string}`
  - `runs.GateDecisionResult{DecisionID uuid.UUID; RunStatus RunStatus; Generation int32; AuthorizationID *uuid.UUID}`
  - `func (s *Service) DecideGate(ctx context.Context, actor authn.Principal, in GateDecisionInput) (GateDecisionResult, error)`
  - `func (s *Service) GateProject(ctx context.Context, runID uuid.UUID) (uuid.UUID, error)` — the API's pre-write authorization lookup
  - `func (s *Service) ListWaitingGates(ctx context.Context, projectIDs []uuid.UUID, all bool) ([]WaitingGate, error)`
  - `runs.WaitingGate{PhaseID, RunID, TaskID, ProjectID uuid.UUID; NodeID, TaskTitle string; Attempt, Generation int32; RequesterID uuid.UUID; WaitingSince time.Time}`

- [ ] **Step 1: Write the failing decision tests**

In `backend/internal/runs/gates_integration_test.go`:

```go
//go:build integration

package runs_test

func TestDecideGate_ApproveMintsASingleUseAuthorizationBoundToTheExaminedArtifact(t *testing.T) {
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	patch := h.artifact(t, run.ID, "implement", runs.ArtifactKindPatch)

	res, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove, Note: "ship it",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.AuthorizationID == nil {
		t.Fatal("an approval that dominates a publish must mint an authorization")
	}

	auth := h.authorization(t, *res.AuthorizationID)
	if auth.ArtifactID != patch.ID {
		t.Errorf("authorization binds artifact %s, want the examined %s", auth.ArtifactID, patch.ID)
	}
	if auth.BaseSHA != run.BaseSHA {
		t.Errorf("base_sha = %q, want the run's pinned %q", auth.BaseSHA, run.BaseSHA)
	}
	if auth.TreeHash != patch.Payload["tree_hash"] {
		t.Errorf("tree_hash = %q, want the patch's %v", auth.TreeHash, patch.Payload["tree_hash"])
	}
	if auth.PublishNodeID != "publish" || auth.GateNodeID != "gate" {
		t.Errorf("authorization scope = %s/%s, want gate/publish", auth.GateNodeID, auth.PublishNodeID)
	}
	if auth.Status != "valid" {
		t.Errorf("status = %q, want valid", auth.Status)
	}

	// The gate row succeeded and the run resumed.
	if gate := h.phases(t, run.ID).byNode(t, "gate"); gate.Status != runs.PhaseSucceeded {
		t.Errorf("gate status = %s, want succeeded", gate.Status)
	}
	h.waitForRunStatus(t, run.ID, runs.RunRunning, 10*time.Second)
}

func TestDecideGate_SecondDecisionOnTheSameAttemptIsAConflict(t *testing.T) {
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionReject,
	})
	if xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("second decision error = %v (class %s), want conflict", err, xerrors.ClassOf(err))
	}
}

func TestDecideGate_ConcurrentDecidersProduceExactlyOneDecision(t *testing.T) {
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	const n = 8
	var wg sync.WaitGroup
	results := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, results[i] = h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
				RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	won := 0
	for i, err := range results {
		switch {
		case err == nil:
			won++
		case xerrors.ClassOf(err) == xerrors.ClassConflict:
		default:
			t.Errorf("decider %d got an unexpected error: %v", i, err)
		}
	}
	if won != 1 {
		t.Fatalf("%d deciders won, want exactly 1", won)
	}
	if got := h.countAuthorizations(t, run.ID); got != 1 {
		t.Fatalf("authorizations = %d, want 1: the token is single-use", got)
	}
}

func TestDecideGate_RejectTerminalizesTheRun(t *testing.T) {
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	res, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionReject, Note: "wrong approach",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RunStatus != runs.RunFailed {
		t.Fatalf("run status = %s, want failed", res.RunStatus)
	}
	got := h.run(t, run.ID)
	if got.Outcome == nil || got.Outcome.Code != runs.CodeGateRejected {
		t.Fatalf("outcome = %+v, want gate_rejected", got.Outcome)
	}
	if got.Outcome.Message == "" || !strings.Contains(got.Outcome.Message, "gate") {
		t.Errorf("the outcome must name the gate: %q", got.Outcome.Message)
	}
	if h.countAuthorizations(t, run.ID) != 0 {
		t.Error("a reject authorizes nothing")
	}
	// The publish node never materialized, so nothing is left to supersede
	// except the gate row itself.
	if gate := h.phases(t, run.ID).byNode(t, "gate"); gate.Status != runs.PhaseFailed {
		t.Errorf("gate status = %s, want failed", gate.Status)
	}
}

func TestDecideGate_ExpiredRunRefusesTheDecisionAndDoesNotAuthorize(t *testing.T) {
	// spec §3 Budgets: every deciding transaction carries the wall-deadline
	// predicate against the DATABASE clock, so an approval can never win a
	// race against the deadline sweeper.
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	h.stopOrchestrator(t) // freeze the sweeper so this is a pure predicate test
	h.backdateWallDeadline(t, run.ID)

	_, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	})
	if xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("error = %v (class %s), want a conflict naming the expired run", err, xerrors.ClassOf(err))
	}
	if h.countAuthorizations(t, run.ID) != 0 {
		t.Fatal("an expired run must authorize nothing")
	}
	if gate := h.phases(t, run.ID).byNode(t, "gate"); gate.Status != runs.PhaseWaitingDecision {
		t.Errorf("a refused decision must leave the gate waiting, got %s", gate.Status)
	}
}

func TestDecideGate_SelfApprovalIsRefusedWhenThePolicyForbidsItAndMarkedWhenItAllows(t *testing.T) {
	h := newSpineHarness(t)
	// The requester is also the approver.
	requester := h.newUser(t, "solo@example.com", authn.GrantApprove)

	strict := runs.DefaultSnapshot()
	strict.AllowSelfApproval = false
	_, run := h.createTaskAs(t, requester, conveyorWorkflow(t, h), strict)
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	_, err := h.svc.DecideGate(h.ctx, requester, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	})
	if xerrors.ClassOf(err) != xerrors.ClassForbidden {
		t.Fatalf("error = %v (class %s), want forbidden", err, xerrors.ClassOf(err))
	}

	// The shipped baseline allows it, and marks it.
	_, run2 := h.createTaskAs(t, requester, conveyorWorkflow(t, h), runs.DefaultSnapshot())
	h.waitForRunStatus(t, run2.ID, runs.RunWaiting, 20*time.Second)
	res, err := h.svc.DecideGate(h.ctx, requester, runs.GateDecisionInput{
		RunID: run2.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !h.decision(t, res.DecisionID).SelfApproval {
		t.Error("a self-approval must be marked distinctly (spec §6)")
	}
	if !h.auditDetailBool(t, "gate.decided", res.DecisionID.String(), "self_approval") {
		t.Error("the audit entry must carry the self-approval marker too")
	}
}

func TestDecideGate_GateWithoutAPublishAuthorizesNothingAndStillResumesTheRun(t *testing.T) {
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	// plan → implement → gate → review: the gate is a checkpoint, not a
	// release decision. Approving it must not mint a token nothing can spend.
	_, run := h.createTask(t, checkpointWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	res, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.AuthorizationID != nil {
		t.Fatal("a gate that dominates no publish must mint no authorization")
	}
	h.waitForRunStatus(t, run.ID, runs.RunSucceeded, 20*time.Second)
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `task backend:test:integration -- -run TestDecideGate -v`
Expected: FAIL — `h.svc.DecideGate undefined`.

- [ ] **Step 3: Write the decision vocabulary and input validation**

Create `backend/internal/runs/gates.go`:

```go
package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// Decision is a gate's typed outcome (spec §2): approve continues the
// forward DAG, request_changes opens a bounded revision generation, reject
// ends the run with a typed terminal outcome.
type Decision string

const (
	DecisionApprove        Decision = "approve"
	DecisionRequestChanges Decision = "request_changes"
	DecisionReject         Decision = "reject"
)

func (d Decision) Valid() bool {
	return d == DecisionApprove || d == DecisionRequestChanges || d == DecisionReject
}

// maxGateNoteLen bounds the note (bytes, house convention). The note is a
// first-class input: request_changes feeds it to the revision target as a
// context file, so it is sized like an instruction, not like a comment.
const maxGateNoteLen = 8 << 10

type GateDecisionInput struct {
	RunID    uuid.UUID
	NodeID   string
	Decision Decision
	Note     string
}

type GateDecisionResult struct {
	DecisionID uuid.UUID
	RunStatus  RunStatus
	Generation int32
	// AuthorizationID is set only when the approval authorizes a publish.
	AuthorizationID *uuid.UUID
}

func validateDecision(in GateDecisionInput) error {
	e := xerrors.E(xerrors.ClassInvalid, "validation failed")
	ok := true
	if !in.Decision.Valid() {
		e = e.WithFields(xerrors.FieldError{Field: "decision", Message: "must be approve, request_changes or reject"})
		ok = false
	}
	if len(in.Note) > maxGateNoteLen {
		e = e.WithFields(xerrors.FieldError{Field: "note", Message: fmt.Sprintf("must be at most %d bytes", maxGateNoteLen)})
		ok = false
	}
	if in.Decision == DecisionRequestChanges && in.Note == "" {
		// A revision with no note re-runs the same work with the same
		// information and reaches the same patch. The note IS the new input.
		e = e.WithFields(xerrors.FieldError{Field: "note", Message: "request_changes requires a note; it is the revision's only new input"})
		ok = false
	}
	if !ok {
		return e
	}
	return nil
}
```

- [ ] **Step 4: Write the decision transaction**

Still in `gates.go`. The shape mirrors `Rerun`: resolve pins outside the transaction (a port call inside would hold a pooled connection and the gate's row lock across a peer-context query), then decide under the lock.

```go
// GateProject resolves a run to its project for the API's pre-write
// authorization check (approve grant on the run's project, spec §6).
func (s *Service) GateProject(ctx context.Context, runID uuid.UUID) (uuid.UUID, error) {
	row, err := store.New(s.pool).GetRunProjectID(ctx, runID)
	if err != nil {
		return uuid.Nil, notFound(err, "no such run")
	}
	return row, nil
}

// DecideGate is THE gate transaction (spec §4: deciding mutations are CAS
// end-to-end). Under one row lock it validates the decision against the
// live gate, appends the decision row, transitions the gate and the run,
// mints or withholds the ApprovedPatch, audits, and emits its events.
//
// The plan is compiled outside the transaction for the same reason Rerun
// resolves its pins outside one: the definition source draws on this pool,
// and holding the gate's lock across that query could self-deadlock a
// saturated pool. The pinned version is immutable, so reading it up front is
// safe.
func (s *Service) DecideGate(ctx context.Context, actor authn.Principal, in GateDecisionInput) (GateDecisionResult, error) {
	if err := validateDecision(in); err != nil {
		return GateDecisionResult{}, err
	}
	runRow, err := store.New(s.pool).GetRun(ctx, in.RunID)
	if err != nil {
		return GateDecisionResult{}, notFound(err, "no such run")
	}
	taskRow, err := store.New(s.pool).GetTask(ctx, runRow.TaskID)
	if err != nil {
		return GateDecisionResult{}, err
	}
	def, err := s.defs.DefinitionByVersionID(ctx, taskRow.WorkflowVersionID)
	if err != nil {
		return GateDecisionResult{}, err
	}
	plan, err := compilePinned(def)
	if err != nil {
		return GateDecisionResult{}, err
	}
	repo, err := s.repos.Repo(ctx, taskRow.RepoID)
	if err != nil {
		return GateDecisionResult{}, err
	}

	correlationID := httpx.CorrelationID(ctx)
	var res GateDecisionResult
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		gate, err := q.GetLiveGateForUpdate(ctx, store.GetLiveGateForUpdateParams{
			RunID: in.RunID, NodeID: in.NodeID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// Either the node is not a gate of this run, or its attempt has
			// already been decided, superseded or swept. All of them are the
			// same answer to the caller: this decision is not available.
			return xerrors.E(xerrors.ClassConflict, "no gate is awaiting a decision on this node")
		}
		if err != nil {
			return fmt.Errorf("runs: lock gate: %w", err)
		}
		dbNow, err := q.DBNow(ctx)
		if err != nil {
			return err
		}
		if !gate.RunWallDeadline.After(dbNow) {
			// The database clock decides, so the sweeper and this transaction
			// cannot disagree about which side of the deadline they are on.
			return xerrors.E(xerrors.ClassConflict, "the run's wall deadline has passed; it can no longer be decided")
		}

		snap, err := snapshotFromRow(gate.PolicySnapshot)
		if err != nil {
			return err
		}
		selfApproval := actor.UserID == gate.RequesterID
		if selfApproval && !snap.AllowSelfApproval {
			return xerrors.E(xerrors.ClassForbidden, "this run's policy does not permit the requester to decide their own gate")
		}

		examined, ok := plan.ExaminedInput(in.NodeID)
		if !ok {
			return fmt.Errorf("runs: gate %q examines nothing; stored definition %s is corrupt", in.NodeID, def.VersionID)
		}
		art, err := q.GetNodeArtifact(ctx, store.GetNodeArtifactParams{
			RunID: in.RunID, NodeID: examined.Origin, Type: examined.Artifact,
		})
		if err != nil {
			return fmt.Errorf("runs: examined artifact %s/%s: %w", examined.Origin, examined.Artifact, err)
		}

		if in.Decision == DecisionRequestChanges {
			return s.openRevision(ctx, tx, revisionContext{
				plan: plan, gate: gate, art: art, in: in, actor: actor,
				projectID: taskRow.ProjectID, correlationID: correlationID, now: dbNow,
			}, &res)
		}

		decision, err := q.InsertGateDecision(ctx, store.InsertGateDecisionParams{
			PhaseRunID: gate.ID, RunID: in.RunID, NodeID: in.NodeID, Generation: gate.Generation,
			Decision: string(in.Decision), ExaminedArtifactID: art.ID, Note: in.Note,
			ActedBy: actor.UserID, SelfApproval: selfApproval, ActedAt: dbNow,
		})
		if isUniqueViolation(err) {
			// Another decider won under the same lock is impossible; this is
			// the row having been decided in a prior, committed transaction
			// whose gate row we somehow still read as waiting. Report it as
			// what it is rather than as a 500.
			return xerrors.E(xerrors.ClassConflict, "this gate attempt has already been decided")
		}
		if err != nil {
			return fmt.Errorf("runs: insert gate decision: %w", err)
		}
		res.DecisionID = decision.ID
		res.Generation = gate.Generation

		evs := []Event{{
			RunID: in.RunID, ProjectID: taskRow.ProjectID, Kind: EventGateDecided,
			CorrelationID: correlationID,
			Payload: map[string]any{
				"node_id": in.NodeID, "attempt": gate.Attempt, "generation": gate.Generation,
				"decision": string(in.Decision), "examined_artifact_id": art.ID.String(),
				"acted_by": actor.UserID.String(), "self_approval": selfApproval,
			},
		}}

		switch in.Decision {
		case DecisionReject:
			if err := s.finishGate(ctx, tx, gate.ID, PhaseFailed, Outcome{
				Class: OutcomePermanent, Code: CodeGateRejected,
				Message: fmt.Sprintf("gate %q was rejected", in.NodeID),
			}, dbNow); err != nil {
				return err
			}
			res.RunStatus = RunFailed
			if err := s.finishRunFromGate(ctx, tx, taskRow.ProjectID, in.RunID, RunFailed, Outcome{
				Class: OutcomePermanent, Code: CodeGateRejected,
				Message: fmt.Sprintf("gate %q was rejected", in.NodeID),
			}, EventRunFailed, dbNow, evs); err != nil {
				return err
			}
		case DecisionApprove:
			if err := s.finishGate(ctx, tx, gate.ID, PhaseSucceeded, Outcome{}, dbNow); err != nil {
				return err
			}
			if publishNode, ok := plan.PublishFor(in.NodeID); ok {
				auth, err := s.mintAuthorization(ctx, q, mintInput{
					runID: in.RunID, gateNode: in.NodeID, publishNode: publishNode,
					decisionID: decision.ID, art: art, baseSHA: runRow.BaseSha,
					repo: repo, generation: gate.Generation, now: dbNow,
				})
				if err != nil {
					return err
				}
				res.AuthorizationID = &auth.ID
				evs = append(evs, Event{
					RunID: in.RunID, ProjectID: taskRow.ProjectID, Kind: EventGateDecided,
					CorrelationID: correlationID,
					Payload: map[string]any{
						"node_id": in.NodeID, "authorization_id": auth.ID.String(),
						"authorizes": publishNode, "artifact_id": art.ID.String(),
					},
				})
			}
			// The run resumes; the janitor's next tick materializes what the
			// gate unblocked. Resuming here rather than materializing inline
			// keeps ALL successor materialization in one place (evaluate),
			// which is the only reason readiness has a single definition.
			if _, err := q.CASRunStatus(ctx, store.CASRunStatusParams{
				ID: in.RunID, FromStatus: string(RunWaiting), ToStatus: string(RunRunning),
			}); err != nil {
				return err
			}
			res.RunStatus = RunRunning
			if err := appendEvents(ctx, tx, dbNow, evs); err != nil {
				return err
			}
		}

		return s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "gate.decided", SubjectKind: "gate_decision",
			SubjectID: decision.ID.String(), CorrelationID: correlationID,
			Detail: map[string]any{
				"run_id": in.RunID.String(), "node_id": in.NodeID,
				"decision": string(in.Decision), "self_approval": selfApproval,
				"examined_artifact_id": art.ID.String(),
			},
		})
	})
	if err != nil {
		return GateDecisionResult{}, err
	}
	return res, nil
}
```

Note the deliberate asymmetry: the **approve** path resumes the run and lets `evaluate` materialize the successors on the next orchestrator tick, while **reject** terminalizes inline. Materialization has exactly one implementation (`materializeNode`, called from `evaluateSuccess`), and duplicating it inside the decision would give readiness two definitions. The cost is one poll interval of latency on approval, which a human will never notice.

Wait — that is wrong in one respect, and it must be fixed rather than shipped: `evaluateSuccess` runs only when a *phase* completes, and the gate's completion is not a phase evaluation. Nothing would ever materialize the publish node. Fix it in Step 5.

- [ ] **Step 5: Give the janitor a materialization tick**

Add a janitor pass that materializes newly-ready nodes for running runs. It is the same loop `evaluateSuccess` runs, factored out so both callers share one definition of readiness:

```go
// materializeReady inserts a phase row for every node of run whose inputs
// have all succeeded and which has no row yet. It is the ONE definition of
// readiness; evaluateSuccess calls it after a phase lands, and the janitor
// calls it for runs a gate decision resumed (a decision is not a phase
// evaluation, so nothing else would ever pick those up).
//
// Returns the events to append and whether the run is now waiting on a human.
func (o *Orchestrator) materializeReady(ctx context.Context, tx pgx.Tx, cp cachedPlan,
	runID uuid.UUID, maxAttempts, generation int32, excludePhase uuid.UUID,
) ([]Event, bool, bool, error) {
	q := store.New(tx)
	succeededNodes, err := q.ListSucceededNodes(ctx, runID)
	if err != nil {
		return nil, false, false, err
	}
	succeeded := map[string]bool{}
	for _, id := range succeededNodes {
		succeeded[id] = true
	}
	phaseRows, err := q.ListPhasesForRun(ctx, runID)
	if err != nil {
		return nil, false, false, err
	}
	live := map[string]bool{}
	// The next attempt ordinal per node. phase_runs is UNIQUE on
	// (run_id, node_id, attempt) (schema.sql:256), so a node a revision
	// rewound already owns ordinal 1 and materializing another one would be a
	// 23505, not a fresh attempt. The rows are already in hand, so this costs
	// nothing beyond the loop we are already running.
	nextAttempt := map[string]int32{}
	for _, pr := range phaseRows {
		if !PhaseStatus(pr.Status).Terminal() || succeeded[pr.NodeID] {
			live[pr.NodeID] = true
		}
		if pr.Attempt >= nextAttempt[pr.NodeID] {
			nextAttempt[pr.NodeID] = pr.Attempt + 1
		}
	}
	var evs []Event
	for _, nodeID := range cp.plan.Order {
		if live[nodeID] || !cp.plan.Ready(nodeID, succeeded) {
			continue
		}
		attempt := nextAttempt[nodeID]
		if attempt == 0 {
			attempt = 1 // never materialized before
		}
		ev, err := o.materializeNode(ctx, tx, cp, runID, nodeID, attempt, maxAttempts, generation)
		if err != nil {
			return nil, false, false, err
		}
		evs = append(evs, ev)
	}

	// Run status is read back from the database AFTER the inserts, never
	// inferred from what this pass happened to do. This is the same read Task 4
	// Step 5 put in evaluateSuccess (and the reasoning is written down there);
	// extracting it here is what stops the two callers from ever answering the
	// question differently.
	//
	// `waiting` is DECLARED by this statement — there is no earlier `waiting :=
	// false` to shadow, because a pre-insertion default is exactly the inference
	// baked decision 20 forbids.
	waiting, err := q.RunIsWaitingOnAGate(ctx, store.RunIsWaitingOnAGateParams{
		RunID: runID, ExcludePhase: excludePhase,
	})
	if err != nil {
		return nil, false, false, err
	}
	complete := len(succeeded) == len(cp.plan.Nodes)
	return evs, waiting, complete && !anyRunnablePhase(phaseRows, excludePhase), nil
}
```

`RunIsWaitingOnAGate` itself landed in **Task 3 Step 7** and Task 4 is its first caller, so there is nothing to add to `queries.sql` here.

The completion half needs one small helper over the rows this transaction already holds. It lives here rather than in Task 4 because this is the task that gains a completion check with a fan-out to worry about:

```go
// anyRunnablePhase reports whether the run still has work a worker could pick
// up. A run is complete only when the ONLY thing left is nothing at all: a
// parallel branch still running means the last succeeded node was not the last
// node, whatever the succeeded count says.
//
// exclude is the phase whose own completion is being evaluated: phaseRows was
// read before this transaction finished that row, so it must be dropped
// explicitly or the run never completes.
func anyRunnablePhase(rows []store.PhaseRun, exclude uuid.UUID) bool {
	for _, r := range rows {
		if r.ID == exclude {
			continue
		}
		switch PhaseStatus(r.Status) {
		case PhasePending, PhaseRunning, PhaseReconciling:
			return true
		}
	}
	return false
}
```

`syncWaiting` is the single place a run enters or leaves `waiting`, so the two callers cannot drift:

```go
// syncWaiting moves the run between running and waiting to match what its
// phases actually say. Both directions matter: a gate materialized beside a
// runnable sibling must NOT park the run, and a sibling completing while a
// gate already waits must park it — that second case is the one that strands
// a gate forever, because FinishGatePhase refuses to decide a run that is not
// waiting.
//
// Both are plain CAS. A zero row count means the run was already on the right
// side (or terminalized under us), which is success, not a conflict.
func (o *Orchestrator) syncWaiting(ctx context.Context, tx pgx.Tx, runID uuid.UUID, waiting bool) error {
	from, to := RunRunning, RunWaiting
	if !waiting {
		from, to = RunWaiting, RunRunning
	}
	_, err := store.New(tx).CASRunStatus(ctx, store.CASRunStatusParams{
		ID: runID, FromStatus: string(from), ToStatus: string(to),
	})
	return err
}
```

`live` is the corrected `attempted` set: a node counts as spoken-for when it has a non-terminal row *or* its newest row succeeded. A node whose newest row is `failed`, `dead` or `superseded` and which is ready again (a revision rewound it) is materializable — which is exactly what Task 6 needs.

Careful: a node whose newest row is `dead` must NOT be re-materialized under ordinary retry semantics, because `evaluateFailure` already terminalized the run in that transaction. The run is terminal, so this loop never runs for it.

In the janitor loop, add the pass:

```go
		if err := o.materializeResumedRuns(ctx); err != nil {
			o.log.Error("orchestrator: materialize resumed runs", "error", err)
		}
```

```go
// materializeResumedRuns picks up runs a gate decision moved back to
// 'running' and materializes whatever the approval unblocked. Cheap: the
// query returns only runs whose newest gate decision is newer than their
// newest phase row, so a steady-state run matches nothing.
func (o *Orchestrator) materializeResumedRuns(ctx context.Context) error {
	ids, err := store.New(o.svc.pool).ListRunsAwaitingMaterialization(ctx, 16)
	if err != nil {
		return err
	}
	for _, runID := range ids {
		cp, err := o.loadPlan(ctx, runID)
		if err != nil {
			o.log.Error("orchestrator: resumed plan load", "run", runID, "error", err)
			continue
		}
		now := o.svc.clock.Now()
		err = pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
			q := store.New(tx)
			runRow, err := q.GetRunForUpdate(ctx, runID)
			if err != nil {
				return err
			}
			if RunStatus(runRow.Status) != RunRunning {
				return nil // terminalized or re-waiting under us; not ours.
			}
			snap, err := snapshotFromRow(runRow.PolicySnapshot)
			if err != nil {
				return err
			}
			evs, waiting, complete, err := o.materializeReady(ctx, tx, cp, runID,
				snap.MaxAttempts, runRow.Generation, uuid.Nil)
			if err != nil {
				return err
			}
			if complete {
				return o.finishRunTx(ctx, tx, cp, runID, RunSucceeded, Outcome{}, EventRunSucceeded, now, evs)
			}
			// Reconcile BOTH directions. `waiting` is now read from the
			// post-insertion state rather than from what this pass did, so
			// it is also the authority on leaving the waiting state: a run
			// that reads as not-waiting while sitting in RunWaiting has a
			// runnable phase again and must go back to running, or the
			// claim path never picks it up.
			if err := o.syncWaiting(ctx, tx, runID, waiting); err != nil {
				return err
			}
			return appendEvents(ctx, tx, now, evs)
		})
		if err != nil {
			o.log.Error("orchestrator: materialize resumed run", "run", runID, "error", err)
		}
	}
	return nil
}
```

with the query:

```sql
-- name: ListRunsAwaitingMaterialization :many
-- Running runs with a node that is ready and has no live-or-succeeded row.
-- Written as an anti-join rather than recomputing readiness in SQL: the
-- graph lives in Go, so this only has to be a cheap over-approximation that
-- materializeReady then decides precisely.
SELECT r.id FROM runs r
WHERE r.status = 'running'
  AND r.wall_deadline > now()
  AND NOT EXISTS (
      SELECT 1 FROM phase_runs pr
      WHERE pr.run_id = r.id
        AND pr.status IN ('pending', 'running', 'waiting_decision', 'reconciling')
  )
ORDER BY r.created_at
LIMIT $1;
```

That predicate is exact enough: a running run with no live phase row is either finished (caught by `complete`) or blocked behind a decision that just landed. Both are this pass's business, and both are idempotent.

Rewrite `evaluateSuccess`'s successor block to call `materializeReady` too, so there is genuinely one implementation:

```go
	evs2, waiting, complete, err := o.materializeReady(ctx, tx, cp, c.Ref.RunID, c.MaxAttempts, gen, c.PhaseID)
	if err != nil {
		return false, err
	}
	evs = append(evs, evs2...)
	if complete {
		return true, o.finishRunTx(ctx, tx, cp, c.Ref.RunID, RunSucceeded, Outcome{}, EventRunSucceeded, now, evs)
	}
	if err := o.syncWaiting(ctx, tx, c.Ref.RunID, waiting); err != nil {
		return false, err
	}
	return false, appendEvents(ctx, tx, now, evs)
```

- [ ] **Step 6: Write the gate helpers**

Back in `gates.go`:

```go
// finishGate is the gate's fencing CAS. There is no worker identity to
// match — a gate has no worker — so winning means being the transaction
// that found the row still waiting.
func (s *Service) finishGate(ctx context.Context, tx pgx.Tx, phaseID uuid.UUID, to PhaseStatus, out Outcome, now time.Time) error {
	if !PhaseWaitingDecision.CanTransition(to) {
		return fmt.Errorf("runs: illegal gate transition waiting_decision -> %s", to)
	}
	var raw []byte
	if out.Code != "" {
		var err error
		if raw, err = json.Marshal(out); err != nil {
			return err
		}
	}
	n, err := store.New(tx).FinishGatePhase(ctx, store.FinishGatePhaseParams{
		ID: phaseID, ToStatus: string(to), Outcome: raw, FinishedAt: &now,
	})
	if err != nil {
		return fmt.Errorf("runs: finish gate: %w", err)
	}
	if n == 0 {
		return xerrors.E(xerrors.ClassConflict, "the gate stopped waiting while this decision was being made")
	}
	return nil
}

type mintInput struct {
	runID                 uuid.UUID
	gateNode, publishNode string
	decisionID            uuid.UUID
	art                   store.Artifact
	baseSHA               string
	repo                  RepoRef
	generation            int32
	now                   time.Time
}

// mintAuthorization writes the single-use ApprovedPatch (spec §2). Every
// field it records is re-validated at consumption, so recording the WRONG
// value here is caught there rather than published: the tree hash comes off
// the examined artifact's own payload, and a patch artifact that carries no
// tree hash is corruption, not a publishable state.
func (s *Service) mintAuthorization(ctx context.Context, q *store.Queries, in mintInput) (store.ApprovedPatch, error) {
	payload, err := unmarshalMap(in.art.Payload)
	if err != nil {
		return store.ApprovedPatch{}, err
	}
	treeHash, _ := payload["tree_hash"].(string)
	if treeHash == "" {
		return store.ApprovedPatch{}, xerrors.E(xerrors.ClassConflict,
			"the examined patch carries no tree hash, so an approval over it could not be re-verified at publish time")
	}
	artBase, _ := payload["base_sha"].(string)
	if artBase != "" && artBase != in.baseSHA {
		// The artifact says it diffs from a different commit than the run
		// pinned. Publishing it would apply a patch to the wrong tree.
		return store.ApprovedPatch{}, xerrors.E(xerrors.ClassConflict,
			fmt.Sprintf("the examined patch diffs from %s but the run pinned %s", artBase, in.baseSHA))
	}
	row, err := q.InsertApprovedPatch(ctx, store.InsertApprovedPatchParams{
		RunID: in.runID, GateNodeID: in.gateNode, PublishNodeID: in.publishNode,
		GateDecisionID: in.decisionID, ArtifactID: in.art.ID, BaseSha: in.baseSHA,
		TreeHash: treeHash, RepoID: in.repo.ID,
		ForgeKind: in.repo.Forge, RepoSlug: in.repo.Slug,
		TargetBranch: in.repo.DefaultBranch,
		Generation: in.generation, CreatedAt: in.now,
	})
	if isUniqueViolation(err) {
		return store.ApprovedPatch{}, xerrors.E(xerrors.ClassConflict,
			"this publish node already holds a live authorization")
	}
	if err != nil {
		return store.ApprovedPatch{}, fmt.Errorf("runs: mint authorization: %w", err)
	}
	return row, nil
}

// finishRunFromGate terminalizes a run whose gate ended it. The decision path
// runs in the API's goroutine, not the janitor's, so it has no Orchestrator
// receiver and no plan cache — but it must not have its own terminalization
// either. It forwards to the free function extracted from orchestrator.go.
func (s *Service) finishRunFromGate(ctx context.Context, tx pgx.Tx, projectID, runID uuid.UUID,
	to RunStatus, out Outcome, eventKind string, now time.Time, evs []Event,
) error {
	return finishRunTx(ctx, tx, s.audit, projectID, runID, to, out, eventKind, now, evs)
}

// isUniqueViolation mirrors the assets context's helper; contexts never
// share hand-rolled helpers across the boundary (six lines beats a seam).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
```

`finishRunFromGate` forwards rather than re-implementing, and that means `orchestrator.go`'s `finishRunTx` (:920-960) is extracted in this task. Its method form uses the receiver for exactly one thing — `o.svc.audit` — and `cp` for exactly one — `cp.projectID`. Take both as parameters and the body is layer-independent, which is the whole objection to a second copy: two terminalization paths drift, and the copy this replaces had already drifted, hardcoding the correlation id `"caesar-run-"+runID` where the original derives one.

```go
// finishRunTx terminalizes a run: FinishRun, supersede whatever is still live,
// append the caller's events plus the run's own, audit. A free function over
// exactly the values it needs, because its two callers reach it from different
// layers — the janitor holds an Orchestrator and a cachedPlan, the gate
// decision holds neither, and a run may have only one way to end.
func finishRunTx(ctx context.Context, tx pgx.Tx, rec *audit.Recorder,
	projectID, runID uuid.UUID, to RunStatus, out Outcome, eventKind string,
	now time.Time, evs []Event,
) error {
	// ...the existing body, verbatim, with o.svc.audit -> rec and
	// cp.projectID -> projectID. The correlation id keeps being DERIVED here;
	// that is the drift the duplicate introduced.
}

// The orchestrator keeps its method, now a forwarding shim, so its call sites
// are untouched.
func (o *Orchestrator) finishRunTx(ctx context.Context, tx pgx.Tx, cp cachedPlan,
	runID uuid.UUID, to RunStatus, out Outcome, eventKind string, now time.Time, evs []Event,
) error {
	return finishRunTx(ctx, tx, o.svc.audit, cp.projectID, runID, to, out, eventKind, now, evs)
}
```

- [ ] **Step 7: Stub `openRevision` honestly**

```go
type revisionContext struct {
	plan          ExecPlan
	gate          store.GetLiveGateForUpdateRow
	art           store.Artifact
	in            GateDecisionInput
	actor         authn.Principal
	projectID     uuid.UUID
	correlationID string
	now           time.Time
}

// openRevision is Task 6. Until then it refuses rather than approving-by-
// accident: a stub that returned nil would silently drop the human's
// decision (decisions §6.13, stubs never fake success).
func (s *Service) openRevision(context.Context, pgx.Tx, revisionContext, *GateDecisionResult) error {
	return xerrors.Wrap(xerrors.ClassInvalid, "request_changes arrives in Task 6", xerrors.ErrNotImplemented)
}
```

- [ ] **Step 8: Add the waiting-gate list**

```go
// WaitingGate is one attention item: a gate somebody has to decide.
type WaitingGate struct {
	PhaseID      uuid.UUID
	RunID        uuid.UUID
	TaskID       uuid.UUID
	ProjectID    uuid.UUID
	NodeID       string
	TaskTitle    string
	Attempt      int32
	Generation   int32
	RequesterID  uuid.UUID
	WaitingSince time.Time
}

// ListWaitingGates is the approvals inbox. all=true is the admin view; a
// member passes the project ids their grants cover, and an empty list is an
// empty inbox rather than everything.
func (s *Service) ListWaitingGates(ctx context.Context, projectIDs []uuid.UUID, all bool) ([]WaitingGate, error) {
	q := store.New(s.pool)
	var out []WaitingGate
	if all {
		rows, err := q.ListWaitingGates(ctx)
		if err != nil {
			return nil, fmt.Errorf("runs: list waiting gates: %w", err)
		}
		for _, r := range rows {
			out = append(out, waitingGateFrom(r.ID, r.RunID, r.TaskID, r.ProjectID, r.NodeID, r.Title, r.Attempt, r.Generation, r.RequesterID, r.CreatedAt))
		}
		return out, nil
	}
	if len(projectIDs) == 0 {
		return []WaitingGate{}, nil
	}
	rows, err := q.ListWaitingGatesByProjects(ctx, projectIDs)
	if err != nil {
		return nil, fmt.Errorf("runs: list waiting gates: %w", err)
	}
	for _, r := range rows {
		out = append(out, waitingGateFrom(r.ID, r.RunID, r.TaskID, r.ProjectID, r.NodeID, r.Title, r.Attempt, r.Generation, r.RequesterID, r.CreatedAt))
	}
	return out, nil
}
```

(`waitingGateFrom` is a five-line struct constructor; write it inline.)

- [ ] **Step 9: Regenerate the store package**

Run: `task db:sqlc`
Expected: regenerated store package compiles; git diff shows only the new methods (`ListRunsAwaitingMaterialization`).

- [ ] **Step 10: Run the decision tests**

Run: `task backend:test:integration -- -run TestDecideGate -race -v`
Expected: PASS, except `request_changes` cases (none in this task's set — Task 6 adds them).

- [ ] **Step 11: Run everything**

Run: `task backend:test:integration && go test ./internal/runs/... -race`
Expected: PASS.

- [ ] **Step 12: Commit**

```bash
git add backend/internal/runs/gates.go backend/internal/runs/gates_integration_test.go \
        backend/internal/runs/orchestrator.go backend/internal/runs/store/
git commit -m "feat(runs): decide gates under a CAS with the wall-deadline predicate and mint approved patches"
```

---

### Task 6: Generations — the revision rematerialization

`request_changes` rewinds the target-to-gate slice, invalidates the superseded generation's approvals, and seeds the target with the examined artifact and the gate note.

**Files:**
- Create: `backend/internal/runs/revisions.go`
- Modify: `backend/internal/runs/gates.go` (`openRevision` replaced)
- Modify: `backend/internal/runs/orchestrator.go` (`buildRequest` :725-769 — seed inputs)
- Test: `backend/internal/runs/revisions_integration_test.go` (new)

**Interfaces:**
- Consumes: `ExecPlan.RevisionSlice`, `store.SupersedePhasesInSlice`, `MaxAttemptForNode`, `BumpRunGeneration`, `InvalidateAuthorizations`, `CountRequestChanges`.
- Produces:
  - `runs.ArtifactKindGateNote = "gate_note"`
  - `func (s *Service) openRevision(...)` (real)
  - the seed convention: a `gate_note` artifact written at `node_id = <revision target>`, `attempt = 0`

- [ ] **Step 1: Write the failing revision tests**

In `backend/internal/runs/revisions_integration_test.go`:

```go
//go:build integration

package runs_test

func TestRequestChanges_RewindsTheSliceAndReDecidesOnTheNewPatch(t *testing.T) {
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h)) // gate rewinds to implement, max_rounds 2
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	firstPatch := h.artifact(t, run.ID, "implement", runs.ArtifactKindPatch)

	res, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges,
		Note: "rename the exported symbol",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Generation != 2 {
		t.Fatalf("generation = %d, want 2", res.Generation)
	}

	// The slice rewound: implement, review and gate all have fresh rows; plan
	// (which dominates the target) does not.
	phases := h.phases(t, run.ID)
	for _, node := range []string{"implement", "review", "gate"} {
		if n := phases.countRows(node); n != 2 {
			t.Errorf("%s has %d rows, want 2 (one per generation)", node, n)
		}
	}
	if n := phases.countRows("plan"); n != 1 {
		t.Errorf("plan has %d rows; a revision must not rewind past its target", n)
	}
	// The old rows stepped aside. Assert SUPERSEDED exactly — accepting
	// "succeeded or superseded" would pass on the version of this design where
	// a succeeded slice node is left alone, which is the version where the
	// node never re-runs and the gate re-decides against a stale verdict.
	if old := phases.row(t, "review", 1); old.Status != runs.PhaseSuperseded {
		t.Errorf("generation 1 review = %s, want superseded", old.Status)
	}

	// The run re-runs the slice and stops at the gate again with a NEW patch.
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 30*time.Second)
	secondPatch := h.artifact(t, run.ID, "implement", runs.ArtifactKindPatch)
	if secondPatch.ID == firstPatch.ID {
		t.Fatal("the gate must re-inspect a NEW terminal patch, not the one it rejected")
	}
	if secondPatch.Generation != 2 {
		t.Errorf("new patch generation = %d, want 2", secondPatch.Generation)
	}

	// And approving now binds the SECOND patch. An old approval can never
	// authorize a new artifact, and this is the other half of that: a new
	// approval never authorizes an old one.
	res2, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth := h.authorization(t, *res2.AuthorizationID); auth.ArtifactID != secondPatch.ID {
		t.Errorf("authorization binds %s, want the regenerated %s", auth.ArtifactID, secondPatch.ID)
	}
}

func TestRequestChanges_DownstreamSliceNodesReallyReExecute(t *testing.T) {
	// The sharpest edge of the whole generation design. Counting phase ROWS
	// is not enough — a row can exist without the node having run — and a
	// gate that re-decides is not enough either, because it will happily
	// re-decide against the PREVIOUS generation's review verdict. What must
	// be true is that the intermediate node produced a NEW artifact and that
	// the gate's second decision examined something derived from it.
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h)) // plan -> implement -> review -> gate
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	firstVerdict := h.artifact(t, run.ID, "review", runs.ArtifactKindReviewVerdict)

	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: "revise",
	}); err != nil {
		t.Fatal(err)
	}
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 40*time.Second)

	// review is INSIDE the slice, so it must have executed a second time and
	// produced its own generation-2 verdict.
	secondVerdict := h.artifact(t, run.ID, "review", runs.ArtifactKindReviewVerdict)
	if secondVerdict.ID == firstVerdict.ID {
		t.Fatal("review did not re-execute: the gate is re-deciding against generation 1's verdict")
	}
	if secondVerdict.Generation != 2 {
		t.Errorf("new verdict generation = %d, want 2", secondVerdict.Generation)
	}
	if got := h.executionCount(t, run.ID, "review"); got != 2 {
		t.Errorf("review executed %d times, want 2: a rewound node runs again, it does not merely get a row", got)
	}
	// plan dominates the target and is OUTSIDE the slice: it must not re-run.
	if got := h.executionCount(t, run.ID, "plan"); got != 1 {
		t.Errorf("plan executed %d times, want 1: a revision never rewinds past its target", got)
	}
	// The generation-1 rows stepped aside rather than being deleted.
	if old := h.phases(t, run.ID).row(t, "review", 1); old.Status != runs.PhaseSuperseded {
		t.Errorf("generation 1 review = %s, want superseded", old.Status)
	}
	// And the attempt ordinal continued rather than colliding with it.
	if newer := h.phases(t, run.ID).row(t, "review", 2); newer.Generation != 2 {
		t.Errorf("review attempt 2 generation = %d, want 2", newer.Generation)
	}
}

func TestRequestChanges_SeedsTheTargetWithTheExaminedArtifactAndTheNote(t *testing.T) {
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)
	examined := h.artifact(t, run.ID, "implement", runs.ArtifactKindPatch)

	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: "use a table test",
	}); err != nil {
		t.Fatal(err)
	}

	note := h.artifact(t, run.ID, "implement", runs.ArtifactKindGateNote)
	if got := note.Payload["note"]; got != "use a table test" {
		t.Errorf("note payload = %v, want the human's words", got)
	}
	if got := note.Payload["gate_node_id"]; got != "gate" {
		t.Errorf("note must name its gate, got %v", got)
	}
	if got := note.Payload["examined_artifact_id"]; got != examined.ID.String() {
		t.Errorf("note must name what was examined, got %v", got)
	}

	// The re-run implement attempt received BOTH: the patch it produced last
	// round (so it revises rather than restarting) and the note.
	req := h.waitForExecutionRequest(t, run.ID, "implement", 2, 30*time.Second)
	var sawPatch, sawNote bool
	for _, in := range req.Inputs {
		switch in.Type {
		case runs.ArtifactKindPatch:
			sawPatch = in.ID == examined.ID
		case runs.ArtifactKindGateNote:
			sawNote = true
		}
	}
	if !sawPatch {
		t.Error("the revision target must be seeded with the examined artifact")
	}
	if !sawNote {
		t.Error("the revision target must be seeded with the gate note")
	}
}

func TestRequestChanges_InvalidatesThePriorGenerationsAuthorization(t *testing.T) {
	// A gate that approved, then a SECOND gate downstream that requests
	// changes back past it, must leave no spendable token behind.
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, twoGateWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	first, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate_one", Decision: runs.DecisionApprove,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.AuthorizationID == nil {
		t.Fatal("gate_one authorizes publish_one")
	}
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 30*time.Second) // gate_two

	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate_two", Decision: runs.DecisionRequestChanges, Note: "start over",
	}); err != nil {
		t.Fatal(err)
	}
	if got := h.authorization(t, *first.AuthorizationID).Status; got != "invalidated" {
		t.Fatalf("prior authorization status = %q, want invalidated: a newer generation invalidates it (spec §2)", got)
	}
}

func TestRequestChanges_RefusedOnceAPublicationHasSettled(t *testing.T) {
	// Chained gates are legal and each authorizes its own publish. So the
	// first publication can be live on the forge while a later gate is still
	// waiting — and no transaction in this process can un-push a commit.
	// Invalidating authorizations does not help: the one that funded the
	// publication is CONSUMED, not valid.
	//
	// Nothing before Task 9 writes a publish_intents row, so this seeds the
	// settled one directly and exercises openRevision's refusal against it.
	// The END-TO-END proof of baked decision 17 — a real push and pull request
	// standing between two gates — is Task 9 Step 12's
	// TestRequestChanges_RefusedOnceARunHasPublished. The split is deliberate,
	// not a gap: the code under test is this task's, only the driving is not.
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, twoGateWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	first, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate_one", Decision: runs.DecisionApprove,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 30*time.Second) // gate_two
	h.seedPublishedIntent(t, run.ID, "publish_one", *first.AuthorizationID)

	_, err = h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate_two", Decision: runs.DecisionRequestChanges, Note: "start over",
	})
	if xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("error = %v (class %s), want a conflict naming the live publication", err, xerrors.ClassOf(err))
	}
	if got := h.run(t, run.ID).Generation; got != 1 {
		t.Errorf("generation = %d, want 1: the refused revision must open nothing", got)
	}
	// The human still has both real answers available.
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate_two", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatalf("approve must still be available: %v", err)
	}
}

func TestRequestChanges_RefusesOnceMaxRoundsIsSpent(t *testing.T) {
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h)) // max_rounds 2

	for round := 1; round <= 2; round++ {
		h.waitForRunStatus(t, run.ID, runs.RunWaiting, 30*time.Second)
		if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
			RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges,
			Note: fmt.Sprintf("round %d", round),
		}); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 30*time.Second)

	_, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: "one more",
	})
	if xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("error = %v (class %s), want a conflict naming the exhausted loop", err, xerrors.ClassOf(err))
	}
	// The gate is still decidable — the human must approve or reject, and
	// the run is NOT killed over a click.
	if got := h.run(t, run.ID); got.Status != runs.RunWaiting {
		t.Fatalf("run status = %s, want it still waiting", got.Status)
	}
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionReject,
	}); err != nil {
		t.Fatalf("reject must still be available: %v", err)
	}
}

func TestRequestChanges_RefusedOnAGateThatDeclaresNoTransition(t *testing.T) {
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, noRevisionWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	_, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: "n/a",
	})
	if xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatalf("error = %v (class %s), want invalid", err, xerrors.ClassOf(err))
	}
}

func TestRequestChanges_BudgetAndDeadlineSurviveTheGeneration(t *testing.T) {
	// "Lineage extends across generations on the run's budget" (spec §3):
	// a revision is not a fresh run and does not refund anything.
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	before := h.run(t, run.ID)
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: "again",
	}); err != nil {
		t.Fatal(err)
	}
	after := h.run(t, run.ID)
	if after.ExecutionUsedMS < before.ExecutionUsedMS {
		t.Errorf("usage fell from %d to %d; a revision refunds nothing", before.ExecutionUsedMS, after.ExecutionUsedMS)
	}
	if !after.WallDeadline.Equal(before.WallDeadline) {
		t.Errorf("wall deadline moved from %s to %s; the clock never pauses", before.WallDeadline, after.WallDeadline)
	}
}
```

One harness addition: `h.seedPublishedIntent(t, runID, nodeID, authID)` inserts a `publish_intents` row in `published` with plausible receipts, straight through the pool. It is test scaffolding, not production code — nothing in the tree writes that table until Task 9 — and it exists so this task can prove its own refusal without waiting four tasks for a publisher.

- [ ] **Step 2: Run them and watch them fail**

Run: `task backend:test:integration -- -run TestRequestChanges -v`
Expected: FAIL — `request_changes arrives in Task 6`.

- [ ] **Step 3: Add the gate-note artifact kind**

In `compile.go`:

```go
	ArtifactKindLogRef          = "log_ref"
	ArtifactKindFailureSnapshot = "failure_snapshot"
	// Plan 08 runtime outputs. Neither is an edge binding: the definitions
	// validator only knows plan/patch/review_verdict, and both of these are
	// written by the control plane rather than produced by a harness.
	ArtifactKindPublication = "publication"
	ArtifactKindGateNote    = "gate_note"
```

`validEdgeArtifact` is unchanged — that is exactly the point.

- [ ] **Step 4: Write the rematerialization**

Create `backend/internal/runs/revisions.go`:

```go
package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// seedAttempt is the attempt ordinal a seeded artifact carries. Zero is
// unreachable for a produced artifact (phase attempts start at 1), so the
// value itself says "the control plane put this here, no phase produced it".
const seedAttempt = 0

// openRevision replays a gate's bounded loop (spec §2, §3). In ONE
// transaction, sharing the decision's row lock:
//
//  1. bound the loop by max_rounds;
//  2. record the decision;
//  3. bump the run's generation;
//  4. invalidate every live authorization — a newer patch generation
//     invalidates prior approvals, so nothing spendable survives;
//  5. supersede every live row in the target→gate slice;
//  6. insert fresh attempt rows for the slice's ready head (the target);
//  7. seed the target with the examined artifact and the gate note.
//
// Everything downstream of the gate is invalidated implicitly: readiness is
// each node's NEWEST attempt row, and the slice's rows are now pending.
func (s *Service) openRevision(ctx context.Context, tx pgx.Tx, rc revisionContext, res *GateDecisionResult) error {
	q := store.New(tx)
	node := rc.plan.Nodes[rc.in.NodeID]
	if node.Revision == nil {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").WithFields(xerrors.FieldError{
			Field: "decision", Message: fmt.Sprintf("gate %q declares no on_request_changes transition", rc.in.NodeID),
		})
	}
	slice, ok := rc.plan.RevisionSlice(rc.in.NodeID)
	if !ok {
		return fmt.Errorf("runs: gate %q declares a revision with no compiled slice", rc.in.NodeID)
	}

	// A revision may not rewind work that has already been published.
	//
	// Chained gates are legal, and each authorizes its own publish
	// (workflows/validate_test.go, "chained gates each authorize their own
	// publish"). So gate_one can approve, its publish node can push a branch
	// and open a pull request, and gate_two — still waiting — can then be sent
	// back for changes. Invalidating authorizations does nothing about that:
	// the first one was CONSUMED, and the plan deliberately leaves consumed
	// rows alone because rewriting them would erase the fact that they funded
	// a publication. The branch and the pull request stay live on the forge,
	// now carrying work of a generation caesar considers superseded — which is
	// exactly what spec §3 forbids ("every downstream output and approval of
	// the superseded generation is invalidated").
	//
	// Nothing in this transaction can un-push a commit, so the honest move is
	// to refuse the decision, the same way an exhausted max_rounds does
	// (decision 12): the human keeps approve and reject, and no work is
	// destroyed over a click. Structural refusal in the validator was the
	// alternative and is worse — it would outlaw definitions that are fine
	// whenever the earlier publish has not actually run yet.
	published, err := q.CountSettledPublications(ctx, rc.in.RunID)
	if err != nil {
		return fmt.Errorf("runs: count settled publications: %w", err)
	}
	if published > 0 {
		return xerrors.E(xerrors.ClassConflict,
			"this run has already published; a revision cannot rewind work that is live on the forge — approve or reject")
	}

	spent, err := q.CountRequestChanges(ctx, store.CountRequestChangesParams{
		RunID: rc.in.RunID, NodeID: rc.in.NodeID,
	})
	if err != nil {
		return fmt.Errorf("runs: count revision rounds: %w", err)
	}
	if spent >= int64(node.Revision.MaxRounds) {
		// Refuse the decision rather than killing the run: the human keeps
		// approve and reject, and a bounded loop should not cost the work.
		return xerrors.E(xerrors.ClassConflict, fmt.Sprintf(
			"gate %q has spent all %d revision rounds; approve or reject it",
			rc.in.NodeID, node.Revision.MaxRounds))
	}

	selfApproval := rc.actor.UserID == rc.gate.RequesterID
	decision, err := q.InsertGateDecision(ctx, store.InsertGateDecisionParams{
		PhaseRunID: rc.gate.ID, RunID: rc.in.RunID, NodeID: rc.in.NodeID,
		Generation: rc.gate.Generation, Decision: string(DecisionRequestChanges),
		ExaminedArtifactID: rc.art.ID, Note: rc.in.Note, ActedBy: rc.actor.UserID,
		SelfApproval: selfApproval, ActedAt: rc.now,
	})
	if isUniqueViolation(err) {
		return xerrors.E(xerrors.ClassConflict, "this gate attempt has already been decided")
	}
	if err != nil {
		return fmt.Errorf("runs: insert gate decision: %w", err)
	}
	res.DecisionID = decision.ID

	// The gate's OWN row is decided BEFORE the slice supersede below, because
	// that supersede covers this row too and whichever runs first wins the
	// status. Deciding first records what actually happened — this attempt was
	// decided, and the decision row holds the answer — and the slice pass then
	// finds the row already superseded and skips it. Running it the other way
	// round makes this call a permanent no-op.
	//
	// This is also the statement carrying the wall-deadline predicate, so a
	// run that expired under the decision refuses here rather than opening a
	// generation nothing may execute.
	if err := s.finishGate(ctx, tx, rc.gate.ID, PhaseSuperseded, Outcome{
		Class: OutcomePermanent, Code: CodeSuperseded,
		Message: fmt.Sprintf("changes requested at gate %q", rc.in.NodeID),
	}, rc.now); err != nil {
		return err
	}

	generation, err := q.BumpRunGeneration(ctx, rc.in.RunID)
	if errorsIsNoRows(err) {
		return xerrors.E(xerrors.ClassConflict,
			"the run's wall deadline has passed; it can no longer be decided")
	}
	if err != nil {
		return fmt.Errorf("runs: bump generation: %w", err)
	}
	res.Generation = generation
	res.RunStatus = RunRunning

	invalidated, err := q.InvalidateAuthorizations(ctx, store.InvalidateAuthorizationsParams{
		RunID: rc.in.RunID, SettledAt: &rc.now,
	})
	if err != nil {
		return fmt.Errorf("runs: invalidate authorizations: %w", err)
	}

	superOut, err := json.Marshal(Outcome{
		Class: OutcomePermanent, Code: CodeSuperseded,
		Message: fmt.Sprintf("superseded by generation %d", generation),
	})
	if err != nil {
		return err
	}
	// This retires EVERY row in the slice, succeeded ones included. That is
	// the whole mechanism: readiness is each node's newest attempt row, so a
	// slice node whose generation-1 row succeeded would otherwise stay
	// readable as done and never re-run — the target would regenerate its
	// patch and the gate would re-decide against the OLD review verdict,
	// which is precisely the bug a revision exists to prevent.
	if _, err := q.SupersedePhasesInSlice(ctx, store.SupersedePhasesInSliceParams{
		RunID: rc.in.RunID, NodeIds: slice, Outcome: superOut, FinishedAt: &rc.now,
	}); err != nil {
		return fmt.Errorf("runs: supersede revision slice: %w", err)
	}

	// Seed the target BEFORE its phase row exists, so the row is never
	// claimable without its inputs in place.
	if err := s.seedRevisionTarget(ctx, q, rc, node.Revision.Target, generation); err != nil {
		return err
	}

	// Only the target is materialized here. Everything else in the slice
	// becomes ready as the target's output lands, through the one readiness
	// path — the janitor's materializeReady.
	nextAttempt, err := q.MaxAttemptForNode(ctx, store.MaxAttemptForNodeParams{
		RunID: rc.in.RunID, NodeID: node.Revision.Target,
	})
	if err != nil {
		return err
	}
	snap, err := snapshotFromRow(rc.gate.PolicySnapshot)
	if err != nil {
		return err
	}
	if _, err := s.insertPhaseTx(ctx, tx, rc.in.RunID, node.Revision.Target,
		nextAttempt+1, snap.MaxAttempts, 0, generation, nil); err != nil {
		return err
	}

	if _, err := q.CASRunStatus(ctx, store.CASRunStatusParams{
		ID: rc.in.RunID, FromStatus: string(RunWaiting), ToStatus: string(RunRunning),
	}); err != nil {
		return err
	}

	invalidatedIDs := make([]string, 0, len(invalidated))
	for _, a := range invalidated {
		invalidatedIDs = append(invalidatedIDs, a.ID.String())
	}
	evs := []Event{
		{
			RunID: rc.in.RunID, ProjectID: rc.projectID, Kind: EventGateDecided,
			CorrelationID: rc.correlationID,
			Payload: map[string]any{
				"node_id": rc.in.NodeID, "attempt": rc.gate.Attempt,
				"generation": rc.gate.Generation, "decision": string(DecisionRequestChanges),
				"examined_artifact_id": rc.art.ID.String(), "acted_by": rc.actor.UserID.String(),
				"self_approval": selfApproval,
			},
		},
		{
			RunID: rc.in.RunID, ProjectID: rc.projectID, Kind: EventRunRevisionOpened,
			CorrelationID: rc.correlationID,
			Payload: map[string]any{
				"generation": generation, "gate_node_id": rc.in.NodeID,
				"target": node.Revision.Target, "slice": slice,
				"round": spent + 1, "max_rounds": node.Revision.MaxRounds,
				"invalidated_authorizations": invalidatedIDs,
			},
		},
	}
	if err := appendEvents(ctx, tx, rc.now, evs); err != nil {
		return err
	}
	return s.audit.Record(ctx, tx, audit.Entry{
		Actor: actorID(rc.actor), Action: "gate.decided", SubjectKind: "gate_decision",
		SubjectID: decision.ID.String(), CorrelationID: rc.correlationID,
		Detail: map[string]any{
			"run_id": rc.in.RunID.String(), "node_id": rc.in.NodeID,
			"decision": string(DecisionRequestChanges), "self_approval": selfApproval,
			"examined_artifact_id": rc.art.ID.String(), "generation": generation,
			"invalidated_authorizations": invalidatedIDs,
		},
	})
}

// seedRevisionTarget writes the two inputs the new generation's target must
// see that its edges cannot give it: the artifact the gate examined (so the
// agent REVISES rather than starting over), and the note (the only new
// information in the loop). Both are written at the target's node id with
// attempt 0, which is unreachable for a produced artifact — the ordinal
// itself says the control plane put them there.
func (s *Service) seedRevisionTarget(ctx context.Context, q *store.Queries, rc revisionContext,
	target string, generation int32,
) error {
	noteID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	notePayload, err := json.Marshal(map[string]any{
		"note":                 rc.in.Note,
		"decision":             string(DecisionRequestChanges),
		"gate_node_id":         rc.in.NodeID,
		"examined_artifact_id": rc.art.ID.String(),
		"acted_by":             rc.actor.UserID.String(),
		"acted_at":             rc.now.UTC().Format(time.RFC3339),
		"generation":           generation,
	})
	if err != nil {
		return err
	}
	parents, err := json.Marshal([]string{rc.art.ID.String()})
	if err != nil {
		return err
	}
	if _, err := q.InsertArtifact(ctx, store.InsertArtifactParams{
		ID: noteID, RunID: rc.in.RunID, NodeID: target, Attempt: seedAttempt,
		Generation: generation, Type: ArtifactKindGateNote, Payload: notePayload,
		ParentIds: parents, CreatedAt: rc.now,
	}); err != nil {
		return fmt.Errorf("runs: seed gate note: %w", err)
	}

	// The examined artifact is carried forward under the TARGET's node id so
	// the seed reader finds it without knowing the graph. It is a copy of the
	// payload with the original as its parent — lineage stays intact, and the
	// original row is untouched because history is append-only.
	carryID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	if _, err := q.InsertArtifact(ctx, store.InsertArtifactParams{
		ID: carryID, RunID: rc.in.RunID, NodeID: target, Attempt: seedAttempt,
		Generation: generation, Type: rc.art.Type, Payload: rc.art.Payload,
		ParentIds: parents, CreatedAt: rc.now,
	}); err != nil {
		return fmt.Errorf("runs: seed examined artifact: %w", err)
	}
	return nil
}
```

**Careful with the carry-forward and `GetNodeArtifact`.** The seeded copy lives at `node_id = target`, the same key the target's own output uses. `GetNodeArtifact` takes the newest, so once the target produces its generation-2 patch, that wins — which is right. But between the seed and that output, a successor asking for the target's patch would read the seed. No successor is ready then (the target is pending), so the window is unreachable. Assert it anyway in Step 6.

- [ ] **Step 5: Feed the seeds into the request**

In `orchestrator.go`, `buildRequest` reads the node's structural inputs. Add the seeds:

```go
	// Seeded inputs: what a revision handed this node. They are stored at the
	// node's OWN id with attempt 0, so a successor's structural lookup can
	// never mistake them for a produced artifact of an ancestor.
	seeds, err := q.ListSeedArtifacts(ctx, store.ListSeedArtifactsParams{
		RunID: ref.RunID, NodeID: ref.NodeID,
	})
	if err != nil {
		return ExecutionRequest{}, fmt.Errorf("runs: seed artifacts for %s: %w", ref.NodeID, err)
	}
	for _, row := range seeds {
		payload, err := unmarshalMap(row.Payload)
		if err != nil {
			return ExecutionRequest{}, err
		}
		req.Inputs = append(req.Inputs, ArtifactInput{ID: row.ID, Type: row.Type, Payload: payload})
	}
```

with:

```sql
-- name: ListSeedArtifacts :many
-- What a revision handed this node. attempt = 0 is unreachable for a
-- produced artifact (phase attempts start at 1), so the ordinal alone
-- distinguishes a control-plane seed from a harness output. Only the newest
-- generation's seeds are returned: an older round's note is spent.
SELECT * FROM artifacts
WHERE run_id = $1 AND node_id = $2 AND attempt = 0
  AND generation = (SELECT generation FROM runs WHERE id = $1)
ORDER BY type, id;
```

Two consequences to be deliberate about:

1. `evaluateSuccess` computes lineage `parentIDs` from `node.Inputs` (structural only), so a seeded artifact is **not** recorded as a parent of the revised output. That is a real gap in the recorded lineage: the revised patch descends from the one the gate examined. Fix it by appending the seed ids to `parentIDs` in `evaluateSuccess` — read them from the same query.

2. Plan 07's staging refuses two patch-typed inputs on one node (the cumulative-patch contract). The seed **is** the node's only patch on a revision round: its structural patch input, if any, comes from an ancestor outside the slice which produced nothing new. Guard it: if a node has both a structural patch input and a seeded patch, the seed wins and the structural one is dropped, because the seed is strictly newer and cumulative. Implement that as an explicit filter with a comment, not as an accident of ordering:

```go
	// One patch reaches a phase, ever (Plan 07's cumulative-patch contract):
	// every runner-produced patch diffs from the run's base and subsumes its
	// ancestors, so a chain would double-apply. A seeded patch is strictly
	// newer than any structural one, so it displaces it.
	req.Inputs = collapsePatchInputs(req.Inputs)
```

```go
// collapsePatchInputs keeps at most one patch input: the last one appended,
// which is the seed when a revision supplied one and the structural binding
// otherwise. Non-patch inputs pass through untouched and in order.
func collapsePatchInputs(in []ArtifactInput) []ArtifactInput {
	lastPatch := -1
	for i, a := range in {
		if a.Type == ArtifactKindPatch {
			lastPatch = i
		}
	}
	if lastPatch < 0 {
		return in
	}
	out := make([]ArtifactInput, 0, len(in))
	for i, a := range in {
		if a.Type == ArtifactKindPatch && i != lastPatch {
			continue
		}
		out = append(out, a)
	}
	return out
}
```

Unit-test `collapsePatchInputs` directly in `orchestrator_test.go` — three cases: no patch, one patch, structural-plus-seed.

- [ ] **Step 6: Write the seed-window assertion**

In `revisions_integration_test.go`:

```go
func TestRequestChanges_TheNewPatchWinsEvenWhenTheAppClockRunsBehind(t *testing.T) {
	// Revision seeds are stamped from the DATABASE clock (they are written
	// inside the decision transaction); produced artifacts are stamped from
	// the injected application clock. Those are two independent clocks, and
	// nothing keeps them in step. If artifact selection ordered by created_at,
	// a host clock behind the database would sort the generation-2 patch
	// BEHIND its own generation-1 seed — and the gate would then approve, and
	// the publisher publish, content the agent already replaced.
	//
	// Ordering by (generation, attempt) makes the skew irrelevant, and this
	// test is what keeps it that way.
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)
	seedPatch := h.artifact(t, run.ID, "implement", runs.ArtifactKindPatch)

	// The host falls an hour behind the database, mid-run.
	h.clock.Set(h.dbNow(t).Add(-time.Hour))

	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: "revise",
	}); err != nil {
		t.Fatal(err)
	}
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 40*time.Second)

	current := h.artifact(t, run.ID, "implement", runs.ArtifactKindPatch)
	if current.ID == seedPatch.ID {
		t.Fatal("the gate is re-examining the seeded generation-1 patch: selection is ordering on a clock, not on provenance")
	}
	if current.Generation != 2 || current.Attempt == 0 {
		t.Errorf("current patch = generation %d attempt %d, want the produced generation-2 one", current.Generation, current.Attempt)
	}
	// And an approval binds the produced patch, not the seed.
	res, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth := h.authorization(t, *res.AuthorizationID); auth.ArtifactID != current.ID {
		t.Errorf("authorization binds %s, want the produced %s", auth.ArtifactID, current.ID)
	}
}

func TestRequestChanges_SeedIsNeverReadAsTheTargetsProducedArtifact(t *testing.T) {
	// The seeded copy shares the target's node id. Between the seed landing
	// and the new attempt producing its own patch, a successor asking for
	// "the target's patch" would read the seed. No successor is ready in that
	// window (the target is pending), and this test is what keeps that true.
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)
	h.stopOrchestrator(t) // freeze so the window stays open

	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionRequestChanges, Note: "hold",
	}); err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{"review", "gate"} {
		if p, ok := h.phases(t, run.ID).tryByNodeLive(node); ok {
			t.Fatalf("%s has a live row (%s) while the target has not re-run", node, p.Status)
		}
	}
}
```

- [ ] **Step 7: Regenerate the store package**

Run: `task db:sqlc`
Expected: regenerated store package compiles; git diff shows only the new methods (`ListSeedArtifacts`).

- [ ] **Step 8: Run the revision tests**

Run: `task backend:test:integration -- -run TestRequestChanges -race -v`
Expected: PASS.

- [ ] **Step 9: Run everything**

Run: `go test ./internal/runs/... -race && task backend:test:integration`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add backend/internal/runs/revisions.go backend/internal/runs/revisions_integration_test.go \
        backend/internal/runs/gates.go backend/internal/runs/compile.go \
        backend/internal/runs/orchestrator.go backend/internal/runs/orchestrator_test.go \
        backend/internal/runs/store/
git commit -m "feat(runs): rematerialize the revision slice into a new generation on request_changes"
```

---
### Task 7: `platform/forge` — hand-rolled GitHub and GitLab REST clients

Pure infrastructure, no bounded-context imports, no SDK. The four calls the publish machine needs and nothing else.

**Files:**
- Create: `backend/internal/platform/forge/forge.go`, `github.go`, `gitlab.go`
- Test: `backend/internal/platform/forge/github_test.go`, `gitlab_test.go` (httptest, never a live call)

**Interfaces:**
- Consumes: `net/http`, `encoding/json`. Nothing else.
- Produces:
  - `forge.Client` interface: `Head`, `BranchHead`, `CreatePullRequest`, `FindPullRequests`
  - `forge.Repo{Slug, DefaultBranch string}` and `forge.Auth{Token string}`
  - `forge.PullRequest{Head, Base, Title, Body string}`, `forge.PullRequestRef{Number int64; URL, HeadSHA, State string}`
  - `forge.ErrNotFound`, `forge.ErrConflict`, `forge.RetryableError`
  - `forge.NewGitHub(baseURL string, hc *http.Client) *GitHub`, `forge.NewGitLab(baseURL string, hc *http.Client) *GitLab`

- [ ] **Step 1: Write the failing GitHub tests**

In `backend/internal/platform/forge/github_test.go`:

```go
package forge_test

func TestGitHub_BranchHeadReadsTheRefObjectSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/repos/acme/widgets/git/ref/heads/caesar%2Fabc"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("api version header = %q; pinning it is what stops a silent shape change", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = io.WriteString(w, `{"ref":"refs/heads/caesar/abc","object":{"sha":"deadbeef","type":"commit"}}`)
	}))
	defer srv.Close()

	c := forge.NewGitHub(srv.URL, srv.Client())
	sha, ok, err := c.BranchHead(context.Background(), forge.Repo{Slug: "acme/widgets"}, forge.Auth{Token: "tok"}, "caesar/abc")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || sha != "deadbeef" {
		t.Fatalf("BranchHead = %q,%v; want deadbeef,true", sha, ok)
	}
}

func TestGitHub_BranchHeadReportsAbsenceRatherThanFailing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
	}))
	defer srv.Close()

	c := forge.NewGitHub(srv.URL, srv.Client())
	sha, ok, err := c.BranchHead(context.Background(), forge.Repo{Slug: "acme/widgets"}, forge.Auth{Token: "tok"}, "nope")
	if err != nil {
		t.Fatalf("an absent branch is an answer, not an error: %v", err)
	}
	if ok || sha != "" {
		t.Fatalf("BranchHead = %q,%v; want \"\",false", sha, ok)
	}
}

func TestGitHub_CreatePullRequestReturnsItsReceipt(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/acme/widgets/pulls" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"number":42,"html_url":"https://github.com/acme/widgets/pull/42","state":"open","head":{"sha":"deadbeef"},"base":{"ref":"main"}}`)
	}))
	defer srv.Close()

	c := forge.NewGitHub(srv.URL, srv.Client())
	got, err := c.CreatePullRequest(context.Background(), forge.Repo{Slug: "acme/widgets"}, forge.Auth{Token: "tok"},
		forge.PullRequest{Head: "caesar/abc", Base: "main", Title: "t", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	// Base is part of the receipt, not decoration: recovery locates a request
	// by head branch alone, and a candidate with no base is unprovable.
	want := forge.PullRequestRef{Number: 42, URL: "https://github.com/acme/widgets/pull/42", State: "open", HeadSHA: "deadbeef", Base: "main"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	if body["head"] != "caesar/abc" || body["base"] != "main" {
		t.Errorf("request body = %v", body)
	}
}

func TestGitHub_ErrorsAreClassifiedForRetry(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		headers     map[string]string
		body        string
		wantRetry   bool
		wantConflict bool
	}{
		{name: "pr exists", status: http.StatusUnprocessableEntity, wantConflict: true},
		// 403 is NOT a synonym for "permanently forbidden" on GitHub: both
		// primary and secondary rate limits are documented to answer 403 or
		// 429. Treating every 403 as permanent turns a few minutes of
		// throttling into a failed run with a branch already on the forge —
		// and re-requesting immediately makes a secondary limit worse.
		{name: "forbidden, no rate signal", status: http.StatusForbidden, wantRetry: false},
		{name: "primary limit as 403", status: http.StatusForbidden, wantRetry: true,
			headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1893456000"}},
		{name: "secondary limit as 403", status: http.StatusForbidden, wantRetry: true,
			headers: map[string]string{"Retry-After": "60"},
			body:    `{"message":"You have exceeded a secondary rate limit"}`},
		{name: "too many requests", status: http.StatusTooManyRequests, wantRetry: true},
		{name: "bad gateway", status: http.StatusBadGateway, wantRetry: true},
		{name: "unavailable", status: http.StatusServiceUnavailable, wantRetry: true},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			for k, v := range c.headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(c.status)
			body := c.body
			if body == "" {
				body = `{"message":"nope"}`
			}
			_, _ = io.WriteString(w, body)
		}))
		cl := forge.NewGitHub(srv.URL, srv.Client())
		_, err := cl.CreatePullRequest(context.Background(), forge.Repo{Slug: "a/b"}, forge.Auth{Token: "t"}, forge.PullRequest{})
		srv.Close()
		if err == nil {
			t.Fatalf("%s: status %d must error", c.name, c.status)
		}
		if got := forge.IsRetryable(err); got != c.wantRetry {
			t.Errorf("%s: retryable = %v, want %v", c.name, got, c.wantRetry)
		}
		if got := errors.Is(err, forge.ErrConflict); got != c.wantConflict {
			t.Errorf("%s: conflict = %v, want %v", c.name, got, c.wantConflict)
		}
		// A stated reset must survive as an instant, not just as "retryable":
		// it is the difference between backing off blind and waiting exactly
		// as long as the forge asked.
		if at, ok := forge.RetryAfter(err); c.wantRetry && len(c.headers) > 0 && !ok {
			t.Errorf("%s: rate-limited error must carry a retry instant (got %v)", c.name, at)
		}
	}
}

func TestGitHub_ErrorsNeverEchoTheToken(t *testing.T) {
	// The error is written into a run event and an intent's halt_reason. A
	// forge that reflected the Authorization header back would put a live
	// credential in the database (spec §6 redaction).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"bad token `+r.Header.Get("Authorization")+`"}`)
	}))
	defer srv.Close()

	c := forge.NewGitHub(srv.URL, srv.Client())
	_, err := c.CreatePullRequest(context.Background(), forge.Repo{Slug: "a/b"}, forge.Auth{Token: "s3cret"}, forge.PullRequest{})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("the error echoed the token: %q", err)
	}
}

func TestGitHub_FindPullRequestsByHeadIsTheIdempotencyProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("head"); got != "acme:caesar/abc" {
			t.Errorf("head filter = %q; GitHub needs owner-qualified", got)
		}
		if got := r.URL.Query().Get("state"); got != "all" {
			t.Errorf("state = %q; a merged or closed PR is still OUR receipt", got)
		}
		_, _ = io.WriteString(w, `[{"number":42,"html_url":"u","state":"open","head":{"sha":"deadbeef"},"base":{"ref":"main"}}]`)
	}))
	defer srv.Close()

	c := forge.NewGitHub(srv.URL, srv.Client())
	got, err := c.FindPullRequests(context.Background(), forge.Repo{Slug: "acme/widgets"}, forge.Auth{Token: "t"}, "caesar/abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("candidates = %d, want 1: the probe returns EVERY request on the head branch", len(got))
	}
	if got[0].Number != 42 || got[0].HeadSHA != "deadbeef" {
		t.Errorf("receipt = %+v", got[0])
	}
	// Base is what makes a candidate provable downstream: matchPublication
	// treats an empty one as unprovable and halts the publication.
	if got[0].Base != "main" {
		t.Errorf("base = %q, want main", got[0].Base)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/platform/forge/ -v`
Expected: FAIL — the package does not exist.

- [ ] **Step 3: Write the shared package surface**

`backend/internal/platform/forge/forge.go`:

```go
// Package forge is the shared-kernel client for the two code-hosting APIs
// caesar publishes to. It is deliberately narrow: four calls, hand-rolled
// over net/http against pinned API versions, no SDK, no domain types.
//
// Purity is load-bearing (internal/arch CheckPlatformPurity): this package
// imports no bounded context, so it speaks its own Repo/Auth/PullRequest
// vocabulary and app translates. That is the same shape platform/dockerapi
// has, for the same reason.
//
// Nothing here retries. Retry is a run-level decision with a budget and a
// deadline behind it, and burying it in a client would spend both invisibly.
package forge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Sentinels. Callers branch on these, never on status codes.
var (
	// ErrNotFound is a resource the forge says does not exist. Callers that
	// treat absence as an answer use the (value, ok, error) shape instead.
	ErrNotFound = errors.New("forge: not found")
	// ErrConflict is the forge refusing because the effect already exists or
	// contradicts the current state. Never retried — the publish machine's
	// recovery probe decides what it means.
	ErrConflict = errors.New("forge: conflict")
	// ErrUnauthorized is a credential the forge will not accept.
	ErrUnauthorized = errors.New("forge: unauthorized")
)

// retryable marks a transport or 5xx/429 failure. A caller asks with
// IsRetryable rather than matching a type, so the classification can grow
// without changing call sites. `at` carries a reset the forge STATED, when it
// stated one.
type retryable struct {
	err error
	at  time.Time
}

func (r retryable) Error() string { return r.err.Error() }
func (r retryable) Unwrap() error { return r.err }

// IsRetryable reports whether another attempt could plausibly succeed.
func IsRetryable(err error) bool {
	var r retryable
	return errors.As(err, &r)
}

// RetryAfter returns the instant the forge said to come back, if it said one.
// The run layer prefers it over its own backoff curve: a provider's stated
// reset is truth, and waking before it retries into a window we were told is
// shut (the same rule retryDelay already applies to provider resets).
func RetryAfter(err error) (time.Time, bool) {
	var r retryable
	if errors.As(err, &r) && !r.at.IsZero() {
		return r.at, true
	}
	return time.Time{}, false
}

// rateLimited decides whether a response is throttling rather than refusal,
// and returns the reset instant when the forge supplied one.
//
// It exists because status code alone cannot answer the question on GitHub: a
// primary rate limit answers 403 or 429 with X-RateLimit-Remaining: 0 and an
// epoch-seconds X-RateLimit-Reset, and a secondary limit answers 403 or 429
// with Retry-After (delta-seconds) and a message naming the secondary limit.
// A 403 with none of those signals is a real permission failure and stays
// permanent — which is what keeps "the token cannot do this" from retrying
// until the wall deadline.
//
// GitLab is simpler and handled by the same helper: 429 with RateLimit-Reset
// (epoch seconds) or Retry-After.
func rateLimited(resp *http.Response, body []byte, now time.Time) (time.Time, bool) {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return time.Time{}, false
	}
	if s := resp.Header.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return now.Add(time.Duration(secs) * time.Second), true
		}
	}
	reset := resp.Header.Get("X-RateLimit-Reset")
	if reset == "" {
		reset = resp.Header.Get("RateLimit-Reset")
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" && reset != "" {
		if epoch, err := strconv.ParseInt(reset, 10, 64); err == nil {
			return time.Unix(epoch, 0).UTC(), true
		}
	}
	// 429 is throttling whether or not it explained itself; a bare 403 is not.
	if resp.StatusCode == http.StatusTooManyRequests {
		return time.Time{}, true
	}
	if bytes.Contains(bytes.ToLower(body), []byte("secondary rate limit")) {
		return time.Time{}, true
	}
	return time.Time{}, false
}

// Repo is a repository as the forge addresses it. Slug is "owner/name" for
// GitHub and the (possibly nested) project path for GitLab.
type Repo struct {
	Slug          string
	DefaultBranch string
}

// Auth carries the forge token for exactly one call. It is never stored on a
// client: the token is opened per use through the audited credential path,
// and a client that held one would outlive that audit.
type Auth struct{ Token string }

type PullRequest struct {
	Head  string
	Base  string
	Title string
	Body  string
}

// PullRequestRef is the receipt a publication records. State is the forge's
// own word ("open", "closed", "merged"); the publish machine records it and
// never interprets it beyond existence.
type PullRequestRef struct {
	Number  int64
	URL     string
	HeadSHA string
	// Base is the branch this pull request merges INTO. It is carried because
	// recovery locates a pull request by HEAD branch alone, and a head branch
	// is not a unique description of a publication: a human or a bot can open
	// a second request from our marker to a different target. Without the
	// base, recovery would adopt it and report a publication that merges
	// somewhere nobody approved.
	Base    string
	State   string
}

// Client is the whole surface. Implemented by GitHub and GitLab.
type Client interface {
	// Head resolves the repository's default branch to a commit sha.
	Head(ctx context.Context, repo Repo, auth Auth) (string, error)
	// BranchHead resolves one branch; ok is false when it does not exist.
	BranchHead(ctx context.Context, repo Repo, auth Auth, branch string) (string, bool, error)
	// CreatePullRequest opens a PR/MR from pr.Head into pr.Base.
	CreatePullRequest(ctx context.Context, repo Repo, auth Auth, pr PullRequest) (PullRequestRef, error)
	// FindPullRequests returns every open-or-closed pull/merge request whose
	// head is this branch, paginated to exhaustion. It is the recovery probe
	// that turns "the receipt was lost" into "it already happened", and it
	// hands back all the candidates rather than picking one because the
	// caller decides ownership: a head branch alone does not identify a
	// publication. The same marker can carry several closed requests and
	// requests to different bases, and only the caller holds the intent to
	// compare them against.
	FindPullRequests(ctx context.Context, repo Repo, auth Auth, headBranch string) ([]PullRequestRef, error)
}

// defaultTimeout bounds one forge call. The publish machine also carries the
// run deadline on its context; this is the floor so a hung connection cannot
// hold a worker for the whole wall clock.
const defaultTimeout = 30 * time.Second

// classify maps a response onto the sentinels. The body is read for its
// message but never echoed verbatim past a bounded prefix, and the request's
// own headers are never included — an error here lands in a run event and in
// publish_intents.halt_reason, both of which are durable.
func classify(op string, resp *http.Response, body []byte) error {
	msg := forgeMessage(body)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s: %w", op, ErrNotFound)
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("%s: %w", op, ErrUnauthorized)
	case resp.StatusCode == http.StatusUnprocessableEntity, resp.StatusCode == http.StatusConflict:
		return fmt.Errorf("%s: %w: %s", op, ErrConflict, msg)
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		return retryable{fmt.Errorf("%s: forge returned %d: %s", op, resp.StatusCode, msg)}
	case resp.StatusCode >= 400:
		return fmt.Errorf("%s: forge returned %d: %s", op, resp.StatusCode, msg)
	}
	return nil
}
```

`forgeMessage` decodes `{"message": …}` (GitHub) or `{"message": …}` / `{"error": …}` (GitLab), truncates to 200 bytes, and falls back to a fixed string when the body is not JSON. It never returns request headers. `maxBodyBytes = 1 << 20` caps every read via `io.LimitReader`.

A shared `do` helper builds the request, applies `defaultTimeout` when the caller's context has a later deadline, sets `User-Agent: caesar`, reads the capped body, calls `classify`, and decodes into `out`. Transport errors are wrapped in `retryable` — a dial failure is exactly the kind that another attempt may fix.

- [ ] **Step 4: Write the GitHub client**

`github.go`. Endpoints (REST, `2022-11-28`):

| call | request |
|---|---|
| `Head` | `GET {base}/repos/{slug}/commits/{defaultBranch}` → `.sha` |
| `BranchHead` | `GET {base}/repos/{slug}/git/ref/heads/{branch}` → `.object.sha`; 404 ⇒ `ok=false` |
| `CreatePullRequest` | `POST {base}/repos/{slug}/pulls` `{head, base, title, body}` → `.number`, `.html_url`, `.state`, `.head.sha`, `.base.ref` → `Base` |
| `FindPullRequests` | `GET {base}/repos/{slug}/pulls?head={owner}:{branch}&state=all` → **every** element, the same fields including `.base.ref` → `Base`, paginated to exhaustion |

`Base` is mapped by both calls on both forges, and that is a correctness requirement rather than completeness for its own sake. `matchPublication` (Task 9) treats a candidate whose `Base` is empty as **unprovable** — absent evidence is not permission — so a client that leaves the field zero makes every recovered candidate ambiguous and halts every publication it touches.

```go
// GitHub speaks the REST API at a pinned version. baseURL is
// "https://api.github.com" in production and an httptest URL in tests;
// self-hosted GitHub Enterprise is the same shape with a different base,
// which is why it is a field rather than a constant.
type GitHub struct {
	baseURL string
	hc      *http.Client
}

func NewGitHub(baseURL string, hc *http.Client) *GitHub {
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	return &GitHub{baseURL: strings.TrimRight(baseURL, "/"), hc: hc}
}

func (g *GitHub) headers(auth Auth) http.Header {
	h := http.Header{}
	h.Set("Accept", "application/vnd.github+json")
	// Pinning the version is what stops a silent response-shape change from
	// becoming a silent publish failure.
	h.Set("X-GitHub-Api-Version", "2022-11-28")
	h.Set("Authorization", "Bearer "+auth.Token)
	return h
}
```

`BranchHead` uses `url.PathEscape(branch)` on the whole branch (so `caesar/abc` becomes `caesar%2Fabc`) because GitHub's `git/ref/heads/{ref}` treats the remainder as one segment. `FindPullRequests` splits `slug` on `/` to build the owner-qualified `head` filter and refuses a slug without exactly one `/`.

- [ ] **Step 5: Write the GitLab client**

`gitlab.go`. Endpoints (REST v4, `PRIVATE-TOKEN` header):

| call | request |
|---|---|
| `Head` | `GET {base}/api/v4/projects/{urlencoded slug}/repository/branches/{branch}` → `.commit.id` |
| `BranchHead` | same; 404 ⇒ `ok=false` |
| `CreatePullRequest` | `POST {base}/api/v4/projects/{slug}/merge_requests` `{source_branch, target_branch, title, description}` → `.iid`, `.web_url`, `.state`, `.sha`, `.target_branch` → `Base` |
| `FindPullRequests` | `GET {base}/api/v4/projects/{slug}/merge_requests?source_branch={b}&state=all` → **every** element, the same fields including `.target_branch` → `Base`, paginated to exhaustion |

Two GitLab-specific facts to pin in comments:
- the project id in the path is the **URL-encoded full path** (`url.PathEscape("group/sub/proj")` → `group%2Fsub%2Fproj`), which is why `Repo.Slug` may nest for GitLab and not for GitHub;
- the merge-request number in a URL is `iid` (project-scoped), not `id` (instance-scoped). Recording `id` would produce a link to somebody else's MR. The test asserts this explicitly:

```go
func TestGitLab_RecordsTheProjectScopedIID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":99999,"iid":7,"web_url":"https://gitlab.com/g/p/-/merge_requests/7","state":"opened","sha":"cafe"}`)
	}))
	defer srv.Close()

	c := forge.NewGitLab(srv.URL, srv.Client())
	got, err := c.CreatePullRequest(context.Background(), forge.Repo{Slug: "g/p"}, forge.Auth{Token: "t"},
		forge.PullRequest{Head: "caesar/abc", Base: "main", Title: "t", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 7 {
		t.Fatalf("Number = %d, want the project-scoped iid 7 (id 99999 links to a different project's MR)", got.Number)
	}
}
```

- [ ] **Step 6: Run the forge tests**

Run: `go test ./internal/platform/forge/ -race -v`
Expected: PASS.

- [ ] **Step 7: Prove platform purity still holds**

Run: `go test ./internal/arch/ -race`
Expected: PASS — `CheckPlatformPurity` must not report `platform/forge`.

- [ ] **Step 8: Commit**

```bash
git add backend/internal/platform/forge/
git commit -m "feat(platform): add hand-rolled github and gitlab rest clients for publishing"
```

---

### Task 8: `gitops.Publish` — the fresh trusted checkout and the precondition push

The git half of a publication: clone the remote at the pinned base SHA, branch, apply the canonical patch, commit with the pinned attribution, push with an expected-remote-SHA precondition.

**Files:**
- Create: `backend/internal/runs/runner/gitops/publish.go`
- Modify: `backend/internal/runs/runner/gitops/gitops.go` (package doc — say why publish lives here)
- Test: `backend/internal/runs/runner/gitops/publish_test.go` (real git binary, local bare repos, no network)

**Interfaces:**
- Consumes: `baseEnv`, `runGit`, `redact`, `typed`, `checkCloneURL`, `assertNoCredentialConfig` (all already in the package).
- Produces:
  - `gitops.PublishInput{CloneURL, BaseSHA, Branch, TreeHash, Patch, Message, AuthorName, AuthorEmail, ExpectHead string; Date time.Time}`
  - `gitops.PublishReport{HeadSHA, TreeHash string; Created bool}`
  - `func Publish(ctx context.Context, dir string, in PublishInput, authEnv []string) (PublishReport, error)`
  - `func ExpectedPublicationHead(ctx context.Context, dir string, in PublishInput, authEnv []string) (string, error)` — the same rebuild without the push, for expired-run settlement

- [ ] **Step 1: Write the failing publish tests**

In `backend/internal/runs/runner/gitops/publish_test.go`:

**Convention for this file.** `Publish` refuses a zero `Date` (it is what makes the commit reproducible), so every `PublishInput` literal below sets one. Unless a test says otherwise it uses the shared fixed instant:

```go
// publishTestDate pins the commit timestamps so a rebuilt publication hashes
// identically. Tests that care about reproducibility rely on this; tests that
// do not still need it, because an unpinned date is a refusal.
var publishTestDate = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
```

```go
package gitops

func TestPublish_CreatesTheBranchAndCommitsWithTheHumanAsAuthor(t *testing.T) {
	remote, base := initRemote(t) // bare repo + one seed commit
	dir := t.TempDir()

	rep, err := Publish(context.Background(), dir, PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: "caesar/" + uuid.NewString(),
		Patch:       addFilePatch("NEW.md", "hello\n"),
		Message:     "Add NEW.md\n\ncaesar-run: r1\n",
		AuthorName:  "alice", AuthorEmail: "alice@example.com",
		Date:        publishTestDate,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Created {
		t.Error("a branch that did not exist must be reported as created")
	}
	if rep.HeadSHA == "" || rep.HeadSHA == base {
		t.Fatalf("head = %q; want a new commit on top of %q", rep.HeadSHA, base)
	}

	// The remote really has it, and the attribution is the pinned split.
	author := gitOut(t, remote, "log", "-1", "--format=%an <%ae>", rep.HeadSHA)
	if author != "alice <alice@example.com>" {
		t.Errorf("author = %q; the requester is the AUTHOR (spec §6 requester principal)", author)
	}
	committer := gitOut(t, remote, "log", "-1", "--format=%cn <%ce>", rep.HeadSHA)
	if committer != "caesar <caesar@invalid>" {
		t.Errorf("committer = %q; caesar is the COMMITTER", committer)
	}
	if body := gitOut(t, remote, "log", "-1", "--format=%B", rep.HeadSHA); !strings.Contains(body, "caesar-run: r1") {
		t.Errorf("the provenance trailers must survive: %q", body)
	}
	if strings.Contains(gitOut(t, remote, "log", "-1", "--format=%B", rep.HeadSHA), "Co-authored-by") {
		t.Error("no machine co-author trailer is emitted (baked decision 7)")
	}
}

func TestPublish_IsIdempotentWhenTheBranchIsAlreadyOurs(t *testing.T) {
	// The crash-after-push case: the branch exists at exactly the head we
	// would have created. Publishing again must adopt it, not fail and not
	// create a second commit.
	//
	// The two calls are deliberately separated by a wall-clock second. If the
	// commit date were not pinned, the rebuilt commit would hash differently,
	// the adopt comparison would miss, and the second call would
	// force-with-lease a replacement over the first commit — the exact
	// duplication this whole state machine exists to prevent. Anything that
	// makes this test pass only when it runs fast is not idempotency.
	remote, base := initRemote(t)
	branch := "caesar/" + uuid.NewString()
	in := PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: branch,
		Patch:      addFilePatch("NEW.md", "hello\n"),
		Message:    "Add NEW.md\n",
		AuthorName: "alice", AuthorEmail: "alice@example.com",
		Date:       time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
	}
	first, err := Publish(context.Background(), t.TempDir(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)

	in.ExpectHead = first.HeadSHA
	second, err := Publish(context.Background(), t.TempDir(), in, nil)
	if err != nil {
		t.Fatalf("re-publishing an identical, already-pushed branch must adopt it: %v", err)
	}
	if second.HeadSHA != first.HeadSHA {
		t.Fatalf("head moved from %s to %s; the second publish created a new commit", first.HeadSHA, second.HeadSHA)
	}
	if second.Created {
		t.Error("an adopted branch was not created by this call")
	}
	if got := gitOut(t, remote, "rev-list", "--count", branch); got != "2" {
		t.Errorf("branch has %s commits, want 2 (seed + one publication)", got)
	}
}

func TestPublish_RefusesWhenTheBranchExistsAtSomethingElse(t *testing.T) {
	remote, base := initRemote(t)
	branch := "caesar/" + uuid.NewString()
	// Somebody else put a commit on our marker.
	pushForeignCommit(t, remote, branch, base)

	_, err := Publish(context.Background(), t.TempDir(), PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: branch,
		Patch: addFilePatch("NEW.md", "x\n"), Message: "m",
		AuthorName: "alice", AuthorEmail: "alice@example.com", Date: publishTestDate,
	}, nil)
	var te TypedError
	if !errors.As(err, &te) {
		t.Fatalf("want a typed outcome, got %v", err)
	}
	if te.Outcome.Code != runs.CodePublishConflict || te.Outcome.Class != runs.OutcomePermanent {
		t.Fatalf("outcome = %+v; want a PERMANENT publish_conflict — a marker we cannot prove is ours is a human's problem", te.Outcome)
	}
}

func TestPublish_AForeignMarkerIsNeverOverwrittenEvenWhenItLooksAdoptable(t *testing.T) {
	// The sharpest version of the ownership question. A foreign commit sits
	// on our marker, and the caller — a recovery path that observed that same
	// sha — passes it as ExpectHead. Every signal available short of rebuilding
	// the commit says "this is the branch you pushed last time". It is not.
	//
	// An implementation that treats an observed head as a lease value will
	// force-push over this commit and pass every other test in this file.
	remote, base := initRemote(t)
	branch := "caesar/" + uuid.NewString()
	foreign := pushForeignCommit(t, remote, branch, base)

	_, err := Publish(context.Background(), t.TempDir(), PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: branch,
		Patch: addFilePatch("NEW.md", "hello\n"), Message: "m",
		AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
		ExpectHead: foreign, // the caller genuinely saw this
	}, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodePublishConflict {
		t.Fatalf("err = %v; want publish_conflict: an observed sha is evidence, not permission", err)
	}
	if got := gitOut(t, remote, "rev-parse", branch); got != foreign {
		t.Fatalf("the foreign commit was replaced: branch is at %s, was %s", got, foreign)
	}
}

func TestPublish_ABranchCreatedBetweenProbeAndPushIsRefused(t *testing.T) {
	// The create path's race. Publish probes, finds nothing, builds its
	// commit — and in that window somebody creates the branch AT THE BASE.
	// A plain push would fast-forward it and report success, silently taking
	// over a ref we never proved was ours; only an atomic create-only lease
	// refuses.
	remote, base := initRemote(t)
	branch := "caesar/" + uuid.NewString()

	in := PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: branch,
		Patch: addFilePatch("NEW.md", "hello\n"), Message: "m",
		AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}
	// afterPrepare runs once the commit is built and before the push — the
	// same runGitFn seam the credential test uses.
	createBranchAfterPrepare(t, remote, branch, base)

	_, err := Publish(context.Background(), t.TempDir(), in, nil)
	if err == nil {
		t.Fatal("a branch that appeared under this publication must not be pushed onto")
	}
	// The ref is untouched: still the interloper's, still at base.
	if got := gitOut(t, remote, "rev-parse", branch); got != base {
		t.Errorf("branch moved to %s; the push fast-forwarded a ref it did not own", got)
	}
}

func TestPublish_ExpectedPublicationHeadRebuildsWithoutTouchingTheRemote(t *testing.T) {
	// Expired-run settlement needs the sha a publication WOULD push while
	// holding no authority to push it. Same answer, no ref written.
	remote, base := initRemote(t)
	branch := "caesar/" + uuid.NewString()
	in := PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: branch,
		Patch: addFilePatch("NEW.md", "hello\n"), Message: "m",
		AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}

	want, err := ExpectedPublicationHead(context.Background(), t.TempDir(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, remote, "branch", "--list", "caesar/*"); got != "" {
		t.Fatalf("reconstructing a publication must write nothing; remote has %q", got)
	}

	got, err := Publish(context.Background(), t.TempDir(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.HeadSHA != want {
		t.Fatalf("reconstruction said %s but publishing produced %s; settlement would misjudge every receiptless branch", want, got.HeadSHA)
	}
}

func TestPublish_ExpectHeadMismatchIsAConflictNotAForce(t *testing.T) {
	remote, base := initRemote(t)
	branch := "caesar/" + uuid.NewString()
	in := PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: branch,
		Patch: addFilePatch("NEW.md", "hello\n"), Message: "m",
		AuthorName: "alice", AuthorEmail: "alice@example.com", Date: publishTestDate,
	}
	first, _ := Publish(context.Background(), t.TempDir(), in, nil)
	pushForeignCommit(t, remote, branch, first.HeadSHA) // the branch moved under us

	in.ExpectHead = first.HeadSHA
	_, err := Publish(context.Background(), t.TempDir(), in, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodePublishConflict {
		t.Fatalf("err = %v; want publish_conflict — never a force push", err)
	}
	// And the foreign commit is still there.
	if gitOut(t, remote, "rev-parse", branch) == first.HeadSHA {
		t.Fatal("the publish overwrote somebody else's commit")
	}
}

func TestPublish_RefusesAPatchThatDoesNotApply(t *testing.T) {
	remote, base := initRemote(t)
	_, err := Publish(context.Background(), t.TempDir(), PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: "caesar/" + uuid.NewString(),
		Patch: addFilePatch("README.md", "conflicting\n"), // README.md already exists
		Message: "m", AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}, nil)
	var te TypedError
	if !errors.As(err, &te) {
		t.Fatalf("want a typed outcome, got %v", err)
	}
	if te.Outcome.Class != runs.OutcomePermanent || te.Outcome.Code != runs.CodeInvalidOutput {
		t.Fatalf("outcome = %+v; a patch that does not apply is permanently invalid", te.Outcome)
	}
	if strings.Contains(te.Outcome.Message, "conflicting") {
		t.Error("git's message quotes hunks from a patch we did not author; it must not be echoed")
	}
}

func TestPublish_VerifiesTheTreeHashItWasPromised(t *testing.T) {
	// The authorization pinned a tree hash. If applying the approved patch
	// produces a different tree, the artifact and the authorization have
	// diverged and NOTHING may be pushed.
	remote, base := initRemote(t)
	_, err := Publish(context.Background(), t.TempDir(), PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: "caesar/" + uuid.NewString(),
		Patch: addFilePatch("NEW.md", "hello\n"), TreeHash: "0000000000000000000000000000000000000000",
		Message: "m", AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodePublishConflict {
		t.Fatalf("err = %v; want publish_conflict on a tree-hash mismatch", err)
	}
	if got := gitOut(t, remote, "branch", "--list", "caesar/*"); got != "" {
		t.Fatalf("a mismatch must push nothing; remote has %q", got)
	}
}

func TestPublish_LeavesNoCredentialBehind(t *testing.T) {
	remote, base := initRemote(t)
	authEnv := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic c3VwZXJzZWNyZXQ=",
	}
	dir := t.TempDir()
	if _, err := Publish(context.Background(), dir, PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: "caesar/" + uuid.NewString(),
		Patch: addFilePatch("NEW.md", "hello\n"), Message: "m",
		AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}, authEnv); err != nil {
		t.Fatal(err)
	}
	if found := grepTree(t, dir, "c3VwZXJzZWNyZXQ="); found != "" {
		t.Fatalf("the credential persisted at %s", found)
	}
}

func TestPublish_EveryRemoteCommandCarriesTheCredential(t *testing.T) {
	// Publish reaches the network three times — clone, the ls-remote
	// precondition probe, and push. A local bare repo authenticates none of
	// them, so a probe that silently ran without the credential would pass
	// every other test in this file and then fail on the first private repo
	// as an unexplained forge_unavailable. Assert the wiring directly.
	remote, base := initRemote(t)
	seen := recordGitInvocations(t) // test hook: records argv + whether authEnv was present

	if _, err := Publish(context.Background(), t.TempDir(), PublishInput{
		CloneURL: remote, BaseSHA: base, Branch: "caesar/" + uuid.NewString(),
		Patch: addFilePatch("NEW.md", "hello\n"), Message: "m",
		AuthorName: "a", AuthorEmail: "a@x", Date: publishTestDate,
	}, []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic c3VwZXJzZWNyZXQ="}); err != nil {
		t.Fatal(err)
	}

	for _, cmd := range []string{"clone", "ls-remote", "push"} {
		if !seen.ranWithAuth(cmd) {
			t.Errorf("%s talks to the remote and must receive the credential", cmd)
		}
	}
	// And the converse: nothing else does.
	for _, cmd := range []string{"apply", "commit", "write-tree", "checkout", "cat-file", "rev-parse"} {
		if seen.ranWithAuth(cmd) {
			t.Errorf("%s never reaches the network and must not see the credential", cmd)
		}
	}
}
```

`recordGitInvocations` needs a seam the package does not have yet: `runGit` is a plain function (`gitops.go:71`) with no indirection. Introduce the narrowest one that works — a package-level `var runGitFn = runGit` that every call site in `publish.go` goes through, swapped and restored by the test via `t.Cleanup`. Do not widen this to `stage.go` or `extract.go`; they are not under test here, and a shared mutable hook across three files is a race waiting to happen under `-race` with `t.Parallel`.

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/runs/runner/gitops/ -run TestPublish -v`
Expected: FAIL — `undefined: Publish`.

- [ ] **Step 3: Write `Publish`**

```go
// The publish checkout's layout inside the working directory.
const (
	publishRepoDir  = "publish"
	publishPatchFile = "publish.patch"
)

// PublishInput is one publication. Patch is the base64 wire form the patch
// artifact carries (Plan 07 decision: json.Marshal silently corrupts
// non-UTF-8 patch bytes that still apply cleanly).
type PublishInput struct {
	CloneURL string
	BaseSHA  string
	Branch   string
	// TreeHash is what the approval pinned. Applying the approved patch must
	// reproduce exactly this tree or nothing is pushed.
	TreeHash string
	Patch    string
	Message  string
	// The requester is the AUTHOR; caesar is the committer (baked decision 7).
	AuthorName, AuthorEmail string
	// Date is the timestamp pinned into BOTH the author and committer
	// identities, and it is what makes this function reproducible.
	//
	// A git commit's SHA is a hash of tree, parent, message and the two
	// identities INCLUDING their timestamps. Letting git default them to the
	// wall clock means rebuilding the same publication one second later
	// produces a different SHA — so the adopt comparison below could never
	// match, and the crash-after-push retry would force-with-lease a
	// replacement commit over the one already on the remote. Pinning the date
	// to a durable value (the publish intent's created_at) makes the SHA a
	// pure function of the inputs, which is what "recovery adopts" requires.
	Date time.Time
	// ExpectHead is the remote precondition. "" means the branch must not
	// exist; a value means it must be exactly that commit. There is no third
	// mode, and there is never a force.
	ExpectHead string
}

type PublishReport struct {
	HeadSHA  string
	TreeHash string
	// Created is false when the branch was already at the head this call
	// would have produced — the crash-after-push case, adopted idempotently.
	Created bool
}

// Publish materializes a fresh trusted checkout of the remote at BaseSHA,
// applies the approved patch, commits with the pinned attribution and pushes
// Branch under an expected-remote-SHA precondition.
//
// Nothing the agent controlled is read here. The clone is the REMOTE's own
// content at a pinned commit — no agent-written .git, no hooks, no
// diff.external — and the patch is the canonical diff extraction already
// produced and validated. Repo code is never executed. That is why this runs
// in the control-plane process where staging and extraction do not (baked
// decision 8).
//
// Structurally it is `publishPrepare` — clone, verify, apply, commit, all of
// which cause nothing outside `dir` — followed by the remote decision and at
// most one push. Splitting there is not tidiness: it is what lets expired-run
// settlement ask "what would this have pushed" through the SAME code, so
// "ours" has exactly one definition. Steps 1-6 below live in publishPrepare;
// steps 7-8 are Publish's own.
func Publish(ctx context.Context, dir string, in PublishInput, authEnv []string) (PublishReport, error) {
	credential := credentialFrom(authEnv)
	repo := filepath.Join(dir, publishRepoDir)
	env := baseEnv(dir)

	if err := checkCloneURL(in.CloneURL); err != nil {
		return PublishReport{}, err
	}
	if in.Branch == "" || strings.HasPrefix(in.Branch, "-") || strings.ContainsAny(in.Branch, " \t\r\n") {
		return PublishReport{}, typed(runs.OutcomePermanent, runs.CodeInvalidOutput,
			"publish branch %q is not a usable ref name", in.Branch)
	}

	// 1. A shallow-ish clone is not enough: the push needs the base commit's
	// full ancestry or the remote refuses a disconnected history. Clone the
	// default refs and check out the pin.
	if _, err := runGit(ctx, dir, slices.Concat(env, authEnv), "clone", "-q", in.CloneURL, repo); err != nil {
		return PublishReport{}, typed(runs.OutcomeTransient, runs.CodeForgeUnavailable,
			"clone for publish: %s", redact(err.Error(), credential))
	}
	if _, err := runGit(ctx, repo, env, "cat-file", "-e", in.BaseSHA+"^{commit}"); err != nil {
		return PublishReport{}, typed(runs.OutcomePermanent, runs.CodeBaseSHAUnavailable,
			"base sha %s is no longer in %s", in.BaseSHA, in.CloneURL)
	}

	// 2. Prove nothing persisted the credential into the clone's config
	// before we write anything of our own into it.
	if err := assertNoCredentialConfig(ctx, dir, repo); err != nil {
		return PublishReport{}, err
	}

	// 3. Read what the marker points at. This is EVIDENCE, not permission.
	//
	// An earlier draft turned the observed head into ExpectHead and pushed
	// against it. That is backwards: observing a sha proves only that some
	// commit is there, and using it as a lease value converts "somebody else's
	// branch" into "a branch I am authorized to replace". The rebuilt commit
	// below is the only thing that can prove ownership, so the decision waits
	// until we have it.
	remoteHead, remoteHas, err := remoteBranchHead(ctx, repo, slices.Concat(env, authEnv), in.Branch, credential)
	if err != nil {
		return PublishReport{}, err
	}

	// 4. Build the commit on a detached base, on a branch of our own name.
	if _, err := runGit(ctx, repo, env, "checkout", "-q", "-b", in.Branch, in.BaseSHA); err != nil {
		return PublishReport{}, typed(runs.OutcomeTransient, runs.CodePushRejected,
			"create %s: %s", in.Branch, redact(err.Error(), credential))
	}
	patch, err := decodePublishPatch(in.Patch)
	if err != nil {
		return PublishReport{}, err
	}
	if len(patch) > 0 {
		path := filepath.Join(dir, publishPatchFile)
		if err := os.WriteFile(path, patch, 0o600); err != nil {
			return PublishReport{}, typed(runs.OutcomeTransient, runs.CodeInternalError, "write publish patch: %v", err)
		}
		if _, err := runGit(ctx, repo, env, "apply", "--index", "--whitespace=nowarn", path); err != nil {
			// git quotes hunks and paths out of a patch we did not author;
			// the caller gets the fact, never that text.
			return PublishReport{}, typed(runs.OutcomePermanent, runs.CodeInvalidOutput,
				"the approved patch does not apply to %s", in.BaseSHA)
		}
	}

	// 5. Verify the tree BEFORE committing: a tree that is not what the human
	// approved must never become a commit, let alone a push.
	tree, err := runGit(ctx, repo, env, "write-tree")
	if err != nil {
		return PublishReport{}, typed(runs.OutcomeTransient, runs.CodeInternalError, "write tree: %v", err)
	}
	treeHash := strings.TrimSpace(tree)
	if in.TreeHash != "" && treeHash != in.TreeHash {
		return PublishReport{}, typed(runs.OutcomePermanent, runs.CodePublishConflict,
			"applying the approved patch produced tree %s, not the approved %s", treeHash, in.TreeHash)
	}

	// 6. Commit. The requester authors it; caesar commits it, and BOTH dates
	// are pinned so this call is reproducible — see PublishInput.Date. RFC3339
	// is one of git's accepted date formats and is unambiguous about the zone.
	if in.Date.IsZero() {
		return PublishReport{}, typed(runs.OutcomePermanent, runs.CodeInvalidOutput,
			"publish requires a pinned commit date; an unpinned one makes the commit irreproducible")
	}
	stamp := in.Date.UTC().Format(time.RFC3339)
	commitEnv := slices.Concat(env, []string{
		"GIT_AUTHOR_NAME=" + in.AuthorName,
		"GIT_AUTHOR_EMAIL=" + in.AuthorEmail,
		"GIT_AUTHOR_DATE=" + stamp,
		"GIT_COMMITTER_DATE=" + stamp,
	})
	if _, err := runGit(ctx, repo, commitEnv, "commit", "-q", "--no-verify", "-m", in.Message); err != nil {
		return PublishReport{}, typed(runs.OutcomePermanent, runs.CodeInvalidOutput,
			"commit the approved patch: %s", redact(err.Error(), credential))
	}
	head, err := runGit(ctx, repo, env, "rev-parse", "HEAD")
	if err != nil {
		return PublishReport{}, typed(runs.OutcomeTransient, runs.CodeInternalError, "read publish head: %v", err)
	}
	headSHA := strings.TrimSpace(head)

	// 7. NOW decide, with the rebuilt commit in hand. There are exactly three
	// possibilities and the sha answers all of them:
	//
	//   - the marker carries exactly this commit → a prior attempt pushed and
	//     lost its receipt. Adopt it; push nothing.
	//   - the marker carries something else → we cannot prove it is ours, and
	//     the commit we would push is demonstrably not what is there. Halt.
	//   - the marker is absent → create it.
	//
	// `in.ExpectHead` is not consulted here. It survives on the struct only as
	// the caller's assertion about what it last saw, checked below for a
	// mismatch worth naming; it is never the thing that authorizes a write.
	if remoteHas {
		if remoteHead == headSHA {
			return PublishReport{HeadSHA: headSHA, TreeHash: treeHash, Created: false}, nil
		}
		return PublishReport{}, typed(runs.OutcomePermanent, runs.CodePublishConflict,
			"branch %s is at %s, which is not this publication's commit %s; nothing here can prove it is ours",
			in.Branch, remoteHead, headSHA)
	}
	if in.ExpectHead != "" {
		return PublishReport{}, typed(runs.OutcomePermanent, runs.CodePublishConflict,
			"branch %s was expected at %s but no longer exists", in.Branch, in.ExpectHead)
	}

	// 8. Create the branch under an ATOMIC create-only lease.
	//
	// A plain push is not sufficient and the difference is not theoretical: a
	// branch created at the base sha between the probe above and this push is
	// a fast-forward, so a plain push updates it and reports success —
	// silently adopting a ref we never proved was ours. --force-with-lease
	// with an EMPTY expected value means "only if this ref does not exist",
	// evaluated by the remote at push time, which closes that window.
	//
	// This is the only force this code may use, and it can never overwrite:
	// an empty lease fails against any existing ref.
	if _, err := runGit(ctx, repo, slices.Concat(env, authEnv),
		"push", "--force-with-lease=refs/heads/"+in.Branch+":",
		"origin", "HEAD:refs/heads/"+in.Branch); err != nil {
		// Two very different failures share an exit code: the ref appeared
		// under us (the lease refused, a conflict a human resolves) and the
		// network blinked (worth a retry). We cannot tell them apart from the
		// exit code, so this is transient and bounded by max_attempts — and
		// the next attempt's rebuilt-sha comparison gives the exact answer.
		return PublishReport{}, typed(runs.OutcomeTransient, runs.CodePushRejected,
			"push %s: %s", in.Branch, redact(err.Error(), credential))
	}
	return PublishReport{HeadSHA: headSHA, TreeHash: treeHash, Created: true}, nil
}

// ExpectedPublicationHead reconstructs the commit a publication WOULD push and
// returns its sha, without touching the remote's refs.
//
// This is what makes a receiptless branch decidable. Recovery's question is
// always "is the thing on our marker the thing this authorization produces",
// and the only honest answer is the sha of the rebuilt commit — the pinned
// date (decision 7) is what makes that reproducible. The expired-run
// settlement path needs that answer while holding no authority to cause
// anything, so it needs the rebuild WITHOUT the push.
//
// It shares every step with Publish up to the commit, so the two can never
// disagree about what "ours" means.
func ExpectedPublicationHead(ctx context.Context, dir string, in PublishInput, authEnv []string) (string, error) {
	rep, err := publishPrepare(ctx, dir, in, authEnv)
	if err != nil {
		return "", err
	}
	return rep.HeadSHA, nil
}
```

**The split, concretely.** Steps 1-6 above move verbatim into `publishPrepare`; `Publish`'s body becomes one call to it plus steps 7-8. Nothing in steps 1-6 mutates the remote — clone, `cat-file`, the credential assertion, `ls-remote`, `checkout`, `apply`, `write-tree`, `commit` — which is what makes the function safe for a run that has lost its authority to cause anything.

```go
// preparedPublication is everything Publish knows before it decides whether
// to write: the commit it built, and what the marker currently points at.
type preparedPublication struct {
	// HeadSHA is the commit this publication produces. Reproducible across
	// calls because the date is pinned, which is the whole basis of "is the
	// branch on the remote ours".
	HeadSHA  string
	TreeHash string
	// RemoteHead is evidence about the marker, never permission to write it.
	RemoteHead string
	RemoteHas  bool
	// Dir is the checkout, so a caller that only wanted the sha can drop it.
	Dir string
}

func publishPrepare(ctx context.Context, dir string, in PublishInput, authEnv []string) (preparedPublication, error)
```

```go
// remoteBranchHead asks the remote (not the clone's stale remote-tracking
// refs) what the branch points at right now. ls-remote is the only read that
// is honest about a ref that moved between clone and push.
//
// env here MUST already carry authEnv: ls-remote is a network call to the same
// host clone and push talk to. Against a public remote — or the local bare
// repos this package's tests use — it succeeds without a credential, which is
// exactly why omitting it would pass every test and then fail on the first
// private repo, with prompts disabled, as an unexplained forge_unavailable.
func remoteBranchHead(ctx context.Context, repo string, env []string, branch, credential string) (string, bool, error) {
	out, err := runGit(ctx, repo, env, "ls-remote", "--exit-code", "origin", "refs/heads/"+branch)
	if err != nil {
		// exit 2 is ls-remote's "no matching ref", which is an answer.
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 2 {
			return "", false, nil
		}
		return "", false, typed(runs.OutcomeTransient, runs.CodeForgeUnavailable,
			"read remote ref %s: %s", branch, redact(err.Error(), credential))
	}
	sha, _, _ := strings.Cut(strings.TrimSpace(out), "\t")
	if sha == "" {
		return "", false, nil
	}
	return sha, true, nil
}
```

`decodePublishPatch` mirrors `decodeInputPatch` (same `maxInputPatchBytes` cap, same base64 wire form, same typed refusals) but classifies an undecodable patch as `CodeInvalidOutput` rather than `CodeStagingFailed` — nothing is being staged here.

Note the `authEnv` is passed to exactly the three commands that talk to the remote — `clone`, `ls-remote` and `push` — and never to the rest, matching `Stage`'s discipline: only the commands that must reach the network ever see the credential. `Stage` needs it on one command because it only clones; `Publish` probes and pushes as well, and every one of those is a request the forge authenticates.

- [ ] **Step 4: Update the package doc**

In `gitops.go`:

```go
// Package gitops drives the git binary for the trusted parts of the run
// pipeline. Every command runs with a constructed environment (spec §3): git
// never sees the ambient env, so no user or system config, no credential
// helper and no terminal prompt can reach it.
//
// Two callers live here for one reason. Stage and Extract run INSIDE the
// caesar-git container because they touch a workspace and a .git the agent
// controlled. Publish does not — it reads the remote at a pinned commit and
// applies a canonical patch — and runs in the control-plane process. It lives
// in this package because this is where the git-driving primitives are, not
// because it is a runner concern; the runner/ path is an accident of birth.
package gitops
```

- [ ] **Step 5: Run the publish tests**

Run: `go test ./internal/runs/runner/gitops/ -run TestPublish -race -v`
Expected: PASS. These need the `git` binary, which dev and CI both have (the Plan 05 `fake.InitBareRepo` fixture already depends on it).

- [ ] **Step 6: Run the whole gitops suite**

Run: `go test ./internal/runs/runner/... -race`
Expected: PASS — Stage and Extract untouched.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/runs/runner/gitops/publish.go \
        backend/internal/runs/runner/gitops/publish_test.go \
        backend/internal/runs/runner/gitops/gitops.go
git commit -m "feat(gitops): publish an approved patch from a fresh checkout under a remote precondition"
```

---

### Task 9: The publish effect state machine

Authorize → push → PR, each effect's receipt committed before the next call. This is the task the whole plan exists for.

**Files:**
- Create: `backend/internal/runs/publish.go`
- Modify: `backend/internal/runs/ports.go` (`Forge` growth, `GitPublisher`, `PrincipalSource`)
- Modify: `backend/internal/runs/orchestrator.go` (`execute` :659-720 — dispatch)
- Modify: `backend/internal/app/runsports.go`, `backend/internal/app/serve.go` (the git-publisher and principal-source adapters and the `NewService` call site — this task changes the constructor, so it also has to keep the tree compiling)
- Test: `backend/internal/runs/publish_integration_test.go` (new), `backend/internal/runs/revisions_integration_test.go`

**Interfaces:**
- Produces:
  - `runs.PullRequestSpec{Head, Base, Title, Body string}`, `runs.PullRequestRef{Number int64; URL, HeadSHA, State string}`
  - `Forge` grows `BranchHead`, `OpenPullRequest`, `FindPullRequests`
  - `runs.GitPublisher` with `PushPatch(ctx, PushRequest) (PushResult, error)`
  - `runs.PushRequest{Ref AttemptRef; Repo RepoRef; Branch, BaseSHA, TreeHash, Patch, Message, AuthorName, AuthorEmail, ExpectHead string; Deadline time.Time}`
  - `runs.PushResult{HeadSHA, TreeHash string; Created bool}`
  - `runs.PublicationBranch(artifactID uuid.UUID) string`
  - `func (o *Orchestrator) executePublish(ctx, workerID string, cp cachedPlan, c claimed)`

**Test-harness convention for this task.** `h.intent(t, runID, node)` returns the **store row** (`store.PublishIntent`), so its fields use sqlc's spelling — `HeadSha`, `PrNumber`, `PrUrl`, `HaltReason`, all pointers for the nullable columns. `h.authorization(t, id)` likewise returns `store.GetAuthorizationByIDRow`. The tests below are written against those names; a harness that returned domain structs instead would need them renamed throughout, so pick one and keep it.

- [ ] **Step 1: Grow the ports**

In `ports.go`:

```go
// Forge is the control plane's view of a code host. Plan 05 needed one
// method; the publish state machine needs three more, and every one of them
// is either an idempotency probe or an effect that records a receipt.
type Forge interface {
	// ResolveHead resolves a repo's default branch to the commit a run pins
	// as its base SHA.
	ResolveHead(ctx context.Context, repo RepoRef) (string, error)
	// BranchHead reports what a branch points at; ok is false when it does
	// not exist. Recovery's first question.
	BranchHead(ctx context.Context, repo RepoRef, branch string) (string, bool, error)
	// OpenPullRequest creates the pull/merge request and returns its receipt.
	OpenPullRequest(ctx context.Context, repo RepoRef, spec PullRequestSpec) (PullRequestRef, error)
	// FindPullRequests lists every pull/merge request on a head branch. This
	// is what turns "the receipt was lost" into "it already happened" — but
	// only after the caller proves one of the candidates is the publication
	// the approval authorized. A head branch alone does not prove it.
	FindPullRequests(ctx context.Context, repo RepoRef, headBranch string) ([]PullRequestRef, error)
}

type PullRequestSpec struct{ Head, Base, Title, Body string }

type PullRequestRef struct {
	Number  int64
	URL     string
	HeadSHA string
	// Base is the branch this pull request merges INTO. It is carried because
	// recovery locates a pull request by HEAD branch alone, and a head branch
	// is not a unique description of a publication: a human or a bot can open
	// a second request from our marker to a different target. Without the
	// base, recovery would adopt it and report a publication that merges
	// somewhere nobody approved.
	Base    string
	State   string
}

// GitPublisher performs the git half of a publication: a fresh trusted
// checkout at BaseSHA, the approved patch applied, a commit with the pinned
// attribution, and a push under an expected-remote-SHA precondition.
//
// It is a port rather than a direct call so the golden set can inject a
// crash between the push and its receipt — the one window that makes a
// publication non-idempotent if the machine gets it wrong.
type GitPublisher interface {
	PushPatch(ctx context.Context, req PushRequest) (PushResult, error)
	// ExpectedHead reports the commit sha PushPatch WOULD produce, touching
	// no ref. Task 10's expired-run settlement needs to identify a branch
	// whose push receipt was lost while holding no authority to write
	// anything; the pinned commit date is what makes the answer exact.
	ExpectedHead(ctx context.Context, req PushRequest) (string, error)
}

type PushRequest struct {
	Ref                     AttemptRef
	Repo                    RepoRef
	Branch                  string
	BaseSHA                 string
	TreeHash                string
	Patch                   string // base64(std) canonical diff, the artifact wire form
	Message                 string
	AuthorName, AuthorEmail string
	// Date is the intent's created_at, pinned into both git identities so the
	// commit sha is a pure function of this request. It is durable and stable
	// across attempts, which is what lets a retry after a lost push receipt
	// rebuild the SAME commit and adopt the branch instead of force-pushing a
	// replacement over it. Never the wall clock.
	Date time.Time
	// ExpectHead is "" when the branch must not exist yet.
	ExpectHead string
	Deadline   time.Time
}

type PushResult struct {
	HeadSHA  string
	TreeHash string
	Created  bool
}

// PublicationBranch is the deterministic marker (spec §3): derived from the
// approved artifact id, which is a uuidv7 and therefore unique per node per
// generation. Recovery reconstructs it from the authorization row alone.
func PublicationBranch(artifactID uuid.UUID) string {
	return "caesar/" + artifactID.String()
}
```

`Service` and `NewService` grow a `publisher GitPublisher` field. `NotImplementedGitPublisher` is **not** added: this plan wires a real one everywhere (baked decision 8), and a stub would be dead code the `deadconfig`/`stubfake` analyzers would rightly question.

- [ ] **Step 2: Write the failing publish tests**

In `backend/internal/runs/publish_integration_test.go`:

```go
//go:build integration

package runs_test

func TestPublish_HappyPathPushesAndOpensAPullRequest(t *testing.T) {
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)
	res, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.waitForRunStatus(t, run.ID, runs.RunSucceeded, 40*time.Second)

	intent := h.intent(t, run.ID, "publish")
	auth := h.authorization(t, *res.AuthorizationID)
	if intent.Branch != runs.PublicationBranch(auth.ArtifactID) {
		t.Errorf("branch = %q, want the deterministic marker %q", intent.Branch, runs.PublicationBranch(auth.ArtifactID))
	}
	if intent.Status != "published" {
		t.Fatalf("intent status = %q, want published", intent.Status)
	}
	if intent.HeadSha == nil || intent.PrNumber == nil {
		t.Fatalf("both receipts must be recorded: %+v", intent)
	}
	if auth = h.authorization(t, auth.ID); auth.Status != "consumed" {
		t.Errorf("authorization status = %q, want consumed", auth.Status)
	}

	// The publication artifact carries what a human needs to find it.
	pub := h.artifact(t, run.ID, "publish", runs.ArtifactKindPublication)
	for _, k := range []string{"branch", "head_sha", "pr_number", "pr_url", "target_branch"} {
		if pub.Payload[k] == nil {
			t.Errorf("publication artifact missing %q: %v", k, pub.Payload)
		}
	}
	// And the real remote really has the branch.
	if got := h.remoteBranchHead(t, "publish-marker", intent.Branch); got != *intent.HeadSha {
		t.Errorf("remote head = %q, want the recorded %q", got, *intent.HeadSha)
	}
	// Ordered receipts: intent, then push, then PR.
	h.assertEventOrder(t, run.ID,
		runs.EventPublishIntentCreated, runs.EventPublishPushed, runs.EventPublishPROpened)
}

func TestPublish_WithoutAnAuthorizationIsPermanentlyStale(t *testing.T) {
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)
	res, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.stopOrchestrator(t)
	h.invalidateAuthorization(t, *res.AuthorizationID) // as a revision would
	h.startOrchestrator(t)

	h.waitForRunStatus(t, run.ID, runs.RunFailed, 40*time.Second)
	got := h.run(t, run.ID)
	if got.Outcome == nil || got.Outcome.Code != runs.CodeAuthorizationStale {
		t.Fatalf("outcome = %+v, want authorization_stale", got.Outcome)
	}
	if h.publisher.PushCount() != 0 {
		t.Fatal("a stale authorization must abort BEFORE any forge call (spec §3 Publishing)")
	}
}

func TestPublish_ExpiredRunAbortsBeforeAnyForgeCall(t *testing.T) {
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)
	h.stopOrchestrator(t)
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	h.backdateWallDeadline(t, run.ID)
	h.startOrchestrator(t)

	h.waitForRunStatus(t, run.ID, runs.RunDead, 40*time.Second)
	if h.publisher.PushCount() != 0 || h.forge.OpenPRCount() != 0 {
		t.Fatal("the publish predicate must fail against the DATABASE clock before any effect")
	}
	if _, ok := h.tryIntent(t, run.ID, "publish"); ok {
		t.Fatal("no intent may be created for an expired run")
	}
}

func TestPublish_CrashAfterPushAdoptsTheBranchInsteadOfPushingAgain(t *testing.T) {
	h := newSpineHarness(t)
	h.publisher.CrashAfterPush(true) // the push lands, the process dies before the receipt
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	h.waitForIntentStatus(t, run.ID, "publish", "pending", 30*time.Second)
	pushes := h.publisher.PushCount()
	if pushes != 1 {
		t.Fatalf("pushes = %d, want 1", pushes)
	}

	h.publisher.CrashAfterPush(false)
	h.waitForRunStatus(t, run.ID, runs.RunSucceeded, 40*time.Second)

	// The remote must carry exactly one publication commit, and the intent
	// must have adopted rather than re-pushed.
	intent := h.intent(t, run.ID, "publish")
	if got := h.remoteCommitCount(t, "publish-marker", intent.Branch); got != 2 {
		t.Fatalf("remote has %d commits on the marker, want 2 (seed + one publication)", got)
	}
	if n := h.countEvents(t, run.ID, runs.EventPublishAdopted); n != 1 {
		t.Errorf("publish.adopted events = %d, want 1", n)
	}
}

func TestPublish_CrashAfterPRCreateAdoptsTheExistingPullRequest(t *testing.T) {
	h := newSpineHarness(t)
	h.forge.CrashAfterPR(true)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	h.waitForIntentStatus(t, run.ID, "publish", "pushed", 30*time.Second)

	h.forge.CrashAfterPR(false)
	h.waitForRunStatus(t, run.ID, runs.RunSucceeded, 40*time.Second)

	if got := h.forge.OpenPRCount(); got != 1 {
		t.Fatalf("the forge opened %d pull requests, want exactly 1: a lost receipt must be FOUND, not recreated", got)
	}
	intent := h.intent(t, run.ID, "publish")
	if intent.PrNumber == nil || *intent.PrNumber == 0 {
		t.Fatal("the adopted PR's number must be recorded")
	}
}

func TestPublish_ForeignBranchOnOurMarkerHaltsForAHuman(t *testing.T) {
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)
	h.stopOrchestrator(t)
	res, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	})
	if err != nil {
		t.Fatal(err)
	}
	auth := h.authorization(t, *res.AuthorizationID)
	h.pushForeignBranch(t, "publish-marker", runs.PublicationBranch(auth.ArtifactID))
	h.startOrchestrator(t)

	h.waitForRunStatus(t, run.ID, runs.RunFailed, 40*time.Second)
	got := h.run(t, run.ID)
	if got.Outcome == nil || got.Outcome.Code != runs.CodePublishConflict {
		t.Fatalf("outcome = %+v, want publish_conflict", got.Outcome)
	}
	intent := h.intent(t, run.ID, "publish")
	if intent.Status != "halted" || intent.HaltReason == nil || *intent.HaltReason == "" {
		t.Fatalf("the intent must halt with a reason a human can act on: %+v", intent)
	}
	if h.forge.OpenPRCount() != 0 {
		t.Error("a halted publication opens nothing")
	}
	// And it stays halted: this is not a retry class.
	time.Sleep(3 * h.pollInterval)
	if h.intent(t, run.ID, "publish").Status != "halted" {
		t.Error("a halted intent must never be resumed automatically")
	}
}

func TestPublish_TransientForgeFailureRetriesWithinMaxAttempts(t *testing.T) {
	h := newSpineHarness(t)
	h.forge.FailPRTimes(2, runs.CodeForgeUnavailable)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	h.waitForRunStatus(t, run.ID, runs.RunSucceeded, 60*time.Second)

	// One branch, one PR, three attempts.
	if got := h.publisher.PushCount(); got != 1 {
		t.Errorf("pushes = %d, want 1: a retried publication resumes, it does not re-push", got)
	}
	if got := h.forge.OpenPRCount(); got != 1 {
		t.Errorf("PRs opened = %d, want 1", got)
	}
	if got := h.phases(t, run.ID).countRows("publish"); got != 3 {
		t.Errorf("publish attempt rows = %d, want 3", got)
	}
	if got := h.countIntents(t, run.ID); got != 1 {
		t.Errorf("intents = %d, want 1: one per (run, node), resumed across attempts", got)
	}
}

func TestPublish_ALostCreatePRRaceAdoptsTheWinnerRatherThanHalting(t *testing.T) {
	// Two drivers, one intent — the state a lease takeover creates and cannot
	// undo, because it can fence a worker's database writes but not a request
	// already on the wire.
	//
	// The interleaving that matters: BOTH find no pull request, A creates one,
	// and A is then held BEFORE its receipt commits. B's create gets the
	// forge's "already exists". The intent is still 'pushed' at that moment,
	// so a maybeHalt here would win its CAS and halt a publication that
	// succeeded — telling a human to reconcile a pull request that is open,
	// correct, and exactly what was approved.
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	// The forge accepts the first create and answers ErrConflict to every
	// later one, holding the winner's receipt until we release it.
	release := h.forge.HoldReceiptAfterFirstPullRequest(t)
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	h.waitForIntentStatus(t, run.ID, "publish", "pushed", 30*time.Second)
	// Take the lease away and let a second driver run the same intent.
	h.expireLease(t, run.ID, "publish")
	h.waitForForgePRConflict(t, 30*time.Second)

	if got := h.intent(t, run.ID, "publish"); got.Status == "halted" {
		t.Fatalf("the losing driver halted a successful publication: %+v", got)
	}
	release()
	h.waitForRunStatus(t, run.ID, runs.RunSucceeded, 60*time.Second)

	intent := h.intent(t, run.ID, "publish")
	if intent.Status != "published" || intent.PrNumber == nil {
		t.Fatalf("both drivers must converge on the one real publication: %+v", intent)
	}
	if got := h.forge.PullRequestCount(intent.Branch); got != 1 {
		t.Errorf("pull requests on %s = %d, want 1: the loser must adopt, never open a second",
			intent.Branch, got)
	}
}
```

Four harness additions this test needs, none of which exist yet:

- `fake.Forge.HoldReceiptAfterFirstPullRequest(t)` — accept one create, answer
  `ErrConflict` to the rest, and block the winner between the forge's success
  and its receipt commit. That gap IS the bug's window; a fake that cannot
  hold it open cannot reproduce the race.
- `fake.Forge.PullRequestCount(branch)` — how many requests actually exist on
  the marker. The count is the assertion that matters: "adopted" and "opened a
  duplicate" both end with a published intent and only this tells them apart.
- `h.waitForForgePRConflict(t, d)` — wait until a create has been refused, so
  the test observes the intent at the moment the loser is deciding what to do.
- `h.expireLease(t, runID, nodeID)` — backdate `phase_runs.leased_until` so
  the lease sweep hands the intent to a second driver while the first is still
  inside its call.

The next test is a **unit** test and belongs in `publish_test.go` (package
`runs`, no build tag), not in the integration file above: it exercises three
functions directly and needs no database.

```go

func TestPublish_TheUnfinishedPublicationInvariantHolds(t *testing.T) {
	// A plain unit test, deliberately: the invariant is a property of three
	// small functions, and pinning it here means it cannot be broken without
	// a red test — including by a later task that adds a fourth exit path.
	//
	// The invariant: while an intent is 'pending' or 'pushed', every error
	// leaving runPublish is TRANSIENT and UNCHARGED. If it is ever permanent,
	// evaluate terminalizes the run; terminalization supersedes every phase,
	// no sweep revisits a failed run, and a branch already on the forge
	// becomes an orphan nobody looks for.
	intent := store.PublishIntent{Branch: "caesar/abc", Status: "pushed"}

	t.Run("a receipt transaction that fails after its effect", func(t *testing.T) {
		// The raw pgx error is the dangerous one: default-deny would make it
		// a permanent internal_error.
		out := ClassifyOutcome(receiptFailure(intent, "push", errors.New("connection reset")))
		if out.Class != OutcomeTransient || out.Chargeable() {
			t.Fatalf("outcome = %+v; want a transient, uncharged failure", out)
		}
	})

	t.Run("a lost receipt CAS still reloads", func(t *testing.T) {
		// errIntentAdvanced is not a failure and must pass through
		// untouched, or the state machine loses its adopt path.
		if err := receiptFailure(intent, "push", errIntentAdvanced); !errors.Is(err, errIntentAdvanced) {
			t.Fatalf("err = %v; want errIntentAdvanced unwrapped", err)
		}
	})

	t.Run("a chargeable transient stops being chargeable", func(t *testing.T) {
		for _, code := range []string{CodeForgeUnavailable, CodePushRejected} {
			in := OutcomeError{Outcome: Outcome{Class: OutcomeTransient, Code: code}}
			out := ClassifyOutcome(unchargedWhileUnfinished(in))
			if out.Class != OutcomeTransient || out.Chargeable() {
				t.Errorf("%s: outcome = %+v; want transient and uncharged — an outage must not "+
					"exhaust a publication whose branch is already pushed", code, out)
			}
		}
	})

	t.Run("a stated reset survives the downgrade", func(t *testing.T) {
		at := time.Now().Add(time.Hour)
		in := OutcomeError{Outcome: Outcome{
			Class: OutcomeTransient, Code: CodeForgeUnavailable, RetryAt: &at,
		}}
		out := ClassifyOutcome(unchargedWhileUnfinished(in))
		if out.RetryAt == nil || !out.RetryAt.Equal(at) {
			t.Fatalf("RetryAt = %v; a forge that stated its own reset must still win", out.RetryAt)
		}
	})

	t.Run("a permanent outcome is left alone", func(t *testing.T) {
		// Converting these to retries would spin until the wall deadline
		// instead of telling the truth now.
		in := OutcomeError{Outcome: Outcome{Class: OutcomePermanent, Code: CodePublishConflict}}
		if out := ClassifyOutcome(unchargedWhileUnfinished(in)); out.Class != OutcomePermanent {
			t.Fatalf("outcome = %+v; want the permanent conflict unchanged", out)
		}
	})
}
```

- [ ] **Step 3: Run them and watch them fail**

Run: `task backend:test:integration -- -run TestPublish -v`
Expected: FAIL — the publish node runs through the fake executor and produces a plan artifact.

- [ ] **Step 4: Write the authorization step**

Create `backend/internal/runs/publish.go`. The whole file is the state machine; each step is its own transaction so a crash between two leaves a durable, readable position.

```go
package runs

// publishStatus mirrors the publish_intents.status CHECK. It exists so the
// state machine branches on a typed value rather than on string literals
// scattered across three files.
type publishStatus string

const (
	publishPending   publishStatus = "pending"
	publishPushed    publishStatus = "pushed"
	publishPublished publishStatus = "published"
	publishHalted    publishStatus = "halted"
)

// publishTitleCap and publishBodyCap bound what reaches a forge. Both are
// generous compared to a task title (200 bytes) and exist so a pathological
// definition cannot make a request the forge rejects for size.
const (
	publishTitleCap = 250
	publishBodyCap  = 8 << 10
)

// derefString reads an optional text column. Every nullable field on
// publish_intents is a receipt that may not have landed yet, and the empty
// string is the honest rendering of "not yet".
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// truncate cuts to at most n bytes without splitting a rune. The caps are
// generous enough that this only ever fires on pathological input, but a
// forge rejecting a request for size would surface as an unexplained
// permanent failure.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// emailLocalPart is the commit author's NAME (baked decision 7). caesar's
// users carry an email and nothing else, so the local part is the closest
// thing to a display name that exists; an address with no "@" is used whole.
func emailLocalPart(email string) string {
	if at := strings.IndexByte(email, '@'); at > 0 {
		return email[:at]
	}
	return email
}

// executePublish drives one git-publish attempt. It never calls the
// Executor: a publication is a control-plane effect with durable receipts,
// not something a container produces.
//
// The shape is: authorize (one tx, ends with the intent committed), push,
// receipt (one tx), open PR, receipt (one tx), then hand the result to the
// SAME evaluate the agentic path uses — so retries, exhaustion, budget and
// completion all keep exactly one implementation.
func (o *Orchestrator) executePublish(ctx context.Context, workerID string, cp cachedPlan, c claimed) {
	deadline := c.RunWallDeadline
	if rem := RemainingBudgetMS(c.BudgetMS, c.UsedMS); rem > 0 {
		if end := o.svc.clock.Now().Add(time.Duration(rem) * time.Millisecond); end.Before(deadline) {
			deadline = end
		}
	}
	execCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	hbDone := o.startHeartbeat(execCtx, cancel, c, workerID)
	res, err := o.runPublish(execCtx, cp, c)
	close(hbDone)
	o.evaluate(ctx, workerID, cp, c, PhaseRunning, res, err)
}
```

(`startHeartbeat` is the goroutine currently inlined in `execute`; extract it so both paths share one implementation and neither can forget to fence.)

```go
// intentRepo is the ONLY repository a publication addresses once its intent
// exists. Every probe, push and pull-request call goes through it.
//
// cp.repo is loaded from the assets row (orchestrator.go loadPlan), and that
// row is mutable: UpdateRepo can change a repo's slug, default branch and
// credential mid-run. So a publication resumed after a restart — or recovered
// after a crash, or settled after a deadline — would address whatever the
// repos table says NOW rather than what the approval named. The failure is
// quiet and bad in both directions: the effects land on the wrong repository,
// and recovery probes the wrong repository and concludes the real branch does
// not exist.
//
// authorizePublish revalidates cp.repo against the authorization before the
// intent is created (that is where a rename is caught and refused). After
// that point the intent's own columns are the authority, and cp.repo is not
// consulted again.
func intentRepo(intent store.PublishIntent) RepoRef {
	return RepoRef{
		ID:            intent.RepoID,
		Forge:         intent.ForgeKind,
		Slug:          intent.RepoSlug,
		DefaultBranch: intent.TargetBranch,
		CredentialID:  intent.CredentialID,
	}
}

// errIntentAdvanced says a receipt write found the intent already further
// along than this worker believed. It is never returned to the caller: the
// state machine reloads the row and continues from the real position.
//
// It exists because a lease takeover cannot retract a forge request already
// in flight. Two drivers can therefore be inside the same publication, and
// the loser's writes must lose without either failing the run or overwriting
// the winner's receipts.
var errIntentAdvanced = errors.New("runs: publish intent advanced concurrently")

// unchargedWhileUnfinished stops a publication in progress from spending its
// own failure budget.
//
// Both callers stand inside a live intent — 'pending' or 'pushed' — so a
// transient failure here is never "this work did not happen". It is "we could
// not see whether it happened", and the whole design answers that by asking
// again. But forge_unavailable and push_rejected are ordinary CHARGEABLE
// failures (Task 1), so a forge outage lasting max_attempts would exhaust the
// node and terminalize the run — with the branch, and possibly the pull
// request, already live on the forge and nothing left that will reconcile
// them. The budget exists to stop useless work; retrying here is not useless,
// it is the only thing that closes the publication.
//
// Only TRANSIENT outcomes are downgraded. A permanent one — a halted
// publication, a stale authorization — is a decision that repeats, and
// converting it to a retry would spin until the wall deadline instead of
// telling the truth now. The wall deadline remains the bound on this path,
// and expired-run settlement (Task 10) is what finishes the record if it hits.
func unchargedWhileUnfinished(err error) error {
	out := ClassifyOutcome(err)
	if out.Class != OutcomeTransient || !out.Chargeable() {
		return err
	}
	return OutcomeError{Outcome: Outcome{
		Class: OutcomeTransient, Code: CodePublishResumable,
		Message: fmt.Sprintf("%s while a publication is unfinished: %s", out.Code, out.Message),
		RetryAt: out.RetryAt, // a forge that stated its own reset still wins
	}}
}

// receiptFailure types the failure of a receipt transaction, and it is the
// difference between a run that resumes and a run that dies over a database
// blip with its branch already on the forge.
//
// The transaction runs AFTER its effect landed. `ClassifyOutcome` is
// default-deny (spec §2), so returning the raw pgx error would classify a lost
// connection as a PERMANENT internal_error: `evaluate` sends permanent
// outcomes straight to terminal failure, terminalizeRun supersedes every live
// phase, and a dead run is never claimed again — so nothing would ever go back
// and finish writing down a push that really happened.
//
// errIntentAdvanced is not a failure and passes through untouched: it means
// another driver's receipt won, which is a position to adopt, not an error.
func receiptFailure(intent store.PublishIntent, effect string, err error) error {
	if errors.Is(err, errIntentAdvanced) {
		return err
	}
	return OutcomeError{Outcome: Outcome{
		Class: OutcomeTransient, Code: CodePublishResumable,
		Message: fmt.Sprintf("%s receipt for %s did not commit: %v", effect, intent.Branch, err),
	}}
}

// runPublish is the state machine proper.
//
// Every step reloads the intent when it loses a receipt CAS, so the loop is
// driven by the DATABASE's idea of the position rather than by this
// goroutine's. It cannot spin: each iteration either advances the status or
// returns, and the status only moves forward.
//
// ONE INVARIANT HOLDS THIS WHOLE DESIGN UP, and it is worth stating plainly
// because the recovery machinery in Task 10 is only sound if it is true:
//
//	While an intent is 'pending' or 'pushed', every error leaving this
//	function is TRANSIENT and UNCHARGED.
//
// Why it has to be an invariant rather than a tendency: `evaluate` terminalizes
// a run on a permanent outcome or on an exhausted failure budget, and
// terminalization is finished — terminalizeRun supersedes every live phase,
// FinishRun carries no deadline predicate, and a dead run is never claimed
// again. There is no sweep that revisits a FAILED run, only expired ones
// (ListExpiredRunIDs matches 'pending', 'running', 'waiting'). So a run that
// dies here dies with its branch on the forge and nothing that will ever look
// again.
//
// With the invariant, the wall deadline becomes the ONLY way to terminalize
// over an unfinished publication — and that path settles first (Task 10 Step
// 5). Three pieces enforce it, and each one is a place the invariant was
// broken before it was written down: receiptFailure types the transaction that
// commits an effect's receipt, reload types the read that recovers the
// position, and unchargedWhileUnfinished stops a forge outage from spending
// the budget. Statuses 'published' and 'halted' are outside the invariant by
// definition: both are settled, so a permanent answer is honest.
func (o *Orchestrator) runPublish(ctx context.Context, cp cachedPlan, c claimed) (ExecutionResult, error) {
	intent, auth, err := o.authorizePublish(ctx, cp, c)
	if err != nil {
		return ExecutionResult{}, err
	}
	if intent.Status == string(publishHalted) {
		// A prior attempt halted this publication. Nothing resumes it but a
		// human, so this attempt reports the same permanent answer rather
		// than re-probing the forge.
		return ExecutionResult{}, OutcomeError{Outcome: Outcome{
			Class: OutcomePermanent, Code: CodePublishConflict,
			Message: "publication halted for manual reconciliation: " + derefString(intent.HaltReason),
		}}
	}
	// reload re-reads the durable position after a lost receipt CAS. The
	// winner's row is the truth; this worker adopts it rather than retrying
	// its own stale view.
	reload := func() error {
		row, rerr := store.New(o.svc.pool).GetPublishIntent(ctx, store.GetPublishIntentParams{
			RunID: c.Ref.RunID, NodeID: c.Ref.NodeID,
		})
		if rerr != nil {
			// Typed for the same reason receiptFailure is: this read happens
			// with an unfinished intent and a real effect already on the
			// forge, and a raw error would default-deny into a permanent
			// internal_error that kills the run over a failed SELECT.
			return OutcomeError{Outcome: Outcome{
				Class: OutcomeTransient, Code: CodePublishResumable,
				Message: fmt.Sprintf("reload publication position: %v", rerr),
			}}
		}
		intent = row
		return nil
	}

	if intent.Status == string(publishPending) {
		switch intent, err = o.pushPublication(ctx, cp, c, intent, auth); {
		case errors.Is(err, errIntentAdvanced):
			if err = reload(); err != nil {
				return ExecutionResult{}, err
			}
		case err != nil:
			return ExecutionResult{}, unchargedWhileUnfinished(err)
		}
	}
	if intent.Status == string(publishPushed) {
		switch intent, err = o.openPublication(ctx, cp, c, intent); {
		case errors.Is(err, errIntentAdvanced):
			if err = reload(); err != nil {
				return ExecutionResult{}, err
			}
		case err != nil:
			return ExecutionResult{}, unchargedWhileUnfinished(err)
		}
	}
	if intent.Status == string(publishHalted) {
		// A concurrent driver halted it under us.
		return ExecutionResult{}, OutcomeError{Outcome: Outcome{
			Class: OutcomePermanent, Code: CodePublishConflict,
			Message: "publication halted for manual reconciliation: " + derefString(intent.HaltReason),
		}}
	}
	return ExecutionResult{Artifacts: []NewArtifact{publicationArtifact(intent)}}, nil
}
```

```go
// authorizePublish is the one transaction that turns an approval into a
// durable intention (spec §3 Publishing). It CAS-consumes the single-use
// ApprovedPatch, revalidating artifact id, base SHA, tree hash, repo/target
// and the run's wall-deadline predicate — a stale, mismatched, expired or
// already-consumed authorization aborts here, before any forge call.
//
// It is idempotent by construction: an intent that already exists is
// returned as-is and its authorization is already consumed, so a retry after
// a crash resumes rather than re-authorizes.
func (o *Orchestrator) authorizePublish(ctx context.Context, cp cachedPlan, c claimed) (store.PublishIntent, authorization, error) {
	var intent store.PublishIntent
	var auth authorization
	err := pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		existing, err := q.GetPublishIntent(ctx, store.GetPublishIntentParams{
			RunID: c.Ref.RunID, NodeID: c.Ref.NodeID,
		})
		switch {
		case err == nil:
			intent = existing
			row, err := q.GetAuthorizationByID(ctx, existing.AuthorizationID)
			if err != nil {
				return err
			}
			auth = authorizationFromIDRow(row)
			return nil
		case !errorsIsNoRows(err):
			return err
		}

		valid, err := q.GetValidAuthorizationForUpdate(ctx, store.GetValidAuthorizationForUpdateParams{
			RunID: c.Ref.RunID, PublishNodeID: c.Ref.NodeID,
		})
		if errorsIsNoRows(err) {
			return OutcomeError{Outcome: Outcome{
				Class: OutcomePermanent, Code: CodeAuthorizationStale,
				Message: fmt.Sprintf("no live approval authorizes %q; it was never granted, already spent, or invalidated by a newer generation", c.Ref.NodeID),
			}}
		}
		if err != nil {
			return err
		}
		auth = authorizationFromValidRow(valid)
		if err := o.revalidateAuthorization(ctx, q, cp, c, auth); err != nil {
			return err
		}

		now, err := q.DBNow(ctx)
		if err != nil {
			return err
		}
		// The predicate is inside the UPDATE, evaluated against the database
		// clock: an expired run fails it and no intent is created, so a
		// publish can never win a race against the deadline sweeper.
		n, err := q.ConsumeAuthorization(ctx, store.ConsumeAuthorizationParams{ID: auth.ID, SettledAt: &now})
		if err != nil {
			return err
		}
		if n == 0 {
			return OutcomeError{Outcome: Outcome{
				Class: OutcomePermanent, Code: CodeAuthorizationStale,
				Message: "the authorization could not be consumed: it was spent concurrently, or the run's wall deadline has passed",
			}}
		}

		title, body := o.publicationText(cp, c, auth)
		intent, err = q.CreatePublishIntent(ctx, store.CreatePublishIntentParams{
			RunID: c.Ref.RunID, NodeID: c.Ref.NodeID, Attempt: c.Ref.Attempt,
			AuthorizationID: auth.ID, RepoID: auth.RepoID,
			// Copied from the authorization, never re-resolved: the publish
			// path addresses the repository the human approved.
			ForgeKind: auth.ForgeKind, RepoSlug: auth.RepoSlug,
			CredentialID: cp.repo.CredentialID,
			Branch:  PublicationBranch(auth.ArtifactID),
			TargetBranch: auth.TargetBranch, BaseSha: auth.BaseSha, TreeHash: auth.TreeHash,
			Title: title, Body: body, CreatedAt: now,
		})
		if isUniqueViolation(err) {
			// Either another worker created the intent between our read and
			// our insert, or the marker collides with a different run's
			// publication. Both are conflicts, and the second is one a human
			// must look at — so name what happened rather than guessing.
			return OutcomeError{Outcome: Outcome{
				Class: OutcomePermanent, Code: CodePublishConflict,
				Message: fmt.Sprintf("a publication intent already exists for branch %s", PublicationBranch(auth.ArtifactID)),
			}}
		}
		if err != nil {
			return fmt.Errorf("runs: create publish intent: %w", err)
		}
		return appendEvents(ctx, tx, now, []Event{{
			RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: EventPublishIntentCreated,
			CorrelationID: c.Ref.String(),
			Payload: map[string]any{
				"node_id": c.Ref.NodeID, "attempt": c.Ref.Attempt,
				"authorization_id": auth.ID.String(), "artifact_id": auth.ArtifactID.String(),
				"branch": intent.Branch, "target_branch": intent.TargetBranch,
				"base_sha": intent.BaseSha, "tree_hash": intent.TreeHash,
			},
		}})
	})
	return intent, auth, err
}

// revalidateAuthorization re-checks every field the approval pinned against
// what the run holds NOW. The gate wrote these values; a mismatch means
// something moved between approval and publication, and the answer is always
// to abort rather than to publish the newer thing.
func (o *Orchestrator) revalidateAuthorization(ctx context.Context, q *store.Queries,
	cp cachedPlan, c claimed, auth authorization,
) error {
	stale := func(format string, args ...any) error {
		return OutcomeError{Outcome: Outcome{
			Class: OutcomePermanent, Code: CodeAuthorizationStale,
			Message: fmt.Sprintf(format, args...),
		}}
	}
	// Every coordinate the approval pinned, not just the id. A repo's slug and
	// default branch are mutable through UpdateRepo (assets/repos.go), so an
	// id that still matches proves nothing about WHERE this patch would land:
	// rename the repo mid-run and the id is unchanged while the push
	// destination is a different repository entirely.
	if auth.RepoID != cp.repo.ID {
		return stale("the approval was granted for repo %s but the run targets %s", auth.RepoID, cp.repo.ID)
	}
	if auth.ForgeKind != cp.repo.Forge || auth.RepoSlug != cp.repo.Slug {
		return stale("the approval was granted for %s:%s but that repository is now %s:%s; it moved after the approval",
			auth.ForgeKind, auth.RepoSlug, cp.repo.Forge, cp.repo.Slug)
	}
	if auth.TargetBranch != cp.repo.DefaultBranch {
		return stale("the approval targeted branch %q but the repository's default branch is now %q",
			auth.TargetBranch, cp.repo.DefaultBranch)
	}
	if auth.BaseSha != cp.baseSHA {
		return stale("the approval pinned base %s but the run pinned %s", auth.BaseSha, cp.baseSHA)
	}
	gate, ok := cp.plan.GateFor(c.Ref.NodeID)
	if !ok || gate != auth.GateNodeID {
		return stale("the approval came from gate %q, which does not authorize %q in this definition", auth.GateNodeID, c.Ref.NodeID)
	}
	art, err := q.GetArtifactByID(ctx, auth.ArtifactID)
	if err != nil {
		return fmt.Errorf("runs: approved artifact %s: %w", auth.ArtifactID, err)
	}
	payload, err := unmarshalMap(art.Payload)
	if err != nil {
		return err
	}
	if got, _ := payload["tree_hash"].(string); got != auth.TreeHash {
		return stale("the approved artifact's tree hash is %s but the approval recorded %s", got, auth.TreeHash)
	}
	// The patch the publish node CONSUMES must be the one that was approved.
	// If the definition routes a different artifact here, the approved thing
	// and the published thing would diverge, which is the exact failure the
	// gate-dominates-exact-patch invariant exists to prevent.
	for _, in := range cp.plan.Nodes[c.Ref.NodeID].Inputs {
		if in.Artifact != ArtifactKindPatch {
			continue
		}
		row, err := q.GetNodeArtifact(ctx, store.GetNodeArtifactParams{
			RunID: c.Ref.RunID, NodeID: in.Origin, Type: in.Artifact,
		})
		if err != nil {
			return fmt.Errorf("runs: consumed patch %s: %w", in.Origin, err)
		}
		if row.ID != auth.ArtifactID {
			return stale("this node consumes artifact %s but the approval authorized %s", row.ID, auth.ArtifactID)
		}
	}
	return nil
}
```

`publicationText` builds the attribution (baked decision 7):

```go
// publicationText renders the PR title and body, and the commit message the
// publisher will use. Everything here is provenance a human reading the PR
// needs to trace it back to the run and the approval.
func (o *Orchestrator) publicationText(cp cachedPlan, c claimed, auth authorization) (title, body string) {
	title = truncate(cp.taskTitle, publishTitleCap)
	if title == "" {
		title = fmt.Sprintf("caesar run %s", c.Ref.RunID)
	}
	body = truncate(fmt.Sprintf(
		"Requested by %s, approved by %s.\n\n"+
			"caesar run `%s`, node `%s`, generation %d.\n"+
			"Definition `%s`.\nBase `%s`.\n",
		cp.requesterEmail, auth.ApproverEmail, c.Ref.RunID, c.Ref.NodeID,
		auth.Generation, cp.definitionHash, auth.BaseSha), publishBodyCap)
	return title, body
}
```

This needs three identities the spine does not carry today. Grow `cachedPlan`:

```go
type cachedPlan struct {
	plan       ExecPlan
	projectID  uuid.UUID
	taskInputs map[string]any
	baseSHA    string
	repo       RepoRef
	// The three a publication renders. All immutable for a run's lifetime —
	// the task's title and the pinned definition hash cannot change, and a
	// stale email costs a wrong "requested by" line on a PR body, which is
	// annoying rather than unsafe. Caching them is therefore honest.
	taskTitle      string
	definitionHash string
	requesterEmail string
}
```

`loadPlan` fills them from the rows it already reads (`taskRow.Title`, `runRow.DefinitionHash`) plus one `PrincipalSource.Email(ctx, taskRow.RequesterID)` call. The requester's *id* is not cached alongside the email: nothing downstream reads it, and a field with no read site is one a reviewer has to ask about five tasks before its answer arrives. The approver's email rides on the authorization read (`ApproverEmail`). Emails come from a new port rather than from a cross-context import:

```go
// PrincipalSource resolves a user id to the identity a publication is
// attributed to (spec §6: "the requester's identity flows into audit, run
// records, and published commits/PRs"). app implements it over identity;
// the runs context never learns what a user row looks like.
type PrincipalSource interface {
	Email(ctx context.Context, userID uuid.UUID) (string, error)
}
```

`Service` and `Orchestrator` take it; `loadPlan` resolves the requester's email once and caches it with the plan (a user's email is stable for a run's lifetime, and a stale one costs a wrong `From` on a PR body — annoying, not unsafe). The approver's email is resolved in `authorizePublish` from `gate_decisions.acted_by` via a join, so a decision by a since-deleted user still renders (the FK is RESTRICT, so it cannot actually be deleted; the join is simply the cheapest read).

- [ ] **Step 5: Write the push step**

```go
// pushPublication performs the git effect and commits its receipt. The order
// is load-bearing: the push happens, and only then is head_sha recorded. A
// crash in between leaves the intent 'pending' with a branch on the remote —
// which the NEXT attempt discovers through the recovery probe and adopts.
// Recording the receipt first would leave the opposite: a receipt for a push
// that never happened, which no probe can distinguish from a lost branch.
func (o *Orchestrator) pushPublication(ctx context.Context, cp cachedPlan, c claimed,
	intent store.PublishIntent, auth authorization,
) (store.PublishIntent, error) {
	// Recovery first: does the marker already exist, and is it ours?
	expect, err := o.recoverBranch(ctx, cp, intent)
	if err != nil {
		return intent, err
	}

	art, err := store.New(o.svc.pool).GetArtifactByID(ctx, auth.ArtifactID)
	if err != nil {
		return intent, err
	}
	diff, ok := artifactDiff(art)
	if !ok {
		// The plan-07 contract: a patch artifact's payload["diff"] is the
		// base64 canonical diff. Missing or non-string is a permanently
		// invalid artifact, never an empty publication.
		return intent, OutcomeError{Outcome: Outcome{
			Class: OutcomePermanent, Code: CodeInvalidOutput,
			Message: fmt.Sprintf("approved artifact %s carries no diff", auth.ArtifactID),
		}}
	}

	res, err := o.svc.publisher.PushPatch(ctx, PushRequest{
		Ref: c.Ref, Repo: intentRepo(intent), Branch: intent.Branch, BaseSHA: intent.BaseSha,
		TreeHash: intent.TreeHash, Patch: diff,
		Message:    commitMessageFor(intent, auth, c.Ref),
		AuthorName: emailLocalPart(cp.requesterEmail), AuthorEmail: cp.requesterEmail,
		// The intent's created_at, never the wall clock: it is the same value
		// on every attempt, so the rebuilt commit hashes identically and the
		// adopt path can actually match.
		Date:       intent.CreatedAt,
		ExpectHead: expect, Deadline: c.RunWallDeadline,
	})
	if err != nil {
		return o.maybeHalt(ctx, cp, c, intent, err)
	}

	now := o.svc.clock.Now()
	kind := EventPublishPushed
	if !res.Created {
		kind = EventPublishAdopted
	}
	err = pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		n, err := q.RecordPushReceipt(ctx, store.RecordPushReceiptParams{
			ID: intent.ID, HeadSha: &res.HeadSHA, UpdatedAt: now,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			// Somebody moved this intent past 'pending' while we were inside
			// the push — a lease takeover, or the expired-run settlement. The
			// receipt is already written and ours would be a regression, so
			// adopt theirs. Reloading rather than failing is what keeps a
			// takeover from charging a failure for work that succeeded.
			return errIntentAdvanced
		}
		return appendEvents(ctx, tx, now, []Event{{
			RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: kind,
			CorrelationID: c.Ref.String(),
			Payload: map[string]any{
				"node_id": c.Ref.NodeID, "branch": intent.Branch,
				"head_sha": res.HeadSHA, "tree_hash": res.TreeHash, "created": res.Created,
			},
		}})
	})
	if err != nil {
		return intent, receiptFailure(intent, "push", err)
	}
	intent.Status = string(publishPushed)
	intent.HeadSha = &res.HeadSHA
	return intent, nil
}

// recoverBranch asks the forge what the deterministic marker points at
// (spec §3: "recovery queries the forge for the deterministic markers") and
// returns what this attempt should ASSERT it last saw.
//
// It deliberately does NOT return an unrecognised head. An earlier shape did,
// handing the observed sha down as the push's lease value — which quietly
// turned "somebody else's branch" into "a branch I may replace". Ownership is
// proved by rebuilding the commit and comparing shas, which the publisher does
// with the pinned date; this function's job is only to fail fast on a
// disagreement we can already see, and to stay silent otherwise.
func (o *Orchestrator) recoverBranch(ctx context.Context, cp cachedPlan, intent store.PublishIntent) (string, error) {
	head, ok, err := o.svc.forge.BranchHead(ctx, intentRepo(intent), intent.Branch)
	if err != nil {
		return "", OutcomeError{Outcome: Outcome{
			Class: OutcomeTransient, Code: CodeForgeUnavailable,
			Message: fmt.Sprintf("probe %s: %v", intent.Branch, err),
		}}
	}
	if !ok {
		return "", nil
	}
	if intent.HeadSha != nil {
		// We hold a receipt, so we know exactly what should be there.
		if *intent.HeadSha != head {
			return "", OutcomeError{Outcome: Outcome{
				Class: OutcomePermanent, Code: CodePublishConflict,
				Message: fmt.Sprintf("branch %s is at %s but this publication recorded %s", intent.Branch, head, *intent.HeadSha),
			}}
		}
		return head, nil
	}
	// No receipt: the branch may be ours from a lost push, or somebody
	// else's entirely, and nothing here can tell them apart. Assert nothing
	// and let the publisher's rebuilt-sha comparison give the exact answer —
	// adopt on a match, halt on anything else.
	return "", nil
}
```

`commitMessage` renders the message the test in Task 8 asserted:

```go
// artifactDiff pulls the base64 canonical diff out of a patch artifact. Shared
// with Task 10's settlement rebuild for the same reason commitMessageFor is:
// the Patch bytes are commit input, so two readings of one artifact would be
// two different commits.
func artifactDiff(art store.Artifact) (string, bool) {
	payload, err := unmarshalMap(art.Payload)
	if err != nil {
		return "", false
	}
	diff, _ := payload["diff"].(string)
	return diff, diff != ""
}

// commitMessageFor renders the commit message. It is a free function over the
// three durable things it interpolates — deliberately NOT a method taking
// cachedPlan and claimed, neither of which it reads.
//
// That matters beyond tidiness: Task 10's expired-run settlement rebuilds this
// exact commit to prove a receiptless branch is ours, and it has no claimed to
// pass. If the rebuild rendered its message through a different code path, any
// drift between the two would change the commit sha and every ownership check
// would fail open — reporting somebody else's branch where ours is.
func commitMessageFor(intent store.PublishIntent, auth authorization, ref AttemptRef) string {
	return fmt.Sprintf("%s\n\ncaesar-run: %s\ncaesar-node: %s\ncaesar-artifact: %s\ncaesar-approved-by: %s\n",
		intent.Title, ref.RunID, ref.NodeID, auth.ArtifactID, auth.ApproverEmail)
}
```

- [ ] **Step 6: Write the pull-request step**

```go
// openPublication opens the pull request and commits its receipt. The FIND
// comes first, unconditionally: a crash between an open and its receipt is
// indistinguishable from never having opened, and asking is the only way to
// tell. A forge that answers "yes, here it is" turns a lost receipt into a
// found one; opening blind would leave two pull requests for one approval.
func (o *Orchestrator) openPublication(ctx context.Context, cp cachedPlan, c claimed,
	intent store.PublishIntent,
) (store.PublishIntent, error) {
	candidates, err := o.svc.forge.FindPullRequests(ctx, intentRepo(intent), intent.Branch)
	if err != nil {
		return intent, OutcomeError{Outcome: Outcome{
			Class: OutcomeTransient, Code: CodeForgeUnavailable,
			Message: fmt.Sprintf("probe pull request for %s: %v", intent.Branch, err),
		}}
	}
	pr, found, ambiguous := matchPublication(candidates, intent)
	if ambiguous {
		// More than one request on our marker could be ours, or one that
		// should be ours is missing the fields that would prove it. Either
		// way nothing here can pick correctly, and picking wrong records a
		// stranger's pull request as this run's publication.
		return o.haltPublication(ctx, cp, c, intent, fmt.Sprintf(
			"%d pull requests on branch %s and none is provably this publication's",
			len(candidates), intent.Branch))
	}
	adopted := found
	if !found && len(candidates) > 0 {
		// Requests exist on our marker and none of them is ours. Opening
		// another would leave two, so a human decides.
		return o.haltPublication(ctx, cp, c, intent, fmt.Sprintf(
			"branch %s already carries %d pull request(s), none matching this publication",
			intent.Branch, len(candidates)))
	}
	if !found {
		pr, err = o.svc.forge.OpenPullRequest(ctx, intentRepo(intent), PullRequestSpec{
			Head: intent.Branch, Base: intent.TargetBranch, Title: intent.Title, Body: intent.Body,
		})
		switch {
		case err != nil && ClassifyOutcome(err).Code == CodePublishConflict:
			// The forge refuses because a request for this head ALREADY
			// EXISTS — and the find above said there was none. Between those
			// two calls somebody opened it, and the only somebody that can
			// is another driver of this same intent: a lease takeover cannot
			// retract a request already on the wire, so two drivers can be
			// inside this publication at once (decision 20).
			//
			// Halting here is the trap. The winner has created the pull
			// request but may not have committed its receipt yet, so the
			// intent is still 'pushed' and HaltPublishIntent would SUCCEED —
			// excluding only 'published' fences the window after the receipt,
			// never the window before it. A human would then be told to
			// reconcile a publication that in fact succeeded, and the run
			// would fail with its pull request open on the forge.
			//
			// So the conflict is a recovery condition, not a verdict: ask
			// again, and let the answer decide.
			pr, err = o.adoptConflictingPublication(ctx, cp, c, intent)
			if err != nil {
				return intent, err
			}
			adopted = true
		case err != nil:
			return o.maybeHalt(ctx, cp, c, intent, err)
		}
	}

	now := o.svc.clock.Now()
	kind := EventPublishPROpened
	if adopted {
		kind = EventPublishAdopted
	}
	err = pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		n, err := q.RecordPRReceipt(ctx, store.RecordPRReceiptParams{
			ID: intent.ID, PrNumber: &pr.Number, PrUrl: &pr.URL, UpdatedAt: now,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return errIntentAdvanced
		}
		return appendEvents(ctx, tx, now, []Event{{
			RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: kind,
			CorrelationID: c.Ref.String(),
			Payload: map[string]any{
				"node_id": c.Ref.NodeID, "branch": intent.Branch,
				"pr_number": pr.Number, "pr_url": pr.URL, "state": pr.State, "adopted": adopted,
			},
		}})
	})
	if err != nil {
		return intent, receiptFailure(intent, "pull request", err)
	}
	intent.Status = string(publishPublished)
	intent.PrNumber, intent.PrUrl = &pr.Number, &pr.URL
	return intent, nil
}

// matchPublication picks the ONE pull request that is provably this
// publication's, out of everything sitting on our marker.
//
// A match must agree on all three coordinates: head branch (implied — that is
// what was queried), target base, and head commit. Anything less is a guess:
// a marker can carry several closed requests and requests to other bases, and
// adopting the wrong one records a stranger's pull request as this run's
// output.
//
// A candidate MISSING a field it needs is not a match either. Treating an
// empty Base or HeadSHA as "no disagreement" inverts the burden of proof —
// absent evidence would become permission — so those count toward ambiguous
// and the caller halts.
//
// ambiguous is true when more than one candidate matches, or when a candidate
// could neither be confirmed nor ruled out.
func matchPublication(candidates []PullRequestRef, intent store.PublishIntent) (pr PullRequestRef, found, ambiguous bool) {
	for _, cand := range candidates {
		switch {
		case cand.Base == "" || cand.HeadSHA == "":
			return PullRequestRef{}, false, true // unprovable either way
		case cand.Base != intent.TargetBranch:
			continue // definitively not ours
		case intent.HeadSha == nil || cand.HeadSHA != *intent.HeadSha:
			continue
		}
		if found {
			return PullRequestRef{}, false, true // two exact matches is not possible; do not choose
		}
		pr, found = cand, true
	}
	return pr, found, false
}

// maybeHalt turns a permanent publish_conflict into a halted intent and
// leaves every other error alone. Halting is durable and terminal: a human
// reconciles it, and no later attempt resumes it (spec §3: "halts for manual
// reconciliation on mismatch").
func (o *Orchestrator) maybeHalt(ctx context.Context, cp cachedPlan, c claimed,
	intent store.PublishIntent, err error,
) (store.PublishIntent, error) {
	out := ClassifyOutcome(err)
	if out.Code != CodePublishConflict {
		return intent, err
	}
	return o.haltPublication(ctx, cp, c, intent, out.Message)
}

// adoptConflictingPublication re-asks the forge after a create-PR conflict and
// returns the request this publication should record.
//
// It exists because "already exists" is an answer about the WORLD, not about
// who is entitled to it, and the entitled party is very often us — a second
// driver of this same intent that got there first. The re-query is what
// distinguishes the two, and it is safe to repeat: the find is read-only, and
// nothing here opens anything.
//
// Three answers, and only one of them is a human's problem:
//
//   - an exact match  → ours, opened by our other driver. Adopt it; the
//     receipt CAS then decides which driver records it, and the loser reloads
//     the winner's row.
//   - candidates, none provably ours → the marker really does carry a
//     stranger's request. Halt, exactly as the pre-create check would have.
//   - nothing visible → the forge told us a request exists and then did not
//     show it: read-after-write lag, or a replica behind the write. Guessing
//     either way is wrong, so resume — UNCHARGED, because a publication whose
//     branch is already pushed must not spend its budget on the forge's
//     eventual consistency.
func (o *Orchestrator) adoptConflictingPublication(ctx context.Context, cp cachedPlan, c claimed,
	intent store.PublishIntent,
) (PullRequestRef, error) {
	candidates, ferr := o.svc.forge.FindPullRequests(ctx, intentRepo(intent), intent.Branch)
	if ferr != nil {
		return PullRequestRef{}, OutcomeError{Outcome: Outcome{
			Class: OutcomeTransient, Code: CodePublishResumable,
			Message: fmt.Sprintf("re-probe after create conflict on %s: %v", intent.Branch, ferr),
		}}
	}
	pr, found, ambiguous := matchPublication(candidates, intent)
	switch {
	case found:
		return pr, nil
	case ambiguous || len(candidates) > 0:
		_, herr := o.haltPublication(ctx, cp, c, intent, fmt.Sprintf(
			"forge refused a second pull request on branch %s and none of the %d present is provably this publication's",
			intent.Branch, len(candidates)))
		return PullRequestRef{}, herr
	default:
		return PullRequestRef{}, OutcomeError{Outcome: Outcome{
			Class: OutcomeTransient, Code: CodePublishResumable,
			Message: fmt.Sprintf("forge reports a pull request on %s that it does not yet list", intent.Branch),
		}}
	}
}

func (o *Orchestrator) haltPublication(ctx context.Context, cp cachedPlan, c claimed,
	intent store.PublishIntent, reason string,
) (store.PublishIntent, error) {
	now := o.svc.clock.Now()
	txErr := pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		n, err := q.HaltPublishIntent(ctx, store.HaltPublishIntentParams{
			ID: intent.ID, HaltReason: &reason, UpdatedAt: now,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			// The intent reached 'published' (or was already halted) while we
			// were deciding to halt it. A stale worker losing a create-PR race
			// lands here, and it must not be able to tell a human to reconcile
			// a publication that in fact succeeded.
			return errIntentAdvanced
		}
		if err := appendEvents(ctx, tx, now, []Event{{
			RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: EventPublishHalted,
			CorrelationID: c.Ref.String(),
			Payload:       map[string]any{"node_id": c.Ref.NodeID, "branch": intent.Branch, "reason": reason},
		}}); err != nil {
			return err
		}
		return o.svc.audit.Record(ctx, tx, audit.Entry{
			Action: "publish.halted", SubjectKind: "publish_intent", SubjectID: intent.ID.String(),
			Detail: map[string]any{
				"run_id": c.Ref.RunID.String(), "node_id": c.Ref.NodeID,
				"branch": intent.Branch, "reason": reason,
			},
		})
	})
	if txErr != nil {
		return intent, txErr
	}
	intent.Status = string(publishHalted)
	return intent, OutcomeError{Outcome: Outcome{
		Class: OutcomePermanent, Code: CodePublishConflict, Message: reason,
	}}
}

func publicationArtifact(intent store.PublishIntent) NewArtifact {
	payload := map[string]any{
		"branch": intent.Branch, "target_branch": intent.TargetBranch,
		"base_sha": intent.BaseSha, "tree_hash": intent.TreeHash,
		"head_sha": derefString(intent.HeadSha),
		"pr_url":   derefString(intent.PrUrl),
	}
	if intent.PrNumber != nil {
		payload["pr_number"] = *intent.PrNumber
	}
	return NewArtifact{Type: ArtifactKindPublication, Payload: payload}
}
```

- [ ] **Step 7: Dispatch on node kind**

In `orchestrator.go`, at the top of `execute`, after `node` is resolved and the `phase.started` event is appended:

```go
	if node.Kind == KindGitPublish {
		// A publication is a control-plane effect with durable receipts, not
		// something a container produces. It shares the claim, the lease, the
		// heartbeat and the evaluation — only the middle differs.
		o.executePublish(ctx, workerID, cp, c)
		return
	}
```

- [ ] **Step 8: Add the missing store statements**

`GetValidAuthorizationForUpdate`, `GetAuthorizationByID` and `CountSettledPublications` already landed in Task 3 — the last of those because Task 6's revision path calls it, four tasks before this one. One read remains:

```sql
-- name: GetArtifactByID :one
SELECT * FROM artifacts WHERE id = $1;
```

**Both authorization reads return a sqlc ROW type, not `store.ApprovedPatch`.** A `SELECT ap.*, u.email` generates `store.GetValidAuthorizationForUpdateRow` and `store.GetAuthorizationByIDRow`, which carry `ApproverEmail` where the table struct does not. The two shapes are structurally identical, but Go will not unify them, so `authorizePublish` cannot assign both to one variable. Declare one local alias and convert at the single point of divergence:

```go
// authorization is the joined authorization the publish path works with: the
// approved_patches row plus the approver's email. Both store reads produce
// structurally identical rows under different generated names, so this is the
// one type the state machine passes around.
type authorization struct {
	store.ApprovedPatch
	ApproverEmail string
}
```

with two five-line converters (`authorizationFromValidRow`, `authorizationFromIDRow`) next to it. Every signature below that says `auth authorization` means this type — not `store.ApprovedPatch`.

- [ ] **Step 9: Extend the fakes**

`fake.Forge` grows the three methods, the injectors the tests use (`CrashAfterPR`, `FailPRTimes`, `FailBranchProbes`, `HoldReceiptAfterFirstPullRequest`), and call counters (`OpenPRCount`, `BranchProbeCount`, `PullRequestCount`). It gets **no** `PushCount`: the forge never pushes — a push is a `gitops` effect — and a counter on both fakes with different meanings is how an assertion ends up watching the wrong thing. Pushes are counted on `fake.Publisher` alone. `fake.Publisher` (new file `internal/runs/fake/publisher.go`) implements **both** `runs.GitPublisher` methods against a local bare repo resolved from a `map[uuid.UUID]string` of repo id → bare path: `PushPatch` calls `gitops.Publish`, and `ExpectedHead` calls `gitops.ExpectedPublicationHead` with the same inputs. Implementing only the first would compile-fail the wiring, and stubbing the second would let Task 10's settlement tests pass without exercising the rebuild they exist to prove.

Its injectors carve up the one window that matters — between the push landing and its receipt committing — four ways, because Tasks 9 and 10 need to enter it from different sides:

| Injector | Push reaches the remote? | Call returns | Models |
|---|---|---|---|
| `CrashAfterPush` | yes | typed transient `publish_resumable` | the worker reports a failure after pushing; the next attempt must adopt |
| `HangAfterPush` | yes | blocks until released | the worker is alive but wedged; a lease sweep or a deadline finds a landed push with no receipt |
| `HangBeforePush` | **no** | blocks until released | the worker is wedged before causing anything; settlement must find nothing on the forge |
| `DelayPushDelivery(t)` | not yet — the push is held before it reaches the remote and delivered when the returned `release` is called | blocks until released | a push issued just before the wall deadline that lands AFTER settlement's first probe. Task 10's stable-negative rule (decision 25) has no other proof, and it belongs on the publisher because a push is a `gitops` effect, not a forge call. |

```go
// CrashAfterPush makes PushPatch do its real work and then fail, which is
// the exact window that makes a publication non-idempotent if recovery is
// wrong: the branch is on the remote and no receipt exists.
func (p *Publisher) CrashAfterPush(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.crashAfterPush = on
}

// ...and inside PushPatch, AFTER gitops.Publish has returned successfully:
//
//	if p.crashAfterPush {
//		return runs.PushResult{}, runs.OutcomeError{Outcome: runs.Outcome{
//			Class: runs.OutcomeTransient, Code: runs.CodePublishResumable,
//			Message: "fake publisher: crashed after pushing",
//		}}
//	}
```

**The injected failure must be TYPED, and that is not a stylistic point.**
`ClassifyOutcome` is default-deny: an untyped error becomes a PERMANENT
`internal_error`, and `evaluate` sends a permanent outcome straight to terminal
failure. A fake returning a bare `errors.New` would kill the run on the first
injected crash, so every "…and then it recovers" assertion in Tasks 9 and 13
would be unreachable — the tests would fail for a reason unrelated to the
behaviour they exist to prove.

The same rule binds the real code. A database error while committing a receipt
happens AFTER the effect landed; classifying it permanent would terminalize a
run whose branch is already on the forge. Wrap receipt-transaction failures as
transient `publish_resumable` rather than letting them fall through to the
default.

Note what each injector actually models, and keep them distinct. A typed
transient is a worker that *reports* a failure and retries — cheap and
deterministic. A real process death returns nothing at all; the lease simply
expires. That second shape is `HangAfterPush` plus `killWorkerAndExpireLease`,
which is the path Task 10's reconciliation tests take. Both windows are real.

The hang switches block on a channel the harness closes, and every one of them — hangs and delays alike — must be released by `t.Cleanup`: a test that leaves a publisher wedged leaks a goroutine that `-race` will eventually surface in an unrelated test.

- [ ] **Step 10: Wire the publisher, the principal source and `serve.go`**

Step 1 grew `NewService` with a `publisher GitPublisher` and Step 4 grew it again with a `PrincipalSource`, and `backend/internal/app/serve.go:101-103` is the only production call site. Step 14 runs `go test ./...`, so the tree has to compile at the end of THIS task — and Step 1 rules out a `NotImplementedGitPublisher`, so there is no stub to lean on. The real adapters land here rather than in Task 12; Task 8 already shipped `gitops.Publish`, so the publisher is pure delegation.

```go
// gitPublisher implements runs.GitPublisher over gitops.Publish (Task 8). Each
// publication gets a fresh directory under the work dir, removed when the call
// returns: nothing about one publication may survive into another. Both methods
// share one `prepare`, so ExpectedHead cannot disagree with PushPatch about
// which repository, which credential or which inputs a publication uses — the
// whole point of ExpectedHead being that it reproduces what PushPatch would do.
type gitPublisher struct {
	assets  *assets.Service
	workDir string
}

// principalSource implements runs.PrincipalSource over identity. Four lines,
// in the composition root, because the runs context never learns what a user
// row looks like.
type principalSource struct{ svc *identity.Service }

func (p principalSource) Email(ctx context.Context, userID uuid.UUID) (string, error) {
	u, err := p.svc.User(ctx, userID)
	if err != nil {
		return "", err
	}
	return u.Email, nil
}
```

and in `serve.go`, both new arguments go in:

```go
	// Task 12 replaces this with cfg.Forge.PublishWorkDir; until that config
	// block exists, publications materialize under the system temp dir.
	publisher := gitPublisher{assets: assetsSvc, workDir: os.TempDir()}

	runsSvc := runs.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}),
		runsDefinitionSource{svc: workflowsSvc}, runsAssetSource{svc: assetsSvc}, runsAssetSource{svc: assetsSvc},
		unavailableForge{}, publisher, principalSource{svc: identitySvc}, runs.Params{Snapshot: runs.DefaultSnapshot()})
```

`unavailableForge` stays for now: this task changes the constructor, not the forge. Task 12 adds `config.Forge`, gives `gitPublisher` its `endpoints` field and the configured work dir, rewrites `cloneURL`, and swaps `unavailableForge{}` for the real adapter — by which time the publisher and the principal source are already in place.

- [ ] **Step 11: Regenerate the store package**

Run: `task db:sqlc`
Expected: regenerated store package compiles; git diff shows only the new methods (`GetArtifactByID`).

- [ ] **Step 12: Prove a live publication refuses a revision**

Baked decision 17's end-to-end proof belongs here rather than in Task 6, because nothing before this task can drive a `publish_intents` row to `published` — Task 6 asserts the same refusal against a seeded row, which is honest but does not exercise a real push standing between two gates. Add to `backend/internal/runs/revisions_integration_test.go`:

```go
func TestRequestChanges_RefusedOnceARunHasPublished(t *testing.T) {
	// Chained gates are legal and each authorizes its own publish. So the
	// first publication can be live on the forge while a later gate is still
	// waiting — and no transaction in this process can un-push a commit.
	// Invalidating authorizations does not help: the one that funded the
	// publication is CONSUMED, not valid.
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, twoGateWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate_one", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	h.waitForIntentStatus(t, run.ID, "publish_one", "published", 40*time.Second)
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 30*time.Second) // gate_two

	_, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate_two", Decision: runs.DecisionRequestChanges, Note: "start over",
	})
	if xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("error = %v (class %s), want a conflict naming the live publication", err, xerrors.ClassOf(err))
	}
	if got := h.run(t, run.ID).Generation; got != 1 {
		t.Errorf("generation = %d, want 1: the refused revision must open nothing", got)
	}
	// The published branch is untouched and the human still has both real
	// answers available.
	if got := h.intent(t, run.ID, "publish_one").Status; got != "published" {
		t.Errorf("publication status = %q, want it left alone", got)
	}
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate_two", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatalf("approve must still be available: %v", err)
	}
}
```

Run: `task backend:test:integration -- -run TestRequestChanges_RefusedOnceARunHasPublished -race -v`
Expected: PASS.

- [ ] **Step 13: Run the publish tests**

Run: `task backend:test:integration -- -run TestPublish -race -v`
Expected: PASS.

- [ ] **Step 14: Run everything**

Run: `go test ./... -race && task backend:test:integration`
Expected: PASS.

- [ ] **Step 15: Commit**

```bash
git add backend/internal/runs/publish.go backend/internal/runs/publish_integration_test.go \
        backend/internal/runs/revisions_integration_test.go \
        backend/internal/runs/ports.go backend/internal/runs/orchestrator.go \
        backend/internal/runs/fake/ backend/internal/runs/store/ \
        backend/internal/app/
git commit -m "feat(runs): drive git-publish as an effect state machine with durable receipts and recovery"
```

---
### Task 10: Reconciling a publication — the crash the lease sweep sees

A worker that dies mid-publication leaves a `running` phase whose lease expires. `reconcileAttempt` currently asks the **Executor** what happened, and the executor knows nothing about publications — it would answer "not completed" and charge a `reconcile_lost` for an attempt that may have already pushed. Reconciliation of a publish node asks the forge instead.

**Files:**
- Modify: `backend/internal/runs/orchestrator.go` (`reconcileAttempt` :599-634; `sweepExpiredRuns` :325)
- Modify: `backend/internal/runs/publish.go` (add `inspectPublication`, `settleRunPublications`)
- Modify: `backend/internal/runs/store/queries.sql` (`ListUnsettledPublishIntents`, `MarkPublicationAbsent`, `ClearPublicationAbsent`, `HaltStablyAbsentPublication`)
- Modify: `backend/internal/runs/fake/` (`DelayBranchDelivery`, `DelayPullRequestDelivery`, `HangBeforePullRequest`; plus the `waitForForgeBranchProbe` and `waitForForgePRProbe` harness helpers. `HangBeforePush`, `FailBranchProbes` and `DelayPushDelivery` already landed in Task 9 Step 9.)
- Test: `backend/internal/runs/publish_integration_test.go`

**Interfaces:**
- Produces:
  - `func (o *Orchestrator) inspectPublication(ctx context.Context, cp cachedPlan, ref AttemptRef) (InspectResult, error)`
  - `func (o *Orchestrator) settleRunPublications(ctx context.Context, runID uuid.UUID) bool`
  - `settlePublication`, `settleUnpushedPublication`, `settleUnopenedPublication`, `recordStableAbsence`, `halt` (the settlement-side halt — the same row write as `haltPublication` without the attempt-scoped event and audit correlation a dead run has no `claimed` to supply), `pushRequestFor`
  - constants `publishSettleGrace`, `publishSettleProbeBudget`
- Consumes (from earlier tasks, and the reason each must already exist):
  - `runs.CodePublishNotStarted`, `runs.CodePublishResumable` and their `Chargeable()`/`retrySchedules` entries — **Task 1**
  - `CountSettledPublications` — **Task 3**
  - `commitMessageFor`, `artifactDiff`, `matchPublication`, `intentRepo`, the `authorization` alias and `authorizationFromIDRow` — **Task 9**, all shared with the live publish path so settlement rebuilds the identical commit

There are **two** sweeps that can meet a half-finished publication, and they need opposite answers. The lease sweep (Steps 1–4) resumes one: the run is alive, so re-driving the state machine is correct. The wall-deadline sweep (Steps 5–6) must not — the run has no authority left to cause anything — so it only asks the forge what already exists and writes the answer down. Both are in this task because getting one right and the other wrong is how a publication becomes an orphan nobody looks for.

- [ ] **Step 1: Write the failing reconciliation tests**

```go
func TestReconcile_PublicationThatAlreadyLandedIsAdoptedNotRepublished(t *testing.T) {
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	// Both effects land, then the worker dies before it can evaluate.
	h.publisher.HangAfterPush(true)
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	h.waitForPhaseStatus(t, run.ID, "publish", runs.PhaseRunning, 30*time.Second)
	h.killWorkerAndExpireLease(t, run.ID, "publish")

	h.waitForRunStatus(t, run.ID, runs.RunSucceeded, 60*time.Second)

	if got := h.publisher.PushCount(); got != 1 {
		t.Errorf("pushes = %d, want 1: reconciliation adopts, it does not re-push", got)
	}
	if got := h.phases(t, run.ID).byNode(t, "publish"); got.Outcome != nil {
		t.Errorf("the adopted attempt must not carry a failure outcome: %+v", got.Outcome)
	}
	if n := h.countEvents(t, run.ID, runs.EventPhaseReconciling); n != 1 {
		t.Errorf("phase.reconciling events = %d, want 1", n)
	}
}

func TestReconcile_PublicationThatNeverStartedIsNotChargedAsLost(t *testing.T) {
	// A worker that died before creating the intent left nothing behind. The
	// next attempt should authorize normally — and must NOT be told the
	// attempt was "lost", which would spend a chargeable failure for a
	// publication that never began.
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)
	h.stopOrchestrator(t)
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	h.materializeAndStrandPublishPhase(t, run.ID) // insert a running row, expire its lease
	h.startOrchestrator(t)

	h.waitForRunStatus(t, run.ID, runs.RunSucceeded, 60*time.Second)
	if got := h.phases(t, run.ID).byNode(t, "publish"); got.ChargeableFailures != 0 {
		t.Errorf("chargeable failures = %d, want 0", got.ChargeableFailures)
	}
}

func TestReconcile_HaltedPublicationStaysHalted(t *testing.T) {
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)
	h.stopOrchestrator(t)
	res, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	})
	if err != nil {
		t.Fatal(err)
	}
	auth := h.authorization(t, *res.AuthorizationID)
	h.pushForeignBranch(t, "publish-marker", runs.PublicationBranch(auth.ArtifactID))
	h.startOrchestrator(t)
	h.waitForRunStatus(t, run.ID, runs.RunFailed, 40*time.Second)

	// A lease sweep over the halted intent must reach the same answer, not
	// probe the forge again.
	before := h.forge.BranchProbeCount()
	h.forceReconcile(t, run.ID, "publish")
	time.Sleep(2 * h.pollInterval)
	if got := h.forge.BranchProbeCount(); got != before {
		t.Errorf("a halted publication was probed again (%d -> %d); halting is terminal", before, got)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `task backend:test:integration -- -run TestReconcile_Publication -v`
Expected: FAIL — the adopted case re-pushes.

- [ ] **Step 3: Branch reconciliation on node kind**

In `reconcileAttempt`, before the `o.exec.Inspect` call:

```go
	var ins InspectResult
	if cp.plan.Nodes[row.NodeID].Kind == KindGitPublish {
		// The executor never saw this attempt: a publication is a
		// control-plane effect. Its durable position IS the intent row, and
		// the forge is the only witness for effects whose receipt was lost.
		ins, err = o.inspectPublication(ctx, cp, ref)
	} else {
		ins, err = o.exec.Inspect(ctx, ref)
	}
```

- [ ] **Step 4: Write `inspectPublication`**

```go
// inspectPublication answers reconciliation's question for a publish node:
// what does the durable record say, and does the forge agree?
//
// The four positions map cleanly:
//
//   - no intent      → nothing happened. Not completed, and NOT a loss: the
//     next attempt authorizes from scratch. Reported as an uncharged
//     transient so the node's failure budget is untouched.
//   - halted         → terminal. The typed permanent conflict, so the run
//     dies naming what a human has to reconcile.
//   - pending/pushed → resume. Re-drive the state machine from the recorded
//     position; both steps are idempotent by probe.
//   - published      → done. Adopt the publication artifact idempotently.
func (o *Orchestrator) inspectPublication(ctx context.Context, cp cachedPlan, ref AttemptRef) (InspectResult, error) {
	q := store.New(o.svc.pool)
	intent, err := q.GetPublishIntent(ctx, store.GetPublishIntentParams{
		RunID: ref.RunID, NodeID: ref.NodeID,
	})
	if errorsIsNoRows(err) {
		// Nothing was authorized, so nothing was published. Say so with an
		// uncharged transient rather than reconcile_lost: no work was lost,
		// and charging here would spend a retry on a worker that died before
		// it did anything at all.
		return InspectResult{Completed: true, Outcome: &Outcome{
			Class: OutcomeTransient, Code: CodePublishNotStarted,
			Message: "publication had not begun when its worker died",
		}}, nil
	}
	if err != nil {
		return InspectResult{}, err
	}
	switch publishStatus(intent.Status) {
	case publishHalted:
		return InspectResult{Completed: true, Outcome: &Outcome{
			Class: OutcomePermanent, Code: CodePublishConflict,
			Message: "publication halted for manual reconciliation: " + derefString(intent.HaltReason),
		}}, nil
	case publishPublished:
		return InspectResult{Completed: true, Result: ExecutionResult{
			Artifacts: []NewArtifact{publicationArtifact(intent)},
		}}, nil
	}
	// pending or pushed: the publication is real and unfinished, and the next
	// attempt resumes from this exact row.
	//
	// Returning Completed:false here would be a bug, not a shorthand.
	// reconcileAttempt turns every incomplete inspection into a CHARGEABLE
	// reconcile_lost (orchestrator.go: `if err != nil || !ins.Completed`),
	// which spends the phase's failure budget — and a publication that loses
	// its lease near max_attempts would exhaust and terminalize the run with a
	// branch already on the forge and no later corrector. Nothing was lost
	// here: the durable position IS the intent row.
	return InspectResult{Completed: true, Outcome: &Outcome{
		Class: OutcomeTransient, Code: CodePublishResumable,
		Message: fmt.Sprintf("publication is %s and resumes from its intent", intent.Status),
	}}, nil
}
```

`CodePublishNotStarted` and `CodePublishResumable`, their `Chargeable()` exemption and their `retrySchedules` rows all landed in **Task 1 Steps 7 and 7b**; this task only uses them. Nothing to redefine here. Do not reach for a Plan 07 credential code because it happens to be uncharged: `publish_not_started` has its own row, `{floor: 0, base: 0, cap: 0}`, so this requeue is **immediate** — which is the whole reason the row exists, since an unknown code falls through to `retrySchedules[CodeRateLimited]` and would park a publication that never began behind a two-minute floor.

One consequence to be deliberate about: an uncharged retry means an attempt that keeps dying before authorizing loops without ever exhausting. The wall deadline is the backstop, exactly as it is for `rate_limited` — and it is a real backstop, because both codes park on a schedule rather than spinning.

- [ ] **Step 5: Make an unsettled publication a terminalization obligation**

Reconciliation above covers the crash a *lease sweep* sees. There is a second sweep, and today it would orphan a publication outright.

`sweepExpiredRuns` (`orchestrator.go:325`) terminalizes a wall-deadline breach. Its comment says the quiet part: *"Past the deadline the run's outcome is already decided; only the credential obligation is still owed."* That was true through Plan 07, when the only thing a dying run could still owe was a lease release. It stops being true here. A publish attempt runs under a context that ends at the wall deadline (`executePublish`), and a cancelled push or PR call **cannot prove the remote refused it** — the request may well have landed. Meanwhile `terminalizeRun` supersedes every live phase, `FinishRun` carries no deadline predicate, and a dead run is never claimed again. Net effect: a branch and possibly a pull request exist on the forge, and caesar's durable record says the publication never happened, with nothing that will ever look again.

A landed external effect is exactly as much an obligation as an unreleased lease, so give it the same treatment. Add `settleRunPublications` beside `settleRunCredentials` and make terminalization conditional on both:

```go
	for _, runID := range ids {
		if !o.settleRunCredentials(ctx, runID) {
			continue
		}
		// A publication that may have landed is an obligation, not a
		// detail. Terminalizing over one makes the orphan durable — the
		// run is dead, its phases superseded, and nothing reconciles a
		// dead run — so an unsettled intent leaves the run for the next
		// tick exactly as an unsettled lease does.
		if !o.settleRunPublications(ctx, runID) {
			continue
		}
```

```go
// settleRunPublications gives every unsettled publication of an expired run a
// final, durable answer, and reports whether the run may now be terminalized.
//
// The asymmetry with reconciliation is deliberate. Reconciliation RESUMES a
// publication: it may push, it may open a pull request. This may do neither —
// the run is past its deadline and has no authority to cause anything. It only
// ASKS the forge what already exists and writes down the answer:
//
//   - the marker is absent            → NOT an answer yet. See below.
//     Once it is an answer, halt the intent with "expired before publishing"
//     so the row stops reading as in-flight.
//   - the marker is ours              → record the push receipt, and record
//     the pull request too if one is already open. The effect is real, so the
//     record must say so even though the run dies.
//   - the marker exists and we cannot prove it is ours → halt with the
//     conflict, which is the same answer the live path gives.
//
// "Ours" is the hard part, because the case that matters most is the one with
// NO receipt: the push landed and head_sha is still NULL, so there is nothing
// stored to compare the remote head against. The forge port cannot answer it
// (it reads a branch sha and no more) and the publisher port must not be asked
// (PushPatch is effectful, and an expired run has no authority to cause
// anything). What closes it is Task 8's ExpectedPublicationHead: the same
// clone-apply-commit the publisher performs, stopping before the push, which
// reproduces the exact sha this authorization produces because the commit date
// is pinned. Compare that to the remote head and the answer is exact.
//
// A forge that cannot be reached returns false: the run stays matched by the
// expired-run predicate and is retried next tick. Guessing here would make the
// wrong answer durable, and this is the one place with no later corrector.
func (o *Orchestrator) settleRunPublications(ctx context.Context, runID uuid.UUID) bool
```

**Absence is the case to be most careful about, and one probe never settles it.**

The whole reason this function exists is that a cancelled request cannot prove
the remote refused it. That cuts both ways, and the second way is easy to miss:
a push issued microseconds before the deadline may still be in flight when
settlement probes. Seeing "no such branch" and halting would terminalize the
run, and then the push lands — an orphan on the forge that no path examines,
because the run is dead and its phases superseded. That is the exact outcome
this obligation was added to prevent, reintroduced through the negative case.

Fencing the phase does not close it either: fencing changes database state,
while the HTTP request is already on the wire.

So absence must be *stable* before it settles anything. Two conditions, both
required:

1. **Quiescence: no phase of this run is still `running`.** State it that way
   and not as "no live phase row", which is the obvious phrasing and is wrong
   in a way that deadlocks the sweep. `settleRunCredentials` runs immediately
   before this and leaves every fenced phase in `reconciling`
   (`MarkPhaseReconcilingPastDeadline`), and `reconciling` is NOT terminal
   (`status.go`: `Terminal()` covers succeeded, failed, dead, superseded). A
   "no live row" test would therefore never pass on the very runs this exists
   for: settlement would defer forever, the run would never terminalize, and
   the orphan would be joined by a run that cannot die.

   What quiescence actually has to mean is *fenced*: the worker has lost the
   CAS it would need to commit anything, so no NEW effect can be issued.
   `running` is the only status where that is not yet true.

2. **A stable negative, on the marker that matches the intent's STATUS.** The
   first absent probe records `absent_since` and returns false, deferring the
   run. Only a later tick that still finds it absent, at least
   `publishSettleGrace` after `absent_since`, may halt. Any probe that finds
   the marker clears `absent_since` — the effect landed after all, and it gets
   recorded.

   Which marker is not a detail, and a single "is the branch there" test
   silently drops half the problem. `ListUnsettledPublishIntents` returns both
   statuses, and they are unfinished in different places:

   - **`pending`** — no push receipt. The marker at risk is the **branch**.
   - **`pushed`** — the branch is recorded and certainly exists. The marker at
     risk is the **pull request**, and its window is identical: a create call
     issued just before the deadline can still be on the wire. Halting a
     `pushed` intent because no pull request is visible orphans exactly the
     request the obligation exists to catch; deferring forever whenever none is
     visible strands every publication that genuinely never got that far.

   One `absent_since` column serves both, because an intent is in exactly one
   status and so has exactly one marker in question.

The grace is a constant (start at 30s, comfortably beyond a forge request's
own timeout) rather than config: it is a property of how long an HTTP request
can be in flight, not an operator preference, and a dead-config knob nobody
tunes is worse than a named constant.

The cost is that an expired run holding a genuinely-never-pushed intent takes
one extra tick to die. That is the right trade against a silent orphan.

Three statements carry the negative. They are `:execrows` and CAS on status so
a settlement racing a live driver's receipt loses instead of overwriting it:

```sql
-- name: MarkPublicationAbsent :execrows
-- First sighting of an absent marker. Set once and never refreshed: the grace
-- measures from when absence was FIRST seen, so re-stamping it on every tick
-- would push the deadline away forever and the intent would never settle.
UPDATE publish_intents SET absent_since = now(), updated_at = now()
WHERE id = $1 AND status = $2 AND absent_since IS NULL;

-- name: ClearPublicationAbsent :execrows
-- The marker turned up. Whatever we thought was missing is not, and the next
-- absence starts its grace from scratch.
UPDATE publish_intents SET absent_since = NULL, updated_at = now()
WHERE id = $1 AND absent_since IS NOT NULL;

-- name: HaltStablyAbsentPublication :execrows
-- Halt only if the marker has been absent, continuously, for the whole grace.
-- The comparison is in SQL against the DATABASE clock, like every other
-- deadline predicate in this system: an app-side comparison would let clock
-- skew shorten the one window protecting an in-flight request.
--
-- The grace arrives as SECONDS and is widened by make_interval rather than
-- being cast from an interval literal: sqlc maps `interval` to pgtype.Interval,
-- so an interval parameter would put a pgtype value in the call signature
-- where a time.Duration belongs.
UPDATE publish_intents SET status = 'halted', halt_reason = sqlc.arg(halt_reason), updated_at = now()
WHERE id = sqlc.arg(id) AND status = sqlc.arg(status)
  AND absent_since IS NOT NULL
  AND absent_since <= now() - make_interval(secs => sqlc.arg(grace_seconds)::int);
```

And the body, which is the part Task 10 previously left as prose:

```go
// publishSettleGrace is how long a marker must stay missing before absence is
// an answer. It bounds how long a request can be in flight after its issuer
// was fenced, not an operator preference — hence a constant.
const publishSettleGrace = 30 * time.Second

func (o *Orchestrator) settleRunPublications(ctx context.Context, runID uuid.UUID) bool {
	q := store.New(o.svc.pool)
	intents, err := q.ListUnsettledPublishIntents(ctx, runID)
	if err != nil {
		o.log.Error("orchestrator: unsettled publications", "run", runID, "error", err)
		return false
	}
	if len(intents) == 0 {
		return true
	}
	// Quiescence, once for the run: any phase still 'running' may still be
	// inside a forge call, and probing under it would race the effect.
	phases, err := q.ListPhasesForRun(ctx, runID)
	if err != nil {
		o.log.Error("orchestrator: settle publications phases", "run", runID, "error", err)
		return false
	}
	for _, p := range phases {
		if PhaseStatus(p.Status) == PhaseRunning {
			return false // not fenced yet; settleRunCredentials gets it next tick
		}
	}

	settled := true
	for _, intent := range intents {
		ok, err := o.settlePublication(ctx, q, intent)
		if err != nil {
			// Never guess. A forge we cannot reach leaves the run matched by
			// the expired-run predicate, and the next tick asks again.
			o.log.Error("orchestrator: settle publication", "intent", intent.ID, "error", err)
			settled = false
			continue
		}
		if !ok {
			settled = false
		}
	}
	return settled
}

// settlePublication gives ONE intent its final answer, or reports that it does
// not have one yet. It probes the marker its status makes uncertain, and it
// never causes anything.
func (o *Orchestrator) settlePublication(ctx context.Context, q *store.Queries,
	intent store.PublishIntent,
) (bool, error) {
	switch publishStatus(intent.Status) {
	case publishPending:
		return o.settleUnpushedPublication(ctx, q, intent)
	case publishPushed:
		return o.settleUnopenedPublication(ctx, q, intent)
	}
	return true, nil // 'published' and 'halted' are settled by definition
}

// settleUnpushedPublication answers "did the push land?" for an intent with no
// push receipt.
func (o *Orchestrator) settleUnpushedPublication(ctx context.Context, q *store.Queries,
	intent store.PublishIntent,
) (bool, error) {
	head, exists, err := o.svc.forge.BranchHead(ctx, intentRepo(intent), intent.Branch)
	if err != nil {
		return false, err
	}
	if !exists {
		return o.recordStableAbsence(ctx, q, intent,
			"expired before publishing: no branch on the forge after the settle grace")
	}
	if _, err := q.ClearPublicationAbsent(ctx, intent.ID); err != nil {
		return false, err
	}
	// A branch exists and there is no receipt to compare it against, so
	// ownership is proved the only way it can be: REBUILD the commit this
	// authorization produces and check the remote head against it (decision
	// 18 — an observed sha is evidence, never permission). The commit date is
	// pinned to the intent's created_at, which is what makes the rebuild
	// reproduce the exact sha.
	req, err := o.pushRequestFor(ctx, intent)
	if err != nil {
		return false, err
	}
	want, err := o.svc.publisher.ExpectedHead(ctx, req)
	if err != nil {
		return false, err
	}
	if want != head {
		return o.halt(ctx, q, intent, fmt.Sprintf(
			"branch %s exists at %s, which this authorization did not produce", intent.Branch, head))
	}
	// Ours, and it landed. Write the receipt the dying attempt never got to.
	n, err := q.RecordPushReceipt(ctx, store.RecordPushReceiptParams{
		ID: intent.ID, HeadSha: &head, UpdatedAt: o.svc.clock.Now(),
	})
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil // a live driver beat us to it; re-read next tick
	}
	// The intent is now 'pushed' and its pull request is the next question.
	// Deferring one tick keeps this function to one effect-shaped answer at a
	// time, and the run is already staying alive for the credential sweep.
	return false, nil
}

// settleUnopenedPublication answers "did the pull request land?" for an intent
// whose push receipt is recorded.
func (o *Orchestrator) settleUnopenedPublication(ctx context.Context, q *store.Queries,
	intent store.PublishIntent,
) (bool, error) {
	candidates, err := o.svc.forge.FindPullRequests(ctx, intentRepo(intent), intent.Branch)
	if err != nil {
		return false, err
	}
	pr, found, ambiguous := matchPublication(candidates, intent)
	switch {
	case found:
		if _, err := q.ClearPublicationAbsent(ctx, intent.ID); err != nil {
			return false, err
		}
		n, err := q.RecordPRReceipt(ctx, store.RecordPRReceiptParams{
			ID: intent.ID, PrNumber: &pr.Number, PrUrl: &pr.URL, UpdatedAt: o.svc.clock.Now(),
		})
		if err != nil {
			return false, err
		}
		return n == 1, nil
	case ambiguous || len(candidates) > 0:
		if _, err := q.ClearPublicationAbsent(ctx, intent.ID); err != nil {
			return false, err
		}
		return o.halt(ctx, q, intent, fmt.Sprintf(
			"branch %s carries %d pull request(s) and none is provably this publication's",
			intent.Branch, len(candidates)))
	default:
		// No pull request visible. This is the window the branch-only version
		// of this function missed entirely: the create call may be on the
		// wire right now, so absence gets the same grace the branch does.
		return o.recordStableAbsence(ctx, q, intent,
			"expired after pushing: no pull request on the forge after the settle grace")
	}
}

// recordStableAbsence implements condition 2 for whichever marker the caller
// found missing: stamp the first sighting and defer, halt once the grace has
// elapsed. It returns false until absence is an answer.
func (o *Orchestrator) recordStableAbsence(ctx context.Context, q *store.Queries,
	intent store.PublishIntent, reason string,
) (bool, error) {
	n, err := q.HaltStablyAbsentPublication(ctx, store.HaltStablyAbsentPublicationParams{
		ID: intent.ID, HaltReason: &reason, Status: intent.Status,
		GraceSeconds: int32(publishSettleGrace / time.Second),
	})
	if err != nil {
		return false, err
	}
	if n == 1 {
		return true, nil // absence held for the whole grace; the intent is settled
	}
	// Either this is the first sighting, or the grace has not elapsed. Stamp
	// it if unstamped (the statement is a no-op otherwise) and defer.
	if _, err := q.MarkPublicationAbsent(ctx, store.MarkPublicationAbsentParams{
		ID: intent.ID, Status: intent.Status,
	}); err != nil {
		return false, err
	}
	return false, nil
}
```

`halt` is the settlement-side halt: the same row write as `haltPublication`
without the attempt-scoped event and audit correlation, which a dead run has
no `claimed` to supply. It emits `EventPublishHalted` with the run id alone.

`pushRequestFor` reconstructs the request the dead attempt would have made,
entirely from durable rows — there is no `claimed` and no live `cachedPlan`
argument to borrow:

```go
// pushRequestFor rebuilds the PushRequest this intent's authorization implies.
// ExpectedHead's answer is only worth anything if every input that reaches the
// commit is identical to what PushPatch used, so each field is sourced from
// the same durable row the live path read it from.
func (o *Orchestrator) pushRequestFor(ctx context.Context, intent store.PublishIntent) (PushRequest, error) {
	q := store.New(o.svc.pool)
	row, err := q.GetAuthorizationByID(ctx, intent.AuthorizationID)
	if err != nil {
		return PushRequest{}, err
	}
	// The read returns store.GetAuthorizationByIDRow; commitMessageFor takes the
	// `authorization` alias Task 9 Step 8 declares, and it must keep taking it —
	// loosening the parameter type would let the two renderers diverge, and a
	// rebuilt commit message that drifts changes the sha and fails every
	// ownership check open.
	auth := authorizationFromIDRow(row)
	art, err := q.GetArtifactByID(ctx, auth.ArtifactID)
	if err != nil {
		return PushRequest{}, err
	}
	diff, ok := artifactDiff(art)
	if !ok {
		return PushRequest{}, fmt.Errorf("runs: approved artifact %s carries no diff", auth.ArtifactID)
	}
	cp, err := o.loadPlan(ctx, intent.RunID)
	if err != nil {
		return PushRequest{}, err
	}
	ref := AttemptRef{RunID: intent.RunID, NodeID: intent.NodeID, Attempt: intent.Attempt}
	return PushRequest{
		Ref: ref, Repo: intentRepo(intent), Branch: intent.Branch, BaseSHA: intent.BaseSha,
		TreeHash: intent.TreeHash, Patch: diff,
		Message:    commitMessageFor(intent, auth, ref),
		AuthorName: emailLocalPart(cp.requesterEmail), AuthorEmail: cp.requesterEmail,
		Date:       intent.CreatedAt,
		// NOT the run's wall deadline, which is by definition already in the
		// past here — passing it would abort the rebuild before it started and
		// turn "I could not check" into a permanent-looking failure on the one
		// path with no later corrector. This read touches no ref, so it gets
		// its own small budget.
		Deadline: o.svc.clock.Now().Add(publishSettleProbeBudget),
	}, nil
}
```

Two consequences of that reconstruction, both worth being explicit about:

- **`commitMessage` must not depend on `claimed` or `cachedPlan`.** Task 9
  writes it as a method taking both; every field it actually interpolates comes
  from the intent, the authorization, and the ref. Define it in Task 9 as the
  free function `commitMessageFor(intent, auth, ref)` and have the live path
  call that too — one renderer, or the rebuilt commit message can silently
  drift from the pushed one and every ownership check fails open.
- **`publishSettleProbeBudget`** is a second constant beside
  `publishSettleGrace` (start at 2 minutes: a shallow clone plus an apply).
  It bounds the rebuild, not the wait.

That rebuild is a port call — `runs` may not import `gitops` — and the port
already carries it: `GitPublisher.ExpectedHead` was declared with `PushPatch`
back in **Task 9**, precisely so this task has no port change to make and the
two halves cannot drift apart. Nothing to add here; only to call.

A publication whose intent reached `pushed` already has its receipt, so settlement compares against `head_sha` directly and never pays for the rebuild; only a `pending` intent with a branch on the remote needs it, which is the rare case this whole path exists for.

Its loop reads `ListUnsettledPublishIntents`, and the "is it ours" comparison is the same one `pushPublication` makes, so ownership has exactly one definition:

```sql
-- name: ListUnsettledPublishIntents :many
-- Intents that may correspond to an effect on the forge nobody has written
-- down. 'published' and 'halted' are settled by definition.
SELECT * FROM publish_intents
WHERE run_id = $1 AND status IN ('pending', 'pushed');
```

- [ ] **Step 6: Regenerate the store package**

Run: `task db:sqlc`
Expected: regenerated store package compiles; git diff shows only the new methods (`MarkPublicationAbsent`, `ClearPublicationAbsent`, `HaltStablyAbsentPublication`, `ListUnsettledPublishIntents`).

- [ ] **Step 7: Write the deadline-orphan tests**

```go
func TestExpiredRun_APushThatLandedIsRecordedBeforeTheRunDies(t *testing.T) {
	// The push lands; the deadline expires before its receipt commits. The
	// run must still die — the deadline is not negotiable — but it must not
	// die pretending nothing was pushed.
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	h.publisher.HangAfterPush(true) // push lands, receipt never commits
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	h.waitForPhaseStatus(t, run.ID, "publish", runs.PhaseRunning, 30*time.Second)
	h.backdateWallDeadline(t, run.ID)

	h.waitForRunStatus(t, run.ID, runs.RunDead, 60*time.Second)

	intent := h.intent(t, run.ID, "publish")
	if intent.HeadSha == nil {
		t.Fatal("a branch exists on the remote and the record does not mention it: the publication is orphaned")
	}
	if got := h.remoteBranchHead(t, "publish-marker", intent.Branch); got != *intent.HeadSha {
		t.Errorf("recorded head %q does not match the remote's %q", *intent.HeadSha, got)
	}
	if intent.Status == "pending" {
		t.Error("the intent must reach a settled state, not stay in-flight forever")
	}
}

func TestExpiredRun_APublicationThatNeverLandedIsHaltedNotLeftPending(t *testing.T) {
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	h.publisher.HangBeforePush(true)
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	h.waitForIntentStatus(t, run.ID, "publish", "pending", 30*time.Second)
	h.backdateWallDeadline(t, run.ID)

	h.waitForRunStatus(t, run.ID, runs.RunDead, 60*time.Second)
	intent := h.intent(t, run.ID, "publish")
	if intent.Status != "halted" || intent.HaltReason == nil {
		t.Fatalf("an intent that never published must be settled with a reason: %+v", intent)
	}
	if h.publisher.PushCount() != 0 {
		t.Error("an expired run must not push; settlement only asks what already exists")
	}
}

func TestExpiredRun_AnEffectLandingAfterANegativeProbeIsStillRecorded(t *testing.T) {
	// The negative case's own race, and the one a single probe gets wrong.
	// The push is issued just before the deadline and lands just after
	// settlement first looks. If "absent" settled the intent on sight, the
	// run would be dead and superseded by the time the branch appeared —
	// an orphan on the forge that nothing ever examines again.
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	// The publisher holds the push on the wire: the remote has not seen it
	// when settlement probes, and receives it afterwards.
	release := h.publisher.DelayPushDelivery(t)
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	h.waitForIntentStatus(t, run.ID, "publish", "pending", 30*time.Second)
	h.backdateWallDeadline(t, run.ID)

	// Settlement looks and finds nothing. It must NOT settle on that.
	h.waitForForgeBranchProbe(t, 30*time.Second)
	if got := h.intent(t, run.ID, "publish").Status; got == "halted" {
		t.Fatal("one absent probe settled the intent; a push still on the wire is now an orphan")
	}
	if got := h.run(t, run.ID).Status; got == runs.RunDead {
		t.Fatal("the run died on a single negative probe")
	}

	release() // the push lands, late
	h.waitForRunStatus(t, run.ID, runs.RunDead, 90*time.Second)

	intent := h.intent(t, run.ID, "publish")
	if intent.HeadSha == nil {
		t.Fatal("the late push must be recorded, not orphaned")
	}
	if got := h.remoteBranchHead(t, "publish-marker", intent.Branch); got != *intent.HeadSha {
		t.Errorf("recorded head %q does not match the remote's %q", *intent.HeadSha, got)
	}
}

func TestExpiredRun_AnUnreachableForgeDefersTerminalizationRatherThanGuessing(t *testing.T) {
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	h.publisher.HangAfterPush(true)
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	h.waitForPhaseStatus(t, run.ID, "publish", runs.PhaseRunning, 30*time.Second)
	h.forge.FailBranchProbes(true)
	h.backdateWallDeadline(t, run.ID)

	// The run is expired but NOT terminalized: settlement could not get an
	// answer, and a wrong durable answer is worse than a late one.
	time.Sleep(4 * h.pollInterval)
	if got := h.run(t, run.ID).Status; got == runs.RunDead {
		t.Fatal("the run died while its publication was unknowable; the orphan is now permanent")
	}

	h.forge.FailBranchProbes(false)
	h.waitForRunStatus(t, run.ID, runs.RunDead, 60*time.Second)
	if h.intent(t, run.ID, "publish").HeadSha == nil {
		t.Error("once the forge answered, the landed push must be recorded")
	}
}

func TestExpiredRun_APullRequestLandingAfterANegativeProbeIsStillRecorded(t *testing.T) {
	// The SAME race as the branch case, one step later — and the one a
	// branch-only stable negative misses completely. The intent is already
	// 'pushed', so its branch is present and provably ours; what is in flight
	// at the deadline is the CREATE PULL REQUEST call. Settlement looks, sees
	// no request, and must not conclude anything: halting here orphans the
	// request that is about to appear, and it will carry the run's work with
	// nobody watching it.
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	// The push completes and records; the pull request is held on the wire.
	release := h.forge.DelayPullRequestDelivery(t)
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	h.waitForIntentStatus(t, run.ID, "publish", "pushed", 30*time.Second)
	h.backdateWallDeadline(t, run.ID)

	h.waitForForgePRProbe(t, 30*time.Second)
	if got := h.intent(t, run.ID, "publish").Status; got == "halted" {
		t.Fatal("one absent PR probe settled the intent; the request on the wire is now an orphan")
	}

	release() // the pull request lands, late
	h.waitForRunStatus(t, run.ID, runs.RunDead, 90*time.Second)

	intent := h.intent(t, run.ID, "publish")
	if intent.PrNumber == nil {
		t.Fatalf("the late pull request must be recorded, not orphaned: %+v", intent)
	}
	if intent.Status != "published" {
		t.Errorf("status = %q; an intent whose PR is recorded is published", intent.Status)
	}
}

func TestExpiredRun_APublicationThatNeverOpenedAPullRequestStillSettles(t *testing.T) {
	// The other half of the same rule: deferring whenever no request is
	// visible would strand every run that genuinely died between its push and
	// its create call. Absence must still settle — after the grace, not on
	// sight.
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	h.forge.HangBeforePullRequest(true) // the create call is never issued
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	h.waitForIntentStatus(t, run.ID, "publish", "pushed", 30*time.Second)
	h.backdateWallDeadline(t, run.ID)

	h.waitForRunStatus(t, run.ID, runs.RunDead, 90*time.Second)
	intent := h.intent(t, run.ID, "publish")
	if intent.Status != "halted" || intent.HaltReason == nil {
		t.Fatalf("a publication that never opened a request must be halted with a reason: %+v", intent)
	}
	if intent.HeadSha == nil {
		t.Error("halting the PR side must not discard the push receipt that did land")
	}
}

func TestExpiredRun_SettlementRunsWhileItsPhasesAreReconciling(t *testing.T) {
	// A regression guard for the quiescence definition. settleRunCredentials
	// runs first and leaves every fenced phase in 'reconciling', which is NOT
	// terminal. A quiescence test spelled "no live phase row" would therefore
	// never pass on exactly the runs this path exists for: settlement would
	// defer forever and the run would never die. Quiescence means FENCED —
	// nothing still 'running'.
	h := newSpineHarness(t)
	approver := h.newUser(t, "approver@example.com", authn.GrantApprove)
	_, run := h.createTask(t, conveyorWorkflow(t, h))
	h.waitForRunStatus(t, run.ID, runs.RunWaiting, 20*time.Second)

	h.publisher.HangAfterPush(true)
	if _, err := h.svc.DecideGate(h.ctx, approver, runs.GateDecisionInput{
		RunID: run.ID, NodeID: "gate", Decision: runs.DecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
	h.waitForPhaseStatus(t, run.ID, "publish", runs.PhaseRunning, 30*time.Second)
	h.backdateWallDeadline(t, run.ID)

	// The phase is fenced to 'reconciling' and stays there. The run must
	// still reach a terminal state rather than deferring on every tick.
	h.waitForPhaseStatus(t, run.ID, "publish", runs.PhaseReconciling, 30*time.Second)
	h.waitForRunStatus(t, run.ID, runs.RunDead, 60*time.Second)
	if h.intent(t, run.ID, "publish").HeadSha == nil {
		t.Error("the landed push must be recorded even though its phase was mid-reconcile")
	}
}
```

Six fake additions carry these tests, and none of them is inherited — Task 9 gave `fake.Forge` only `CrashAfterPR`, `FailPRTimes`, `FailBranchProbes` and the counters, and gave `fake.Publisher` the three push injectors including `DelayPushDelivery`. **This task** adds the branch-side pair `forge.DelayBranchDelivery` and the `h.waitForForgeBranchProbe` helper, and mirrors them one step later with `forge.DelayPullRequestDelivery`, `forge.HangBeforePullRequest` and `h.waitForForgePRProbe`.

Run: `task backend:test:integration -- -run 'TestReconcile|TestExpiredRun' -race -v`
Expected: PASS.

- [ ] **Step 8: Run the reconciliation tests**

Run: `task backend:test:integration -- -run TestReconcile -race -v`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add backend/internal/runs/orchestrator.go backend/internal/runs/publish.go \
        backend/internal/runs/outcome.go backend/internal/runs/outcome_test.go \
        backend/internal/runs/retry.go backend/internal/runs/store/ \
        backend/internal/runs/fake/ backend/internal/runs/publish_integration_test.go
git commit -m "feat(runs): reconcile a publication against its intent and the forge instead of the executor"
```

---

### Task 11: The HTTP surface — decisions, the approvals inbox, and the projections

**Files:**
- Modify: `backend/internal/runs/api/api.go` (`Operations` :37-75), `backend/internal/runs/api/dto.go`
- Regenerate: `backend/api/openapi.json`, `web/src/api/schema.d.ts`
- Test: `backend/internal/runs/api/api_test.go`, `backend/internal/app/api_e2e_integration_test.go`

**Interfaces:**
- Produces:
  - `POST /api/v1/runs/{id}/gates/{node}/decision` → `GateDecisionResponse` (200)
  - `GET /api/v1/approvals` → `ApprovalListResponse` (200)
  - `RunResponse.Generation`, `PhaseResponse.Generation` / `.Decision`, `ArtifactResponse.Generation`, `RunResponse.Publications []PublicationResponse`

- [ ] **Step 1: Write the failing API tests**

In `backend/internal/app/api_e2e_integration_test.go` (the end-to-end surface, so authorization is exercised through the real middleware):

```go
func TestAPI_GateDecisionRequiresApproveGrant(t *testing.T) {
	env := newAPIEnv(t)
	runner := env.login(t, "runner@example.com")   // run grant
	approver := env.login(t, "approver@example.com") // approve grant
	runID := env.driveToGate(t, runner)

	body := runsapi.GateDecisionRequest{Decision: "approve"}
	if got := runner.do(t, http.MethodPost, "/api/v1/runs/"+runID+"/gates/gate/decision", body, nil); got != http.StatusForbidden {
		t.Fatalf("run grant decided a gate: status %d, want 403", got)
	}
	var out runsapi.GateDecisionResponse
	if got := approver.do(t, http.MethodPost, "/api/v1/runs/"+runID+"/gates/gate/decision", body, &out); got != http.StatusOK {
		t.Fatalf("approve grant status %d, want 200", got)
	}
	if out.Decision != "approve" || out.AuthorizationID == "" {
		t.Fatalf("response = %+v", out)
	}
}

func TestAPI_GateDecisionOnAnUnknownRunIs404AndOnADecidedGateIs409(t *testing.T) {
	env := newAPIEnv(t)
	approver := env.login(t, "approver@example.com")
	runID := env.driveToGate(t, approver)

	if got := approver.do(t, http.MethodPost, "/api/v1/runs/"+uuid.NewString()+"/gates/gate/decision",
		runsapi.GateDecisionRequest{Decision: "approve"}, nil); got != http.StatusNotFound {
		t.Errorf("unknown run status %d, want 404", got)
	}
	if got := approver.do(t, http.MethodPost, "/api/v1/runs/"+runID+"/gates/nosuchnode/decision",
		runsapi.GateDecisionRequest{Decision: "approve"}, nil); got != http.StatusConflict {
		t.Errorf("unknown node status %d, want 409 (the node is not a gate awaiting a decision)", got)
	}
	if got := approver.do(t, http.MethodPost, "/api/v1/runs/"+runID+"/gates/gate/decision",
		runsapi.GateDecisionRequest{Decision: "approve"}, nil); got != http.StatusOK {
		t.Fatal("first decision must win")
	}
	if got := approver.do(t, http.MethodPost, "/api/v1/runs/"+runID+"/gates/gate/decision",
		runsapi.GateDecisionRequest{Decision: "reject"}, nil); got != http.StatusConflict {
		t.Errorf("second decision status %d, want 409", got)
	}
}

func TestAPI_GateDecisionValidatesItsBody(t *testing.T) {
	env := newAPIEnv(t)
	approver := env.login(t, "approver@example.com")
	runID := env.driveToGate(t, approver)

	for _, c := range []struct{ name string; body runsapi.GateDecisionRequest }{
		{"unknown decision", runsapi.GateDecisionRequest{Decision: "maybe"}},
		{"request_changes without a note", runsapi.GateDecisionRequest{Decision: "request_changes"}},
		{"oversize note", runsapi.GateDecisionRequest{Decision: "approve", Note: strings.Repeat("x", 9000)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var prob httpx.ProblemDocument
			if got := approver.do(t, http.MethodPost, "/api/v1/runs/"+runID+"/gates/gate/decision", c.body, &prob); got != http.StatusBadRequest {
				t.Fatalf("status %d, want 400", got)
			}
			if len(prob.Errors) == 0 {
				t.Error("a validation failure must name its field")
			}
		})
	}
}

func TestAPI_ApprovalsInboxIsScopedToGrants(t *testing.T) {
	env := newAPIEnv(t)
	inA := env.login(t, "a@example.com")     // approve on project A only
	inB := env.login(t, "b@example.com")     // approve on project B only
	adminC := env.loginAdmin(t, "admin@example.com")
	runA := env.driveToGateOnProject(t, inA, "A")

	var listA runsapi.ApprovalListResponse
	inA.do(t, http.MethodGet, "/api/v1/approvals", nil, &listA)
	if len(listA.Approvals) != 1 || listA.Approvals[0].RunID != runA {
		t.Fatalf("A's inbox = %+v", listA.Approvals)
	}
	if listA.Approvals[0].NodeID != "gate" || listA.Approvals[0].TaskTitle == "" {
		t.Errorf("an inbox entry must be actionable on its own: %+v", listA.Approvals[0])
	}

	var listB runsapi.ApprovalListResponse
	inB.do(t, http.MethodGet, "/api/v1/approvals", nil, &listB)
	if len(listB.Approvals) != 0 {
		t.Fatalf("B sees A's gate: %+v", listB.Approvals)
	}

	var listAdmin runsapi.ApprovalListResponse
	adminC.do(t, http.MethodGet, "/api/v1/approvals", nil, &listAdmin)
	if len(listAdmin.Approvals) != 1 {
		t.Fatalf("admin inbox = %+v", listAdmin.Approvals)
	}
}

func TestAPI_RunProjectionCarriesGenerationsDecisionsAndPublications(t *testing.T) {
	env := newAPIEnv(t)
	approver := env.login(t, "approver@example.com")
	runID := env.driveToGate(t, approver)
	approver.do(t, http.MethodPost, "/api/v1/runs/"+runID+"/gates/gate/decision",
		runsapi.GateDecisionRequest{Decision: "request_changes", Note: "again"}, nil)
	env.waitForGate(t, runID)
	approver.do(t, http.MethodPost, "/api/v1/runs/"+runID+"/gates/gate/decision",
		runsapi.GateDecisionRequest{Decision: "approve"}, nil)
	env.waitForRunStatus(t, runID, "succeeded")

	var run runsapi.RunResponse
	approver.do(t, http.MethodGet, "/api/v1/runs/"+runID, nil, &run)
	if run.Generation != 2 {
		t.Errorf("generation = %d, want 2", run.Generation)
	}
	var decided int
	for _, p := range run.Phases {
		if p.Decision != nil {
			decided++
			if p.Decision.ActedBy == "" || p.Decision.ExaminedArtifactID == "" {
				t.Errorf("a decided phase must carry who and what: %+v", p.Decision)
			}
		}
	}
	if decided != 2 {
		t.Errorf("decided gate phases = %d, want 2 (one per generation)", decided)
	}
	if len(run.Publications) != 1 {
		t.Fatalf("publications = %d, want 1", len(run.Publications))
	}
	pub := run.Publications[0]
	if pub.Status != "published" || pub.PRURL == "" || pub.Branch == "" {
		t.Errorf("publication = %+v", pub)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `task backend:test:integration -- -run 'TestAPI_Gate|TestAPI_Approvals|TestAPI_RunProjection' -v`
Expected: FAIL — 404 on the routes.

- [ ] **Step 3: Add the DTOs**

In `dto.go`:

```go
type GateDecisionRequest struct {
	Decision string `json:"decision"` // approve | request_changes | reject
	Note     string `json:"note,omitempty"`
}

type GateDecisionResponse struct {
	DecisionID string `json:"decision_id"`
	Decision   string `json:"decision"`
	RunStatus  string `json:"run_status"`
	Generation int32  `json:"generation"`
	// AuthorizationID is empty when the gate authorizes no publish node.
	AuthorizationID string `json:"authorization_id,omitempty"`
}

// ApprovalResponse is one attention item. It carries everything a list view
// renders so the inbox is one request, not one plus N.
type ApprovalResponse struct {
	RunID        string    `json:"run_id"`
	TaskID       string    `json:"task_id"`
	ProjectID    string    `json:"project_id"`
	TaskTitle    string    `json:"task_title"`
	NodeID       string    `json:"node_id"`
	Attempt      int32     `json:"attempt"`
	Generation   int32     `json:"generation"`
	RequesterID  string    `json:"requester_id"`
	WaitingSince time.Time `json:"waiting_since"`
}

type ApprovalListResponse struct {
	Approvals []ApprovalResponse `json:"approvals"`
}

// GateDecisionSummary projects a decided gate onto its phase.
type GateDecisionSummary struct {
	Decision           string    `json:"decision"`
	Note               string    `json:"note"`
	ActedBy            string    `json:"acted_by"`
	ActedAt            time.Time `json:"acted_at"`
	SelfApproval       bool      `json:"self_approval"`
	ExaminedArtifactID string    `json:"examined_artifact_id"`
}

type PublicationResponse struct {
	NodeID       string `json:"node_id"`
	Status       string `json:"status"`
	Branch       string `json:"branch"`
	TargetBranch string `json:"target_branch"`
	BaseSHA      string `json:"base_sha"`
	HeadSHA      string `json:"head_sha,omitempty"`
	PRNumber     int64  `json:"pr_number,omitempty"`
	PRURL        string `json:"pr_url,omitempty"`
	HaltReason   string `json:"halt_reason,omitempty"`
}
```

`RunResponse` gains `Generation int32` and `Publications []PublicationResponse`; `PhaseResponse` gains `Generation int32` and `Decision *GateDecisionSummary`; `ArtifactResponse` gains `Generation int32`.

- [ ] **Step 4: Add the operations**

```go
		{
			Pattern: "POST /api/v1/runs/{id}/gates/{node}/decision", ID: "decideGate",
			Summary: "Approve, request changes on, or reject a waiting gate (approve grant)",
			Request: GateDecisionRequest{}, Response: GateDecisionResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleDecideGate),
		},
		{
			Pattern: "GET /api/v1/approvals", ID: "listApprovals",
			Summary:  "List gates awaiting a decision, scoped to the caller's grants (view)",
			Response: ApprovalListResponse{}, Status: http.StatusOK,
			Handler: a.authenticated(a.handleListApprovals),
		},
```

Two things about the pattern to be deliberate about:
- `{node}` is a wildcard segment, and node ids match `^[a-z][a-z0-9_-]{0,63}$` — no slashes — so a single segment always suffices. Read it with `r.PathValue("node")` and refuse an empty value with a 404 rather than passing it through.
- The route sits under `/runs/{id}` so `runScoped`'s existing 404-before-403 semantics apply unchanged: an id nobody may see is a 404 for everybody.

- [ ] **Step 5: Write the handlers**

```go
func (a *API) handleDecideGate(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	req, err := decode[GateDecisionRequest](r)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	runID, err := pathUUID(r, "no such run")
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	node := r.PathValue("node")
	if node == "" {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassNotFound, "no such gate"))
		return
	}
	// The project comes from the run so the grant check precedes any write,
	// and an unknown run is 404 before it is 403 — the peer contexts'
	// semantics.
	projectID, err := a.svc.GateProject(r.Context(), runID)
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	if !a.authorize(w, r, sess, projectID, authn.GrantApprove) {
		return
	}
	res, err := a.svc.DecideGate(r.Context(), sess.Principal, runs.GateDecisionInput{
		RunID: runID, NodeID: node, Decision: runs.Decision(req.Decision), Note: req.Note,
	})
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := GateDecisionResponse{
		DecisionID: res.DecisionID.String(), Decision: req.Decision,
		RunStatus: string(res.RunStatus), Generation: res.Generation,
	}
	if res.AuthorizationID != nil {
		out.AuthorizationID = res.AuthorizationID.String()
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) handleListApprovals(w http.ResponseWriter, r *http.Request, sess authn.AuthSession) {
	var (
		list []runs.WaitingGate
		err  error
	)
	if sess.Role == authn.RoleAdmin {
		list, err = a.svc.ListWaitingGates(r.Context(), nil, true)
	} else {
		var grants []authn.Grant
		grants, err = a.grants.GrantsForUser(r.Context(), sess.UserID)
		if err == nil {
			// The inbox shows what the caller can SEE; the decision endpoint
			// is what enforces approve. A view-only member seeing that a gate
			// is pending is information they already have from the run.
			ids := make([]uuid.UUID, 0, len(grants))
			for _, g := range grants {
				ids = append(ids, g.ProjectID)
			}
			list, err = a.svc.ListWaitingGates(r.Context(), ids, false)
		}
	}
	if err != nil {
		httpx.Problem(w, r, err)
		return
	}
	out := ApprovalListResponse{Approvals: make([]ApprovalResponse, 0, len(list))}
	for _, g := range list {
		out.Approvals = append(out.Approvals, approvalResponse(g))
	}
	httpx.JSON(w, http.StatusOK, out)
}
```

- [ ] **Step 6: Extend `GetRun`'s projection**

`Service.GetRun` returns `(Run, []Phase, []ArtifactMeta, error)`. Adding two more return values makes an unreadable signature; introduce a struct instead and update the three call sites:

```go
// RunDetail is the single-run read: everything the run page renders in one
// query set. It replaces GetRun's four return values, which had run out of
// room.
type RunDetail struct {
	Run          Run
	Phases       []Phase
	Artifacts    []ArtifactMeta
	Publications []Publication
}

type Publication struct {
	NodeID       string
	Status       string
	Branch       string
	TargetBranch string
	BaseSHA      string
	HeadSHA      string
	PRNumber     int64
	PRURL        string
	HaltReason   string
}
```

`Phase` gains `Generation int32` and `Decision *GateDecision`; `ArtifactMeta` gains `Generation int32`. `GateDecision` is the domain twin of `GateDecisionSummary`. The decisions come from one query joined onto the phase rows:

```sql
-- name: ListGateDecisionsForRun :many
SELECT gd.*, u.email AS acted_by_email
FROM gate_decisions gd
JOIN users u ON u.id = gd.acted_by
WHERE gd.run_id = $1
ORDER BY gd.acted_at;

-- name: ListPublishIntentsForRun :many
SELECT * FROM publish_intents WHERE run_id = $1 ORDER BY created_at;
```

- [ ] **Step 7: Regenerate the contracts**

```bash
task contracts:gen
task contracts:check
```

Expected: `openapi.json` gains `decideGate` and `listApprovals`, `web/src/api/schema.d.ts` regenerates, and the check is green because both are committed.

Also document the real error statuses on the new operations. The Plan 02 carry-forward notes that only 400 is documented among error statuses; do not fix that globally here, but **do** declare `409` on `decideGate` — it is the endpoint's most common non-success answer, and a client that cannot see it in the contract will treat a legitimate conflict as a bug.

- [ ] **Step 8: Run the API tests**

Run: `task backend:test:integration -- -run 'TestAPI_' -race -v && go test ./internal/runs/api/ -race`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add backend/internal/runs/api/ backend/api/openapi.json web/src/api/schema.d.ts \
        backend/internal/runs/tasks.go backend/internal/runs/gates.go backend/internal/runs/store/ \
        backend/internal/app/api_e2e_integration_test.go
git commit -m "feat(api): expose gate decisions, the approvals inbox and publication projections"
```

---

### Task 12: App wiring — the real forge, and production intake stops being refused

**Files:**
- Modify: `backend/internal/app/runsports.go` (`unavailableForge` :105-112 **deleted**; the real `forgeAdapter` takes its place)
- Modify: `backend/internal/app/runnerports.go` (`cloneURL` :234-245 rewritten to read the configured clone bases)
- Modify: `backend/internal/app/serve.go` (:101-130)
- Modify: `backend/internal/platform/config/config.go` (a `Forge` block)
- Modify: `backend/internal/runs/service.go`, `orchestrator.go` (constructor growth)
- Test: `backend/internal/app/runsports_test.go`, `backend/internal/app/runnerports_test.go`, `backend/internal/app/serve_integration_test.go`

**Interfaces:**
- Produces:
  - `app.forgeAdapter` implementing `runs.Forge` over `platform/forge` + `assets.UseCredential`
  - `app.gitPublisher` grows its `endpoints forgeEndpoints` field and the configured work dir (Task 9 landed the type itself)
  - `config.Forge{GitHubBaseURL, GitLabBaseURL, GitHubCloneBaseURL, GitLabCloneBaseURL string; PublishWorkDir string}`
  - `app.cloneURL` rewritten to read the configured clone bases (it currently hardcodes the public hosts and refuses everything else with "self-hosted base URLs arrive in Plan 08")

`app.principalSource` is deliberately **not** on that list. Task 9 wired it, because Task 9 is where `runs.NewService` grew the dependency and the tree had to compile at the end of the task. This task's remaining job is exactly what its title says: swapping `unavailableForge` for the real forge, and giving the publisher its configured endpoints.

- [ ] **Step 1: Write the failing wiring tests**

```go
func TestForgeAdapter_OpensTheCredentialThroughTheAuditedPath(t *testing.T) {
	// spec §6: every credential use is audited. A forge call that read the
	// secret with GetCredential would publish with an unaudited token.
	env := newAssetsEnv(t)
	repo := env.repo(t, "github", "acme/widgets")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"sha":"deadbeef"}`)
	}))
	defer srv.Close()

	fa := newForgeAdapter(env.assets, forgeEndpoints{GitHub: srv.URL, GitLab: srv.URL}, srv.Client())
	sha, err := fa.ResolveHead(env.ctx, repo)
	if err != nil || sha != "deadbeef" {
		t.Fatalf("ResolveHead = %q, %v", sha, err)
	}
	if n := env.countAudit(t, "credential.used"); n != 1 {
		t.Fatalf("credential.used audit entries = %d, want 1", n)
	}
}

func TestForgeAdapter_RefusesAnUnsupportedForgeBeforeOpeningACredential(t *testing.T) {
	env := newAssetsEnv(t)
	repo := env.repo(t, "github", "acme/widgets")
	repo.Forge = "bitbucket"
	fa := newForgeAdapter(env.assets, forgeEndpoints{}, http.DefaultClient)
	if _, err := fa.ResolveHead(env.ctx, repo); err == nil {
		t.Fatal("an unsupported forge must fail")
	}
	if n := env.countAudit(t, "credential.used"); n != 0 {
		t.Fatal("an unsupported forge must fail WITHOUT an audited secret use")
	}
}

func TestForgeAdapter_ClassifiesRetryabilityOntoTypedOutcomes(t *testing.T) {
	env := newAssetsEnv(t)
	repo := env.repo(t, "github", "acme/widgets")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	fa := newForgeAdapter(env.assets, forgeEndpoints{GitHub: srv.URL}, srv.Client())
	_, err := fa.OpenPullRequest(env.ctx, repo, runs.PullRequestSpec{Head: "h", Base: "main"})
	out := runs.ClassifyOutcome(err)
	if out.Class != runs.OutcomeTransient || out.Code != runs.CodeForgeUnavailable {
		t.Fatalf("outcome = %+v, want a transient forge_unavailable", out)
	}
}
```

There is deliberately **no** test asserting that `unavailableForge` is gone. Reading `runsports.go` and grepping it for the identifier is a test of the source text rather than of behaviour: it breaks when the file is renamed and misses the type moving to a neighbour. The compiler is the stronger check — Step 4 deletes the type, so any surviving reference is a build failure — and `serve_integration_test.go`'s existing boot test proves the binary still starts. Verification item 4 carries the requirement.

- [ ] **Step 2: Add the config block**

```go
// Forge is how caesar reaches its code hosts. Base URLs are configurable so
// self-hosted GitHub Enterprise and GitLab instances work without a code
// change; the defaults are the public endpoints.
//
// There are TWO endpoints per forge and they are genuinely different hosts,
// not one value used twice. GitHub's API lives at api.github.com while its
// repositories live at github.com; a GitHub Enterprise install typically
// serves both from one host under different paths. Configuring only the API
// base is the trap: every API call would reach the self-hosted instance while
// every clone and push still went to the public site, which reads as "the run
// can see the repo but publishes nothing" and is miserable to diagnose.
type Forge struct {
	GitHubBaseURL string `json:"github_base_url"`
	GitLabBaseURL string `json:"gitlab_base_url"`
	// The HTTPS roots a clone URL is built on. `cloneURL` in
	// app/runnerports.go currently hardcodes github.com and gitlab.com and
	// refuses anything else with "self-hosted base URLs arrive in Plan 08" —
	// this is that arrival. Rewrite it to read these instead, keeping it pure
	// (no credential, no assets) so its switch stays unit-testable.
	GitHubCloneBaseURL string `json:"github_clone_base_url"`
	GitLabCloneBaseURL string `json:"gitlab_clone_base_url"`
	// PublishWorkDir is where publication checkouts are materialized. Each
	// publication gets a fresh subdirectory that is removed when it ends,
	// so this only ever holds work in flight.
	PublishWorkDir string `json:"publish_work_dir"`
	// RequestTimeout bounds one forge API call. Unqualified: this struct
	// lives in package config, so config.Duration would not compile.
	RequestTimeout Duration `json:"request_timeout"`
}
```

Defaults: `https://api.github.com`, `https://gitlab.com`, `https://github.com`, `https://gitlab.com`, `/var/lib/caesar/publish`, `30s`. Every field has a read site — the two API bases in `buildForge` below, the two clone bases in `cloneURL` — which is what `deadconfig` requires. Add them to `caesar.dev.json` and to the README's config table.

Add a unit test for the rewritten `cloneURL` covering both the public defaults and a self-hosted base, and assert the endpoints move together:

```go
func TestCloneURL_FollowsTheConfiguredForgeAndNotThePublicHost(t *testing.T) {
	// The failure this guards is silent: API calls reach the self-hosted
	// instance, clone and push still reach github.com, and the run reads as
	// "can see the repo, publishes nothing".
	got, err := cloneURL(forgeEndpoints{
		GitHubClone: "https://git.example.internal",
	}, "github", "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://git.example.internal/acme/widgets.git"; got != want {
		t.Errorf("cloneURL = %q, want %q", got, want)
	}
	if strings.Contains(got, "github.com") {
		t.Error("a self-hosted run must not clone from the public host")
	}
}
```

- [ ] **Step 3: Write the adapters**

```go
// forgeEndpoints is the resolved base URLs per forge kind: the API host the
// clients talk to, and the HTTPS root clone and push use. They are separate
// because they genuinely are — api.github.com versus github.com — and because
// configuring one without the other is the silent self-hosted failure.
type forgeEndpoints struct {
	GitHub, GitLab           string
	GitHubClone, GitLabClone string
}

// forgeAdapter implements runs.Forge over the platform clients. It is the
// composition root's job because it needs BOTH the runs vocabulary and the
// assets credential path, and no context may see both.
//
// The credential is opened per call through the AUDITED path
// (assets.UseCredential), never cached on the adapter: caching it would
// outlive the audit entry that justifies it.
type forgeAdapter struct {
	assets *assets.Service
	github *forge.GitHub
	gitlab *forge.GitLab
}

// newForgeAdapter is the one construction site, shared by serve and the
// tests so a wiring change cannot pass in one and fail in the other.
func newForgeAdapter(a *assets.Service, eps forgeEndpoints, hc *http.Client) forgeAdapter {
	return forgeAdapter{
		assets: a,
		github: forge.NewGitHub(eps.GitHub, hc),
		gitlab: forge.NewGitLab(eps.GitLab, hc),
	}
}

func (f forgeAdapter) client(repo runs.RepoRef) (forge.Client, error) {
	switch repo.Forge {
	case "github":
		return f.github, nil
	case "gitlab":
		return f.gitlab, nil
	}
	return nil, xerrors.E(xerrors.ClassInvalid,
		fmt.Sprintf("unsupported forge %q", repo.Forge))
}

// open resolves the client and the credential in that order: an unsupported
// forge must fail WITHOUT an audited secret use.
func (f forgeAdapter) open(ctx context.Context, repo runs.RepoRef, purpose string) (forge.Client, forge.Auth, error) {
	c, err := f.client(repo)
	if err != nil {
		return nil, forge.Auth{}, err
	}
	token, err := f.assets.UseCredential(ctx, repo.CredentialID, purpose+":"+repo.Slug)
	if err != nil {
		return nil, forge.Auth{}, err
	}
	return c, forge.Auth{Token: string(token)}, nil
}

func (f forgeAdapter) OpenPullRequest(ctx context.Context, repo runs.RepoRef, spec runs.PullRequestSpec) (runs.PullRequestRef, error) {
	c, auth, err := f.open(ctx, repo, "open_pull_request")
	if err != nil {
		return runs.PullRequestRef{}, err
	}
	pr, err := c.CreatePullRequest(ctx, forgeRepo(repo), auth, forge.PullRequest{
		Head: spec.Head, Base: spec.Base, Title: spec.Title, Body: spec.Body,
	})
	if err != nil {
		return runs.PullRequestRef{}, typedForgeError("open pull request", err)
	}
	return runs.PullRequestRef{Number: pr.Number, URL: pr.URL, HeadSHA: pr.HeadSHA, Base: pr.Base, State: pr.State}, nil
}

// typedForgeError translates the platform sentinels into the spine's typed
// outcomes. The classification lives HERE rather than in the client because
// retry is a run-level decision: platform reports what happened, and the
// composition root says what it means for a run.
func typedForgeError(op string, err error) error {
	switch {
	case forge.IsRetryable(err):
		out := runs.Outcome{
			Class: runs.OutcomeTransient, Code: runs.CodeForgeUnavailable,
			Message: fmt.Sprintf("%s: %v", op, err),
		}
		// A rate limit that named its own reset is the one case where the
		// spine should not guess: retryDelay takes a stated RetryAt verbatim,
		// so carrying it here is what turns "back off on a curve" into
		// "come back exactly when GitHub said". Waking earlier would spend an
		// attempt into a window we were told is shut, and on a SECONDARY
		// limit it actively extends the lockout.
		if at, ok := forge.RetryAfter(err); ok {
			out.RetryAt = &at
		}
		return runs.OutcomeError{Outcome: out}
	case errors.Is(err, forge.ErrConflict):
		return runs.OutcomeError{Outcome: runs.Outcome{
			Class: runs.OutcomePermanent, Code: runs.CodePublishConflict,
			Message: fmt.Sprintf("%s: %v", op, err),
		}}
	case errors.Is(err, forge.ErrUnauthorized):
		return runs.OutcomeError{Outcome: runs.Outcome{
			Class: runs.OutcomePermanent, Code: runs.CodeInternalError,
			Message: fmt.Sprintf("%s: the forge rejected the repository's credential", op),
		}}
	}
	return runs.OutcomeError{Outcome: runs.Outcome{
		Class: runs.OutcomePermanent, Code: runs.CodeForgeUnavailable,
		Message: fmt.Sprintf("%s: %v", op, err),
	}}
}
```

`gitPublisher` wraps `gitops.Publish`. Task 9 Step 10 landed it with `assets` and a work dir; this task adds the `endpoints` field and points the work dir at the configured `PublishWorkDir`, so the whole type reads:

```go
// gitPublisher implements runs.GitPublisher. Each publication gets a fresh
// directory under the configured work dir, removed when the call returns:
// nothing about one publication may survive into another.
type gitPublisher struct {
	assets  *assets.Service
	workDir string
	// The configured clone roots. Without these, cloneURL falls back to the
	// public hosts and a self-hosted run queries one server while pushing to
	// another.
	endpoints forgeEndpoints
}

// prepare resolves everything BOTH GitPublisher methods need: the clone URL
// from the configured endpoints, the per-call credential through the audited
// path, and a fresh temp directory. It exists so PushPatch and ExpectedHead
// cannot disagree about which repository, which credential, or which inputs a
// publication uses — the whole point of ExpectedHead is that it reproduces
// what PushPatch would do.
//
// The returned cleanup is always non-nil.
func (p gitPublisher) prepare(ctx context.Context, req runs.PushRequest) (gitops.PublishInput, []string, string, func(), error) {
	url, err := cloneURL(p.endpoints, req.Repo.Forge, req.Repo.Slug)
	if err != nil {
		return gitops.PublishInput{}, nil, "", func() {}, err
	}
	token, err := p.assets.UseCredential(ctx, req.Repo.CredentialID, "publish:"+req.Repo.Slug)
	if err != nil {
		return gitops.PublishInput{}, nil, "", func() {}, err
	}
	// The token travels as env-only git config git does not persist — the
	// same in-memory credential path staging uses, and the reason
	// gitops.Publish can then PROVE the clone is credential-free.
	authEnv := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraheader",
		"GIT_CONFIG_VALUE_0=" + cloneAuthHeader(string(token)),
	}
	dir, err := os.MkdirTemp(p.workDir, "publish-")
	if err != nil {
		return gitops.PublishInput{}, nil, "", func() {}, fmt.Errorf("app: publish workdir: %w", err)
	}
	in := gitops.PublishInput{
		CloneURL: url, BaseSHA: req.BaseSHA, Branch: req.Branch, TreeHash: req.TreeHash,
		Patch: req.Patch, Message: req.Message,
		AuthorName: req.AuthorName, AuthorEmail: req.AuthorEmail, ExpectHead: req.ExpectHead,
		// The pinned commit date. Dropping it here is not a cosmetic slip:
		// Publish refuses a zero Date, so every real publication would fail
		// as invalid_output while the whole golden set — which builds its own
		// PushRequest — stayed green.
		Date: req.Date,
	}
	return in, authEnv, dir, func() { os.RemoveAll(dir) }, nil
}

func (p gitPublisher) PushPatch(ctx context.Context, req runs.PushRequest) (runs.PushResult, error) {
	in, authEnv, dir, cleanup, err := p.prepare(ctx, req)
	defer cleanup()
	if err != nil {
		return runs.PushResult{}, err
	}
	rep, err := gitops.Publish(ctx, dir, in, authEnv)
	if err != nil {
		return runs.PushResult{}, typedPublishError(err)
	}
	return runs.PushResult{HeadSHA: rep.HeadSHA, TreeHash: rep.TreeHash, Created: rep.Created}, nil
}

// ExpectedHead is the read-only half of the port: the sha PushPatch WOULD
// produce, with no ref written. Expired-run settlement is its only caller —
// it must identify a branch whose push receipt was lost while holding no
// authority to write anything.
func (p gitPublisher) ExpectedHead(ctx context.Context, req runs.PushRequest) (string, error) {
	in, authEnv, dir, cleanup, err := p.prepare(ctx, req)
	defer cleanup()
	if err != nil {
		return "", err
	}
	head, err := gitops.ExpectedPublicationHead(ctx, dir, in, authEnv)
	if err != nil {
		return "", typedPublishError(err)
	}
	return head, nil
}

func typedPublishError(err error) error {
	var te gitops.TypedError
	if errors.As(err, &te) {
		return runs.OutcomeError{Outcome: te.Outcome}
	}
	return err
}
```

`gitPublisher` carries `endpoints forgeEndpoints` alongside `assets` and `workDir`, so `cloneURL` reads the configured clone bases rather than the hardcoded public hosts.

`gitPublisher` itself and `principalSource` both landed in **Task 9 Step 10** — that is where `runs.NewService` grew the two dependencies and the tree had to compile. What this task adds to `gitPublisher` is the `endpoints` field and the configured `PublishWorkDir`; `principalSource` is unchanged and needs nothing here.

- [ ] **Step 4: Delete `unavailableForge` and wire the real thing**

In `serve.go`:

```go
	// One endpoints value for both adapters. Building it twice is how the API
	// host and the clone host drift apart, which is the silent self-hosted
	// failure: the run can read the repository and publishes nothing.
	endpoints := forgeEndpoints{
		GitHub: cfg.Forge.GitHubBaseURL, GitLab: cfg.Forge.GitLabBaseURL,
		GitHubClone: cfg.Forge.GitHubCloneBaseURL, GitLabClone: cfg.Forge.GitLabCloneBaseURL,
	}
	forgeSvc := newForgeAdapter(assetsSvc,
		endpoints,
		&http.Client{Timeout: cfg.Forge.RequestTimeout.Std()})
	if err := os.MkdirAll(cfg.Forge.PublishWorkDir, 0o700); err != nil {
		return fmt.Errorf("app: publish work dir: %w", err)
	}
	publisher := gitPublisher{assets: assetsSvc, workDir: cfg.Forge.PublishWorkDir, endpoints: endpoints}

	runsSvc := runs.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}),
		runsDefinitionSource{svc: workflowsSvc}, runsAssetSource{svc: assetsSvc}, runsAssetSource{svc: assetsSvc},
		forgeSvc, publisher, principalSource{svc: svc}, runs.Params{Snapshot: runs.DefaultSnapshot()})
```

Delete the `unavailableForge` type entirely.

- [ ] **Step 5: Run the wiring tests and boot the binary**

Run: `go test ./internal/app/ -race && task backend:test:integration -- -run TestServe -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/app/ backend/internal/platform/config/config.go backend/caesar.dev.json \
        backend/internal/runs/service.go backend/internal/runs/orchestrator.go
git commit -m "feat(app): wire the real forge and publisher so production task intake works"
```

---

### Task 13: The golden set — the whole conveyor, with crash injection around every publish step

Spec §7's acceptance evidence for this plan: `task → phases → gate → approve → publish-with-expected-SHA`, driven through the API, asserting artifacts, events and audit, with crashes injected post-push, post-PR-create and pre-DB-commit.

**Files:**
- Modify: `backend/internal/app/goldenset_integration_test.go`
- Modify: `backend/internal/runs/fake/forge.go`, `publisher.go`
- Test: the file above

- [ ] **Step 1: Extend the golden-set fixture to the full loop**

The existing golden set drives a gateless workflow. Add the conveyor definition (`plan → implement → review → gate → publish`, gate rewinding to `implement` with `max_rounds: 2`), a real bare repo via `fake.InitBareRepo`, and the two crash switches.

- [ ] **Step 2: Write the failing golden-set tests**

```go
func TestGoldenSet_FullConveyorThroughTheAPI(t *testing.T) {
	g := newGoldenEnv(t)
	requester := goldenLogin(t, g.srv, "requester@example.com", "pw")
	approver := goldenLogin(t, g.srv, "approver@example.com", "pw")

	var task runsapi.TaskResponse
	if got := requester.do(t, http.MethodPost, "/api/v1/tasks", runsapi.CreateTaskRequest{
		RepoID: g.repoID, WorkflowID: g.workflowID, WorkflowVersion: 1,
		Title: "Add a CHANGELOG", Inputs: map[string]any{"topic": "changelog"},
	}, &task); got != http.StatusCreated {
		t.Fatalf("createTask = %d", got)
	}

	// One request-changes round, then approve.
	g.waitForApproval(t, approver, task.LatestRunID)
	approver.decide(t, task.LatestRunID, "gate", "request_changes", "add a header")
	g.waitForApproval(t, approver, task.LatestRunID)
	approver.decide(t, task.LatestRunID, "gate", "approve", "")

	run := g.waitForRunStatus(t, approver, task.LatestRunID, "succeeded", 90*time.Second)

	// Artifacts: two generations of plan/patch/verdict, one publication.
	byType := map[string]int{}
	for _, a := range run.Artifacts {
		byType[a.Type]++
	}
	for _, want := range []struct {
		typ string
		n   int
	}{{"plan", 1}, {"patch", 3}, {"review_verdict", 2}, {"gate_note", 1}, {"publication", 1}} {
		if byType[want.typ] != want.n {
			t.Errorf("%s artifacts = %d, want %d (all: %v)", want.typ, byType[want.typ], want.n, byType)
		}
	}
	// patch = 2 produced + 1 seeded carry-forward; the seed is what makes
	// the second round a REVISION rather than a restart.

	// Events, in order and complete.
	g.assertEventSequence(t, approver, task.LatestRunID, []string{
		"run.created", "run.started",
		"phase.materialized", // plan
		"gate.waiting", "gate.decided", "run.revision_opened",
		"gate.waiting", "gate.decided",
		"publish.intent_created", "publish.pushed", "publish.pr_opened",
		"run.succeeded",
	})

	// Audit, by action.
	for _, action := range []string{"task.created", "run.created", "gate.decided", "run.finished"} {
		if n := g.countAudit(t, action, ""); n == 0 {
			t.Errorf("no %s audit entry", action)
		}
	}
	// Two decisions, one per generation, both attributed to the approver.
	if n := g.countAuditBySubject(t, "gate.decided", "gate_decision"); n != 2 {
		t.Errorf("gate.decided audit entries = %d, want 2", n)
	}

	// The remote really carries the publication.
	pub := run.Publications[0]
	if got := g.remoteHead(t, pub.Branch); got != pub.HeadSHA {
		t.Errorf("remote %s = %q, want %q", pub.Branch, got, pub.HeadSHA)
	}
	if got := g.remoteFileAt(t, pub.HeadSHA, "CAESAR.md"); got == "" {
		t.Error("the published commit does not contain the patch's file")
	}
}

func TestGoldenSet_CrashInjectionAroundEveryPublishStep(t *testing.T) {
	// spec §7: "crash injection around every publish step (post-push,
	// post-PR-create, pre-DB-commit)". Each case runs the SAME conveyor and
	// asserts the same end state, which is what makes the machine
	// crash-safe rather than merely retryable.
	cases := []struct {
		name  string
		arm   func(*goldenEnv)
		wantPushes, wantPRs int
	}{
		{"post-push", func(g *goldenEnv) { g.publisher.CrashAfterPush(true) }, 1, 1},
		{"post-pr-create", func(g *goldenEnv) { g.forge.CrashAfterPR(true) }, 1, 1},
		{"pre-db-commit-on-push-receipt", func(g *goldenEnv) { g.failNextTx("RecordPushReceipt") }, 1, 1},
		{"pre-db-commit-on-pr-receipt", func(g *goldenEnv) { g.failNextTx("RecordPRReceipt") }, 1, 1},
		{"pre-db-commit-on-intent", func(g *goldenEnv) { g.failNextTx("CreatePublishIntent") }, 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newGoldenEnv(t)
			requester := goldenLogin(t, g.srv, "requester@example.com", "pw")
			approver := goldenLogin(t, g.srv, "approver@example.com", "pw")
			task := g.createTask(t, requester)
			g.waitForApproval(t, approver, task.LatestRunID)

			c.arm(g)
			approver.decide(t, task.LatestRunID, "gate", "approve", "")
			g.waitUntilArmedFailureFired(t, 30*time.Second)
			g.disarm()

			run := g.waitForRunStatus(t, approver, task.LatestRunID, "succeeded", 90*time.Second)

			if got := g.publisher.PushCount(); got != c.wantPushes {
				t.Errorf("pushes = %d, want %d", got, c.wantPushes)
			}
			if got := g.forge.OpenPRCount(); got != c.wantPRs {
				t.Errorf("pull requests opened = %d, want %d", got, c.wantPRs)
			}
			if got := g.remoteBranchCount(t, "caesar/"); got != 1 {
				t.Errorf("caesar branches on the remote = %d, want 1", got)
			}
			if got := g.countIntents(t, run.ID); got != 1 {
				t.Errorf("publish intents = %d, want 1", got)
			}
			if got := g.countAuthorizations(t, run.ID, "consumed"); got != 1 {
				t.Errorf("consumed authorizations = %d, want 1: the token is single-use through a crash too", got)
			}
			if len(run.Publications) != 1 || run.Publications[0].Status != "published" {
				t.Errorf("publications = %+v", run.Publications)
			}
		})
	}
}

func TestGoldenSet_CrashBeforeTheIntentCommitsPublishesNothing(t *testing.T) {
	// The one case where "resume" must mean "start over": if the intent's
	// transaction never committed, no branch exists and the authorization
	// was never consumed. The next attempt must find a LIVE authorization.
	g := newGoldenEnv(t)
	requester := goldenLogin(t, g.srv, "requester@example.com", "pw")
	approver := goldenLogin(t, g.srv, "approver@example.com", "pw")
	task := g.createTask(t, requester)
	g.waitForApproval(t, approver, task.LatestRunID)

	g.failNextTx("CreatePublishIntent")
	approver.decide(t, task.LatestRunID, "gate", "approve", "")
	g.waitUntilArmedFailureFired(t, 30*time.Second)

	// While it is still armed, the authorization must be untouched: the
	// consume and the intent insert are in ONE transaction.
	if got := g.countAuthorizations(t, task.LatestRunID, "valid"); got != 1 {
		t.Fatalf("valid authorizations = %d, want 1: consumption and intent creation are atomic", got)
	}
	if g.publisher.PushCount() != 0 {
		t.Fatal("nothing may be pushed before the intent commits")
	}
	g.disarm()
	g.waitForRunStatus(t, approver, task.LatestRunID, "succeeded", 90*time.Second)
}

func TestGoldenSet_RejectedGateLeavesTheRemoteUntouched(t *testing.T) {
	g := newGoldenEnv(t)
	requester := goldenLogin(t, g.srv, "requester@example.com", "pw")
	approver := goldenLogin(t, g.srv, "approver@example.com", "pw")
	task := g.createTask(t, requester)
	g.waitForApproval(t, approver, task.LatestRunID)
	approver.decide(t, task.LatestRunID, "gate", "reject", "wrong approach")

	run := g.waitForRunStatus(t, approver, task.LatestRunID, "failed", 30*time.Second)
	if run.Outcome == nil || run.Outcome.Code != "gate_rejected" {
		t.Fatalf("outcome = %+v", run.Outcome)
	}
	if got := g.remoteBranchCount(t, "caesar/"); got != 0 {
		t.Errorf("a rejected gate published %d branches", got)
	}
	if g.publisher.PushCount() != 0 || g.forge.OpenPRCount() != 0 {
		t.Error("a rejected gate must reach no forge at all")
	}
}
```

- [ ] **Step 3: Build the crash-injection seam**

`failNextTx(statement string)` needs a place to stand. The honest one is a `pgxpool` wrapper that fails a named statement once, but the pool type is concrete throughout. Instead, inject at the **port** boundary the golden set already owns plus one narrow test hook on the service:

```go
// FailNextStatement makes the named store statement return an error exactly
// once, then disarm. It exists so the golden set can inject a crash between
// an effect and its receipt — the window that decides whether a publication
// is idempotent (spec §7 crash injection).
//
// Test-only and guarded: it is a no-op unless the service was constructed
// with Params.AllowFaultInjection, which only the test harness sets. The
// stubfake analyzer's rule is about production stubs, not about a seam that
// makes a crash reproducible.
func (s *Service) FailNextStatement(name string) {
	s.faultMu.Lock()
	defer s.faultMu.Unlock()
	s.faultNext = name
}

// faultBefore fires and disarms. It returns nil — the zero cost of the seam —
// unless fault injection was enabled at construction AND the named statement
// is armed.
func (s *Service) faultBefore(name string) error {
	if !s.params.AllowFaultInjection {
		return nil
	}
	s.faultMu.Lock()
	defer s.faultMu.Unlock()
	if s.faultNext != name {
		return nil
	}
	s.faultNext = ""
	return fmt.Errorf("runs: injected fault before %s", name)
}
```

`Params` grows one field:

```go
type Params struct {
	Snapshot PolicySnapshot
	// AllowFaultInjection enables FailNextStatement. Only the golden-set
	// harness sets it; production wiring leaves it false, which makes
	// faultBefore a single boolean read on the publish path.
	AllowFaultInjection bool
}
```

The check happens in exactly three places — `CreatePublishIntent`, `RecordPushReceipt`, `RecordPRReceipt` — via a one-line `if err := s.faultBefore("RecordPushReceipt"); err != nil { return err }`. Three call sites, each named in a test, is a cost worth paying for a property no other technique reaches.

If the reviewer prefers a production-free seam, the alternative is a `store.Queries` interface with a faulting decorator; that is a much larger change (sqlc generates a concrete type) and should be argued for explicitly rather than assumed.

- [ ] **Step 4: Run the golden set**

Run: `task backend:test:integration -- -run TestGoldenSet -race -v`
Expected: PASS, all five crash cases.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/app/goldenset_integration_test.go backend/internal/runs/fake/ \
        backend/internal/runs/service.go backend/internal/runs/publish.go
git commit -m "test(app): drive the full conveyor through the api with crash injection at every publish step"
```

---

### Task 14: Carry-forwards from Plans 04 and 05

Four defects the earlier plans' final reviews triaged as "Plan 08 owns this". Each is small; together they are one commit's worth of debt this plan promised to clear.

**Files:**
- Modify: `backend/internal/runs/tasks.go` (`Rerun` :271-352)
- Modify: `backend/internal/app/goldenset_integration_test.go`
- Modify: `backend/internal/workflows/validate.go` (:119-131), `workflows.go`
- Test: the corresponding test files

- [ ] **Step 1: Write the failing carry-forward tests**

```go
func TestRerun_ConcurrentRerunsConflictInsteadOf500(t *testing.T) {
	// Plan 05 carry-forward: two reruns racing past LatestRunForUpdate hit
	// runs_task_live_key and surfaced as an unclassified 23505 — a 500 for
	// what is plainly a conflict.
	h := newSpineHarness(t)
	actor := h.newUser(t, "runner@example.com", authn.GrantRun)
	task, _ := h.createTask(t, gatelessWorkflow(t, h))
	h.waitForRunStatus(t, task.LatestRunID, runs.RunSucceeded, 30*time.Second)

	const n = 6
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = h.svc.Rerun(h.ctx, actor, task.ID)
		}(i)
	}
	close(start)
	wg.Wait()

	won := 0
	for i, err := range errs {
		switch {
		case err == nil:
			won++
		case xerrors.ClassOf(err) == xerrors.ClassConflict:
		default:
			t.Errorf("rerun %d: %v (class %s), want a conflict", i, err, xerrors.ClassOf(err))
		}
	}
	if won != 1 {
		t.Fatalf("%d reruns won, want 1", won)
	}
}

func TestRerun_ReResolvesHarnessesSoADeletedOneIsCaughtAtIntake(t *testing.T) {
	// Plan 05 carry-forward: Rerun skipped the harness re-resolution
	// CreateTask does, so a deleted harness surfaced only as a typed
	// harness_error at execution — after minting a run that could never run.
	h := newSpineHarness(t)
	actor := h.newUser(t, "runner@example.com", authn.GrantRun)
	task, _ := h.createTask(t, gatelessWorkflow(t, h))
	h.waitForRunStatus(t, task.LatestRunID, runs.RunSucceeded, 30*time.Second)
	h.deleteHarness(t, "h")

	_, err := h.svc.Rerun(h.ctx, actor, task.ID)
	if xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatalf("err = %v (class %s), want an invalid naming the missing harness", err, xerrors.ClassOf(err))
	}
	if got := h.countRuns(t, task.ID); got != 1 {
		t.Errorf("runs = %d, want 1: a refused rerun mints nothing", got)
	}
}

func TestValidate_DuplicateEdgeIsReportedOnceNotTwice(t *testing.T) {
	// Plan 04 hardening item 1: the multiplicity map counted a duplicate
	// edge twice, so one mistake produced two violations.
	doc := workflows.Document{
		Schema: 1,
		Nodes: []workflows.Node{
			{ID: "implement", Type: workflows.NodeImplement, Harness: "h", Instructions: "x"},
			{ID: "review", Type: workflows.NodeReview, Harness: "h", Instructions: "x"},
		},
		Edges: []workflows.Edge{
			{From: "implement", To: "review", Artifact: workflows.ArtifactPatch},
			{From: "implement", To: "review", Artifact: workflows.ArtifactPatch},
		},
	}
	vs := workflows.Validate(doc)
	dupes := 0
	for _, v := range vs {
		if strings.Contains(v.Message, "duplicate edge") {
			dupes++
		}
	}
	if dupes != 1 {
		t.Errorf("duplicate-edge violations = %d, want 1", dupes)
	}
	for _, v := range vs {
		if strings.Contains(v.Message, "at most 1 patch") {
			t.Errorf("a duplicate edge must not ALSO read as a multiplicity breach: %+v", vs)
		}
	}
}

func TestWorkflowName_LengthErrorSaysBytes(t *testing.T) {
	// Plan 04 hardening item 2: the message said "characters" while the
	// check counted bytes, which is a lie for any non-ASCII name.
	_, err := workflowsSvcCreate(t, strings.Repeat("é", 200))
	var fe interface{ Fields() []xerrors.FieldError }
	if !errors.As(err, &fe) {
		t.Fatal("want field errors")
	}
	if !strings.Contains(fe.Fields()[0].Message, "bytes") {
		t.Errorf("message = %q; the check counts bytes and must say so", fe.Fields()[0].Message)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `task backend:test:integration -- -run 'TestRerun_' -v && go test ./internal/workflows/ -run 'TestValidate_Duplicate|TestWorkflowName' -v`
Expected: FAIL.

- [ ] **Step 3: Classify the concurrent-rerun unique violation**

In `Rerun`, wrap the `insertRun` call:

```go
		run, err = s.insertRun(ctx, tx, taskID, taskRow.ProjectID, def, baseSHA, correlationID)
		if isUniqueViolation(err) {
			// runs_task_live_key: another rerun committed its pending run
			// between our LatestRunForUpdate and this insert. That is a
			// conflict, not an internal failure — the caller should retry or
			// look at the run that won.
			return xerrors.E(xerrors.ClassConflict, "another re-run of this task is already pending")
		}
		if err != nil {
			return err
		}
```

- [ ] **Step 4: Re-resolve harnesses on rerun**

Extract the harness loop from `resolveIntake` and call it from both:

```go
// checkHarnesses resolves every agentic node's harness against the registry.
// Both intake paths run it: a run whose harness no longer exists can never
// execute, and minting it anyway trades a clear 400 at the door for a typed
// harness_error three phases later.
func (s *Service) checkHarnesses(ctx context.Context, plan ExecPlan) error {
	for _, id := range plan.Order {
		n := plan.Nodes[id]
		if n.Harness == "" {
			continue
		}
		if _, err := s.harnesses.HarnessByName(ctx, n.Harness); err != nil {
			if xerrors.ClassOf(err) == xerrors.ClassNotFound {
				return xerrors.E(xerrors.ClassInvalid, "validation failed").
					WithFields(xerrors.FieldError{Field: "harness", Message: fmt.Sprintf("node %q references unknown harness %q", id, n.Harness)})
			}
			return err
		}
	}
	return nil
}
```

In `Rerun`, after `definitionByVersionID` — and have `definitionByVersionID` return the compiled plan so it is not compiled twice.

- [ ] **Step 5: Fix the two Plan 04 hardening items**

In `validate.go`'s multiplicity loop, skip edges already reported as duplicates by tracking the same `seen` set the shape phase built. Restructure so `validateShape` computes `seen` once and passes the deduplicated edge list to the multiplicity count.

In `workflows.go:58`, change `characters` to `bytes` in the name-length message.

- [ ] **Step 6: Fix the golden-set audit-query scoping**

The Plan 05 carry-forward: the golden set's audit assertions scope by action alone, so an unrelated entry of the same action passes them. Add the subject id, and make the heads map settable so a second repo can be seeded:

```go
// countAuditBySubject scopes by action AND subject, which is what makes the
// assertion about THIS run rather than about any run in the database.
func (g *goldenEnv) countAuditBySubject(t *testing.T, action, subjectKind string) int {
	t.Helper()
	var n int
	err := g.pool.QueryRow(g.ctx,
		`SELECT count(*) FROM audit_entries WHERE action = $1 AND subject_kind = $2`,
		action, subjectKind).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
```

`fake.Forge.Heads` is already an exported map; add a `SetHead(repoID uuid.UUID, sha string)` method with a mutex so a test can move a head mid-run without a data race (the golden set now runs the orchestrator concurrently with its assertions).

- [ ] **Step 7: Run everything**

Run: `go test ./... -race && task backend:test:integration`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add backend/internal/runs/tasks.go backend/internal/workflows/ backend/internal/app/ \
        backend/internal/runs/fake/
git commit -m "fix(runs): classify concurrent reruns as conflicts and re-resolve harnesses at re-run intake"
```

---

### Task 15: CI, docs, roadmap, and the plan committed

**Files:**
- Modify: `backend/README.md`
- Modify: `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md` (row 08 and the note under the table)
- Modify: `docs/superpowers/plans/2026-08-03-caesar-05-execution-spine.md` (the `phase.materialized` payload row)
- Modify: `docs/superpowers/plans/2026-08-07-caesar-08-gates-publishing.md` — this file is **already tracked** (`5cb627b`); this task AMENDS it, see Step 4
- `.github/workflows/ci.yml`: **no change needed.** Verified — the `backend` job runs on `ubuntu-latest` with `actions/checkout@v4`, so `git` is present by construction, and `git` was already required by the untagged `internal/runs/fake/executor_test.go` before this plan.

- [ ] **Step 1: Confirm CI needs no new lane**

Everything this plan adds runs in the existing `backend` lane: unit tests, integration tests (testcontainers Postgres), and `gitops.Publish` tests (the `git` binary, already required by `fake.InitBareRepo` since Plan 05). Nothing here needs Docker-in-Docker or the `runnerintegration` tag.

Verify rather than assume:

```bash
grep -n "test:integration\|backend:ci" Taskfile.yml
```

Confirm `backend:ci` runs `backend:test:integration`. If the forge tests were accidentally given a build tag, remove it — they are pure `httptest` and belong in the default unit run.

- [ ] **Step 2: Add the README section**

Under the existing Plan 06/07 sections — after "Subscription-first harness drivers (Plan 07)" and before `## Contracts` — add a section covering the following. Name it to match the README's own convention, `## Gates and publishing (Plan 08)`, rather than the "Plan 08 — …" form: every neighbouring section is `<Title> (Plan NN)`.

- **The gate lifecycle.** A gate materializes as `waiting_decision`, and the execution budget stops accruing because nothing is claimed. The wall clock does not stop. The run parks to `waiting` only once nothing else on the graph is runnable — a gate in one branch of a parallel graph leaves the run `running` while its sibling executes — and that answer is read back from the database (`RunIsWaitingOnAGate`), never inferred. `GET /api/v1/approvals` is the inbox; `POST /api/v1/runs/{id}/gates/{node}/decision` is the door, and it needs `approve` on the project resolved from the run.
- **What an approval authorizes.** The single-use `ApprovedPatch`: artifact id, base SHA, tree hash, the repository AS APPROVED (`repo_id` **plus** `forge_kind` and `repo_slug`, pinned because `UpdateRepo` can rename a repo mid-run and a rename must become a refusal, not a patch delivered to the wrong repository), target branch, generation. Scoped to the one publish node its gate dominates. A newer generation invalidates it. Name **both** indexes: `publish_intents.authorization_id … UNIQUE` (a second consumption is a constraint violation, not a lost CAS) and `approved_patches_live_key ON approved_patches (run_id, publish_node_id) WHERE status = 'valid'` (what makes "invalidate then mint" impossible to get half-done).
- **Revisions.** `request_changes` bumps the generation, rewinds the target→gate slice, invalidates live authorizations, and seeds the target with the examined artifact plus the note. `max_rounds` is per gate, but the generation counter and the authorization invalidation are per **run** — a revision at one gate advances the whole run's generation and invalidates every live authorization on it. Exhausting `max_rounds` refuses the decision (409) rather than killing the run. A **second** 409 refuses `request_changes` once the run has begun ANY publication — including a `pending` intent, whose row commits before the first forge call and whose authorization is already spent. Nothing in that transaction can un-push a commit, so refusing is the honest answer; the human keeps approve and reject.
- **The publish state machine.** `pending → pushed → published`, with `halted` as the terminal manual-reconciliation state. Each effect's receipt commits before the next call. Recovery probes the deterministic branch marker `caesar/<approved-artifact-id>` and the pull requests on that marker. A request is adopted only if it carries both a base and a head, its base equals the branch the approval named, and its head equals the sha we recorded. A candidate missing either field, two candidates matching, or any candidate present that cannot be ruled out all halt — absent evidence is never permission.
- **Attribution**, verbatim. Author = the requester (`GIT_AUTHOR_NAME` is the email's local part, `GIT_AUTHOR_EMAIL` the address); committer = `caesar <caesar@invalid>`. `GIT_AUTHOR_DATE` and `GIT_COMMITTER_DATE` are one pinned RFC3339 stamp, because the commit sha is what recovery rebuilds to prove a receiptless branch is ours. Four trailers, in this order: `caesar-run`, `caesar-node`, `caesar-artifact`, `caesar-approved-by` (the APPROVER's address — the author is the requester, and the message is where the approver is recorded). No machine co-author trailer; `gitops/publish_test.go` asserts none appears. Say that both `author_email` and `commit_message` are SNAPSHOTTED on the intent and never re-rendered, and why: both are hashed into the commit sha, both interpolate a mutable `users` row, and a re-render between attempts would turn a crash-after-push into a permanent `publish_conflict` on a branch that is ours.
- **Operating a halted publication** — the runbook. The run is already `failed` with the halting code (`publish_conflict`, `auth_expired` or `repo_unavailable`); caesar will never resume it, because `HaltPublishIntent` is terminal by design and no later attempt reopens a halted intent. Read `halt_reason` (surfaced on the run's publication projection) and the `publish.halted` run event, then inspect the branch on the forge. Two answers: re-run the task — a new run mints a new artifact id and therefore a NEW `caesar/<id>` marker, so the abandoned ref never blocks it and deleting that ref is hygiene rather than a precondition — or accept what is already on the forge and close the task by hand. **There is no endpoint that marks a halted publication resolved**, and that is deliberate: it belongs with Plan 09's operator surface.
- **Config**: do NOT re-document the `forge` block — the README's Config section already covers both endpoints per forge, the self-hosted trap, `publish_work_dir` and its default, and the base-URL validation rules. Cross-reference it, verify the key names still match `config.Forge` (`internal/platform/config/config.go`), and add only what is operational: `publish_work_dir` must be writable by the caesar process, and `serve` creates it at boot rather than lazily.

- [ ] **Step 2b: Fix the two stale README paragraphs this plan falsified**

`backend/README.md`'s Plan 05 section claimed `serve` wires "a forge that refuses to resolve a head" and that "a definition containing gate or `git-publish` nodes is rejected at intake (`waiting` stays unreachable until Plan 08)". Both are false as of Task 12: `serve.go` wires the real forge adapter and the real `gitPublisher`, and `unavailableForge` and `ExecPlan.HasUnsupportedNodes` are deleted. Only the executor half survives — `NotImplementedExecutor` is still the default, so a phase claimed with `runner.enabled` false still dies `not_implemented`. The Plan 06 section repeats the same false claim ("task intake is refused until Plan 08's forge adapters land, so an enabled runner is unreachable today"); an enabled runner is now reachable end to end. The ledger calls for a coordinated rewrite of the first, not a one-line edit.

- [ ] **Step 2c: Write the live-forge smoke checklist**

Row 08 closes on it (Step 3), so it is load-bearing rather than optional. ~25 lines in `backend/README.md`, mirroring "Manual smoke checklist (operator-run, real subscriptions)": a throwaway repo per forge, a real `forge_token`, one task through a gate — with one revision round — to an approve, then verify the branch, the four trailers, the committer identity, and the pull request's base, plus the two halting answers (`auth_expired`, `repo_unavailable`).

- [ ] **Step 3: Update the roadmap row**

Only the Status column changes; the deliverable cell is already correct.

**>>> USER RULING (sofo): row 08 does NOT flip to done. <<<** It reads
**`implementation complete — live-forge smoke pending`**, mirroring row 07
exactly. The reasoning: row 07's note establishes that a plan whose acceptance
evidence is an operator event does not flip to done on green CI, and the failure
mode here is identical in kind — the GitHub and GitLab response shapes and error
mappings are invented in `httptest` fixtures exactly as the codex exit markers
were invented in a CI fixture. Marking 08 done would set the precedent that the
row-07 rule was a one-off. Row 08 closes on Step 2c's smoke checklist.

```markdown
| 08 | Gates & publishing | … | **implementation complete — live-forge smoke pending** — `2026-08-07-caesar-08-gates-publishing.md` |
```

Also add a note under the table (row 07's caveat stands unchanged — this plan touches no harness driver) recording that **row 08's forge adapters are exercised only against `httptest` and local bare repos**: no CI lane makes a live forge call, so the first real GitHub/GitLab publication is an operator event exactly as row 07's smoke run is, and the row's own *Milestone: month-one loop complete via API* claim rides on the same evidence. Do not claim it as verified.

- [ ] **Step 4: Verify the plan document is committed, then amend its stale text**

The plan is ALREADY tracked — it landed at `5cb627b` (8,993 lines), a deliberate deviation from the 03/04/05/06/07 convention recorded in the ledger, because that much corrected text should not sit untracked while thirteen tasks execute against it. Verify, do not create:

```bash
git log --oneline -- docs/superpowers/plans/2026-08-07-caesar-08-gates-publishing.md
# expect: 5cb627b docs(plan): add plan 08 with its pre-flight cross-task corrections applied
```

Then apply the amendments execution proved necessary:

1. **Decision 14** above — publish failures split five ways, three halting, not three ways with one halting.
2. **Plan 05's docs table** — `phase.materialized` gained a `generation` key.
3. **`CountSettledPublications` is deliberately misnamed** — record it in the residuals.
4. **`CodeRevisionRoundsExhausted` is deliberately unused** — record it in the residuals.
5. **The `problemShapes` residual is false and this plan falsified it** — rewrite it.
6. **The `schema.sql` equality gap** becomes a residual, not a test in this task.

- [ ] **Step 5: Run the gate**

`task ci` CANNOT go green from a linked worktree: its `secrets` lane mounts `ROOT_DIR` and a worktree's `.git` is a file pointing outside that mount. Do not run `task ci`. Run its parts:

```bash
task backend:ci        # includes backend:test:integration — do not run it twice
task contracts:check
task lint:actions      # measured green from a worktree; it never touches .git

# gitleaks, via the PARENT repo's absolute mount. gitleaks scans all refs, so
# this covers this branch even though the parent's HEAD is main.
docker run --rm -u "$(id -u):$(id -g)" \
  -v "/Users/sofo/Projects/caesar/repos/caesar:/repo" \
  zricethezav/gitleaks:v8.21.2 git /repo --config /repo/.gitleaks.toml --verbose
```

Record the commit count. At `f8a59a8` it was **234 commits scanned, no leaks** (= `git rev-list --count --all --no-merges`). A gitleaks run that says "partial scan" has FAILED even when it also says "no leaks found" — check the exit status, not the words.

`task backend:test:runner` (Plan 06/07's Docker lane) belongs to the **whole-branch review**, not to this task: Task 15 changes zero Go files, and its last attestation already came from an implementer.

- [ ] **Step 6: Commit**

```bash
git add backend/README.md docs/superpowers/plans/
git commit -m "docs(plans): record plan 08 gates and publishing"
```

---

## Verification

The plan is done when all of the following hold:

1. `task backend:ci`, `task contracts:check` and `task lint:actions` are green, and the parent-repo gitleaks scan reports its full commit count with no leaks. `task ci` is not runnable from a linked worktree (see Task 15, Step 5). `task backend:test:runner` is green — attested by the whole-branch review, not by an implementer.
2. `TestGoldenSet_FullConveyorThroughTheAPI` passes: a task created through the API reaches a gate, survives one revision round, is approved, and lands a branch and a pull request on a real (local, bare) remote — with the artifact, event and audit assertions holding.
3. All five `TestGoldenSet_CrashInjectionAroundEveryPublishStep` cases pass. The asserted properties are one branch CREATION (`publisher.CreatedCount() == 1`), one pull request, one caesar branch on the remote, one intent, one consumed authorization, one `pr_number` event, one `publish.intent_created`, no leftover attention item — and `publish.adopted == 1` for the four forge-reaching cases, `0` for `pre-db-commit-on-intent`. **PUSH COUNT IS NOT ASSERTED and must not be**: `CrashAfterPush` is sticky (measured 8 pushes) and a receipt failure costs the push plus its recovery (measured 2). `publish.adopted` is the only assertion that separates "crashed then recovered" from "never crashed" — two cases once passed with both crash switches turned into no-ops.
4. `unavailableForge` no longer exists and `ExecPlan.HasUnsupportedNodes` no longer exists.
5. Roadmap row 08 reads **implementation complete — live-forge smoke pending** (user ruling; see Task 15, Step 3), with the honest note that no CI lane has ever made a live forge call, and `backend/README.md` carries the manual forge smoke checklist the row closes on.

## Known residuals (name them in the final review, do not silently carry them)

- **No live-forge evidence.** Every forge assertion in this plan is against `httptest` and local bare repos. The first real GitHub or GitLab publication is an operator event. Task 15 wrote the manual forge smoke checklist into `backend/README.md`, mirroring row 07's; roadmap row 08 stays *implementation complete — live-forge smoke pending* until it runs against throwaway repos and its result is committed. **This residual is open, not carried:** it has a named closing action.
- **The `repo_unavailable` halting arm stays unobserved even after the smoke run.** It fires only on `OpenPullRequest` and `FindPullRequests` (`internal/app/runsports.go`), i.e. once an intent is already `pushed`, so reaching it means deleting the repository inside the push→PR-open window — which no manual procedure hits reliably. The route an operator CAN take (delete the repo, then approve) does not reach it: `BranchHead`'s 404 is folded into "no branch" by design, because absence is recovery's answer rather than its failure; the clone then fails, and `gitops` types a failed clone transient `forge_unavailable` because it genuinely cannot tell a deleted repository from a network fault. Decision 14 is accurate; it is narrower in *reach* than it reads. What stays live is exactly the failure the smoke checklist was written for — a `repo_unavailable` presenting as `forge_unavailable` retries a vanished repository until the wall deadline with a pushed branch orphaned behind it — and closing it needs a fault-injected integration case at the PR-open step, not another operator run.
- **Statement-fault injection is test-only, not a production seam.** Task 13 added zero production lines. The mechanism is `testdb.QueryFaults`, a `pgx.QueryTracer` in `backend/internal/platform/testdb/faults.go` behind `//go:build integration || runnerintegration`, matching sqlc statements by name. No production file, no params flag, nothing importable from a non-test build. The residual is therefore that fault injection matches statements by their sqlc NAME (a string), so renaming a query silently disarms the case that used it.
- **`CountSettledPublications` is deliberately misnamed.** It counts EVERY `publish_intents` row for the run, `pending` included — `SELECT count(*) FROM publish_intents WHERE run_id = $1`. The name reads as "settled", but a pending intent has already committed its row and consumed its authorization before the first forge call, so it is precisely the window a revision cannot take back. The query's own comment carries the reasoning; the name was left alone rather than churn a generated store, and this line is the record that it is wrong on purpose.
- **`runs.CodeRevisionRoundsExhausted` is deliberately unused.** Exhausting `max_rounds` refuses the DECISION with a 409 (`internal/runs/revisions.go`) and terminalizes nothing, so no attempt or run ever carries this code — one definition in `outcome.go`, one row in an `outcome_test.go` table, no production reference. It stays defined because it names a real answer the API gives, and because deleting it would invite someone to re-invent it as a terminal outcome, which is exactly what decision 12 forbids.
- **Nothing asserts the migrated schema equals `schema.sql`.** `migrate_updown_integration_test.go` walks every migration down and back up and then checks that two tables exist by name. `schema.sql` is the declarative source Atlas diffs migrations from and sqlc generates every store from, so a hand-edited migration that drifts from it is invisible to CI until a generated query fails at runtime. The check is a real test, not a doc line: either an Atlas lane (atlas is not in CI today) or a Go integration test that applies `schema.sql` to a second database and compares catalogs, whose real cost is the false-positive tail — constraint naming, check-constraint text normalization, index expression formatting. **Scheduled, not written**; it deserves its own task with its own review.
- **A halted publication has no API.** It is visible in the run projection and in `publish.halted` events, and reconciled by hand per the README runbook. A resolve endpoint belongs with Plan 09's operator surface.
- **`publish_intents` is never pruned.** Like `run_events`, it is history; unlike the outbox, it has no retention sweeper. Fold it into the retention story when Plan 09 pins retention numbers.
- **The seed carry-forward duplicates a patch payload per revision round.** A run with `max_rounds: 10` over a 4 MiB patch stores 40 MiB of near-identical artifacts. Acceptable at V1 scale and with the pinned caps; revisit if patch sizes grow.
- **Documented non-2xx statuses are still opt-in, and only three operations opt in.** 400 is emitted on every operation by the generator; `httpx.Operation.Errors` lists the extras, and across the whole tree exactly three routes declare any: `createTask` and `rerunTask` (404 + 429, both added by THIS plan) and `decideGate` (409). Every other endpoint's 401/403/404/409 answers are real but undocumented in the OpenAPI contract. The systematic fix belongs to a contracts pass, not here.
