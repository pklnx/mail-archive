-- +goose Up

-- Storage usage (#397, part 1): exact instance totals, figures per customer
-- group and account, and the schema the quota parts build on. Message bytes
-- are the plaintext sizes of unique messages (messages.size), after
-- deduplication. See docs/development/architecture.md, "Storage usage".

-- A group's quota in message bytes, deduplicated within the group; NULL
-- means no limit of its own (only the instance quota applies, part 2).
ALTER TABLE customer_groups ADD COLUMN quota_bytes BIGINT CHECK (quota_bytes > 0);

-- Instance totals: one base row plus append-only deltas that the statement
-- triggers on messages below insert, one row per statement. Writers never
-- update a shared row, so they never wait for each other or for a reader.
-- The job storage_fold adds the deltas to the base every 5 minutes. A
-- total is base plus the sum of the deltas, read in one statement.
CREATE TABLE storage_counter_base (
    id       BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    messages BIGINT  NOT NULL,
    bytes    BIGINT  NOT NULL
);
CREATE TABLE storage_counter_deltas (
    id       BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    messages BIGINT NOT NULL,
    bytes    BIGINT NOT NULL
);

-- Group figures between snapshots: append-only rows the store writes in a
-- save's transaction for the messages new to the group (store.
-- recordGroupUsage). storage_fold adds them to added_* of the group's
-- usage row; usage_refresh deletes the ones its snapshot saw.
CREATE TABLE storage_group_deltas (
    id       BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    group_id BIGINT NOT NULL REFERENCES customer_groups (id) ON DELETE CASCADE,
    messages BIGINT NOT NULL,
    bytes    BIGINT NOT NULL
);
CREATE INDEX storage_group_deltas_group_idx ON storage_group_deltas (group_id);

-- The latest snapshot (usage_refresh) per group and account; only the
-- latest is kept. A group's current figure is messages/bytes plus added_*
-- plus its remaining deltas.
CREATE TABLE storage_usage_groups (
    group_id       BIGINT      PRIMARY KEY REFERENCES customer_groups (id) ON DELETE CASCADE,
    messages       BIGINT      NOT NULL,
    bytes          BIGINT      NOT NULL,
    added_messages BIGINT      NOT NULL DEFAULT 0,
    added_bytes    BIGINT      NOT NULL DEFAULT 0,
    computed_at    TIMESTAMPTZ NOT NULL
);
CREATE TABLE storage_usage_accounts (
    account_id      BIGINT      PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    messages        BIGINT      NOT NULL,
    bytes           BIGINT      NOT NULL,
    -- Bytes of the messages that no other account holds: what a purge of
    -- the account would free.
    exclusive_bytes BIGINT      NOT NULL,
    computed_at     TIMESTAMPTZ NOT NULL
);

-- One row: when the snapshot ran, the accounts without a group (no owner,
-- only before the first user), the database by relation, the difference
-- between the counters and an exact count, and the refresh request it
-- covered (storage_refresh_requests.seq as its snapshot saw it).
CREATE TABLE storage_snapshot (
    id                 BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    computed_at        TIMESTAMPTZ,
    ungrouped_messages BIGINT,
    ungrouped_bytes    BIGINT,
    database_bytes     BIGINT,
    relation_bytes     JSONB,
    counter_drift      BIGINT,
    covered_seq        BIGINT NOT NULL DEFAULT 0
);
INSERT INTO storage_snapshot DEFAULT VALUES;

-- Requests for a snapshot: every request (a move, the end of a purge, a
-- deletion run or an import, restore-dump, an admin) bumps seq in its own
-- transaction and enqueues usage_refresh. A separate row, so that the
-- snapshot's REPEATABLE READ write of storage_snapshot never meets it.
CREATE TABLE storage_refresh_requests (
    id  BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    seq BIGINT  NOT NULL DEFAULT 0
);
INSERT INTO storage_refresh_requests DEFAULT VALUES;

