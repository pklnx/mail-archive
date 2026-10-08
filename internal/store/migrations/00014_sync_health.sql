-- +goose Up
-- The sync health of each IMAP account: how many syncs in a row failed, and
-- what the notifier last announced about it. A sync only updates the streak;
-- the notifier compares it with notified_state and sends the difference.
CREATE TABLE account_sync_health (
    account_id            BIGINT      PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    failure_streak        INTEGER     NOT NULL DEFAULT 0 CHECK (failure_streak >= 0),
    -- Start of the current (or, after a success, the last) failure streak.
    failing_since         TIMESTAMPTZ,
    last_success_at       TIMESTAMPTZ,
    notified_state        TEXT        NOT NULL DEFAULT 'ok' CHECK (notified_state IN ('ok', 'failing')),
    notified_at           TIMESTAMPTZ,
    -- Delivery of the pending announcement: failed attempts, when to try
    -- again, the last error, and when the first attempt failed (to give up
    -- after a day). Reset when an announcement is done or a streak starts
    -- or ends.
    notify_attempts       INTEGER     NOT NULL DEFAULT 0 CHECK (notify_attempts >= 0),
    notify_next_at        TIMESTAMPTZ,
    notify_error          TEXT,
    notify_first_error_at TIMESTAMPTZ,
    -- A notifier that claimed the row; only it may write notified_state
    -- until the lease expires.
    notify_lease          BYTEA,
    notify_lease_until    TIMESTAMPTZ
);

-- Existing IMAP accounts: failed runs since the last success (runs closed as
-- interrupted do not count). Accounts that are already failing alert once
-- after the upgrade.
INSERT INTO account_sync_health (account_id, failure_streak, failing_since, last_success_at)
SELECT a.id,
       (SELECT count(*) FROM sync_runs r
        WHERE r.account_id = a.id AND r.status = 'failed' AND r.error IS DISTINCT FROM 'interrupted'
          AND r.started_at > coalesce(s.at, '-infinity')),
       (SELECT min(r.started_at) FROM sync_runs r
        WHERE r.account_id = a.id AND r.status = 'failed' AND r.error IS DISTINCT FROM 'interrupted'
          AND r.started_at > coalesce(s.at, '-infinity')),
       s.finished
FROM accounts a
LEFT JOIN LATERAL (
    SELECT max(r.started_at) AS at, max(coalesce(r.finished_at, r.started_at)) AS finished
    FROM sync_runs r WHERE r.account_id = a.id AND r.status IN ('ok', 'partial')
) s ON TRUE
WHERE a.kind = 'imap';

-- +goose Down
DROP TABLE account_sync_health;
