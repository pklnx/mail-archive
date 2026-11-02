-- Customer groups (#470).

-- name: InsertGroup :one
INSERT INTO customer_groups (name) VALUES (@name) RETURNING *;

-- name: LockGroup :one
-- The row stays locked until the transaction ends.
SELECT * FROM customer_groups WHERE id = @id FOR UPDATE;

-- name: RenameGroup :execrows
UPDATE customer_groups SET name = @name WHERE id = @id;

-- name: DeleteGroup :execrows
DELETE FROM customer_groups WHERE id = @id;

-- name: CountGroups :one
SELECT count(*) FROM customer_groups;

-- name: FirstGroups :many
-- The groups with the lowest ids, for "the only group": two rows mean
-- that there are several.
SELECT id FROM customer_groups ORDER BY id LIMIT 2;

-- name: GetGroupByID :one
SELECT * FROM customer_groups WHERE id = @id;

-- name: GetGroupByName :one
-- Group names are unique regardless of case.
SELECT * FROM customer_groups WHERE lower(name) = lower(@name);

-- name: ListGroups :many
-- Every group with its users and the accounts they own (removed ones too).
SELECT g.id, g.name, g.created_at,
       (SELECT count(*) FROM users u WHERE u.group_id = g.id) AS users,
       (SELECT count(*) FROM accounts a JOIN users u ON u.id = a.owner_id WHERE u.group_id = g.id) AS accounts
FROM customer_groups g
ORDER BY lower(g.name), g.id;

-- name: CountGroupUsers :one
SELECT count(*) FROM users WHERE group_id = @group_id;

-- name: SetUserGroup :execrows
UPDATE users SET group_id = @group_id WHERE id = @id;

-- name: LockUserGroups :many
-- The groups of the given users, locked FOR SHARE so that no group change
-- of theirs commits until the transaction ends. Ordered by id, so two
-- transactions lock the same rows in the same order.
SELECT id, group_id FROM users WHERE id = ANY(@ids::bigint[]) ORDER BY id FOR SHARE;

-- name: DeleteCrossingShares :many
-- The shares of a user that cross groups, as a group change of theirs
-- leaves them: the user's accounts shared with users of other groups and
-- other groups' accounts shared with the user.
WITH gone AS (
    DELETE FROM account_shares s
    USING accounts a, users o, users r
    WHERE a.id = s.account_id AND o.id = a.owner_id AND r.id = s.user_id
      AND (a.owner_id = @user_id::bigint OR s.user_id = @user_id::bigint)
      AND o.group_id <> r.group_id
    RETURNING s.account_id, s.user_id
)
SELECT a.name AS account, o.name AS owner, r.name AS user_name
FROM gone g
JOIN accounts a ON a.id = g.account_id
JOIN users o ON o.id = a.owner_id
JOIN users r ON r.id = g.user_id
ORDER BY o.name, a.name, r.name;

-- name: DeleteAllAccountShares :many
-- Every share of an account, when it moves to another group.
WITH gone AS (
    DELETE FROM account_shares WHERE account_id = @account_id RETURNING user_id
)
SELECT r.name AS user_name FROM gone g JOIN users r ON r.id = g.user_id ORDER BY r.name;

-- name: AccountGroupID :one
-- The group of an account: its owner's. NULL without an owner.
SELECT u.group_id FROM accounts a LEFT JOIN users u ON u.id = a.owner_id WHERE a.id = @account_id;

-- name: UserGroupID :one
SELECT group_id FROM users WHERE id = @id;
