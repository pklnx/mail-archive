-- The conversation of a message: the other messages of its thread, plus one
-- hop of direct links that need no common thread ID: the parent (its
-- Message-ID is the message's In-Reply-To) and the replies (their
-- In-Reply-To or References name the message's Message-ID). Only messages
-- found in one of the user's accounts.

-- name: ListConversation :many
-- The newest row_limit members, newest first; the caller reverses them.
WITH me AS (
    SELECT x.sha256, x.message_id, x.in_reply_to, COALESCE(x.thread_id, x.sha256)::text AS thread_key
    FROM messages x WHERE x.sha256 = @sha256
)
SELECT m.sha256, m.subject, m.from_addr, m.sent_at, m.sort_at,
       (CASE
            WHEN m.sha256 = me.sha256 THEN 'self'
            WHEN m.message_id = me.in_reply_to THEN 'parent'
            WHEN m.in_reply_to = me.message_id OR m.reference_ids @> ARRAY[me.message_id] THEN 'reply'
            ELSE 'thread'
        END)::text AS relation
FROM messages m, me
WHERE (COALESCE(m.thread_id, m.sha256) = me.thread_key
       OR m.message_id = me.in_reply_to
       OR m.in_reply_to = me.message_id
       OR m.reference_ids @> ARRAY[me.message_id])
  AND EXISTS (
      SELECT 1 FROM message_locations l
      JOIN folders f ON f.id = l.folder_id
      JOIN accounts a ON a.id = f.account_id
      WHERE l.message_sha256 = m.sha256 AND a.owner_id = @owner::bigint)
ORDER BY m.sort_at DESC, m.sha256 DESC
LIMIT @row_limit;

-- name: CountConversation :one
WITH me AS (
    SELECT x.sha256, x.message_id, x.in_reply_to, COALESCE(x.thread_id, x.sha256)::text AS thread_key
    FROM messages x WHERE x.sha256 = @sha256
)
SELECT count(*)::bigint
FROM messages m, me
WHERE (COALESCE(m.thread_id, m.sha256) = me.thread_key
       OR m.message_id = me.in_reply_to
       OR m.in_reply_to = me.message_id
       OR m.reference_ids @> ARRAY[me.message_id])
  AND EXISTS (
      SELECT 1 FROM message_locations l
      JOIN folders f ON f.id = l.folder_id
      JOIN accounts a ON a.id = f.account_id
      WHERE l.message_sha256 = m.sha256 AND a.owner_id = @owner::bigint);
