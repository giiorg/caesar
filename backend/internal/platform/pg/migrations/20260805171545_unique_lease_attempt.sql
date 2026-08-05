-- +goose Up
-- drop index "credential_leases_attempt_idx" from table: "credential_leases"
DROP INDEX "public"."credential_leases_attempt_idx";
-- create index "credential_leases_attempt_ref_key" to table: "credential_leases"
CREATE UNIQUE INDEX "credential_leases_attempt_ref_key" ON "public"."credential_leases" ("attempt_ref");

-- +goose Down
-- reverse: create index "credential_leases_attempt_ref_key" to table: "credential_leases"
DROP INDEX "public"."credential_leases_attempt_ref_key";
-- reverse: drop index "credential_leases_attempt_idx" from table: "credential_leases"
CREATE INDEX "credential_leases_attempt_idx" ON "public"."credential_leases" ("attempt_ref");
