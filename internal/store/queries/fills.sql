-- name: InsertFill :one
-- A recurring fill has no cut-off and starts its first run at once.
INSERT INTO smtp_fills (account_id, host, port, tls_mode, username, included_folders, excluded_folders,
                        folder_prefix, cutoff, created_by, keep_running, interval_seconds,
                        recurring_since, run_started_at)
VALUES (@account_id, @host, @port, @tls_mode, @username, @included_folders::text[], @excluded_folders::text[],
        @folder_prefix, @cutoff, @created_by, @keep_running, @interval_seconds,
        @recurring_since, @run_started_at)
RETURNING id, created_at;

-- name: SetFillPassword :execrows
UPDATE smtp_fills SET password_enc = @password_enc WHERE id = @id AND status = 'running';

-- name: GetFill :one
SELECT * FROM smtp_fills WHERE id = $1;

-- name: ListAccountFills :many
-- The fills of an account, newest first.
SELECT * FROM smtp_fills WHERE account_id = $1 ORDER BY id DESC;

-- name: OwnerFills :many
-- The fills the owner's account cards show: every fill that was ever
-- recurring, and the newest fill of each account; newest first.
SELECT sf.*
FROM smtp_fills sf JOIN accounts a ON a.id = sf.account_id
WHERE a.owner_id = $1
  AND (sf.recurring_since IS NOT NULL
    OR sf.id = (SELECT max(n.id) FROM smtp_fills n WHERE n.account_id = sf.account_id))
ORDER BY sf.account_id, sf.id DESC;

-- name: RunningFills :many
SELECT * FROM smtp_fills WHERE status = 'running' ORDER BY id;

-- name: RecurringFills :many
-- Active recurring fills between runs, of enabled accounts that are not
-- removed; the runner decides which are due.
SELECT sf.* FROM smtp_fills sf JOIN accounts a ON a.id = sf.account_id
WHERE sf.keep_running AND sf.status <> 'running' AND a.enabled AND a.removed_at IS NULL
ORDER BY sf.id;

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
    journaled = @journaled, present = @present, deleted = @deleted, waiting = @waiting, error = @error
WHERE fill_id = @fill_id AND source_name = @source_name;

-- name: ResetFillRun :exec
-- A new run of a recurring fill: every folder is visited again, and the
-- counts are those of the run.
UPDATE smtp_fill_folders
SET done = FALSE, error = NULL, scanned = 0, imported = 0, new_messages = 0, after_cutoff = 0,
    journaled = 0, present = 0, deleted = 0, waiting = 0
WHERE fill_id = $1;

-- name: FinishFill :one
-- Only a running fill: a cancel that came first stays. A recurring fill
-- keeps its password for the next run.
UPDATE smtp_fills SET status = @status, error = @error, finished_at = now(),
    password_enc = CASE WHEN keep_running THEN password_enc END
WHERE id = @id AND status = 'running'
RETURNING keep_running;

-- name: StartFillRun :execrows
-- The next run of an active recurring fill, started at @started_at by the
-- clock of the runner, which compares it with that clock.
UPDATE smtp_fills SET status = 'running', error = NULL, finished_at = NULL, run_started_at = @started_at
WHERE id = @id AND keep_running AND status IN ('ok', 'partial', 'failed');

-- name: ResumeFill :one
-- A one-time fill continues where it stopped; a stopped recurring fill
-- keeps running again, starting a run.
UPDATE smtp_fills SET status = 'running', error = NULL, finished_at = NULL, password_enc = @password_enc,
    keep_running = recurring_since IS NOT NULL,
    run_started_at = CASE WHEN recurring_since IS NOT NULL THEN now() ELSE run_started_at END
WHERE id = @id AND NOT keep_running AND status IN ('failed', 'partial', 'cancelled')
RETURNING keep_running;

-- name: SetFillRecurring :execrows
-- Switches an ended fill on: it keeps running from now on, with the new
-- password, and starts its first run.
UPDATE smtp_fills SET recurring_since = coalesce(recurring_since, now()), keep_running = TRUE,
    interval_seconds = @interval_seconds, password_enc = @password_enc,
    status = 'running', error = NULL, finished_at = NULL, run_started_at = now()
