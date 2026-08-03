-- +goose Up
-- create "run_event_counter" table
CREATE TABLE "public"."run_event_counter" ("onerow" boolean NOT NULL DEFAULT true, "next_id" bigint NOT NULL, PRIMARY KEY ("onerow"), CONSTRAINT "run_event_counter_onerow_check" CHECK (onerow));
-- modify "workflow_versions" table
ALTER TABLE "public"."workflow_versions" ADD CONSTRAINT "workflow_versions_version_check" CHECK (version > 0);
-- create "tasks" table
CREATE TABLE "public"."tasks" ("id" uuid NOT NULL DEFAULT uuidv7(), "project_id" uuid NOT NULL, "repo_id" uuid NOT NULL, "workflow_version_id" uuid NOT NULL, "title" text NOT NULL, "inputs" jsonb NOT NULL DEFAULT '{}', "requester_id" uuid NOT NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "tasks_project_id_fkey" FOREIGN KEY ("project_id") REFERENCES "public"."projects" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "tasks_repo_id_fkey" FOREIGN KEY ("repo_id") REFERENCES "public"."repos" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "tasks_requester_id_fkey" FOREIGN KEY ("requester_id") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "tasks_workflow_version_id_fkey" FOREIGN KEY ("workflow_version_id") REFERENCES "public"."workflow_versions" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT);
-- create index "tasks_project_id_idx" to table: "tasks"
CREATE INDEX "tasks_project_id_idx" ON "public"."tasks" ("project_id");
-- create "runs" table
CREATE TABLE "public"."runs" ("id" uuid NOT NULL DEFAULT uuidv7(), "task_id" uuid NOT NULL, "status" text NOT NULL, "definition_hash" text NOT NULL, "base_sha" text NOT NULL, "policy_snapshot" jsonb NOT NULL, "execution_budget_ms" bigint NOT NULL, "execution_used_ms" bigint NOT NULL DEFAULT 0, "wall_deadline" timestamptz NOT NULL, "outcome" jsonb NULL, "created_at" timestamptz NOT NULL, "started_at" timestamptz NULL, "finished_at" timestamptz NULL, PRIMARY KEY ("id"), CONSTRAINT "runs_task_id_fkey" FOREIGN KEY ("task_id") REFERENCES "public"."tasks" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "runs_execution_budget_ms_check" CHECK (execution_budget_ms > 0), CONSTRAINT "runs_execution_used_ms_check" CHECK (execution_used_ms >= 0), CONSTRAINT "runs_status_check" CHECK (status = ANY (ARRAY['pending'::text, 'running'::text, 'waiting'::text, 'succeeded'::text, 'failed'::text, 'superseded'::text, 'dead'::text])));
-- create index "runs_task_id_idx" to table: "runs"
CREATE INDEX "runs_task_id_idx" ON "public"."runs" ("task_id");
-- create index "runs_task_live_key" to table: "runs"
CREATE UNIQUE INDEX "runs_task_live_key" ON "public"."runs" ("task_id") WHERE (status = ANY (ARRAY['pending'::text, 'running'::text, 'waiting'::text]));
-- create "artifacts" table
CREATE TABLE "public"."artifacts" ("id" uuid NOT NULL, "run_id" uuid NOT NULL, "node_id" text NOT NULL, "attempt" integer NOT NULL, "type" text NOT NULL, "payload" jsonb NOT NULL, "parent_ids" jsonb NOT NULL DEFAULT '[]', "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "artifacts_run_id_fkey" FOREIGN KEY ("run_id") REFERENCES "public"."runs" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "artifacts_type_check" CHECK (type = ANY (ARRAY['plan'::text, 'patch'::text, 'review_verdict'::text, 'log_ref'::text, 'failure_snapshot'::text])));
-- create index "artifacts_run_id_idx" to table: "artifacts"
CREATE INDEX "artifacts_run_id_idx" ON "public"."artifacts" ("run_id");
-- create "run_events" table
CREATE TABLE "public"."run_events" ("id" bigint NOT NULL, "run_id" uuid NOT NULL, "project_id" uuid NOT NULL, "kind" text NOT NULL, "payload" jsonb NOT NULL DEFAULT '{}', "occurred_at" timestamptz NOT NULL, "correlation_id" text NOT NULL DEFAULT '', PRIMARY KEY ("id"), CONSTRAINT "run_events_run_id_fkey" FOREIGN KEY ("run_id") REFERENCES "public"."runs" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT);
-- create index "run_events_project_id_idx" to table: "run_events"
CREATE INDEX "run_events_project_id_idx" ON "public"."run_events" ("project_id", "id");
-- create index "run_events_run_id_idx" to table: "run_events"
CREATE INDEX "run_events_run_id_idx" ON "public"."run_events" ("run_id", "id");
-- create "outbox" table
CREATE TABLE "public"."outbox" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "event_id" bigint NOT NULL, "kind" text NOT NULL, "payload" jsonb NOT NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "outbox_event_id_fkey" FOREIGN KEY ("event_id") REFERENCES "public"."run_events" ("id") ON UPDATE NO ACTION ON DELETE CASCADE);
-- create index "outbox_created_at_idx" to table: "outbox"
CREATE INDEX "outbox_created_at_idx" ON "public"."outbox" ("created_at");
-- create "phase_runs" table
CREATE TABLE "public"."phase_runs" ("id" uuid NOT NULL DEFAULT uuidv7(), "run_id" uuid NOT NULL, "node_id" text NOT NULL, "attempt" integer NOT NULL, "max_attempts" integer NOT NULL, "status" text NOT NULL, "worker_id" text NULL, "leased_until" timestamptz NULL, "claimed_at" timestamptz NULL, "finished_at" timestamptz NULL, "outcome" jsonb NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "phase_runs_run_id_node_id_attempt_key" UNIQUE ("run_id", "node_id", "attempt"), CONSTRAINT "phase_runs_run_id_fkey" FOREIGN KEY ("run_id") REFERENCES "public"."runs" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "phase_runs_attempt_check" CHECK (attempt >= 1), CONSTRAINT "phase_runs_max_attempts_check" CHECK (max_attempts >= 1), CONSTRAINT "phase_runs_status_check" CHECK (status = ANY (ARRAY['pending'::text, 'running'::text, 'reconciling'::text, 'succeeded'::text, 'failed'::text, 'dead'::text, 'superseded'::text])));
-- create index "phase_runs_claim_idx" to table: "phase_runs"
CREATE INDEX "phase_runs_claim_idx" ON "public"."phase_runs" ("created_at") WHERE (status = 'pending'::text);
-- create index "phase_runs_lease_idx" to table: "phase_runs"
CREATE INDEX "phase_runs_lease_idx" ON "public"."phase_runs" ("leased_until") WHERE (status = 'running'::text);
-- create index "phase_runs_live_key" to table: "phase_runs"
CREATE UNIQUE INDEX "phase_runs_live_key" ON "public"."phase_runs" ("run_id", "node_id") WHERE (status = ANY (ARRAY['pending'::text, 'running'::text, 'reconciling'::text]));
-- seed the single "run_event_counter" row: event ids start at 1
INSERT INTO run_event_counter (onerow, next_id) VALUES (true, 1);

