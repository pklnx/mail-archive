-- +goose Up

-- Deletion marks per account and customer group (#539, part 1): whether a
-- message deleted by retention or an account purge is archived again
-- depends only on the accounts and groups it was deleted from, never on
-- what other groups hold or held.
--
-- One mark per deletion, written in the deleting transaction before the
-- locations go: a retention run marks every account and group that held
-- the message, a purge marks the purged account and its group for every
-- purged message, whether or not it was the last copy. The group of an
-- account is its owner's group, 0 for an account without an owner.
--
-- A tombstone (deleted_messages) that no mark references counts as a
-- mark of every group: those written before this migration, and those a
-- process of a version before 1.6.0 writes after it. So existing
-- tombstones are not copied. deleted_messages is unchanged: attestation,
-- export and verify keep reading it archive-wide.
--
-- A new, empty table: nothing large is read or locked.
CREATE TABLE deletion_marks (
    id           BIGSERIAL   PRIMARY KEY,
    sha256       CHAR(64)    NOT NULL,
    -- messages.created_at at the deletion, as in the tombstone.
    archived_at  TIMESTAMPTZ NOT NULL,
    -- The accounts and groups (0: without an owner) it was deleted from.
    account_ids  BIGINT[]    NOT NULL CHECK (cardinality(account_ids) > 0),
    group_ids    BIGINT[]    NOT NULL CHECK (cardinality(group_ids) > 0),
    run_id       BIGINT      REFERENCES deletion_runs (id) ON DELETE RESTRICT,
    purge_id     BIGINT      REFERENCES account_purges (id) ON DELETE RESTRICT,
    -- The tombstone of this deletion; NULL for a purge of a message that
    -- stays in other accounts (it gets no tombstone).
    tombstone_id BIGINT      REFERENCES deleted_messages (id) ON DELETE RESTRICT,
    marked_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((run_id IS NULL) <> (purge_id IS NULL)),
    CHECK (run_id IS NULL OR tombstone_id IS NOT NULL)
);
CREATE INDEX deletion_marks_sha_idx ON deletion_marks (sha256);
-- One mark per tombstone; also the lookup of tombstones without a mark.
CREATE UNIQUE INDEX deletion_marks_tombstone_idx ON deletion_marks (tombstone_id);

CREATE TRIGGER deletion_marks_append_only BEFORE UPDATE OR DELETE ON deletion_marks
    FOR EACH ROW EXECUTE FUNCTION refuse_change();
CREATE TRIGGER deletion_marks_no_truncate BEFORE TRUNCATE ON deletion_marks
    FOR EACH STATEMENT EXECUTE FUNCTION refuse_change();

-- +goose Down

-- Without marks every tombstone counts for every group again, as before:
-- the safer direction. Messages purged while other accounts held them have
-- no tombstone and are archived again.
DROP TABLE deletion_marks;
