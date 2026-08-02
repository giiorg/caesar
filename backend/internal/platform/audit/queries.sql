-- name: InsertAuditEntry :exec
INSERT INTO audit_entries (occurred_at, actor_user_id, action, subject_kind, subject_id, correlation_id, detail)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListAuditEntriesByAction :many
SELECT id, occurred_at, actor_user_id, action, subject_kind, subject_id, correlation_id, detail
FROM audit_entries
WHERE action = $1
ORDER BY id;
