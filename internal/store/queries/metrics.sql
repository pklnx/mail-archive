-- Instance metrics for GET /api/metrics (#397, part 3; internal/store/metrics.go).
-- These queries select IDs, counts, times and fixed enums only, never a
-- name, host, address, folder, subject or error text.

-- name: MetricsGroups :many
-- Each group by ID: its quota, its figure as in GroupStorageUsage, and its
-- quota state (ok before part 2 writes one).
SELECT g.id, g.quota_bytes,
       (coalesce(u.messages, 0) + coalesce(u.added_messages, 0) + coalesce(d.messages, 0))::bigint AS messages,
       (coalesce(u.bytes, 0) + coalesce(u.added_bytes, 0) + coalesce(d.bytes, 0))::bigint AS bytes,
       coalesce(st.state, 'ok')::text AS state, st.state_since
FROM customer_groups g
LEFT JOIN storage_usage_groups u ON u.group_id = g.id
LEFT JOIN (SELECT gd.group_id, sum(gd.messages) AS messages, sum(gd.bytes) AS bytes
           FROM storage_group_deltas gd GROUP BY gd.group_id) d ON d.group_id = g.id
LEFT JOIN storage_states st ON st.group_id = g.id
ORDER BY g.id;

-- name: MetricsAccounts :many
-- Each account that is not removed, by ID: its owner's group (none for an
-- ownerless account), kind, whether it is enabled, its figures at the last
-- snapshot, and its last successful sync.
SELECT a.id, o.group_id, a.kind, a.enabled,
       coalesce(u.messages, 0)::bigint AS messages, coalesce(u.bytes, 0)::bigint AS bytes,
       h.last_success_at
FROM accounts a
LEFT JOIN users o ON o.id = a.owner_id
LEFT JOIN storage_usage_accounts u ON u.account_id = a.id
LEFT JOIN account_sync_health h ON h.account_id = a.id
WHERE a.removed_at IS NULL
ORDER BY a.id;

-- name: MetricsUsers :many
-- Users per role and lock state.
SELECT (CASE WHEN is_admin THEN 'admin' WHEN is_auditor THEN 'auditor' ELSE 'member' END)::text AS role,
       (locked_at IS NOT NULL)::boolean AS locked, count(*)::bigint AS n
FROM users
GROUP BY 1, 2
ORDER BY 1, 2;