WHERE id = @id AND status <> 'running' AND NOT keep_running;

-- name: RewindFillFolders :exec
-- A one-time fill that becomes recurring: its folders are scanned again
-- from the start, so the new rule sees what the cut-off left out.
UPDATE smtp_fill_folders SET last_uid = 0, done = FALSE WHERE fill_id = $1;

-- name: SetFillInterval :execrows
UPDATE smtp_fills SET interval_seconds = @interval_seconds WHERE id = @id AND keep_running;

-- name: SetFillUnconfirmed :exec
UPDATE smtp_fills SET unconfirmed_folders = @folders::text[] WHERE id = @id;

-- name: CancelFill :execrows
-- Stops a running fill, or a recurring one between runs.
UPDATE smtp_fills SET status = 'cancelled', finished_at = coalesce(finished_at, now()), password_enc = NULL,
    keep_running = FALSE
WHERE id = $1 AND (status = 'running' OR keep_running);

-- name: CancelAccountFills :exec
-- For a removed account: no fill runs on or keeps running, and no
-- password stays.
UPDATE smtp_fills SET status = 'cancelled', finished_at = coalesce(finished_at, now()), password_enc = NULL,
    keep_running = FALSE, error = 'the account was removed'
WHERE account_id = $1 AND (status = 'running' OR keep_running);

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

-- name: FillImported :many
-- What the fill imported among messages with these Message-IDs: the
-- Message-ID, INTERNALDATE and size of its locations, the key of the
-- pre-check of a recurring fill. Uses messages_message_id_idx.
SELECT m.message_id::text AS message_id, l.internal_date, m.size
FROM messages m JOIN message_locations l ON l.message_sha256 = m.sha256
WHERE l.fill_id = @fill_id AND m.message_id = ANY (@ids::text[]);

-- name: LegacyFillFolders :many
-- The folders of a fill from before #112 other than the journal folder.
SELECT DISTINCT folder_id FROM smtp_fill_folders WHERE fill_id = @fill_id AND folder_id <> @journal_id;

-- name: MoveFillFolders :exec
-- After the copy: the fill's IMAP folders fill the journal folder, and
-- are scanned again from the start under the rule of a recurring fill.
UPDATE smtp_fill_folders SET folder_id = @journal_id, last_uid = 0, done = FALSE
WHERE fill_id = @fill_id AND folder_id <> @journal_id;

-- name: ConvertLegacyFillBatch :many
-- Copies the locations of a fill's own folders into the journal folder,
-- one per message and none for a message the journal folder holds, in
-- the order of the hashes after @after. The copies take the journal
-- folder's UIDVALIDITY and UIDs after @last_uid; the caller holds its row
-- lock and reserves the UIDs (AllocateUIDs).
WITH p AS (
    SELECT @journal_id::bigint AS journal_id, @uidvalidity::bigint AS uidvalidity, @last_uid::bigint AS last_uid,
           @fill_id::bigint AS fill_id, @folders::bigint[] AS folders, @after::text AS after, @max_rows::int AS max_rows
), batch AS (
    SELECT DISTINCT ON (l.message_sha256) l.message_sha256, l.flags, l.internal_date
    FROM message_locations l, p
    WHERE l.folder_id = ANY (p.folders) AND l.message_sha256 > p.after
      AND NOT EXISTS (SELECT 1 FROM message_locations j WHERE j.folder_id = p.journal_id AND j.message_sha256 = l.message_sha256)
    ORDER BY l.message_sha256, l.folder_id, l.uid
    LIMIT (SELECT max_rows FROM p)
)
INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid, flags, internal_date, fill_id)
SELECT b.message_sha256, p.journal_id, p.uidvalidity, p.last_uid + row_number() OVER (ORDER BY b.message_sha256),
       b.flags, b.internal_date, p.fill_id
FROM batch b, p
RETURNING message_sha256;
