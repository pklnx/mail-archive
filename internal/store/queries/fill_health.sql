-- name: RecordFillSuccess :exec
-- An ok or partial run of a recurring fill, as RecordSyncSuccess.
INSERT INTO smtp_fill_health AS h (fill_id, last_success_at) VALUES (@fill_id, now())
ON CONFLICT (fill_id) DO UPDATE SET
    failure_streak = 0,
    last_success_at = now(),
    notify_attempts = CASE WHEN h.failure_streak > 0 THEN 0 ELSE h.notify_attempts END,
    notify_next_at = CASE WHEN h.failure_streak > 0 THEN NULL ELSE h.notify_next_at END,
    notify_error = CASE WHEN h.failure_streak > 0 THEN NULL ELSE h.notify_error END,
    notify_first_error_at = CASE WHEN h.failure_streak > 0 THEN NULL ELSE h.notify_first_error_at END;

-- name: RecordFillFailure :exec
-- A failed run of a recurring fill, as RecordSyncFailure.
INSERT INTO smtp_fill_health AS h (fill_id, failure_streak, failing_since) VALUES (@fill_id, 1, now())
ON CONFLICT (fill_id) DO UPDATE SET
    failure_streak = h.failure_streak + 1,
    failing_since = CASE WHEN h.failure_streak = 0 THEN now() ELSE h.failing_since END,
    notify_attempts = CASE WHEN h.failure_streak = 0 THEN 0 ELSE h.notify_attempts END,
    notify_next_at = CASE WHEN h.failure_streak = 0 THEN NULL ELSE h.notify_next_at END,
    notify_error = CASE WHEN h.failure_streak = 0 THEN NULL ELSE h.notify_error END,
    notify_first_error_at = CASE WHEN h.failure_streak = 0 THEN NULL ELSE h.notify_first_error_at END;

-- name: FillHealth :many
-- The health of active recurring fills of accounts that are not removed,
-- of all users or one.
SELECT sf.id, sf.account_id, a.name AS account_name, a.owner_id, a.enabled, sf.username, sf.interval_seconds,
       sf.recurring_since::timestamptz AS recurring_since, sf.status, sf.run_started_at,
       coalesce(h.failure_streak, 0)::int AS failure_streak, h.failing_since, h.last_success_at,
       coalesce(h.notified_state, 'ok')::text AS notified_state
FROM smtp_fills sf JOIN accounts a ON a.id = sf.account_id
LEFT JOIN smtp_fill_health h ON h.fill_id = sf.id
WHERE sf.keep_running AND a.removed_at IS NULL
  AND (sqlc.narg(owner)::bigint IS NULL OR a.owner_id = sqlc.narg(owner)::bigint)
ORDER BY sf.id;

-- name: ClaimFillNotifications :many
-- ClaimNotifications for active recurring fills of enabled accounts that
-- are not removed.
UPDATE smtp_fill_health h
SET notify_lease = @lease, notify_lease_until = now() + make_interval(secs => @lease_seconds::float8)
FROM smtp_fills sf JOIN accounts a ON a.id = sf.account_id LEFT JOIN users u ON u.id = a.owner_id
WHERE h.fill_id = sf.id AND h.fill_id IN (
    SELECT c.fill_id FROM smtp_fill_health c
    JOIN smtp_fills cf ON cf.id = c.fill_id JOIN accounts ca ON ca.id = cf.account_id
    WHERE cf.keep_running AND ca.enabled AND ca.removed_at IS NULL
      AND ((c.failure_streak >= @threshold::int AND c.notified_state = 'ok')
        OR (c.failure_streak = 0 AND c.notified_state = 'failing'))
      AND (c.notify_next_at IS NULL OR c.notify_next_at <= now())
      AND (c.notify_lease_until IS NULL OR c.notify_lease_until < now())
    ORDER BY c.fill_id
    LIMIT @max_rows
    FOR UPDATE OF c SKIP LOCKED)
RETURNING h.fill_id, sf.account_id, a.name AS account_name, coalesce(u.name, '')::text AS owner_name,
    sf.username, h.failure_streak, h.failing_since, h.notified_state, h.notify_attempts, h.notify_first_error_at,
    coalesce(sf.error, '')::text AS last_error;

-- name: FinishFillNotification :execrows
UPDATE smtp_fill_health
SET notified_state = @notified_state, notified_at = now(), notify_attempts = 0, notify_next_at = NULL,
    notify_error = NULLIF(@notify_error::text, ''), notify_first_error_at = NULL,
    notify_lease = NULL, notify_lease_until = NULL
WHERE fill_id = @fill_id AND notify_lease = @lease;

-- name: DeferFillNotification :execrows
UPDATE smtp_fill_health
SET notify_attempts = notify_attempts + 1, notify_next_at = @next_at::timestamptz, notify_error = @notify_error::text,
    notify_first_error_at = coalesce(notify_first_error_at, now()),
    notify_lease = NULL, notify_lease_until = NULL
WHERE fill_id = @fill_id AND notify_lease = @lease;
