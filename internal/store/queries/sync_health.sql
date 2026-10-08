-- name: RecordSyncSuccess :exec
-- An ok or partial run: the login worked, the streak ends. A pending alert
-- is dropped and its delivery state reset.
INSERT INTO account_sync_health AS h (account_id, last_success_at) VALUES (@account_id, now())
ON CONFLICT (account_id) DO UPDATE SET
    failure_streak = 0,
    last_success_at = now(),
    notify_attempts = CASE WHEN h.failure_streak > 0 THEN 0 ELSE h.notify_attempts END,
    notify_next_at = CASE WHEN h.failure_streak > 0 THEN NULL ELSE h.notify_next_at END,
    notify_error = CASE WHEN h.failure_streak > 0 THEN NULL ELSE h.notify_error END,
    notify_first_error_at = CASE WHEN h.failure_streak > 0 THEN NULL ELSE h.notify_first_error_at END;

-- name: RecordSyncFailure :exec
-- A failed run extends the streak. The first failure starts a new streak
-- and drops a pending recovery message.
INSERT INTO account_sync_health AS h (account_id, failure_streak, failing_since) VALUES (@account_id, 1, now())
ON CONFLICT (account_id) DO UPDATE SET
    failure_streak = h.failure_streak + 1,
    failing_since = CASE WHEN h.failure_streak = 0 THEN now() ELSE h.failing_since END,
    notify_attempts = CASE WHEN h.failure_streak = 0 THEN 0 ELSE h.notify_attempts END,
    notify_next_at = CASE WHEN h.failure_streak = 0 THEN NULL ELSE h.notify_next_at END,
    notify_error = CASE WHEN h.failure_streak = 0 THEN NULL ELSE h.notify_error END,
    notify_first_error_at = CASE WHEN h.failure_streak = 0 THEN NULL ELSE h.notify_first_error_at END;

-- name: ClaimNotifications :many
-- Leases up to max_rows enabled IMAP accounts with an announcement due: an
-- alert (streak reached the threshold, last announced ok) or a recovery
-- (streak ended, last announced failing). Rows another notifier holds are
-- skipped, so no transition is claimed twice.
UPDATE account_sync_health h
SET notify_lease = @lease, notify_lease_until = now() + make_interval(secs => @lease_seconds::float8)
FROM accounts a LEFT JOIN users u ON u.id = a.owner_id
WHERE h.account_id = a.id AND h.account_id IN (
    SELECT c.account_id FROM account_sync_health c JOIN accounts ca ON ca.id = c.account_id
    WHERE ca.kind = 'imap' AND ca.enabled AND ca.removed_at IS NULL
      AND ((c.failure_streak >= @threshold::int AND c.notified_state = 'ok')
        OR (c.failure_streak = 0 AND c.notified_state = 'failing'))
      AND (c.notify_next_at IS NULL OR c.notify_next_at <= now())
      AND (c.notify_lease_until IS NULL OR c.notify_lease_until < now())
    ORDER BY c.account_id
    LIMIT @max_rows
    FOR UPDATE OF c SKIP LOCKED)
RETURNING h.account_id, a.name AS account_name, coalesce(u.name, '')::text AS owner_name, h.failure_streak, h.failing_since,
    h.notified_state, h.notify_attempts, h.notify_first_error_at,
    coalesce((SELECT r.error FROM sync_runs r WHERE r.account_id = h.account_id AND r.finished_at IS NOT NULL
              ORDER BY r.started_at DESC LIMIT 1), '')::text AS last_error;

-- name: FinishNotification :execrows
-- The announcement was delivered (or given up): record it and clear the
-- delivery state. Only the notifier holding the lease may do this.
UPDATE account_sync_health
SET notified_state = @notified_state, notified_at = now(), notify_attempts = 0, notify_next_at = NULL,
    notify_error = NULLIF(@notify_error::text, ''), notify_first_error_at = NULL,
    notify_lease = NULL, notify_lease_until = NULL
WHERE account_id = @account_id AND notify_lease = @lease;

-- name: DeferNotification :execrows
-- A delivery attempt failed: try again at next_at.
UPDATE account_sync_health
SET notify_attempts = notify_attempts + 1, notify_next_at = @next_at::timestamptz, notify_error = @notify_error::text,
    notify_first_error_at = coalesce(notify_first_error_at, now()),
    notify_lease = NULL, notify_lease_until = NULL
WHERE account_id = @account_id AND notify_lease = @lease;

-- name: SyncHealth :many
-- The health of IMAP accounts that are not removed, of all users or one.
SELECT a.id, a.name, a.owner_id, a.enabled, a.created_at,
       coalesce(h.failure_streak, 0)::int AS failure_streak, h.failing_since, h.last_success_at,
       coalesce(h.notified_state, 'ok')::text AS notified_state
FROM accounts a LEFT JOIN account_sync_health h ON h.account_id = a.id
WHERE a.kind = 'imap' AND a.removed_at IS NULL
  AND (sqlc.narg(owner)::bigint IS NULL OR a.owner_id = sqlc.narg(owner)::bigint)
ORDER BY a.id;

-- name: CountFailingByOwner :many
-- Enabled IMAP accounts per owner whose streak reached the threshold.
SELECT a.owner_id, count(*) AS failing
FROM accounts a JOIN account_sync_health h ON h.account_id = a.id
WHERE a.kind = 'imap' AND a.enabled AND a.removed_at IS NULL AND a.owner_id IS NOT NULL
  AND h.failure_streak >= @threshold::int
GROUP BY a.owner_id;
