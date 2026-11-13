-- +goose Up
-- Codes tried on a 2FA login challenge (GHSA-w6jg-ffvw-q4f5, #6). Each code
-- reserves an attempt in its own statement before it is checked, so the
-- count stays when the login transaction rolls back for a wrong code, and
-- parallel requests cannot check more than the limit (3) on one challenge.
-- A constant default: PostgreSQL 11 and later change only the catalog, so
-- the table is not rewritten.
ALTER TABLE two_factor_challenges ADD COLUMN attempts SMALLINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE two_factor_challenges DROP COLUMN attempts;
