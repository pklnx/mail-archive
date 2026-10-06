-- +goose Up

-- The WebAuthn user ID: random, created with the first passkey. It is
-- neither the database ID nor the name.
ALTER TABLE users ADD COLUMN webauthn_handle BYTEA UNIQUE CHECK (length(webauthn_handle) = 32);

CREATE TABLE passkeys (
    id             BIGSERIAL PRIMARY KEY,
    user_id        BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credential_id  BYTEA NOT NULL UNIQUE,
    name           TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    -- The library's credential record: public key, sign count, flags.
    credential     JSONB NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at   TIMESTAMPTZ,
    UNIQUE (user_id, name)
);

-- A registration or login in progress. Single use; only the hash of the
-- token given to the client is stored.
CREATE TABLE webauthn_ceremonies (
    id          BYTEA PRIMARY KEY CHECK (length(id) = 32),
    kind        TEXT NOT NULL CHECK (kind IN ('register', 'login')),
    user_id     BIGINT REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT,
    data        JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL,
    CHECK ((kind = 'register') = (user_id IS NOT NULL AND name IS NOT NULL))
);
CREATE INDEX webauthn_ceremonies_user_idx ON webauthn_ceremonies(user_id);
CREATE INDEX webauthn_ceremonies_expires_idx ON webauthn_ceremonies(expires_at);

-- +goose Down

DROP TABLE webauthn_ceremonies;
DROP TABLE passkeys;
ALTER TABLE users DROP COLUMN webauthn_handle;
