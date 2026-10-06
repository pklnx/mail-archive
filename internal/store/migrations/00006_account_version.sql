-- +goose Up
-- Every change to an account increments its version. A change based on an
-- older version is refused, so concurrent edits never overwrite each other
-- silently (see store.UpdateAccount).
ALTER TABLE accounts ADD COLUMN version BIGINT NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE accounts DROP COLUMN version;
