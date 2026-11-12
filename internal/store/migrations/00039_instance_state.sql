-- +goose Up
-- The state of the instance as a whole (#396). Exactly one row.
--
-- Part 1 uses the migration failure record: a migration that failed at
-- start with an error that repeats on the same SQL and data (SQLSTATE
-- classes 22, 23, 42, 0A and P0) is recorded with its version and the
-- SHA-256 of its file, so that the next start with the same migration
-- refuses at once instead of running it again in a restart loop. `migrate`
-- retries it and clears the record; so does a start whose migration file
-- changed. Before this table exists, nothing is recorded.
--
-- The other columns are for parts 2 and 3: the check value of the secret
-- key (HMAC-SHA256 with the key, never the key) and the first admin from a
-- setup token (the SHA-256 of the token, never the token). An instance
-- that already has users counts as set up.
CREATE TABLE instance_state (
    id                       BOOLEAN     PRIMARY KEY DEFAULT TRUE CHECK (id),
    secret_key_check         BYTEA       CHECK (octet_length(secret_key_check) = 32),
    setup_token_hash         BYTEA       CHECK (octet_length(setup_token_hash) = 32),
    setup_token_since        TIMESTAMPTZ,
    setup_completed_at       TIMESTAMPTZ,
    setup_user_id            BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    migration_failed_version BIGINT      CHECK (migration_failed_version > 0),
    migration_failed_sha256  BYTEA       CHECK (octet_length(migration_failed_sha256) = 32),
    migration_failed_at      TIMESTAMPTZ,
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((setup_token_hash IS NULL) = (setup_token_since IS NULL)),
    CHECK ((migration_failed_version IS NULL) = (migration_failed_sha256 IS NULL)),
    CHECK ((migration_failed_version IS NULL) = (migration_failed_at IS NULL))
);

INSERT INTO instance_state (setup_completed_at)
SELECT CASE WHEN EXISTS (SELECT 1 FROM users) THEN now() END;

-- +goose Down
DROP TABLE instance_state;
