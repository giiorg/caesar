-- +goose Up
-- modify "credentials" table
ALTER TABLE "public"."credentials" DROP CONSTRAINT "credentials_kind_check", ADD CONSTRAINT "credentials_kind_check" CHECK (kind = ANY (ARRAY['forge_token'::text, 'model_key'::text, 'subscription_token'::text, 'agent_auth_file'::text])), ADD CONSTRAINT "credentials_secret_version_check" CHECK (secret_version >= 1), ADD COLUMN "secret_version" bigint NOT NULL DEFAULT 1, ADD COLUMN "fingerprint" text NULL, ADD COLUMN "last_used_at" timestamptz NULL, ADD COLUMN "last_auth_expired_at" timestamptz NULL;
-- create index "credentials_fingerprint_key" to table: "credentials"
CREATE UNIQUE INDEX "credentials_fingerprint_key" ON "public"."credentials" ("fingerprint") WHERE (fingerprint IS NOT NULL);

-- +goose Down
-- reverse: create index "credentials_fingerprint_key" to table: "credentials"
DROP INDEX "public"."credentials_fingerprint_key";
-- reverse: modify "credentials" table
-- Hand-edited: atlas emits an explicit DROP CONSTRAINT for
-- credentials_secret_version_check, but dropping secret_version already
-- takes its check with it, so the explicit drop errors 42704.
ALTER TABLE "public"."credentials" DROP COLUMN "last_auth_expired_at", DROP COLUMN "last_used_at", DROP COLUMN "fingerprint", DROP COLUMN "secret_version", DROP CONSTRAINT "credentials_kind_check", ADD CONSTRAINT "credentials_kind_check" CHECK (kind = ANY (ARRAY['forge_token'::text, 'model_key'::text]));
