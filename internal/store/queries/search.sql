-- Listing and search for the web API. Results are ordered by date (newest
-- first) and paginated with a keyset cursor (sort_at, sha256).

-- name: SearchMessages :many
SELECT m.sha256, m.size, m.subject, m.from_addr, m.sent_at, m.sort_at,
       CASE
           WHEN sqlc.narg(query)::text IS NULL THEN left(coalesce(m.body_text, ''), 240)
           ELSE ts_headline('german', coalesce(m.body_text, ''),
                    websearch_to_tsquery('simple', sqlc.narg(query)::text) ||
                    websearch_to_tsquery('german', sqlc.narg(query)::text) ||
                    websearch_to_tsquery('english', sqlc.narg(query)::text),
                    'MaxFragments=1, MaxWords=30, MinWords=12, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
       END::text AS snippet
FROM messages m
WHERE (sqlc.narg(query)::text IS NULL
       OR m.search @@ (websearch_to_tsquery('simple', sqlc.narg(query)::text) ||
                       websearch_to_tsquery('german', sqlc.narg(query)::text) ||
                       websearch_to_tsquery('english', sqlc.narg(query)::text))
       OR m.subject ILIKE sqlc.narg(pattern)::text
       OR m.from_addr ILIKE sqlc.narg(pattern)::text)
  -- Only messages found in one of the user's accounts.
  AND EXISTS (
           SELECT 1 FROM message_locations l
           JOIN folders f ON f.id = l.folder_id
           JOIN accounts a ON a.id = f.account_id
           WHERE l.message_sha256 = m.sha256
             AND a.owner_id = sqlc.arg(owner)::bigint
             AND (sqlc.narg(account)::text IS NULL OR a.name = sqlc.narg(account)::text)
             AND (sqlc.narg(folder)::text IS NULL OR f.name = sqlc.narg(folder)::text))
  AND (sqlc.narg(after)::timestamptz IS NULL OR m.sort_at >= sqlc.narg(after)::timestamptz)
  AND (sqlc.narg(before)::timestamptz IS NULL OR m.sort_at < sqlc.narg(before)::timestamptz)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL
       OR (m.sort_at, m.sha256) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_sha)::text))
ORDER BY m.sort_at DESC, m.sha256 DESC
LIMIT sqlc.arg(row_limit);

-- name: GetMessageSummary :one
-- Only if the message was found in one of the user's accounts.
SELECT m.sha256, m.size, m.message_id, m.subject, m.from_addr, m.sent_at, m.sort_at, m.stored_path
FROM messages m
WHERE m.sha256 = @sha256
  AND EXISTS (
      SELECT 1 FROM message_locations l
      JOIN folders f ON f.id = l.folder_id
      JOIN accounts a ON a.id = f.account_id
      WHERE l.message_sha256 = m.sha256 AND a.owner_id = @owner::bigint);

-- name: ListLocations :many
-- The user's own locations of a message.
SELECT a.name AS account, f.name AS folder, l.uid, l.flags, l.internal_date
FROM message_locations l
JOIN folders f ON f.id = l.folder_id
JOIN accounts a ON a.id = f.account_id
WHERE l.message_sha256 = @sha256 AND a.owner_id = @owner::bigint
ORDER BY a.name, f.name;

-- name: ListFolderCounts :many
SELECT a.name AS account, a.enabled, (a.removed_at IS NOT NULL)::boolean AS removed,
       f.name AS folder, count(l.id) AS messages, f.last_synced_at
FROM accounts a
LEFT JOIN folders f ON f.account_id = a.id
LEFT JOIN message_locations l ON l.folder_id = f.id
WHERE a.owner_id = @owner::bigint
GROUP BY a.name, a.enabled, a.removed_at, f.name, f.last_synced_at
ORDER BY a.name, f.name;
