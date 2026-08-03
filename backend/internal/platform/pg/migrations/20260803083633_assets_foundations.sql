-- +goose Up
-- create "credentials" table
CREATE TABLE "public"."credentials" ("id" uuid NOT NULL, "name" text NOT NULL, "kind" text NOT NULL, "key_id" text NOT NULL, "wrapped_dek" bytea NOT NULL, "ciphertext" bytea NOT NULL, "created_at" timestamptz NOT NULL DEFAULT now(), "updated_at" timestamptz NOT NULL DEFAULT now(), PRIMARY KEY ("id"), CONSTRAINT "credentials_name_key" UNIQUE ("name"), CONSTRAINT "credentials_kind_check" CHECK (kind = ANY (ARRAY['forge_token'::text, 'model_key'::text])));
-- create "harnesses" table
CREATE TABLE "public"."harnesses" ("id" uuid NOT NULL DEFAULT uuidv7(), "name" text NOT NULL, "kind" text NOT NULL, "image_ref" text NOT NULL, "provider_endpoint" text NOT NULL, "credential_id" uuid NOT NULL, "defaults" jsonb NOT NULL DEFAULT '{}', "created_at" timestamptz NOT NULL DEFAULT now(), "updated_at" timestamptz NOT NULL DEFAULT now(), PRIMARY KEY ("id"), CONSTRAINT "harnesses_name_key" UNIQUE ("name"), CONSTRAINT "harnesses_credential_id_fkey" FOREIGN KEY ("credential_id") REFERENCES "public"."credentials" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "harnesses_kind_check" CHECK (kind = ANY (ARRAY['claude-code'::text, 'codex'::text])));
-- modify "projects" table
ALTER TABLE "public"."projects" ADD COLUMN "description" text NOT NULL DEFAULT '', ADD COLUMN "updated_at" timestamptz NOT NULL DEFAULT now();
-- create "repos" table
CREATE TABLE "public"."repos" ("id" uuid NOT NULL DEFAULT uuidv7(), "project_id" uuid NOT NULL, "forge_kind" text NOT NULL, "slug" text NOT NULL, "default_branch" text NOT NULL DEFAULT 'main', "credential_id" uuid NOT NULL, "created_at" timestamptz NOT NULL DEFAULT now(), "updated_at" timestamptz NOT NULL DEFAULT now(), PRIMARY KEY ("id"), CONSTRAINT "repos_project_id_forge_kind_slug_key" UNIQUE ("project_id", "forge_kind", "slug"), CONSTRAINT "repos_credential_id_fkey" FOREIGN KEY ("credential_id") REFERENCES "public"."credentials" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "repos_project_id_fkey" FOREIGN KEY ("project_id") REFERENCES "public"."projects" ("id") ON UPDATE NO ACTION ON DELETE RESTRICT, CONSTRAINT "repos_forge_kind_check" CHECK (forge_kind = ANY (ARRAY['github'::text, 'gitlab'::text])));
-- create index "repos_project_id_idx" to table: "repos"
CREATE INDEX "repos_project_id_idx" ON "public"."repos" ("project_id");

-- +goose Down
-- reverse: create index "repos_project_id_idx" to table: "repos"
DROP INDEX "public"."repos_project_id_idx";
-- reverse: create "repos" table
DROP TABLE "public"."repos";
-- reverse: modify "projects" table
ALTER TABLE "public"."projects" DROP COLUMN "updated_at", DROP COLUMN "description";
-- reverse: create "harnesses" table
DROP TABLE "public"."harnesses";
-- reverse: create "credentials" table
DROP TABLE "public"."credentials";
