-- name: GetBlobEncryption :one
SELECT * FROM blob_encryption;

-- name: GetBlobEncryptionForUpdate :one
SELECT * FROM blob_encryption FOR UPDATE;

-- name: BlobGeneration :one
-- The generation every inserting transaction compares (#392, part 2); no
-- row is encryption off.
SELECT generation FROM blob_encryption;

-- name: InsertBlobEncryption :exec
INSERT INTO blob_encryption (state, generation, keyring, keyring_id, keyring_generation, current_key)
VALUES (@state, @generation, @keyring, @keyring_id, @keyring_generation, @current_key);

-- name: SetBlobEncryptionState :execrows
UPDATE blob_encryption
SET state = @state, generation = generation + 1, cursor = '', files_done = 0, files_corrupt = 0,
    started_at = now(), finished_at = NULL, updated_at = now();

-- name: UpdateBlobKeyring :execrows
-- The reconciled keyring copy; a new current data key is a key change, so
-- it increments the generation too.
UPDATE blob_encryption
SET keyring = @keyring, keyring_id = @keyring_id, keyring_generation = @keyring_generation,
    generation = generation + CASE WHEN current_key = @current_key THEN 0 ELSE 1 END,
    current_key = @current_key, updated_at = now();

-- name: DeleteBlobEncryption :execrows
DELETE FROM blob_encryption;
