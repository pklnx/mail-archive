-- Listing and search for the web API. Results are ordered by date (newest
-- first) and paginated with a keyset cursor (sort_at, sha256).

-- name: SearchMessages :many
SELECT m.sha256, m.size, m.subject, m.from_addr, m.sent_at, m.sort_at, m.has_attachment,
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
  -- Filters on single fields. Recipients and attachment names are only
  -- found here, not by the full-text query.
  AND (sqlc.narg(from_pattern)::text IS NULL OR m.from_addr ILIKE sqlc.narg(from_pattern)::text)
  AND (sqlc.narg(to_pattern)::text IS NULL
       OR (coalesce(m.to_addr, '') || E'\n' || coalesce(m.cc_addr, '')) ILIKE sqlc.narg(to_pattern)::text)
  AND (sqlc.narg(attachment_pattern)::text IS NULL OR m.attachment_names ILIKE sqlc.narg(attachment_pattern)::text)
  AND (NOT sqlc.arg(has_attachment)::boolean OR m.has_attachment)
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
-- The user's own locations of a message, current ones first. A location is
-- superseded when the folder's UIDVALIDITY changed since it was stored: the
-- server renumbered the folder, and the rescan added a new location for
-- every message still there. Superseded locations stay as history.
SELECT a.name AS account, f.name AS folder, l.uid, l.flags, l.internal_date,
       (l.uidvalidity <> f.uidvalidity)::boolean AS superseded
FROM message_locations l
JOIN folders f ON f.id = l.folder_id
JOIN accounts a ON a.id = f.account_id
WHERE l.message_sha256 = @sha256 AND a.owner_id = @owner::bigint
ORDER BY superseded, a.name, f.name;

-- name: ListFolderCounts :many
-- The number of distinct messages per folder, like the folder's message
-- list: a message with an old and a new location (after a UIDVALIDITY
-- change) counts once. The subquery per folder uses
-- message_locations_folder_sha_idx; a count(DISTINCT) over the grouped join
-- is about 20 times slower on large archives.
SELECT a.name AS account, a.enabled, (a.removed_at IS NOT NULL)::boolean AS removed, a.kind,
       f.name AS folder,
       (SELECT count(*) FROM (
            SELECT DISTINCT l.message_sha256 FROM message_locations l WHERE l.folder_id = f.id) d
       )::bigint AS messages,
       f.last_synced_at
FROM accounts a
LEFT JOIN folders f ON f.account_id = a.id
WHERE a.owner_id = @owner::bigint
ORDER BY a.name, f.name;
