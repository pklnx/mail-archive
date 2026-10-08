-- +goose Up
-- Links between replies and their originals. in_reply_to is the first ID
-- of In-Reply-To, reference_ids the References header in order, both
-- without angle brackets like message_id. thread_id is the first
-- reference, else in_reply_to, else message_id. The columns are nullable or
-- have a constant default, so the table is not rewritten; reindex fills
-- them for existing messages (index_version 3).
ALTER TABLE messages ADD COLUMN in_reply_to TEXT;
ALTER TABLE messages ADD COLUMN reference_ids TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE messages ADD COLUMN thread_id TEXT;

-- The thread key COALESCE(thread_id, sha256) groups messages: a message
-- without any ID is its own thread. Queries use the same expression, for
-- the members of a thread, the newest one per thread and keyset paging.
CREATE INDEX messages_thread_idx ON messages ((COALESCE(thread_id, sha256)), sort_at DESC, sha256 DESC);
-- Direct links in the message view: the parent and the replies.
CREATE INDEX messages_in_reply_to_idx ON messages (in_reply_to);
CREATE INDEX messages_reference_ids_idx ON messages USING GIN (reference_ids);

-- +goose Down
-- Only data that reindex can recompute is lost. Rows extracted with the
-- conversation fields go back to the version before them, so that reindex
-- fills the fields again when this migration is applied again. Only
-- index_version is set, so the full-text column is not recomputed.
UPDATE messages SET index_version = 2 WHERE index_version > 2;
DROP INDEX messages_reference_ids_idx;
DROP INDEX messages_in_reply_to_idx;
DROP INDEX messages_thread_idx;
ALTER TABLE messages DROP COLUMN thread_id;
ALTER TABLE messages DROP COLUMN reference_ids;
ALTER TABLE messages DROP COLUMN in_reply_to;
