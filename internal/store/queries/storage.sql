-- Storage usage (#397, part 1; migration 00041, internal/store/storage.go).

-- name: StorageTotals :one
-- The exact instance totals: the base plus the deltas, in one statement, so
-- that a fold committing meanwhile is seen entirely or not at all.
SELECT (b.messages + coalesce((SELECT sum(d.messages) FROM storage_counter_deltas d), 0))::bigint AS messages,
       (b.bytes + coalesce((SELECT sum(d.bytes) FROM storage_counter_deltas d), 0))::bigint AS bytes
FROM storage_counter_base b;

-- name: LockStorageXact :exec
-- The lock that storage_fold and usage_refresh share, until the end of the
-- transaction (usage_refresh takes it for its session, StorageLockSession).
SELECT pg_advisory_xact_lock(@class::int, 0);

-- name: FoldStorageCounters :one
-- Adds the deltas committed so far to the base and deletes them. Deltas
-- that commit meanwhile are not seen and stay for the next fold.
WITH d AS (
    DELETE FROM storage_counter_deltas RETURNING messages, bytes
)
UPDATE storage_counter_base
SET messages = messages + coalesce((SELECT sum(d.messages) FROM d), 0),
    bytes = bytes + coalesce((SELECT sum(d.bytes) FROM d), 0)
RETURNING (SELECT count(*) FROM d)::bigint AS folded;

-- name: FoldGroupDeltas :execrows
-- Adds the group deltas to added_* of their group's usage row. A group
-- without a row yet (created after the last snapshot) keeps its deltas
-- until the next snapshot writes the row.
WITH d AS (
    DELETE FROM storage_group_deltas gd
    WHERE EXISTS (SELECT 1 FROM storage_usage_groups u WHERE u.group_id = gd.group_id)
    RETURNING gd.group_id, gd.messages, gd.bytes
), s AS (
    SELECT d.group_id, sum(d.messages)::bigint AS messages, sum(d.bytes)::bigint AS bytes FROM d GROUP BY d.group_id
)
UPDATE storage_usage_groups u
SET added_messages = u.added_messages + s.messages, added_bytes = u.added_bytes + s.bytes
FROM s WHERE u.group_id = s.group_id;

-- name: UsageRefreshState :one
-- What the fold checks at its end: the latest refresh request, the request
-- the last snapshot covered, and whether usage_refresh failed in the last
-- hour (then only a request or the hourly run queues it again). Plain
-- reads: they never wait for a requester.
SELECT r.seq, s.covered_seq,
       coalesce((SELECT j.state = 'failed' AND j.finished_at > now() - interval '1 hour'
                 FROM jobs j WHERE j.kind = 'usage_refresh' AND j.key = 'singleton'), false)::boolean AS failed_recently
FROM storage_refresh_requests r CROSS JOIN storage_snapshot s;

-- name: BumpUsageRefreshRequest :one
UPDATE storage_refresh_requests SET seq = seq + 1 RETURNING seq;

-- name: UsageRefreshRequestSeq :one
-- A plain read: in usage_refresh's REPEATABLE READ transaction it is the
-- first statement, and a locking read would wait for an open requester and
-- then fail with 40001.
SELECT seq FROM storage_refresh_requests;

-- name: RefreshUsage :one
-- The snapshot of usage_refresh, in one statement: the distinct
-- (account, message) pairs over every location, gone ones included,
-- materialized once; per account its messages, bytes and the bytes no other
-- account holds; per customer group its distinct messages (an account's
-- group is its owner's, NULL without an owner). Every account and group
-- gets its row. Hash aggregates throughout; sizes come from the covering
-- index messages_sha_size_idx (TestUsageRefreshPlan).
WITH al AS MATERIALIZED (
    SELECT f.account_id, l.message_sha256 AS sha
    FROM message_locations l JOIN folders f ON f.id = l.folder_id
    GROUP BY f.account_id, l.message_sha256
), holders AS (
    SELECT al.sha, count(*) AS n FROM al GROUP BY al.sha
), sized AS MATERIALIZED (
    SELECT al.account_id, al.sha, m.size, h.n
    FROM al
    JOIN holders h ON h.sha = al.sha
    JOIN messages m ON m.sha256 = al.sha
), per_account AS (
    SELECT sized.account_id, count(*) AS messages, sum(sized.size)::bigint AS bytes,
           coalesce(sum(sized.size) FILTER (WHERE sized.n = 1), 0)::bigint AS exclusive_bytes
    FROM sized GROUP BY sized.account_id
), accounts_written AS (
    INSERT INTO storage_usage_accounts AS u (account_id, messages, bytes, exclusive_bytes, computed_at)
    SELECT a.id, coalesce(p.messages, 0), coalesce(p.bytes, 0), coalesce(p.exclusive_bytes, 0), now()
    FROM accounts a LEFT JOIN per_account p ON p.account_id = a.id
    ON CONFLICT (account_id) DO UPDATE
    SET messages = EXCLUDED.messages, bytes = EXCLUDED.bytes, exclusive_bytes = EXCLUDED.exclusive_bytes,
        computed_at = EXCLUDED.computed_at
    RETURNING 1
), in_group AS (
    SELECT o.group_id, sized.sha, sized.size
    FROM sized
    JOIN accounts a ON a.id = sized.account_id
    LEFT JOIN users o ON o.id = a.owner_id
    GROUP BY o.group_id, sized.sha, sized.size
), per_group AS (
    SELECT in_group.group_id, count(*) AS messages, sum(in_group.size)::bigint AS bytes
    FROM in_group GROUP BY in_group.group_id
), groups_written AS (
    INSERT INTO storage_usage_groups AS u (group_id, messages, bytes, added_messages, added_bytes, computed_at)
    SELECT g.id, coalesce(p.messages, 0), coalesce(p.bytes, 0), 0, 0, now()
    FROM customer_groups g LEFT JOIN per_group p ON p.group_id = g.id
    ON CONFLICT (group_id) DO UPDATE
    SET messages = EXCLUDED.messages, bytes = EXCLUDED.bytes, added_messages = 0, added_bytes = 0,
        computed_at = EXCLUDED.computed_at
    RETURNING 1
)
SELECT (SELECT count(*) FROM accounts_written)::bigint AS accounts,
       (SELECT count(*) FROM groups_written)::bigint AS groups,
       coalesce((SELECT p.messages FROM per_group p WHERE p.group_id IS NULL), 0)::bigint AS ungrouped_messages,
       coalesce((SELECT p.bytes FROM per_group p WHERE p.group_id IS NULL), 0)::bigint AS ungrouped_bytes;

-- name: StorageCounterCheck :one
-- The counters against an exact count, in usage_refresh's snapshot (an
-- index-only scan of messages_sha_size_idx).
SELECT (b.messages + coalesce((SELECT sum(d.messages) FROM storage_counter_deltas d), 0))::bigint AS counted_messages,
       (b.bytes + coalesce((SELECT sum(d.bytes) FROM storage_counter_deltas d), 0))::bigint AS counted_bytes,
       m.messages AS exact_messages, m.bytes AS exact_bytes
FROM storage_counter_base b
CROSS JOIN (SELECT count(*)::bigint AS messages, coalesce(sum(size), 0)::bigint AS bytes FROM messages) m;

-- name: DeleteSeenGroupDeltas :execrows
-- In usage_refresh's snapshot: the deltas it counted. Deltas committed
-- after the snapshot began are not seen and stay.
DELETE FROM storage_group_deltas;

-- name: WriteStorageSnapshot :exec
-- The last write of usage_refresh: when, the accounts without a group, the
-- database by relation, the counter difference and the request covered.
UPDATE storage_snapshot
SET computed_at = now(), ungrouped_messages = @ungrouped_messages, ungrouped_bytes = @ungrouped_bytes,
    database_bytes = pg_database_size(current_database()),
    relation_bytes = jsonb_build_object(
        'messages', pg_total_relation_size('messages'),
        'message_locations', pg_total_relation_size('message_locations'),
        'attachment_texts', pg_total_relation_size('attachment_texts'),
        'audit_log', coalesce((SELECT sum(pg_total_relation_size(t.relid)) FROM pg_partition_tree('audit_log') t), 0)),
    counter_drift = @counter_drift, covered_seq = @covered_seq;

-- name: GroupHeldMessages :many
-- The hashes of a batch that another account of the group holds (gone
-- locations included). Driven from the group's own folders: one probe of
-- message_locations_folder_sha_idx per folder and hash, so the cost
-- depends only on the group's folders and the batch, never on how many
-- accounts of other groups hold the same bytes (#539; TestUsageRefreshPlan
-- checks the custom and the generic plan). OFFSET 0 and LIMIT 1 keep the
-- planner from flattening the probes into a lookup by hash
-- (message_locations_sha_idx).
SELECT DISTINCT s.sha::text AS sha256
FROM unnest(@shas::bpchar[]) AS s (sha)
CROSS JOIN (
    SELECT f.id FROM folders f
    JOIN accounts a ON a.id = f.account_id
    JOIN users o ON o.id = a.owner_id
    WHERE o.group_id = @group_id AND a.id <> @account_id
    OFFSET 0
) gf
JOIN LATERAL (
    SELECT 1 FROM message_locations ml
    WHERE ml.folder_id = gf.id AND ml.message_sha256 = s.sha
    LIMIT 1
) held ON true;

-- name: InsertGroupDelta :exec
INSERT INTO storage_group_deltas (group_id, messages, bytes) VALUES (@group_id, @messages, @bytes);

-- name: LockFolderAccountGroup :one
-- The account of a folder, FOR KEY SHARE until the save commits: an owner
-- change waits for the save, and a save waits for an owner change in
-- flight and then reads the new owner.
SELECT a.id, a.owner_id FROM folders f JOIN accounts a ON a.id = f.account_id WHERE f.id = @folder_id FOR KEY SHARE OF a;

-- name: LockAccountGroup :one
SELECT id, owner_id FROM accounts WHERE id = @id FOR KEY SHARE;

-- name: LockOwnerGroup :one
-- The owner's group, FOR KEY SHARE until the save commits: a group change
-- of the owner (LockUser FOR UPDATE) waits for the save, and a save waits
-- for a group change in flight and then reads the new group.
SELECT group_id FROM users WHERE id = @id FOR KEY SHARE;

-- name: StorageSnapshot :one
SELECT computed_at, ungrouped_messages, ungrouped_bytes, database_bytes, relation_bytes, counter_drift, covered_seq
FROM storage_snapshot;

-- name: InstanceStorageState :one
SELECT state, state_since FROM storage_states WHERE group_id IS NULL;

-- name: GroupStorageUsage :many
-- Each group's figure: its snapshot, the folded deltas and the deltas not
-- folded yet; an upper bound between snapshots.
SELECT g.id, g.name, g.quota_bytes,
       (coalesce(u.messages, 0) + coalesce(u.added_messages, 0) + coalesce(d.messages, 0))::bigint AS messages,
       (coalesce(u.bytes, 0) + coalesce(u.added_bytes, 0) + coalesce(d.bytes, 0))::bigint AS bytes,
       u.computed_at, coalesce(st.state, 'ok')::text AS state
FROM customer_groups g
LEFT JOIN storage_usage_groups u ON u.group_id = g.id
LEFT JOIN (SELECT gd.group_id, sum(gd.messages) AS messages, sum(gd.bytes) AS bytes
           FROM storage_group_deltas gd GROUP BY gd.group_id) d ON d.group_id = g.id
LEFT JOIN storage_states st ON st.group_id = g.id
ORDER BY lower(g.name), g.id;

-- name: AccountStorageUsage :many
-- Each account with its owner and group and its last snapshot (no row
-- before the first snapshot that saw it).
SELECT a.id, a.name, a.kind, (a.removed_at IS NOT NULL)::boolean AS removed, a.owner_id, o.name AS owner_name,
       o.group_id, u.messages, u.bytes, u.exclusive_bytes, u.computed_at
FROM accounts a
LEFT JOIN users o ON o.id = a.owner_id
LEFT JOIN storage_usage_accounts u ON u.account_id = a.id
ORDER BY o.name NULLS FIRST, a.name, a.id;
