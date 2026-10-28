-- +goose Up
-- A volatile default rewrites every row.
ALTER TABLE items ADD COLUMN token UUID NOT NULL DEFAULT gen_random_uuid(); -- want adding-field-with-default
-- now() is stable, not volatile: no rewrite, no finding.
ALTER TABLE items ADD COLUMN seen TIMESTAMPTZ DEFAULT now();
ALTER TABLE items DROP COLUMN legacy; -- want ban-drop-column
CREATE TABLE item_tags (
    item_id BIGINT NOT NULL REFERENCES items (id),
    tag TEXT NOT NULL
);

-- +goose Down
-- Down sections are not checked: dropping is their job.
DROP TABLE item_tags;
ALTER TABLE items ADD COLUMN legacy TEXT;
ALTER TABLE items DROP COLUMN seen;
ALTER TABLE items DROP COLUMN token;
