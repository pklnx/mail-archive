-- +goose NO TRANSACTION
-- +goose Up
ALTER TABLE items ADD COLUMN note TEXT;
CREATE INDEX CONCURRENTLY items_note_idx ON items (note);

-- +goose Down
DROP INDEX CONCURRENTLY items_note_idx;
ALTER TABLE items DROP COLUMN note;
