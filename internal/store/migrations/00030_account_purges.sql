-- +goose Up

-- Deleting a removed account completely (#155): a purge is confirmed with
-- the number of messages it deletes, then executed in batches like a
-- deletion run. The row is the record of the purge and outlives the
-- account, so it has no foreign key to it.
CREATE TABLE account_purges (
    id              BIGSERIAL   PRIMARY KEY,
    account_id      BIGINT      NOT NULL,
    account_name    TEXT        NOT NULL,
    account_kind    TEXT        NOT NULL,
    -- The account's version and owner when the purge was confirmed. A
    -- batch refuses to go on once either changed.
    account_version BIGINT      NOT NULL,
    owner_id        BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    owner_name      TEXT,
    state           TEXT        NOT NULL DEFAULT 'confirmed' CHECK (state IN ('confirmed', 'done', 'cancelled')),
    -- Messages to delete (their last location is in the account), as
    -- confirmed by the admin.
    expected        INTEGER     NOT NULL CHECK (expected >= 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by      BIGINT      REFERENCES users (id) ON DELETE SET NULL,  -- NULL: CLI
    finished_at     TIMESTAMPTZ,
    locations       BIGINT      NOT NULL DEFAULT 0,
    deleted         INTEGER     NOT NULL DEFAULT 0,
    kept            INTEGER     NOT NULL DEFAULT 0,
    note            TEXT,       -- why a purge was cancelled
    CHECK ((state = 'confirmed') = (finished_at IS NULL))
);
-- At most one open purge: purges are executed one at a time, so the
-- number of messages a confirmed purge deletes can only shrink.
CREATE UNIQUE INDEX account_purges_open_idx ON account_purges ((true)) WHERE state = 'confirmed';
CREATE INDEX account_purges_account_idx ON account_purges (account_id);
CREATE TRIGGER account_purges_no_delete BEFORE DELETE ON account_purges
    FOR EACH ROW EXECUTE FUNCTION refuse_change();
CREATE TRIGGER account_purges_no_truncate BEFORE TRUNCATE ON account_purges
    FOR EACH STATEMENT EXECUTE FUNCTION refuse_change();

-- A tombstone comes from a deletion run (with the end of its retention
-- period) or from a purge (no rule, so no end).
ALTER TABLE deleted_messages
    ALTER COLUMN run_id DROP NOT NULL,
    ALTER COLUMN retain_until DROP NOT NULL,
    ADD COLUMN purge_id BIGINT REFERENCES account_purges (id) ON DELETE RESTRICT,
    ADD CONSTRAINT deleted_messages_source_check CHECK ((run_id IS NULL) <> (purge_id IS NULL)),
    ADD CONSTRAINT deleted_messages_retain_check CHECK (run_id IS NULL OR retain_until IS NOT NULL);

ALTER TABLE message_deletions
    DROP CONSTRAINT message_deletions_reason_check,
    ADD CONSTRAINT message_deletions_reason_check CHECK (reason IN ('retention', 'account_purge'));

-- The active holds that cover a location: location_held's conditions,
-- returning the holds, so that a purge can name them. location_held is
-- now defined by it, so the two cannot differ.
-- +goose StatementBegin
CREATE FUNCTION location_holds(sha256 bpchar, folder_id bigint, sent_at timestamptz, internal_date timestamptz)
RETURNS SETOF bigint LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT h.id
    FROM folders f
    JOIN accounts a ON a.id = f.account_id
    JOIN legal_holds h ON h.released_at IS NULL
    WHERE f.id = $2
      AND (h.owner_id IS NULL OR h.owner_id = a.owner_id)
      AND (h.account_id IS NULL OR h.account_id = a.id)
      AND (h.period_start IS NULL OR coalesce(GREATEST($3, $4) >= h.period_start, true))
      AND (h.period_end IS NULL OR coalesce(LEAST($3, $4) < h.period_end, true))
      AND (h.filter IS NULL OR h.matched_at IS NULL OR $1 IS NULL
           OR EXISTS (SELECT 1 FROM legal_hold_messages hm
                      WHERE hm.hold_id = h.id AND hm.message_sha256 = $1))
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION location_held(sha256 bpchar, folder_id bigint, sent_at timestamptz, internal_date timestamptz)
RETURNS boolean LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT EXISTS (SELECT 1 FROM location_holds($1, $2, $3, $4))
$$;
-- +goose StatementEnd

-- A held location is never deleted. A location still retained only by a
-- purge: the transaction names the account in mail_archive.purge_account
-- (set_config, transaction-local), the account is removed, has a
-- confirmed purge and no rule covers the location ('infinity'). The
-- setting is compared as text: once set on a connection, it reads as ''
-- there after the transaction, which must neither fail nor match.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION message_locations_retention_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    m       messages%ROWTYPE;
    purging text := nullif(current_setting('mail_archive.purge_account', true), '');
    until   timestamptz;
BEGIN
    SELECT * INTO STRICT m FROM messages WHERE sha256 = OLD.message_sha256;
    IF location_held(OLD.message_sha256, OLD.folder_id, m.sent_at, OLD.internal_date) THEN
        RAISE EXCEPTION 'location % of message % is still retained or on legal hold', OLD.id, OLD.message_sha256
            USING ERRCODE = 'restrict_violation';
    END IF;
    until := location_retain_until(OLD.folder_id, m.sent_at, OLD.internal_date, m.created_at);
    IF until > now() AND NOT (
        purging IS NOT NULL
        AND until = 'infinity'
        AND EXISTS (SELECT 1 FROM folders f JOIN accounts a ON a.id = f.account_id
                    WHERE f.id = OLD.folder_id AND a.id::text = purging AND a.removed_at IS NOT NULL)
        AND EXISTS (SELECT 1 FROM account_purges p
                    WHERE p.account_id::text = purging AND p.state = 'confirmed'))
    THEN
        RAISE EXCEPTION 'location % of message % is still retained or on legal hold', OLD.id, OLD.message_sha256
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN OLD;
END $$;
-- +goose StatementEnd

-- +goose Down

-- Purges are part of the record of deletions; refuse to drop them (goose
-- then keeps everything).
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM account_purges)
       OR EXISTS (SELECT 1 FROM deleted_messages WHERE purge_id IS NOT NULL)
       OR EXISTS (SELECT 1 FROM message_deletions WHERE reason = 'account_purge') THEN
        RAISE EXCEPTION 'purged accounts are recorded; rolling back would delete their record';
    END IF;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION message_locations_retention_guard() RETURNS trigger LANGUAGE plpgsql AS $$
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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION location_held(sha256 bpchar, folder_id bigint, sent_at timestamptz, internal_date timestamptz)
RETURNS boolean LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT EXISTS (
        SELECT 1
        FROM folders f
        JOIN accounts a ON a.id = f.account_id
        JOIN legal_holds h ON h.released_at IS NULL
        WHERE f.id = $2
          AND (h.owner_id IS NULL OR h.owner_id = a.owner_id)
          AND (h.account_id IS NULL OR h.account_id = a.id)
          AND (h.period_start IS NULL OR coalesce(GREATEST($3, $4) >= h.period_start, true))
          AND (h.period_end IS NULL OR coalesce(LEAST($3, $4) < h.period_end, true))
          AND (h.filter IS NULL OR h.matched_at IS NULL OR $1 IS NULL
               OR EXISTS (SELECT 1 FROM legal_hold_messages hm
                          WHERE hm.hold_id = h.id AND hm.message_sha256 = $1)))
$$;
-- +goose StatementEnd
DROP FUNCTION location_holds(bpchar, bigint, timestamptz, timestamptz);

ALTER TABLE message_deletions
    DROP CONSTRAINT message_deletions_reason_check,
    ADD CONSTRAINT message_deletions_reason_check CHECK (reason IN ('retention'));

ALTER TABLE deleted_messages
    DROP CONSTRAINT deleted_messages_retain_check,
    DROP CONSTRAINT deleted_messages_source_check,
    DROP COLUMN purge_id,
    ALTER COLUMN run_id SET NOT NULL,
    ALTER COLUMN retain_until SET NOT NULL;

DROP TABLE account_purges;
