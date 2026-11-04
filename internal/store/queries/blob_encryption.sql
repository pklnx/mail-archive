-- name: GetBlobEncryption :one
SELECT * FROM blob_encryption;

-- name: GetBlobEncryptionForUpdate :one
SELECT * FROM blob_encryption FOR UPDATE;

-- name: BlobGeneration :one
-- The generation every inserting transaction compares (#392, part 2); no
-- row is encryption off, never enabled (generation 0).
SELECT generation FROM blob_encryption;

-- name: InsertBlobEncryption :exec
INSERT INTO blob_encryption (state, generation, keyring, keyring_id, keyring_generation, current_key)
VALUES (@state, @generation, @keyring, @keyring_id, @keyring_generation, @current_key);

-- name: SetBlobEncryptionState :execrows
UPDATE blob_encryption
SET state = @state, generation = generation + 1, cursor = '', files_done = 0, files_corrupt = 0,
    started_at = now(), finished_at = NULL, updated_at = now();

-- name: EnableBlobEncryption :execrows
-- Leaves state off: the new state, with the keyring copy of the enabling
-- command; the generation continues from the off row's.
UPDATE blob_encryption
SET state = @state, generation = generation + 1, keyring = @keyring, keyring_id = @keyring_id,
    keyring_generation = @keyring_generation, current_key = @current_key,
    cursor = '', files_done = 0, files_corrupt = 0, started_at = now(), finished_at = NULL, updated_at = now()
WHERE state = 'off';

-- name: RecordBlobOff :exec
-- The state derived from the files with no encrypted file found: off, with
-- the keyring file as the database copy, at generation 0 like no row, so
-- that writers running since before it do not stop (the form of new files
-- does not change). A row written meanwhile by another writer or an
-- encryption command stays (its generation decides); an off row only gets
-- the copy updated, without a new generation.
INSERT INTO blob_encryption (state, generation, keyring, keyring_id, keyring_generation, current_key)
VALUES ('off', 0, @keyring, @keyring_id, @keyring_generation, @current_key)
ON CONFLICT (id) DO UPDATE
SET keyring = EXCLUDED.keyring, keyring_id = EXCLUDED.keyring_id,
    keyring_generation = EXCLUDED.keyring_generation, current_key = EXCLUDED.current_key, updated_at = now()
WHERE blob_encryption.state = 'off';

-- name: UpdateBlobKeyring :execrows
-- The reconciled keyring copy; a new current data key is a key change, so
-- it increments the generation too.
UPDATE blob_encryption
SET keyring = @keyring, keyring_id = @keyring_id, keyring_generation = @keyring_generation,
    generation = generation + CASE WHEN current_key = @current_key THEN 0 ELSE 1 END,
    current_key = @current_key, updated_at = now();

-- name: ClearBlobEncryption :execrows
-- Encryption off: the row stays, so the generation keeps increasing.
UPDATE blob_encryption
SET state = 'off', generation = generation + 1, cursor = '', files_done = 0, files_corrupt = 0,
    finished_at = now(), updated_at = now()
WHERE state <> 'off';

-- name: SaveBlobConversionProgress :execrows
-- The progress of an offline conversion after a batch (#392, parts 3 and 5), only
-- while the state and generation are still those the command set.
UPDATE blob_encryption
SET cursor = @cursor, files_done = @files_done, files_corrupt = @files_corrupt, updated_at = now()
WHERE generation = @generation AND state IN ('encrypting', 'decrypting', 'rekeying');

-- name: FinishBlobEncryption :execrows
-- The end of `encryption enable`: state on, the next generation; the
-- counters stay as the record of the conversion.
UPDATE blob_encryption
SET state = 'on', generation = generation + 1, cursor = '', finished_at = now(), updated_at = now()
WHERE generation = @generation AND state = 'encrypting';

-- name: FinishBlobDecryption :execrows
-- The end of `encryption disable`: state off, the next generation, as
-- ClearBlobEncryption, but only from decrypting at the generation the
-- command worked at.
UPDATE blob_encryption
SET state = 'off', generation = generation + 1, cursor = '', files_done = 0, files_corrupt = 0,
    finished_at = now(), updated_at = now()
WHERE generation = @generation AND state = 'decrypting';

-- name: StartBlobRekey :execrows
-- The start of `encryption rekey` (#392, part 5): state rekeying, the next
-- generation, and the new keyring (new current data key, new keyring key)
-- as the database copy, in one statement; only from on and only while the
-- copy is still the one the command reconciled.
UPDATE blob_encryption
SET state = 'rekeying', generation = generation + 1, keyring = @keyring, keyring_id = @keyring_id,
    keyring_generation = @keyring_generation, current_key = @current_key,
    cursor = '', files_done = 0, files_corrupt = 0, started_at = now(), finished_at = NULL, updated_at = now()
WHERE state = 'on' AND keyring_generation = @expected_keyring_generation;

-- name: FinishBlobRekey :execrows
-- The end of `encryption rekey`: state on, the next generation, and the
-- keyring without the data keys no file names any more; only from rekeying
-- at the generation the command worked at, and only while the copy is still
-- the one it read.
UPDATE blob_encryption
SET state = 'on', generation = generation + 1, keyring = @keyring, keyring_id = @keyring_id,
    keyring_generation = @keyring_generation, current_key = @current_key,
    cursor = '', finished_at = now(), updated_at = now()
WHERE generation = @generation AND state = 'rekeying' AND keyring_generation = @expected_keyring_generation;
