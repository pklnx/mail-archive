-- name: StartSyncRun :one
INSERT INTO sync_runs (account_id) VALUES ($1) RETURNING id;

-- name: FinishSyncRun :execrows
UPDATE sync_runs
SET finished_at = now(), status = @status, messages_fetched = @messages_fetched,
    messages_new = @messages_new, error = NULLIF(@error::text, '')
WHERE id = @id;

-- name: AccountStats :many
-- All accounts, or only those of one owner.
SELECT a.id, a.name, a.enabled, a.owner_id,
       (SELECT count(*) FROM folders f WHERE f.account_id = a.id) AS folders,
       (SELECT count(*) FROM message_locations l JOIN folders f ON f.id = l.folder_id
        WHERE f.account_id = a.id) AS locations
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
       messages_fetched, messages_new, error
FROM sync_runs
ORDER BY account_id, started_at DESC;
