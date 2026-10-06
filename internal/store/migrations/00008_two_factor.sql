-- +goose Up

ALTER TABLE users
    ADD COLUMN two_factor_secret BYTEA,
    ADD COLUMN two_factor_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN two_factor_pending_secret BYTEA,
    ADD COLUMN two_factor_last_counter BIGINT,
    ADD COLUMN two_factor_version BIGINT NOT NULL DEFAULT 0;

CREATE TABLE two_factor_recovery_codes (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash   BYTEA NOT NULL UNIQUE CHECK (length(code_hash) = 32),
    used_at     TIMESTAMPTZ
);
CREATE INDEX two_factor_recovery_codes_user_idx ON two_factor_recovery_codes(user_id);

CREATE TABLE two_factor_challenges (
    id             BYTEA PRIMARY KEY CHECK (length(id) = 32),
    user_id        BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    two_factor_version BIGINT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at     TIMESTAMPTZ NOT NULL
);
CREATE INDEX two_factor_challenges_user_idx ON two_factor_challenges(user_id);

-- +goose Down

DROP TABLE two_factor_challenges;
DROP TABLE two_factor_recovery_codes;
ALTER TABLE users
    DROP COLUMN two_factor_version,
    DROP COLUMN two_factor_last_counter,
    DROP COLUMN two_factor_pending_secret,
    DROP COLUMN two_factor_enabled,
    DROP COLUMN two_factor_secret;
