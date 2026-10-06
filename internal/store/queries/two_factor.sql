-- name: GetTwoFactorUser :one
SELECT id, name, is_admin, two_factor_enabled, two_factor_secret,
       two_factor_pending_secret, two_factor_last_counter, two_factor_version,
       must_change_password
FROM users WHERE id = $1;

-- name: LockTwoFactorUser :one
SELECT id, name, is_admin, two_factor_enabled, two_factor_secret,
       two_factor_pending_secret, two_factor_last_counter, two_factor_version,
       must_change_password
FROM users WHERE id = $1 FOR UPDATE;

-- name: BeginTwoFactorSetup :exec
UPDATE users
SET two_factor_pending_secret = $2, two_factor_version = two_factor_version + 1
WHERE id = $1;

-- name: EnableTwoFactor :exec
UPDATE users
SET two_factor_secret = $2, two_factor_pending_secret = NULL,
    two_factor_enabled = TRUE, two_factor_last_counter = $3,
    two_factor_version = two_factor_version + 1
WHERE id = $1;

-- name: DisableTwoFactor :exec
UPDATE users
SET two_factor_secret = NULL, two_factor_pending_secret = NULL,
    two_factor_enabled = FALSE, two_factor_last_counter = NULL,
    two_factor_version = two_factor_version + 1
WHERE id = $1;

-- name: AcceptTwoFactorCounter :execrows
UPDATE users
SET two_factor_last_counter = $2
WHERE id = $1 AND two_factor_enabled = TRUE
  AND (two_factor_last_counter IS NULL OR two_factor_last_counter < $2);

-- name: BumpTwoFactorVersion :exec
UPDATE users SET two_factor_version = two_factor_version + 1 WHERE id = $1;

-- name: InsertRecoveryCode :exec
INSERT INTO two_factor_recovery_codes (user_id, code_hash) VALUES ($1, $2);

-- name: DeleteRecoveryCodes :exec
DELETE FROM two_factor_recovery_codes WHERE user_id = $1;

-- name: ConsumeRecoveryCode :execrows
UPDATE two_factor_recovery_codes
SET used_at = now()
WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL;

-- name: CreateTwoFactorChallenge :exec
INSERT INTO two_factor_challenges
(id, user_id, two_factor_version, expires_at) VALUES ($1, $2, $3, $4);

-- name: GetTwoFactorChallenge :one
SELECT id, user_id, two_factor_version, created_at, expires_at
FROM two_factor_challenges WHERE id = $1 AND expires_at > now();

-- name: DeleteTwoFactorChallenge :exec
DELETE FROM two_factor_challenges WHERE id = $1;

-- name: DeleteUserTwoFactorChallenges :exec
DELETE FROM two_factor_challenges WHERE user_id = $1;
