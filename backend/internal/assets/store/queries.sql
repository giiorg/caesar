-- Assets store queries. sqlc generates the Go into this package:
-- `task db:sqlc` after any change here or in schema.sql.

-- name: CreateProject :one
INSERT INTO projects (name, description)
VALUES ($1, $2)
RETURNING id, name, description, created_at, updated_at;

-- name: ListProjects :many
SELECT id, name, description, created_at, updated_at
FROM projects ORDER BY name;

-- name: ListProjectsByIDs :many
SELECT id, name, description, created_at, updated_at
FROM projects WHERE id = ANY(sqlc.arg(ids)::uuid[]) ORDER BY name;

-- name: GetProject :one
SELECT id, name, description, created_at, updated_at
FROM projects WHERE id = $1;

-- name: UpdateProject :one
UPDATE projects SET name = $2, description = $3, updated_at = $4
WHERE id = $1
RETURNING id, name, description, created_at, updated_at;

-- name: DeleteProject :execrows
DELETE FROM projects WHERE id = $1;

-- name: ProjectExists :one
SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1);
