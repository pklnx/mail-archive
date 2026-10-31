-- +goose Up

-- Shared accounts (#389): the owner of an account lets other users read its
-- mail. A row grants read access only; managing, syncing and deleting stay
-- with the owner. The store keeps the owner out of its own shares and
-- refuses smtp accounts. Removing the user or deleting (or purging) the
-- account ends the share, so a later user with the same name gets nothing.
CREATE TABLE account_shares (
    account_id BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    user_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, user_id)
);
-- The accounts shared with a user, for every read of theirs.
CREATE INDEX account_shares_user_idx ON account_shares (user_id);

-- Reads by shared users name the owners they read from in
-- details.via_owners; the owner's Log finds them through this index. It is
-- partial, so it stays small while shared reads are rare. On the
-- partitioned audit_log, CREATE INDEX builds one per partition (and every
-- partition created later gets its own) while holding a SHARE lock, so
-- audit writes wait until it is done.
CREATE INDEX audit_log_via_owners_idx ON audit_log
    USING gin ((details -> 'via_owners') jsonb_path_ops) WHERE details ? 'via_owners';

-- +goose Down
DROP INDEX audit_log_via_owners_idx;
DROP TABLE account_shares;
