-- name: LockFenceShared :exec
-- The attestation fence, shared, until the transaction ends. Every
-- transaction that inserts into messages or message_deletions takes it
-- (inWriteTx), so an attestation holding it exclusively sees no writer in
-- progress.
SELECT pg_advisory_xact_lock_shared(@class::int, 0);

-- name: AttestBounds :one
-- The first statement of an attestation's snapshot, taken while the fence
-- is held exclusively.
SELECT coalesce((SELECT max(seq) FROM messages), 0)::bigint AS messages_upto,
       coalesce((SELECT max(id) FROM message_deletions), 0)::bigint AS deletions_upto,
       (SELECT count(*) FROM messages)::bigint AS total;

-- name: ListAttestMessages :many
SELECT seq, sha256::text AS sha256 FROM messages
WHERE seq > @after::bigint AND seq <= @upto::bigint
ORDER BY seq LIMIT @page_size;

-- name: ListAttestDeletions :many
SELECT id, message_seq, sha256::text AS sha256, reason FROM message_deletions
WHERE id > @after::bigint AND id <= @upto::bigint
ORDER BY id LIMIT @page_size;

-- name: ListAttestedRange :many
-- The messages an attestation covered, rebuilt from today's database: the
-- rows of its range still present, and those deleted after it (deletion
-- id beyond its deletions_upto). Deletions up to its deletions_upto
-- happened before it, so those rows were not present then.
SELECT seq, sha256::text AS sha256 FROM messages
WHERE seq > @after::bigint AND seq <= @upto::bigint AND seq > @cursor::bigint
UNION ALL
SELECT message_seq, sha256::text FROM message_deletions
WHERE message_seq > @after::bigint AND message_seq <= @upto::bigint AND message_seq > @cursor::bigint
  AND id > @deletions_upto::bigint
ORDER BY 1 LIMIT @page_size;

-- name: InsertAttestation :exec
INSERT INTO attestations (seq, created_at, messages_after, messages_upto, messages_count,
                          deletions_after, deletions_upto, deletions_count, total,
                          leaves_sha256, prev_hash, public_key, hash, signature)
VALUES (@seq, @created_at, @messages_after, @messages_upto, @messages_count,
        @deletions_after, @deletions_upto, @deletions_count, @total,
        @leaves_sha256, @prev_hash, @public_key, @hash, @signature);

-- name: ListAttestations :many
SELECT * FROM attestations ORDER BY seq;

-- name: LastAttestation :one
SELECT * FROM attestations ORDER BY seq DESC LIMIT 1;

-- name: CountAttestations :one
SELECT count(*) FROM attestations;

-- name: CountDeletionsAfter :one
-- Deletions no attestation covers yet.
SELECT count(*) FROM message_deletions WHERE id > @after::bigint;

-- name: AdvanceMessageSeq :exec
-- After importing attestations from the file: new messages must get
-- numbers beyond every imported range.
SELECT setval(pg_get_serial_sequence('messages', 'seq'), @upto::bigint)
WHERE @upto::bigint > coalesce(pg_sequence_last_value(pg_get_serial_sequence('messages', 'seq')::regclass), 0);

-- name: AdvanceDeletionID :exec
SELECT setval(pg_get_serial_sequence('message_deletions', 'id'), @upto::bigint)
WHERE @upto::bigint > coalesce(pg_sequence_last_value(pg_get_serial_sequence('message_deletions', 'id')::regclass), 0);

-- name: ListMessagesInSeqRange :many
SELECT seq, sha256::text AS sha256 FROM messages
WHERE seq > @after::bigint AND seq <= @upto::bigint
ORDER BY seq;

-- name: RenumberMessages :execrows
-- Moves rows past every imported range: seq is an identity column, and
-- DEFAULT draws the next value.
UPDATE messages SET seq = DEFAULT WHERE seq = ANY(@seqs::bigint[]);
