-- +goose Up

-- Web UI settings of each user (#90). language picks the UI texts, locale
-- the formats of dates and numbers. 'auto' follows the browser. The UI
-- treats a value it cannot use like 'auto', so a new UI language or format
-- needs no migration.
ALTER TABLE users
    ADD COLUMN language TEXT NOT NULL DEFAULT 'auto'
        CHECK (language = 'auto' OR (char_length(language) <= 35 AND language ~ '^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$')),
    ADD COLUMN locale TEXT NOT NULL DEFAULT 'auto'
        CHECK (locale = 'auto' OR (char_length(locale) <= 35 AND locale ~ '^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8})*$'));

-- +goose Down

ALTER TABLE users DROP COLUMN locale, DROP COLUMN language;
