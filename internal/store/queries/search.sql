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
WHERE
  -- filters:begin (keep all copies equal; TestSearchFilterCopies checks)
  (sqlc.narg(query)::text IS NULL
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
  AND (sqlc.narg(thread)::text IS NULL OR COALESCE(m.thread_id, m.sha256) = sqlc.narg(thread)::text)
  AND (sqlc.narg(after)::timestamptz IS NULL OR m.sort_at >= sqlc.narg(after)::timestamptz)
  AND (sqlc.narg(before)::timestamptz IS NULL OR m.sort_at < sqlc.narg(before)::timestamptz)
  -- Only in the archive: the user's IMAP locations (of the account, if
  -- given) are all gone from the server. Import accounts say nothing about
  -- servers, and removed accounts are never reconciled, so they count as
  -- present. Starts from the few gone locations (message_locations_gone_idx).
  AND (NOT sqlc.arg(gone)::boolean OR m.sha256 IN (
       SELECT g.message_sha256 FROM message_locations g
       JOIN folders gf ON gf.id = g.folder_id
       JOIN accounts ga ON ga.id = gf.account_id
       WHERE g.gone_at IS NOT NULL
         AND ga.owner_id = sqlc.arg(owner)::bigint AND ga.kind = 'imap'
         AND (sqlc.narg(account)::text IS NULL OR ga.name = sqlc.narg(account)::text)
         AND NOT EXISTS (
             SELECT 1 FROM message_locations p
             JOIN folders pf ON pf.id = p.folder_id
             JOIN accounts pa ON pa.id = pf.account_id
             WHERE p.message_sha256 = g.message_sha256 AND p.gone_at IS NULL
               AND pa.owner_id = sqlc.arg(owner)::bigint AND pa.kind = 'imap'
               AND (sqlc.narg(account)::text IS NULL OR pa.name = sqlc.narg(account)::text))))
  -- filters:end
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL
       OR (m.sort_at, m.sha256) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_sha)::text))
ORDER BY m.sort_at DESC, m.sha256 DESC
LIMIT sqlc.arg(row_limit);

