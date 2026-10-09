-- Reconcile: compare the archive's locations with what the server lists.
-- Called while holding the account's sync lock.

-- name: LockFolderForReconcile :one
SELECT uidvalidity, last_uid, last_reconciled_at FROM folders WHERE id = $1 FOR UPDATE;

-- name: ReconcileFolder :one
-- One statement for the whole folder. The server's UIDs and flags (flags
-- joined by spaces; IMAP flags are atoms without spaces) are hashed once
-- and joined with the folder's current locations. Only rows that change
-- are written: locations missing on the server become gone, gone ones that
-- are listed again come back, and listed ones get their flags when they
-- differ as a set. Locations from an older UIDVALIDITY are gone too: the
-- rescan in this run stored a new location for every message still there.
WITH listed AS MATERIALIZED (
    -- Two unnests in the select list are zipped (same length, from Go).
    SELECT u.uid, string_to_array(u.flags, ' ') AS flags
    FROM (SELECT unnest(@uids::bigint[]) AS uid, unnest(@flags::text[]) AS flags) u
),
cur AS MATERIALIZED (
    SELECT l.id, l.gone_at, l.flags, s.uid IS NOT NULL AS listed, s.flags AS new_flags
    FROM message_locations l
    LEFT JOIN listed s ON s.uid = l.uid
    WHERE l.folder_id = @folder_id AND l.uidvalidity = @uidvalidity AND l.uid <= @last_uid
),
gone AS (
    UPDATE message_locations l
    SET gone_at = now(), last_seen_at = GREATEST(l.last_seen_at, sqlc.narg(prev_reconciled_at)::timestamptz)
    FROM cur c
    WHERE l.id = c.id AND NOT c.listed AND c.gone_at IS NULL
    RETURNING 1
),
back AS (
    UPDATE message_locations l
    SET gone_at = NULL, last_seen_at = now(), flags = c.new_flags
    FROM cur c
    WHERE l.id = c.id AND c.listed AND c.gone_at IS NOT NULL
    RETURNING 1
),
changed AS (
    UPDATE message_locations l
    SET flags = c.new_flags
    FROM cur c
    WHERE l.id = c.id AND c.listed AND c.gone_at IS NULL
      -- Rows stored before \Recent was dropped keep it until a real change.
      AND NOT (array_remove(c.flags, '\Recent') @> c.new_flags AND array_remove(c.flags, '\Recent') <@ c.new_flags)
    RETURNING 1
),
superseded AS (
    UPDATE message_locations l
    SET gone_at = now(), last_seen_at = GREATEST(l.last_seen_at, sqlc.narg(prev_reconciled_at)::timestamptz)
    WHERE l.folder_id = @folder_id AND l.uidvalidity <> @uidvalidity AND l.gone_at IS NULL
    RETURNING 1
),
done AS (
    UPDATE folders SET last_reconciled_at = now() WHERE id = @folder_id RETURNING 1
)
SELECT (SELECT count(*) FROM cur WHERE cur.gone_at IS NULL)::bigint AS present_before,
       (SELECT count(*) FROM gone)::bigint AS gone,
       (SELECT count(*) FROM superseded)::bigint AS superseded,
       (SELECT count(*) FROM back)::bigint AS back,
       (SELECT count(*) FROM changed)::bigint AS changed,
       (SELECT count(*) FROM done)::bigint AS done;

-- name: MarkFolderVanished :one
-- A folder the server no longer lists: all its locations are gone.
WITH gone AS (
    UPDATE message_locations l
    SET gone_at = now(), last_seen_at = GREATEST(l.last_seen_at, sqlc.narg(prev_reconciled_at)::timestamptz)
    WHERE l.folder_id = @folder_id AND l.gone_at IS NULL
    RETURNING 1
),
done AS (
    UPDATE folders SET last_reconciled_at = now() WHERE id = @folder_id RETURNING 1
)
SELECT (SELECT count(*) FROM gone)::bigint AS gone, (SELECT count(*) FROM done)::bigint AS done;

-- name: ListAccountFolderStates :many
SELECT id, name, last_reconciled_at FROM folders WHERE account_id = $1 ORDER BY name;

-- name: ReconcileStates :many
-- Per account of a user: messages whose locations in the account are all
-- gone from the server, and the latest reconcile of any of its folders.
SELECT a.id,
       (SELECT count(DISTINCT l.message_sha256) FROM message_locations l JOIN folders f ON f.id = l.folder_id
        WHERE f.account_id = a.id AND l.gone_at IS NOT NULL
          AND NOT EXISTS (
              SELECT 1 FROM message_locations p JOIN folders pf ON pf.id = p.folder_id
              WHERE p.message_sha256 = l.message_sha256 AND pf.account_id = a.id AND p.gone_at IS NULL)
       ) AS gone,
       (SELECT f.last_reconciled_at FROM folders f WHERE f.account_id = a.id AND f.last_reconciled_at IS NOT NULL
        ORDER BY f.last_reconciled_at DESC LIMIT 1) AS last_reconciled_at
FROM accounts a
WHERE a.owner_id = @owner::bigint;
