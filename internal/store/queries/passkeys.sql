-- name: SetWebAuthnHandle :exec
UPDATE users SET webauthn_handle = $2 WHERE id = $1 AND webauthn_handle IS NULL;

-- name: ListPasskeys :many
SELECT * FROM passkeys WHERE user_id = $1 ORDER BY created_at, id;

-- name: CountPasskeys :one
SELECT count(*) FROM passkeys WHERE user_id = $1;

-- name: CountPasskeysByUser :many
SELECT user_id, count(*) AS n FROM passkeys GROUP BY user_id;

-- name: InsertPasskey :exec
INSERT INTO passkeys (user_id, credential_id, name, credential) VALUES ($1, $2, $3, $4);

-- name: PasskeyOwner :one
SELECT user_id FROM passkeys WHERE credential_id = $1;

-- name: LockPasskey :one
-- The row stays locked until the transaction ends, so concurrent logins
-- with the same passkey see each other's sign count.
SELECT * FROM passkeys WHERE credential_id = $1 FOR UPDATE;

-- name: UpdatePasskeyUse :exec
UPDATE passkeys SET credential = $2, last_used_at = now() WHERE id = $1;

-- name: DeletePasskey :one
DELETE FROM passkeys WHERE id = $1 AND user_id = $2 RETURNING name, credential_id;

-- name: DeleteUserPasskeys :execrows
DELETE FROM passkeys WHERE user_id = $1;

-- name: CreateWebAuthnCeremony :exec
INSERT INTO webauthn_ceremonies (id, kind, user_id, name, data, expires_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: TakeWebAuthnCeremony :one
-- Uses up a ceremony that has not expired.
DELETE FROM webauthn_ceremonies WHERE id = $1 AND kind = $2 AND expires_at > now()
RETURNING *;

-- name: CountOpenLoginCeremonies :one
SELECT count(*) FROM webauthn_ceremonies WHERE kind = 'login' AND expires_at > now();

-- name: DeleteExpiredWebAuthnCeremonies :exec
DELETE FROM webauthn_ceremonies WHERE expires_at <= now();

-- name: DeleteUserWebAuthnCeremonies :exec
DELETE FROM webauthn_ceremonies WHERE user_id = $1;
