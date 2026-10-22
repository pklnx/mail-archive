-- name: PurgeCounts :one
-- What a purge of the account removes: its locations, its messages, and
-- of those the ones without a location in any other account (deleted).
WITH loc AS (
    SELECT l.message_sha256 FROM message_locations l
    JOIN folders f ON f.id = l.folder_id
    WHERE f.account_id = @account_id::bigint
), msgs AS (
    SELECT DISTINCT message_sha256 FROM loc
), last AS (
    SELECT s.message_sha256 FROM msgs s
    WHERE NOT EXISTS (SELECT 1 FROM message_locations o
                      JOIN folders fo ON fo.id = o.folder_id
                      WHERE o.message_sha256 = s.message_sha256 AND fo.account_id <> @account_id::bigint)
)
SELECT (SELECT count(*) FROM loc)::bigint AS locations,
       (SELECT count(*) FROM msgs)::bigint AS messages,
       (SELECT count(*) FROM last)::bigint AS deleted,
       (SELECT coalesce(sum(m.size), 0) FROM last JOIN messages m ON m.sha256 = last.message_sha256)::bigint AS deleted_bytes;

-- name: PurgeKeptIn :many
-- Where the account's messages that a purge keeps are too: counts by
-- owner and account, no message details.
SELECT coalesce(u.name, '')::text AS owner, a.name AS account, count(DISTINCT o.message_sha256)::bigint AS messages
FROM message_locations o
JOIN folders fo ON fo.id = o.folder_id
JOIN accounts a ON a.id = fo.account_id
LEFT JOIN users u ON u.id = a.owner_id
WHERE a.id <> @account_id::bigint
  AND o.message_sha256 IN (SELECT l.message_sha256 FROM message_locations l
                           JOIN folders f ON f.id = l.folder_id
                           WHERE f.account_id = @account_id::bigint)
GROUP BY 1, 2
ORDER BY 1, 2;

-- name: PurgeRules :many
-- Every rule of the account, also for folders it does not have: deleting
-- the account would drop them (ON DELETE CASCADE).
SELECT id, folder, years FROM retention_rules WHERE account_id = @account_id::bigint ORDER BY id;

-- name: PurgeHolds :many
-- The holds that keep the account: every hold naming it, released ones
-- too (their record keeps it), and every active hold covering one of its
-- locations (location_holds, the conditions of location_held).
SELECT h.id, h.name, (h.released_at IS NOT NULL)::boolean AS released,
       coalesce(h.account_id = @account_id::bigint, false)::boolean AS names_account
FROM legal_holds h
WHERE h.account_id = @account_id::bigint
   OR h.id IN (SELECT location_holds(l.message_sha256, l.folder_id, m.sent_at, l.internal_date)
               FROM message_locations l
               JOIN folders f ON f.id = l.folder_id
               JOIN messages m ON m.sha256 = l.message_sha256
               WHERE f.account_id = @account_id::bigint)
ORDER BY h.id;

-- name: PurgeNamingBlockers :one
-- The blockers a purge re-checks in every batch and at the end: rules of
-- the account and holds naming it.
SELECT (SELECT count(*) FROM retention_rules r WHERE r.account_id = @account_id::bigint)::bigint AS rules,
       (SELECT count(*) FROM legal_holds h WHERE h.account_id = @account_id::bigint)::bigint AS holds;

-- name: InsertAccountPurge :one
INSERT INTO account_purges (account_id, account_name, account_kind, account_version, owner_id, owner_name, expected, created_by)
VALUES (@account_id, @account_name, @account_kind, @account_version, sqlc.narg(owner_id), sqlc.narg(owner_name), @expected,
        sqlc.narg(created_by))
RETURNING id;

-- name: GetAccountPurge :one
SELECT p.id, p.account_id, p.account_name, p.account_kind, p.account_version, p.owner_id, p.owner_name, p.state,
       p.expected, p.created_at, cu.name AS created_by, p.finished_at, p.locations, p.deleted, p.kept, p.note
FROM account_purges p
LEFT JOIN users cu ON cu.id = p.created_by
WHERE p.id = @id;

-- name: LockAccountPurge :one
SELECT id, account_id, account_version, owner_id, state, expected, deleted
FROM account_purges WHERE id = @id FOR UPDATE;

-- name: OpenAccountPurgeID :one
SELECT id FROM account_purges WHERE state = 'confirmed';

-- name: PurgeBatchShas :many
-- The next messages of the account to purge, in hash order. Purged ones
-- leave the account, so no cursor is needed.
SELECT DISTINCT l.message_sha256::text AS sha256
FROM message_locations l
JOIN folders f ON f.id = l.folder_id
WHERE f.account_id = @account_id::bigint
ORDER BY 1
LIMIT @row_limit;

-- name: PurgeHeldOf :one
-- Whether an active hold covers one of the account's locations of shas.
SELECT coalesce(bool_or(location_held(l.message_sha256, l.folder_id, m.sent_at, l.internal_date)), false)::boolean AS held
FROM message_locations l
JOIN folders f ON f.id = l.folder_id
JOIN messages m ON m.sha256 = l.message_sha256
WHERE f.account_id = @account_id::bigint AND l.message_sha256 = ANY (@shas::bpchar[]);

-- name: PurgeLastOf :many
-- Of shas, those without a location outside the account: the purge
-- deletes them.
SELECT s::text AS sha256
FROM unnest(@shas::bpchar[]) s
WHERE NOT EXISTS (SELECT 1 FROM message_locations o
                  JOIN folders fo ON fo.id = o.folder_id
                  WHERE o.message_sha256 = s AND fo.account_id <> @account_id::bigint)
ORDER BY 1;

-- name: InsertPurgeTombstones :execrows
-- No rule covers a purged message, so it has no end of a retention
-- period.
INSERT INTO deleted_messages (sha256, size, message_seq, archived_at, retain_until, locations, purge_id)
SELECT m.sha256, m.size, m.seq, m.created_at, NULL,
       (SELECT count(*) FROM message_locations l WHERE l.message_sha256 = m.sha256),
       @purge_id::bigint
FROM messages m
WHERE m.sha256 = ANY (@shas::bpchar[])
ORDER BY m.seq;

-- name: InsertPurgeDeletions :execrows
-- The deletion records of the attestation chain (#66), in the same
-- transaction as the tombstones.
INSERT INTO message_deletions (message_seq, sha256, reason)
SELECT seq, sha256, 'account_purge' FROM messages WHERE sha256 = ANY (@shas::bpchar[]) ORDER BY seq;

-- name: SetPurgeAccount :exec
-- Opens the retention guard for the unruled locations of this removed
-- account until the transaction ends.
SELECT set_config('mail_archive.purge_account', @account::text, true);

-- name: DeleteAccountLocationsOf :execrows
DELETE FROM message_locations l
USING folders f
WHERE f.id = l.folder_id AND f.account_id = @account_id::bigint AND l.message_sha256 = ANY (@shas::bpchar[]);

-- name: AddAccountPurgeCounts :exec
UPDATE account_purges
SET locations = locations + @locations::bigint, deleted = deleted + @deleted::int, kept = kept + @kept::int
WHERE id = @id;

-- name: FinishAccountPurge :execrows
UPDATE account_purges SET state = 'done', finished_at = now() WHERE id = @id AND state = 'confirmed';

-- name: CancelAccountPurge :execrows
UPDATE account_purges SET state = 'cancelled', finished_at = now(), note = @note
WHERE id = @id AND state = 'confirmed';
