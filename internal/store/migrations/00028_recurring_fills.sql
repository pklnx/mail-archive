-- +goose Up

-- Recurring fills (#112): a fill can keep running, importing what shows up
-- in the mailbox later and never passed the journal. Instead of the
-- cut-off, a recurring fill imports a message once its INTERNALDATE is two
-- hours old, unless the account received it over SMTP. One fill per
-- account is in progress at a time (smtp_fills_running_idx); between runs
-- a recurring fill has the outcome of its last run as its status.
ALTER TABLE smtp_fills
    -- A fill that was recurring from the start has no cut-off.
    ALTER COLUMN cutoff DROP NOT NULL,
    -- Set when the fill became recurring, never cleared: the fill then uses
    -- the two-hour rule, and a resume makes it recurring again.
    ADD COLUMN recurring_since     TIMESTAMPTZ,
    -- Recurring and active; stopping it clears this.
    ADD COLUMN keep_running        BOOLEAN NOT NULL DEFAULT FALSE,
    -- NULL uses MAIL_ARCHIVE_SYNC_INTERVAL; the floor applies when reading,
    -- as for accounts.sync_interval_seconds. There is no "manual".
    ADD COLUMN interval_seconds    INTEGER
        CHECK (interval_seconds BETWEEN 300 AND 2592000 AND interval_seconds % 60 = 0),
    -- Start of the current or last run; the next one is due an interval
    -- later.
    ADD COLUMN run_started_at      TIMESTAMPTZ,
    -- Trash and spam folders the last run skipped because nobody confirmed
    -- them (they appeared after the fill started).
    ADD COLUMN unconfirmed_folders TEXT[] NOT NULL DEFAULT '{}',
    ADD CONSTRAINT smtp_fills_keep_running_recurring CHECK (NOT keep_running OR recurring_since IS NOT NULL),
    ADD CONSTRAINT smtp_fills_keep_running_not_cancelled CHECK (NOT keep_running OR status <> 'cancelled'),
    ADD CONSTRAINT smtp_fills_rule CHECK (cutoff IS NOT NULL OR recurring_since IS NOT NULL),
    -- Was "the database never keeps a password after the end": a recurring
    -- fill keeps it between runs, like an IMAP account.
    DROP CONSTRAINT smtp_fills_check1,
    ADD CONSTRAINT smtp_fills_password_check CHECK (status = 'running' OR keep_running OR password_enc IS NULL);
CREATE INDEX smtp_fills_keep_running_idx ON smtp_fills (id) WHERE keep_running;

-- Messages younger than the two hours at the last run of a recurring fill:
-- a snapshot of that run, like the other counts of a recurring fill.
ALTER TABLE smtp_fill_folders ADD COLUMN waiting INTEGER NOT NULL DEFAULT 0;

-- The health of recurring fills, like account_sync_health for IMAP
-- accounts: failed runs in a row and what the notifier last announced.
CREATE TABLE smtp_fill_health (
    fill_id               BIGINT      PRIMARY KEY REFERENCES smtp_fills (id) ON DELETE CASCADE,
    failure_streak        INTEGER     NOT NULL DEFAULT 0 CHECK (failure_streak >= 0),
    failing_since         TIMESTAMPTZ,
    last_success_at       TIMESTAMPTZ,
    notified_state        TEXT        NOT NULL DEFAULT 'ok' CHECK (notified_state IN ('ok', 'failing')),
    notified_at           TIMESTAMPTZ,
    notify_attempts       INTEGER     NOT NULL DEFAULT 0 CHECK (notify_attempts >= 0),
    notify_next_at        TIMESTAMPTZ,
    notify_error          TEXT,
    notify_first_error_at TIMESTAMPTZ,
    notify_lease          BYTEA,
    notify_lease_until    TIMESTAMPTZ
);

-- +goose Down

-- Recurring fills simply stop: their passwords go, nothing archived is
-- lost.
DROP TABLE smtp_fill_health;
ALTER TABLE smtp_fill_folders DROP COLUMN waiting;
UPDATE smtp_fills SET password_enc = NULL WHERE status <> 'running';
UPDATE smtp_fills SET cutoff = coalesce(cutoff, created_at);
DROP INDEX smtp_fills_keep_running_idx;
ALTER TABLE smtp_fills
    DROP CONSTRAINT smtp_fills_password_check,
    DROP CONSTRAINT smtp_fills_rule,
    DROP CONSTRAINT smtp_fills_keep_running_not_cancelled,
    DROP CONSTRAINT smtp_fills_keep_running_recurring,
    DROP COLUMN unconfirmed_folders,
    DROP COLUMN run_started_at,
    DROP COLUMN interval_seconds,
    DROP COLUMN keep_running,
    DROP COLUMN recurring_since,
    ALTER COLUMN cutoff SET NOT NULL,
    ADD CONSTRAINT smtp_fills_check1 CHECK (status = 'running' OR password_enc IS NULL);
