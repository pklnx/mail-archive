-- +goose Up

-- Legal holds per customer group (#470, part 4): a hold can name a group
-- and then covers every location of every account whose owner is in that
-- group, also of users who join the group later. Like owner_id and
-- account_id, it only narrows a hold: a group can never make a hold delete
-- more. Holds are released, never deleted, so RESTRICT keeps a group that
-- a hold names (DeleteGroup answers ErrGroupInUse).
ALTER TABLE legal_holds ADD COLUMN group_id BIGINT REFERENCES customer_groups (id) ON DELETE RESTRICT;
CREATE INDEX legal_holds_group_idx ON legal_holds (group_id);

-- The 00030 body plus the group condition: the group of a location is its
-- account's owner's. An account without owner is in no group, so a group
-- hold does not cover it. The owner's group is looked up only for an active
-- hold that names a group (a subquery, not a join: measured on 50,000
-- locations, a join made a deletion run's candidate query about 60 % slower
-- with one group hold active, the subquery about 8 %). location_held is
-- defined through this function (00030), so the deletion guard trigger,
-- deletion runs and purges all follow.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION location_holds(sha256 bpchar, folder_id bigint, sent_at timestamptz, internal_date timestamptz)
RETURNS SETOF bigint LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT h.id
    FROM folders f
    JOIN accounts a ON a.id = f.account_id
    JOIN legal_holds h ON h.released_at IS NULL
    WHERE f.id = $2
      AND (h.owner_id IS NULL OR h.owner_id = a.owner_id)
      AND (h.account_id IS NULL OR h.account_id = a.id)
      AND (h.group_id IS NULL OR h.group_id = (SELECT u.group_id FROM users u WHERE u.id = a.owner_id))
      AND (h.period_start IS NULL OR coalesce(GREATEST($3, $4) >= h.period_start, true))
      AND (h.period_end IS NULL OR coalesce(LEAST($3, $4) < h.period_end, true))
      AND (h.filter IS NULL OR h.matched_at IS NULL OR $1 IS NULL
           OR EXISTS (SELECT 1 FROM legal_hold_messages hm
                      WHERE hm.hold_id = h.id AND hm.message_sha256 = $1))
$$;
-- +goose StatementEnd

-- +goose Down

-- A hold is a record (00021): rolling back must not drop the group a hold
-- names, nor widen a group hold to the whole archive.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM legal_holds WHERE group_id IS NOT NULL) THEN
        RAISE EXCEPTION 'legal holds name customer groups; rolling back would drop their scope';
    END IF;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION location_holds(sha256 bpchar, folder_id bigint, sent_at timestamptz, internal_date timestamptz)
RETURNS SETOF bigint LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT h.id
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
                      WHERE hm.hold_id = h.id AND hm.message_sha256 = $1))
$$;
-- +goose StatementEnd

DROP INDEX legal_holds_group_idx;
ALTER TABLE legal_holds DROP COLUMN group_id;
