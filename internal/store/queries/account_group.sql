-- The customer group of a save's account (#539, part 1; the same queries
-- as #397, part 1, which whichever lands second shares).

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

-- name: GroupHasLocations :many
-- The hashes of a batch that an account of the customer group holds, the
-- saving account included (gone locations too); group 0 is the accounts
-- without an owner. Driven from the group's own folders: one probe of
-- message_locations_folder_sha_idx per folder and hash, so the cost
-- depends only on the group's folders and the batch, never on how many
-- accounts of other groups hold the same bytes (#539). The LATERAL
-- subquery's LIMIT 1 is the fence: without it the planner flattens the
-- probes into a lookup of each hash in message_locations_sha_idx, in the
-- custom and in the generic plan (TestGroupHasLocationsPlan). OFFSET 0 on
-- the group's folders is the second fence of the shape #397 uses.
SELECT DISTINCT s.sha::text AS sha256
FROM unnest(@shas::bpchar[]) AS s (sha)
CROSS JOIN (
    SELECT f.id FROM folders f
    JOIN accounts a ON a.id = f.account_id
    LEFT JOIN users o ON o.id = a.owner_id
    WHERE coalesce(o.group_id, 0) = @group_id::bigint
    OFFSET 0
) gf
JOIN LATERAL (
    SELECT 1 FROM message_locations ml
    WHERE ml.folder_id = gf.id AND ml.message_sha256 = s.sha
    LIMIT 1
) held ON true;
