-- +goose Up
-- create index "audit_entries_read_idx" to table: "audit_entries"
CREATE INDEX "audit_entries_read_idx" ON "public"."audit_entries" ("occurred_at" DESC, "id" DESC);

-- +goose Down
-- reverse: create index "audit_entries_read_idx" to table: "audit_entries"
DROP INDEX "public"."audit_entries_read_idx";
