-- +goose Up
CREATE TABLE items (
    id BIGINT PRIMARY KEY,
    name TEXT NOT NULL,
    legacy TEXT
);

-- +goose Down
DROP TABLE items;
