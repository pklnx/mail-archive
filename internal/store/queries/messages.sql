-- name: MessageExists :one
SELECT EXISTS (SELECT 1 FROM messages WHERE sha256 = $1);

-- name: InsertMessage :execrows
INSERT INTO messages (sha256, size, message_id, subject, from_addr, sent_at, stored_path, body_text,
                      to_addr, cc_addr, attachment_names, has_attachment,
                      in_reply_to, reference_ids, thread_id, index_version)
VALUES (@sha256, @size, NULLIF(@message_id::text, ''), NULLIF(@subject::text, ''),
        NULLIF(@from_addr::text, ''), @sent_at, @stored_path, @body_text::text,
        NULLIF(@to_addr::text, ''), NULLIF(@cc_addr::text, ''), NULLIF(@attachment_names::text, ''),
        @has_attachment, NULLIF(@in_reply_to::text, ''), @reference_ids::text[], NULLIF(@thread_id::text, ''),
        @index_version)
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
    in_reply_to = NULLIF(@in_reply_to::text, ''), reference_ids = @reference_ids::text[],
    thread_id = NULLIF(@thread_id::text, ''), index_version = @index_version
WHERE sha256 = @sha256 AND index_version < @index_version;

-- name: UpsertLocation :one
-- A location stored again (a UIDVALIDITY that returned to an old value) is
-- on the server again. It keeps its attachment_text_ok: the conflict
-- never changes the message (store/attachmentflag.go decides the flag of
-- a new one). Returns whether the location is new.
INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid, flags, internal_date, attachment_text_ok)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (folder_id, uidvalidity, uid)
DO UPDATE SET flags = EXCLUDED.flags, last_seen_at = now(), gone_at = NULL
RETURNING (xmax = 0)::boolean AS inserted;

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

-- name: AccountHeldMessages :many
-- Hashes of a batch that the account of the folder holds already, in any
-- of its folders (gone locations included). Driven from the account's
-- folders: one probe of message_locations_folder_sha_idx per folder and
-- hash, so the cost does not depend on how many other accounts hold the
-- same bytes.
SELECT DISTINCT l.message_sha256::text AS sha256
FROM folders f
JOIN LATERAL (
    SELECT ml.message_sha256 FROM message_locations ml
    WHERE ml.folder_id = f.id AND ml.message_sha256 = ANY (@shas::bpchar[])
) l ON true
WHERE f.account_id = (SELECT a.account_id FROM folders a WHERE a.id = @folder_id);

-- name: GroupFolderIDs :many
-- The folders of a customer group (0: the accounts without an owner), for
-- FoldersHoldMessages; read once per folder run of a sync or fill and per
-- SMTP delivery, and again when the account's group changed.
SELECT f.id FROM folders f
JOIN accounts a ON a.id = f.account_id
LEFT JOIN users o ON o.id = a.owner_id
WHERE coalesce(o.group_id, 0) = @group_id::bigint
ORDER BY f.id;

-- name: FoldersHoldMessages :many
-- The hashes of shas with a location (gone ones too) in one of the folders
-- that still belongs to the group: GroupHasLocations with the group's
-- folder list given (GroupFolderIDs, read once per run), so a message's
-- lookup does not build it again (#539, part 3). One probe of
-- message_locations_folder_sha_idx per folder and hash; the LATERAL's
-- LIMIT 1 keeps the planner from turning the probes into a lookup by hash
-- in message_locations_sha_idx, in the custom and the generic plan
-- (TestFoldersHoldMessagesPlan). Only the folders that hold a hash are
-- then checked against the group, so a folder whose account left the
-- group since the list was read never counts.
SELECT DISTINCT h.sha::text AS sha256
FROM (
    SELECT s.sha, gf.id
    FROM unnest(@shas::bpchar[]) AS s (sha)
    CROSS JOIN (SELECT unnest(@folder_ids::bigint[]) AS id OFFSET 0) gf
    JOIN LATERAL (
        SELECT 1 FROM message_locations ml
        WHERE ml.folder_id = gf.id AND ml.message_sha256 = s.sha
        LIMIT 1
    ) held ON true
    OFFSET 0
) h
JOIN folders f ON f.id = h.id
JOIN accounts a ON a.id = f.account_id
LEFT JOIN users o ON o.id = a.owner_id
WHERE coalesce(o.group_id, 0) = @group_id::bigint;
