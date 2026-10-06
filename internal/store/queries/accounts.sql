-- name: CreateAccount :one
INSERT INTO accounts (name, host, port, tls_mode, username, password_enc, included_folders, excluded_folders, enabled, owner_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, created_at;

-- name: UpdatePassword :execrows
UPDATE accounts SET password_enc = $2, updated_at = now() WHERE id = $1 AND removed_at IS NULL;

-- name: SetAccountEnabled :execrows
UPDATE accounts SET enabled = $2, updated_at = now() WHERE id = $1 AND removed_at IS NULL;

-- name: SetFolderFilters :execrows
UPDATE accounts SET included_folders = $2, excluded_folders = $3, updated_at = now() WHERE id = $1 AND removed_at IS NULL;

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
UPDATE accounts SET owner_id = $2, updated_at = now() WHERE id = $1;

-- name: AdoptUnownedAccounts :execrows
UPDATE accounts SET owner_id = $1, updated_at = now() WHERE owner_id IS NULL;

-- name: CountOwnedAccounts :many
SELECT owner_id, count(*) AS accounts FROM accounts WHERE owner_id IS NOT NULL GROUP BY owner_id;

-- name: ListAccounts :many
SELECT * FROM accounts ORDER BY name;

-- name: RemoveAccount :execrows
-- Keeps the archived data; wipes the credentials.
UPDATE accounts
SET removed_at = now(), enabled = FALSE, password_enc = ''::bytea, updated_at = now()
WHERE id = $1 AND removed_at IS NULL;

-- name: UpdateAccountConnection :execrows
UPDATE accounts
SET host = $2, port = $3, tls_mode = $4, username = $5, password_enc = $6, updated_at = now()
WHERE id = $1 AND removed_at IS NULL;

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = $1;

-- name: RenameAccount :execrows
-- The password is encrypted with the account name as context, so it changes too.
UPDATE accounts SET name = $2, password_enc = $3, updated_at = now()
WHERE id = $1 AND removed_at IS NULL;

-- name: UserOwnsAccounts :one
SELECT EXISTS (SELECT 1 FROM accounts WHERE owner_id = $1);
