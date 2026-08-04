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

-- name: CreateCredential :one
INSERT INTO credentials (id, name, kind, key_id, wrapped_dek, ciphertext)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, name, kind, key_id, created_at, updated_at;

-- name: ListCredentials :many
SELECT id, name, kind, key_id, created_at, updated_at
FROM credentials ORDER BY name;

-- name: GetCredential :one
SELECT id, name, kind, key_id, created_at, updated_at
FROM credentials WHERE id = $1;

-- name: GetCredentialRow :one
SELECT id, name, kind, key_id, wrapped_dek, ciphertext, created_at, updated_at
FROM credentials WHERE id = $1;

-- name: ListCredentialRowsForUpdate :many
SELECT id, name, kind, key_id, wrapped_dek, ciphertext, created_at, updated_at
FROM credentials ORDER BY id
FOR UPDATE;

-- name: UpdateCredentialSecret :one
UPDATE credentials SET key_id = $2, wrapped_dek = $3, ciphertext = $4, updated_at = $5
WHERE id = $1
RETURNING id, name, kind, key_id, created_at, updated_at;

-- name: DeleteCredential :execrows
DELETE FROM credentials WHERE id = $1;

-- name: CreateRepo :one
INSERT INTO repos (project_id, forge_kind, slug, default_branch, credential_id)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, project_id, forge_kind, slug, default_branch, credential_id, created_at, updated_at;

-- name: ListReposByProject :many
SELECT id, project_id, forge_kind, slug, default_branch, credential_id, created_at, updated_at
FROM repos WHERE project_id = $1 ORDER BY slug;

-- name: GetRepo :one
SELECT id, project_id, forge_kind, slug, default_branch, credential_id, created_at, updated_at
FROM repos WHERE id = $1;

-- name: UpdateRepo :one
UPDATE repos SET slug = $2, default_branch = $3, credential_id = $4, updated_at = $5
WHERE id = $1
RETURNING id, project_id, forge_kind, slug, default_branch, credential_id, created_at, updated_at;

-- name: DeleteRepo :execrows
DELETE FROM repos WHERE id = $1;

-- name: RegisterHarness :one
INSERT INTO harnesses (name, kind, image_ref, provider_endpoint, credential_id, defaults)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, name, kind, image_ref, provider_endpoint, credential_id, defaults, created_at, updated_at;

-- name: ListHarnesses :many
SELECT id, name, kind, image_ref, provider_endpoint, credential_id, defaults, created_at, updated_at
FROM harnesses ORDER BY name;

-- name: GetHarness :one
SELECT id, name, kind, image_ref, provider_endpoint, credential_id, defaults, created_at, updated_at
FROM harnesses WHERE id = $1;

-- name: GetHarnessByName :one
SELECT * FROM harnesses WHERE name = $1;

-- name: UpdateHarness :one
UPDATE harnesses SET name = $2, image_ref = $3, provider_endpoint = $4, credential_id = $5, defaults = $6, updated_at = $7
WHERE id = $1
RETURNING id, name, kind, image_ref, provider_endpoint, credential_id, defaults, created_at, updated_at;

-- name: DeleteHarness :execrows
DELETE FROM harnesses WHERE id = $1;
