-- Storage alerts (#397, part 3; internal/store/storage_alerts.go): the
-- outbox of storage_states. Part 2's gate writes a transition by setting
-- state and state_since and incrementing event_seq; a scope has an
-- announcement due while event_seq is above notified_seq.

-- name: ClaimStorageNotifications :many
-- Leases up to max_rows scopes with a transition not announced yet. Rows
-- another notifier holds are skipped, so no transition is claimed twice.
-- used_bytes is the scope's figure now: the exact instance total, or the
-- group's figure as in GroupStorageUsage.
UPDATE storage_states s
SET notify_lease = @lease, notify_lease_until = now() + make_interval(secs => @lease_seconds::float8)
WHERE s.id IN (
    SELECT c.id FROM storage_states c
    WHERE c.event_seq > c.notified_seq
      AND (c.notify_next_at IS NULL OR c.notify_next_at <= now())
      AND (c.notify_lease_until IS NULL OR c.notify_lease_until < now())
    ORDER BY c.id
    LIMIT @max_rows
    FOR UPDATE OF c SKIP LOCKED)
RETURNING s.id, s.group_id, s.state, s.state_since, s.event_seq, s.notify_attempts, s.notify_first_error_at,
    (SELECT g.quota_bytes FROM customer_groups g WHERE g.id = s.group_id) AS group_quota_bytes,
    (CASE WHEN s.group_id IS NULL THEN
        (SELECT b.bytes + coalesce((SELECT sum(d.bytes) FROM storage_counter_deltas d), 0) FROM storage_counter_base b)
     ELSE
        coalesce((SELECT u.bytes + u.added_bytes FROM storage_usage_groups u WHERE u.group_id = s.group_id), 0)
        + coalesce((SELECT sum(gd.bytes) FROM storage_group_deltas gd WHERE gd.group_id = s.group_id), 0)
     END)::bigint AS used_bytes;

-- name: FinishStorageNotification :execrows
-- The transition up to event_seq was announced (or given up): record it
-- and clear the delivery state. A transition written meanwhile has a
-- higher event_seq and stays due. Only the notifier holding the lease may
-- do this.
UPDATE storage_states
SET notified_seq = greatest(notified_seq, @event_seq::bigint), notify_attempts = 0, notify_next_at = NULL,
    notify_error = NULLIF(@notify_error::text, ''), notify_first_error_at = NULL,
    notify_lease = NULL, notify_lease_until = NULL
WHERE id = @id AND notify_lease = @lease;

-- name: DeferStorageNotification :execrows
-- A delivery attempt failed: try again at next_at.
UPDATE storage_states
SET notify_attempts = notify_attempts + 1, notify_next_at = @next_at::timestamptz, notify_error = @notify_error::text,
    notify_first_error_at = coalesce(notify_first_error_at, now()),
    notify_lease = NULL, notify_lease_until = NULL
WHERE id = @id AND notify_lease = @lease;
