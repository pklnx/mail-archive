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
       END::text AS snippet,
       -- The row is only in the archive: exactly the gone filter's predicate
       -- below, with its account scope, for this row. Each subquery looks
       -- up the row's locations through an index of message_locations (sha,
       -- or folder and sha), not a Seq Scan; OFFSET 0 keeps the planner from
       -- hashing all of the scope's locations up front.
       (EXISTS (
            SELECT 1 FROM message_locations g
            JOIN folders gf ON gf.id = g.folder_id
            JOIN accounts ga ON ga.id = gf.account_id
            WHERE g.message_sha256 = m.sha256 AND g.gone_at IS NOT NULL
              AND gf.account_id = ANY (sqlc.arg(accounts)::bigint[]) AND ga.kind = 'imap'
            OFFSET 0)
        AND NOT EXISTS (
            SELECT 1 FROM message_locations p
            JOIN folders pf ON pf.id = p.folder_id
            JOIN accounts pa ON pa.id = pf.account_id
            WHERE p.message_sha256 = m.sha256 AND p.gone_at IS NULL
              AND pf.account_id = ANY (sqlc.arg(accounts)::bigint[]) AND pa.kind = 'imap'
            OFFSET 0))::boolean AS gone
FROM messages m
WHERE
  -- filters:begin (keep all copies equal; TestSearchFilterCopies checks)
  (sqlc.narg(query)::text IS NULL
   OR m.search @@ (websearch_to_tsquery('simple', sqlc.narg(query)::text) ||
                    websearch_to_tsquery('german', sqlc.narg(query)::text) ||
                    websearch_to_tsquery('english', sqlc.narg(query)::text))
   OR m.subject ILIKE sqlc.narg(pattern)::text
   OR m.from_addr ILIKE sqlc.narg(pattern)::text
   -- A match in the text of one attachment, found first by
   -- AttachmentMatches; with too many of them, att_walk checks the
   -- attachments of each message instead. All words must be in the
   -- message or all in one attachment.
   OR m.sha256 = ANY (sqlc.narg(att_shas)::bpchar[])
   OR sqlc.arg(att_walk)::boolean AND EXISTS (
       SELECT 1 FROM attachment_texts ax
       WHERE ax.message_sha256 = m.sha256
         AND ax.search @@ (websearch_to_tsquery('simple', sqlc.narg(query)::text) ||
                           websearch_to_tsquery('german', sqlc.narg(query)::text) ||
                           websearch_to_tsquery('english', sqlc.narg(query)::text))
       OFFSET 0))
  -- Only messages found in one of the scope's accounts (own or shared).
  AND EXISTS (
       SELECT 1 FROM message_locations l
       JOIN folders f ON f.id = l.folder_id
       WHERE l.message_sha256 = m.sha256
         AND f.account_id = ANY (sqlc.arg(accounts)::bigint[])
         AND (sqlc.narg(folder)::text IS NULL OR f.name = sqlc.narg(folder)::text))
  -- Filters on single fields. Recipients and attachment names are only
  -- found here, not by the full-text query.
  AND (sqlc.narg(from_pattern)::text IS NULL OR m.from_addr ILIKE sqlc.narg(from_pattern)::text)
  -- To or Cc of the message, or an envelope recipient (BCC included) of
  -- one of the viewer's own smtp locations. The envelope matches come as
  -- to_shas, found first by EnvelopeMatches, so that both arms use an
  -- index of messages. With too many of them, to_walk checks the envelopes
  -- of each message instead.
  AND (sqlc.narg(to_pattern)::text IS NULL
       OR (coalesce(m.to_addr, '') || E'\n' || coalesce(m.cc_addr, '')) ILIKE sqlc.narg(to_pattern)::text
       OR m.sha256 = ANY (sqlc.narg(to_shas)::bpchar[])
       OR sqlc.arg(to_walk)::boolean AND EXISTS (
           SELECT 1 FROM message_locations rl
           JOIN envelopes re ON re.location_id = rl.id
           JOIN folders rf ON rf.id = rl.folder_id
           WHERE rl.message_sha256 = m.sha256
             AND rf.account_id = ANY (sqlc.arg(envelope_accounts)::bigint[])
             AND re.rcpt_text ILIKE sqlc.narg(to_pattern)::text
           -- OFFSET 0 keeps the planner from hashing all matching envelopes
           -- up front: the check runs for each message of the walk.
           OFFSET 0))
  AND (sqlc.narg(attachment_pattern)::text IS NULL OR m.attachment_names ILIKE sqlc.narg(attachment_pattern)::text)
  AND (NOT sqlc.arg(has_attachment)::boolean OR m.has_attachment)
  AND (sqlc.narg(thread)::text IS NULL OR COALESCE(m.thread_id, m.sha256) = sqlc.narg(thread)::text)
  AND (sqlc.narg(after)::timestamptz IS NULL OR m.sort_at >= sqlc.narg(after)::timestamptz)
  AND (sqlc.narg(before)::timestamptz IS NULL OR m.sort_at < sqlc.narg(before)::timestamptz)
  -- Only in the archive: the scope's IMAP locations (narrowed to one
  -- account, if given) are all gone from the server. Import accounts say nothing about
  -- servers, and removed accounts are never reconciled, so they count as
  -- present. Starts from the few gone locations (message_locations_gone_idx).
  AND (NOT sqlc.arg(gone)::boolean OR m.sha256 IN (
       SELECT g.message_sha256 FROM message_locations g
       JOIN folders gf ON gf.id = g.folder_id
       JOIN accounts ga ON ga.id = gf.account_id
       WHERE g.gone_at IS NOT NULL
         AND gf.account_id = ANY (sqlc.arg(accounts)::bigint[]) AND ga.kind = 'imap'
         AND NOT EXISTS (
             SELECT 1 FROM message_locations p
             JOIN folders pf ON pf.id = p.folder_id
             JOIN accounts pa ON pa.id = pf.account_id
             WHERE p.message_sha256 = g.message_sha256 AND p.gone_at IS NULL
               AND pf.account_id = ANY (sqlc.arg(accounts)::bigint[]) AND pa.kind = 'imap')))
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
       -- The row is only in the archive: exactly the gone filter's predicate
       -- below, with its account scope, for this row. Each subquery looks
       -- up the row's locations through an index of message_locations (sha,
       -- or folder and sha), not a Seq Scan; OFFSET 0 keeps the planner from
       -- hashing all of the scope's locations up front.
       (EXISTS (
            SELECT 1 FROM message_locations g
            JOIN folders gf ON gf.id = g.folder_id
            JOIN accounts ga ON ga.id = gf.account_id
            WHERE g.message_sha256 = m.sha256 AND g.gone_at IS NOT NULL
              AND gf.account_id = ANY (sqlc.arg(accounts)::bigint[]) AND ga.kind = 'imap'
            OFFSET 0)
        AND NOT EXISTS (
            SELECT 1 FROM message_locations p
            JOIN folders pf ON pf.id = p.folder_id
            JOIN accounts pa ON pa.id = pf.account_id
            WHERE p.message_sha256 = m.sha256 AND p.gone_at IS NULL
              AND pf.account_id = ANY (sqlc.arg(accounts)::bigint[]) AND pa.kind = 'imap'
            OFFSET 0))::boolean AS gone,
       COALESCE(m.thread_id, m.sha256)::text AS thread_key
