-- +goose Up
-- Every account belongs to a user, who alone sees it and the mail found in
-- it. Existing accounts go to the oldest admin (or the oldest user). Without
-- any user they stay without owner until the first user is created, who then
-- gets them (see store.CreateUser).
ALTER TABLE accounts ADD COLUMN owner_id BIGINT REFERENCES users (id) ON DELETE RESTRICT;
UPDATE accounts
SET owner_id = (SELECT id FROM users ORDER BY is_admin DESC, id LIMIT 1);

-- Account names are unique per owner. NULLS NOT DISTINCT also keeps names
-- of accounts without owner unique.
ALTER TABLE accounts DROP CONSTRAINT accounts_name_key;
ALTER TABLE accounts ADD CONSTRAINT accounts_owner_name_key UNIQUE NULLS NOT DISTINCT (owner_id, name);

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT name FROM accounts GROUP BY name HAVING count(*) > 1) THEN
        RAISE EXCEPTION 'several users have accounts with the same name; rename them before rolling back';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE accounts DROP CONSTRAINT accounts_owner_name_key;
ALTER TABLE accounts ADD CONSTRAINT accounts_name_key UNIQUE (name);
ALTER TABLE accounts DROP COLUMN owner_id;
