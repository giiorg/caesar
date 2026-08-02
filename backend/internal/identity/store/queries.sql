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
