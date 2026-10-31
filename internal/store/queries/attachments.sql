-- Attachment text for search (see 00033 and internal/archive/attachments.go).

-- name: AttachmentIndexVersion :one
-- The extraction state of a message: 0 when it has none.
SELECT coalesce((SELECT s.version FROM attachment_index_state s WHERE s.message_sha256 = @sha256), 0)::smallint;

-- name: MarkAttachmentsIndexed :execrows
-- Records that a message is done at version, unless it is already: 0 rows
-- means another writer finished first (or the version is not newer), and
-- the caller drops its result. A message that is gone fails on the
-- foreign key.
INSERT INTO attachment_index_state AS s (message_sha256, version) VALUES (@sha256, @version)
ON CONFLICT (message_sha256) DO UPDATE SET version = EXCLUDED.version
WHERE s.version < EXCLUDED.version;

-- name: DeleteAttachmentTexts :exec
-- The texts of an older version, before the new ones are written.
DELETE FROM attachment_texts WHERE message_sha256 = @sha256;

-- name: InsertAttachmentText :exec
INSERT INTO attachment_texts (message_sha256, part_index, filename, status, body_text)
VALUES (@sha256, @part_index, @filename, @status, sqlc.narg(body_text));

-- name: ListAttachmentPending :many
-- Messages with attachments that are not done at version, newest first
-- from the cursor (exclusive): through messages_attachment_sort_idx and
-- the state's primary key. Messages with an attachment_text job that is
-- queued, running or failed are skipped: they are either in the queue or
-- wait for `jobs retry`.
SELECT m.sha256::text AS sha256, m.sort_at
FROM messages m
WHERE m.has_attachment
  AND (sqlc.narg(after_sort_at)::timestamptz IS NULL
       OR (m.sort_at, m.sha256) < (sqlc.narg(after_sort_at)::timestamptz, sqlc.narg(after_sha)::text))
  AND NOT EXISTS (
      SELECT 1 FROM attachment_index_state s
      WHERE s.message_sha256 = m.sha256 AND s.version >= @version::smallint)
  AND NOT EXISTS (
      SELECT 1 FROM jobs j
      WHERE j.kind = 'attachment_text' AND j.key = m.sha256::text AND j.state IN ('queued', 'running', 'failed'))
ORDER BY m.sort_at DESC, m.sha256 DESC
LIMIT @row_limit;

-- name: CountAttachmentPending :one
-- Messages with attachments that are not done at version, including those
-- whose job is queued or running (not failed ones).
SELECT count(*)::bigint
FROM messages m
WHERE m.has_attachment
  AND NOT EXISTS (
      SELECT 1 FROM attachment_index_state s
      WHERE s.message_sha256 = m.sha256 AND s.version >= @version::smallint)
  AND NOT EXISTS (
      SELECT 1 FROM jobs j
      WHERE j.kind = 'attachment_text' AND j.key = m.sha256::text AND j.state = 'failed');

-- name: AttachmentMatches :many
-- Messages with an attachment whose text matches the query, found in one
-- of the scope's accounts, up to row_limit; uses
-- attachment_texts_search_idx.
SELECT DISTINCT a.message_sha256::text
FROM attachment_texts a
-- Only rows with text: an empty one would match any negation (-draft).
WHERE a.body_text IS NOT NULL
  AND a.search @@ (websearch_to_tsquery('simple', @query::text) ||
                   websearch_to_tsquery('german', @query::text) ||
                   websearch_to_tsquery('english', @query::text))
  AND EXISTS (
      SELECT 1 FROM message_locations l
      JOIN folders f ON f.id = l.folder_id
      WHERE l.message_sha256 = a.message_sha256
        AND f.account_id = ANY (@accounts::bigint[]))
LIMIT @row_limit;

-- name: AttachmentSnippets :many
-- For listed messages that matched by an attachment only: the matching
-- attachment with the lowest part index, and a snippet of its text marked
-- like the body snippets of SearchMessages. Only messages found in one of
-- the scope's accounts. Each hash is looked up on its own through the
-- primary key, and the snippet is made for one attachment per message.
SELECT s.sha::text AS sha256, a.part_index, a.filename,
       ts_headline('german', a.body_text,
           websearch_to_tsquery('simple', @query::text) ||
           websearch_to_tsquery('german', @query::text) ||
           websearch_to_tsquery('english', @query::text),
           'MaxFragments=1, MaxWords=30, MinWords=12, StartSel=' || chr(57344) || ', StopSel=' || chr(57345))::text AS snippet
FROM unnest(@shas::bpchar[]) AS s (sha)
CROSS JOIN LATERAL (
    SELECT ax.part_index, ax.filename, ax.body_text
    FROM attachment_texts ax
    WHERE ax.message_sha256 = s.sha
      -- Only rows with text, as in AttachmentMatches.
      AND ax.body_text IS NOT NULL
      AND ax.search @@ (websearch_to_tsquery('simple', @query::text) ||
                        websearch_to_tsquery('german', @query::text) ||
                        websearch_to_tsquery('english', @query::text))
    ORDER BY ax.part_index
    LIMIT 1) a
WHERE EXISTS (
    SELECT 1 FROM message_locations l
    JOIN folders f ON f.id = l.folder_id
    WHERE l.message_sha256 = s.sha
      AND f.account_id = ANY (@accounts::bigint[]))
ORDER BY 1;

-- name: CountFailedJobsWithCode :one
-- Failed jobs of a kind whose last error is code.
SELECT count(*)::bigint FROM jobs WHERE kind = @kind AND state = 'failed' AND last_error = @code::text;