FROM messages m
WHERE
  -- filters:begin (keep all copies equal; TestSearchFilterCopies checks)
  (sqlc.narg(query)::text IS NULL
   OR m.search @@ (websearch_to_tsquery('simple', sqlc.narg(query)::text) ||
                    websearch_to_tsquery('german', sqlc.narg(query)::text) ||
                    websearch_to_tsquery('english', sqlc.narg(query)::text))
   OR m.subject ILIKE sqlc.narg(pattern)::text
   OR m.from_addr ILIKE sqlc.narg(pattern)::text
   -- A match in the text of one attachment, found first by
   -- AttachmentMatches; with too many of them, att_walk checks the
   -- attachments of each message instead. All words must be in the
   -- message or all in one attachment.
   OR m.sha256 = ANY (sqlc.narg(att_shas)::bpchar[])
   OR sqlc.arg(att_walk)::boolean AND EXISTS (
       SELECT 1 FROM attachment_texts ax
       WHERE ax.message_sha256 = m.sha256
         AND ax.search @@ (websearch_to_tsquery('simple', sqlc.narg(query)::text) ||
                           websearch_to_tsquery('german', sqlc.narg(query)::text) ||
                           websearch_to_tsquery('english', sqlc.narg(query)::text))
       OFFSET 0))
  -- Only messages found in one of the scope's accounts (own or shared).
  AND EXISTS (
       SELECT 1 FROM message_locations l
       JOIN folders f ON f.id = l.folder_id
       WHERE l.message_sha256 = m.sha256
         AND f.account_id = ANY (sqlc.arg(accounts)::bigint[])
         AND (sqlc.narg(folder)::text IS NULL OR f.name = sqlc.narg(folder)::text))
  -- Filters on single fields. Recipients and attachment names are only
  -- found here, not by the full-text query.
  AND (sqlc.narg(from_pattern)::text IS NULL OR m.from_addr ILIKE sqlc.narg(from_pattern)::text)
  -- To or Cc of the message, or an envelope recipient (BCC included) of
  -- one of the viewer's own smtp locations. The envelope matches come as
  -- to_shas, found first by EnvelopeMatches, so that both arms use an
  -- index of messages. With too many of them, to_walk checks the envelopes
  -- of each message instead.
  AND (sqlc.narg(to_pattern)::text IS NULL
       OR (coalesce(m.to_addr, '') || E'\n' || coalesce(m.cc_addr, '')) ILIKE sqlc.narg(to_pattern)::text
       OR m.sha256 = ANY (sqlc.narg(to_shas)::bpchar[])
       OR sqlc.arg(to_walk)::boolean AND EXISTS (
           SELECT 1 FROM message_locations rl
           JOIN envelopes re ON re.location_id = rl.id
           JOIN folders rf ON rf.id = rl.folder_id
           WHERE rl.message_sha256 = m.sha256
             AND rf.account_id = ANY (sqlc.arg(envelope_accounts)::bigint[])
             AND re.rcpt_text ILIKE sqlc.narg(to_pattern)::text
           -- OFFSET 0 keeps the planner from hashing all matching envelopes
           -- up front: the check runs for each message of the walk.
           OFFSET 0))
  AND (sqlc.narg(attachment_pattern)::text IS NULL OR m.attachment_names ILIKE sqlc.narg(attachment_pattern)::text)
  AND (NOT sqlc.arg(has_attachment)::boolean OR m.has_attachment)
  AND (sqlc.narg(thread)::text IS NULL OR COALESCE(m.thread_id, m.sha256) = sqlc.narg(thread)::text)
  AND (sqlc.narg(after)::timestamptz IS NULL OR m.sort_at >= sqlc.narg(after)::timestamptz)
  AND (sqlc.narg(before)::timestamptz IS NULL OR m.sort_at < sqlc.narg(before)::timestamptz)
  -- Only in the archive: the scope's IMAP locations (narrowed to one
  -- account, if given) are all gone from the server. Import accounts say nothing about
  -- servers, and removed accounts are never reconciled, so they count as
  -- present. Starts from the few gone locations (message_locations_gone_idx).
  AND (NOT sqlc.arg(gone)::boolean OR m.sha256 IN (
       SELECT g.message_sha256 FROM message_locations g
       JOIN folders gf ON gf.id = g.folder_id
       JOIN accounts ga ON ga.id = gf.account_id
       WHERE g.gone_at IS NOT NULL
         AND gf.account_id = ANY (sqlc.arg(accounts)::bigint[]) AND ga.kind = 'imap'
         AND NOT EXISTS (
             SELECT 1 FROM message_locations p
             JOIN folders pf ON pf.id = p.folder_id
             JOIN accounts pa ON pa.id = pf.account_id
             WHERE p.message_sha256 = g.message_sha256 AND p.gone_at IS NULL
               AND pf.account_id = ANY (sqlc.arg(accounts)::bigint[]) AND pa.kind = 'imap')))
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
         OR n.from_addr ILIKE sqlc.narg(pattern)::text
         -- A match in the text of one attachment, found first by
         -- AttachmentMatches; with too many of them, att_walk checks the
         -- attachments of each message instead. All words must be in the
         -- message or all in one attachment.
         OR n.sha256 = ANY (sqlc.narg(att_shas)::bpchar[])
         OR sqlc.arg(att_walk)::boolean AND EXISTS (
             SELECT 1 FROM attachment_texts ax
             WHERE ax.message_sha256 = n.sha256
               AND ax.search @@ (websearch_to_tsquery('simple', sqlc.narg(query)::text) ||
                                 websearch_to_tsquery('german', sqlc.narg(query)::text) ||
                                 websearch_to_tsquery('english', sqlc.narg(query)::text))
             OFFSET 0))
        -- Only messages found in one of the scope's accounts (own or shared).
        AND EXISTS (
             SELECT 1 FROM message_locations l
             JOIN folders f ON f.id = l.folder_id
             WHERE l.message_sha256 = n.sha256
               AND f.account_id = ANY (sqlc.arg(accounts)::bigint[])
               AND (sqlc.narg(folder)::text IS NULL OR f.name = sqlc.narg(folder)::text))
        -- Filters on single fields. Recipients and attachment names are only
        -- found here, not by the full-text query.
        AND (sqlc.narg(from_pattern)::text IS NULL OR n.from_addr ILIKE sqlc.narg(from_pattern)::text)
        -- To or Cc of the message, or an envelope recipient (BCC included) of
        -- one of the viewer's own smtp locations. The envelope matches come as
        -- to_shas, found first by EnvelopeMatches, so that both arms use an
        -- index of messages. With too many of them, to_walk checks the envelopes
        -- of each message instead.
        AND (sqlc.narg(to_pattern)::text IS NULL
             OR (coalesce(n.to_addr, '') || E'\n' || coalesce(n.cc_addr, '')) ILIKE sqlc.narg(to_pattern)::text
             OR n.sha256 = ANY (sqlc.narg(to_shas)::bpchar[])
             OR sqlc.arg(to_walk)::boolean AND EXISTS (
                 SELECT 1 FROM message_locations rl
                 JOIN envelopes re ON re.location_id = rl.id
                 JOIN folders rf ON rf.id = rl.folder_id
                 WHERE rl.message_sha256 = n.sha256
                   AND rf.account_id = ANY (sqlc.arg(envelope_accounts)::bigint[])
                   AND re.rcpt_text ILIKE sqlc.narg(to_pattern)::text
                 -- OFFSET 0 keeps the planner from hashing all matching envelopes
                 -- up front: the check runs for each message of the walk.
                 OFFSET 0))
        AND (sqlc.narg(attachment_pattern)::text IS NULL OR n.attachment_names ILIKE sqlc.narg(attachment_pattern)::text)
        AND (NOT sqlc.arg(has_attachment)::boolean OR n.has_attachment)
        AND (sqlc.narg(thread)::text IS NULL OR COALESCE(n.thread_id, n.sha256) = sqlc.narg(thread)::text)
        AND (sqlc.narg(after)::timestamptz IS NULL OR n.sort_at >= sqlc.narg(after)::timestamptz)
        AND (sqlc.narg(before)::timestamptz IS NULL OR n.sort_at < sqlc.narg(before)::timestamptz)
        -- Only in the archive: the scope's IMAP locations (narrowed to one
        -- account, if given) are all gone from the server. Import accounts say nothing about
        -- servers, and removed accounts are never reconciled, so they count as
        -- present. Starts from the few gone locations (message_locations_gone_idx).
        AND (NOT sqlc.arg(gone)::boolean OR n.sha256 IN (
             SELECT g.message_sha256 FROM message_locations g
             JOIN folders gf ON gf.id = g.folder_id
             JOIN accounts ga ON ga.id = gf.account_id
             WHERE g.gone_at IS NOT NULL
               AND gf.account_id = ANY (sqlc.arg(accounts)::bigint[]) AND ga.kind = 'imap'
               AND NOT EXISTS (
                   SELECT 1 FROM message_locations p
                   JOIN folders pf ON pf.id = p.folder_id
                   JOIN accounts pa ON pa.id = pf.account_id
                   WHERE p.message_sha256 = g.message_sha256 AND p.gone_at IS NULL
                     AND pf.account_id = ANY (sqlc.arg(accounts)::bigint[]) AND pa.kind = 'imap')))
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
   OR t.from_addr ILIKE sqlc.narg(pattern)::text
   -- A match in the text of one attachment, found first by
   -- AttachmentMatches; with too many of them, att_walk checks the
   -- attachments of each message instead. All words must be in the
   -- message or all in one attachment.
   OR t.sha256 = ANY (sqlc.narg(att_shas)::bpchar[])
   OR sqlc.arg(att_walk)::boolean AND EXISTS (
       SELECT 1 FROM attachment_texts ax
       WHERE ax.message_sha256 = t.sha256
         AND ax.search @@ (websearch_to_tsquery('simple', sqlc.narg(query)::text) ||
                           websearch_to_tsquery('german', sqlc.narg(query)::text) ||
                           websearch_to_tsquery('english', sqlc.narg(query)::text))
       OFFSET 0))
  -- Only messages found in one of the scope's accounts (own or shared).
  AND EXISTS (
       SELECT 1 FROM message_locations l
       JOIN folders f ON f.id = l.folder_id
       WHERE l.message_sha256 = t.sha256
         AND f.account_id = ANY (sqlc.arg(accounts)::bigint[])
         AND (sqlc.narg(folder)::text IS NULL OR f.name = sqlc.narg(folder)::text))
  -- Filters on single fields. Recipients and attachment names are only
  -- found here, not by the full-text query.
  AND (sqlc.narg(from_pattern)::text IS NULL OR t.from_addr ILIKE sqlc.narg(from_pattern)::text)
  -- To or Cc of the message, or an envelope recipient (BCC included) of
  -- one of the viewer's own smtp locations. The envelope matches come as
  -- to_shas, found first by EnvelopeMatches, so that both arms use an
  -- index of messages. With too many of them, to_walk checks the envelopes
  -- of each message instead.
  AND (sqlc.narg(to_pattern)::text IS NULL
       OR (coalesce(t.to_addr, '') || E'\n' || coalesce(t.cc_addr, '')) ILIKE sqlc.narg(to_pattern)::text
       OR t.sha256 = ANY (sqlc.narg(to_shas)::bpchar[])
       OR sqlc.arg(to_walk)::boolean AND EXISTS (
           SELECT 1 FROM message_locations rl
           JOIN envelopes re ON re.location_id = rl.id
           JOIN folders rf ON rf.id = rl.folder_id
           WHERE rl.message_sha256 = t.sha256
             AND rf.account_id = ANY (sqlc.arg(envelope_accounts)::bigint[])
             AND re.rcpt_text ILIKE sqlc.narg(to_pattern)::text
           -- OFFSET 0 keeps the planner from hashing all matching envelopes
           -- up front: the check runs for each message of the walk.
           OFFSET 0))
  AND (sqlc.narg(attachment_pattern)::text IS NULL OR t.attachment_names ILIKE sqlc.narg(attachment_pattern)::text)
  AND (NOT sqlc.arg(has_attachment)::boolean OR t.has_attachment)
  AND (sqlc.narg(thread)::text IS NULL OR COALESCE(t.thread_id, t.sha256) = sqlc.narg(thread)::text)
  AND (sqlc.narg(after)::timestamptz IS NULL OR t.sort_at >= sqlc.narg(after)::timestamptz)
  AND (sqlc.narg(before)::timestamptz IS NULL OR t.sort_at < sqlc.narg(before)::timestamptz)
  -- Only in the archive: the scope's IMAP locations (narrowed to one
  -- account, if given) are all gone from the server. Import accounts say nothing about
  -- servers, and removed accounts are never reconciled, so they count as
  -- present. Starts from the few gone locations (message_locations_gone_idx).
  AND (NOT sqlc.arg(gone)::boolean OR t.sha256 IN (
       SELECT g.message_sha256 FROM message_locations g
       JOIN folders gf ON gf.id = g.folder_id
       JOIN accounts ga ON ga.id = gf.account_id
       WHERE g.gone_at IS NOT NULL
         AND gf.account_id = ANY (sqlc.arg(accounts)::bigint[]) AND ga.kind = 'imap'
         AND NOT EXISTS (
             SELECT 1 FROM message_locations p
             JOIN folders pf ON pf.id = p.folder_id
             JOIN accounts pa ON pa.id = pf.account_id
             WHERE p.message_sha256 = g.message_sha256 AND p.gone_at IS NULL
               AND pf.account_id = ANY (sqlc.arg(accounts)::bigint[]) AND pa.kind = 'imap')))
  -- filters:end
