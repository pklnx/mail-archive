-- +goose Up

-- SMTP credentials of smtp accounts (#395): a mail server logs in with the
-- user name "smtp-<account id>" and a secret. Only the SHA-256 of the
-- secret is stored, with its last 4 characters as a hint; the secret is
-- shown once. A new, empty table: nothing large is locked.
CREATE TABLE smtp_credentials (
    id           BIGSERIAL   PRIMARY KEY,
    account_id   BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    secret_hash  BYTEA       NOT NULL UNIQUE CHECK (length(secret_hash) = 32),
    hint         TEXT        NOT NULL CHECK (hint ~ '^[a-z2-7]{4}$'),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Who created it: the owner, or NULL for the CLI and for a user who was
    -- removed later.
    created_by   BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    -- Set when a newer secret supersedes it with a grace period.
    expires_at   TIMESTAMPTZ CHECK (expires_at >= created_at),
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    -- Who revoked it: the owner, an admin, or NULL for the CLI and for a
    -- user who was removed later.
    revoked_by   BIGINT      REFERENCES users (id) ON DELETE SET NULL
);
-- Listing an account's secrets and the cascade on purge.
CREATE INDEX smtp_credentials_account_idx ON smtp_credentials (account_id);
-- An account has at most one secret without an end; the store keeps the
-- rest of "at most two usable" under the account's row lock.
CREATE UNIQUE INDEX smtp_credentials_current_idx ON smtp_credentials (account_id)
    WHERE expires_at IS NULL AND revoked_at IS NULL;

-- +goose Down

DROP TABLE smtp_credentials;
