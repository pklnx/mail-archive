-- SMTP credentials of smtp accounts (#395). Whether a secret has expired is
-- decided in Go with the caller's clock, so the queries take @now where
-- they need the time.

-- name: ListSMTPCredentials :many
SELECT id, account_id, hint, created_at, expires_at, last_used_at, revoked_at
FROM smtp_credentials WHERE account_id = $1
ORDER BY created_at DESC, id DESC;

-- name: ListAllSMTPCredentials :many
-- Every smtp account's secrets for admins, also of removed accounts.
SELECT c.id, c.account_id, c.hint, c.created_at, c.expires_at, c.last_used_at, c.revoked_at,
       a.name AS account_name, a.removed_at AS account_removed_at, u.name AS owner_name
FROM smtp_credentials c
JOIN accounts a ON a.id = c.account_id
LEFT JOIN users u ON u.id = a.owner_id
ORDER BY a.name, a.id, c.created_at DESC, c.id DESC;

-- name: LockUnrevokedSMTPCredentials :many
-- The account's row is locked by the caller.
SELECT id, created_at, expires_at FROM smtp_credentials
WHERE account_id = $1 AND revoked_at IS NULL
ORDER BY id
FOR UPDATE;

-- name: SupersedeSMTPCredential :exec
UPDATE smtp_credentials SET expires_at = sqlc.arg(expires_at)::timestamptz WHERE id = @id;

-- name: RevokeSupersededSMTPCredential :exec
UPDATE smtp_credentials SET revoked_at = sqlc.arg(now)::timestamptz, revoked_by = sqlc.narg(revoked_by) WHERE id = @id;

-- name: InsertSMTPCredential :one
INSERT INTO smtp_credentials (account_id, secret_hash, hint, created_at, created_by)
VALUES (@account_id, @secret_hash, @hint, @now, sqlc.narg(created_by))
RETURNING id, created_at;

-- name: RevokeSMTPCredential :one
-- account is NULL for an admin, who may revoke any secret.
UPDATE smtp_credentials c SET revoked_at = sqlc.arg(now)::timestamptz, revoked_by = sqlc.narg(revoked_by)
FROM accounts a
WHERE c.id = @id AND c.revoked_at IS NULL AND a.id = c.account_id
  AND (sqlc.narg(account)::bigint IS NULL OR c.account_id = sqlc.narg(account))
RETURNING c.id, c.account_id, c.hint, a.name AS account_name;

-- name: GetSMTPCredentialLogin :one
-- The secret with its account's kind and state, whatever its own state:
-- the caller tells why a login is refused.
SELECT c.id, c.account_id, c.expires_at, c.revoked_at, c.last_used_at,
       a.kind, a.removed_at
FROM smtp_credentials c JOIN accounts a ON a.id = c.account_id
WHERE c.secret_hash = $1;

-- name: GetSMTPCredentialState :one
SELECT c.id, c.account_id, c.expires_at, c.revoked_at, c.last_used_at,
       a.kind, a.removed_at
FROM smtp_credentials c JOIN accounts a ON a.id = c.account_id
WHERE c.id = $1;

-- name: TouchSMTPCredential :exec
UPDATE smtp_credentials SET last_used_at = sqlc.arg(now)::timestamptz
WHERE id = @id AND (last_used_at IS NULL OR last_used_at < sqlc.arg(now)::timestamptz - interval '1 minute');

-- name: CountUsableSMTPCredentialsByOwner :many
-- Usable secrets of the user's smtp accounts that are not removed, by
-- account.
SELECT a.id AS account_id, a.name AS account_name, count(*) AS n
FROM smtp_credentials c JOIN accounts a ON a.id = c.account_id
WHERE a.owner_id = sqlc.arg(owner)::bigint AND a.removed_at IS NULL AND a.kind = 'smtp'
  AND c.revoked_at IS NULL AND (c.expires_at IS NULL OR c.expires_at > sqlc.arg(now)::timestamptz)
GROUP BY a.id, a.name
ORDER BY a.name, a.id;

-- name: CountUsableSMTPCredentials :one
SELECT count(*) FROM smtp_credentials
WHERE account_id = @account_id AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > sqlc.arg(now)::timestamptz);

-- name: DeleteOldSMTPCredentials :execrows
-- Revoked and expired secrets stay listed for 30 days.
DELETE FROM smtp_credentials
WHERE revoked_at < sqlc.arg(now)::timestamptz - interval '30 days'
   OR expires_at < sqlc.arg(now)::timestamptz - interval '30 days';