GROUP BY 1;

-- name: GetMessageSummary :one
-- Only if the message was found in one of the scope's accounts.
SELECT m.sha256, m.size, m.message_id, m.subject, m.from_addr, m.sent_at, m.sort_at, m.stored_path,
       m.in_reply_to, COALESCE(m.thread_id, m.sha256)::text AS thread_key
FROM messages m
WHERE m.sha256 = @sha256
  AND EXISTS (
      SELECT 1 FROM message_locations l
      JOIN folders f ON f.id = l.folder_id
      WHERE l.message_sha256 = m.sha256 AND f.account_id = ANY (@accounts::bigint[]));

-- name: ListLocations :many
-- The locations of a message in the scope's accounts, current ones first.
-- A location is superseded when the folder's UIDVALIDITY changed since it
-- was stored: the server renumbered the folder, and the rescan added a new
-- location for every message still there. Superseded locations stay as
-- history. Envelopes come only from envelope_accounts, the viewer's own
-- smtp accounts.
SELECT a.id AS account_id, a.name AS account, u.id AS owner_id, u.name AS owner,
       f.name AS folder, l.uid, l.flags, l.internal_date,
       (l.uidvalidity <> f.uidvalidity)::boolean AS superseded, l.gone_at,
       -- A present location was seen at the folder's last reconcile.
       (CASE WHEN l.gone_at IS NULL AND l.uidvalidity = f.uidvalidity
             THEN GREATEST(l.last_seen_at, f.last_reconciled_at)
             ELSE l.last_seen_at END)::timestamptz AS last_seen_at,
       -- The SMTP envelope of locations in smtp accounts.
       e.location_id AS envelope_location, e.mail_from, e.rcpt_to, e.received_at, e.client_ip, e.helo,
       -- A location in an smtp account without an envelope came from a
       -- fill or an import of files: ReceiveMessage always writes one.
       (a.kind = 'smtp' AND NOT EXISTS (SELECT 1 FROM envelopes fe WHERE fe.location_id = l.id))::boolean AS filled,
       -- The fill it came from, if still known: its mailbox.
       sf.id AS fill_id, sf.username AS fill_username, sf.host AS fill_host,
       -- The import of files it came from, if still known: its label.
       si.id AS import_id, si.label AS import_label,
       -- The end of the retention period (#55); pgx cannot scan infinity
       -- into a time, so "no rule" is a flag.
       (r.until = 'infinity')::boolean AS kept_forever,
       LEAST(r.until, '9999-12-31')::timestamptz AS retain_until,
       location_held(l.message_sha256, l.folder_id, m.sent_at, l.internal_date)::boolean AS on_hold
FROM message_locations l
JOIN messages m ON m.sha256 = l.message_sha256
JOIN folders f ON f.id = l.folder_id
JOIN accounts a ON a.id = f.account_id
JOIN users u ON u.id = a.owner_id
LEFT JOIN envelopes e ON e.location_id = l.id AND f.account_id = ANY (@envelope_accounts::bigint[])
LEFT JOIN smtp_fills sf ON sf.id = l.fill_id
LEFT JOIN smtp_imports si ON si.id = l.import_id
CROSS JOIN LATERAL (SELECT location_retain_until(l.folder_id, m.sent_at, l.internal_date, m.created_at) AS until) r
WHERE l.message_sha256 = @sha256 AND f.account_id = ANY (@accounts::bigint[])
ORDER BY superseded, a.name, u.name, a.id, f.name;

-- name: ListFolderCounts :many
-- The scope's accounts with their folders and the number of distinct
-- messages per folder, like the folder's message list: a message with an
-- old and a new location (after a UIDVALIDITY change) counts once. The
-- subquery per folder uses message_locations_folder_sha_idx; a
-- count(DISTINCT) over the grouped join is about 20 times slower on large
-- archives. Accounts are told apart by ID: two owners may both have an
-- account of the same name.
SELECT a.id, a.name AS account, u.id AS owner_id, u.name AS owner,
       a.enabled, (a.removed_at IS NOT NULL)::boolean AS removed, a.kind,
       f.name AS folder,
       (SELECT count(*) FROM (
            SELECT DISTINCT l.message_sha256 FROM message_locations l WHERE l.folder_id = f.id) d
       )::bigint AS messages,
       f.last_synced_at, f.last_reconciled_at, f.delimiter
FROM accounts a
JOIN users u ON u.id = a.owner_id
LEFT JOIN folders f ON f.account_id = a.id
WHERE a.id = ANY (@accounts::bigint[])
ORDER BY u.name, a.name, a.id, f.name;

-- name: EnvelopeMatches :many
-- Messages with an envelope recipient matching the to: filter in one of
-- envelope_accounts (the viewer's own smtp accounts), up to row_limit;
-- uses envelopes_rcpt_trgm_idx.
SELECT DISTINCT rl.message_sha256 FROM envelopes re
JOIN message_locations rl ON rl.id = re.location_id
JOIN folders rf ON rf.id = rl.folder_id
WHERE rf.account_id = ANY (@envelope_accounts::bigint[]) AND re.rcpt_text ILIKE @pattern::text
LIMIT @row_limit;

-- name: ListShareOnlyLocations :many
-- For the given messages, the shared accounts through which the viewer
-- sees each of them, for those the viewer finds in none of own_accounts:
-- one row per (message, account). Each hash is looked up on its own
-- through message_locations_sha_idx; OFFSET 0 keeps the planner from
-- reading the locations of the accounts instead.
SELECT DISTINCT x.sha256::text AS sha256, x.account_id, x.owner_id
FROM unnest(@shas::bpchar[]) AS s (sha)
CROSS JOIN LATERAL (
    SELECT l.message_sha256 AS sha256, f.account_id, a.owner_id::bigint AS owner_id
    FROM message_locations l
    JOIN folders f ON f.id = l.folder_id
    JOIN accounts a ON a.id = f.account_id
    WHERE l.message_sha256 = s.sha AND f.account_id = ANY (@shared_accounts::bigint[])
    OFFSET 0) x
WHERE NOT EXISTS (
    SELECT 1 FROM message_locations o
    JOIN folders ofo ON ofo.id = o.folder_id
    WHERE o.message_sha256 = s.sha AND ofo.account_id = ANY (@own_accounts::bigint[])
    OFFSET 0)
ORDER BY 1, 2;
