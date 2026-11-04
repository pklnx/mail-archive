-- GoBD export (#394, part 2). Every query but GoBDNewMessages runs in the
-- export's repeatable read snapshot (store.GoBDSnapshot). The selection
-- predicate is the one of a store.Scope (accounts, the folder pairs of
-- scope_folder_granted and the period, as in search.sql), plus the
-- location cutoff of a resumed export and, for an export by legal hold,
-- the hold.

-- name: GoBDSelectionGroups :many
-- The customer group of every selected user and account (an account's is
-- its owner's; none without an owner), and the claimed group itself. The
-- first statement of the snapshot, so the snapshot every later pass reads
-- is the one the check saw.
SELECT 'account'::text AS kind, a.id, (coalesce(ou.name || '/', '') || a.name)::text AS name, ou.group_id, g.name AS group_name
FROM accounts a
LEFT JOIN users ou ON ou.id = a.owner_id
LEFT JOIN customer_groups g ON g.id = ou.group_id
WHERE a.id = ANY (@account_ids::bigint[])
UNION ALL
SELECT 'user'::text, u.id, u.name, u.group_id, g.name
FROM users u
LEFT JOIN customer_groups g ON g.id = u.group_id
WHERE u.id = ANY (@user_ids::bigint[])
UNION ALL
SELECT 'group'::text, g.id, g.name, g.id, g.name
FROM customer_groups g
WHERE g.id = sqlc.narg(group_id)::bigint
ORDER BY 1, 3, 2;

-- name: GoBDGroupUsers :many
-- The users of a group, without auditors (they own no accounts), for an
-- export of a whole customer group.
SELECT id, name FROM users
WHERE group_id = @group_id AND NOT is_auditor
ORDER BY name;

-- name: GoBDAccounts :many
-- Accounts by ID, removed ones included, for the accounts of a selection
-- without an owner (an owner's accounts come from OwnerScope).
SELECT a.id, a.name, a.kind, (a.removed_at IS NOT NULL)::boolean AS removed, a.owner_id
FROM accounts a
WHERE a.id = ANY (@ids::bigint[])
ORDER BY a.id;

-- name: GoBDHold :one
SELECT id, owner_id, account_id, group_id, (released_at IS NOT NULL)::boolean AS released
FROM legal_holds WHERE id = @id;

-- name: GoBDGrant :one
SELECT id, owner_id, period_from, period_until, (revoked_at IS NOT NULL)::boolean AS revoked
FROM auditor_grants WHERE id = @id;

-- name: GoBDGrantAccounts :many
-- The accounts a grant still names, with their granted folder IDs (only
-- those of the account itself).
SELECT ga.account_id, ga.all_folders,
       coalesce(array_agg(gf.folder_id ORDER BY gf.folder_id) FILTER (WHERE gf.folder_id IS NOT NULL), '{}')::bigint[] AS folders
FROM auditor_grant_accounts ga
LEFT JOIN auditor_grant_folders gf ON gf.grant_id = ga.grant_id AND gf.account_id = ga.account_id
WHERE ga.grant_id = @grant_id
GROUP BY ga.account_id, ga.all_folders
ORDER BY ga.account_id;

-- name: GoBDMaxLocationID :one
SELECT coalesce(max(id), 0)::bigint FROM message_locations;

-- name: GoBDNewMessages :one
-- Whether messages were archived after the newest attestation (or any,
-- without one): only then does an export write one first.
SELECT EXISTS (
    SELECT 1 FROM messages
    WHERE seq > coalesce((SELECT messages_upto FROM attestations ORDER BY seq DESC LIMIT 1), 0));

-- name: GoBDLastAttestation :one
SELECT * FROM attestations ORDER BY seq DESC LIMIT 1;

-- name: GoBDChain :many
SELECT * FROM attestations WHERE seq <= @upto ORDER BY seq;

-- name: GoBDTotals :one
-- The number and bytes of the selected messages, for the free-space check
-- and the ZIP limit.
SELECT count(*)::bigint AS messages, coalesce(sum(m.size), 0)::bigint AS bytes
FROM messages m
WHERE (sqlc.narg(scope_after)::timestamptz IS NULL OR m.sort_at >= sqlc.narg(scope_after)::timestamptz)
  AND (sqlc.narg(scope_before)::timestamptz IS NULL OR m.sort_at < sqlc.narg(scope_before)::timestamptz)
  AND EXISTS (
       SELECT 1 FROM message_locations l
       JOIN folders f ON f.id = l.folder_id
       WHERE l.message_sha256 = m.sha256
         AND l.id <= @max_location_id::bigint
         AND f.account_id = ANY (sqlc.arg(accounts)::bigint[])
         AND (f.account_id = ANY (sqlc.arg(scope_all_folder_accounts)::bigint[]) OR scope_folder_granted(f.account_id, f.id, sqlc.arg(scope_folder_accounts)::bigint[], sqlc.arg(scope_folders)::bigint[]))
         AND (sqlc.narg(hold_id)::bigint IS NULL
              OR sqlc.narg(hold_id)::bigint IN (SELECT location_holds(l.message_sha256, l.folder_id, m.sent_at, l.internal_date))));

-- name: GoBDMessages :many
-- One page of the selected messages by seq (passes B and A1): a message is
-- selected if any of its locations is. attestation is the first
-- attestation up to att_upto whose range holds the message, 0 if none.
SELECT m.seq, m.sha256::text AS sha256, m.size, m.stored_path, m.sort_at::timestamptz AS sort_at, m.created_at,
       coalesce(m.from_addr, '')::text AS from_addr, coalesce(m.to_addr, '')::text AS to_addr,
       coalesce(m.cc_addr, '')::text AS cc_addr, coalesce(m.subject, '')::text AS subject,
       coalesce(m.message_id, '')::text AS message_id, coalesce(m.in_reply_to, '')::text AS in_reply_to,
       m.has_attachment, coalesce(m.attachment_names, '')::text AS attachment_names,
       coalesce((SELECT min(a.seq) FROM attestations a
                 WHERE a.seq <= @att_upto::bigint AND a.messages_after < m.seq AND m.seq <= a.messages_upto), 0)::bigint AS attestation
FROM messages m
WHERE m.seq > @after_seq::bigint
  AND (sqlc.narg(scope_after)::timestamptz IS NULL OR m.sort_at >= sqlc.narg(scope_after)::timestamptz)
  AND (sqlc.narg(scope_before)::timestamptz IS NULL OR m.sort_at < sqlc.narg(scope_before)::timestamptz)
  AND EXISTS (
       SELECT 1 FROM message_locations l
       JOIN folders f ON f.id = l.folder_id
       WHERE l.message_sha256 = m.sha256
         AND l.id <= @max_location_id::bigint
         AND f.account_id = ANY (sqlc.arg(accounts)::bigint[])
         AND (f.account_id = ANY (sqlc.arg(scope_all_folder_accounts)::bigint[]) OR scope_folder_granted(f.account_id, f.id, sqlc.arg(scope_folder_accounts)::bigint[], sqlc.arg(scope_folders)::bigint[]))
         AND (sqlc.narg(hold_id)::bigint IS NULL
              OR sqlc.narg(hold_id)::bigint IN (SELECT location_holds(l.message_sha256, l.folder_id, m.sent_at, l.internal_date))))
ORDER BY m.seq
LIMIT @page_size;

-- name: GoBDSelectedAmong :many
-- Which of these messages are selected, with their sort_at: a resumed
-- export removes the files of the others.
SELECT m.sha256::text AS sha256, m.sort_at::timestamptz AS sort_at
FROM messages m
WHERE m.sha256 = ANY (@shas::bpchar[])
  AND (sqlc.narg(scope_after)::timestamptz IS NULL OR m.sort_at >= sqlc.narg(scope_after)::timestamptz)
  AND (sqlc.narg(scope_before)::timestamptz IS NULL OR m.sort_at < sqlc.narg(scope_before)::timestamptz)
  AND EXISTS (
       SELECT 1 FROM message_locations l
       JOIN folders f ON f.id = l.folder_id
       WHERE l.message_sha256 = m.sha256
         AND l.id <= @max_location_id::bigint
         AND f.account_id = ANY (sqlc.arg(accounts)::bigint[])
         AND (f.account_id = ANY (sqlc.arg(scope_all_folder_accounts)::bigint[]) OR scope_folder_granted(f.account_id, f.id, sqlc.arg(scope_folder_accounts)::bigint[], sqlc.arg(scope_folders)::bigint[]))
         AND (sqlc.narg(hold_id)::bigint IS NULL
              OR sqlc.narg(hold_id)::bigint IN (SELECT location_holds(l.message_sha256, l.folder_id, m.sent_at, l.internal_date))));

-- name: GoBDLocations :many
-- One page of the selected locations by id (pass A2). holds is filled only
-- with with_holds (an export by legal hold), and lists only the active
-- holds that name an owner, an account or a group: each of them lies in
-- the location's group (location_holds, 00035); a hold over every
-- customer is left out.
SELECT l.id, l.message_sha256::text AS sha256, coalesce(u.name, '')::text AS owner, a.name AS account, a.kind,
       f.name AS folder, l.uidvalidity, l.uid, l.flags, l.internal_date, l.first_seen_at, l.last_seen_at, l.gone_at,
       e.mail_from, e.rcpt_to, e.received_at,
       CASE WHEN ru.retain_until < 'infinity'::timestamptz THEN ru.retain_until END AS retain_until,
       CASE WHEN @with_holds::boolean THEN
           ARRAY(SELECT h.id FROM legal_holds h
                 WHERE h.id IN (SELECT location_holds(l.message_sha256, l.folder_id, m.sent_at, l.internal_date))
                   AND (h.owner_id IS NOT NULL OR h.account_id IS NOT NULL OR h.group_id IS NOT NULL)
                 ORDER BY h.id)
       ELSE '{}'::bigint[] END::bigint[] AS holds
FROM message_locations l
JOIN messages m ON m.sha256 = l.message_sha256
JOIN folders f ON f.id = l.folder_id
JOIN accounts a ON a.id = f.account_id
LEFT JOIN users u ON u.id = a.owner_id
LEFT JOIN envelopes e ON e.location_id = l.id
-- 'infinity' (no retention rule) becomes NULL above.
CROSS JOIN LATERAL (SELECT location_retain_until(l.folder_id, m.sent_at, l.internal_date, m.created_at)::timestamptz AS retain_until) ru
WHERE l.id > @after_id::bigint
  AND l.id <= @max_location_id::bigint
  AND (sqlc.narg(scope_after)::timestamptz IS NULL OR m.sort_at >= sqlc.narg(scope_after)::timestamptz)
  AND (sqlc.narg(scope_before)::timestamptz IS NULL OR m.sort_at < sqlc.narg(scope_before)::timestamptz)
  AND f.account_id = ANY (sqlc.arg(accounts)::bigint[])
  AND (f.account_id = ANY (sqlc.arg(scope_all_folder_accounts)::bigint[]) OR scope_folder_granted(f.account_id, f.id, sqlc.arg(scope_folder_accounts)::bigint[], sqlc.arg(scope_folders)::bigint[]))
  AND (sqlc.narg(hold_id)::bigint IS NULL
       OR sqlc.narg(hold_id)::bigint IN (SELECT location_holds(l.message_sha256, l.folder_id, m.sent_at, l.internal_date)))
ORDER BY l.id
LIMIT @page_size;
