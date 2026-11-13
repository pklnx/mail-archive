-- +goose Up

-- Attachment text per location (#539, part 2). A location shows its
-- message's attachment text in search only when attachment_text_ok is
-- true: an extraction started after the location existed (the job
-- attachment_text sets it), or, when it was inserted, its customer group
-- already had a location of the message with the flag true, or the
-- message has no attachment. Nothing else sets it, and a move carries it
-- with the location. See docs/development/architecture.md, "Attachment
-- text per location".
--
-- Every existing location keeps what it shows: the column is added with
-- the default true, which only changes the catalog (PostgreSQL 11 and
-- later), and the default then becomes false for every location written
-- afterwards, also by a process of an older version that still runs.
--
-- `migrate down` followed by `migrate up` turns every false flag true
-- (the default of the first step): for development only.
ALTER TABLE message_locations ADD COLUMN attachment_text_ok BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE message_locations ALTER COLUMN attachment_text_ok SET DEFAULT false;

-- The locations that do not show their attachment text yet: few in steady
-- state (their job is queued, running or failed). The repair
-- (store.RepairAttachmentText) and the job's start read them. Building it
-- scans message_locations once, under the ACCESS EXCLUSIVE lock of the
-- ALTER above, until the commit.
CREATE INDEX message_locations_attachment_pending_idx ON message_locations (message_sha256) WHERE NOT attachment_text_ok;

-- A running job whose rerun was asked for (EnqueueJob with
-- requeue_running): FinishJob queues it again instead of marking it done.
ALTER TABLE jobs ADD COLUMN requeue BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE jobs DROP COLUMN requeue;
DROP INDEX message_locations_attachment_pending_idx;
ALTER TABLE message_locations DROP COLUMN attachment_text_ok;
