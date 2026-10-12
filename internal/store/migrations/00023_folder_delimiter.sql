-- +goose Up

-- The IMAP hierarchy delimiter of the folder (#91), from the server's LIST
-- answer. NULL: not known yet, filled by the next sync while the folder is
-- on the server. '': the server has no hierarchy (NIL). Import and smtp
-- folders keep NULL; their names use '/'. Nullable without a default, so
-- adding it does not rewrite the table.
ALTER TABLE folders ADD COLUMN delimiter TEXT CHECK (char_length(delimiter) <= 1);

-- +goose Down
ALTER TABLE folders DROP COLUMN delimiter;
