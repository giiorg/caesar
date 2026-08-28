-- name: InsertAuditEntry :exec
INSERT INTO audit_entries (occurred_at, actor_user_id, action, subject_kind, subject_id, correlation_id, detail)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListAuditEntriesByAction :many
SELECT id, occurred_at, actor_user_id, action, subject_kind, subject_id, correlation_id, detail
FROM audit_entries
WHERE action = $1
ORDER BY id;

-- name: ListAuditPage :many
-- Newest-first keyset, one page. The row-value comparison names the same two
-- columns as the ORDER BY, in the same direction, which is what makes the
-- cursor and the sort key incapable of disagreeing: written out by hand as
-- `occurred_at < $1 OR (occurred_at = $1 AND id < $2)`, a later edit to one
-- half silently drops or repeats the rows that share a timestamp — and here
-- they are not an edge case, because one transaction stamps every entry it
-- emits from the same clock reading.
--
-- A null cursor means "the first page": both halves are narg and only the
-- first is tested, because they are set together or not at all.
SELECT id, occurred_at, actor_user_id, action, subject_kind, subject_id, correlation_id, detail
FROM audit_entries
WHERE (sqlc.narg(cursor_occurred_at)::timestamptz IS NULL
       OR (occurred_at, id) < (sqlc.narg(cursor_occurred_at)::timestamptz, sqlc.narg(cursor_id)::bigint))
ORDER BY occurred_at DESC, id DESC
LIMIT sqlc.arg(page_limit);
