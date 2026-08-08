-- +goose Up
-- modify "publish_intents" table
ALTER TABLE "public"."publish_intents" ADD COLUMN "author_email" text NOT NULL;

-- +goose Down
-- reverse: modify "publish_intents" table
ALTER TABLE "public"."publish_intents" DROP COLUMN "author_email";
