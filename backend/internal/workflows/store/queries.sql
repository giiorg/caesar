-- name: CreateWorkflow :one
INSERT INTO workflows (name, description)
VALUES ($1, $2)
RETURNING *;

-- name: GetWorkflow :one
SELECT w.id, w.name, w.description, w.created_at, w.updated_at,
       COALESCE(MAX(v.version), 0)::int AS latest_version
FROM workflows w
LEFT JOIN workflow_versions v ON v.workflow_id = w.id
WHERE w.id = $1
GROUP BY w.id;

-- name: ListWorkflows :many
SELECT w.id, w.name, w.description, w.created_at, w.updated_at,
       COALESCE(MAX(v.version), 0)::int AS latest_version
FROM workflows w
LEFT JOIN workflow_versions v ON v.workflow_id = w.id
GROUP BY w.id
ORDER BY w.name;

-- name: GetWorkflowForUpdate :one
SELECT id, name, description, created_at, updated_at
FROM workflows
WHERE id = $1
FOR UPDATE;

-- name: UpdateWorkflow :one
UPDATE workflows
SET name = $2, description = $3, updated_at = $4
WHERE id = $1
RETURNING *;

-- name: DeleteWorkflow :execrows
DELETE FROM workflows WHERE id = $1;

-- name: GetVersionByHash :one
SELECT * FROM workflow_versions
WHERE workflow_id = $1 AND content_hash = $2;

-- name: NextVersionNumber :one
SELECT (COALESCE(MAX(version), 0) + 1)::int AS next_version
FROM workflow_versions
WHERE workflow_id = $1;

-- name: InsertVersion :one
INSERT INTO workflow_versions (workflow_id, version, content_hash, document)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListVersions :many
SELECT id, workflow_id, version, content_hash, created_at
FROM workflow_versions
WHERE workflow_id = $1
ORDER BY version DESC;

-- name: GetVersion :one
SELECT * FROM workflow_versions
WHERE workflow_id = $1 AND version = $2;
