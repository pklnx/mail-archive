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
SELECT r.id, r.account_id, a.name AS account, a.kind AS account_kind, (a.removed_at IS NOT NULL)::boolean AS account_removed,
       u.name AS owner, r.folder, r.years, r.time_zone, r.note, r.created_at, cu.name AS created_by
FROM retention_rules r
JOIN accounts a ON a.id = r.account_id
LEFT JOIN users u ON u.id = a.owner_id
LEFT JOIN users cu ON cu.id = r.created_by
ORDER BY u.name NULLS FIRST, a.name, r.folder NULLS FIRST;

-- name: DeleteRetentionRule :one
DELETE FROM retention_rules WHERE id = @id
RETURNING account_id, folder, years, time_zone;

-- name: InsertLegalHold :one
INSERT INTO legal_holds (name, reason, owner_id, account_id, filter, period_start, period_end, created_by)
VALUES (@name, @reason, sqlc.narg(owner_id), sqlc.narg(account_id), sqlc.narg(filter), sqlc.narg(period_start),
        sqlc.narg(period_end), sqlc.narg(created_by))
RETURNING id, created_at;

-- name: ListLegalHolds :many
SELECT h.id, h.name, h.reason, h.owner_id, ou.name AS owner, h.account_id, a.name AS account, au.name AS account_owner,
       h.filter, h.period_start, h.period_end, h.matched_at, h.created_at, cu.name AS created_by,
       h.released_at, ru.name AS released_by, h.release_note,
       (SELECT count(*) FROM legal_hold_messages hm WHERE hm.hold_id = h.id)::bigint AS matched
FROM legal_holds h
LEFT JOIN users ou ON ou.id = h.owner_id
LEFT JOIN accounts a ON a.id = h.account_id
LEFT JOIN users au ON au.id = a.owner_id
LEFT JOIN users cu ON cu.id = h.created_by
LEFT JOIN users ru ON ru.id = h.released_by
WHERE @released::boolean OR h.released_at IS NULL
ORDER BY h.id;

-- name: ActiveFilterHolds :many
-- The active holds whose filter is matched by search, with the owner of
-- their account.
SELECT h.id, h.owner_id, h.account_id, a.name AS account, a.owner_id AS account_owner, h.filter
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

-- name: SetLegalHoldMatched :exec
UPDATE legal_holds SET matched_at = @matched_at::timestamptz WHERE id = @id AND released_at IS NULL;

-- name: ReleaseLegalHold :one
UPDATE legal_holds SET released_at = now(), released_by = sqlc.narg(released_by), release_note = @note::text
WHERE id = @id AND released_at IS NULL
RETURNING name, owner_id, account_id;

-- name: LegalHoldExists :one
SELECT EXISTS (SELECT 1 FROM legal_holds WHERE id = @id);

-- name: ActiveHoldsOnOwner :many
-- Active holds that cover an account through its owner: moving the
-- account to another user would release its mail from them.
SELECT id FROM legal_holds
WHERE released_at IS NULL AND owner_id = @owner_id::bigint
  AND (account_id IS NULL OR account_id = @account_id::bigint)
ORDER BY id;

-- name: HoldsNamingUser :many
SELECT id FROM legal_holds WHERE owner_id = @owner_id::bigint ORDER BY id;

-- name: HoldsNamingAccount :many
SELECT id FROM legal_holds WHERE account_id = @account_id::bigint ORDER BY id;

-- name: RetentionCounts :one
SELECT (SELECT count(*) FROM retention_rules)::bigint AS rules,
       (SELECT count(*) FROM legal_holds WHERE released_at IS NULL)::bigint AS active_holds;

-- name: ListRetentionAccounts :many
-- Every account with its owner and folders, for choosing a rule's or a
-- hold's account.
SELECT a.id, a.name, a.kind, (a.removed_at IS NOT NULL)::boolean AS removed, u.name AS owner,
       coalesce((SELECT array_agg(f.name ORDER BY f.name) FROM folders f WHERE f.account_id = a.id), '{}')::text[] AS folders
FROM accounts a
LEFT JOIN users u ON u.id = a.owner_id
ORDER BY u.name NULLS FIRST, a.name;
