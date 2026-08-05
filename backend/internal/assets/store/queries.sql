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
INSERT INTO credentials (id, name, kind, key_id, wrapped_dek, ciphertext, fingerprint)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, name, kind, key_id, secret_version, last_used_at, last_auth_expired_at, created_at, updated_at;

-- name: ListCredentials :many
SELECT id, name, kind, key_id, secret_version, last_used_at, last_auth_expired_at, created_at, updated_at
FROM credentials ORDER BY name;

-- name: GetCredential :one
SELECT id, name, kind, key_id, secret_version, last_used_at, last_auth_expired_at, created_at, updated_at
FROM credentials WHERE id = $1;

-- UseCredentialRow is the use-time read: it stamps last_used_at in the
-- same statement that hands back the envelope, so the freshness view can
-- never drift from what was actually opened.
-- name: UseCredentialRow :one
UPDATE credentials SET last_used_at = $2
WHERE id = $1
RETURNING id, name, kind, key_id, wrapped_dek, ciphertext, secret_version, created_at, updated_at;

-- name: ListCredentialRowsForUpdate :many
SELECT id, name, kind, key_id, wrapped_dek, ciphertext, created_at, updated_at
FROM credentials ORDER BY id
FOR UPDATE;

-- UpdateCredentialSecret is the operator-facing rotation: new plaintext,
-- so the version advances and the fingerprint is recomputed. Rekeying
-- re-wraps the same plaintext and goes through RewrapCredential instead.
-- name: UpdateCredentialSecret :one
UPDATE credentials
SET key_id = $2, wrapped_dek = $3, ciphertext = $4, fingerprint = $5,
    secret_version = secret_version + 1, updated_at = $6
WHERE id = $1
RETURNING id, name, kind, key_id, secret_version, last_used_at, last_auth_expired_at, created_at, updated_at;

-- RewrapCredential moves a row to another master key. The plaintext is
-- unchanged, so secret_version, fingerprint, and updated_at ("when the
-- operator last supplied the secret") all stay put.
-- name: RewrapCredential :exec
UPDATE credentials SET key_id = $2, wrapped_dek = $3, ciphertext = $4
WHERE id = $1;

-- name: MarkCredentialAuthExpired :execrows
UPDATE credentials SET last_auth_expired_at = $2 WHERE id = $1;

-- name: DeleteCredential :execrows
DELETE FROM credentials WHERE id = $1;

-- AcquireCredentialRow is the lease path's counterpart to UseCredentialRow:
-- it row-locks the credential, hands back the envelope and the version the
-- lease is about to grant, and stamps last_used_at in the same statement.
-- Leased kinds never pass through UseCredentialRow, and the freshness view
-- (spec §5) must still see them. Taking the credential lock before the
-- lease row pins the aggregate's lock order.
-- name: AcquireCredentialRow :one
UPDATE credentials SET last_used_at = now()
WHERE id = $1
RETURNING id, kind, key_id, wrapped_dek, ciphertext, secret_version;

-- name: GetCredentialRowForUpdate :one
SELECT id, kind, key_id, secret_version
FROM credentials WHERE id = $1
FOR UPDATE;

-- CredentialFingerprintHeldByOther is the sync-back pre-check. Letting the
-- partial unique index raise 23505 instead would abort the transaction, and
-- the discard audit could not be written after that without savepoints.
-- name: CredentialFingerprintHeldByOther :one
SELECT EXISTS(SELECT 1 FROM credentials WHERE fingerprint = $2 AND id <> $1);

-- UpdateCredentialSecretCAS is the sync-back write: it lands only if the
-- store still holds the version the attempt materialized, so an operator
-- rotation mid-attempt makes the stale refresh lose (spec §2.6).
-- name: UpdateCredentialSecretCAS :execrows
UPDATE credentials
SET key_id = $2, wrapped_dek = $3, ciphertext = $4, fingerprint = $5,
    secret_version = secret_version + 1, updated_at = now()
WHERE id = $1 AND secret_version = $6;

-- name: AcquireLease :one
INSERT INTO credential_leases (credential_id, attempt_ref, secret_version)
VALUES ($1, $2, $3)
ON CONFLICT (credential_id) DO NOTHING
RETURNING *;

-- name: GetLease :one
SELECT * FROM credential_leases WHERE credential_id = $1;

-- GetLeaseForUpdate fences the sync-back commit against a concurrent
-- takeover: without the row lock a worker could read epoch N, lose the
-- lease to a reconciler, and still commit its stale snapshot.
-- name: GetLeaseForUpdate :one
SELECT * FROM credential_leases WHERE credential_id = $1
FOR UPDATE;

-- name: GetLeaseByAttempt :one
SELECT * FROM credential_leases WHERE attempt_ref = $1;

-- RebindLeaseVersion re-points an idempotent re-acquire at the version the
-- caller is about to be handed: the store may have rotated since the first
-- acquire, and lease.secret_version must match the delivered snapshot.
-- name: RebindLeaseVersion :one
UPDATE credential_leases SET secret_version = $3
WHERE credential_id = $1 AND attempt_ref = $2
RETURNING *;

-- name: HeartbeatLease :execrows
UPDATE credential_leases SET heartbeat_at = now()
WHERE credential_id = $1 AND attempt_ref = $2 AND epoch = $3;

-- name: TakeoverLease :one
UPDATE credential_leases SET epoch = epoch + 1
WHERE credential_id = $1 AND attempt_ref = $2
RETURNING *;

-- name: ReleaseLease :execrows
DELETE FROM credential_leases
WHERE credential_id = $1 AND attempt_ref = $2 AND epoch = $3;

-- ListStaleLeases finds leases whose owner stopped heartbeating, for the
-- reconciliation sweep. Staleness never releases a lease by itself (spec
-- §2.1): only a concluded reconciliation does.
-- name: ListStaleLeases :many
SELECT * FROM credential_leases
WHERE heartbeat_at < now() - make_interval(secs => sqlc.arg(stale_seconds)::double precision)
ORDER BY credential_id;

-- name: GetSyncReceipt :one
SELECT * FROM credential_sync_receipts
WHERE credential_id = $1 AND attempt_ref = $2;

-- name: InsertSyncReceipt :exec
INSERT INTO credential_sync_receipts (credential_id, attempt_ref, epoch, from_version, to_version)
VALUES ($1, $2, $3, $4, $5);

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
