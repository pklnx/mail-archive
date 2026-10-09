-- name: StartSyncRun :one
INSERT INTO sync_runs (account_id) VALUES ($1) RETURNING id;

-- name: FinishSyncRun :execrows
UPDATE sync_runs
SET finished_at = now(), status = @status, messages_fetched = @messages_fetched,
    messages_new = @messages_new, error = NULLIF(@error::text, ''),
    reconciled_folders = @reconciled_folders, locations_gone = @locations_gone,
    locations_back = @locations_back, flags_changed = @flags_changed
WHERE id = @id;

-- name: AccountStats :many
-- All accounts, or only those of one owner.
SELECT a.id, a.name, a.enabled, a.owner_id, a.kind,
       (SELECT count(*) FROM folders f WHERE f.account_id = a.id) AS folders,
       (SELECT count(DISTINCT l.message_sha256) FROM message_locations l JOIN folders f ON f.id = l.folder_id
        WHERE f.account_id = a.id) AS messages,
       -- Messages whose locations in this account are all gone from the
       -- server. Reads the gone locations from message_locations_gone_idx.
       (SELECT count(DISTINCT l.message_sha256) FROM message_locations l JOIN folders f ON f.id = l.folder_id
        WHERE f.account_id = a.id AND l.gone_at IS NOT NULL
          AND NOT EXISTS (
              SELECT 1 FROM message_locations p JOIN folders pf ON pf.id = p.folder_id
              WHERE p.message_sha256 = l.message_sha256 AND pf.account_id = a.id AND p.gone_at IS NULL)
       ) AS gone,
       -- The latest reconcile of any folder (written like this, not as max(),
       -- so that sqlc types it as a nullable time).
       (SELECT f.last_reconciled_at FROM folders f WHERE f.account_id = a.id AND f.last_reconciled_at IS NOT NULL
        ORDER BY f.last_reconciled_at DESC LIMIT 1) AS last_reconciled_at
FROM accounts a
WHERE sqlc.narg(owner)::bigint IS NULL OR a.owner_id = sqlc.narg(owner)::bigint
ORDER BY a.name, a.owner_id;


-- name: UpdateSyncRunProgress :exec
UPDATE sync_runs SET messages_fetched = @messages_fetched, messages_new = @messages_new
WHERE id = @id AND finished_at IS NULL;

-- name: FailStaleSyncRuns :execrows
-- Runs left "running" by a process that died. Only called while holding the
-- account's sync lock, so no other run of the account can be active.
UPDATE sync_runs SET finished_at = now(), status = 'failed', error = 'interrupted'
WHERE account_id = $1 AND finished_at IS NULL;

-- name: LastSyncRuns :many
-- The most recent sync run per account, with counters.
SELECT DISTINCT ON (account_id) account_id, started_at, finished_at, status,
       messages_fetched, messages_new, error,
       reconciled_folders, locations_gone, locations_back, flags_changed
FROM sync_runs
ORDER BY account_id, started_at DESC;
