-- +goose Up
-- modify "publish_intents" table
ALTER TABLE "public"."publish_intents" ADD COLUMN "commit_message" text NOT NULL;

-- +goose Down
-- reverse: modify "publish_intents" table
ALTER TABLE "public"."publish_intents" DROP COLUMN "commit_message";
