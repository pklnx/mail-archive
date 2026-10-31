-- The job queue (see 00032 and internal/jobs).

-- name: EnqueueJob :one
-- Idempotent per (kind, key), one rule per state of an existing row:
-- queued keeps its row and only moves forward (the lower priority; the
-- earlier run_after, unless a retry is pending, attempts > 0); done is
-- queued again with the payload of the caller and attempts = 0; running is left
-- alone (handlers are idempotent, delivery is at least once); failed is
-- left alone unless retry_failed. Returns a row only when the job is
-- queued afterwards.
INSERT INTO jobs AS j (kind, key, message_sha256, priority, max_attempts, run_after, payload)
VALUES (@kind, @key, sqlc.narg(message_sha256), @priority, @max_attempts,
        coalesce(sqlc.narg(run_after)::timestamptz, now()), @payload)
ON CONFLICT (kind, key) DO UPDATE SET
    priority = CASE WHEN j.state = 'queued' THEN LEAST(j.priority, EXCLUDED.priority) ELSE EXCLUDED.priority END,
    run_after = CASE
        WHEN j.state = 'queued' AND j.attempts > 0 THEN j.run_after
        WHEN j.state = 'queued' THEN LEAST(j.run_after, EXCLUDED.run_after)
        ELSE EXCLUDED.run_after END,
    payload = CASE WHEN j.state = 'queued' THEN j.payload ELSE EXCLUDED.payload END,
    attempts = CASE WHEN j.state = 'queued' THEN j.attempts ELSE 0 END,
    last_error = CASE WHEN j.state = 'queued' THEN j.last_error END,
    max_attempts = EXCLUDED.max_attempts,
    message_sha256 = coalesce(EXCLUDED.message_sha256, j.message_sha256),
    started_at = CASE WHEN j.state = 'queued' THEN j.started_at END,
    finished_at = NULL,
    state = 'queued'
WHERE j.state IN ('queued', 'done') OR (j.state = 'failed' AND @retry_failed::boolean)
RETURNING j.id;

-- name: NotifyJobs :exec
-- Wakes the pools listening on mail_archive_jobs at commit. PostgreSQL
-- merges identical notifications of one transaction.
SELECT pg_notify('mail_archive_jobs', @kind::text);

-- name: ClaimJobs :many
-- Leases up to max_rows due jobs of the kinds, in priority order; rows
-- another pool holds are skipped. The attempt counts from here.
UPDATE jobs j
SET state = 'running', lease_token = @lease, lease_until = now() + make_interval(secs => @lease_seconds::float8),
    attempts = j.attempts + 1, started_at = now()
WHERE j.id IN (
    SELECT c.id FROM jobs c
    WHERE c.state = 'queued' AND c.run_after <= now() AND c.kind = ANY (@kinds::text[])
    ORDER BY c.priority, c.run_after, c.id
    LIMIT @max_rows
    FOR UPDATE OF c SKIP LOCKED)
RETURNING j.id, j.kind, j.key, j.message_sha256, j.priority, j.run_after, j.payload, j.attempts, j.max_attempts;

-- name: ExtendJobLeases :many
-- The heartbeat: extends the leases a pool still holds. A job missing
-- from the result lost its lease (it expired and was taken over, or its
-- message was deleted).
UPDATE jobs j SET lease_until = now() + make_interval(secs => @lease_seconds::float8)
WHERE j.state = 'running'
  AND (j.id, j.lease_token) IN (SELECT unnest(@ids::bigint[]), unnest(@leases::bytea[]))
RETURNING j.id, j.lease_token;

-- name: FinishJob :execrows
UPDATE jobs
SET state = 'done', finished_at = now(), lease_token = NULL, lease_until = NULL, last_error = NULL
WHERE id = @id AND lease_token = @lease;

-- name: RetryJob :one
-- After a handler error: failed once max_attempts claims were made (or at
-- once if permanent), else queued again after an exponential backoff with
-- equal jitter, so that restarted pools do not retry in step:
-- min(first * 2^(attempts-1), cap) * [0.5, 1). ReapExpiredJobs uses the
-- same expression.
WITH c AS (
    SELECT j.id, (sqlc.arg(permanent)::boolean OR j.attempts >= j.max_attempts) AS give_up,
        now() + make_interval(secs => LEAST(sqlc.arg(first_seconds)::float8 * power(2::float8, GREATEST(j.attempts - 1, 0)::float8),
                                            sqlc.arg(cap_seconds)::float8) * (0.5 + random() * 0.5)) AS retry_at
    FROM jobs j WHERE j.id = sqlc.arg(id) AND j.lease_token = sqlc.arg(lease)
)
UPDATE jobs j
SET state = CASE WHEN c.give_up THEN 'failed' ELSE 'queued' END,
    finished_at = CASE WHEN c.give_up THEN now() END,
    run_after = CASE WHEN c.give_up THEN j.run_after ELSE c.retry_at END,
    last_error = sqlc.arg(code)::text, lease_token = NULL, lease_until = NULL
