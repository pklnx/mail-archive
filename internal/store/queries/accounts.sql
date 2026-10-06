-- name: CreateAccount :one
INSERT INTO accounts (name, host, port, tls_mode, username, password_enc, included_folders, excluded_folders, enabled, owner_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, created_at, version;

-- name: UpdatePassword :execrows
-- Only for a new account, inside the transaction that creates it.
UPDATE accounts SET password_enc = $2 WHERE id = $1;

-- name: ReplacePassword :execrows
-- Compare-and-swap: only if the password is still the one that was read.
-- The logical state does not change, so the version stays.
UPDATE accounts SET password_enc = @new_enc WHERE id = @id AND password_enc = @old_enc;

-- name: LockAccount :one
-- The row stays locked until the transaction ends.
SELECT * FROM accounts WHERE id = $1 FOR UPDATE;

-- name: WriteAccount :exec
UPDATE accounts
SET name = @name, host = @host, port = @port, tls_mode = @tls_mode, username = @username,
    password_enc = @password_enc, included_folders = @included_folders,
    excluded_folders = @excluded_folders, enabled = @enabled,
    version = version + 1, updated_at = now()
WHERE id = @id;

-- name: CountAccountLocations :one
SELECT count(*) FROM message_locations l
JOIN folders f ON f.id = l.folder_id
WHERE f.account_id = $1;

-- name: DeleteAccountFolders :exec
DELETE FROM folders WHERE account_id = $1;

-- name: DeleteAccount :execrows
DELETE FROM accounts WHERE id = $1;

-- name: ListAccountsByName :many
-- Accounts of all users with this name, for the CLI.
SELECT * FROM accounts WHERE name = $1 ORDER BY owner_id NULLS FIRST;

-- name: GetOwnedAccount :one
SELECT * FROM accounts WHERE owner_id = $1 AND name = $2;

-- name: ListOwnedAccounts :many
SELECT * FROM accounts WHERE owner_id = $1 ORDER BY name;

-- name: SetAccountOwner :execrows
UPDATE accounts SET owner_id = $2, version = version + 1, updated_at = now() WHERE id = $1;

-- name: AdoptUnownedAccounts :execrows
UPDATE accounts SET owner_id = $1, version = version + 1, updated_at = now() WHERE owner_id IS NULL;

-- name: CountOwnedAccounts :many
SELECT owner_id, count(*) AS accounts FROM accounts WHERE owner_id IS NOT NULL GROUP BY owner_id;

-- name: ListAccounts :many
SELECT * FROM accounts ORDER BY name;

-- name: RemoveAccount :execrows
-- Keeps the archived data; wipes the credentials.
UPDATE accounts
SET removed_at = now(), enabled = FALSE, password_enc = ''::bytea, version = version + 1, updated_at = now()
WHERE id = $1 AND removed_at IS NULL;

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = $1;

-- name: UserOwnsAccounts :one
SELECT EXISTS (SELECT 1 FROM accounts WHERE owner_id = $1);
