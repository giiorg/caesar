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
INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, chargeable_failures, not_before, status, created_at)
VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7)
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
WHERE run_id = $1 AND status IN ('pending', 'running', 'reconciling')
RETURNING id, node_id, attempt;

-- name: GetPhase :one
SELECT * FROM phase_runs WHERE id = $1;

-- name: ListPhasesForRun :many
SELECT * FROM phase_runs WHERE run_id = $1 ORDER BY created_at, node_id, attempt;

-- name: ListSucceededNodes :many
SELECT node_id FROM phase_runs WHERE run_id = $1 AND status = 'succeeded';

-- name: InsertArtifact :one
INSERT INTO artifacts (id, run_id, node_id, attempt, type, payload, parent_ids, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetNodeArtifact :one
SELECT * FROM artifacts
WHERE run_id = $1 AND node_id = $2 AND type = $3
ORDER BY attempt DESC, created_at DESC
LIMIT 1;

-- name: ListArtifactsForRun :many
SELECT id, run_id, node_id, attempt, type, parent_ids, created_at
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
