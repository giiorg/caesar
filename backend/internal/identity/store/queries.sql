-- Identity store queries. sqlc generates the Go into this package:
-- `task db:sqlc` after any change here or in schema.sql.

-- name: CreateUser :one
INSERT INTO users (email, password_hash, role)
VALUES ($1, $2, $3)
RETURNING id, email, password_hash, role, active, created_at, updated_at;

-- name: GetUserByEmail :one
SELECT id, email, password_hash, role, active, created_at, updated_at
FROM users
WHERE lower(email) = lower($1);

-- name: GetUserByID :one
SELECT id, email, password_hash, role, active, created_at, updated_at
FROM users
WHERE id = $1;

-- name: ListUsers :many
SELECT id, email, password_hash, role, active, created_at, updated_at
FROM users
ORDER BY created_at, id;

-- name: UpdateUserRoleActive :one
UPDATE users
SET role = $2, active = $3, updated_at = $4
WHERE id = $1
RETURNING id, email, password_hash, role, active, created_at, updated_at;

-- name: UpdateUserPassword :exec
UPDATE users
SET password_hash = $2, updated_at = $3
WHERE id = $1;

-- name: CountAdmins :one
SELECT count(*) FROM users WHERE role = 'admin';

-- name: CountActiveAdmins :one
SELECT count(*) FROM users WHERE role = 'admin' AND active;

-- name: GrantsForUser :many
SELECT project_id, level FROM grants WHERE user_id = $1 ORDER BY project_id;

-- name: GetGrantLevel :one
SELECT level FROM grants WHERE user_id = $1 AND project_id = $2;

-- name: InsertSession :one
INSERT INTO sessions (user_id, token_hash, created_at, last_seen_at, expires_at)
VALUES ($1, $2, $3, $3, $4)
RETURNING id;

-- name: GetSessionByTokenHash :one
SELECT s.id, s.user_id, s.created_at, s.last_seen_at, s.expires_at, s.revoked_at,
       u.email, u.role, u.active
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = $1;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = $2 WHERE id = $1;

-- name: RevokeSession :exec
UPDATE sessions SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeUserSessions :exec
UPDATE sessions SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL;

-- name: GetLoginRateForUpdate :one
SELECT scope, failures, window_started_at, locked_until
FROM login_rate WHERE scope = $1
FOR UPDATE;

-- name: UpsertLoginRate :exec
INSERT INTO login_rate (scope, failures, window_started_at, locked_until)
VALUES ($1, $2, $3, $4)
ON CONFLICT (scope) DO UPDATE
SET failures = excluded.failures,
    window_started_at = excluded.window_started_at,
    locked_until = excluded.locked_until;

-- name: DeleteLoginRate :exec
DELETE FROM login_rate WHERE scope = $1;

-- name: EnsureLoginRate :exec
INSERT INTO login_rate (scope, failures, window_started_at, locked_until)
VALUES ($1, 0, $2, NULL)
ON CONFLICT (scope) DO NOTHING;

-- name: PurgeStaleLoginRate :execrows
-- Reclaims rows whose lock (if any) has expired and whose window is older
-- than the retention cutoff. SKIP LOCKED so this never blocks on a row a
-- concurrent login holds FOR UPDATE.
DELETE FROM login_rate
WHERE scope IN (
    SELECT scope FROM login_rate
    WHERE (locked_until IS NULL OR locked_until < sqlc.arg(now)::timestamptz)
      AND window_started_at < sqlc.arg(cutoff)::timestamptz
    FOR UPDATE SKIP LOCKED
);
