-- +goose Up
-- Import accounts hold mail read from mbox files or Maildirs. They have no
-- server, login or password and are never synced.
ALTER TABLE accounts ADD COLUMN kind TEXT NOT NULL DEFAULT 'imap' CHECK (kind IN ('imap', 'import'));
ALTER TABLE accounts DROP CONSTRAINT accounts_port_check;
ALTER TABLE accounts ADD CONSTRAINT accounts_port_check
    CHECK ((kind = 'imap' AND port BETWEEN 1 AND 65535) OR (kind = 'import' AND port = 0));
ALTER TABLE accounts ADD CONSTRAINT accounts_import_disabled_check CHECK (kind = 'imap' OR NOT enabled);

-- +goose Down
-- Import accounts would become IMAP accounts without a server; refuse while
-- any exist (goose then keeps the column).
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM accounts WHERE kind = 'import') THEN
        RAISE EXCEPTION 'import accounts exist; delete them before rolling back';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE accounts DROP CONSTRAINT accounts_import_disabled_check;
ALTER TABLE accounts DROP CONSTRAINT accounts_port_check;
ALTER TABLE accounts ADD CONSTRAINT accounts_port_check CHECK (port BETWEEN 1 AND 65535);
ALTER TABLE accounts DROP COLUMN kind;
