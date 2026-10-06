-- +goose Up
-- Users of the web UI. Names are lowercase login names; passwords are only
-- stored as Argon2id hashes (PHC string format).
CREATE TABLE users (
    id                  BIGSERIAL PRIMARY KEY,
    name                TEXT NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9._-]{1,64}$'),
    password_hash       TEXT NOT NULL,
    is_admin            BOOLEAN NOT NULL DEFAULT FALSE,
    locked_at           TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    password_changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at       TIMESTAMPTZ
);

-- Login sessions. The id is the SHA-256 of the cookie token, so a leaked
-- database does not reveal usable tokens.
CREATE TABLE sessions (
    id           BYTEA PRIMARY KEY CHECK (length(id) = 32),
    user_id      BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    user_agent   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);

-- +goose Down
DROP TABLE sessions;
DROP TABLE users;