-- name: SearchThreads :many
-- One row per thread (the thread key COALESCE(thread_id, sha256)): the
-- newest message of the thread that matches the filters. Walking the date
-- order and skipping messages with a newer match in their thread stops
-- after row_limit rows, like SearchMessages.
SELECT m.sha256, m.size, m.subject, m.from_addr, m.sent_at, m.sort_at, m.has_attachment,
       CASE
           WHEN sqlc.narg(query)::text IS NULL THEN left(coalesce(m.body_text, ''), 240)
           ELSE ts_headline('german', coalesce(m.body_text, ''),
                    websearch_to_tsquery('simple', sqlc.narg(query)::text) ||
                    websearch_to_tsquery('german', sqlc.narg(query)::text) ||
                    websearch_to_tsquery('english', sqlc.narg(query)::text),
                    'MaxFragments=1, MaxWords=30, MinWords=12, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))
       END::text AS snippet,
       COALESCE(m.thread_id, m.sha256)::text AS thread_key
FROM messages m
WHERE
  -- filters:begin (keep all copies equal; TestSearchFilterCopies checks)
  (sqlc.narg(query)::text IS NULL
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
  AND (sqlc.narg(thread)::text IS NULL OR COALESCE(m.thread_id, m.sha256) = sqlc.narg(thread)::text)
  AND (sqlc.narg(after)::timestamptz IS NULL OR m.sort_at >= sqlc.narg(after)::timestamptz)
  AND (sqlc.narg(before)::timestamptz IS NULL OR m.sort_at < sqlc.narg(before)::timestamptz)
  -- Only in the archive: the user's IMAP locations (of the account, if
  -- given) are all gone from the server. Import accounts say nothing about
  -- servers, and removed accounts are never reconciled, so they count as
  -- present. Starts from the few gone locations (message_locations_gone_idx).
  AND (NOT sqlc.arg(gone)::boolean OR m.sha256 IN (
       SELECT g.message_sha256 FROM message_locations g
       JOIN folders gf ON gf.id = g.folder_id
       JOIN accounts ga ON ga.id = gf.account_id
       WHERE g.gone_at IS NOT NULL
         AND ga.owner_id = sqlc.arg(owner)::bigint AND ga.kind = 'imap'
         AND (sqlc.narg(account)::text IS NULL OR ga.name = sqlc.narg(account)::text)
         AND NOT EXISTS (
             SELECT 1 FROM message_locations p
             JOIN folders pf ON pf.id = p.folder_id
             JOIN accounts pa ON pa.id = pf.account_id
             WHERE p.message_sha256 = g.message_sha256 AND p.gone_at IS NULL
               AND pa.owner_id = sqlc.arg(owner)::bigint AND pa.kind = 'imap'
               AND (sqlc.narg(account)::text IS NULL OR pa.name = sqlc.narg(account)::text))))
  -- filters:end
  AND NOT EXISTS (
      SELECT 1 FROM messages n
      WHERE COALESCE(n.thread_id, n.sha256) = COALESCE(m.thread_id, m.sha256)
        AND (n.sort_at, n.sha256) > (m.sort_at, m.sha256)
        AND
        -- filters:begin (keep all copies equal; TestSearchFilterCopies checks)
        (sqlc.narg(query)::text IS NULL
         OR n.search @@ (websearch_to_tsquery('simple', sqlc.narg(query)::text) ||
                          websearch_to_tsquery('german', sqlc.narg(query)::text) ||
                          websearch_to_tsquery('english', sqlc.narg(query)::text))
         OR n.subject ILIKE sqlc.narg(pattern)::text
         OR n.from_addr ILIKE sqlc.narg(pattern)::text)
        -- Only messages found in one of the user's accounts.
        AND EXISTS (
             SELECT 1 FROM message_locations l
             JOIN folders f ON f.id = l.folder_id
             JOIN accounts a ON a.id = f.account_id
             WHERE l.message_sha256 = n.sha256
               AND a.owner_id = sqlc.arg(owner)::bigint
               AND (sqlc.narg(account)::text IS NULL OR a.name = sqlc.narg(account)::text)
               AND (sqlc.narg(folder)::text IS NULL OR f.name = sqlc.narg(folder)::text))
        -- Filters on single fields. Recipients and attachment names are only
        -- found here, not by the full-text query.
        AND (sqlc.narg(from_pattern)::text IS NULL OR n.from_addr ILIKE sqlc.narg(from_pattern)::text)
        AND (sqlc.narg(to_pattern)::text IS NULL
             OR (coalesce(n.to_addr, '') || E'\n' || coalesce(n.cc_addr, '')) ILIKE sqlc.narg(to_pattern)::text)
        AND (sqlc.narg(attachment_pattern)::text IS NULL OR n.attachment_names ILIKE sqlc.narg(attachment_pattern)::text)
        AND (NOT sqlc.arg(has_attachment)::boolean OR n.has_attachment)
        AND (sqlc.narg(thread)::text IS NULL OR COALESCE(n.thread_id, n.sha256) = sqlc.narg(thread)::text)
        AND (sqlc.narg(after)::timestamptz IS NULL OR n.sort_at >= sqlc.narg(after)::timestamptz)
        AND (sqlc.narg(before)::timestamptz IS NULL OR n.sort_at < sqlc.narg(before)::timestamptz)
        -- Only in the archive: the user's IMAP locations (of the account, if
        -- given) are all gone from the server. Import accounts say nothing about
        -- servers, and removed accounts are never reconciled, so they count as
        -- present. Starts from the few gone locations (message_locations_gone_idx).
        AND (NOT sqlc.arg(gone)::boolean OR n.sha256 IN (
             SELECT g.message_sha256 FROM message_locations g
             JOIN folders gf ON gf.id = g.folder_id
             JOIN accounts ga ON ga.id = gf.account_id
             WHERE g.gone_at IS NOT NULL
               AND ga.owner_id = sqlc.arg(owner)::bigint AND ga.kind = 'imap'
               AND (sqlc.narg(account)::text IS NULL OR ga.name = sqlc.narg(account)::text)
               AND NOT EXISTS (
                   SELECT 1 FROM message_locations p
                   JOIN folders pf ON pf.id = p.folder_id
                   JOIN accounts pa ON pa.id = pf.account_id
                   WHERE p.message_sha256 = g.message_sha256 AND p.gone_at IS NULL
                     AND pa.owner_id = sqlc.arg(owner)::bigint AND pa.kind = 'imap'
                     AND (sqlc.narg(account)::text IS NULL OR pa.name = sqlc.narg(account)::text))))
        -- filters:end
  )
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL
       OR (m.sort_at, m.sha256) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_sha)::text))
ORDER BY m.sort_at DESC, m.sha256 DESC
LIMIT sqlc.arg(row_limit);

