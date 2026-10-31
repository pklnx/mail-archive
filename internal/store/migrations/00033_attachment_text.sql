-- +goose Up
-- The text of PDF and text attachments, for search (#390). The job kind
-- attachment_text fills these tables in the background (internal/archive,
-- attachments.go); nothing here reads or rewrites the rows of messages.
-- Both tables reference messages, so deleting a message (retention, an
-- account purge) deletes its texts and its state with it.
CREATE TABLE attachment_texts (
    message_sha256 CHAR(64) NOT NULL REFERENCES messages (sha256) ON DELETE CASCADE,
    -- The leaf part index, as in the parts API (/api/messages/{sha}/parts/{n}).
    part_index     INT      NOT NULL CHECK (part_index >= 0),
    filename       TEXT     NOT NULL DEFAULT '',
    status         TEXT     NOT NULL CHECK (status IN ('ok', 'truncated', 'no_text', 'encrypted', 'too_large', 'failed', 'timeout')),
    -- NULL unless ok or truncated; at most 256 KiB, which keeps each
    -- tsvector under PostgreSQL's 1 MB limit.
    body_text      TEXT     CHECK ((body_text IS NULL) = (status NOT IN ('ok', 'truncated'))),
    search tsvector GENERATED ALWAYS AS (
        to_tsvector('simple',  coalesce(body_text, '')) ||
        to_tsvector('german',  coalesce(body_text, '')) ||
        to_tsvector('english', coalesce(body_text, ''))) STORED,
    PRIMARY KEY (message_sha256, part_index)
);
CREATE INDEX attachment_texts_search_idx ON attachment_texts USING GIN (search);

-- Extraction state per message with attachments: a message is done when
-- version >= store.AttachmentIndexVersion, also when none of its
-- attachments had text or its file could not be read. The worker never
-- updates messages rows; pending messages are found through
-- messages_attachment_sort_idx and this table's primary key.
CREATE TABLE attachment_index_state (
    message_sha256 CHAR(64) PRIMARY KEY REFERENCES messages (sha256) ON DELETE CASCADE,
    version        SMALLINT NOT NULL DEFAULT 0
);

-- +goose Down
-- Only text the worker can extract again is lost.
DROP TABLE attachment_index_state;
DROP TABLE attachment_texts;
