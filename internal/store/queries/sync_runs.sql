-- name: StartSyncRun :one
INSERT INTO sync_runs (account_id) VALUES ($1) RETURNING id;

-- name: FinishSyncRun :execrows
UPDATE sync_runs
SET finished_at = now(), status = @status, messages_fetched = @messages_fetched,
    messages_new = @messages_new, error = NULLIF(@error::text, '')
WHERE id = @id;

-- name: AccountStats :many
SELECT a.id, a.name, a.enabled,
       (SELECT count(*) FROM folders f WHERE f.account_id = a.id) AS folders,
       (SELECT count(*) FROM message_locations l JOIN folders f ON f.id = l.folder_id
        WHERE f.account_id = a.id) AS locations
FROM accounts a
ORDER BY a.name;

-- name: LastSyncRuns :many
-- The most recent sync run per account.
SELECT DISTINCT ON (account_id) account_id, started_at, status, error
FROM sync_runs
ORDER BY account_id, started_at DESC;
