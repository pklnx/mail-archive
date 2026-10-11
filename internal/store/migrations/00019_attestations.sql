-- +goose Up
-- Insertion order of messages, for attestations (tamper evidence). Filled
-- for existing rows by the ALTER, which rewrites the table. CACHE 1 keeps
-- values increasing across sessions, which the attestation fence relies
-- on: a cache above 1 would let a later insert draw a smaller value.
ALTER TABLE messages ADD COLUMN seq BIGINT NOT NULL GENERATED ALWAYS AS IDENTITY (CACHE 1);
CREATE UNIQUE INDEX messages_seq_idx ON messages (seq);

-- One row per deleted message, written in the same transaction as the
-- delete (retention). message_seq and sha256 let verify rebuild old
-- attestations. Nothing writes here yet.
CREATE TABLE message_deletions (
    id          BIGINT      GENERATED ALWAYS AS IDENTITY (CACHE 1) PRIMARY KEY,
    message_seq BIGINT      NOT NULL UNIQUE,
    sha256      CHAR(64)    NOT NULL,
    reason      TEXT        NOT NULL CHECK (reason IN ('retention')),
    deleted_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX message_deletions_sha_idx ON message_deletions (sha256);

-- The signed chain. Each row covers the messages with
-- messages_after < seq <= messages_upto and the deletions with
-- deletions_after < id <= deletions_upto; leaves_sha256 is the digest of
-- the leaves file that lists them, and hash and signature are over the
-- text built from the other columns (internal/attest).
CREATE TABLE attestations (
    seq             BIGINT      PRIMARY KEY CHECK (seq >= 1),
    created_at      TIMESTAMPTZ NOT NULL,
    messages_after  BIGINT      NOT NULL,
    messages_upto   BIGINT      NOT NULL,
    messages_count  BIGINT      NOT NULL,
    deletions_after BIGINT      NOT NULL,
    deletions_upto  BIGINT      NOT NULL,
    deletions_count BIGINT      NOT NULL,
    total           BIGINT      NOT NULL,
    leaves_sha256   CHAR(64)    NOT NULL,
    prev_hash       CHAR(64)    NOT NULL,
    public_key      TEXT        NOT NULL, -- base64 of the raw 32-byte key
    hash            CHAR(64)    NOT NULL UNIQUE,
    signature       TEXT        NOT NULL, -- base64 of the 64-byte signature
    CHECK (messages_upto >= messages_after AND deletions_upto >= deletions_after)
);

-- Both tables only grow. This stops mistakes, not an attacker with owner
-- rights; verify is what detects that.
-- +goose StatementBegin
CREATE FUNCTION refuse_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME;
END $$;
-- +goose StatementEnd
CREATE TRIGGER attestations_append_only BEFORE UPDATE OR DELETE ON attestations
    FOR EACH ROW EXECUTE FUNCTION refuse_change();
CREATE TRIGGER attestations_no_truncate BEFORE TRUNCATE ON attestations
    FOR EACH STATEMENT EXECUTE FUNCTION refuse_change();
CREATE TRIGGER message_deletions_append_only BEFORE UPDATE OR DELETE ON message_deletions
    FOR EACH ROW EXECUTE FUNCTION refuse_change();
CREATE TRIGGER message_deletions_no_truncate BEFORE TRUNCATE ON message_deletions
    FOR EACH STATEMENT EXECUTE FUNCTION refuse_change();

-- +goose Down
-- Dropping seq makes existing attestations unverifiable; refuse while any
-- exist (goose then keeps everything).
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM attestations) THEN
        RAISE EXCEPTION 'attestations exist; rolling back would make them unverifiable';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE attestations;
DROP TABLE message_deletions;
DROP FUNCTION refuse_change();
DROP INDEX messages_seq_idx;
ALTER TABLE messages DROP COLUMN seq;
