-- +goose Up

-- Folder counts count distinct messages per folder (a message with an old
-- and a new location after a UIDVALIDITY change counts once). This index
-- lets PostgreSQL read each folder's hashes in order from the index alone.
CREATE INDEX message_locations_folder_sha_idx ON message_locations (folder_id, message_sha256);

-- +goose Down

DROP INDEX message_locations_folder_sha_idx;
