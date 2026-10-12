-- +goose Up

-- Deletion by retention (#55): an admin creates a run (the candidates),
-- reviews it and confirms it; only a confirmed run deletes, and only what
-- is still deletable then.
CREATE TABLE deletion_runs (
    id           BIGSERIAL   PRIMARY KEY,
    state        TEXT        NOT NULL DEFAULT 'proposed' CHECK (state IN ('proposed', 'confirmed', 'done', 'cancelled')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by   BIGINT      REFERENCES users (id) ON DELETE SET NULL,   -- NULL: CLI
    candidates   INTEGER     NOT NULL DEFAULT 0,
    bytes        BIGINT      NOT NULL DEFAULT 0,
    confirmed_at TIMESTAMPTZ,
    confirmed_by BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    finished_at  TIMESTAMPTZ,
    deleted      INTEGER     NOT NULL DEFAULT 0,
    kept         INTEGER     NOT NULL DEFAULT 0,
    CHECK (state IN ('proposed', 'cancelled') OR confirmed_at IS NOT NULL),
    CHECK ((state IN ('done', 'cancelled')) = (finished_at IS NOT NULL))
);
-- At most one open run, so that two reviews never overlap.
CREATE UNIQUE INDEX deletion_runs_open_idx ON deletion_runs ((true)) WHERE state IN ('proposed', 'confirmed');

CREATE TABLE deletion_candidates (
    run_id         BIGINT      NOT NULL REFERENCES deletion_runs (id) ON DELETE RESTRICT,
    message_sha256 CHAR(64)    NOT NULL,
    size           BIGINT      NOT NULL,
    retain_until   TIMESTAMPTZ NOT NULL,
    state          TEXT        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'deleted', 'kept')),
    PRIMARY KEY (run_id, message_sha256)
);
CREATE INDEX deletion_candidates_pending_idx ON deletion_candidates (run_id, message_sha256) WHERE state = 'pending';

-- One row per deleted message: the record of the deletion. Append-only. It
-- keeps no subject, sender or recipients. A message archived again and
-- deleted again gets a second row.
CREATE TABLE deleted_messages (
    id           BIGSERIAL   PRIMARY KEY,
    sha256       CHAR(64)    NOT NULL,
    size         BIGINT      NOT NULL,
    message_seq  BIGINT      NOT NULL,   -- messages.seq, as in message_deletions
    archived_at  TIMESTAMPTZ NOT NULL,   -- messages.created_at, for the re-archive check
    retain_until TIMESTAMPTZ NOT NULL,
    locations    INTEGER     NOT NULL CHECK (locations > 0),
    run_id       BIGINT      NOT NULL REFERENCES deletion_runs (id) ON DELETE RESTRICT,
    deleted_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX deleted_messages_sha_idx ON deleted_messages (sha256);

-- Message files to remove once due_at has passed. The files outlive their
-- rows by a grace period, so a backup (dump first, then the files) taken
-- meanwhile has every file its dump needs.
CREATE TABLE blob_removals (
    sha256 CHAR(64)    PRIMARY KEY,
    due_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX blob_removals_due_idx ON blob_removals (due_at);

-- A location can only be deleted once its period ended and no hold covers
-- it. With message_locations' RESTRICT on messages, a message can only go
-- once every location passed this.
-- +goose StatementBegin
CREATE FUNCTION message_locations_retention_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    m messages%ROWTYPE;
BEGIN
    SELECT * INTO STRICT m FROM messages WHERE sha256 = OLD.message_sha256;
    IF location_retain_until(OLD.folder_id, m.sent_at, OLD.internal_date, m.created_at) > now()
       OR location_held(OLD.message_sha256, OLD.folder_id, m.sent_at, OLD.internal_date) THEN
        RAISE EXCEPTION 'location % of message % is still retained or on legal hold', OLD.id, OLD.message_sha256
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN OLD;
END $$;
-- +goose StatementEnd
CREATE TRIGGER message_locations_retention_guard BEFORE DELETE ON message_locations
    FOR EACH ROW EXECUTE FUNCTION message_locations_retention_guard();

-- A message row can only be deleted with its tombstone.
-- +goose StatementBegin
CREATE FUNCTION messages_tombstone_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM deleted_messages d WHERE d.sha256 = OLD.sha256 AND d.archived_at = OLD.created_at) THEN
        RAISE EXCEPTION 'message % has no deletion record', OLD.sha256 USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN OLD;
END $$;
-- +goose StatementEnd
CREATE TRIGGER messages_tombstone_guard BEFORE DELETE ON messages
    FOR EACH ROW EXECUTE FUNCTION messages_tombstone_guard();

CREATE TRIGGER deleted_messages_append_only BEFORE UPDATE OR DELETE ON deleted_messages
    FOR EACH ROW EXECUTE FUNCTION refuse_change();
CREATE TRIGGER deleted_messages_no_truncate BEFORE TRUNCATE ON deleted_messages
    FOR EACH STATEMENT EXECUTE FUNCTION refuse_change();

-- +goose Down

-- The deletion records are part of the evidence (#66); refuse to drop them
-- (goose then keeps everything).
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM deleted_messages) THEN
        RAISE EXCEPTION 'deleted messages are recorded; rolling back would delete their record';
    END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER messages_tombstone_guard ON messages;
DROP FUNCTION messages_tombstone_guard();
DROP TRIGGER message_locations_retention_guard ON message_locations;
DROP FUNCTION message_locations_retention_guard();
DROP TABLE blob_removals;
DROP TABLE deleted_messages;
DROP TABLE deletion_candidates;
DROP TABLE deletion_runs;
