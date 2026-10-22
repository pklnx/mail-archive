-- name: InsertDeletionRun :one
INSERT INTO deletion_runs (created_by) VALUES (sqlc.narg(created_by)) RETURNING id, created_at;

-- name: InsertDeletionCandidates :execrows
-- Every message whose locations all have an ended retention period and no
-- hold. A location without a rule ends at 'infinity', so it keeps the
-- message (bool_and would ignore a NULL).
INSERT INTO deletion_candidates (run_id, message_sha256, size, retain_until)
SELECT @run_id::bigint, l.message_sha256, max(m.size),
       max(location_retain_until(l.folder_id, m.sent_at, l.internal_date, m.created_at))::timestamptz
FROM message_locations l
JOIN messages m ON m.sha256 = l.message_sha256
GROUP BY l.message_sha256
HAVING bool_and(location_retain_until(l.folder_id, m.sent_at, l.internal_date, m.created_at) <= now())
   AND NOT bool_or(location_held(l.message_sha256, l.folder_id, m.sent_at, l.internal_date));

-- name: SetDeletionRunCounts :one
UPDATE deletion_runs r
SET candidates = (SELECT count(*) FROM deletion_candidates c WHERE c.run_id = @id::bigint),
    bytes = (SELECT coalesce(sum(c.size), 0) FROM deletion_candidates c WHERE c.run_id = @id::bigint)::bigint
WHERE r.id = @id::bigint
RETURNING r.candidates, r.bytes;

-- name: GetDeletionRun :one
SELECT r.id, r.state, r.created_at, cu.name AS created_by, r.candidates, r.bytes, r.confirmed_at,
       fu.name AS confirmed_by, r.finished_at, r.deleted, r.kept
FROM deletion_runs r
LEFT JOIN users cu ON cu.id = r.created_by
LEFT JOIN users fu ON fu.id = r.confirmed_by
WHERE r.id = @id;

-- name: ListDeletionRuns :many
SELECT r.id, r.state, r.created_at, cu.name AS created_by, r.candidates, r.bytes, r.confirmed_at,
       fu.name AS confirmed_by, r.finished_at, r.deleted, r.kept
FROM deletion_runs r
LEFT JOIN users cu ON cu.id = r.created_by
LEFT JOIN users fu ON fu.id = r.confirmed_by
ORDER BY r.id DESC
LIMIT @row_limit;

-- name: OpenDeletionRunID :one
SELECT id FROM deletion_runs WHERE state IN ('proposed', 'confirmed');

-- name: ConfirmDeletionRun :execrows
UPDATE deletion_runs SET state = 'confirmed', confirmed_at = now(), confirmed_by = sqlc.narg(confirmed_by)
WHERE id = @id AND state = 'proposed';

-- name: CancelDeletionRun :execrows
UPDATE deletion_runs SET state = 'cancelled', finished_at = now()
WHERE id = @id AND state = 'proposed';

-- name: FinishDeletionRun :execrows
UPDATE deletion_runs SET state = 'done', finished_at = now()
WHERE id = @id AND state = 'confirmed';

-- name: AddDeletionRunCounts :exec
UPDATE deletion_runs SET deleted = deleted + @deleted::int, kept = kept + @kept::int WHERE id = @id;

-- name: PendingCandidates :many
SELECT message_sha256::text AS sha256 FROM deletion_candidates
WHERE run_id = @run_id AND state = 'pending' AND message_sha256 > @after::bpchar
ORDER BY message_sha256
LIMIT @row_limit;

-- name: DeletableOf :many
-- The candidate check again, restricted to some messages, at deletion
-- time: a rule removed, a hold added or a new location since the preview
-- keeps the message.
SELECT l.message_sha256::text AS sha256
FROM message_locations l
JOIN messages m ON m.sha256 = l.message_sha256
WHERE l.message_sha256 = ANY (@shas::bpchar[])
GROUP BY l.message_sha256
HAVING bool_and(location_retain_until(l.folder_id, m.sent_at, l.internal_date, m.created_at) <= now())
   AND NOT bool_or(location_held(l.message_sha256, l.folder_id, m.sent_at, l.internal_date));

-- name: InsertTombstones :execrows
INSERT INTO deleted_messages (sha256, size, message_seq, archived_at, retain_until, locations, run_id)
SELECT m.sha256, m.size, m.seq, m.created_at,
       (SELECT max(location_retain_until(l.folder_id, m.sent_at, l.internal_date, m.created_at))
        FROM message_locations l WHERE l.message_sha256 = m.sha256),
       (SELECT count(*) FROM message_locations l WHERE l.message_sha256 = m.sha256),
       @run_id::bigint
FROM messages m
WHERE m.sha256 = ANY (@shas::bpchar[])
ORDER BY m.seq;

