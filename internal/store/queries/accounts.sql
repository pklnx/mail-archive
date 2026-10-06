-- name: CreateAccount :one
INSERT INTO accounts (name, host, port, tls_mode, username, password_enc, included_folders, excluded_folders, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
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

-- name: GetAccountByName :one
SELECT * FROM accounts WHERE name = $1;

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
