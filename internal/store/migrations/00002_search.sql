-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Plain text extracted from the message body (text/plain, or text/html
-- converted to text), truncated so the tsvector stays below its 1 MB limit.
-- NULL means "not extracted yet" (see `mail-archive reindex`).
ALTER TABLE messages ADD COLUMN body_text TEXT;

-- Sort key for listings: the Date header, or the archive time if missing.
ALTER TABLE messages
    ADD COLUMN sort_at TIMESTAMPTZ GENERATED ALWAYS AS (COALESCE(sent_at, created_at)) STORED;
CREATE INDEX messages_sort_idx ON messages (sort_at DESC, sha256 DESC);

-- Full-text index. Mail is mixed German/English, so the text is indexed with
-- the simple (exact words), german and english (stemmed) configurations.
-- Weights: subject A, sender B, body C.
ALTER TABLE messages ADD COLUMN search tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('simple',  coalesce(subject, '')), 'A') ||
    setweight(to_tsvector('german',  coalesce(subject, '')), 'A') ||
    setweight(to_tsvector('english', coalesce(subject, '')), 'A') ||
    setweight(to_tsvector('simple',  coalesce(from_addr, '')), 'B') ||
    setweight(to_tsvector('simple',  coalesce(body_text, '')), 'C') ||
    setweight(to_tsvector('german',  coalesce(body_text, '')), 'C') ||
    setweight(to_tsvector('english', coalesce(body_text, '')), 'C')
) STORED;
CREATE INDEX messages_search_idx ON messages USING GIN (search);

-- Substring matches on subject and sender (ILIKE '%...%').
CREATE INDEX messages_subject_trgm_idx ON messages USING GIN (subject gin_trgm_ops);
CREATE INDEX messages_from_trgm_idx ON messages USING GIN (from_addr gin_trgm_ops);

-- +goose Down
DROP INDEX messages_from_trgm_idx;
DROP INDEX messages_subject_trgm_idx;
DROP INDEX messages_search_idx;
ALTER TABLE messages DROP COLUMN search;
DROP INDEX messages_sort_idx;
ALTER TABLE messages DROP COLUMN sort_at;
ALTER TABLE messages DROP COLUMN body_text;
DROP EXTENSION IF EXISTS pg_trgm;
