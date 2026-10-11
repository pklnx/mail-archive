-- name: InsertAuditEntry :exec
-- at comes from the database clock (the column default).
INSERT INTO audit_log (actor, user_id, user_name, api_key_id, api_key_name, action,
                       target_type, target_id, target_name, client_ip, user_agent_id, details)
VALUES (@actor, sqlc.narg(user_id), sqlc.narg(user_name), sqlc.narg(api_key_id), sqlc.narg(api_key_name), @action,
        sqlc.narg(target_type), sqlc.narg(target_id), sqlc.narg(target_name), sqlc.narg(client_ip), sqlc.narg(user_agent_id), @details);

-- name: AuditUserAgentID :one
-- The id of a user agent, inserted if new. ON CONFLICT DO UPDATE would be
-- refused by the append-only trigger. Under READ COMMITTED a concurrent
-- insert of the same new string leaves both branches empty for the loser:
-- no row means run it once more.
WITH ins AS (
    INSERT INTO audit_user_agents (user_agent) VALUES (@user_agent)
    ON CONFLICT (user_agent) DO NOTHING
    RETURNING id)
SELECT id FROM ins
UNION ALL
SELECT id FROM audit_user_agents WHERE user_agent = @user_agent
LIMIT 1;

-- name: EnsureAuditPartitions :one
-- Creates the partitions of this month and months_ahead months; returns how
-- many it created.
SELECT audit_log_ensure_partitions(@months_ahead::integer)::integer;

-- name: ExportAuditEntries :many
-- One batch of the entries in [since, until) after (after_at, after_id), oldest
-- first, optionally of one user.
SELECT l.id, l.at, l.actor, l.user_id, l.user_name, l.api_key_id, l.api_key_name, l.action,
       l.target_type, l.target_id, l.target_name, l.client_ip, ua.user_agent, l.details
FROM audit_log l
LEFT JOIN audit_user_agents ua ON ua.id = l.user_agent_id
WHERE l.at >= @since AND l.at < @until
  AND (sqlc.narg(user_id)::bigint IS NULL OR l.user_id = sqlc.narg(user_id))
  AND l.action LIKE ANY (@patterns::text[])
  AND (l.at, l.id) > (@after_at::timestamptz, @after_id::bigint)
ORDER BY l.at, l.id
LIMIT @lim;

-- name: ListAuditEntries :many
-- A page of every user's entries in [since, until), newest first, for
-- admins; optionally one user's or one actor's. subject is the message's
-- subject only if the viewer has a location of it.
SELECT l.id, l.at, l.actor, l.user_id, l.user_name, l.api_key_name, l.action,
       l.target_type, l.target_id, l.target_name, l.client_ip, ua.user_agent, l.details,
       s.subject
FROM audit_log l
LEFT JOIN audit_user_agents ua ON ua.id = l.user_agent_id
LEFT JOIN LATERAL (
    SELECT m.subject FROM messages m
    WHERE l.target_type = 'message' AND m.sha256 = l.target_id::bpchar
      AND EXISTS (SELECT 1 FROM message_locations ml
                  JOIN folders f ON f.id = ml.folder_id
                  JOIN accounts a ON a.id = f.account_id
                  WHERE ml.message_sha256 = m.sha256 AND a.owner_id = @viewer::bigint)
) s ON true
WHERE l.at >= @since AND l.at < @until
  AND (sqlc.narg(user_id)::bigint IS NULL OR l.user_id = sqlc.narg(user_id))
  AND (sqlc.narg(actor)::text IS NULL OR l.actor = sqlc.narg(actor))
  AND l.action LIKE ANY (@patterns::text[])
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (l.at, l.id) < (sqlc.narg(cursor_at), @cursor_id::bigint))
ORDER BY l.at DESC, l.id DESC
LIMIT @lim;

-- name: ListAuditEntriesForUser :many
-- A page of one user's own entries, newest first. A separate query rather
-- than "user_id IS NULL OR", so that a cached generic plan still uses
-- audit_log_user_idx.
SELECT l.id, l.at, l.actor, l.user_id, l.user_name, l.api_key_name, l.action,
       l.target_type, l.target_id, l.target_name, l.client_ip, ua.user_agent, l.details,
       s.subject
FROM audit_log l
LEFT JOIN audit_user_agents ua ON ua.id = l.user_agent_id
LEFT JOIN LATERAL (
    SELECT m.subject FROM messages m
    WHERE l.target_type = 'message' AND m.sha256 = l.target_id::bpchar
      AND EXISTS (SELECT 1 FROM message_locations ml
                  JOIN folders f ON f.id = ml.folder_id
                  JOIN accounts a ON a.id = f.account_id
                  WHERE ml.message_sha256 = m.sha256 AND a.owner_id = @user_id::bigint)
) s ON true
WHERE l.user_id = @user_id::bigint
  AND l.at >= @since AND l.at < @until
  AND l.action LIKE ANY (@patterns::text[])
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (l.at, l.id) < (sqlc.narg(cursor_at), @cursor_id::bigint))
ORDER BY l.at DESC, l.id DESC
LIMIT @lim;