-- +goose Down
-- reverse: seed the single "run_event_counter" row
DELETE FROM run_event_counter WHERE onerow;
-- reverse: create index "phase_runs_live_key" to table: "phase_runs"
DROP INDEX "public"."phase_runs_live_key";
-- reverse: create index "phase_runs_lease_idx" to table: "phase_runs"
DROP INDEX "public"."phase_runs_lease_idx";
-- reverse: create index "phase_runs_claim_idx" to table: "phase_runs"
DROP INDEX "public"."phase_runs_claim_idx";
-- reverse: create "phase_runs" table
DROP TABLE "public"."phase_runs";
-- reverse: create index "outbox_created_at_idx" to table: "outbox"
DROP INDEX "public"."outbox_created_at_idx";
-- reverse: create "outbox" table
DROP TABLE "public"."outbox";
-- reverse: create index "run_events_run_id_idx" to table: "run_events"
DROP INDEX "public"."run_events_run_id_idx";
-- reverse: create index "run_events_project_id_idx" to table: "run_events"
DROP INDEX "public"."run_events_project_id_idx";
-- reverse: create "run_events" table
DROP TABLE "public"."run_events";
-- reverse: create index "artifacts_run_id_idx" to table: "artifacts"
DROP INDEX "public"."artifacts_run_id_idx";
-- reverse: create "artifacts" table
DROP TABLE "public"."artifacts";
-- reverse: create index "runs_task_live_key" to table: "runs"
DROP INDEX "public"."runs_task_live_key";
-- reverse: create index "runs_task_id_idx" to table: "runs"
DROP INDEX "public"."runs_task_id_idx";
-- reverse: create "runs" table
DROP TABLE "public"."runs";
-- reverse: create index "tasks_project_id_idx" to table: "tasks"
DROP INDEX "public"."tasks_project_id_idx";
-- reverse: create "tasks" table
DROP TABLE "public"."tasks";
-- reverse: modify "workflow_versions" table
ALTER TABLE "public"."workflow_versions" DROP CONSTRAINT "workflow_versions_version_check";
-- reverse: create "run_event_counter" table
DROP TABLE "public"."run_event_counter";
