-- name: LockRetentionChanges :exec
-- Serializes changes to rules and holds with each other, and later with
-- the batches of a deletion run, until the transaction ends.
SELECT pg_advisory_xact_lock(@class::int, 1);

-- name: CheckTimeZone :exec
-- Fails for a time zone PostgreSQL does not know.
SELECT now() AT TIME ZONE @time_zone::text;

-- name: InsertRetentionRule :one
INSERT INTO retention_rules (account_id, folder, years, time_zone, note, created_by)
VALUES (@account_id, sqlc.narg(folder), @years, @time_zone, @note, sqlc.narg(created_by))
RETURNING id, created_at;

-- name: ListRetentionRules :many
-- With the customer group of the account (its owner's, #470).
SELECT r.id, r.account_id, a.name AS account, a.kind AS account_kind, (a.removed_at IS NOT NULL)::boolean AS account_removed,
       u.name AS owner, r.folder, r.years, r.time_zone, r.note, r.created_at, cu.name AS created_by,
       g.id AS group_id, g.name AS group_name
FROM retention_rules r
JOIN accounts a ON a.id = r.account_id
LEFT JOIN users u ON u.id = a.owner_id
LEFT JOIN customer_groups g ON g.id = u.group_id
LEFT JOIN users cu ON cu.id = r.created_by
ORDER BY u.name NULLS FIRST, a.name, r.folder NULLS FIRST;

-- name: DeleteRetentionRule :one
DELETE FROM retention_rules WHERE id = @id
RETURNING account_id, folder, years, time_zone;

-- name: InsertLegalHold :one
INSERT INTO legal_holds (name, reason, owner_id, account_id, group_id, filter, period_start, period_end, created_by)
VALUES (@name, @reason, sqlc.narg(owner_id), sqlc.narg(account_id), sqlc.narg(group_id), sqlc.narg(filter),
        sqlc.narg(period_start), sqlc.narg(period_end), sqlc.narg(created_by))
RETURNING id, created_at;

-- name: ListLegalHolds :many
-- group_id is the group the hold names (#470); covered_group_id the group
-- whose mail it covers: that one, or the group of its user or of its
-- account's owner. NULL: every group.
SELECT h.id, h.name, h.reason, h.owner_id, ou.name AS owner, h.account_id, a.name AS account, au.name AS account_owner,
       a.owner_id AS account_owner_id,
       h.filter, h.period_start, h.period_end, h.matched_at, h.created_at, cu.name AS created_by,
       h.released_at, ru.name AS released_by, h.release_note,
       (SELECT count(*) FROM legal_hold_messages hm WHERE hm.hold_id = h.id)::bigint AS matched,
       h.group_id, hg.name AS group_name, cg.id AS covered_group_id, cg.name AS covered_group_name
FROM legal_holds h
LEFT JOIN users ou ON ou.id = h.owner_id
LEFT JOIN accounts a ON a.id = h.account_id
LEFT JOIN users au ON au.id = a.owner_id
LEFT JOIN customer_groups hg ON hg.id = h.group_id
LEFT JOIN customer_groups cg ON cg.id = coalesce(h.group_id, ou.group_id, au.group_id)
LEFT JOIN users cu ON cu.id = h.created_by
LEFT JOIN users ru ON ru.id = h.released_by
WHERE @released::boolean OR h.released_at IS NULL
ORDER BY h.id;

-- name: ActiveFilterHolds :many
-- The active holds whose filter is matched by search, with the owner of
-- their account and the group they name.
SELECT h.id, h.owner_id, h.account_id, a.name AS account, a.owner_id AS account_owner, h.group_id, h.filter
FROM legal_holds h
LEFT JOIN accounts a ON a.id = h.account_id
WHERE h.released_at IS NULL AND h.filter IS NOT NULL
ORDER BY h.id;

-- name: AddLegalHoldMessages :execrows
-- Only while the hold is active.
INSERT INTO legal_hold_messages (hold_id, message_sha256)
SELECT @hold_id::bigint, s FROM unnest(@shas::text[]) s
WHERE EXISTS (SELECT 1 FROM legal_holds WHERE id = @hold_id::bigint AND released_at IS NULL)
ON CONFLICT DO NOTHING;

-- name: CountLegalHoldMessages :one
-- The messages one hold's filter matched so far.
SELECT count(*)::bigint FROM legal_hold_messages WHERE hold_id = @hold_id::bigint;

-- name: SetLegalHoldMatched :exec
UPDATE legal_holds SET matched_at = @matched_at::timestamptz WHERE id = @id AND released_at IS NULL;

-- name: ReleaseLegalHold :one
UPDATE legal_holds SET released_at = now(), released_by = sqlc.narg(released_by), release_note = @note::text
WHERE id = @id AND released_at IS NULL
RETURNING name, owner_id, account_id, group_id;

-- name: LegalHoldExists :one
SELECT EXISTS (SELECT 1 FROM legal_holds WHERE id = @id);

-- name: ActiveHoldsOnOwner :many
-- Active holds that cover the account now, through its owner or the
-- owner's customer group (#470), and would not once it belongs to the new
-- owner in the new group: moving it would release its mail from them.
-- The other conditions (period, filter) do not change with the owner.
SELECT id FROM legal_holds
WHERE released_at IS NULL
  AND (account_id IS NULL OR account_id = @account_id::bigint)
  AND (owner_id IS NULL OR owner_id = @owner_id::bigint)
  AND (group_id IS NULL OR group_id = @group_id::bigint)
  AND NOT ((owner_id IS NULL OR owner_id = @new_owner_id::bigint)
           AND (group_id IS NULL OR group_id = @new_group_id::bigint))
ORDER BY id;

-- name: ActiveGroupHoldsOnUser :many
-- Active holds naming the user's group that cover one of the accounts the
-- user owns: moving the user to another group would release that mail
-- (#470). A user without accounts releases nothing.
SELECT h.id FROM legal_holds h
WHERE h.released_at IS NULL AND h.group_id = @group_id::bigint
  AND (h.owner_id IS NULL OR h.owner_id = @owner_id::bigint)
  AND EXISTS (SELECT 1 FROM accounts a
              WHERE a.owner_id = @owner_id::bigint AND (h.account_id IS NULL OR h.account_id = a.id))
ORDER BY h.id;

-- name: HoldsNamingUser :many
SELECT id FROM legal_holds WHERE owner_id = @owner_id::bigint ORDER BY id;

-- name: HoldsNamingAccount :many
SELECT id FROM legal_holds WHERE account_id = @account_id::bigint ORDER BY id;

-- name: RetentionCounts :one
SELECT (SELECT count(*) FROM retention_rules)::bigint AS rules,
       (SELECT count(*) FROM legal_holds WHERE released_at IS NULL)::bigint AS active_holds;

-- name: ListRetentionAccounts :many
-- Every account with its owner, its owner's group and its folders, for
-- choosing a rule's or a hold's account.
SELECT a.id, a.name, a.kind, (a.removed_at IS NOT NULL)::boolean AS removed, u.name AS owner,
       coalesce((SELECT array_agg(f.name ORDER BY f.name) FROM folders f WHERE f.account_id = a.id), '{}')::text[] AS folders,
       g.id AS group_id, g.name AS group_name
FROM accounts a
LEFT JOIN users u ON u.id = a.owner_id
LEFT JOIN customer_groups g ON g.id = u.group_id
ORDER BY u.name NULLS FIRST, a.name;

-- name: LegalHoldScope :one
-- Whom a hold names, which decides the class of its audit entries (#470).
SELECT owner_id, account_id, group_id FROM legal_holds WHERE id = @id;

-- name: LockUserGroupShare :one
-- The user's group, the row locked FOR SHARE: a hold naming the user and a
-- group is checked against a group that cannot change until the commit.
SELECT group_id FROM users WHERE id = @id FOR SHARE;

-- name: LockAccountOwnerShare :one
-- The account's owner, the row locked FOR SHARE: no move of the account
-- commits until the transaction ends.
SELECT owner_id FROM accounts WHERE id = @id FOR SHARE;
