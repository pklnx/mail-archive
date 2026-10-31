package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// Job priorities: lower runs first.
const (
	// PriorityNew is work caused by new mail.
	PriorityNew = 0
	// PriorityBackfill is work for mail archived before.
	PriorityBackfill = 100
)

// Job states.
const (
	JobQueued  = "queued"
	JobRunning = "running"
	JobDone    = "done"
	JobFailed  = "failed"
)

// JobsChannel is the channel EnqueueJobs notifies, with the kind as payload.
const JobsChannel = "mail_archive_jobs"

// JobListenerApp is the application_name of a pool's LISTEN connection.
const JobListenerApp = "mail-archive jobs listener"

// MaxJobPayload is the largest payload in bytes: a cursor or identifiers,
// never message content.
const MaxJobPayload = 1024

// maxJobKey is the longest job key in characters.
const maxJobKey = 200

var jobKindPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// ValidJobKind reports whether kind is a valid job kind name: a lower-case
// letter, then up to 39 lower-case letters, digits or underscores.
func ValidJobKind(kind string) bool { return jobKindPattern.MatchString(kind) }

// NewJob is a job to enqueue.
type NewJob struct {
	Kind string
	// Key identifies the job within its kind (a SHA-256, "v1"); never
	// message content.
	Key string
	// MessageSHA256 is the message the job is about, if any: deleting the
	// message deletes the job.
	MessageSHA256 string
	Priority      int
	MaxAttempts   int
	// RunAfter is when the job becomes due; zero means now (the
	// database's clock).
	RunAfter time.Time
	// Payload is a cursor or identifiers (JSON); nil means {}.
	Payload json.RawMessage
}

func (j *NewJob) params(retryFailed bool) (db.EnqueueJobParams, error) {
	switch {
	case !ValidJobKind(j.Kind):
		return db.EnqueueJobParams{}, fmt.Errorf("job kind %q is not valid", j.Kind)
	case j.Key == "" || len([]rune(j.Key)) > maxJobKey:
		return db.EnqueueJobParams{}, fmt.Errorf("job key of kind %s must have 1 to %d characters", j.Kind, maxJobKey)
	case j.MaxAttempts < 1 || j.MaxAttempts > 100:
		return db.EnqueueJobParams{}, fmt.Errorf("job kind %s: max attempts must be 1 to 100", j.Kind)
	case j.Priority < math.MinInt16 || j.Priority > math.MaxInt16:
		return db.EnqueueJobParams{}, fmt.Errorf("job kind %s: priority out of range", j.Kind)
	case len(j.Payload) > MaxJobPayload:
		return db.EnqueueJobParams{}, fmt.Errorf("job kind %s: payload over %d bytes", j.Kind, MaxJobPayload)
	}
	p := db.EnqueueJobParams{
		Kind: j.Kind, Key: j.Key, Priority: int16(j.Priority), MaxAttempts: int16(j.MaxAttempts),
		Payload: j.Payload, RetryFailed: retryFailed,
	}
	if p.Payload == nil {
		p.Payload = []byte("{}")
	}
	if j.MessageSHA256 != "" {
		p.MessageSha256 = &j.MessageSHA256
	}
	if !j.RunAfter.IsZero() {
		p.RunAfter = &j.RunAfter
	}
	return p, nil
}

// EnqueueJobs enqueues jobs in one transaction and returns how many are
// queued afterwards. Enqueueing is idempotent per kind and key: a queued
// job only moves forward (lower priority, earlier due time, but a pending
// retry keeps its time); a done one is queued again from scratch with the
// new payload; a running one is left alone; a failed one too, unless
// retryFailed. The pools listening are woken at commit, once per kind.
func (s *Store) EnqueueJobs(ctx context.Context, jobs []NewJob, retryFailed bool) (int, error) {
	var n int
	err := s.inTx(ctx, func(q *db.Queries) error {
		var err error
		n, err = enqueueJobs(ctx, q, jobs, retryFailed)
		return err
	})
	return n, err
}