-- Quota states of the instance (group_id NULL) and of each group, with the
-- alert outbox of account_sync_health (parts 2, 3 and 5).
CREATE TABLE storage_states (
    id                    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    group_id              BIGINT UNIQUE NULLS NOT DISTINCT REFERENCES customer_groups (id) ON DELETE CASCADE,
    state                 TEXT        NOT NULL DEFAULT 'ok' CHECK (state IN ('ok', 'warn', 'full', 'overflow', 'disk_low')),
    state_since           TIMESTAMPTZ NOT NULL DEFAULT now(),
    event_seq             BIGINT      NOT NULL DEFAULT 0,
    notified_seq          BIGINT      NOT NULL DEFAULT 0,
    notify_attempts       INTEGER     NOT NULL DEFAULT 0,
    notify_next_at        TIMESTAMPTZ,
    notify_error          TEXT,
    notify_first_error_at TIMESTAMPTZ,
    notify_lease          BYTEA,
    notify_lease_until    TIMESTAMPTZ,
    mail_enqueued_event   TEXT,
    CHECK (state <> 'disk_low' OR group_id IS NULL)
);
INSERT INTO storage_states (group_id) VALUES (NULL);

-- +goose StatementBegin
CREATE FUNCTION storage_count_inserted() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO storage_counter_deltas (messages, bytes)
    SELECT count(*), sum(size) FROM storage_new_rows HAVING count(*) > 0;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION storage_count_deleted() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO storage_counter_deltas (messages, bytes)
    SELECT -count(*), -sum(size) FROM storage_old_rows HAVING count(*) > 0;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION storage_refuse_size_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'messages.size and messages.sha256 never change (storage counters)'
        USING ERRCODE = 'check_violation';
END
$$;
-- +goose StatementEnd

-- Writers of messages wait until the fill and the index below are done
-- (SHARE ROW EXCLUSIVE, which CREATE TRIGGER needs: readers go on). The
-- migration runs under the migration lock before serve answers requests;
-- docs/guide/operations.md, "One-time steps", gives the measured time.
LOCK TABLE messages IN SHARE ROW EXCLUSIVE MODE;

-- Statement triggers with transition tables: an INSERT … ON CONFLICT DO
-- NOTHING lists only the rows it inserted, and a rollback takes the delta
-- row back with them.
CREATE TRIGGER messages_storage_insert AFTER INSERT ON messages
    REFERENCING NEW TABLE AS storage_new_rows
    FOR EACH STATEMENT EXECUTE FUNCTION storage_count_inserted();
CREATE TRIGGER messages_storage_delete AFTER DELETE ON messages
    REFERENCING OLD TABLE AS storage_old_rows
    FOR EACH STATEMENT EXECUTE FUNCTION storage_count_deleted();
-- A message's size and hash never change; the counters rely on it. Updates
-- of other columns (the search index) do not run it.
CREATE TRIGGER messages_storage_guard BEFORE UPDATE OF size, sha256 ON messages
    FOR EACH ROW WHEN (OLD.size IS DISTINCT FROM NEW.size OR OLD.sha256 IS DISTINCT FROM NEW.sha256)
    EXECUTE FUNCTION storage_refuse_size_change();

-- The snapshot reads hashes and sizes from this index (index-only), not
-- from the message heap. The index comes first and the base is filled
-- from it by an index-only scan, so that the heap is read once. Measured
-- cold (#446): with 300,000 messages both orders read the heap from disk
-- once (the second pass finds it in the page cache); where the heap does
-- not fit in the page cache (1.5M messages, about 2.6 GB, on a 4 GB host)
-- filling first reads it twice: estimated from the measured disk rate
-- (138 MB/s), about 40 s instead of about 23 s.
CREATE INDEX messages_sha_size_idx ON messages (sha256) INCLUDE (size);
INSERT INTO storage_counter_base (messages, bytes)
SELECT count(*), coalesce(sum(size), 0) FROM messages;
-- The first snapshot is queued by the next job pool that starts (serve or
-- worker, storageSchedule), as long as none was taken.

-- +goose Down

-- Group quotas are lost (docs/guide/operations.md, "One-time steps").
DELETE FROM jobs WHERE kind IN ('usage_refresh', 'storage_fold');
DROP TRIGGER messages_storage_guard ON messages;
DROP TRIGGER messages_storage_delete ON messages;
DROP TRIGGER messages_storage_insert ON messages;
DROP FUNCTION storage_refuse_size_change();
DROP FUNCTION storage_count_deleted();
DROP FUNCTION storage_count_inserted();
DROP INDEX messages_sha_size_idx;
DROP TABLE storage_states;
DROP TABLE storage_refresh_requests;
DROP TABLE storage_snapshot;
DROP TABLE storage_usage_accounts;
DROP TABLE storage_usage_groups;
DROP TABLE storage_group_deltas;
DROP TABLE storage_counter_deltas;
DROP TABLE storage_counter_base;
ALTER TABLE customer_groups DROP COLUMN quota_bytes;
