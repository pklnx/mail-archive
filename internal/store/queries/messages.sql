-- name: MessageExists :one
SELECT EXISTS (SELECT 1 FROM messages WHERE sha256 = $1);

-- name: InsertMessage :execrows
INSERT INTO messages (sha256, size, message_id, subject, from_addr, sent_at, stored_path, body_text)
VALUES (@sha256, @size, NULLIF(@message_id::text, ''), NULLIF(@subject::text, ''),
        NULLIF(@from_addr::text, ''), @sent_at, @stored_path, @body_text::text)
ON CONFLICT (sha256) DO NOTHING;

-- name: ListUnindexed :many
-- Messages archived before full-text search existed.
SELECT sha256, stored_path FROM messages
WHERE body_text IS NULL
ORDER BY sha256
LIMIT $1;

-- name: SetBodyText :exec
UPDATE messages SET body_text = $2 WHERE sha256 = $1;

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
