-- +goose Up
-- Encryption at rest of the message files (#392, part 2). At most one row;
-- no row, or state 'off', means encryption is off. state is the target the
-- message files are in or are being converted to: 'off' (every new file is
-- written in plaintext), 'on' (every new file is written encrypted), or one
-- of the transitional states of an offline conversion (part 3: encrypting,
-- decrypting; part 5: rekeying), during which no writer starts. generation
-- increases with every state or data key change; a writer reads it at
-- start and every transaction that inserts messages compares it, so a
-- writer that missed a change stops instead of writing in the old form.
-- Turning encryption off keeps the row with state 'off', so the generation
-- never repeats (disable and enable again cannot bring back a generation a
-- stale writer pinned).
--
-- A writer that finds no row but a keyring file derives the state from the
-- files (part 2, revision 1, B2) and records the result, 'off' with the
-- file as the database copy when no encrypted file exists, so later starts
-- do not walk messages/ again. With state 'off' the keyring columns hold
-- the copy last recorded; a keyring file that differs from it (id or
-- generation) makes the next writer derive again.
--
-- keyring is the database copy of <data dir>/keys/keyring, as written by
-- blobcrypt (wrapped under the archive keys, so safe in a dump); the
-- writers reconcile the two copies at start (the newer generation decides
-- the current key and the slots, the data keys are the union).
-- keyring_id, keyring_generation and current_key repeat its unauthenticated
-- metadata for `status` and for operators reading the table; the copy
-- itself is authoritative.
--
-- cursor, files_done and files_corrupt are the progress of an offline
-- conversion (part 3): the last SHA-256 of the completed batches.
CREATE TABLE blob_encryption (
    id                 BOOLEAN     PRIMARY KEY DEFAULT TRUE CHECK (id),
    state              TEXT        NOT NULL CHECK (state IN ('off', 'encrypting', 'on', 'decrypting', 'rekeying')),
    generation         BIGINT      NOT NULL CHECK (generation > 0),
    keyring            BYTEA       NOT NULL,
    keyring_id         CHAR(32)    NOT NULL,
    keyring_generation BIGINT      NOT NULL CHECK (keyring_generation > 0),
    current_key        BIGINT      NOT NULL CHECK (current_key BETWEEN 1 AND 4294967295),
    cursor             TEXT        NOT NULL DEFAULT '',
    files_done         BIGINT      NOT NULL DEFAULT 0,
    files_corrupt      BIGINT      NOT NULL DEFAULT 0,
    started_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at        TIMESTAMPTZ,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
-- Older versions cannot read encrypted message files: decrypt first. A row
-- with state 'off' (all files plaintext) does not block.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM blob_encryption WHERE state <> 'off') THEN
        RAISE EXCEPTION 'encryption at rest is on or being changed; run `encryption disable` first';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE blob_encryption;
