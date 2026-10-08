-- name: MessageExists :one
SELECT EXISTS (SELECT 1 FROM messages WHERE sha256 = $1);

-- name: InsertMessage :execrows
INSERT INTO messages (sha256, size, message_id, subject, from_addr, sent_at, stored_path, body_text,
                      to_addr, cc_addr, attachment_names, has_attachment, index_version)
VALUES (@sha256, @size, NULLIF(@message_id::text, ''), NULLIF(@subject::text, ''),
        NULLIF(@from_addr::text, ''), @sent_at, @stored_path, @body_text::text,
        NULLIF(@to_addr::text, ''), NULLIF(@cc_addr::text, ''), NULLIF(@attachment_names::text, ''),
        @has_attachment, @index_version)
ON CONFLICT (sha256) DO NOTHING;

-- name: ListUnindexed :many
-- Messages extracted by an older version, in primary key order after the
-- last one seen, so a full pass reads the table once. The bpchar cast lets
-- the primary key index seek to the start of each batch.
SELECT sha256, stored_path FROM messages
WHERE index_version < @index_version AND sha256 > @after::bpchar
ORDER BY sha256
LIMIT @row_limit;

-- name: HasUnindexed :one
SELECT EXISTS (SELECT 1 FROM messages WHERE index_version < @index_version);

-- name: SetIndexData :execrows
-- Never overwrites a row that a newer version already extracted.
UPDATE messages
SET body_text = @body_text::text, to_addr = NULLIF(@to_addr::text, ''), cc_addr = NULLIF(@cc_addr::text, ''),
    attachment_names = NULLIF(@attachment_names::text, ''), has_attachment = @has_attachment,
    index_version = @index_version
WHERE sha256 = @sha256 AND index_version < @index_version;

-- name: UpsertLocation :exec
INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid, flags, internal_date)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (folder_id, uidvalidity, uid)
DO UPDATE SET flags = EXCLUDED.flags, last_seen_at = now();

-- name: GetMessage :one
SELECT sha256, size, message_id, subject, from_addr, sent_at, stored_path
FROM messages WHERE sha256 = $1;

-- name: CountMessages :one
SELECT count(*) FROM messages;

-- name: CountOwnedMessages :one
-- Unique messages found in the user's accounts.
SELECT count(DISTINCT l.message_sha256) FROM message_locations l
JOIN folders f ON f.id = l.folder_id
JOIN accounts a ON a.id = f.account_id
WHERE a.owner_id = $1;
