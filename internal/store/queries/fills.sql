-- name: InsertFill :one
INSERT INTO smtp_fills (account_id, host, port, tls_mode, username, included_folders, excluded_folders,
                        folder_prefix, cutoff, created_by)
VALUES (@account_id, @host, @port, @tls_mode, @username, @included_folders::text[], @excluded_folders::text[],
        @folder_prefix, @cutoff, @created_by)
RETURNING id, created_at;

-- name: SetFillPassword :execrows
UPDATE smtp_fills SET password_enc = @password_enc WHERE id = @id AND status = 'running';

-- name: GetFill :one
SELECT * FROM smtp_fills WHERE id = $1;

-- name: ListAccountFills :many
-- The fills of an account, newest first.
SELECT * FROM smtp_fills WHERE account_id = $1 ORDER BY id DESC;

-- name: LatestFills :many
-- The newest fill of each account of the owner.
SELECT DISTINCT ON (sf.account_id) sf.*
FROM smtp_fills sf JOIN accounts a ON a.id = sf.account_id
WHERE a.owner_id = $1
ORDER BY sf.account_id, sf.id DESC;

-- name: RunningFills :many
SELECT * FROM smtp_fills WHERE status = 'running' ORDER BY id;

-- name: ListFillFolders :many
SELECT ff.*, f.name AS folder_name
FROM smtp_fill_folders ff JOIN folders f ON f.id = ff.folder_id
WHERE ff.fill_id = $1
ORDER BY ff.source_name;

-- name: InsertFillFolder :exec
INSERT INTO smtp_fill_folders (fill_id, source_name, folder_id) VALUES ($1, $2, $3)
ON CONFLICT (fill_id, source_name) DO NOTHING;

-- name: GetFillFolder :one
SELECT ff.*, f.name AS folder_name
FROM smtp_fill_folders ff JOIN folders f ON f.id = ff.folder_id
WHERE ff.fill_id = $1 AND ff.source_name = $2;

-- name: UpdateFillFolder :exec
UPDATE smtp_fill_folders
SET source_uidvalidity = @source_uidvalidity, last_uid = @last_uid, done = @done,
    scanned = @scanned, imported = @imported, new_messages = @new_messages, after_cutoff = @after_cutoff,
    journaled = @journaled, present = @present, deleted = @deleted, error = @error
WHERE fill_id = @fill_id AND source_name = @source_name;

-- name: FinishFill :execrows
-- Only a running fill: a cancel that came first stays.
UPDATE smtp_fills SET status = @status, error = @error, finished_at = now(), password_enc = NULL
WHERE id = @id AND status = 'running';

-- name: ResumeFill :execrows
UPDATE smtp_fills SET status = 'running', error = NULL, finished_at = NULL, password_enc = @password_enc
WHERE id = @id AND status IN ('failed', 'partial', 'cancelled');

-- name: CancelFill :execrows
UPDATE smtp_fills SET status = 'cancelled', finished_at = now(), password_enc = NULL
WHERE id = $1 AND status = 'running';

-- name: CancelAccountFills :exec
-- For a removed account: no fill runs on, and no password stays.
UPDATE smtp_fills SET status = 'cancelled', finished_at = now(), password_enc = NULL, error = 'the account was removed'
WHERE account_id = $1 AND status = 'running';

-- name: FillStatus :one
SELECT status FROM smtp_fills WHERE id = $1;

-- name: JournaledMessageIDs :many
-- Message-IDs among @ids of messages the account received over SMTP (with
-- an envelope); uses messages_message_id_idx.
SELECT DISTINCT m.message_id::text AS message_id
FROM messages m
JOIN message_locations l ON l.message_sha256 = m.sha256
JOIN envelopes e ON e.location_id = l.id
JOIN folders f ON f.id = l.folder_id
WHERE f.account_id = @account_id AND m.message_id = ANY (@ids::text[]);

-- name: FirstReceived :one
-- The first message the smtp account received over SMTP that is still
-- stored: the lowest UID in its journal folder with an envelope (fills
-- write into the folder too, without one).
SELECT l.internal_date, m.subject
FROM folders f
JOIN message_locations l ON l.folder_id = f.id
JOIN envelopes e ON e.location_id = l.id
JOIN messages m ON m.sha256 = l.message_sha256
WHERE f.account_id = @account_id AND f.name = @folder
ORDER BY l.uidvalidity, l.uid
LIMIT 1;

-- name: JournalFolder :one
-- The journal folder of an smtp account, which fills write into.
SELECT id FROM folders WHERE account_id = @account_id AND name = @folder;
