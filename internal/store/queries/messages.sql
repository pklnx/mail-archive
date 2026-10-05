-- name: MessageExists :one
SELECT EXISTS (SELECT 1 FROM messages WHERE sha256 = $1);

-- name: InsertMessage :execrows
INSERT INTO messages (sha256, size, message_id, subject, from_addr, sent_at, stored_path)
VALUES (@sha256, @size, NULLIF(@message_id::text, ''), NULLIF(@subject::text, ''),
        NULLIF(@from_addr::text, ''), @sent_at, @stored_path)
ON CONFLICT (sha256) DO NOTHING;

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
