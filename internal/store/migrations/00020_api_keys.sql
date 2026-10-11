-- +goose Up

-- API keys for scripts. Only the SHA-256 of the key is stored; the key is
-- shown once at creation.
CREATE TABLE api_keys (
    id            BIGSERIAL   PRIMARY KEY,
    user_id       BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    key_hash      BYTEA       NOT NULL UNIQUE CHECK (length(key_hash) = 32),
    hint          TEXT        NOT NULL CHECK (hint ~ '^[a-z2-7]{4}$'),
    name          TEXT        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    scopes        TEXT[]      NOT NULL CHECK (cardinality(scopes) >= 1 AND scopes <@ ARRAY['read', 'sync']::text[]),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ CHECK (expires_at > created_at),
    last_used_at  TIMESTAMPTZ,
    revoked_at    TIMESTAMPTZ,
    -- Who revoked it: the owner, an admin, or NULL for the CLI and for a
    -- revoking user who was removed later.
    revoked_by    BIGINT      REFERENCES users (id) ON DELETE SET NULL
);
-- Names are unique among a user's keys that are not revoked, so a name can
-- be reused after revoking the old key.
CREATE UNIQUE INDEX api_keys_user_name_idx ON api_keys (user_id, name) WHERE revoked_at IS NULL;
-- Listing a user's keys (also revoked ones) and the cascade on user removal.
CREATE INDEX api_keys_user_id_idx ON api_keys (user_id);

-- +goose Down

DROP TABLE api_keys;
