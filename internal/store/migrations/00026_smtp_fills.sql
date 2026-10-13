-- +goose Up

-- A one-time copy of an IMAP mailbox into an smtp account ("fill", #89):
-- the mail that was there before journaling started. The password is
-- kept, sealed with the fill's ID, only while the fill runs.
CREATE TABLE smtp_fills (
    id               BIGSERIAL   PRIMARY KEY,
    account_id       BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    host             TEXT        NOT NULL,
    port             INTEGER     NOT NULL CHECK (port BETWEEN 1 AND 65535),
    tls_mode         TEXT        NOT NULL CHECK (tls_mode IN ('tls', 'starttls', 'none')),
    username         TEXT        NOT NULL,
    password_enc     BYTEA,
    included_folders TEXT[]      NOT NULL DEFAULT '{}',
    excluded_folders TEXT[]      NOT NULL DEFAULT '{}',
    -- The fill writes into "<prefix>/<IMAP folder>", never into the
    -- journal INBOX.
    folder_prefix    TEXT        NOT NULL CHECK (folder_prefix <> '' AND upper(folder_prefix) <> 'INBOX'),
    -- Messages with an INTERNALDATE from an hour after the cut-off on are
    -- left to journaling.
    cutoff           TIMESTAMPTZ NOT NULL,
    status           TEXT        NOT NULL DEFAULT 'running'
                                 CHECK (status IN ('running', 'ok', 'partial', 'failed', 'cancelled')),
    error            TEXT,
    created_by       BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at      TIMESTAMPTZ,
    CHECK ((status = 'running') = (finished_at IS NULL)),
    -- The database never keeps a password after the end.
    CHECK (status = 'running' OR password_enc IS NULL)
);
-- One running fill per account.
CREATE UNIQUE INDEX smtp_fills_running_idx ON smtp_fills (account_id) WHERE status = 'running';
CREATE INDEX smtp_fills_account_idx ON smtp_fills (account_id, id);

-- Progress per IMAP folder: source_uidvalidity and last_uid are the
-- server's; a resume continues after last_uid while the UIDVALIDITY is the
-- same.
CREATE TABLE smtp_fill_folders (
    fill_id            BIGINT  NOT NULL REFERENCES smtp_fills (id) ON DELETE CASCADE,
    source_name        TEXT    NOT NULL,
    folder_id          BIGINT  NOT NULL REFERENCES folders (id) ON DELETE CASCADE,
    source_uidvalidity BIGINT  NOT NULL DEFAULT 0,
    last_uid           BIGINT  NOT NULL DEFAULT 0,
    done               BOOLEAN NOT NULL DEFAULT FALSE,
    scanned            INTEGER NOT NULL DEFAULT 0, -- headers read
    imported           INTEGER NOT NULL DEFAULT 0, -- locations added
    new_messages       INTEGER NOT NULL DEFAULT 0, -- new to the archive
    after_cutoff       INTEGER NOT NULL DEFAULT 0,
    journaled          INTEGER NOT NULL DEFAULT 0, -- same Message-ID received over SMTP
    present            INTEGER NOT NULL DEFAULT 0, -- already in the folder
    deleted            INTEGER NOT NULL DEFAULT 0, -- deleted by retention, not stored again
    error              TEXT,
    PRIMARY KEY (fill_id, source_name)
);
CREATE INDEX smtp_fill_folders_folder_idx ON smtp_fill_folders (folder_id);

-- +goose Down

-- The fill folders and their mail stay, as ordinary folders of the smtp
-- account; only the record of where they came from is lost.
DROP TABLE smtp_fill_folders;
DROP TABLE smtp_fills;
