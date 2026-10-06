-- +goose Up
-- A removed account keeps its folders and message locations, so its mail
-- stays in the archive. Its password is wiped and it is never synced again.
ALTER TABLE accounts ADD COLUMN removed_at TIMESTAMPTZ;

-- +goose Down
-- Removed accounts would become active again; drop them only if they hold no
-- data, otherwise refuse (goose then keeps the column).
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM accounts WHERE removed_at IS NOT NULL) THEN
        RAISE EXCEPTION 'removed accounts exist; delete them before rolling back';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE accounts DROP COLUMN removed_at;
