-- +goose Up
-- Alerts about messages deleted on the server in bulk: one row per sync run
-- whose reconcile found that many of an account's messages are now only in
-- the archive. An outbox: the run writes the row, the notifier sends it.
-- account_sync_health holds a state per account; these are events, so they
-- get their own rows.
CREATE TABLE loss_alerts (
    id                    BIGSERIAL   PRIMARY KEY,
    account_id            BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    sync_run_id           BIGINT      NOT NULL UNIQUE REFERENCES sync_runs (id) ON DELETE CASCADE,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Messages that lost their last present location in the account in
    -- this run, and how many were present before it.
    lost                  INTEGER     NOT NULL CHECK (lost > 0),
    present_before        INTEGER     NOT NULL CHECK (present_before >= lost),
    -- The folders where those messages went gone, most first; the rest is
    -- counted in more_folders.
    folder_names          TEXT[]      NOT NULL DEFAULT '{}',
    folder_lost           INTEGER[]   NOT NULL DEFAULT '{}',
    more_folders          INTEGER     NOT NULL DEFAULT 0,
    -- Delivery, as in account_sync_health.
    notify_attempts       INTEGER     NOT NULL DEFAULT 0 CHECK (notify_attempts >= 0),
    notify_next_at        TIMESTAMPTZ,
    notify_error          TEXT,
    notify_lease          BYTEA,
    notify_lease_until    TIMESTAMPTZ,
    sent_at               TIMESTAMPTZ,
    given_up_at           TIMESTAMPTZ,
    CHECK (cardinality(folder_names) = cardinality(folder_lost))
);

-- Only unsent alerts are looked up; sent and expired ones leave the index.
CREATE INDEX loss_alerts_pending_idx ON loss_alerts (created_at) WHERE sent_at IS NULL AND given_up_at IS NULL;

-- +goose Down
DROP TABLE loss_alerts;
