-- Auditor access (#393). An active grant is only what active_auditor_grants
-- says (00036), and its accounts only what active_auditor_grant_accounts
-- says.

-- name: GetAuditorByNameInGroup :one
-- One query whether the name is unknown, a member or admin, or a user of
-- another group, so that none can be told apart.
SELECT * FROM users WHERE name = @name AND group_id = @group_id AND is_auditor;

-- name: UserIsAuditor :one
-- The role never changes (users_auditor_fixed), so no lock is needed.
SELECT is_auditor FROM users WHERE id = @id;

-- name: InsertGrant :one
INSERT INTO auditor_grants (auditor_id, owner_id, label, period_from, period_until, starts_at, ends_at)
VALUES (@auditor_id, @owner_id, @label, sqlc.narg(period_from), sqlc.narg(period_until), @starts_at, @ends_at)
RETURNING id;

-- name: InsertGrantAccount :exec
INSERT INTO auditor_grant_accounts (grant_id, account_id, all_folders) VALUES (@grant_id, @account_id, @all_folders);

-- name: InsertGrantFolder :exec
INSERT INTO auditor_grant_folders (grant_id, account_id, folder_id) VALUES (@grant_id, @account_id, @folder_id);

-- name: LockAccountsForGrant :many
-- The named accounts, locked FOR SHARE in id order: the first step of the
-- lock order (accounts, users, grant rows).
SELECT id, owner_id, kind, name FROM accounts WHERE id = ANY (@ids::bigint[]) ORDER BY id FOR SHARE;

-- name: AccountFolderIDs :many
SELECT id FROM folders WHERE account_id = @account_id AND id = ANY (@ids::bigint[]);

-- name: GetGrantUsers :one
-- The grant's users, read without a lock: owner_id never changes, and
-- auditor_id only to NULL on a revoked grant (00036).
SELECT id, owner_id, auditor_id FROM auditor_grants WHERE id = @id AND owner_id = @owner_id;

-- name: LockGrant :one
SELECT g.id, g.auditor_id, g.owner_id, g.label, g.starts_at, g.ends_at, g.revoked_at,
       a.name AS auditor_name, o.group_id AS owner_group
FROM auditor_grants g
JOIN users o ON o.id = g.owner_id
LEFT JOIN users a ON a.id = g.auditor_id
WHERE g.id = @id
FOR UPDATE OF g;

-- name: SetGrantEnd :exec
UPDATE auditor_grants SET ends_at = @ends_at WHERE id = @id;

-- name: RevokeGrants :many
-- Revokes the given grants that are not revoked yet and returns them with
-- the names and the owner's group they had.
UPDATE auditor_grants g
SET revoked_at = now(), revoked_by = sqlc.narg(revoked_by), revoke_reason = @reason::text
FROM users o
WHERE g.id = ANY (@ids::bigint[]) AND g.revoked_at IS NULL AND o.id = g.owner_id
RETURNING g.id, g.label, g.owner_id, g.auditor_id, o.name AS owner_name, o.group_id AS owner_group,
          coalesce((SELECT a.name FROM users a WHERE a.id = g.auditor_id), '')::text AS auditor_name;

-- name: RevokeCrossingGrants :many
-- The unrevoked grants of a user whose two users are now in different
-- groups, as the user's group change leaves them; expired and future ones
-- too, so that no extension brings one back. old_group is the group the
-- grant was in: the group of the user who did not move.
UPDATE auditor_grants g
SET revoked_at = now(), revoked_by = sqlc.narg(revoked_by), revoke_reason = 'group_change'
FROM users a, users o
WHERE a.id = g.auditor_id AND o.id = g.owner_id
  AND g.revoked_at IS NULL
  AND (g.auditor_id = @user_id::bigint OR g.owner_id = @user_id::bigint)
  AND a.group_id <> o.group_id
RETURNING g.id, g.label, g.owner_id, g.auditor_id, o.name AS owner_name, a.name AS auditor_name,
          (CASE WHEN g.auditor_id = @user_id::bigint THEN o.group_id ELSE a.group_id END)::bigint AS old_group;

-- name: UnrevokedGrantsOfAuditor :many
SELECT id FROM auditor_grants WHERE auditor_id = @auditor_id::bigint AND revoked_at IS NULL ORDER BY id;

-- name: LockAccountGrants :many
-- The unrevoked grants that name an account, locked FOR UPDATE in id
-- order: the last step of the lock order (accounts, users, grant rows). A
-- revocation not committed yet makes this wait, and the grant it revoked
-- is then left out.
SELECT g.id FROM auditor_grants g
WHERE g.revoked_at IS NULL
  AND EXISTS (SELECT 1 FROM auditor_grant_accounts ga WHERE ga.grant_id = g.id AND ga.account_id = @account_id)
ORDER BY g.id
FOR UPDATE;

-- name: DropAccountFromGrants :many
-- Takes an account out of the given (locked, unrevoked) grants; rows of
-- revoked grants stay as history. Returns the grants with the accounts
-- they have left.
WITH gone AS (
    DELETE FROM auditor_grant_accounts ga
    USING auditor_grants g
    WHERE ga.grant_id = g.id AND ga.account_id = @account_id AND g.id = ANY (@grant_ids::bigint[]) AND g.revoked_at IS NULL
    RETURNING ga.grant_id
)
SELECT g.id, g.label, g.owner_id, g.auditor_id, o.group_id AS owner_group,
       coalesce((SELECT a.name FROM users a WHERE a.id = g.auditor_id), '')::text AS auditor_name,
       (SELECT count(*) FROM auditor_grant_accounts r
        WHERE r.grant_id = g.id AND r.account_id <> @account_id)::bigint AS accounts_left
FROM gone x
JOIN auditor_grants g ON g.id = x.grant_id
JOIN users o ON o.id = g.owner_id
ORDER BY g.id;

-- name: DeleteGrantSessions :exec
DELETE FROM sessions WHERE grant_id = ANY (@grant_ids::bigint[]);

-- name: SetSessionGrant :execrows
-- Binds an auditor's session to one of their active grants; the session
-- ends with the grant at the latest.
UPDATE sessions s
SET grant_id = g.id, expires_at = LEAST(s.expires_at, g.ends_at)
FROM active_auditor_grants g
WHERE s.id = @id AND s.user_id = @auditor_id AND g.id = @grant_id AND g.auditor_id = @auditor_id;

-- name: AuditorScopeAccounts :many
-- The accounts of an active grant of the auditor, with their granted
-- folders: a folder counts only under its own account.
SELECT a.id, a.name, a.owner_id::bigint AS owner_id, u.name AS owner, a.kind, (a.removed_at IS NOT NULL)::boolean AS removed,
       ga.all_folders, ga.period_from, ga.period_until,
       array(SELECT f.id FROM auditor_grant_folders gf
             JOIN folders f ON f.id = gf.folder_id AND f.account_id = gf.account_id
             WHERE gf.grant_id = ga.grant_id AND gf.account_id = ga.account_id
             ORDER BY f.id)::bigint[] AS folders
FROM active_auditor_grant_accounts ga
JOIN accounts a ON a.id = ga.account_id
JOIN users u ON u.id = a.owner_id
WHERE ga.grant_id = @grant_id AND ga.auditor_id = @auditor_id::bigint
ORDER BY u.name, a.name, a.id;

-- name: ListActiveGrants :many
-- The auditor's active grants, for the choice after login.
SELECT g.id, g.owner_id, o.name AS owner_name, g.label, g.period_from, g.period_until, g.starts_at, g.ends_at
FROM active_auditor_grants g
JOIN users o ON o.id = g.owner_id
WHERE g.auditor_id = @auditor_id::bigint
ORDER BY g.ends_at, g.id;

-- name: ListOwnerGrants :many
-- An owner's grants, history included, newest first. The auditor's name
-- only while the auditor is in the owner's group.
SELECT g.id, g.owner_id, o.name AS owner_name, g.label, g.period_from, g.period_until, g.starts_at, g.ends_at,
       g.revoked_at, g.revoke_reason, g.created_at,
       a.id AS auditor_id, a.name AS auditor_name,
       EXISTS (SELECT 1 FROM active_auditor_grants v WHERE v.id = g.id)::boolean AS active
FROM auditor_grants g
JOIN users o ON o.id = g.owner_id
LEFT JOIN users a ON a.id = g.auditor_id AND a.group_id = o.group_id
WHERE g.owner_id = @owner_id
ORDER BY g.created_at DESC, g.id DESC;

-- name: ListOwnerGrantAccounts :many
-- The accounts of an owner's grants that the owner still owns, with the
-- names of their granted folders.
SELECT ga.grant_id, a.id AS account_id, a.name, ga.all_folders,
       array(SELECT f.name FROM auditor_grant_folders gf
             JOIN folders f ON f.id = gf.folder_id AND f.account_id = gf.account_id
             WHERE gf.grant_id = ga.grant_id AND gf.account_id = ga.account_id
             ORDER BY f.name)::text[] AS folders
FROM auditor_grant_accounts ga
JOIN auditor_grants g ON g.id = ga.grant_id
JOIN accounts a ON a.id = ga.account_id AND a.owner_id = g.owner_id AND a.kind <> 'smtp'
WHERE g.owner_id = @owner_id
ORDER BY ga.grant_id, a.name, a.id;

-- name: ListAuditorGrants :many
-- An auditor's grants whose owner is in the auditor's current group,
-- history included, newest first.
SELECT g.id, g.owner_id, o.name AS owner_name, g.label, g.period_from, g.period_until, g.starts_at, g.ends_at,
       g.revoked_at, g.revoke_reason, g.created_at,
       a.id AS auditor_id, a.name AS auditor_name,
       EXISTS (SELECT 1 FROM active_auditor_grants v WHERE v.id = g.id)::boolean AS active
FROM auditor_grants g
JOIN users a ON a.id = g.auditor_id
JOIN users o ON o.id = g.owner_id AND o.group_id = a.group_id
WHERE g.auditor_id = @auditor_id::bigint
ORDER BY g.created_at DESC, g.id DESC;

-- name: ListAuditorGrantAccounts :many
-- The accounts of the auditor's active grants, only through the active
-- view: an ended grant shows no accounts (coordinator decision 31).
SELECT ga.grant_id, a.id AS account_id, a.name, ga.all_folders,
       array(SELECT f.name FROM auditor_grant_folders gf
             JOIN folders f ON f.id = gf.folder_id AND f.account_id = gf.account_id
             WHERE gf.grant_id = ga.grant_id AND gf.account_id = ga.account_id
             ORDER BY f.name)::text[] AS folders
FROM active_auditor_grant_accounts ga
JOIN accounts a ON a.id = ga.account_id
WHERE ga.auditor_id = @auditor_id::bigint
ORDER BY ga.grant_id, a.name, a.id;