-- name: CountThreadMatches :many
-- The number of messages per thread that match the filters, for the rows
-- of one SearchThreads page.
SELECT COALESCE(t.thread_id, t.sha256)::text AS thread_key, count(*)::bigint AS matches
FROM messages t
WHERE COALESCE(t.thread_id, t.sha256) = ANY(sqlc.arg(thread_keys)::text[])
  AND
  -- filters:begin (keep all copies equal; TestSearchFilterCopies checks)
  (sqlc.narg(query)::text IS NULL
   OR t.search @@ (websearch_to_tsquery('simple', sqlc.narg(query)::text) ||
                    websearch_to_tsquery('german', sqlc.narg(query)::text) ||
                    websearch_to_tsquery('english', sqlc.narg(query)::text))
   OR t.subject ILIKE sqlc.narg(pattern)::text
   OR t.from_addr ILIKE sqlc.narg(pattern)::text)
  -- Only messages found in one of the user's accounts.
  AND EXISTS (
       SELECT 1 FROM message_locations l
       JOIN folders f ON f.id = l.folder_id
       JOIN accounts a ON a.id = f.account_id
       WHERE l.message_sha256 = t.sha256
         AND a.owner_id = sqlc.arg(owner)::bigint
         AND (sqlc.narg(account)::text IS NULL OR a.name = sqlc.narg(account)::text)
         AND (sqlc.narg(folder)::text IS NULL OR f.name = sqlc.narg(folder)::text))
  -- Filters on single fields. Recipients and attachment names are only
  -- found here, not by the full-text query.
  AND (sqlc.narg(from_pattern)::text IS NULL OR t.from_addr ILIKE sqlc.narg(from_pattern)::text)
  AND (sqlc.narg(to_pattern)::text IS NULL
       OR (coalesce(t.to_addr, '') || E'\n' || coalesce(t.cc_addr, '')) ILIKE sqlc.narg(to_pattern)::text)
  AND (sqlc.narg(attachment_pattern)::text IS NULL OR t.attachment_names ILIKE sqlc.narg(attachment_pattern)::text)
  AND (NOT sqlc.arg(has_attachment)::boolean OR t.has_attachment)
  AND (sqlc.narg(thread)::text IS NULL OR COALESCE(t.thread_id, t.sha256) = sqlc.narg(thread)::text)
  AND (sqlc.narg(after)::timestamptz IS NULL OR t.sort_at >= sqlc.narg(after)::timestamptz)
  AND (sqlc.narg(before)::timestamptz IS NULL OR t.sort_at < sqlc.narg(before)::timestamptz)
  -- Only in the archive: the user's IMAP locations (of the account, if
  -- given) are all gone from the server. Import accounts say nothing about
  -- servers, and removed accounts are never reconciled, so they count as
  -- present. Starts from the few gone locations (message_locations_gone_idx).
  AND (NOT sqlc.arg(gone)::boolean OR t.sha256 IN (
       SELECT g.message_sha256 FROM message_locations g
       JOIN folders gf ON gf.id = g.folder_id
       JOIN accounts ga ON ga.id = gf.account_id
       WHERE g.gone_at IS NOT NULL
         AND ga.owner_id = sqlc.arg(owner)::bigint AND ga.kind = 'imap'
         AND (sqlc.narg(account)::text IS NULL OR ga.name = sqlc.narg(account)::text)
         AND NOT EXISTS (
             SELECT 1 FROM message_locations p
             JOIN folders pf ON pf.id = p.folder_id
             JOIN accounts pa ON pa.id = pf.account_id
             WHERE p.message_sha256 = g.message_sha256 AND p.gone_at IS NULL
               AND pa.owner_id = sqlc.arg(owner)::bigint AND pa.kind = 'imap'
               AND (sqlc.narg(account)::text IS NULL OR pa.name = sqlc.narg(account)::text))))
  -- filters:end
GROUP BY 1;

-- name: GetMessageSummary :one
-- Only if the message was found in one of the user's accounts.
SELECT m.sha256, m.size, m.message_id, m.subject, m.from_addr, m.sent_at, m.sort_at, m.stored_path,
       m.in_reply_to, COALESCE(m.thread_id, m.sha256)::text AS thread_key
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
       (l.uidvalidity <> f.uidvalidity)::boolean AS superseded, l.gone_at,
       -- A present location was seen at the folder's last reconcile.
       (CASE WHEN l.gone_at IS NULL AND l.uidvalidity = f.uidvalidity
             THEN GREATEST(l.last_seen_at, f.last_reconciled_at)
             ELSE l.last_seen_at END)::timestamptz AS last_seen_at
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
       f.last_synced_at, f.last_reconciled_at
FROM accounts a
LEFT JOIN folders f ON f.account_id = a.id
WHERE a.owner_id = @owner::bigint
ORDER BY a.name, f.name;
