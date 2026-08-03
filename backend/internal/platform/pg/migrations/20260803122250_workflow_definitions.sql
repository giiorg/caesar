-- +goose Up
-- create "workflows" table
CREATE TABLE "public"."workflows" ("id" uuid NOT NULL DEFAULT uuidv7(), "name" text NOT NULL, "description" text NOT NULL DEFAULT '', "created_at" timestamptz NOT NULL DEFAULT now(), "updated_at" timestamptz NOT NULL DEFAULT now(), PRIMARY KEY ("id"), CONSTRAINT "workflows_name_key" UNIQUE ("name"));
-- create "workflow_versions" table
CREATE TABLE "public"."workflow_versions" ("id" uuid NOT NULL DEFAULT uuidv7(), "workflow_id" uuid NOT NULL, "version" integer NOT NULL, "content_hash" text NOT NULL, "document" jsonb NOT NULL, "created_at" timestamptz NOT NULL DEFAULT now(), PRIMARY KEY ("id"), CONSTRAINT "workflow_versions_workflow_id_content_hash_key" UNIQUE ("workflow_id", "content_hash"), CONSTRAINT "workflow_versions_workflow_id_version_key" UNIQUE ("workflow_id", "version"), CONSTRAINT "workflow_versions_workflow_id_fkey" FOREIGN KEY ("workflow_id") REFERENCES "public"."workflows" ("id") ON UPDATE NO ACTION ON DELETE CASCADE);

-- +goose Down
-- reverse: create "workflow_versions" table
DROP TABLE "public"."workflow_versions";
-- reverse: create "workflows" table
DROP TABLE "public"."workflows";
