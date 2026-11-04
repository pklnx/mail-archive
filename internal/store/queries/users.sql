-- name: CreateUser :one
INSERT INTO users (name, password_hash, is_admin, must_change_password, group_id, is_auditor)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetUserByName :one
SELECT * FROM users WHERE name = $1;

-- name: GetUserByNameInGroup :one
-- One query whether the name is unknown or a user of another group, so
-- the two cannot be told apart (#476).
SELECT * FROM users WHERE name = @name AND group_id = @group_id;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY name;

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: LockActiveAdmins :many
-- Locks the rows of all unlocked admins, so that two changes cannot remove
-- the last two admins at the same time.
SELECT id FROM users WHERE is_admin AND locked_at IS NULL FOR UPDATE;

-- name: LockUsableAdmins :many
-- Locks admins who are currently able to log in (unlocked and TOTP-enabled).
SELECT id FROM users WHERE is_admin AND locked_at IS NULL AND two_factor_enabled FOR UPDATE;

-- name: SetUserPassword :execrows
UPDATE users SET password_hash = $2, must_change_password = $3, password_changed_at = now() WHERE id = $1;

-- name: SetUserAdmin :execrows
UPDATE users SET is_admin = $2 WHERE id = $1;

-- name: DeleteOtherSessions :exec
-- All sessions of a user except one.
DELETE FROM sessions WHERE user_id = $1 AND id <> $2;

-- name: RehashUserPassword :execrows
-- Replaces the hash with one of the same password (new parameters); unlike
-- SetUserPassword it keeps the sessions.
UPDATE users SET password_hash = $2 WHERE id = $1;

-- name: SetUserLocked :execrows
UPDATE users SET locked_at = CASE WHEN @locked::boolean THEN coalesce(locked_at, now()) END WHERE id = $1;

-- name: RecordLogin :exec
UPDATE users SET last_login_at = now() WHERE id = $1;

-- name: LockUserSettings :one
-- The current settings, locked, so concurrent changes record the right old value.
SELECT language, locale FROM users WHERE id = $1 FOR UPDATE;

-- name: SetUserSettings :exec
UPDATE users SET language = @language, locale = @locale WHERE id = @id;

-- name: DeleteUser :execrows
DELETE FROM users WHERE id = $1;

-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, expires_at, user_agent)
VALUES ($1, $2, $3, $4);

-- name: GetSession :one
-- A session is valid while it has not expired, was used after the idle
-- cutoff and its user is not locked. An auditor's session also needs an
-- active grant (#393): the one it is bound to, or any while it has none
-- yet. For members the NOT short-circuits the lookup. grant_owner_id is
-- the owner of the session's grant, whose Log gets the auditor's entries.
SELECT s.id, s.last_seen_at, u.id AS user_id, u.name, u.is_admin, u.must_change_password, u.two_factor_enabled,
       u.language, u.locale, u.group_id, u.is_auditor, s.grant_id, sg.owner_id AS grant_owner_id
FROM sessions s
JOIN users u ON u.id = s.user_id
LEFT JOIN auditor_grants sg ON sg.id = s.grant_id
WHERE s.id = $1
  AND s.expires_at > now()
  AND s.last_seen_at > @idle_cutoff
  AND u.locked_at IS NULL
  AND (NOT u.is_auditor OR EXISTS (
      SELECT 1 FROM active_auditor_grants g
      WHERE g.auditor_id = u.id AND (g.id = s.grant_id OR s.grant_id IS NULL)));

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = now() WHERE id = $1;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = $1;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= now() OR last_seen_at <= @idle_cutoff;
