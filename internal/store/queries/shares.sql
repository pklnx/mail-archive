-- Shared accounts (#389): read access of other users to an account.

-- name: ListScopeAccounts :many
-- The accounts a user may read: their own and, with include_shared, those
-- shared with them. Removed accounts included; their mail stays readable.
SELECT a.id, a.name, u.id AS owner_id, u.name AS owner, a.kind,
       (a.removed_at IS NOT NULL)::boolean AS removed, (a.owner_id = @user_id::bigint)::boolean AS own
FROM accounts a
JOIN users u ON u.id = a.owner_id
WHERE a.owner_id = @user_id::bigint
   OR @include_shared::boolean
      AND a.id IN (SELECT s.account_id FROM account_shares s WHERE s.user_id = @user_id::bigint)
ORDER BY own DESC, u.name, a.name, a.id;

-- name: InsertAccountShare :execrows
-- No row when the share exists already.
INSERT INTO account_shares (account_id, user_id) VALUES (@account_id, @user_id)
ON CONFLICT (account_id, user_id) DO NOTHING;

-- name: DeleteAccountShare :execrows
DELETE FROM account_shares WHERE account_id = @account_id AND user_id = @user_id;

-- name: ListAccountShares :many
SELECT s.user_id, u.name AS user_name, s.created_at
FROM account_shares s
JOIN users u ON u.id = s.user_id
WHERE s.account_id = @account_id
ORDER BY u.name;
