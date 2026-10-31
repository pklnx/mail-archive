-- +goose Up
-- A generic job queue (#390): work that runs in the background, outside
-- the transaction that caused it, in the web server or a separate
-- `mail-archive worker`. A pool claims due rows with FOR UPDATE SKIP LOCKED
-- and a lease, as loss_alerts and account_sync_health do for alerts; see
-- docs/development/architecture.md, "Background jobs". Kinds are
-- registered in Go (internal/jobs).
CREATE TABLE jobs (
    id             BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind           TEXT        NOT NULL CHECK (kind ~ '^[a-z][a-z0-9_]{0,39}$'),
    -- Identifies the job within its kind (a SHA-256, 'v1'): an identifier,
    -- never message content. Enqueueing the same key again is idempotent.
    key            TEXT        NOT NULL CHECK (char_length(key) <= 200),
    -- The message a job is about, if any: deleting the message deletes its
    -- jobs.
    message_sha256 CHAR(64)    REFERENCES messages (sha256) ON DELETE CASCADE,
    -- Lower runs first: 0 for work caused by new mail, 100 for backfills.
    priority       SMALLINT    NOT NULL DEFAULT 100,
    state          TEXT        NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'running', 'done', 'failed')),
    -- Claims since the job was last enqueued, counted at the claim: a job
    -- whose process dies keeps the attempt (the crash-loop guard).
    attempts       SMALLINT    NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts   SMALLINT    NOT NULL CHECK (max_attempts BETWEEN 1 AND 100),
    run_after      TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_token    BYTEA,
    lease_until    TIMESTAMPTZ,
    -- A cursor or identifiers for the kind, never message content.
    payload        JSONB       NOT NULL DEFAULT '{}' CHECK (octet_length(payload::text) <= 1024),
    -- A short code ('lease expired', 'panic', 'db 40001'), never the error
    -- text or message content.
    last_error     TEXT        CHECK (char_length(last_error) <= 200),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at     TIMESTAMPTZ,
    finished_at    TIMESTAMPTZ,
    UNIQUE (kind, key),
    CHECK ((state = 'running') = (lease_token IS NOT NULL AND lease_until IS NOT NULL))
);
-- The claim: due rows in priority order.
CREATE INDEX jobs_due_idx ON jobs (priority, run_after, id) WHERE state = 'queued';
-- Expired leases.
CREATE INDEX jobs_lease_idx ON jobs (lease_until) WHERE state = 'running';
-- The cleanup of done rows.
CREATE INDEX jobs_done_idx ON jobs (finished_at) WHERE state = 'done';
-- The cascade when a message is deleted.
CREATE INDEX jobs_message_idx ON jobs (message_sha256) WHERE message_sha256 IS NOT NULL;

-- +goose Down
-- Only queued work is lost; whoever enqueued it finds it again.
DROP TABLE jobs;