-- name: InsertRetentionDeletions :execrows
-- The deletion records of the attestation chain (#66), in the same
-- transaction as the tombstones.
INSERT INTO message_deletions (message_seq, sha256, reason)
SELECT seq, sha256, 'retention' FROM messages WHERE sha256 = ANY (@shas::bpchar[]) ORDER BY seq;

-- name: DeleteLocationsOf :execrows
DELETE FROM message_locations WHERE message_sha256 = ANY (@shas::bpchar[]);

-- name: DeleteMessagesOf :execrows
DELETE FROM messages WHERE sha256 = ANY (@shas::bpchar[]);

-- name: ScheduleBlobRemovals :exec
INSERT INTO blob_removals (sha256, due_at)
SELECT s, @due_at::timestamptz FROM unnest(@shas::text[]) s
ON CONFLICT (sha256) DO UPDATE SET due_at = GREATEST(blob_removals.due_at, EXCLUDED.due_at);

-- name: MarkCandidates :exec
UPDATE deletion_candidates SET state = @state
WHERE run_id = @run_id AND message_sha256 = ANY (@shas::bpchar[]) AND state = 'pending';

-- name: DeletionRunSummary :many
-- The pending candidates of a run by owner, account, folder and the year
-- their retention period ended in, for the review. A message found in two
-- folders counts in both.
SELECT coalesce(u.name, '')::text AS owner, a.name AS account, f.name AS folder,
       extract(year FROM (c.retain_until - interval '1 microsecond') AT TIME ZONE 'UTC')::int AS year,
       count(DISTINCT c.message_sha256)::bigint AS messages
FROM deletion_candidates c
JOIN message_locations l ON l.message_sha256 = c.message_sha256
JOIN folders f ON f.id = l.folder_id
JOIN accounts a ON a.id = f.account_id
LEFT JOIN users u ON u.id = a.owner_id
WHERE c.run_id = @run_id AND c.state = 'pending'
GROUP BY 1, 2, 3, 4
ORDER BY 1, 2, 3, 4;

-- name: DeletionCandidateDetails :many
-- The candidates of a run with what the CLI shows in the review.
SELECT c.message_sha256::text AS sha256, c.size, c.retain_until, c.state,
       m.subject, m.from_addr, m.sent_at,
       coalesce((SELECT array_agg(DISTINCT coalesce(u.name, '-') || '/' || a.name || '/' || f.name)
                 FROM message_locations l
                 JOIN folders f ON f.id = l.folder_id
                 JOIN accounts a ON a.id = f.account_id
                 LEFT JOIN users u ON u.id = a.owner_id
                 WHERE l.message_sha256 = c.message_sha256), '{}')::text[] AS places
FROM deletion_candidates c
LEFT JOIN messages m ON m.sha256 = c.message_sha256
WHERE c.run_id = @run_id
ORDER BY c.retain_until, c.message_sha256;

-- name: BlobRemovalDue :one
SELECT EXISTS (SELECT 1 FROM blob_removals WHERE due_at <= now());

-- name: DueBlobRemovals :many
-- Due file removals, and whether the message was archived again.
SELECT b.sha256::text AS sha256, b.due_at,
       EXISTS (SELECT 1 FROM messages m WHERE m.sha256 = b.sha256) AS archived
FROM blob_removals b
WHERE b.due_at <= now() AND (b.due_at, b.sha256) > (@after_due::timestamptz, @after_sha::bpchar)
ORDER BY b.due_at, b.sha256
LIMIT @row_limit;

-- name: DeleteBlobRemovals :execrows
DELETE FROM blob_removals WHERE sha256 = ANY (@shas::bpchar[]) AND due_at <= now();

-- name: TombstonedOf :many
-- Hashes of a batch that were deleted and are not in the archive now.
SELECT d.sha256::text AS sha256, min(d.archived_at)::timestamptz AS archived_at
FROM deleted_messages d
WHERE d.sha256 = ANY (@shas::bpchar[])
  AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.sha256 = d.sha256)
GROUP BY d.sha256;

-- name: SkipRearchive :one
-- Whether a deleted message would already be expired at a new location:
-- its period, from no later than when it was first archived, has ended
-- and no hold covers it. A filter hold counts as matching, because the
-- message cannot be searched without its row.
SELECT (location_retain_until(@folder_id::bigint, sqlc.narg(sent_at)::timestamptz,
                              sqlc.narg(internal_date)::timestamptz, @archived_at::timestamptz) <= now()
        AND NOT location_held(NULL, @folder_id::bigint, sqlc.narg(sent_at)::timestamptz,
                              sqlc.narg(internal_date)::timestamptz))::boolean AS skip;

-- name: IsTombstoned :one
SELECT EXISTS (SELECT 1 FROM deleted_messages WHERE sha256 = @sha256::bpchar);

-- name: DeletionCounts :one
SELECT (SELECT count(*) FROM blob_removals)::bigint AS pending_files,
       coalesce((SELECT id FROM deletion_runs WHERE state IN ('proposed', 'confirmed')), 0)::bigint AS open_run;

-- name: DeletedFiles :many
-- Of some hashes, those deleted (retention or account purge) and not
-- archived again, and
-- whether their file is scheduled for removal.
SELECT s::text AS sha256, EXISTS (SELECT 1 FROM blob_removals b WHERE b.sha256 = s) AS scheduled
FROM unnest(@shas::text[]) s
WHERE EXISTS (SELECT 1 FROM deleted_messages d WHERE d.sha256 = s)
  AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.sha256 = s);
