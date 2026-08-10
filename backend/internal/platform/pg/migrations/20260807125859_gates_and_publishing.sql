-- +goose Up
-- modify "artifacts" table
ALTER TABLE "public"."artifacts" DROP CONSTRAINT "artifacts_type_check", ADD CONSTRAINT "artifacts_type_check" CHECK (type = ANY (ARRAY['plan'::text, 'patch'::text, 'review_verdict'::text, 'log_ref'::text, 'failure_snapshot'::text, 'publication'::text, 'gate_note'::text])), ADD CONSTRAINT "artifacts_generation_check" CHECK (generation >= 1), ADD COLUMN "generation" integer NOT NULL DEFAULT 1;
-- create index "artifacts_node_lookup_idx" to table: "artifacts"
CREATE INDEX "artifacts_node_lookup_idx" ON "public"."artifacts" ("run_id", "node_id", "type", "generation" DESC, "attempt" DESC, "created_at" DESC, "id" DESC);
-- drop index "phase_runs_live_key" from table: "phase_runs"
DROP INDEX "public"."phase_runs_live_key";
-- modify "phase_runs" table
ALTER TABLE "public"."phase_runs" DROP CONSTRAINT "phase_runs_status_check", ADD CONSTRAINT "phase_runs_status_check" CHECK (status = ANY (ARRAY['pending'::text, 'running'::text, 'waiting_decision'::text, 'reconciling'::text, 'succeeded'::text, 'failed'::text, 'dead'::text, 'superseded'::text])), ADD CONSTRAINT "phase_runs_generation_check" CHECK (generation >= 1), ADD COLUMN "generation" integer NOT NULL DEFAULT 1;
-- create index "phase_runs_live_key" to table: "phase_runs"
CREATE UNIQUE INDEX "phase_runs_live_key" ON "public"."phase_runs" ("run_id", "node_id") WHERE (status = ANY (ARRAY['pending'::text, 'running'::text, 'waiting_decision'::text, 'reconciling'::text]));
-- modify "runs" table
ALTER TABLE "public"."runs" ADD CONSTRAINT "runs_generation_check" CHECK (generation >= 1), ADD COLUMN "generation" integer NOT NULL DEFAULT 1;
-- create "gate_decisions" table
CREATE TABLE "public"."gate_decisions" ("id" uuid NOT NULL DEFAULT uuidv7(), "phase_run_id" uuid NOT NULL, "run_id" uuid NOT NULL, "node_id" text NOT NULL, "generation" integer NOT NULL, "decision" text NOT NULL, "examined_artifact_id" uuid NOT NULL, "note" text NOT NULL DEFAULT '', "acted_by" uuid NOT NULL, "self_approval" boolean NOT NULL DEFAULT false, "acted_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "gate_decisions_phase_run_id_key" UNIQUE ("phase_run_id"), CONSTRAINT "gate_decisions_acted_by_fkey" FOREIGN KEY ("acted_by") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "gate_decisions_examined_artifact_id_fkey" FOREIGN KEY ("examined_artifact_id") REFERENCES "public"."artifacts" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "gate_decisions_phase_run_id_fkey" FOREIGN KEY ("phase_run_id") REFERENCES "public"."phase_runs" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "gate_decisions_run_id_fkey" FOREIGN KEY ("run_id") REFERENCES "public"."runs" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "gate_decisions_decision_check" CHECK (decision = ANY (ARRAY['approve'::text, 'request_changes'::text, 'reject'::text])), CONSTRAINT "gate_decisions_generation_check" CHECK (generation >= 1));
-- create index "gate_decisions_run_node_idx" to table: "gate_decisions"
CREATE INDEX "gate_decisions_run_node_idx" ON "public"."gate_decisions" ("run_id", "node_id");
-- create "approved_patches" table
CREATE TABLE "public"."approved_patches" ("id" uuid NOT NULL DEFAULT uuidv7(), "run_id" uuid NOT NULL, "gate_node_id" text NOT NULL, "publish_node_id" text NOT NULL, "gate_decision_id" uuid NOT NULL, "artifact_id" uuid NOT NULL, "base_sha" text NOT NULL, "tree_hash" text NOT NULL, "repo_id" uuid NOT NULL, "forge_kind" text NOT NULL, "repo_slug" text NOT NULL, "target_branch" text NOT NULL, "generation" integer NOT NULL, "status" text NOT NULL, "created_at" timestamptz NOT NULL, "settled_at" timestamptz NULL, PRIMARY KEY ("id"), CONSTRAINT "approved_patches_gate_decision_id_key" UNIQUE ("gate_decision_id"), CONSTRAINT "approved_patches_artifact_id_fkey" FOREIGN KEY ("artifact_id") REFERENCES "public"."artifacts" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "approved_patches_gate_decision_id_fkey" FOREIGN KEY ("gate_decision_id") REFERENCES "public"."gate_decisions" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "approved_patches_repo_id_fkey" FOREIGN KEY ("repo_id") REFERENCES "public"."repos" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "approved_patches_run_id_fkey" FOREIGN KEY ("run_id") REFERENCES "public"."runs" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "approved_patches_generation_check" CHECK (generation >= 1), CONSTRAINT "approved_patches_status_check" CHECK (status = ANY (ARRAY['valid'::text, 'consumed'::text, 'invalidated'::text])));
-- create index "approved_patches_live_key" to table: "approved_patches"
CREATE UNIQUE INDEX "approved_patches_live_key" ON "public"."approved_patches" ("run_id", "publish_node_id") WHERE (status = 'valid'::text);
-- create "publish_intents" table
CREATE TABLE "public"."publish_intents" ("id" uuid NOT NULL DEFAULT uuidv7(), "run_id" uuid NOT NULL, "node_id" text NOT NULL, "attempt" integer NOT NULL, "authorization_id" uuid NOT NULL, "repo_id" uuid NOT NULL, "forge_kind" text NOT NULL, "repo_slug" text NOT NULL, "credential_id" uuid NOT NULL, "branch" text NOT NULL, "target_branch" text NOT NULL, "base_sha" text NOT NULL, "tree_hash" text NOT NULL, "title" text NOT NULL, "body" text NOT NULL, "status" text NOT NULL, "head_sha" text NULL, "pr_number" bigint NULL, "pr_url" text NULL, "halt_reason" text NULL, "absent_since" timestamptz NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "publish_intents_authorization_id_key" UNIQUE ("authorization_id"), CONSTRAINT "publish_intents_run_id_node_id_key" UNIQUE ("run_id", "node_id"), CONSTRAINT "publish_intents_authorization_id_fkey" FOREIGN KEY ("authorization_id") REFERENCES "public"."approved_patches" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "publish_intents_credential_id_fkey" FOREIGN KEY ("credential_id") REFERENCES "public"."credentials" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "publish_intents_repo_id_fkey" FOREIGN KEY ("repo_id") REFERENCES "public"."repos" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "publish_intents_run_id_fkey" FOREIGN KEY ("run_id") REFERENCES "public"."runs" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "publish_intents_attempt_check" CHECK (attempt >= 1), CONSTRAINT "publish_intents_status_check" CHECK (status = ANY (ARRAY['pending'::text, 'pushed'::text, 'published'::text, 'halted'::text])));
-- create index "publish_intents_marker_key" to table: "publish_intents"
CREATE UNIQUE INDEX "publish_intents_marker_key" ON "public"."publish_intents" ("repo_id", "branch");

-- +goose Down
-- reverse: create index "publish_intents_marker_key" to table: "publish_intents"
DROP INDEX "public"."publish_intents_marker_key";
-- reverse: create "publish_intents" table
DROP TABLE "public"."publish_intents";
-- reverse: create index "approved_patches_live_key" to table: "approved_patches"
DROP INDEX "public"."approved_patches_live_key";
-- reverse: create "approved_patches" table
DROP TABLE "public"."approved_patches";
-- reverse: create index "gate_decisions_run_node_idx" to table: "gate_decisions"
DROP INDEX "public"."gate_decisions_run_node_idx";
-- reverse: create "gate_decisions" table
DROP TABLE "public"."gate_decisions";
-- reverse: modify "runs" table
-- Hand-edited: Atlas emitted DROP COLUMN before DROP CONSTRAINT, but dropping
-- the column already drops the check that depends on it, so the later DROP
-- CONSTRAINT fails with 42704. Both are one ALTER pass, executed in the order
-- written — the constraint goes first.
ALTER TABLE "public"."runs" DROP CONSTRAINT "runs_generation_check", DROP COLUMN "generation";
-- reverse: create index "phase_runs_live_key" to table: "phase_runs"
DROP INDEX "public"."phase_runs_live_key";
-- reverse: modify "phase_runs" table
-- Hand-edited: Atlas emitted DROP COLUMN before DROP CONSTRAINT, but dropping
-- the column already drops the check that depends on it, so the later DROP
-- CONSTRAINT fails with 42704. Both are one ALTER pass, executed in the order
-- written — the constraint goes first.
ALTER TABLE "public"."phase_runs" DROP CONSTRAINT "phase_runs_generation_check", DROP CONSTRAINT "phase_runs_status_check", DROP COLUMN "generation", ADD CONSTRAINT "phase_runs_status_check" CHECK (status = ANY (ARRAY['pending'::text, 'running'::text, 'reconciling'::text, 'succeeded'::text, 'failed'::text, 'dead'::text, 'superseded'::text]));
-- reverse: drop index "phase_runs_live_key" from table: "phase_runs"
CREATE UNIQUE INDEX "phase_runs_live_key" ON "public"."phase_runs" ("run_id", "node_id") WHERE (status = ANY (ARRAY['pending'::text, 'running'::text, 'reconciling'::text]));
-- reverse: create index "artifacts_node_lookup_idx" to table: "artifacts"
DROP INDEX "public"."artifacts_node_lookup_idx";
-- reverse: modify "artifacts" table
-- Hand-edited: Atlas emitted DROP COLUMN before DROP CONSTRAINT, but dropping
-- the column already drops the check that depends on it, so the later DROP
-- CONSTRAINT fails with 42704. Both are one ALTER pass, executed in the order
-- written — the constraint goes first.
ALTER TABLE "public"."artifacts" DROP CONSTRAINT "artifacts_generation_check", DROP CONSTRAINT "artifacts_type_check", DROP COLUMN "generation", ADD CONSTRAINT "artifacts_type_check" CHECK (type = ANY (ARRAY['plan'::text, 'patch'::text, 'review_verdict'::text, 'log_ref'::text, 'failure_snapshot'::text]));
