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

-- name: CreateSMTPFolder :one
INSERT INTO folders (account_id, name, uidvalidity) VALUES ($1, $2, $3)
RETURNING id;

-- name: FindReceivedLocation :one
-- The location of a message already received into the folder, with its
-- envelope; uses message_locations_sha_idx.
SELECT l.id, l.uid, e.rcpt_to
FROM message_locations l
LEFT JOIN envelopes e ON e.location_id = l.id
WHERE l.message_sha256 = @sha256 AND l.folder_id = @folder_id
ORDER BY l.id
LIMIT 1;

-- name: NextUID :one
UPDATE folders SET last_uid = last_uid + 1, last_synced_at = now() WHERE id = $1
RETURNING last_uid;

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
-- When each SMTP account last accepted a message: its only folder is
-- touched on every accepted transaction.
SELECT f.account_id, f.last_synced_at
FROM folders f JOIN accounts a ON a.id = f.account_id
WHERE a.kind = 'smtp' AND f.last_synced_at IS NOT NULL;

-- name: BumpAccountVersion :execrows
UPDATE accounts SET version = version + 1, updated_at = now() WHERE id = $1;

-- name: OwnsSMTPAccount :one
-- Whether the user has an smtp account, removed ones included: only then
-- can a to: filter match envelopes.
SELECT EXISTS (SELECT 1 FROM accounts WHERE owner_id = @owner::bigint AND kind = 'smtp');

-- name: LockBlobsShared :exec
-- The blob lock, shared, until the transaction ends: verify, which holds it
-- exclusive while it moves orphans, waits for the commit.
SELECT pg_advisory_xact_lock_shared(@class::int, 0);
