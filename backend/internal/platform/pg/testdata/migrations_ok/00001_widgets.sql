-- +goose Up
CREATE TABLE widgets (
    id   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL
);

-- +goose Down
DROP TABLE widgets;
