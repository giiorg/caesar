-- +goose Up
-- create index "runs_created_id_idx" to table: "runs"
CREATE INDEX "runs_created_id_idx" ON "public"."runs" ("created_at" DESC, "id" DESC);

-- +goose Down
-- reverse: create index "runs_created_id_idx" to table: "runs"
DROP INDEX "public"."runs_created_id_idx";
