-- +goose Up

-- Auditor access (#393, part 1): an auditor (a tax advisor or tax
-- auditor) is a user with a fixed role who reads only what a grant of one
-- owner opens: some of the owner's accounts, maybe only some folders and
-- a period of the mail, inside a validity window. An auditor and the
-- grant's owner are in the same customer group while the grant is not
-- revoked (#470, maintainer decision M3); the grant's group is the
-- owner's. users and sessions are small: the CHECK and the foreign key
-- validate under brief locks.

-- The role is chosen at creation and never changes (users_auditor_fixed).
-- A constant default and a nullable column: no table rewrite.
ALTER TABLE users ADD COLUMN is_auditor BOOLEAN NOT NULL DEFAULT FALSE,
    ADD CONSTRAINT users_auditor_not_admin CHECK (NOT (is_admin AND is_auditor));

-- A grant is kept as history after it ends or is revoked. Its scope
-- (owner, auditor, label, accounts, folders, period, start) never
-- changes; only ends_at (while unrevoked) and the revocation columns do.
-- auditor_id becomes NULL only when the auditor user is removed, which
-- revokes every unrevoked grant of theirs first (store.DeleteUser):
-- auditor_grants_auditor_kept refuses it on an unrevoked grant.
CREATE TABLE auditor_grants (
    id            BIGSERIAL   PRIMARY KEY,
    auditor_id    BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    owner_id      BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    label         TEXT        NOT NULL CHECK (char_length(label) BETWEEN 1 AND 200),
    -- On messages.sort_at, half-open: [period_from, period_until).
    period_from   TIMESTAMPTZ,
    period_until  TIMESTAMPTZ,
    starts_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    ends_at       TIMESTAMPTZ NOT NULL,
    revoked_at    TIMESTAMPTZ,
    revoked_by    BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    revoke_reason TEXT        CHECK (revoke_reason IN ('owner', 'operator', 'group_change', 'account_move', 'account_purge', 'user_removed')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (auditor_id <> owner_id),
    CHECK (ends_at > starts_at),
    CHECK (period_until IS NULL OR period_from IS NULL OR period_until > period_from),
    CHECK ((revoked_at IS NULL) = (revoke_reason IS NULL)),
    CONSTRAINT auditor_grants_auditor_kept CHECK (auditor_id IS NOT NULL OR revoked_at IS NOT NULL)
);
CREATE INDEX auditor_grants_auditor_idx ON auditor_grants (auditor_id);
CREATE INDEX auditor_grants_owner_idx ON auditor_grants (owner_id);

-- The granted accounts: all their folders, or those in
-- auditor_grant_folders (none at all if there is no row).
CREATE TABLE auditor_grant_accounts (
    grant_id    BIGINT  NOT NULL REFERENCES auditor_grants (id) ON DELETE CASCADE,
    account_id  BIGINT  NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    all_folders BOOLEAN NOT NULL,
    PRIMARY KEY (grant_id, account_id)
);
CREATE INDEX auditor_grant_accounts_account_idx ON auditor_grant_accounts (account_id);

-- Granted folders by ID: a folder created later, or renamed (a new row),
-- is not covered. The read side joins folders on both IDs, so a folder
-- stored under another account opens nothing.
CREATE TABLE auditor_grant_folders (
    grant_id   BIGINT NOT NULL,
    account_id BIGINT NOT NULL,
    folder_id  BIGINT NOT NULL REFERENCES folders (id) ON DELETE CASCADE,
    PRIMARY KEY (grant_id, folder_id),
    FOREIGN KEY (grant_id, account_id) REFERENCES auditor_grant_accounts (grant_id, account_id) ON DELETE CASCADE
);
CREATE INDEX auditor_grant_folders_folder_idx ON auditor_grant_folders (folder_id);

-- The grant an auditor's session reads through; NULL until the auditor
-- picks one. A removed grant takes its sessions along.
ALTER TABLE sessions ADD COLUMN grant_id BIGINT REFERENCES auditor_grants (id) ON DELETE CASCADE;
CREATE INDEX sessions_grant_idx ON sessions (grant_id) WHERE grant_id IS NOT NULL;

-- The folder restriction of a scope (#393), per account: the folder is
-- granted only if (its account, it) is one of the pairs given as two
-- parallel arrays. A folder ID listed under another account opens
-- nothing, so no narrowing can widen a scope by naming a folder under the
-- wrong account. Accounts read with all their folders are matched apart
-- (scope_all_folder_accounts). The body is a plain expression without a
-- subquery, so the planner inlines it (an EXISTS over unnest(...) would
-- not be inlined and costs a function call per location). It looks up the
-- first pair of the folder: store.Scope.folderParams lists each folder ID
-- at most once, and drops one listed under two accounts.
CREATE FUNCTION scope_folder_granted(account_id bigint, folder_id bigint, accounts bigint[], folders bigint[])
RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE
RETURN (accounts[array_position(folders, folder_id)] = account_id) IS TRUE;

-- The role never changes: a member never becomes an auditor (who then
-- would read without a grant's limits) and an auditor never a member.
-- +goose StatementBegin
CREATE FUNCTION users_auditor_fixed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'the auditor role of user % cannot change', NEW.id
        USING ERRCODE = 'check_violation';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER users_auditor_fixed BEFORE UPDATE OF is_auditor ON users
    FOR EACH ROW WHEN (OLD.is_auditor IS DISTINCT FROM NEW.is_auditor)
    EXECUTE FUNCTION users_auditor_fixed();

-- No unrevoked grant crosses groups, whatever writes it. In this order:
--  1. A revoked grant is final: only revoked_by and auditor_id may become
--     NULL (their ON DELETE SET NULL); nothing else changes.
--  2. On an unrevoked grant, owner, label, period and start never change,
--     nor does the auditor, except to NULL (left to the CHECK).
--  3. A revoking row returns at once and takes no user lock, so that a
--     group move, which locks the user first and then revokes, never waits
--     for a lock in the other order.
--  4. A row without auditor returns too: auditor_grants_auditor_kept
--     refuses it by name (coordinator decision 30).
--  5. Otherwise both users are locked FOR SHARE (in id order) until the
--     commit, and the auditor must be an auditor, the owner not, in the
--     same group. A group change not yet committed makes the grant wait
--     and then see the new group.
-- +goose StatementBegin
CREATE FUNCTION auditor_grants_same_group() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    a users%ROWTYPE;
    o users%ROWTYPE;
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.revoked_at IS NOT NULL THEN
        IF (NEW.id, NEW.owner_id, NEW.label, NEW.period_from, NEW.period_until, NEW.starts_at, NEW.ends_at,
            NEW.revoked_at, NEW.revoke_reason, NEW.created_at)
           IS NOT DISTINCT FROM
           (OLD.id, OLD.owner_id, OLD.label, OLD.period_from, OLD.period_until, OLD.starts_at, OLD.ends_at,
            OLD.revoked_at, OLD.revoke_reason, OLD.created_at)
           AND (NEW.revoked_by IS NOT DISTINCT FROM OLD.revoked_by OR NEW.revoked_by IS NULL)
           AND (NEW.auditor_id IS NOT DISTINCT FROM OLD.auditor_id OR NEW.auditor_id IS NULL) THEN
            RETURN NEW;
        END IF;
        RAISE EXCEPTION 'auditor grant % is revoked; a revocation is final', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;
    IF TG_OP = 'UPDATE' AND (
           (NEW.id, NEW.owner_id, NEW.label, NEW.period_from, NEW.period_until, NEW.starts_at, NEW.created_at)
           IS DISTINCT FROM
           (OLD.id, OLD.owner_id, OLD.label, OLD.period_from, OLD.period_until, OLD.starts_at, OLD.created_at)
           OR NEW.auditor_id IS DISTINCT FROM OLD.auditor_id AND NEW.auditor_id IS NOT NULL) THEN
        RAISE EXCEPTION 'the scope of auditor grant % cannot change', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;
    IF NEW.revoked_at IS NOT NULL OR NEW.auditor_id IS NULL THEN
        RETURN NEW;
    END IF;
    PERFORM 1 FROM users WHERE id IN (NEW.owner_id, NEW.auditor_id) ORDER BY id FOR SHARE;
    SELECT * INTO a FROM users WHERE id = NEW.auditor_id;
    SELECT * INTO o FROM users WHERE id = NEW.owner_id;
    IF a.id IS NULL OR o.id IS NULL THEN
        RETURN NEW; -- the foreign keys refuse it
    END IF;
    IF NOT a.is_auditor OR o.is_auditor OR a.group_id IS DISTINCT FROM o.group_id THEN
        RAISE EXCEPTION 'auditor grant of user % to user % crosses customer groups or roles', NEW.owner_id, NEW.auditor_id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER auditor_grants_same_group BEFORE INSERT OR UPDATE ON auditor_grants
    FOR EACH ROW EXECUTE FUNCTION auditor_grants_same_group();

-- A granted account belongs to the grant's owner and is not an smtp
-- (journal) account (maintainer decision M1). The account row stays
-- locked FOR SHARE until the commit, so an owner change waits for it and
-- is then checked at its own commit (accounts_grants_owner).
-- +goose StatementBegin
CREATE FUNCTION auditor_grant_accounts_owner() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    acc_owner BIGINT;
    acc_kind  TEXT;
BEGIN
    SELECT owner_id, kind INTO acc_owner, acc_kind FROM accounts WHERE id = NEW.account_id FOR SHARE;
    IF NOT FOUND THEN
        RETURN NEW; -- the foreign key refuses it
    END IF;
    IF acc_owner IS DISTINCT FROM (SELECT owner_id FROM auditor_grants WHERE id = NEW.grant_id) OR acc_kind = 'smtp' THEN
        RAISE EXCEPTION 'account % cannot be granted in auditor grant %', NEW.account_id, NEW.grant_id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER auditor_grant_accounts_owner BEFORE INSERT OR UPDATE ON auditor_grant_accounts
    FOR EACH ROW EXECUTE FUNCTION auditor_grant_accounts_owner();

-- A user's group change and an account's owner change are checked at
-- commit, so the transaction that makes them can first revoke or narrow
-- the grants (store.SetUserGroup, SetAccountOwner).
-- +goose StatementBegin
CREATE FUNCTION users_grants_same_group() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM auditor_grants g
        JOIN users a ON a.id = g.auditor_id
        JOIN users o ON o.id = g.owner_id
        WHERE g.revoked_at IS NULL
          AND (g.auditor_id = NEW.id OR g.owner_id = NEW.id)
          AND a.group_id IS DISTINCT FROM o.group_id
    ) THEN
        RAISE EXCEPTION 'an auditor grant of user % would cross customer groups', NEW.id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION accounts_grants_owner() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM auditor_grant_accounts ga
        JOIN auditor_grants g ON g.id = ga.grant_id
        WHERE ga.account_id = NEW.id
          AND g.revoked_at IS NULL
          AND g.owner_id IS DISTINCT FROM NEW.owner_id
    ) THEN
        RAISE EXCEPTION 'account % is still in an auditor grant of its old owner', NEW.id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER users_grants_same_group AFTER UPDATE OF group_id ON users
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
    WHEN (OLD.group_id IS DISTINCT FROM NEW.group_id)
    EXECUTE FUNCTION users_grants_same_group();
CREATE CONSTRAINT TRIGGER accounts_grants_owner AFTER UPDATE OF owner_id ON accounts
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
    WHEN (OLD.owner_id IS DISTINCT FROM NEW.owner_id)
    EXECUTE FUNCTION accounts_grants_owner();

-- The one definition of an active grant: not revoked, inside its window,
-- an auditor and a non-auditor, neither locked (a locked owner suspends
-- the grant, maintainer decision M2), in the same group. A row that
-- crosses groups past the triggers grants nothing.
CREATE VIEW active_auditor_grants AS
SELECT g.*
FROM auditor_grants g
JOIN users a ON a.id = g.auditor_id
JOIN users o ON o.id = g.owner_id
WHERE g.revoked_at IS NULL AND g.starts_at <= now() AND g.ends_at > now()
  AND a.is_auditor AND NOT o.is_auditor
  AND a.locked_at IS NULL AND o.locked_at IS NULL
  AND a.group_id = o.group_id;

-- The only source of an active grant's accounts: each one must still
-- belong to the grant's owner and not be an smtp account, so a stray row
-- (another owner's or another group's account) grants nothing.
CREATE VIEW active_auditor_grant_accounts AS
SELECT g.id AS grant_id, g.auditor_id, g.owner_id, ga.account_id, ga.all_folders, g.period_from, g.period_until
FROM active_auditor_grants g
JOIN auditor_grant_accounts ga ON ga.grant_id = g.id
JOIN accounts a ON a.id = ga.account_id AND a.owner_id = g.owner_id AND a.kind <> 'smtp';

-- +goose Down
-- Dropping the role would turn every auditor login into a member: refuse
-- while there is one.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM users WHERE is_auditor) THEN
        RAISE EXCEPTION 'auditor users exist; remove them before rolling back';
    END IF;
END
$$;
-- +goose StatementEnd
DROP VIEW active_auditor_grant_accounts;
DROP VIEW active_auditor_grants;
DROP TRIGGER accounts_grants_owner ON accounts;
DROP TRIGGER users_grants_same_group ON users;
DROP TRIGGER auditor_grant_accounts_owner ON auditor_grant_accounts;
DROP TRIGGER auditor_grants_same_group ON auditor_grants;
DROP TRIGGER users_auditor_fixed ON users;
DROP FUNCTION accounts_grants_owner();
DROP FUNCTION users_grants_same_group();
DROP FUNCTION auditor_grant_accounts_owner();
DROP FUNCTION auditor_grants_same_group();
DROP FUNCTION users_auditor_fixed();
DROP FUNCTION scope_folder_granted(bigint, bigint, bigint[], bigint[]);
ALTER TABLE sessions DROP COLUMN grant_id;
DROP TABLE auditor_grant_folders;
DROP TABLE auditor_grant_accounts;
DROP TABLE auditor_grants;
ALTER TABLE users DROP CONSTRAINT users_auditor_not_admin, DROP COLUMN is_auditor;
-- The audit log keeps its entries.
