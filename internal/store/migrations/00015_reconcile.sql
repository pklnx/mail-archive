-- +goose Up
-- Reconcile: which archived messages are no longer on the server.
-- NULL means present, or unknown for folders that were never reconciled.
ALTER TABLE message_locations ADD COLUMN gone_at TIMESTAMPTZ;
-- Gone locations are few; counts and the "only in archive" filter read them
-- from this index instead of every location.
CREATE INDEX message_locations_gone_idx ON message_locations (folder_id) WHERE gone_at IS NOT NULL;
-- When the folder was last compared with the server. Locations still there
-- were seen at that time; their last_seen_at is not rewritten each run.
ALTER TABLE folders ADD COLUMN last_reconciled_at TIMESTAMPTZ;

ALTER TABLE sync_runs ADD COLUMN reconciled_folders INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_runs ADD COLUMN locations_gone INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_runs ADD COLUMN locations_back INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_runs ADD COLUMN flags_changed INTEGER NOT NULL DEFAULT 0;

-- +goose Down
-- Loses which messages are no longer on the server; the next reconcile
-- after migrating up again finds them again.
ALTER TABLE sync_runs DROP COLUMN flags_changed;
ALTER TABLE sync_runs DROP COLUMN locations_back;
ALTER TABLE sync_runs DROP COLUMN locations_gone;
ALTER TABLE sync_runs DROP COLUMN reconciled_folders;
ALTER TABLE folders DROP COLUMN last_reconciled_at;
DROP INDEX message_locations_gone_idx;
ALTER TABLE message_locations DROP COLUMN gone_at;
