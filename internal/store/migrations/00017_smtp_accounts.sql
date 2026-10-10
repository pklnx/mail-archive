-- +goose Up
-- SMTP accounts receive copies of every message from a mail server
-- (journaling). Like import accounts they have no server, login or password
-- and are never synced, but they can be enabled and disabled: a disabled
-- account answers 451, so the mail server keeps the copies queued.
ALTER TABLE accounts DROP CONSTRAINT accounts_kind_check;
ALTER TABLE accounts ADD CONSTRAINT accounts_kind_check CHECK (kind IN ('imap', 'import', 'smtp'));
ALTER TABLE accounts DROP CONSTRAINT accounts_port_check;
ALTER TABLE accounts ADD CONSTRAINT accounts_port_check
    CHECK ((kind = 'imap' AND port BETWEEN 1 AND 65535) OR (kind IN ('import', 'smtp') AND port = 0));
ALTER TABLE accounts DROP CONSTRAINT accounts_import_disabled_check;
ALTER TABLE accounts ADD CONSTRAINT accounts_import_disabled_check CHECK (kind IN ('imap', 'smtp') OR NOT enabled);

-- The addresses an SMTP account receives for: lower case, without the
-- "+extension" that carries the original recipient. An address belongs to
-- one account; removing the account frees it.
CREATE TABLE smtp_addresses (
    address    TEXT   PRIMARY KEY CHECK (address = lower(address) AND address LIKE '_%@_%' AND address NOT LIKE '%+%@%'),
    account_id BIGINT NOT NULL REFERENCES accounts (id) ON DELETE CASCADE
);
CREATE INDEX smtp_addresses_account_idx ON smtp_addresses (account_id);

-- The SMTP envelope of a received message, one row per location. rcpt_to
-- holds the original recipients, BCC included; rcpt_text is the same list
-- joined by newlines for the trigram index (array_to_string is not
-- immutable, so it cannot be an index expression). A retry or a split
-- delivery of the same message adds its recipients to the same row.
CREATE TABLE envelopes (
    location_id BIGINT      PRIMARY KEY REFERENCES message_locations (id) ON DELETE CASCADE,
    mail_from   TEXT        NOT NULL,
    rcpt_to     TEXT[]      NOT NULL,
    rcpt_text   TEXT        NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    client_ip   INET,
    helo        TEXT        NOT NULL DEFAULT '',
    CHECK (rcpt_text = array_to_string(rcpt_to, E'\n'))
);
CREATE INDEX envelopes_rcpt_trgm_idx ON envelopes USING gin (rcpt_text gin_trgm_ops);

-- +goose Down
-- SMTP accounts would violate the old constraints; refuse while any exist
-- (goose then keeps the tables).
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM accounts WHERE kind = 'smtp') THEN
        RAISE EXCEPTION 'smtp accounts exist; delete them before rolling back';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE envelopes;
DROP TABLE smtp_addresses;
ALTER TABLE accounts DROP CONSTRAINT accounts_import_disabled_check;
ALTER TABLE accounts ADD CONSTRAINT accounts_import_disabled_check CHECK (kind = 'imap' OR NOT enabled);
ALTER TABLE accounts DROP CONSTRAINT accounts_port_check;
ALTER TABLE accounts ADD CONSTRAINT accounts_port_check
    CHECK ((kind = 'imap' AND port BETWEEN 1 AND 65535) OR (kind = 'import' AND port = 0));
ALTER TABLE accounts DROP CONSTRAINT accounts_kind_check;
ALTER TABLE accounts ADD CONSTRAINT accounts_kind_check CHECK (kind IN ('imap', 'import'));