FROM c
-- The token again: a waiting UPDATE re-checks only its own WHERE against
-- a row another pool took over meanwhile, not the CTE.
WHERE j.id = c.id AND j.lease_token = sqlc.arg(lease)
RETURNING j.state, j.run_after;

-- name: ReapExpiredJobs :many
-- Running jobs of the kind whose lease expired (their process died or
-- hangs) are retried as after an error, with code 'lease expired': the
-- attempt the dead process made counts. Rows a heartbeat holds are
-- skipped, and so are the leases of the calling pool (own): only its
-- heartbeat decides about the jobs it runs.
WITH c AS (
    SELECT x.id, x.attempts >= x.max_attempts AS give_up,
        now() + make_interval(secs => LEAST(sqlc.arg(first_seconds)::float8 * power(2::float8, GREATEST(x.attempts - 1, 0)::float8),
                                            sqlc.arg(cap_seconds)::float8) * (0.5 + random() * 0.5)) AS retry_at
    FROM jobs x
    WHERE x.state = 'running' AND x.lease_until < now() AND x.kind = sqlc.arg(kind)
      AND NOT (x.lease_token = ANY (sqlc.arg(own)::bytea[]))
    ORDER BY x.lease_until
    LIMIT sqlc.arg(max_rows)
    FOR UPDATE OF x SKIP LOCKED
)
UPDATE jobs j
SET state = CASE WHEN c.give_up THEN 'failed' ELSE 'queued' END,
    finished_at = CASE WHEN c.give_up THEN now() END,
    run_after = CASE WHEN c.give_up THEN j.run_after ELSE c.retry_at END,
    last_error = 'lease expired', lease_token = NULL, lease_until = NULL
FROM c
WHERE j.id = c.id AND j.state = 'running' AND j.lease_until < now()
RETURNING j.id, j.kind, j.key, j.state, j.attempts;

-- name: ReleaseJob :execrows
-- At shutdown: queued again at once, and the attempt is taken back.
UPDATE jobs
SET state = 'queued', attempts = GREATEST(attempts - 1, 0), run_after = now(),
    lease_token = NULL, lease_until = NULL, started_at = NULL
WHERE id = @id AND lease_token = @lease;

-- name: ContinueJob :execrows
-- A handler that did part of its work: queued again at once with the new
-- payload (its cursor), as a fresh job.
UPDATE jobs
SET state = 'queued', payload = @payload, attempts = 0, run_after = now(), last_error = NULL,
    lease_token = NULL, lease_until = NULL, started_at = NULL
WHERE id = @id AND lease_token = @lease;

-- name: NextJobDue :one
-- Seconds until the next queued job of the kinds is due or the next lease
-- of the kinds that the pool does not hold itself expires; negative when
-- that has passed, infinite when there is none.
SELECT coalesce(EXTRACT(EPOCH FROM LEAST(
    (SELECT min(q.run_after) FROM jobs q WHERE q.state = 'queued' AND q.kind = ANY (@kinds::text[])),
    (SELECT min(r.lease_until) FROM jobs r WHERE r.state = 'running' AND r.kind = ANY (@kinds::text[])
        AND NOT (r.lease_token = ANY (@own::bytea[])))
) - now())::float8, 'Infinity'::float8)::float8 AS seconds;

-- name: DeleteDoneJobs :execrows
-- Done rows older than keep_seconds, at most 1000 per call.
DELETE FROM jobs WHERE id IN (
    SELECT c.id FROM jobs c
    WHERE c.state = 'done' AND c.finished_at < now() - make_interval(secs => @keep_seconds::float8)
    ORDER BY c.finished_at
    LIMIT 1000
    FOR UPDATE OF c SKIP LOCKED);

-- name: JobStats :many
-- Per kind with work that is not done: queued (due, or waiting without a
-- retry), queued with a retry pending, running and failed, and how long the
-- oldest due job (of any priority, and of new mail) has been due.
SELECT kind,
    count(*) FILTER (WHERE state = 'queued' AND NOT (attempts > 0 AND run_after > now()))::bigint AS queued,
    count(*) FILTER (WHERE state = 'queued' AND attempts > 0 AND run_after > now())::bigint AS retrying,
    count(*) FILTER (WHERE state = 'running')::bigint AS running,
    count(*) FILTER (WHERE state = 'failed')::bigint AS failed,
    coalesce(EXTRACT(EPOCH FROM now() - min(run_after) FILTER (WHERE state = 'queued' AND run_after <= now())), 0)::float8 AS oldest_due_seconds,
    coalesce(EXTRACT(EPOCH FROM now() - min(run_after) FILTER (WHERE state = 'queued' AND run_after <= now() AND priority <= @new_priority::int)), 0)::float8 AS oldest_new_seconds
FROM jobs
WHERE state <> 'done'
GROUP BY kind
ORDER BY kind;

-- name: RetryFailedJobs :execrows
-- `jobs retry KIND`: failed jobs of the kind are queued again from scratch.
UPDATE jobs
SET state = 'queued', attempts = 0, last_error = NULL, run_after = now(), started_at = NULL, finished_at = NULL
WHERE kind = @kind AND state = 'failed';
