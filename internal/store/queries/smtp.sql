-- name: InsertSMTPAddress :execrows
INSERT INTO smtp_addresses (address, account_id) VALUES ($1, $2)
ON CONFLICT (address) DO NOTHING;

-- name: DeleteSMTPAddress :execrows
DELETE FROM smtp_addresses WHERE address = $1 AND account_id = $2;

-- name: DeleteAccountSMTPAddresses :exec
DELETE FROM smtp_addresses WHERE account_id = $1;

-- name: ListSMTPAddresses :many
SELECT address, account_id FROM smtp_addresses ORDER BY account_id, address;

-- name: AccountSMTPAddresses :many
SELECT address FROM smtp_addresses WHERE account_id = $1 ORDER BY address;

-- name: LookupSMTPAddress :one
SELECT a.id, a.enabled, (a.removed_at IS NOT NULL)::boolean AS removed
FROM smtp_addresses s JOIN accounts a ON a.id = s.account_id
WHERE s.address = $1;

-- name: ShareAccount :one
-- Blocks removal and other changes of the account until the transaction ends.
SELECT kind, enabled, (removed_at IS NOT NULL)::boolean AS removed FROM accounts WHERE id = $1 FOR SHARE;

-- name: LockFolder :one
-- Serializes the receptions of one folder: UIDs and the check for an
-- existing location must not race.
SELECT id, uidvalidity FROM folders WHERE account_id = $1 AND name = $2 FOR UPDATE;

-- name: LockFolderByID :one
-- LockFolder by ID, for a fill's batch.
SELECT id, uidvalidity, last_uid FROM folders WHERE id = $1 FOR UPDATE;

-- name: CreateSMTPFolder :one
INSERT INTO folders (account_id, name, uidvalidity) VALUES ($1, $2, $3)
RETURNING id;

-- name: FindReceivedLocation :one
-- The location of a message already received over SMTP into the folder,
-- with its envelope; uses message_locations_sha_idx. A location a fill
-- imported has no envelope and does not count.
SELECT l.id, l.uid, e.rcpt_to
FROM message_locations l
JOIN envelopes e ON e.location_id = l.id
WHERE l.message_sha256 = @sha256 AND l.folder_id = @folder_id
ORDER BY l.id
LIMIT 1;

-- name: NextUID :one
UPDATE folders SET last_uid = last_uid + 1, last_synced_at = now() WHERE id = $1
RETURNING last_uid;

-- name: AllocateUIDs :one
-- Reserves @n UIDs of a folder for a fill's batch and returns the last
-- one; the caller holds the folder row lock (LockFolder). It leaves
-- last_synced_at alone: LastReceived reads it as the last SMTP delivery.
UPDATE folders SET last_uid = last_uid + @n::bigint WHERE id = @id
RETURNING last_uid;

-- name: InsertFilledLocation :exec
-- A location a fill or an import of files added to the journal folder: no
-- ON CONFLICT, the UIDs come from AllocateUIDs under the folder row lock.
-- It names its fill or its import; the other is NULL.
INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid, flags, internal_date, fill_id, import_id)
VALUES (@message_sha256, @folder_id, @uidvalidity, @uid, @flags, @internal_date, @fill_id, @import_id);

-- name: InsertSMTPImport :one
-- A run of `import --journal` into an smtp account.
INSERT INTO smtp_imports (account_id, label, format, source)
VALUES (@account_id, @label, @format, @source)
RETURNING id, created_at;

-- name: TouchReceivedFolder :exec
UPDATE folders SET last_synced_at = now() WHERE id = $1;

-- name: InsertReceivedLocation :one
INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid, internal_date)
VALUES ($1, $2, $3, $4, $5)
RETURNING id;

-- name: InsertEnvelope :exec
INSERT INTO envelopes (location_id, mail_from, rcpt_to, rcpt_text, received_at, client_ip, helo)
VALUES (@location_id, @mail_from, @rcpt_to::text[], @rcpt_text, @received_at, @client_ip, @helo);

-- name: SetEnvelopeRecipients :exec
UPDATE envelopes SET rcpt_to = @rcpt_to::text[], rcpt_text = @rcpt_text WHERE location_id = @location_id;

-- name: LastReceived :many
-- When each SMTP account last accepted a message: its journal folder is
-- touched on every accepted transaction, and only then.
SELECT f.account_id, f.last_synced_at
FROM folders f JOIN accounts a ON a.id = f.account_id
WHERE a.kind = 'smtp' AND f.name = @folder AND f.last_synced_at IS NOT NULL;

-- name: BumpAccountVersion :execrows
UPDATE accounts SET version = version + 1, updated_at = now() WHERE id = $1;

-- name: LockBlobsShared :exec
-- The blob lock, shared, until the transaction ends: verify, which holds it
-- exclusive while it moves orphans, waits for the commit.
SELECT pg_advisory_xact_lock_shared(@class::int, 0);

-- name: TryLockBlobsShared :one
-- LockBlobsShared without waiting: false while verify, a deletion batch,
-- the sweep or a purge holds the blob lock exclusively.
SELECT pg_try_advisory_xact_lock_shared(@class::int, 0)::boolean AS locked;
