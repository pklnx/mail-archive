-- +goose Up
CREATE TABLE accounts (
    id               BIGSERIAL PRIMARY KEY,
    name             TEXT        NOT NULL UNIQUE,
    host             TEXT        NOT NULL,
    port             INTEGER     NOT NULL CHECK (port BETWEEN 1 AND 65535),
    tls_mode         TEXT        NOT NULL CHECK (tls_mode IN ('tls', 'starttls', 'none')),
    username         TEXT        NOT NULL,
    password_enc     BYTEA       NOT NULL,
    included_folders TEXT[]      NOT NULL DEFAULT '{}', -- empty = all folders
    excluded_folders TEXT[]      NOT NULL DEFAULT '{}',
    enabled          BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE folders (
    id             BIGSERIAL PRIMARY KEY,
    account_id     BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE RESTRICT,
    name           TEXT        NOT NULL,
    uidvalidity    BIGINT      NOT NULL DEFAULT 0,
    last_uid       BIGINT      NOT NULL DEFAULT 0,
    last_synced_at TIMESTAMPTZ,
    UNIQUE (account_id, name)
);

-- One row per unique message content (deduplicated by SHA-256).
CREATE TABLE messages (
    sha256      CHAR(64)    PRIMARY KEY,
    size        BIGINT      NOT NULL,
    message_id  TEXT,
    subject     TEXT,
    from_addr   TEXT,
    sent_at     TIMESTAMPTZ,
    stored_path TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX messages_message_id_idx ON messages (message_id);
CREATE INDEX messages_sent_at_idx ON messages (sent_at);

-- Where a message was seen: one row per (folder, uidvalidity, uid).
CREATE TABLE message_locations (
    id             BIGSERIAL PRIMARY KEY,
    message_sha256 CHAR(64)    NOT NULL REFERENCES messages (sha256) ON DELETE RESTRICT,
    folder_id      BIGINT      NOT NULL REFERENCES folders (id) ON DELETE RESTRICT,
    uidvalidity    BIGINT      NOT NULL,
    uid            BIGINT      NOT NULL,
    flags          TEXT[]      NOT NULL DEFAULT '{}',
    internal_date  TIMESTAMPTZ,
    first_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (folder_id, uidvalidity, uid)
);

CREATE INDEX message_locations_sha_idx ON message_locations (message_sha256);

CREATE TABLE sync_runs (
    id               BIGSERIAL PRIMARY KEY,
    account_id       BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at      TIMESTAMPTZ,
    status           TEXT        NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'ok', 'partial', 'failed')),
    messages_fetched INTEGER     NOT NULL DEFAULT 0,
    messages_new     INTEGER     NOT NULL DEFAULT 0,
    error            TEXT
);

CREATE INDEX sync_runs_account_idx ON sync_runs (account_id, started_at DESC);

-- +goose Down
DROP TABLE sync_runs;
DROP TABLE message_locations;
DROP TABLE messages;
DROP TABLE folders;
DROP TABLE accounts;
