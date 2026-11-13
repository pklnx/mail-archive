-- Email alerts for admins (#397, part 5; internal/store/alert_mail.go):
-- the notification address of each admin, its confirmation code, and the
-- notify_mail jobs the notifier enqueues at its claim.

-- name: AlertMailRecipients :many
-- The users who get alert mail: admins with a confirmed address who are
-- not locked.
SELECT u.id FROM users u JOIN user_notify_emails e ON e.user_id = u.id
WHERE u.is_admin AND u.locked_at IS NULL AND e.notify_email_confirmed_at IS NOT NULL
ORDER BY u.id;

-- name: AlertMailRecipient :one
-- A recipient as at delivery: still an admin, not locked, address
-- confirmed. No row means the mail is not sent.
SELECT u.id, u.name, e.notify_email::text AS notify_email, u.language, e.notify_email_language
FROM users u JOIN user_notify_emails e ON e.user_id = u.id
WHERE u.id = @id AND u.is_admin AND u.locked_at IS NULL AND e.notify_email_confirmed_at IS NOT NULL;

-- name: EnqueueJobOnce :execrows
-- Insert-only: a job of the kind and key that exists in any state, done
-- included, stays as it is. Unlike EnqueueJob, nothing is queued again.
INSERT INTO jobs (kind, key, priority, max_attempts, payload)
VALUES (@kind, @key, @priority, @max_attempts, @payload)
ON CONFLICT (kind, key) DO NOTHING;

-- name: SetSyncMailEnqueued :exec
UPDATE account_sync_health SET mail_enqueued_event = @event WHERE account_id = @account_id;

-- name: SetFillMailEnqueued :exec
UPDATE smtp_fill_health SET mail_enqueued_event = @event WHERE fill_id = @fill_id;

-- name: SetLossMailEnqueued :exec
UPDATE loss_alerts SET mail_enqueued_event = @event WHERE id = @id;

-- name: SetStorageMailEnqueued :exec
UPDATE storage_states SET mail_enqueued_event = @event WHERE id = @id;

-- name: AlertMailAccountNames :one
-- The names an alert mail about an account shows: the account, its owner
-- and the owner's group, as they are at delivery.
SELECT a.name AS account_name, coalesce(u.name, '')::text AS owner_name, coalesce(g.name, '')::text AS group_name
FROM accounts a LEFT JOIN users u ON u.id = a.owner_id LEFT JOIN customer_groups g ON g.id = u.group_id
WHERE a.id = @id;

-- name: AlertMailGroupName :one
SELECT name FROM customer_groups WHERE id = @id;

-- name: NotifyEmailState :one
-- The own notification address of a user, for Profile → Notifications;
-- all empty for a user who never set one.
SELECT e.notify_email, e.notify_email_confirmed_at, e.notify_email_bounced_at, e.notify_email_code_expires_at,
    coalesce(e.notify_email_code_attempts, 0)::smallint AS notify_email_code_attempts
FROM users u LEFT JOIN user_notify_emails e ON e.user_id = u.id
WHERE u.id = @id;

-- name: LockNotifyEmail :one
-- The user's role and address for a change of the address or a code
-- check; the user's row is locked until the transaction ends.
SELECT u.is_admin, e.notify_email, e.notify_email_code_hash, e.notify_email_code_expires_at,
    coalesce(e.notify_email_code_attempts, 0)::smallint AS notify_email_code_attempts,
    e.notify_email_codes_window_start, coalesce(e.notify_email_codes_sent, 0)::smallint AS notify_email_codes_sent
FROM users u LEFT JOIN user_notify_emails e ON e.user_id = u.id
WHERE u.id = @id
FOR NO KEY UPDATE OF u;

-- name: SetNotifyEmailCode :exec
-- A new address (or the same one again) with a new code: unconfirmed
-- until the code is entered.
INSERT INTO user_notify_emails AS e (user_id, notify_email, notify_email_code_hash, notify_email_code_expires_at,
    notify_email_codes_window_start, notify_email_codes_sent, notify_email_language)
VALUES (@id, @email::text, @code_hash, @expires_at::timestamptz, @window_start::timestamptz, @codes_sent::smallint,
    sqlc.narg(language)::text)
ON CONFLICT (user_id) DO UPDATE SET
    notify_email = EXCLUDED.notify_email, notify_email_confirmed_at = NULL, notify_email_bounced_at = NULL,
    notify_email_code_hash = EXCLUDED.notify_email_code_hash, notify_email_code_expires_at = EXCLUDED.notify_email_code_expires_at,
    notify_email_code_attempts = 0, notify_email_codes_window_start = EXCLUDED.notify_email_codes_window_start,
    notify_email_codes_sent = EXCLUDED.notify_email_codes_sent, notify_email_language = EXCLUDED.notify_email_language;

-- name: CountNotifyEmailCodeTry :execrows
-- Every code entered counts a try first, committed on its own, so that
-- tries sent at the same time cannot pass the limit. No row: no code
-- waits, or its tries are used.
UPDATE user_notify_emails SET notify_email_code_attempts = notify_email_code_attempts + 1
WHERE user_id = @id AND notify_email_code_hash IS NOT NULL AND notify_email_code_attempts < @max_tries::smallint;

-- name: ConfirmNotifyEmail :exec
UPDATE user_notify_emails
SET notify_email_confirmed_at = now(), notify_email_bounced_at = NULL, notify_email_code_hash = NULL,
    notify_email_code_expires_at = NULL, notify_email_code_attempts = 0
WHERE user_id = @id;

-- name: RemoveNotifyEmail :execrows
-- The address goes; the count of codes sent in the hour stays.
UPDATE user_notify_emails
SET notify_email = NULL, notify_email_confirmed_at = NULL, notify_email_bounced_at = NULL, notify_email_code_hash = NULL,
    notify_email_code_expires_at = NULL, notify_email_code_attempts = 0, notify_email_language = NULL
WHERE user_id = @id AND notify_email IS NOT NULL;

-- name: BounceNotifyEmail :execrows
-- A relay answered 5.1.x at RCPT to the address: it is unconfirmed, and
-- the admin sees a banner. Only if it is still the address that bounced.
UPDATE user_notify_emails
SET notify_email_confirmed_at = NULL, notify_email_bounced_at = now()
WHERE user_id = @id AND notify_email = @email::text AND notify_email_confirmed_at IS NOT NULL;
