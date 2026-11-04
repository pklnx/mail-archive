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
-- the keyring file as the database copy. A row written meanwhile by
-- another writer or an encryption command stays (its generation decides);
-- an off row only gets the copy updated, without a new generation (the
-- form of new files does not change).
INSERT INTO blob_encryption (state, generation, keyring, keyring_id, keyring_generation, current_key)
VALUES ('off', 1, @keyring, @keyring_id, @keyring_generation, @current_key)
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
