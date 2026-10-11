-- name: GetAPIKeyLogin :one
-- A key works while it is not revoked or expired and its user is not
-- locked. A removed user's keys are gone through the cascade.
SELECT k.id, k.name, k.scopes, k.last_used_at,
       u.id AS user_id, u.name AS user_name, u.is_admin, u.must_change_password, u.two_factor_enabled
FROM api_keys k
JOIN users u ON u.id = k.user_id
WHERE k.key_hash = $1
  AND k.revoked_at IS NULL
  AND (k.expires_at IS NULL OR k.expires_at > now())
  AND u.locked_at IS NULL;

-- name: GetAPIKeyByHash :one
-- Only after GetAPIKeyLogin found nothing: names the key in the audit entry
-- of a refused attempt.
SELECT k.id, k.user_id, u.name AS user_name, k.name, k.revoked_at, k.expires_at, u.locked_at
FROM api_keys k JOIN users u ON u.id = k.user_id
WHERE k.key_hash = $1;

-- name: TouchAPIKey :exec
UPDATE api_keys SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: CountActiveAPIKeys :one
SELECT count(*) FROM api_keys
WHERE user_id = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now());

-- name: InsertAPIKey :one
INSERT INTO api_keys (user_id, key_hash, hint, name, scopes, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, created_at;

-- name: ListUserAPIKeys :many
SELECT id, hint, name, scopes, created_at, expires_at, last_used_at, revoked_at
FROM api_keys WHERE user_id = $1 ORDER BY created_at DESC, id DESC;

-- name: ListAllAPIKeys :many
SELECT k.id, k.hint, k.name, k.scopes, k.created_at, k.expires_at, k.last_used_at, k.revoked_at,
       u.id AS user_id, u.name AS user_name
FROM api_keys k JOIN users u ON u.id = k.user_id
ORDER BY u.name, k.created_at DESC, k.id DESC;

-- name: CountAPIKeysByUser :many
SELECT user_id, count(*) AS n FROM api_keys
WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())
GROUP BY user_id;

-- name: RevokeAPIKey :one
-- owner is NULL for an admin, who may revoke any key.
UPDATE api_keys k SET revoked_at = now(), revoked_by = sqlc.narg(revoked_by)
FROM users u
WHERE k.id = @id AND k.revoked_at IS NULL AND u.id = k.user_id
  AND (sqlc.narg(owner)::bigint IS NULL OR k.user_id = sqlc.narg(owner))
RETURNING k.id, k.user_id, u.name AS user_name, k.name;

-- name: RevokeUserAPIKeys :execrows
UPDATE api_keys SET revoked_at = now(), revoked_by = sqlc.narg(revoked_by)
WHERE user_id = @user_id AND revoked_at IS NULL;

-- name: DeleteOldAPIKeys :execrows
-- Revoked and expired keys stay listed for 30 days.
DELETE FROM api_keys
WHERE revoked_at < now() - interval '30 days'
   OR expires_at < now() - interval '30 days';
