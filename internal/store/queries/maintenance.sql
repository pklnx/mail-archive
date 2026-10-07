-- name: ListMessagesAfter :many
-- One page of all messages by hash, for verify.
SELECT sha256, size, stored_path FROM messages
WHERE sha256 > @after::text
ORDER BY sha256
LIMIT @page_size;

-- name: ExistingMessages :many
-- The hashes of the list that have a messages row.
SELECT sha256::text FROM messages WHERE sha256 = ANY(@hashes::text[]);

-- name: LocationsOfMessages :many
-- Where messages were found, to tell owners what to restore.
SELECT l.message_sha256::text AS sha256, coalesce(u.name, '') AS owner, a.name AS account,
       f.name AS folder, l.uidvalidity, l.uid
FROM message_locations l
JOIN folders f ON f.id = l.folder_id
JOIN accounts a ON a.id = f.account_id
LEFT JOIN users u ON u.id = a.owner_id
WHERE l.message_sha256 = ANY(@hashes::text[])
ORDER BY l.message_sha256, u.name, a.name, f.name, l.uidvalidity, l.uid;

-- name: MaxLocationID :one
SELECT coalesce(max(id), 0)::bigint FROM message_locations;

-- name: ListExportFolders :many
-- The folders of the given accounts, only if they belong to owner (NULL for
-- accounts without owner). An empty folder name means all folders.
SELECT f.id, a.name AS account, f.name AS folder
FROM folders f
JOIN accounts a ON a.id = f.account_id
WHERE a.id = ANY(@account_ids::bigint[])
  AND a.owner_id IS NOT DISTINCT FROM sqlc.narg('owner')::bigint
  AND (@folder::text = '' OR f.name = @folder::text)
ORDER BY a.name, f.name, f.id;

-- name: ListExportLocations :many
-- One page of a folder's locations up to max_id, by (uidvalidity, uid), with
-- the same owner check as ListExportFolders.
SELECT l.message_sha256::text AS sha256, l.uidvalidity, l.uid, l.flags, l.internal_date,
       m.sent_at, m.size
FROM message_locations l
JOIN messages m ON m.sha256 = l.message_sha256
JOIN folders f ON f.id = l.folder_id
JOIN accounts a ON a.id = f.account_id
WHERE l.folder_id = @folder_id
  AND a.owner_id IS NOT DISTINCT FROM sqlc.narg('owner')::bigint
  AND l.id <= @max_id::bigint
  AND (l.uidvalidity, l.uid) > (@after_uidvalidity::bigint, @after_uid::bigint)
ORDER BY l.uidvalidity, l.uid
LIMIT @page_size;
