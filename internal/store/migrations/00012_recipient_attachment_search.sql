-- +goose Up
-- Recipients and attachment names for search filters (to:, attachment:,
-- has:attachment). They are not part of the full-text vector: searching
-- one's own address would otherwise match nearly every received message.
-- Constant defaults change only the catalog, so the table is not rewritten.
ALTER TABLE messages ADD COLUMN to_addr TEXT;
ALTER TABLE messages ADD COLUMN cc_addr TEXT;
-- File names joined by newlines.
ALTER TABLE messages ADD COLUMN attachment_names TEXT;
ALTER TABLE messages ADD COLUMN has_attachment BOOLEAN NOT NULL DEFAULT false;
-- Version of the extraction that filled body_text and the columns above.
-- Rows below the current version are filled by `mail-archive reindex`.
ALTER TABLE messages ADD COLUMN index_version SMALLINT NOT NULL DEFAULT 0;

-- Substring matches (ILIKE '%...%'). The recipient index covers To and Cc
-- in one expression; the search query uses the same expression.
CREATE INDEX messages_rcpt_trgm_idx ON messages
    USING GIN ((coalesce(to_addr, '') || E'\n' || coalesce(cc_addr, '')) gin_trgm_ops);
CREATE INDEX messages_attachment_trgm_idx ON messages USING GIN (attachment_names gin_trgm_ops);
-- has:attachment without a query walks this instead of the whole table.
CREATE INDEX messages_attachment_sort_idx ON messages (sort_at DESC, sha256 DESC) WHERE has_attachment;

-- +goose Down
-- Only data that reindex can recompute is lost.
DROP INDEX messages_attachment_sort_idx;
DROP INDEX messages_attachment_trgm_idx;
DROP INDEX messages_rcpt_trgm_idx;
ALTER TABLE messages DROP COLUMN index_version;
ALTER TABLE messages DROP COLUMN has_attachment;
ALTER TABLE messages DROP COLUMN attachment_names;
ALTER TABLE messages DROP COLUMN cc_addr;
ALTER TABLE messages DROP COLUMN to_addr;
