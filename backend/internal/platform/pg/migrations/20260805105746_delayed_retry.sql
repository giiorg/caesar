-- +goose Up
-- modify "phase_runs" table
ALTER TABLE "public"."phase_runs" ADD CONSTRAINT "phase_runs_chargeable_failures_check" CHECK (chargeable_failures >= 0), ADD COLUMN "chargeable_failures" integer NOT NULL DEFAULT 0, ADD COLUMN "not_before" timestamptz NULL;
-- create index "phase_runs_not_before_idx" to table: "phase_runs"
CREATE INDEX "phase_runs_not_before_idx" ON "public"."phase_runs" ("not_before") WHERE ((status = 'pending'::text) AND (not_before IS NOT NULL));

-- +goose Down
-- reverse: create index "phase_runs_not_before_idx" to table: "phase_runs"
DROP INDEX "public"."phase_runs_not_before_idx";
-- reverse: modify "phase_runs" table
-- Hand-edited: Atlas emitted an explicit DROP CONSTRAINT for the check,
-- but dropping the column it constrains already drops it, so the extra
-- clause fails with "constraint does not exist" (down round-trip test).
ALTER TABLE "public"."phase_runs" DROP COLUMN "not_before", DROP COLUMN "chargeable_failures";