// enqueueJobs is EnqueueJobs in the caller's transaction q, so that a job
// is queued together with what caused it.
func enqueueJobs(ctx context.Context, q *db.Queries, jobs []NewJob, retryFailed bool) (int, error) {
	n := 0
	var kinds []string
	for i := range jobs {
		p, err := jobs[i].params(retryFailed)
		if err != nil {
			return 0, err
		}
		if _, err := q.EnqueueJob(ctx, p); errors.Is(err, pgx.ErrNoRows) {
			continue
		} else if err != nil {
			return 0, fmt.Errorf("enqueue %s job: %w", p.Kind, err)
		}
		n++
		if !slices.Contains(kinds, p.Kind) {
			kinds = append(kinds, p.Kind)
		}
	}
	for _, k := range kinds {
		if err := q.NotifyJobs(ctx, k); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// ClaimedJob is a job a pool holds a lease on.
type ClaimedJob struct {
	ID            int64
	Kind          string
	Key           string
	MessageSHA256 string
	Priority      int
	Payload       json.RawMessage
	// Attempt is this claim's number since the job was enqueued, from 1.
	Attempt     int
	MaxAttempts int
	Lease       []byte
}

// ClaimJobs leases up to limit due jobs of kinds for leaseFor, in priority
// order, and counts the attempt. Jobs another pool holds are skipped.
func (s *Store) ClaimJobs(ctx context.Context, kinds []string, lease []byte, leaseFor time.Duration, limit int) ([]ClaimedJob, error) {
	if len(kinds) == 0 || limit < 1 {
		return nil, nil
	}
	rows, err := s.q.ClaimJobs(ctx, db.ClaimJobsParams{Lease: lease, LeaseSeconds: leaseFor.Seconds(), Kinds: kinds, MaxRows: clampInt32(limit)})
	if err != nil {
		return nil, err
	}
	// RETURNING has no order: the claim's order, so that a pool starts
	// the most urgent job first.
	slices.SortFunc(rows, func(a, b db.ClaimJobsRow) int {
		return cmp.Or(cmp.Compare(a.Priority, b.Priority), a.RunAfter.Compare(b.RunAfter), cmp.Compare(a.ID, b.ID))
	})
	out := make([]ClaimedJob, 0, len(rows))
	for _, r := range rows {
		out = append(out, ClaimedJob{
			ID: r.ID, Kind: r.Kind, Key: r.Key, MessageSHA256: deref(r.MessageSha256), Priority: int(r.Priority),
			Payload: r.Payload, Attempt: int(r.Attempts), MaxAttempts: int(r.MaxAttempts), Lease: lease,
		})
	}
	return out, nil
}

// ExtendJobLeases extends the leases of the jobs (ids[i] with leases[i])
// by leaseFor and returns the IDs whose lease is still held. A job
// missing from the result lost its lease.
func (s *Store) ExtendJobLeases(ctx context.Context, ids []int64, leases [][]byte, leaseFor time.Duration) (map[int64]bool, error) {
	held := make(map[int64]bool, len(ids))
	if len(ids) == 0 {
		return held, nil
	}
	rows, err := s.q.ExtendJobLeases(ctx, db.ExtendJobLeasesParams{LeaseSeconds: leaseFor.Seconds(), Ids: ids, Leases: leases})
	if err != nil {
		return nil, err
	}
	for _, id := range rows {
		held[id] = true
	}
	return held, nil
}

// FinishJob marks a job done. It returns ErrNotFound if the lease was
// lost.
func (s *Store) FinishJob(ctx context.Context, id int64, lease []byte) error {
	return one(s.q.FinishJob(ctx, db.FinishJobParams{ID: id, Lease: lease}))
}

// JobBackoff is the retry delay of a kind: first after the first attempt,
// doubled per attempt up to limit, and from half of that to all of it
// (equal jitter).
type JobBackoff struct {
	First, Cap time.Duration
}

// RetryJob records a failed attempt with code (never message content):
// the job fails if it used its attempts or permanent is set, else it is
// queued again after the backoff. It returns the new state and due time,
// or ErrNotFound if the lease was lost.
func (s *Store) RetryJob(ctx context.Context, id int64, lease []byte, code string, permanent bool, b JobBackoff) (string, time.Time, error) {
	r, err := s.q.RetryJob(ctx, db.RetryJobParams{
		ID: id, Lease: lease, Code: code, Permanent: permanent, FirstSeconds: b.First.Seconds(), CapSeconds: b.Cap.Seconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, ErrNotFound
	}
	return r.State, r.RunAfter, err
}

// ReapedJob is a job whose lease expired.
type ReapedJob struct {
	ID       int64
	Kind     string
	Key      string
	State    string // queued again, or failed
	Attempts int
}

// ReapExpiredJobs retries up to limit running jobs of kind whose lease
// expired, as after an error with code "lease expired": the attempt of
// the process that held it counts.
func (s *Store) ReapExpiredJobs(ctx context.Context, kind string, b JobBackoff, limit int) ([]ReapedJob, error) {
	rows, err := s.q.ReapExpiredJobs(ctx, db.ReapExpiredJobsParams{
		Kind: kind, FirstSeconds: b.First.Seconds(), CapSeconds: b.Cap.Seconds(), MaxRows: clampInt32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]ReapedJob, 0, len(rows))
	for _, r := range rows {
		out = append(out, ReapedJob{ID: r.ID, Kind: r.Kind, Key: r.Key, State: r.State, Attempts: int(r.Attempts)})
	}
	return out, nil
}

// ReleaseJob queues a job again at once without counting the attempt, at
// shutdown. It returns ErrNotFound if the lease was lost.
func (s *Store) ReleaseJob(ctx context.Context, id int64, lease []byte) error {
	return one(s.q.ReleaseJob(ctx, db.ReleaseJobParams{ID: id, Lease: lease}))
}

// ContinueJob queues a job again at once with a new payload (a cursor)
// and its attempts reset, for a handler that did part of its work. It
// returns ErrNotFound if the lease was lost.
func (s *Store) ContinueJob(ctx context.Context, id int64, lease []byte, payload json.RawMessage) error {
	if payload == nil {
		payload = []byte("{}")
	}
	if len(payload) > MaxJobPayload {
		return fmt.Errorf("job payload over %d bytes", MaxJobPayload)
	}
	return one(s.q.ContinueJob(ctx, db.ContinueJobParams{ID: id, Lease: lease, Payload: payload}))
}

// NextJobDue returns how long until the next queued job of kinds is due,
// or the next lease of kinds not in own expires (zero or less if that has
// passed), measured on the database's clock. ok is false when there is
// neither.
func (s *Store) NextJobDue(ctx context.Context, kinds []string, own [][]byte) (wait time.Duration, ok bool, err error) {
	if len(kinds) == 0 {
		return 0, false, nil
	}
	if own == nil {
		own = [][]byte{}
	}
	secs, err := s.q.NextJobDue(ctx, db.NextJobDueParams{Kinds: kinds, Own: own})
	if err != nil || math.IsInf(secs, 1) {
		return 0, false, err
	}
	if secs > (100 * 365 * 24 * time.Hour).Seconds() {
		secs = (100 * 365 * 24 * time.Hour).Seconds()
	}
	return time.Duration(secs * float64(time.Second)), true, nil
}

// DeleteDoneJobs removes done jobs that finished more than keep ago and
// returns how many.
func (s *Store) DeleteDoneJobs(ctx context.Context, keep time.Duration) (int, error) {
	total := 0
	for {
		n, err := s.q.DeleteDoneJobs(ctx, keep.Seconds())
		total += int(n)
		if err != nil || n < 1000 {
			return total, err
		}
	}
}

// JobStat is the queue of one kind with work that is not done.
type JobStat struct {
	Kind string
	// Queued counts the queued jobs without a pending retry: due, or
	// enqueued for later.
	Queued int64
	// Retrying counts the queued jobs that wait for a retry.
	Retrying int64
	Running  int64
	Failed   int64
	// OldestDue is how long the oldest due job has been due; OldestNew the
	// same for jobs of new mail (PriorityNew). Zero when there is none.
	OldestDue time.Duration
	OldestNew time.Duration
}

// JobStats returns the queue per kind, by kind name.
func (s *Store) JobStats(ctx context.Context) ([]JobStat, error) {
	rows, err := s.q.JobStats(ctx, PriorityNew)
	if err != nil {
		return nil, err
	}
	out := make([]JobStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, JobStat{
			Kind: r.Kind, Queued: r.Queued, Retrying: r.Retrying, Running: r.Running, Failed: r.Failed,
			OldestDue: secondsDuration(r.OldestDueSeconds), OldestNew: secondsDuration(r.OldestNewSeconds),
		})
	}
	return out, nil
}

func secondsDuration(s float64) time.Duration {
	if s <= 0 {
		return 0
	}
	return time.Duration(s * float64(time.Second))
}

// RetryFailedJobs queues the failed jobs of kind again from scratch and
// returns how many, together with the audit entry of ctx (with the
// number in "jobs"); the pools
// listening are woken at commit.
func (s *Store) RetryFailedJobs(ctx context.Context, kind string) (int, error) {
	if !ValidJobKind(kind) {
		return 0, fmt.Errorf("job kind %q is not valid", kind)
	}
	var n int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		var err error
		if n, err = q.RetryFailedJobs(ctx, kind); err != nil {
			return err
		}
		if e := auditFrom(ctx); e != nil {
			e.Detail("jobs", n)
		}
		if n == 0 {
			return nil
		}
		return q.NotifyJobs(ctx, kind)
	})
	return int(n), err
}

// JobListener receives the notifications of EnqueueJobs on a connection
// of its own, outside the pool.
type JobListener struct {
	conn *pgx.Conn
}

// ListenJobs opens a connection with the store's settings (application_name
// JobListenerApp) and listens on JobsChannel.
func (s *Store) ListenJobs(ctx context.Context) (*JobListener, error) {
	cfg := s.pool.Config().ConnConfig.Copy()
	cfg.RuntimeParams["application_name"] = JobListenerApp
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, "LISTEN "+JobsChannel); err != nil {
		_ = conn.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	return &JobListener{conn: conn}, nil
}

// Wait returns the kind of the next notification.
func (l *JobListener) Wait(ctx context.Context) (string, error) {
	n, err := l.conn.WaitForNotification(ctx)
	if err != nil {
		return "", err
	}
	return n.Payload, nil
}

// Close closes the connection.
func (l *JobListener) Close(ctx context.Context) error { return l.conn.Close(ctx) }

// SQLState returns the SQLSTATE of a PostgreSQL error in err's chain.
func SQLState(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, true
	}
	return "", false
}
