package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// jobsFixture is a migrated database for queue tests. Time-dependent
// states are set directly in the database instead of waiting.
type jobsFixture struct {
	t    *testing.T
	ctx  context.Context
	st   *store.Store
	url  string
	conn *pgx.Conn
}

func newJobsFixture(t *testing.T) *jobsFixture {
	t.Helper()
	st, url := storetest.NewWithURL(t)
	return &jobsFixture{t: t, ctx: context.Background(), st: st, url: url, conn: connect(t, url)}
}

var testBackoff = store.JobBackoff{First: 30 * time.Second, Cap: time.Hour}

func (f *jobsFixture) enqueue(jobs ...store.NewJob) int {
	f.t.Helper()
	n, err := f.st.EnqueueJobs(f.ctx, jobs, false)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

func testJob(key string, priority int) store.NewJob {
	return store.NewJob{Kind: "test", Key: key, Priority: priority, MaxAttempts: 3}
}

func (f *jobsFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.conn.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

// jobRow is a job as stored.
type jobRow struct {
	ID         int64
	State      string
	Priority   int
	Attempts   int
	Payload    string
	LastError  *string
	DueIn      time.Duration // run_after - now()
	HasLease   bool
	Message    *string
	LeaseUntil *time.Time
}

func (f *jobsFixture) job(kind, key string) jobRow {
	f.t.Helper()
	var r jobRow
	var dueIn float64
	err := f.conn.QueryRow(f.ctx, `SELECT id, state, priority, attempts, payload::text, last_error,
		EXTRACT(EPOCH FROM run_after - now())::float8, lease_token IS NOT NULL, message_sha256, lease_until
		FROM jobs WHERE kind = $1 AND key = $2`, kind, key).
		Scan(&r.ID, &r.State, &r.Priority, &r.Attempts, &r.Payload, &r.LastError, &dueIn, &r.HasLease, &r.Message, &r.LeaseUntil)
	if err != nil {
		f.t.Fatalf("job %s/%s: %v", kind, key, err)
	}
	r.DueIn = time.Duration(dueIn * float64(time.Second))
	return r
}

func (f *jobsFixture) count(where string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.conn.QueryRow(f.ctx, "SELECT count(*) FROM jobs WHERE "+where, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// expireLease moves a running job's lease into the past.
func (f *jobsFixture) expireLease(id int64) {
	f.exec(`UPDATE jobs SET lease_until = now() - interval '1 second' WHERE id = $1 AND state = 'running'`, id)
}

// makeDue makes a queued job due now.
func (f *jobsFixture) makeDue(id int64) {
	f.exec(`UPDATE jobs SET run_after = now() WHERE id = $1`, id)
}

// overdue makes a queued job due for d already.
func (f *jobsFixture) overdue(id int64, d time.Duration) {
	f.exec(`UPDATE jobs SET run_after = now() - make_interval(secs => $2), created_at = now() - make_interval(secs => $2)
		WHERE id = $1`, id, d.Seconds())
}

func (f *jobsFixture) claim(limit int, kinds ...string) []store.ClaimedJob {
	f.t.Helper()
	if len(kinds) == 0 {
		kinds = []string{"test"}
	}
	got, err := f.st.ClaimJobs(f.ctx, kinds, newTestLease(), time.Minute, limit)
	if err != nil {
		f.t.Fatal(err)
	}
	return got
}

var leaseSeq byte

func newTestLease() []byte {
	leaseSeq++
	return []byte(strings.Repeat(string(rune('a'+leaseSeq%26)), 15) + string([]byte{leaseSeq}))
}

func (f *jobsFixture) message(c byte) string {
	f.t.Helper()
	sha := hashOf(c)
	f.exec(`INSERT INTO messages (sha256, size, stored_path) VALUES ($1, 1, $2)`, sha, "messages/"+sha+".eml")
	return sha
}

// downJobsMigration rolls back 00033_attachment_text and 00032_jobs, for
// tests of older migrations.
func downJobsMigration(t *testing.T, st *store.Store) {
	t.Helper()
	downAttachmentTextMigration(t, st)
	if m, err := st.MigrateDown(context.Background()); err != nil || !strings.Contains(m, "00032_jobs") {
		t.Fatalf("down: %q %v", m, err)
	}
}

// deleteMessage deletes a message as retention does, with its tombstone.
func (f *jobsFixture) deleteMessage(sha string) {
	f.t.Helper()
	f.exec(`WITH r AS (INSERT INTO deletion_runs (state, confirmed_at, finished_at) VALUES ('done', now(), now()) RETURNING id)
		INSERT INTO deleted_messages (sha256, size, message_seq, archived_at, retain_until, locations, run_id)
		SELECT m.sha256, m.size, 1, m.created_at, now(), 1, r.id FROM messages m, r WHERE m.sha256 = $1`, sha)
	f.exec(`DELETE FROM messages WHERE sha256 = $1`, sha)
}

func TestEnqueueJobsIdempotent(t *testing.T) {
	f := newJobsFixture(t)
	if n := f.enqueue(testJob("a", store.PriorityBackfill), testJob("a", store.PriorityBackfill)); n != 2 {
		t.Fatalf("queued %d, want 2 (one row, twice queued)", n)
	}
	if n := f.count("kind = 'test'"); n != 1 {
		t.Fatalf("%d rows for one key", n)
	}
	// A lower priority and an earlier due time move a queued job forward.
	later := testJob("b", store.PriorityBackfill)
	later.RunAfter = time.Now().Add(time.Hour)
	f.enqueue(later)
	earlier := testJob("b", store.PriorityNew)
	f.enqueue(earlier)
	if r := f.job("test", "b"); r.Priority != store.PriorityNew || r.DueIn > time.Minute {
		t.Errorf("not moved forward: %+v", r)
	}
	// A higher priority and a later time do not move it back.
	f.enqueue(later)
	if r := f.job("test", "b"); r.Priority != store.PriorityNew || r.DueIn > time.Minute {
		t.Errorf("moved back: %+v", r)
	}

	// A pending retry keeps its time; only the priority moves.
	retry := testJob("r", store.PriorityBackfill)
	f.enqueue(retry)
	c := f.claim(10)
	for _, j := range c {
		if j.Key == "r" {
			if _, _, err := f.st.RetryJob(f.ctx, j.ID, j.Lease, "internal", false, testBackoff); err != nil {
				t.Fatal(err)
			}
		} else if err := f.st.ReleaseJob(f.ctx, j.ID, j.Lease); err != nil {
			t.Fatal(err)
		}
	}
	before := f.job("test", "r")
	f.enqueue(testJob("r", store.PriorityNew))
	if r := f.job("test", "r"); r.DueIn < 10*time.Second || r.Priority != store.PriorityNew || r.Attempts != 1 {
		t.Errorf("retry skipped by enqueue: before %+v after %+v", before, r)
	}

	// done: queued again from scratch, with the new payload.
	d := testJob("d", store.PriorityNew)
	d.Payload = json.RawMessage(`{"cursor":1}`)
	f.enqueue(d)
	f.exec(`UPDATE jobs SET state = 'done', attempts = 2, last_error = 'x', finished_at = now() WHERE key = 'd'`)
	d.Payload = json.RawMessage(`{}`)
	if n := f.enqueue(d); n != 1 {
		t.Errorf("done not queued: %d", n)
	}
	if r := f.job("test", "d"); r.State != store.JobQueued || r.Attempts != 0 || r.LastError != nil || r.Payload != "{}" {
		t.Errorf("done enqueued: %+v", r)
	}

	// running: unchanged.
	f.enqueue(testJob("run", store.PriorityBackfill))
	f.exec(`UPDATE jobs SET state = 'running', lease_token = '\x01', lease_until = now() + interval '1 minute', attempts = 1 WHERE key = 'run'`)
	if n := f.enqueue(testJob("run", store.PriorityNew)); n != 0 {
		t.Errorf("running counted as queued: %d", n)
	}
	if r := f.job("test", "run"); r.State != store.JobRunning || r.Priority != store.PriorityBackfill || !r.HasLease {
		t.Errorf("running changed: %+v", r)
	}

	// failed: unchanged, unless retryFailed.
	f.enqueue(testJob("f", store.PriorityBackfill))
	f.exec(`UPDATE jobs SET state = 'failed', attempts = 3, last_error = 'panic', finished_at = now() WHERE key = 'f'`)
	if n := f.enqueue(testJob("f", store.PriorityNew)); n != 0 || f.job("test", "f").State != store.JobFailed {
		t.Errorf("failed re-queued without retryFailed: %d %+v", n, f.job("test", "f"))
	}
	if n, err := f.st.EnqueueJobs(f.ctx, []store.NewJob{testJob("f", store.PriorityNew)}, true); err != nil || n != 1 {
		t.Fatalf("retryFailed: %d %v", n, err)
	}
	if r := f.job("test", "f"); r.State != store.JobQueued || r.Attempts != 0 || r.LastError != nil {
		t.Errorf("failed with retryFailed: %+v", r)
	}

	// Invalid jobs are refused before the database.
	for _, bad := range []store.NewJob{
		{Kind: "Bad", Key: "k", MaxAttempts: 1},
		{Kind: "test", Key: "", MaxAttempts: 1},
		{Kind: "test", Key: "k", MaxAttempts: 0},
		{Kind: "test", Key: "k", MaxAttempts: 1, Payload: json.RawMessage(`"` + strings.Repeat("x", 1100) + `"`)},
	} {
		if _, err := f.st.EnqueueJobs(f.ctx, []store.NewJob{bad}, false); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// TestEnqueueJobsNotifiesOncePerKind: a bulk enqueue in one transaction
// sends one notification for its kind; a second transaction (the
// sentinel) arrives after it.
func TestEnqueueJobsNotifiesOncePerKind(t *testing.T) {
	f := newJobsFixture(t)
	l, err := f.st.ListenJobs(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close(context.Background()) })
	bulk := make([]store.NewJob, 500)
	for i := range bulk {
		bulk[i] = testJob(fmt.Sprintf("k%d", i), store.PriorityBackfill)
	}
	if _, err := f.st.EnqueueJobs(f.ctx, bulk, false); err != nil {
		t.Fatal(err)
	}
	f.enqueue(store.NewJob{Kind: "sentinel", Key: "s", MaxAttempts: 1})
	ctx, cancel := context.WithTimeout(f.ctx, testwait.Limit(t))
	defer cancel()
	var got []string
	for {
		kind, err := l.Wait(ctx)
		if err != nil {
			t.Fatalf("after %v: %v", got, err)
		}
		got = append(got, kind)
		if kind == "sentinel" {
			break
		}
	}
	if strings.Join(got, ",") != "test,sentinel" {
		t.Errorf("notifications %v, want one for test, then the sentinel", got)
	}
}

func TestClaimJobsPriorityAndDue(t *testing.T) {
	f := newJobsFixture(t)
	f.enqueue(testJob("b1", store.PriorityBackfill), testJob("b2", store.PriorityBackfill), testJob("b3", store.PriorityBackfill))
	f.enqueue(testJob("new", store.PriorityNew))
	future := testJob("future", store.PriorityNew)
	future.RunAfter = time.Now().Add(time.Hour)
	f.enqueue(future)
	f.enqueue(store.NewJob{Kind: "other", Key: "o", Priority: store.PriorityNew, MaxAttempts: 1})
	// One at a time, so the order is the claim's, not only the sorted
	// result of one claim.
	var keys []string
	for _, j := range append(append(f.claim(1), f.claim(1)...), f.claim(10)...) {
		keys = append(keys, j.Key)
		if j.Attempt != 1 || j.MaxAttempts != 3 {
			t.Errorf("claimed %+v", j)
		}
	}
	if strings.Join(keys, ",") != "new,b1,b2,b3" {
		t.Errorf("claim order %v", keys)
	}
	if r := f.job("test", "b1"); r.State != store.JobRunning || r.Attempts != 1 || !r.HasLease {
		t.Errorf("claimed row %+v", r)
	}
	if len(f.claim(10)) != 0 {
		t.Error("claimed again")
	}
}

// TestClaimJobsSkipsLockedRows: a row another transaction locks is
// skipped, not waited for.
func TestClaimJobsSkipsLockedRows(t *testing.T) {
	f := newJobsFixture(t)
	f.enqueue(testJob("a", store.PriorityNew), testJob("b", store.PriorityNew))
	tx, err := f.conn.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	if _, err := tx.Exec(f.ctx, `SELECT id FROM jobs WHERE key = 'a' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	other, err := store.Open(f.ctx, f.url, store.WithSessionSettings("SET lock_timeout = '1ms'"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	got, err := other.ClaimJobs(f.ctx, []string{"test"}, newTestLease(), time.Minute, 1)
	if err != nil || len(got) != 1 || got[0].Key != "b" {
		t.Fatalf("claim beside a lock: %+v %v", got, err)
	}
}

func TestJobLeaseExpiry(t *testing.T) {
	f := newJobsFixture(t)
	f.enqueue(testJob("a", store.PriorityNew))
	f.enqueue(store.NewJob{Kind: "other", Key: "o", MaxAttempts: 1})
	other := f.claim(1, "other")[0]
	f.expireLease(other.ID)
	for attempt := 1; attempt <= 3; attempt++ {
		c := f.claim(1)
		if len(c) != 1 || c[0].Attempt != attempt {
			t.Fatalf("claim %d: %+v", attempt, c)
		}
		if reaped, err := f.st.ReapExpiredJobs(f.ctx, "test", testBackoff, nil, 100); err != nil || len(reaped) != 0 {
			t.Fatalf("reaped a live lease: %+v %v", reaped, err)
		}
		f.expireLease(c[0].ID)
		reaped, err := f.st.ReapExpiredJobs(f.ctx, "test", testBackoff, nil, 100)
		if err != nil || len(reaped) != 1 {
			t.Fatalf("reap %d: %+v %v", attempt, reaped, err)
		}
		r := f.job("test", "a")
		if r.LastError == nil || *r.LastError != "lease expired" || r.Attempts != attempt || r.HasLease {
			t.Errorf("after expiry %d: %+v", attempt, r)
		}
		if attempt < 3 {
			lo, hi := time.Duration(15<<(attempt-1))*time.Second, time.Duration(30<<(attempt-1))*time.Second
			if r.State != store.JobQueued || r.DueIn < lo-2*time.Second || r.DueIn > hi {
				t.Errorf("after expiry %d: %v due in %v, want [%v, %v]", attempt, r.State, r.DueIn, lo, hi)
			}
			f.makeDue(r.ID)
		} else if r.State != store.JobFailed {
			t.Errorf("third expiry: %+v", r)
		}
	}
	if len(f.claim(1)) != 0 {
		t.Error("failed job claimed")
	}
	// The reap of kind test left the other kind's expired lease alone.
	if r := f.job("other", "o"); r.State != store.JobRunning {
		t.Errorf("other kind reaped: %+v", r)
	}
}

func TestRetryJobBackoff(t *testing.T) {
	f := newJobsFixture(t)
	f.enqueue(testJob("a", store.PriorityNew))
	for i, want := range []struct{ lo, hi time.Duration }{{15 * time.Second, 30 * time.Second}, {30 * time.Second, 60 * time.Second}} {
		c := f.claim(1)[0]
		state, next, err := f.st.RetryJob(f.ctx, c.ID, c.Lease, "db 40001", false, testBackoff)
		if err != nil || state != store.JobQueued {
			t.Fatalf("retry %d: %v %v", i+1, state, err)
		}
		r := f.job("test", "a")
		if r.DueIn < want.lo-2*time.Second || r.DueIn > want.hi || next.IsZero() {
			t.Errorf("failure %d: due in %v, want [%v, %v]", i+1, r.DueIn, want.lo, want.hi)
		}
		if r.LastError == nil || *r.LastError != "db 40001" {
			t.Errorf("last_error %v", r.LastError)
		}
		// A lost lease records nothing.
		if _, _, err := f.st.RetryJob(f.ctx, c.ID, c.Lease, "x", false, testBackoff); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("retry without lease: %v", err)
		}
		f.makeDue(r.ID)
	}
	c := f.claim(1)[0]
	if err := f.st.FinishJob(f.ctx, c.ID, c.Lease); err != nil {
		t.Fatal(err)
	}
	if r := f.job("test", "a"); r.State != store.JobDone || r.LastError != nil || r.HasLease {
		t.Errorf("done: %+v", r)
	}
	// The cap bounds the delay.
	f.enqueue(testJob("cap", store.PriorityNew))
	f.exec(`UPDATE jobs SET attempts = 30, max_attempts = 100 WHERE key = 'cap'`)
	c = f.claim(1)[0]
	if _, _, err := f.st.RetryJob(f.ctx, c.ID, c.Lease, "x", false, store.JobBackoff{First: time.Second, Cap: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if r := f.job("test", "cap"); r.DueIn > time.Minute || r.DueIn < 28*time.Second {
		t.Errorf("capped delay %v", r.DueIn)
	}
	// A permanent error fails at once.
	f.enqueue(testJob("perm", store.PriorityNew))
	c = f.claim(1)[0]
	if state, _, err := f.st.RetryJob(f.ctx, c.ID, c.Lease, "encrypted", true, testBackoff); err != nil || state != store.JobFailed {
		t.Fatalf("permanent: %v %v", state, err)
	}
}

func TestReleaseAndContinueJob(t *testing.T) {
	f := newJobsFixture(t)
	f.enqueue(testJob("a", store.PriorityNew))
	for range 3 {
		c := f.claim(1)[0]
		if err := f.st.ReleaseJob(f.ctx, c.ID, c.Lease); err != nil {
			t.Fatal(err)
		}
	}
	if r := f.job("test", "a"); r.State != store.JobQueued || r.Attempts != 0 || r.HasLease || r.DueIn > 0 {
		t.Errorf("released three times: %+v", r)
	}
	for i, payload := range []string{`{"c": 1}`, `{"c": 2}`} {
		c := f.claim(1)[0]
		if c.Attempt != 1 {
			t.Errorf("continue %d: attempt %d", i, c.Attempt)
		}
		if err := f.st.ContinueJob(f.ctx, c.ID, c.Lease, json.RawMessage(payload)); err != nil {
			t.Fatal(err)
		}
		if err := f.st.ContinueJob(f.ctx, c.ID, c.Lease, json.RawMessage(`{}`)); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("continue without lease: %v", err)
		}
		if r := f.job("test", "a"); r.State != store.JobQueued || r.Payload != payload || r.Attempts != 0 {
			t.Errorf("continued: %+v", r)
		}
	}
	c := f.claim(1)[0]
	if string(c.Payload) != `{"c": 2}` {
		t.Errorf("payload %s", c.Payload)
	}
	if err := f.st.ReleaseJob(f.ctx, c.ID, []byte("stolen")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("release with a wrong lease: %v", err)
	}
	if err := f.st.FinishJob(f.ctx, c.ID, []byte("stolen")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("finish with a wrong lease: %v", err)
	}
}

func TestExtendJobLeases(t *testing.T) {
	f := newJobsFixture(t)
	f.enqueue(testJob("a", store.PriorityNew), testJob("b", store.PriorityNew))
	c := f.claim(2)
	f.exec(`UPDATE jobs SET lease_until = now() + interval '5 seconds'`)
	held, err := f.st.ExtendJobLeases(f.ctx, []int64{c[0].ID, c[1].ID}, [][]byte{c[0].Lease, []byte("other")}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(held[c[0].ID], c[0].Lease) || held[c[1].ID] != nil {
		t.Errorf("held %v", held)
	}
	if r := f.job("test", c[0].Key); r.LeaseUntil == nil || time.Until(*r.LeaseUntil) < 30*time.Second {
		t.Errorf("not extended: %+v", r)
	}
	if r := f.job("test", c[1].Key); time.Until(*r.LeaseUntil) > 10*time.Second {
		t.Errorf("extended with a wrong lease: %v", r.LeaseUntil)
	}
}

func TestDeleteDoneJobsAndCascade(t *testing.T) {
	f := newJobsFixture(t)
	m1, m2 := f.message('1'), f.message('2')
	f.enqueue(testJob("old", 0), testJob("young", 0), testJob("failed", 0), testJob("queued", 0))
	f.exec(`UPDATE jobs SET state = 'done', finished_at = now() - interval '25 hours' WHERE key = 'old'`)
	f.exec(`UPDATE jobs SET state = 'done', finished_at = now() - interval '23 hours' WHERE key = 'young'`)
	f.exec(`UPDATE jobs SET state = 'failed', finished_at = now() - interval '30 days' WHERE key = 'failed'`)
	if n, err := f.st.DeleteDoneJobs(f.ctx, 24*time.Hour); err != nil || n != 1 {
		t.Fatalf("deleted %d %v", n, err)
	}
	if n := f.count("kind = 'test'"); n != 3 || f.count("key = 'old'") != 0 {
		t.Errorf("left %d rows", n)
	}

	j1 := testJob("m1", 0)
	j1.MessageSHA256 = m1
	j2 := testJob("m2", 0)
	j2.MessageSHA256 = m2
	f.enqueue(j1, j2)
	f.deleteMessage(m1)
	if f.count("key = 'm1'") != 0 || f.count("key = 'm2'") != 1 {
		t.Error("cascade")
	}
}

func TestJobStatsAndNextDue(t *testing.T) {
	f := newJobsFixture(t)
	if s, err := f.st.JobStats(f.ctx); err != nil || len(s) != 0 {
		t.Fatalf("empty: %+v %v", s, err)
	}
	if _, ok, err := f.st.NextJobDue(f.ctx, []string{"test"}, nil); ok || err != nil {
		t.Errorf("next due without jobs: %v %v", ok, err)
	}
	f.enqueue(testJob("new", store.PriorityNew), testJob("back", store.PriorityBackfill), testJob("run", store.PriorityBackfill),
		testJob("retry", store.PriorityBackfill), testJob("fail", store.PriorityBackfill), testJob("done", store.PriorityBackfill))
	f.enqueue(store.NewJob{Kind: "other", Key: "x", MaxAttempts: 1})
	f.overdue(f.job("test", "new").ID, 10*time.Minute)
	f.overdue(f.job("test", "back").ID, time.Hour)
	f.exec(`UPDATE jobs SET state = 'running', attempts = 1, lease_token = '\x01', lease_until = now() + interval '1 minute' WHERE key = 'run'`)
	f.exec(`UPDATE jobs SET attempts = 1, run_after = now() + interval '10 minutes' WHERE key = 'retry'`)
	f.exec(`UPDATE jobs SET state = 'failed', finished_at = now() WHERE key = 'fail'`)
	f.exec(`UPDATE jobs SET state = 'done', finished_at = now() WHERE key IN ('done', 'x')`)
	stats, err := f.st.JobStats(f.ctx)
	if err != nil || len(stats) != 1 {
		t.Fatalf("stats %+v %v", stats, err)
	}
	s := stats[0]
	if s.Kind != "test" || s.Queued != 2 || s.Retrying != 1 || s.Running != 1 || s.Failed != 1 {
		t.Errorf("stats %+v", s)
	}
	if s.OldestDue < 59*time.Minute || s.OldestDue > 61*time.Minute || s.OldestNew < 9*time.Minute || s.OldestNew > 11*time.Minute {
		t.Errorf("ages %+v", s)
	}

	// Due work is due now (negative); a pool's own lease is not waited for,
	// nor are kinds it does not run.
	if wait, ok, err := f.st.NextJobDue(f.ctx, []string{"test"}, nil); !ok || err != nil || wait > 0 {
		t.Errorf("due: %v %v %v", wait, ok, err)
	}
	f.exec(`DELETE FROM jobs WHERE state = 'queued' AND run_after <= now()`)
	if wait, ok, _ := f.st.NextJobDue(f.ctx, []string{"test"}, nil); !ok || wait > time.Minute+time.Second || wait < 50*time.Second {
		t.Errorf("next is the lease: %v %v", wait, ok)
	}
	if wait, ok, _ := f.st.NextJobDue(f.ctx, []string{"test"}, [][]byte{{1}}); !ok || wait < 9*time.Minute {
		t.Errorf("own lease waited for: %v %v", wait, ok)
	}
	if _, ok, _ := f.st.NextJobDue(f.ctx, []string{"nothing"}, nil); ok {
		t.Error("other kinds waited for")
	}
}

func TestRetryFailedJobs(t *testing.T) {
	f := newJobsFixture(t)
	f.enqueue(testJob("a", 0), testJob("b", 0), testJob("c", 0))
	f.exec(`UPDATE jobs SET state = 'failed', attempts = 3, last_error = 'panic', finished_at = now() WHERE key IN ('a', 'b')`)
	e := &store.AuditEntry{Actor: store.ActorCLI, Action: "jobs.retry"}
	e.Detail("kind", "test")
	n, err := f.st.RetryFailedJobs(store.WithAudit(f.ctx, e), "test")
	if err != nil || n != 2 {
		t.Fatalf("retried %d %v", n, err)
	}
	if r := f.job("test", "a"); r.State != store.JobQueued || r.Attempts != 0 || r.LastError != nil {
		t.Errorf("retried: %+v", r)
	}
	var audits int
	if err := f.conn.QueryRow(f.ctx, `SELECT count(*) FROM audit_log WHERE action = 'jobs.retry' AND details->>'kind' = 'test' AND details->>'jobs' = '2'`).Scan(&audits); err != nil || audits != 1 {
		t.Errorf("audit entries %d %v", audits, err)
	}
	if _, err := f.st.RetryFailedJobs(f.ctx, "Bad kind"); err == nil {
		t.Error("invalid kind accepted")
	}
}

// TestJobsMigration: 00032 down drops the table, up creates it again; and
// creating it while another connection keeps inserting messages makes no
// insert wait past a lock_timeout of 1 s.
func TestJobsMigration(t *testing.T) {
	f := newJobsFixture(t)
	downAttachmentTextMigration(t, f.st)
	if m, err := f.st.MigrateDown(f.ctx); err != nil || !strings.Contains(m, "00032_jobs") {
		t.Fatalf("down: %q %v", m, err)
	}
	var exists bool
	if err := f.conn.QueryRow(f.ctx, "SELECT to_regclass('jobs') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("table after down: %v %v", exists, err)
	}

	writer := connect(t, f.url)
	if _, err := writer.Exec(f.ctx, "SET lock_timeout = '1s'"); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan error, 1)
	var inserted testwait.Signal
	var count atomic.Int64
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			if _, err := writer.Exec(f.ctx, `INSERT INTO messages (sha256, size, stored_path) VALUES (lpad($1::text, 64, '0'), 1, 'x')`,
				fmt.Sprint(i)); err != nil {
				done <- err
				return
			}
			count.Add(1)
			inserted.Notify()
		}
	}()
	failed := func() bool {
		select {
		case err := <-done:
			t.Fatalf("an insert failed during the migration: %v", err)
		default:
		}
		return false
	}
	testwait.Until(t, &inserted, "an insert before the migration", func() bool { return failed() || count.Load() > 0 })
	if _, err := f.st.Migrate(f.ctx); err != nil {
		t.Fatal(err)
	}
	after := count.Load()
	testwait.Until(t, &inserted, "an insert after the migration", func() bool { return failed() || count.Load() > after })
	close(stop)
	if err := <-done; err != nil {
		t.Fatalf("an insert failed during the migration: %v", err)
	}
	f.enqueue(testJob("after", 0))
}

// TestJobCompletionKeepsTakenOverLease: a pool whose lease another pool
// took over while its finish, retry, release or continue waited for the
// row lock records nothing; the new holder's lease stays.
func TestJobCompletionKeepsTakenOverLease(t *testing.T) {
	f := newJobsFixture(t)
	other := connect(t, f.url)
	for name, op := range map[string]func(c store.ClaimedJob) error{
		"retry": func(c store.ClaimedJob) error {
			_, _, err := f.st.RetryJob(f.ctx, c.ID, c.Lease, "internal", false, testBackoff)
			return err
		},
		"finish":  func(c store.ClaimedJob) error { return f.st.FinishJob(f.ctx, c.ID, c.Lease) },
		"release": func(c store.ClaimedJob) error { return f.st.ReleaseJob(f.ctx, c.ID, c.Lease) },
		"continue": func(c store.ClaimedJob) error {
			return f.st.ContinueJob(f.ctx, c.ID, c.Lease, json.RawMessage(`{"c":1}`))
		},
	} {
		f.enqueue(testJob(name, store.PriorityNew))
		c := f.claim(1)[0]
		// The other pool takes the job over: the lease expired, it reaped
		// and claimed it. Its transaction commits only after ours waits.
		tx, err := other.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(f.ctx, `UPDATE jobs SET lease_token = '\x0b0b', attempts = attempts + 1, lease_until = now() + interval '1 minute' WHERE id = $1`, c.ID); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- op(c) }()
		waitForLockWait(t, f.conn, done)
		if err := tx.Commit(f.ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s with the old lease: %v, want not found", name, err)
		}
		var state string
		var lease []byte
		if err := f.conn.QueryRow(f.ctx, `SELECT state, lease_token FROM jobs WHERE id = $1`, c.ID).Scan(&state, &lease); err != nil {
			t.Fatal(err)
		}
		if state != store.JobRunning || !bytes.Equal(lease, []byte{0x0b, 0x0b}) {
			t.Errorf("%s with the old lease changed the row: %s %x", name, state, lease)
		}
	}
}

// TestReapSkipsOwnLeases: a pool does not reap the expired leases it holds
// itself; another pool does.
func TestReapSkipsOwnLeases(t *testing.T) {
	f := newJobsFixture(t)
	f.enqueue(testJob("a", store.PriorityNew))
	c := f.claim(1)[0]
	f.expireLease(c.ID)
	if reaped, err := f.st.ReapExpiredJobs(f.ctx, "test", testBackoff, [][]byte{[]byte("x"), c.Lease}, 100); err != nil || len(reaped) != 0 {
		t.Fatalf("own lease reaped: %+v %v", reaped, err)
	}
	if reaped, err := f.st.ReapExpiredJobs(f.ctx, "test", testBackoff, [][]byte{[]byte("x")}, 100); err != nil || len(reaped) != 1 {
		t.Fatalf("foreign lease not reaped: %+v %v", reaped, err)
	}
}
