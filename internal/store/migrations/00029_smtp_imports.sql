-- +goose Up

-- Imports of files into an smtp account's journal folder (#152): one row
-- per run of `import --journal`. The locations a run added name it, so the
-- message view can show where they came from. Like a fill's, they have no
-- envelope.
CREATE TABLE smtp_imports (
    id         BIGSERIAL   PRIMARY KEY,
    account_id BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    label      TEXT        NOT NULL CHECK (label <> ''),
    format     TEXT        NOT NULL CHECK (format IN ('mbox', 'maildir', 'eml')),
    source     TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX smtp_imports_account_idx ON smtp_imports (account_id, id);

-- Nullable without a default: no table rewrite. The foreign key check and
-- the partial index read every location once, as in 00027.
ALTER TABLE message_locations ADD COLUMN import_id BIGINT REFERENCES smtp_imports (id) ON DELETE SET NULL;
-- Keeps ON DELETE SET NULL from scanning every location when an import
-- row goes (only with its account).
CREATE INDEX message_locations_import_idx ON message_locations (import_id) WHERE import_id IS NOT NULL;

-- +goose Down

-- The locations stay: without an envelope they show as imported, with the
-- neutral origin text. Only the labels are lost.
DROP INDEX message_locations_import_idx;
ALTER TABLE message_locations DROP COLUMN import_id;
DROP TABLE smtp_imports;
