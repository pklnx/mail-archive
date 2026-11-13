-- +goose Up

-- Email alerts for admins (#397, part 5). Each admin may set one
-- notification address in Profile → Notifications and confirms it with a
-- code mailed to it. Only admins with a confirmed address who are not
-- locked get alert mail; see internal/alertmail and
-- docs/guide/email-alerts.md.
--
-- The notify_email columns live in a table of their own, one row per user
-- who ever set an address, and not on users: the store reads users rows
-- with every column, so new columns there would break `status` and the
-- other commands that only warn while this migration is pending.
--
-- notify_email is the address; notify_email_confirmed_at is set when the
-- code was entered, and cleared when a relay answers 5.1.x to it at RCPT
-- (notify_email_bounced_at then shows a banner). Only a SHA-256 of the
-- code is stored, with its expiry and the tries made; codes_window_start
-- and codes_sent limit the codes sent per hour, and stay when the address
-- is removed. notify_email_language is the UI's language when the code was
-- asked for: alert mail falls back to it when users.language is auto.
CREATE TABLE user_notify_emails (
    user_id                         BIGINT      PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    notify_email                    TEXT        CHECK (char_length(notify_email) BETWEEN 3 AND 254 AND notify_email !~ '[[:cntrl:]]'),
    notify_email_confirmed_at       TIMESTAMPTZ,
    notify_email_bounced_at         TIMESTAMPTZ,
    notify_email_code_hash          BYTEA       CHECK (length(notify_email_code_hash) = 32),
    notify_email_code_expires_at    TIMESTAMPTZ,
    notify_email_code_attempts      SMALLINT    NOT NULL DEFAULT 0 CHECK (notify_email_code_attempts >= 0),
    notify_email_codes_window_start TIMESTAMPTZ,
    notify_email_codes_sent         SMALLINT    NOT NULL DEFAULT 0 CHECK (notify_email_codes_sent >= 0),
    notify_email_language           TEXT        CHECK (notify_email_language IN ('de', 'en')),
    CHECK (notify_email IS NOT NULL OR notify_email_confirmed_at IS NULL)
);

-- The alert outboxes remember the last event for which notify_mail jobs
-- were enqueued: a webhook retry claims the same event again and enqueues
-- nothing; a new transition has a new event ID. storage_states has the
-- column since 00041. Adding a column without a default changes only the
-- catalog.
ALTER TABLE account_sync_health ADD COLUMN mail_enqueued_event TEXT;
ALTER TABLE smtp_fill_health ADD COLUMN mail_enqueued_event TEXT;
ALTER TABLE loss_alerts ADD COLUMN mail_enqueued_event TEXT;

-- +goose Down

-- Queued alert mail is dropped with the addresses it would go to.
DELETE FROM jobs WHERE kind = 'notify_mail';
UPDATE storage_states SET mail_enqueued_event = NULL;
ALTER TABLE loss_alerts DROP COLUMN mail_enqueued_event;
ALTER TABLE smtp_fill_health DROP COLUMN mail_enqueued_event;
ALTER TABLE account_sync_health DROP COLUMN mail_enqueued_event;
DROP TABLE user_notify_emails;
