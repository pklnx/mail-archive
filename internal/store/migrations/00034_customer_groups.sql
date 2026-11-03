-- +goose Up

-- Customer groups (#470): one shared instance serves several customers.
-- Every user belongs to exactly one group; an account belongs to its
-- owner's group (there is no accounts.group_id, so the two can never
-- disagree). Shares stay inside a group (#476).

-- Every audit entry from now on records its group at write time: the group
-- of the user or account it is about, or of the actor for their own
-- actions, and none for instance-level work. No foreign key, like user_id:
-- groups may go, the entries stay. Older entries keep NULL (the log is
-- append-only, 00018). The column and its index come first and survive a
-- Down (see there), hence IF NOT EXISTS. ADD COLUMN without a default
-- changes only the catalog; the partial index stays empty while every row
-- is NULL, but it is built per partition under a SHARE lock, so audit
-- writes wait until it is done (as for 00031's index).
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS group_id BIGINT;
CREATE INDEX IF NOT EXISTS audit_log_group_idx ON audit_log (group_id, at, id) WHERE group_id IS NOT NULL;

CREATE TABLE customer_groups (
    id         BIGSERIAL   PRIMARY KEY,
    name       TEXT        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100 AND name = btrim(name)),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX customer_groups_name_key ON customer_groups (lower(name));

-- A re-Up after a Down must never give a new group an id that the kept
-- audit column still holds for an earlier, different group: start the
-- sequence above the largest one (found through the partial index). On a
-- first Up the column is empty and the ids start at 1.
SELECT setval(pg_get_serial_sequence('customer_groups', 'id'),
              coalesce((SELECT max(group_id) FROM audit_log), 0) + 1, false);

-- Every existing user, and with them every account, goes into one group.
-- All existing shares are then inside it and stay valid.
INSERT INTO customer_groups (name) VALUES ('Default');
ALTER TABLE users ADD COLUMN group_id BIGINT REFERENCES customer_groups (id) ON DELETE RESTRICT;
UPDATE users SET group_id = (SELECT id FROM customer_groups);
-- users has a few rows per customer, all filled by the UPDATE above, and
-- ./ma migrate runs before the server starts (review gate: accepted).
-- squawk-ignore adding-not-nullable-field
ALTER TABLE users ALTER COLUMN group_id SET NOT NULL;
CREATE INDEX users_group_idx ON users (group_id);

-- No share crosses groups, whatever writes it. A new or changed share is
-- checked at once, with the owner's and the reader's rows locked FOR SHARE
-- (in id order) until the commit: a concurrent group change of either
-- waits for the share, and a group change not yet committed makes the
-- share wait and then see the new group. A user's group change and an
-- account's owner change are checked at commit, so the transaction that
-- makes them can first end the shares that would cross (store.SetUserGroup,
-- SetAccountOwner).
-- +goose StatementBegin
CREATE FUNCTION account_shares_same_group() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    owner        BIGINT;
    owner_group  BIGINT;
    reader_group BIGINT;
BEGIN
    SELECT owner_id INTO owner FROM accounts WHERE id = NEW.account_id;
    PERFORM 1 FROM users WHERE id IN (owner, NEW.user_id) ORDER BY id FOR SHARE;
    SELECT group_id INTO reader_group FROM users WHERE id = NEW.user_id;
    IF NOT FOUND THEN
        RETURN NEW; -- the foreign key refuses it
    END IF;
    SELECT group_id INTO owner_group FROM users WHERE id = owner;
    IF owner_group IS DISTINCT FROM reader_group THEN
        RAISE EXCEPTION 'account % and user % are not in the same customer group', NEW.account_id, NEW.user_id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER account_shares_same_group BEFORE INSERT OR UPDATE ON account_shares
    FOR EACH ROW EXECUTE FUNCTION account_shares_same_group();

-- +goose StatementBegin
CREATE FUNCTION users_shares_same_group() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM account_shares s
        JOIN accounts a ON a.id = s.account_id
        LEFT JOIN users o ON o.id = a.owner_id
        JOIN users r ON r.id = s.user_id
        WHERE (s.user_id = NEW.id OR a.owner_id = NEW.id)
          AND o.group_id IS DISTINCT FROM r.group_id
    ) THEN
        RAISE EXCEPTION 'a share of user % would cross customer groups', NEW.id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION accounts_shares_same_group() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM account_shares s
        LEFT JOIN users o ON o.id = NEW.owner_id
        JOIN users r ON r.id = s.user_id
        WHERE s.account_id = NEW.id
          AND o.group_id IS DISTINCT FROM r.group_id
    ) THEN
        RAISE EXCEPTION 'a share of account % would cross customer groups', NEW.id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER users_shares_same_group AFTER UPDATE OF group_id ON users
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
    WHEN (OLD.group_id IS DISTINCT FROM NEW.group_id)
    EXECUTE FUNCTION users_shares_same_group();
CREATE CONSTRAINT TRIGGER accounts_shares_same_group AFTER UPDATE OF owner_id ON accounts
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
    WHEN (OLD.owner_id IS DISTINCT FROM NEW.owner_id)
    EXECUTE FUNCTION accounts_shares_same_group();

-- +goose Down
-- A rollback must never merge customers silently and reopen shares across
-- them: the operator moves everyone into one group first.
-- +goose StatementBegin
DO $$
BEGIN
    IF (SELECT count(*) FROM customer_groups) > 1 THEN
        RAISE EXCEPTION 'several customer groups exist; move every user into one group before rolling back';
    END IF;
END
$$;
-- +goose StatementEnd
DROP TRIGGER accounts_shares_same_group ON accounts;
DROP TRIGGER users_shares_same_group ON users;
DROP TRIGGER account_shares_same_group ON account_shares;
DROP FUNCTION accounts_shares_same_group();
DROP FUNCTION users_shares_same_group();
DROP FUNCTION account_shares_same_group();
ALTER TABLE users DROP COLUMN group_id;
DROP TABLE customer_groups;
-- audit_log.group_id and audit_log_group_idx stay: the log is append-only,
-- and a rollback does not rewrite the record. A later Up keeps them (IF NOT
-- EXISTS) and starts the new group ids above every id they hold.
