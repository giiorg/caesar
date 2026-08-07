-- +goose Up
-- create "credential_leases" table
CREATE TABLE "public"."credential_leases" ("credential_id" uuid NOT NULL, "attempt_ref" text NOT NULL, "epoch" bigint NOT NULL DEFAULT 1, "secret_version" bigint NOT NULL, "acquired_at" timestamptz NOT NULL DEFAULT now(), "heartbeat_at" timestamptz NOT NULL DEFAULT now(), PRIMARY KEY ("credential_id"), CONSTRAINT "credential_leases_credential_id_fkey" FOREIGN KEY ("credential_id") REFERENCES "public"."credentials" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "credential_leases_epoch_check" CHECK (epoch >= 1));
-- create index "credential_leases_attempt_idx" to table: "credential_leases"
CREATE INDEX "credential_leases_attempt_idx" ON "public"."credential_leases" ("attempt_ref");
-- create "credential_sync_receipts" table
CREATE TABLE "public"."credential_sync_receipts" ("credential_id" uuid NOT NULL, "attempt_ref" text NOT NULL, "epoch" bigint NOT NULL, "from_version" bigint NOT NULL, "to_version" bigint NOT NULL, "committed_at" timestamptz NOT NULL DEFAULT now(), PRIMARY KEY ("credential_id", "attempt_ref"), CONSTRAINT "credential_sync_receipts_credential_id_fkey" FOREIGN KEY ("credential_id") REFERENCES "public"."credentials" ("id") ON UPDATE NO ACTION ON DELETE CASCADE);

-- +goose Down
-- reverse: create "credential_sync_receipts" table
DROP TABLE "public"."credential_sync_receipts";
-- reverse: create index "credential_leases_attempt_idx" to table: "credential_leases"
DROP INDEX "public"."credential_leases_attempt_idx";
-- reverse: create "credential_leases" table
DROP TABLE "public"."credential_leases";
