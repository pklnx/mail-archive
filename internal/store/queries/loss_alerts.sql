-- Alerts about messages deleted on the server in bulk (see 00016).

-- name: LostMessages :many
-- Messages of the account that lost their last present location in the
-- run: a location of theirs went gone during the run, outside the folders
-- whose first reconcile ran in it (those only catch up with old
-- deletions), and none of their locations in the account is present. Gone
-- locations are read from message_locations_gone_idx. One row per folder
-- where such a location went gone, and a last row marked total with the
-- number of distinct messages.
WITH lost AS (
    SELECT l.message_sha256, f.name AS folder
    FROM message_locations l
    JOIN folders f ON f.id = l.folder_id
    WHERE f.account_id = @account_id AND l.gone_at IS NOT NULL
      AND l.gone_at >= (SELECT r.started_at FROM sync_runs r WHERE r.id = @run_id)
      AND NOT (l.folder_id = ANY (@baseline::bigint[]))
      AND NOT EXISTS (
          SELECT 1 FROM message_locations p JOIN folders pf ON pf.id = p.folder_id
          WHERE p.message_sha256 = l.message_sha256 AND pf.account_id = @account_id AND p.gone_at IS NULL)
)
SELECT coalesce(folder, '')::text AS folder, (GROUPING(folder) = 1)::boolean AS total,
       count(DISTINCT message_sha256)::bigint AS lost
FROM lost
GROUP BY ROLLUP (folder)
ORDER BY GROUPING(folder), lost DESC, folder;

-- name: CountPresentMessages :one
-- Distinct messages of the account that are still on the server.
SELECT count(DISTINCT l.message_sha256)::bigint FROM message_locations l
JOIN folders f ON f.id = l.folder_id
WHERE f.account_id = @account_id AND l.gone_at IS NULL;

-- name: InsertLossAlert :exec
INSERT INTO loss_alerts (account_id, sync_run_id, lost, present_before, folder_names, folder_lost, more_folders)
VALUES (@account_id, @sync_run_id, @lost, @present_before, @folder_names::text[], @folder_lost::int[], @more_folders);

-- name: ExpireLossAlerts :execrows
-- Alerts that were not sent within max_age are given up, so that a webhook
-- configured later never sends old losses and the pending index stays small.
UPDATE loss_alerts
SET given_up_at = now(), notify_error = coalesce(notify_error, 'not sent in time'),
    notify_lease = NULL, notify_lease_until = NULL
WHERE sent_at IS NULL AND given_up_at IS NULL
  AND created_at < now() - make_interval(secs => @max_age_seconds::float8)
  AND (notify_lease_until IS NULL OR notify_lease_until < now());

-- name: ClaimLossAlerts :many
-- Leases up to max_rows unsent alerts that are due and younger than
-- max_age, of accounts that were not removed. Rows another notifier holds
-- are skipped.
UPDATE loss_alerts la
SET notify_lease = @lease, notify_lease_until = now() + make_interval(secs => @lease_seconds::float8)
FROM accounts a LEFT JOIN users u ON u.id = a.owner_id
WHERE la.account_id = a.id AND la.id IN (
    SELECT c.id FROM loss_alerts c JOIN accounts ca ON ca.id = c.account_id
    WHERE c.sent_at IS NULL AND c.given_up_at IS NULL AND ca.removed_at IS NULL
      AND c.created_at > now() - make_interval(secs => @max_age_seconds::float8)
      AND (c.notify_next_at IS NULL OR c.notify_next_at <= now())
      AND (c.notify_lease_until IS NULL OR c.notify_lease_until < now())
    ORDER BY c.id
    LIMIT @max_rows
    FOR UPDATE OF c SKIP LOCKED)
RETURNING la.id, la.sync_run_id, la.account_id, a.name AS account_name, coalesce(u.name, '')::text AS owner_name,
    la.created_at, la.lost, la.present_before, la.folder_names, la.folder_lost, la.more_folders, la.notify_attempts;

-- name: FinishLossAlert :execrows
-- Sent, or given up with an error. Only the notifier holding the lease may
-- do this.
UPDATE loss_alerts
SET sent_at = CASE WHEN @given_up::boolean THEN NULL ELSE now() END,
    given_up_at = CASE WHEN @given_up::boolean THEN now() END,
    notify_error = NULLIF(@notify_error::text, ''),
    notify_lease = NULL, notify_lease_until = NULL
WHERE id = @id AND notify_lease = @lease;

-- name: DeferLossAlert :execrows
UPDATE loss_alerts
SET notify_attempts = notify_attempts + 1, notify_next_at = @next_at::timestamptz, notify_error = @notify_error::text,
    notify_lease = NULL, notify_lease_until = NULL
WHERE id = @id AND notify_lease = @lease;
