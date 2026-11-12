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
