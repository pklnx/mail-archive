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
