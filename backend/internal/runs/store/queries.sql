-- name: InsertTask :one
INSERT INTO tasks (project_id, repo_id, workflow_version_id, title, inputs, requester_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetTask :one
SELECT * FROM tasks WHERE id = $1;

-- name: ListTasks :many
-- The inner JOIN LATERAL is deliberate, not an oversight: intake inserts a
-- task and its first run in one transaction (durable-before-acknowledge),
-- so a run-less task is unrepresentable and no row can be dropped here.
SELECT sqlc.embed(t), r.id AS latest_run_id, r.status AS latest_run_status
FROM tasks t
JOIN LATERAL (
    SELECT id, status FROM runs WHERE task_id = t.id ORDER BY created_at DESC, id DESC LIMIT 1
) r ON true
ORDER BY t.created_at DESC;

-- name: ListTasksByProjects :many
-- Inner JOIN LATERAL for the same reason as ListTasks: intake commits the
-- task and its first run together, so every task has at least one run.
SELECT sqlc.embed(t), r.id AS latest_run_id, r.status AS latest_run_status
FROM tasks t
JOIN LATERAL (
    SELECT id, status FROM runs WHERE task_id = t.id ORDER BY created_at DESC, id DESC LIMIT 1
) r ON true
WHERE t.project_id = ANY(sqlc.arg(project_ids)::uuid[])
ORDER BY t.created_at DESC;

-- name: InsertRun :one
INSERT INTO runs (task_id, status, definition_hash, base_sha, policy_snapshot,
                  execution_budget_ms, wall_deadline, created_at)
VALUES ($1, 'pending', $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetRun :one
SELECT * FROM runs WHERE id = $1;

-- name: ListRunsForTask :many
SELECT * FROM runs WHERE task_id = $1 ORDER BY created_at DESC, id DESC;

-- name: LatestRunForUpdate :one
SELECT * FROM runs WHERE task_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1 FOR UPDATE;

-- name: GetRunForUpdate :one
SELECT * FROM runs WHERE id = $1 FOR UPDATE;

-- name: ListPendingRunIDs :many
SELECT id FROM runs WHERE status = 'pending' ORDER BY created_at LIMIT $1;

-- name: CASRunStatus :execrows
UPDATE runs SET status = sqlc.arg(to_status)
WHERE id = $1 AND status = sqlc.arg(from_status);

-- name: StartRun :execrows
UPDATE runs SET status = 'running', started_at = $2
WHERE id = $1 AND status = 'pending';

-- name: FinishRun :execrows
UPDATE runs SET status = sqlc.arg(to_status), outcome = $2, finished_at = $3
WHERE id = $1 AND status IN ('pending', 'running', 'waiting');

-- name: AddRunUsage :one
-- GREATEST clamps a negative service-clock delta (reconciler adoption
-- across DB-roundtripped timestamps; no monotonic clock) so it can never
-- violate runs_execution_used_ms_check and roll back an adoption.
UPDATE runs SET execution_used_ms = LEAST(execution_used_ms + GREATEST(sqlc.arg(delta_ms), 0), 9223372036854775807)
WHERE id = $1
RETURNING execution_used_ms, execution_budget_ms;

-- name: ListRunsAwaitingMaterialization :many
-- Running runs that have nothing a worker could pick up. Such a run is either
-- finished (every node succeeded) or blocked behind a gate decision that just
-- landed — a decision is not a phase evaluation, so nothing else would ever
-- pick those up.
--
-- Written as an anti-join rather than recomputing readiness in SQL: the graph
-- lives in Go, so this only has to be a cheap over-approximation that
-- materializeReady then decides precisely. Both answers are idempotent, so a
-- run matched spuriously costs one no-op transaction.
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

-- name: ListExpiredRunIDs :many
SELECT id FROM runs
WHERE status IN ('pending', 'running', 'waiting') AND wall_deadline <= now()
LIMIT $1;

-- name: DBNow :one
-- The clock every deciding predicate reads. A caller that computes a
-- not_before, or compares one against a wall deadline, reads it from here
-- inside its own transaction so both sides of the comparison are DB
-- timestamps and app-clock skew cannot move the boundary.
SELECT now()::timestamptz;

-- name: InsertPhase :one
INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, chargeable_failures,
                        not_before, generation, status, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8)
RETURNING *;

-- name: ClaimPhase :one
-- The attempt bound is chargeable_failures < max_attempts, not the attempt
-- ordinal: a cap wait or a held lease requeues without charging, so the
-- ordinal outruns max_attempts on purpose (spec §4.3). not_before parks a
-- delayed retry until the DATABASE clock passes it — same clock as the
-- deadline predicates, so nothing app-side can move the boundary (§4.1).
WITH candidate AS (
    SELECT pr.id, r.wall_deadline, r.execution_budget_ms, r.execution_used_ms
    FROM phase_runs pr
    JOIN runs r ON r.id = pr.run_id
    WHERE pr.status = 'pending'
      AND pr.chargeable_failures < pr.max_attempts
      AND (pr.not_before IS NULL OR pr.not_before <= now())
      AND r.status = 'running'
      AND r.wall_deadline > now()
      AND r.execution_used_ms < r.execution_budget_ms
    ORDER BY pr.created_at
    FOR UPDATE OF pr SKIP LOCKED
    LIMIT 1
)
UPDATE phase_runs
SET status = 'running', worker_id = $1,
    leased_until = now() + make_interval(secs => sqlc.arg(lease_seconds)::float8),
    claimed_at = $2
FROM candidate
WHERE phase_runs.id = candidate.id
RETURNING phase_runs.*, candidate.wall_deadline AS run_wall_deadline,
          candidate.execution_budget_ms, candidate.execution_used_ms;

-- name: HeartbeatPhase :execrows
UPDATE phase_runs SET leased_until = now() + make_interval(secs => sqlc.arg(lease_seconds)::float8)
WHERE id = $1 AND status = 'running' AND worker_id = $2;

-- name: FinishPhase :execrows
UPDATE phase_runs SET status = sqlc.arg(to_status), outcome = $2, finished_at = $3
WHERE id = $1 AND status = sqlc.arg(from_status) AND (worker_id = sqlc.arg(worker_id) OR sqlc.arg(worker_id) = '');

-- name: ListExpiredLeaseIDs :many
SELECT id FROM phase_runs WHERE status = 'running' AND leased_until <= now() LIMIT $1;

-- name: MarkPhaseReconciling :one
-- The run's wall deadline rides along because reconciliation now adopts
-- typed waits (a cap window with its reset): parking one past the deadline
-- would leave a row that could never legally run when it woke, so the
-- reconcile path needs the same boundary the claim path reads (§4.1).
UPDATE phase_runs SET status = 'reconciling'
WHERE phase_runs.id = $1 AND phase_runs.status = 'running' AND phase_runs.leased_until <= now()
RETURNING phase_runs.*,
          (SELECT r.wall_deadline FROM runs r WHERE r.id = phase_runs.run_id) AS run_wall_deadline;

-- name: MarkPhaseReconcilingPastDeadline :one
-- The deadline sweep's variant of MarkPhaseReconciling: fenced by the RUN's
-- expired wall deadline (db clock) instead of the phase lease — a live lease
-- must not shield a phase whose run is already past its deadline (spec §2.8).
-- The worker's own evaluate loses its FinishPhase CAS against this, which is
-- what stops it committing a result the run is no longer entitled to.
UPDATE phase_runs SET status = 'reconciling'
WHERE phase_runs.id = $1 AND phase_runs.status = 'running'
  AND EXISTS (SELECT 1 FROM runs r WHERE r.id = phase_runs.run_id AND r.wall_deadline <= now())
RETURNING phase_runs.*,
          (SELECT r.wall_deadline FROM runs r WHERE r.id = phase_runs.run_id) AS run_wall_deadline;

-- name: ClaimStuckReconciling :many
-- Rows whose owner died between marking 'reconciling' and evaluating:
-- the lease sweep can't see them (its predicate is status = 'running').
-- Extending the lease is the CAS that makes concurrent sweepers safe —
-- one winner per row per grace window.
UPDATE phase_runs SET leased_until = now() + make_interval(secs => @lease_seconds::float8)
WHERE id IN (
    SELECT id FROM phase_runs
    WHERE status = 'reconciling'
      AND leased_until < now() - make_interval(secs => @grace_seconds::float8)
    ORDER BY leased_until
    LIMIT @max_rows FOR UPDATE SKIP LOCKED
)
RETURNING phase_runs.*,
          (SELECT r.wall_deadline FROM runs r WHERE r.id = phase_runs.run_id) AS run_wall_deadline;

-- name: SupersedeLivePhases :many
UPDATE phase_runs SET status = 'superseded', outcome = $2, finished_at = $3
WHERE run_id = $1 AND status IN ('pending', 'running', 'waiting_decision', 'reconciling')
RETURNING id, node_id, attempt;

-- name: GetPhase :one
SELECT * FROM phase_runs WHERE id = $1;

-- name: GetPhaseByRef :one
-- The reverse lookup recovery needs: a credential lease records only the
-- attempt ref, and the janitor must know whether the phase behind it is still
-- live before it takes custody of that credential.
SELECT * FROM phase_runs WHERE run_id = $1 AND node_id = $2 AND attempt = $3;

-- name: ListPhasesForRun :many
SELECT * FROM phase_runs WHERE run_id = $1 ORDER BY created_at, node_id, attempt;

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

-- name: InsertArtifact :one
INSERT INTO artifacts (id, run_id, node_id, attempt, generation, type, payload, parent_ids, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetArtifactByID :one
-- The publish path's read: an authorization names ONE artifact id and the
-- publication must carry that artifact's diff, not whatever is newest for the
-- node now. Addressing it by id is what makes the approved thing and the
-- published thing the same thing.
SELECT * FROM artifacts WHERE id = $1;

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

-- name: ListSeedArtifacts :many
-- What a revision handed this node. attempt = 0 is unreachable for a
-- produced artifact (phase attempts start at 1), so the ordinal alone
-- distinguishes a control-plane seed from a harness output. Only the newest
-- generation's seeds are returned: an older round's note is spent.
SELECT * FROM artifacts
WHERE run_id = $1 AND node_id = $2 AND attempt = 0
  AND generation = (SELECT generation FROM runs WHERE id = $1)
ORDER BY type, id;

-- name: ListArtifactsForRun :many
-- Metadata only: payload is deliberately absent, and generation is present
-- because after a revision a node has one artifact per round and a reader
-- cannot tell them apart without it.
SELECT id, run_id, node_id, attempt, generation, type, parent_ids, created_at
FROM artifacts WHERE run_id = $1 ORDER BY created_at, id;

-- name: LockEventCounter :one
SELECT next_id FROM run_event_counter WHERE onerow FOR UPDATE;

-- name: AdvanceEventCounter :exec
UPDATE run_event_counter SET next_id = next_id + sqlc.arg(n) WHERE onerow;

-- name: InsertRunEvent :exec
INSERT INTO run_events (id, run_id, project_id, kind, payload, occurred_at, correlation_id)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: InsertOutbox :exec
INSERT INTO outbox (event_id, kind, payload, created_at)
VALUES ($1, $2, $3, $4);

-- name: ListRunEventsAfter :many
SELECT * FROM run_events WHERE run_id = $1 AND id > $2 ORDER BY id LIMIT $3;

-- name: CountOutbox :one
SELECT count(*) FROM outbox;

-- name: PruneOutbox :execrows
DELETE FROM outbox WHERE created_at < $1;

-- name: ListTerminalRunIDsAmong :many
SELECT id FROM runs
WHERE id = ANY(@ids::uuid[]) AND status IN ('succeeded', 'failed', 'superseded', 'dead');

-- name: GetRunProjectID :one
SELECT t.project_id FROM runs r JOIN tasks t ON t.id = r.task_id WHERE r.id = $1;

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
WHERE phase_runs.id = $1 AND phase_runs.status = 'waiting_decision'
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

-- name: ListGateDecisionsForRun :many
-- The run projection's decided gates, keyed by phase_run_id so a decision
-- lands on the attempt row that was actually decided. Keying on node_id would
-- attach every round's decision to every round's row: after a revision both
-- gate attempts carry the same node id.
--
-- The approver's address is joined because a run page renders "approved by"
-- and the uuid is not readable; the id rides along for the audit trail. Both
-- are projected under names that say which is which.
SELECT gd.*, u.email AS acted_by_email
FROM gate_decisions gd
JOIN users u ON u.id = gd.acted_by
WHERE gd.run_id = $1
ORDER BY gd.acted_at;

-- name: ListPublishIntentsForRun :many
SELECT * FROM publish_intents WHERE run_id = $1 ORDER BY created_at;

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
WHERE approved_patches.id = $1 AND approved_patches.status = 'valid'
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
                             base_sha, tree_hash, title, body, author_email,
                             commit_message, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, 'pending', $17, $17)
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
--
-- absent_since is cleared in the SAME statement, and that is not tidiness.
-- Settlement stamps absent_since against the marker the intent's STATUS makes
-- uncertain — the branch while 'pending', the pull request once 'pushed'. A
-- stamp taken while pending and left behind would already be older than the
-- grace when the pull-request half first probes, so the very first PR-absence
-- sighting would halt an intent whose create call may still be on the wire.
-- The receipt is also the proof the stamp was wrong: the branch turned up.
UPDATE publish_intents SET status = 'pushed', head_sha = $2, absent_since = NULL, updated_at = $3
WHERE id = $1 AND status = 'pending';

-- name: RecordPRReceipt :execrows
-- absent_since is cleared for the same reason RecordPushReceipt clears it: a
-- settled intent carries no open question about a missing marker.
UPDATE publish_intents SET status = 'published', pr_number = $2, pr_url = $3,
                           absent_since = NULL, updated_at = $4
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

-- name: ListUnsettledPublishIntents :many
-- Intents that may correspond to an effect on the forge nobody has written
-- down. 'published' and 'halted' are settled by definition.
SELECT * FROM publish_intents
WHERE run_id = $1 AND status IN ('pending', 'pushed');

-- The three statements below carry the NEGATIVE answer for an expired run's
-- settlement: a marker that is not there. All three are :execrows and CAS on
-- status, so a settlement racing a live driver's receipt loses instead of
-- overwriting it.

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

-- name: HaltUnverifiablePublication :execrows
-- The give-up bound on a publication nobody can verify. Settlement never
-- guesses — an unreachable forge defers to the next tick — but "defer" with no
-- bound is its own durable answer: an outage, or a credential revoked after the
-- intent was created, would leave the run alive past its deadline forever,
-- holding a phase slot and re-probing on every tick.
--
-- The age is measured against the RUN's wall_deadline, not against this row.
-- updated_at is bumped by both absence statements and created_at would halt on
-- the first blip for any run with a long wall deadline; the deadline is the one
-- timestamp that says how long this run has been owed an answer.
UPDATE publish_intents SET status = 'halted', halt_reason = sqlc.arg(halt_reason), updated_at = now()
WHERE publish_intents.id = sqlc.arg(id) AND publish_intents.status = sqlc.arg(status)
  AND EXISTS (
      SELECT 1 FROM runs r
      WHERE r.id = publish_intents.run_id
        AND r.wall_deadline <= now() - make_interval(secs => sqlc.arg(unverifiable_seconds)::int)
  );

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
           SELECT 1 FROM phase_runs gate
           WHERE gate.run_id = @run_id AND gate.status = 'waiting_decision'
       ) AND NOT EXISTS (
           SELECT 1 FROM phase_runs live
           WHERE live.run_id = @run_id
             AND live.id <> @exclude_phase
             AND live.status IN ('pending', 'running', 'reconciling')
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
UPDATE phase_runs SET status = 'superseded', outcome = sqlc.arg(outcome), finished_at = sqlc.arg(finished_at)
WHERE run_id = sqlc.arg(run_id) AND node_id = ANY(sqlc.arg(node_ids)::text[])
  AND status IN ('pending', 'running', 'waiting_decision', 'reconciling', 'succeeded')
RETURNING id, node_id, attempt;
