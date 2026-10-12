-- +goose Up

-- An account's own sync interval in seconds (#92). NULL uses the server's
-- MAIL_ARCHIVE_SYNC_INTERVAL; 0 keeps the account off the schedule. The
-- server's floor (MAIL_ARCHIVE_SYNC_INTERVAL_MIN) is applied when reading,
-- so raising it needs no data change. Whole minutes from 5m to 720h.
ALTER TABLE accounts
    ADD COLUMN sync_interval_seconds INTEGER
        CHECK (sync_interval_seconds = 0
            OR (sync_interval_seconds BETWEEN 300 AND 2592000 AND sync_interval_seconds % 60 = 0)),
    -- Only IMAP accounts are synced.
    ADD CONSTRAINT accounts_sync_interval_imap CHECK (kind = 'imap' OR sync_interval_seconds IS NULL);

-- +goose Down

ALTER TABLE accounts
    DROP CONSTRAINT accounts_sync_interval_imap,
    DROP COLUMN sync_interval_seconds;
