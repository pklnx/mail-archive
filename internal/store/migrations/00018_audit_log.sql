-- +goose Up
-- User agents are stored once and referenced from audit_log.
CREATE TABLE audit_user_agents (
    id         BIGSERIAL PRIMARY KEY,
    user_agent TEXT NOT NULL UNIQUE CHECK (user_agent <> '' AND length(user_agent) <= 256)
);

-- The audit log: who did what, when, from where. Append-only (see the
-- triggers below). user_id, api_key_id and target_id have no foreign keys:
-- users, keys and accounts are deleted, their log rows stay. The names are
-- kept as they were at the time. at comes from the database clock, like the
-- partition bounds.
CREATE TABLE audit_log (
    id            BIGSERIAL   NOT NULL,
    at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor         TEXT        NOT NULL CHECK (actor IN ('session', 'api_key', 'cli')),
    user_id       BIGINT,
    user_name     TEXT,
    api_key_id    BIGINT,
    api_key_name  TEXT,
    action        TEXT        NOT NULL CHECK (action ~ '^[a-z0-9_]+\.[a-z0-9_]+$'),
    target_type   TEXT        CHECK (target_type IN ('message', 'account', 'user')),
    target_id     TEXT,
    target_name   TEXT,
    client_ip     INET,
    user_agent_id BIGINT      REFERENCES audit_user_agents (id),
    details       JSONB       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(details) = 'object'),
    PRIMARY KEY (at, id),
    CHECK ((target_type IS NULL) = (target_id IS NULL)),
    CHECK (target_type <> 'message' OR target_id ~ '^[0-9a-f]{64}$'),
    CHECK ((actor = 'api_key') = (api_key_id IS NOT NULL)),
    CHECK (actor <> 'cli' OR (client_ip IS NULL AND user_agent_id IS NULL))
) PARTITION BY RANGE (at);

-- A user's own entries, newest first.
CREATE INDEX audit_log_user_idx ON audit_log (user_id, at, id);

-- +goose StatementBegin
CREATE FUNCTION audit_refuse_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only: % refused', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'insufficient_privilege';
END $$;
-- +goose StatementEnd

-- Row triggers on a partitioned table are cloned to every partition, also
-- to later ones; TRUNCATE triggers are not (see audit_log_ensure_partitions).
CREATE TRIGGER audit_log_append_only BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_refuse_change();
CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_refuse_change();
CREATE TRIGGER audit_user_agents_append_only BEFORE UPDATE OR DELETE ON audit_user_agents
    FOR EACH ROW EXECUTE FUNCTION audit_refuse_change();
CREATE TRIGGER audit_user_agents_no_truncate BEFORE TRUNCATE ON audit_user_agents
    FOR EACH STATEMENT EXECUTE FUNCTION audit_refuse_change();
-- Also with session_replication_role = replica.
ALTER TABLE audit_log ENABLE ALWAYS TRIGGER audit_log_append_only;
ALTER TABLE audit_log ENABLE ALWAYS TRIGGER audit_log_no_truncate;
ALTER TABLE audit_user_agents ENABLE ALWAYS TRIGGER audit_user_agents_append_only;
ALTER TABLE audit_user_agents ENABLE ALWAYS TRIGGER audit_user_agents_no_truncate;
-- The owner gives up its own rights (no effect for a superuser). UPDATE
-- stays on audit_user_agents: the foreign key check of audit_log needs it.
REVOKE UPDATE, DELETE, TRUNCATE ON audit_log FROM PUBLIC, CURRENT_USER;
REVOKE DELETE, TRUNCATE ON audit_user_agents FROM PUBLIC, CURRENT_USER;

-- Creates the partitions of the current month (UTC) and the next
-- months_ahead months, with the TRUNCATE trigger and the revoke that
-- partitions do not inherit. There is no default partition: a month's
-- partition could not be created once rows of that month were in it.
-- Returns how many it created.
-- +goose StatementBegin
CREATE FUNCTION audit_log_ensure_partitions(months_ahead integer) RETURNS integer
LANGUAGE plpgsql AS $$
DECLARE
    first   date := date_trunc('month', now() AT TIME ZONE 'UTC')::date;
    m       date;
    part    text;
    created integer := 0;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('audit_log_ensure_partitions'));
    FOR i IN 0..months_ahead LOOP
        m := (first + make_interval(months => i))::date;
        part := 'audit_log_' || to_char(m, 'YYYY_MM');
        CONTINUE WHEN to_regclass(part) IS NOT NULL;
        EXECUTE format('CREATE TABLE %I PARTITION OF audit_log FOR VALUES FROM (%L) TO (%L)',
            part, m::timestamp AT TIME ZONE 'UTC', (m + interval '1 month')::timestamp AT TIME ZONE 'UTC');
        EXECUTE format('CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON %I FOR EACH STATEMENT EXECUTE FUNCTION audit_refuse_change()', part);
        EXECUTE format('ALTER TABLE %I ENABLE ALWAYS TRIGGER audit_log_no_truncate', part);
        EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON %I FROM PUBLIC, CURRENT_USER', part);
        created := created + 1;
    END LOOP;
    RETURN created;
END $$;
-- +goose StatementEnd

SELECT audit_log_ensure_partitions(3);

-- +goose Down
-- The log is append-only; a rollback must not drop it silently.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM audit_log) THEN
        RAISE EXCEPTION 'the audit log has entries; it is not dropped by a rollback';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE audit_log;
DROP TABLE audit_user_agents;
DROP FUNCTION audit_log_ensure_partitions(integer);
DROP FUNCTION audit_refuse_change();
