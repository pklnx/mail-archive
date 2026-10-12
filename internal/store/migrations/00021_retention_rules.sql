-- +goose Up

-- Retention rules (#55): how many years the mail of an account, or of one
-- folder of it, must be kept. A message is never deleted while one of its
-- locations has no rule, and the longest period of all matching rules
-- wins. Rules are added and removed, never changed. Nothing deletes yet;
-- deletion runs come with the next migration.
CREATE TABLE retention_rules (
    id         BIGSERIAL   PRIMARY KEY,
    account_id BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    folder     TEXT        CHECK (folder <> ''),   -- NULL: all folders of the account
    years      INTEGER     NOT NULL CHECK (years BETWEEN 1 AND 100),
    time_zone  TEXT        NOT NULL DEFAULT 'Europe/Berlin',
    note       TEXT        NOT NULL DEFAULT '' CHECK (char_length(note) <= 500),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by BIGINT      REFERENCES users (id) ON DELETE SET NULL,  -- NULL: CLI
    UNIQUE NULLS NOT DISTINCT (account_id, folder)
);

-- Legal holds: no deletion of what they cover while they are active.
-- Every condition that is set must match; none at all holds the whole
-- archive. Holds are released, never deleted: RESTRICT keeps the users and
-- accounts they name.
CREATE TABLE legal_holds (
    id           BIGSERIAL   PRIMARY KEY,
    name         TEXT        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    reason       TEXT        NOT NULL DEFAULT '' CHECK (char_length(reason) <= 2000),
    owner_id     BIGINT      REFERENCES users (id) ON DELETE RESTRICT,
    account_id   BIGINT      REFERENCES accounts (id) ON DELETE RESTRICT,
    -- A search filter (q, from, to, attachment, has_attachment). Its
    -- matches are stored in legal_hold_messages.
    filter       JSONB       CHECK (filter IS NULL OR jsonb_typeof(filter) = 'object'),
    period_start TIMESTAMPTZ,
    period_end   TIMESTAMPTZ,
    -- When the filter's matches were last stored. Until the first time,
    -- the hold covers its whole scope.
    matched_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by   BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    released_at  TIMESTAMPTZ,
    released_by  BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    release_note TEXT        CHECK (char_length(release_note) BETWEEN 1 AND 2000),
    CHECK (period_start < period_end),
    CHECK (filter IS NOT NULL OR matched_at IS NULL),
    CHECK ((released_at IS NULL) = (release_note IS NULL))
);
CREATE INDEX legal_holds_owner_idx ON legal_holds (owner_id);
CREATE INDEX legal_holds_account_idx ON legal_holds (account_id);

-- Messages a filter hold matched. Rows are only added while the hold is
-- active, never removed, so a reindex that changes the extracted text
-- never releases a message. No foreign key to messages: the rows are the
-- record of what was held.
CREATE TABLE legal_hold_messages (
    hold_id        BIGINT      NOT NULL REFERENCES legal_holds (id) ON DELETE RESTRICT,
    message_sha256 CHAR(64)    NOT NULL,
    added_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (hold_id, message_sha256)
);
CREATE INDEX legal_hold_messages_sha_idx ON legal_hold_messages (message_sha256);

-- End of the retention period: the end of the calendar year (in time_zone)
-- of the basis, plus years (§147 Abs. 4 AO, §257 Abs. 5 HGB). The basis is
-- the later of the Date header and the internal date, but never later
-- than when the archive first stored the message. Returns the first
-- instant at which the period is over.
-- +goose StatementBegin
CREATE FUNCTION retention_end(sent_at timestamptz, internal_date timestamptz, archived_at timestamptz,
                              years integer, time_zone text) RETURNS timestamptz
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT (date_trunc('year', LEAST(GREATEST($1, $2), $3) AT TIME ZONE $5)
            + make_interval(years => $4 + 1)) AT TIME ZONE $5
$$;
-- +goose StatementEnd

-- End for a location in folder_id: the longest of all matching rules;
-- 'infinity' without a rule, never NULL. (NULL would let bool_and over a
-- message's locations ignore an unruled location.)
-- +goose StatementBegin
CREATE FUNCTION location_retain_until(folder_id bigint, sent_at timestamptz, internal_date timestamptz,
                                      archived_at timestamptz) RETURNS timestamptz
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT coalesce(max(retention_end($2, $3, $4, r.years, r.time_zone)), 'infinity')
    FROM folders f
    JOIN retention_rules r ON r.account_id = f.account_id AND (r.folder IS NULL OR r.folder = f.name)
    WHERE f.id = $1
$$;
-- +goose StatementEnd

-- Whether an active hold covers a location. sha256 NULL (a message not in
-- the archive) counts as matched by every filter hold in scope. A period
-- matches if the Date header or the internal date falls in it; a message
-- without either date counts as inside.
-- +goose StatementBegin
CREATE FUNCTION location_held(sha256 bpchar, folder_id bigint, sent_at timestamptz, internal_date timestamptz)
RETURNS boolean LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT EXISTS (
        SELECT 1
        FROM folders f
        JOIN accounts a ON a.id = f.account_id
        JOIN legal_holds h ON h.released_at IS NULL
        WHERE f.id = $2
          AND (h.owner_id IS NULL OR h.owner_id = a.owner_id)
          AND (h.account_id IS NULL OR h.account_id = a.id)
          AND (h.period_start IS NULL OR coalesce(GREATEST($3, $4) >= h.period_start, true))
          AND (h.period_end IS NULL OR coalesce(LEAST($3, $4) < h.period_end, true))
          AND (h.filter IS NULL OR h.matched_at IS NULL OR $1 IS NULL
               OR EXISTS (SELECT 1 FROM legal_hold_messages hm
                          WHERE hm.hold_id = h.id AND hm.message_sha256 = $1)))
$$;
-- +goose StatementEnd

-- +goose Down

-- Holds are a record; refuse to drop them (goose then keeps everything).
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM legal_holds) THEN
        RAISE EXCEPTION 'legal holds exist; rolling back would delete their record';
    END IF;
END $$;
-- +goose StatementEnd
DROP FUNCTION location_held(bpchar, bigint, timestamptz, timestamptz);
DROP FUNCTION location_retain_until(bigint, timestamptz, timestamptz, timestamptz);
DROP FUNCTION retention_end(timestamptz, timestamptz, timestamptz, integer, text);
DROP TABLE legal_hold_messages;
DROP TABLE legal_holds;
DROP TABLE retention_rules;
