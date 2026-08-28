-- +goose Up
-- create index "gate_decisions_acted_idx" to table: "gate_decisions"
CREATE INDEX "gate_decisions_acted_idx" ON "public"."gate_decisions" ("acted_at" DESC, "id" DESC);

-- +goose Down
-- reverse: create index "gate_decisions_acted_idx" to table: "gate_decisions"
DROP INDEX "public"."gate_decisions_acted_idx";
