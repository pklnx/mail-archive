-- name: GetOrCreateFolder :one
INSERT INTO folders (account_id, name) VALUES ($1, $2)
ON CONFLICT (account_id, name) DO UPDATE SET name = EXCLUDED.name
RETURNING id, uidvalidity, last_uid, last_reconciled_at;

-- name: SetFolderDelimiters :execrows
-- Records the hierarchy delimiters from a LIST answer for the account's
-- folders that have a row, also those the sync skips; writes only changes.
UPDATE folders f SET delimiter = v.delimiter
-- Two unnests in the select list are zipped (same length, from Go).
FROM (SELECT unnest(@names::text[]) AS name, unnest(@delimiters::text[]) AS delimiter) v
WHERE f.account_id = @account_id AND f.name = v.name AND f.delimiter IS DISTINCT FROM v.delimiter;

-- name: ResetFolder :execrows
UPDATE folders SET uidvalidity = $2, last_uid = 0 WHERE id = $1;

-- name: AdvanceFolder :exec
UPDATE folders SET last_uid = GREATEST(last_uid, @last_uid::bigint), last_synced_at = now() WHERE id = @id;

-- name: TouchFolder :execrows
UPDATE folders SET last_synced_at = now() WHERE id = $1;

-- name: LocationInFolder :one
-- Whether the message already has a location in the folder; uses
-- message_locations_sha_idx.
SELECT EXISTS (SELECT 1 FROM message_locations WHERE message_sha256 = @sha256 AND folder_id = @folder_id);
