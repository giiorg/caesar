-- +goose Up
-- create "login_rate" table
CREATE TABLE "public"."login_rate" ("scope" text NOT NULL, "failures" integer NOT NULL DEFAULT 0, "window_started_at" timestamptz NOT NULL, "locked_until" timestamptz NULL, PRIMARY KEY ("scope"));
-- create "users" table
CREATE TABLE "public"."users" ("id" uuid NOT NULL DEFAULT uuidv7(), "email" text NOT NULL, "password_hash" text NOT NULL, "role" text NOT NULL, "active" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL DEFAULT now(), "updated_at" timestamptz NOT NULL DEFAULT now(), PRIMARY KEY ("id"), CONSTRAINT "users_role_check" CHECK (role = ANY (ARRAY['admin'::text, 'member'::text])));
-- create index "users_email_key" to table: "users"
CREATE UNIQUE INDEX "users_email_key" ON "public"."users" ((lower(email)));
-- create "audit_entries" table
CREATE TABLE "public"."audit_entries" ("id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "occurred_at" timestamptz NOT NULL, "actor_user_id" uuid NULL, "action" text NOT NULL, "subject_kind" text NOT NULL, "subject_id" text NOT NULL, "correlation_id" text NOT NULL DEFAULT '', "detail" jsonb NOT NULL DEFAULT '{}', PRIMARY KEY ("id"), CONSTRAINT "audit_entries_actor_user_id_fkey" FOREIGN KEY ("actor_user_id") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- create index "audit_entries_occurred_idx" to table: "audit_entries"
CREATE INDEX "audit_entries_occurred_idx" ON "public"."audit_entries" ("occurred_at");
-- create "projects" table
CREATE TABLE "public"."projects" ("id" uuid NOT NULL DEFAULT uuidv7(), "name" text NOT NULL, "created_at" timestamptz NOT NULL DEFAULT now(), PRIMARY KEY ("id"), CONSTRAINT "projects_name_key" UNIQUE ("name"));
-- create "grants" table
CREATE TABLE "public"."grants" ("user_id" uuid NOT NULL, "project_id" uuid NOT NULL, "level" text NOT NULL, "created_at" timestamptz NOT NULL DEFAULT now(), PRIMARY KEY ("user_id", "project_id"), CONSTRAINT "grants_project_id_fkey" FOREIGN KEY ("project_id") REFERENCES "public"."projects" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "grants_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "grants_level_check" CHECK (level = ANY (ARRAY['view'::text, 'run'::text, 'approve'::text, 'manage'::text])));
-- create "sessions" table
CREATE TABLE "public"."sessions" ("id" uuid NOT NULL DEFAULT uuidv7(), "user_id" uuid NOT NULL, "token_hash" bytea NOT NULL, "created_at" timestamptz NOT NULL, "last_seen_at" timestamptz NOT NULL, "expires_at" timestamptz NOT NULL, "revoked_at" timestamptz NULL, PRIMARY KEY ("id"), CONSTRAINT "sessions_token_hash_key" UNIQUE ("token_hash"), CONSTRAINT "sessions_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE);
-- create index "sessions_user_id_idx" to table: "sessions"
CREATE INDEX "sessions_user_id_idx" ON "public"."sessions" ("user_id");

-- +goose Down
-- reverse: create index "sessions_user_id_idx" to table: "sessions"
DROP INDEX "public"."sessions_user_id_idx";
-- reverse: create "sessions" table
DROP TABLE "public"."sessions";
-- reverse: create "grants" table
DROP TABLE "public"."grants";
-- reverse: create "projects" table
DROP TABLE "public"."projects";
-- reverse: create index "audit_entries_occurred_idx" to table: "audit_entries"
DROP INDEX "public"."audit_entries_occurred_idx";
-- reverse: create "audit_entries" table
DROP TABLE "public"."audit_entries";
-- reverse: create index "users_email_key" to table: "users"
DROP INDEX "public"."users_email_key";
-- reverse: create "users" table
DROP TABLE "public"."users";
-- reverse: create "login_rate" table
DROP TABLE "public"."login_rate";
