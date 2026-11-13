-- name: InstanceStateExists :one
-- Before migration 00039 the table does not exist: nothing is read or
-- written then (#396).
SELECT (to_regclass('instance_state') IS NOT NULL)::boolean AS exists;

-- name: GetMigrationFailure :one
SELECT migration_failed_version, migration_failed_sha256, migration_failed_at
FROM instance_state;

-- name: SetMigrationFailure :execrows
UPDATE instance_state
SET migration_failed_version = @version::bigint, migration_failed_sha256 = @sha256::bytea,
    migration_failed_at = now(), updated_at = now();

-- name: ClearMigrationFailure :execrows
UPDATE instance_state
SET migration_failed_version = NULL, migration_failed_sha256 = NULL,
    migration_failed_at = NULL, updated_at = now()
WHERE migration_failed_version IS NOT NULL;

-- name: LastBackupSince :one
-- The newest backup entry younger than @since, newest first through the
-- audit log's primary key (at, id): it reads only the entries since then.
SELECT at FROM audit_log
WHERE action = 'data.backup' AND at >= @since::timestamptz
ORDER BY at DESC
LIMIT 1;

-- name: GetSecretKeyCheck :one
SELECT secret_key_check FROM instance_state;

-- name: RecordSecretKeyCheck :execrows
-- The first start of a key records its check value, unless one is there.
UPDATE instance_state
SET secret_key_check = @key_check::bytea, updated_at = now()
WHERE secret_key_check IS NULL;

-- name: LockSecretKeyCheck :one
SELECT secret_key_check FROM instance_state FOR UPDATE;

-- name: SetSecretKeyCheck :execrows
UPDATE instance_state
SET secret_key_check = @key_check::bytea, updated_at = now();

-- name: SampleTwoFactorSecrets :many
-- Some of the sealed TOTP secrets, to tell whether the secret key opens
-- them (#396).
SELECT id, two_factor_secret FROM users
WHERE two_factor_secret IS NOT NULL
ORDER BY id
LIMIT @max_rows::int;

-- name: GetSetupState :one
-- The setup of the first admin (#396, part 3) and whether a user exists.
SELECT setup_token_hash, setup_token_since, setup_completed_at,
       EXISTS (SELECT 1 FROM users)::boolean AS has_users
FROM instance_state;

-- name: LockSetupState :one
-- serializes the creation of the first user: POST /api/setup and every
-- other way to create a user lock this row first.
SELECT setup_token_hash, setup_token_since, setup_completed_at
FROM instance_state FOR UPDATE;

-- name: SetSetupToken :execrows
-- A start with a token while setup is not complete: a token of another
-- hash replaces the stored one and starts a new window; the same token
-- keeps its window.
UPDATE instance_state
SET setup_token_hash = @token_hash::bytea, setup_token_since = @since::timestamptz, updated_at = now()
WHERE setup_completed_at IS NULL
  AND setup_token_hash IS DISTINCT FROM @token_hash::bytea;

-- name: ClearSetupToken :execrows
-- A start without a token revokes a stored one.
UPDATE instance_state
SET setup_token_hash = NULL, setup_token_since = NULL, updated_at = now()
WHERE setup_token_hash IS NOT NULL;

-- name: CompleteSetup :execrows
-- The first user completes the setup for good and consumes the token.
UPDATE instance_state
SET setup_completed_at = now(), setup_user_id = @user_id::bigint,
    setup_token_hash = NULL, setup_token_since = NULL, updated_at = now()
WHERE setup_completed_at IS NULL;

-- name: FreshInstance :one
-- A new instance (#396, revision 1): setup not complete, no user, no
-- message and no state of encryption at rest recorded.
SELECT (setup_completed_at IS NULL
        AND NOT EXISTS (SELECT 1 FROM users)
        AND NOT EXISTS (SELECT 1 FROM messages)
        AND NOT EXISTS (SELECT 1 FROM blob_encryption))::boolean AS fresh
FROM instance_state;
