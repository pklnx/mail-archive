package jobs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// harness runs a pool against a test database. Time never passes in a
// test: the poll, the heartbeat and the due timer are channels the test
// fires, and due times are moved in the database.
type harness struct {
	t    *testing.T
	st   *store.Store
	url  string
	conn *pgx.Conn
	reg  *Registry
	pool *Pool

	poll, heartbeat, due chan time.Time

	mu        sync.Mutex
	waits     []waitInfo
	waitSig   testwait.Signal
	listens   atomic.Int32
	listenSig testwait.Signal
	notified  map[string]int
	notifySig testwait.Signal
	beats     atomic.Int32
	beatSig   testwait.Signal
	lost      []int64
	lostSig   testwait.Signal

	cancel context.CancelFunc
	done   chan error
}

type waitInfo struct {
	d  time.Duration
	ok bool
}

func newHarness(t *testing.T, workers int) *harness {
	t.Helper()
	st, url := storetest.NewWithURL(t)
	h := &harness{
		t: t, st: st, url: url, conn: dbConn(t, url), reg: &Registry{},
		poll: make(chan time.Time), heartbeat: make(chan time.Time), due: make(chan time.Time, 1),
		notified: map[string]int{},
	}
	h.pool = h.newPool(st, workers)
	return h
}

func dbConn(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func (h *harness) newPool(st *store.Store, workers int) *Pool {
	return &Pool{
		Store: st, Kinds: h.reg, Workers: workers, Logger: quietLogger(),
		poll: h.poll, heartbeat: h.heartbeat, cleanup: make(chan time.Time),
		after:          func(time.Duration) <-chan time.Time { return h.due },
		reconnectDelay: func(int) time.Duration { return 0 },
		onWait: func(d time.Duration, ok bool) {
			h.mu.Lock()
			h.waits = append(h.waits, waitInfo{d, ok})
			h.mu.Unlock()
			h.waitSig.Notify()
		},
		onListen: func() { h.listens.Add(1); h.listenSig.Notify() },
		onNotify: func(kind string) {
			h.mu.Lock()
			h.notified[kind]++
			h.mu.Unlock()
			h.notifySig.Notify()
		},
		onHeartbeat: func() { h.beats.Add(1); h.beatSig.Notify() },
		onLost: func(id int64) {
			h.mu.Lock()
			h.lost = append(h.lost, id)
			h.mu.Unlock()
			h.lostSig.Notify()
		},
	}
}

func (h *harness) register(k Kind) Kind {
	h.t.Helper()
	if err := h.reg.Register(k); err != nil {
		h.t.Fatal(err)
	}
	k, _ = h.reg.Kind(k.Name)
	return k
}

func (h *harness) start() {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel, h.done = cancel, make(chan error, 1)
	go func() { h.done <- h.pool.Run(ctx) }()
	h.t.Cleanup(func() {
		if h.cancel != nil {
			h.stop()
		}
	})
}

// stop cancels Run and waits for it to return.
func (h *harness) stop() {
	h.t.Helper()
	h.cancel()
	h.cancel = nil
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Fatal(err)
		}
	case <-time.After(testwait.Limit(h.t)):
		h.t.Fatal("Run did not return after the cancel")
	}
}

func (h *harness) waitCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.waits)
}

// nextWait returns the first wait after the n-th.
func (h *harness) nextWait(n int) waitInfo {
	h.t.Helper()
	testwait.Until(h.t, &h.waitSig, "the dispatcher to wait", func() bool { return h.waitCount() > n })
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.waits[n]
}

// waitWhere waits for a wait from the n-th on that matches.
func (h *harness) waitWhere(n int, what string, match func(waitInfo) bool) {
	h.t.Helper()
	testwait.Until(h.t, &h.waitSig, what, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, w := range h.waits[min(n, len(h.waits)):] {
			if match(w) {
				return true
			}
		}
		return false
	})
}

func (h *harness) listening(n int32) {
	h.t.Helper()
	testwait.Until(h.t, &h.listenSig, "the listener", func() bool { return h.listens.Load() >= n })
}

func (h *harness) fire(c chan time.Time, what string) {
	h.t.Helper()
	select {
	case c <- time.Now():
	case <-time.After(testwait.Limit(h.t)):
		h.t.Fatalf("nobody took the %s", what)
	}
}

// fireDue fires the due timer of the dispatcher's current or next wait.
// A pass that runs for another reason (a wake-up) does the same work, so
// the fire is not waited for.
func (h *harness) fireDue() {
	select {
	case h.due <- time.Now():
	default:
	}
}

func (h *harness) beat() {
	h.t.Helper()
	n := h.beats.Load()
	h.fire(h.heartbeat, "heartbeat")
	testwait.Until(h.t, &h.beatSig, "the heartbeat", func() bool { return h.beats.Load() > n })
}

func (h *harness) enqueue(jobs ...store.NewJob) {
	h.t.Helper()
	if _, err := h.st.EnqueueJobs(context.Background(), jobs, false); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) exec(sql string, args ...any) {
	h.t.Helper()
	if _, err := h.conn.Exec(context.Background(), sql, args...); err != nil {
		h.t.Fatalf("%s: %v", sql, err)
	}
}

type row struct {
	ID        int64
	State     string
	Attempts  int
	Payload   string
	LastError string
	DueIn     time.Duration
	Lease     []byte
}

func (h *harness) row(kind, key string) row {
	h.t.Helper()
	var r row
	var lastErr *string
	var due float64
	err := h.conn.QueryRow(context.Background(), `SELECT id, state, attempts, payload::text, last_error,
		EXTRACT(EPOCH FROM run_after - now())::float8, lease_token FROM jobs WHERE kind = $1 AND key = $2`, kind, key).
		Scan(&r.ID, &r.State, &r.Attempts, &r.Payload, &lastErr, &due, &r.Lease)
	if err != nil {
		h.t.Fatalf("job %s/%s: %v", kind, key, err)
	}
	if lastErr != nil {
		r.LastError = *lastErr
	}
	r.DueIn = time.Duration(due * float64(time.Second))
	return r
}

// stateUntil waits until the job is in state, for a state no hook
// signals (the result is recorded after the handler returned). It queries
// again at once, bounded by testwait.Limit.
func (h *harness) stateUntil(kind, key, state string) row {
	h.t.Helper()
	deadline := time.Now().Add(testwait.Limit(h.t))
	for {
		r := h.row(kind, key)
		if r.State == state {
			return r
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("job %s/%s is %s, want %s", kind, key, r.State, state)
		}
	}
}

func (h *harness) makeDue(id int64) { h.exec(`UPDATE jobs SET run_after = now() WHERE id = $1`, id) }

func (h *harness) expireLease(id int64) {
	h.exec(`UPDATE jobs SET lease_until = now() - interval '1 second' WHERE id = $1 AND state = 'running'`, id)
}

// started is a handler's signal that it runs, with the job it got.
type recorder struct {
	mu   sync.Mutex
	jobs []Job
	sig  testwait.Signal
}

func (r *recorder) add(j Job) {
	r.mu.Lock()
	r.jobs = append(r.jobs, j)
	r.mu.Unlock()
	r.sig.Notify()
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.jobs)
}

func (r *recorder) waitFor(t *testing.T, n int, what string) []Job {
	t.Helper()
	testwait.Until(t, &r.sig, what, func() bool { return r.count() >= n })
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Job(nil), r.jobs...)
}

func newJob(kind, key string, priority int) store.NewJob {
	return store.NewJob{Kind: kind, Key: key, Priority: priority, MaxAttempts: 3}
}

// TestPoolWakesOnNotify: a job enqueued by another store runs at once,
// without a poll; after the listener's connection is killed, it
// reconnects and the next enqueue runs too.
func TestPoolWakesOnNotify(t *testing.T) {
	h := newHarness(t, 1)
	var rec recorder
	h.register(Kind{Name: "test", Handler: func(_ context.Context, j Job) error { rec.add(j); return nil }})
	h.start()
	h.listening(1)
	other, err := store.Open(context.Background(), h.url)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.EnqueueJobs(context.Background(), []store.NewJob{newJob("test", "a", store.PriorityNew)}, false); err != nil {
		t.Fatal(err)
	}
	rec.waitFor(t, 1, "the job after the notification")
	h.stateUntil("test", "a", store.JobDone)

	var killed int
	if err := h.conn.QueryRow(context.Background(), `SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity
		WHERE datname = current_database() AND application_name = $1`, store.JobListenerApp).Scan(&killed); err != nil || killed != 1 {
		t.Fatalf("terminated %d listeners: %v", killed, err)
	}
	h.listening(2)
	if _, err := other.EnqueueJobs(context.Background(), []store.NewJob{newJob("test", "b", store.PriorityNew)}, false); err != nil {
		t.Fatal(err)
	}
	rec.waitFor(t, 2, "the job after the reconnect")
	h.stop()
	if r := h.row("test", "b"); r.State != store.JobDone {
		t.Errorf("after reconnect: %+v", r)
	}
}

// TestPoolBulkEnqueueWakesOnce: 500 jobs in one transaction wake the pool
// once; a sentinel enqueued afterwards in its own transaction marks the
// end.
func TestPoolBulkEnqueueWakesOnce(t *testing.T) {
	h := newHarness(t, 1)
	h.register(Kind{Name: "test", Handler: func(context.Context, Job) error { return nil }})
	h.register(Kind{Name: "sentinel", Handler: func(context.Context, Job) error { return nil }})
	h.start()
	h.listening(1)
	bulk := make([]store.NewJob, 500)
	for i := range bulk {
		bulk[i] = newJob("test", fmt.Sprint(i), store.PriorityBackfill)
	}
	h.enqueue(bulk...)
	h.enqueue(newJob("sentinel", "s", store.PriorityNew))
	testwait.Until(t, &h.notifySig, "the sentinel", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.notified["sentinel"] > 0
	})
	h.mu.Lock()
	n := h.notified["test"]
	h.mu.Unlock()
	if n != 1 {
		t.Errorf("woken %d times for one transaction", n)
	}
}

// TestPoolFallbackPoll: without LISTEN, a poll tick runs the job.
func TestPoolFallbackPoll(t *testing.T) {
	h := newHarness(t, 1)
	var rec recorder
	h.register(Kind{Name: "test", Handler: func(_ context.Context, j Job) error { rec.add(j); return nil }})
	var tries atomic.Int32
	h.pool.listen = func(context.Context) (listener, error) {
		tries.Add(1)
		return nil, errors.New("LISTEN is not supported")
	}
	h.pool.reconnectDelay = func(int) time.Duration { return time.Hour }
	h.start()
	h.nextWait(0) // the first pass found nothing
	h.enqueue(newJob("test", "a", store.PriorityNew))
	if rec.count() != 0 {
		t.Fatal("ran without a poll")
	}
	h.fire(h.poll, "poll")
	rec.waitFor(t, 1, "the job after the poll")
	if tries.Load() < 1 {
		t.Error("never tried to listen")
	}
}

// TestPoolIsBounded: with 2 workers and 5 blocking jobs, 2 run at a
// time, and the third starts only after one ended.
func TestPoolIsBounded(t *testing.T) {
	h := newHarness(t, 2)
	var rec recorder
	var current, peak atomic.Int32
	release := make(chan struct{})
	h.register(Kind{Name: "test", Handler: func(ctx context.Context, j Job) error {
		n := current.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		rec.add(j)
		defer current.Add(-1)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	for i := range 5 {
		h.enqueue(newJob("test", fmt.Sprint(i), store.PriorityNew))
	}
	h.start()
	rec.waitFor(t, 2, "two jobs")
	var running int
	if err := h.conn.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE state = 'running'`).Scan(&running); err != nil || running != 2 {
		t.Fatalf("%d running: %v", running, err)
	}
	if rec.count() != 2 {
		t.Fatalf("%d started", rec.count())
	}
	release <- struct{}{}
	rec.waitFor(t, 3, "the third job after a release")
	for range 3 {
		release <- struct{}{}
	}
	rec.waitFor(t, 5, "all jobs")
	release <- struct{}{}
	h.stop()
	if p := peak.Load(); p != 2 {
		t.Errorf("at most %d at once, want 2", p)
	}
}

// TestPoolShutdownReleases: a job cancelled at shutdown is queued again
// without counting the attempt, so shutdowns never fail it.
func TestPoolShutdownReleases(t *testing.T) {
	h := newHarness(t, 1)
	var rec recorder
	h.register(Kind{Name: "test", MaxAttempts: 1, Handler: func(ctx context.Context, j Job) error {
		rec.add(j)
		<-ctx.Done()
		return fmt.Errorf("stopped: %w", ctx.Err())
	}})
	h.enqueue(store.NewJob{Kind: "test", Key: "a", MaxAttempts: 1})
	for i := 1; i <= 3; i++ {
		h.start()
		rec.waitFor(t, i, "the job to start")
		h.stop()
		r := h.row("test", "a")
		if r.State != store.JobQueued || r.Attempts != 0 || r.Lease != nil || r.DueIn > 0 {
			t.Fatalf("after shutdown %d: %+v", i, r)
		}
	}
}

// TestPoolHeartbeat: a tick extends the lease; a lease taken over (or a
// message deleted) cancels the handler at the next tick, and its result
// is dropped.
func TestPoolHeartbeat(t *testing.T) {
	h := newHarness(t, 2)
	var rec recorder
	var cancelled recorder
	h.register(Kind{Name: "test", Handler: func(ctx context.Context, j Job) error {
		rec.add(j)
		<-ctx.Done()
		cancelled.add(j)
		return nil // a result that must be dropped
	}})
	h.exec(`INSERT INTO messages (sha256, size, stored_path) VALUES ($1, 1, 'x')`, strings.Repeat("1", 64))
	withMessage := newJob("test", "m", store.PriorityNew)
	withMessage.MessageSHA256 = strings.Repeat("1", 64)
	h.enqueue(newJob("test", "a", store.PriorityNew), withMessage)
	h.start()
	rec.waitFor(t, 2, "both jobs")
	h.exec(`UPDATE jobs SET lease_until = now() + interval '5 seconds'`)
	h.beat()
	var low float64
	if err := h.conn.QueryRow(context.Background(), `SELECT EXTRACT(EPOCH FROM min(lease_until) - now())::float8 FROM jobs`).Scan(&low); err != nil || low < 30 {
		t.Fatalf("lease not extended: %v s, %v", low, err)
	}

	h.exec(`UPDATE jobs SET lease_token = '\x99' WHERE key = 'a'`)
	h.exec(`WITH r AS (INSERT INTO deletion_runs (state, confirmed_at, finished_at) VALUES ('done', now(), now()) RETURNING id)
		INSERT INTO deleted_messages (sha256, size, message_seq, archived_at, retain_until, locations, run_id)
		SELECT m.sha256, m.size, 1, m.created_at, now(), 1, r.id FROM messages m, r`)
	h.exec(`DELETE FROM messages`)
	h.beat()
	cancelled.waitFor(t, 2, "both handlers cancelled")
	h.stop()
	h.mu.Lock()
	lost := len(h.lost)
	h.mu.Unlock()
	if lost != 2 {
		t.Errorf("%d leases lost", lost)
	}
	if r := h.row("test", "a"); r.State != store.JobRunning || string(r.Lease) != "\x99" {
		t.Errorf("the taken-over job was changed: %+v", r)
	}
}

// TestPoolRetries: a handler that fails twice is retried after its
// backoff and then done; Permanent fails at once.
func TestPoolRetries(t *testing.T) {
	h := newHarness(t, 1)
	var rec recorder
	h.register(Kind{Name: "test", Handler: func(_ context.Context, j Job) error {
		rec.add(j)
		switch {
		case j.Key == "perm":
			return Permanent(errors.New("encrypted"))
		case j.Attempt < 3:
			return errors.New("flaky")
		}
		return nil
	}})
	h.enqueue(newJob("test", "a", store.PriorityNew))
	h.start()
	for attempt, window := range []struct{ lo, hi time.Duration }{{15 * time.Second, 30 * time.Second}, {30 * time.Second, 60 * time.Second}} {
		jobs := rec.waitFor(t, attempt+1, "an attempt")
		n := h.waitCount()
		if jobs[attempt].Attempt != attempt+1 {
			t.Fatalf("attempt %d: %+v", attempt+1, jobs[attempt])
		}
		r := h.stateUntil("test", "a", store.JobQueued)
		if r.DueIn < window.lo-2*time.Second || r.DueIn > window.hi || r.LastError != CodeInternal {
			t.Fatalf("after failure %d: %+v, want due in [%v, %v]", attempt+1, r, window.lo, window.hi)
		}
		// The dispatcher waits for it, not for a poll.
		h.waitWhere(n, "a wait for the backoff", func(w waitInfo) bool {
			return w.ok && w.d >= window.lo-2*time.Second && w.d <= window.hi
		})
		h.makeDue(r.ID)
		h.fireDue()
	}
	rec.waitFor(t, 3, "the third attempt")
	if r := h.stateUntil("test", "a", store.JobDone); r.LastError != "" || r.Lease != nil {
		t.Errorf("done: %+v", r)
	}
	h.enqueue(newJob("test", "perm", store.PriorityNew))
	h.fire(h.poll, "poll")
	if r := h.stateUntil("test", "perm", store.JobFailed); r.Attempts != 1 || r.LastError != CodeInternal {
		t.Errorf("permanent: %+v", r)
	}
}

// TestPoolLastErrorHasNoContent: an error's text never reaches the queue,
// only its code.
func TestPoolLastErrorHasNoContent(t *testing.T) {
	h := newHarness(t, 1)
	const marker = "Subject: secret invoice 4711"
	h.register(Kind{Name: "test", MaxAttempts: 1, Handler: func(_ context.Context, j Job) error {
		switch j.Key {
		case "panic":
			panic(marker)
		case "code":
			return Code("encrypted", errors.New(marker))
		case "badcode":
			return Code(marker, errors.New(marker))
		}
		return errors.New(marker)
	}})
	for _, k := range []string{"plain", "panic", "code", "badcode"} {
		h.enqueue(store.NewJob{Kind: "test", Key: k, MaxAttempts: 1})
	}
	h.start()
	for k, want := range map[string]string{"plain": CodeInternal, "panic": CodePanic, "code": "encrypted", "badcode": CodeInternal} {
		if r := h.stateUntil("test", k, store.JobFailed); r.LastError != want {
			t.Errorf("%s: last_error %q, want %q", k, r.LastError, want)
		}
	}
	var n int
	if err := h.conn.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE strpos(coalesce(last_error, '') || payload::text, 'secret') > 0`).Scan(&n); err != nil || n != 0 {
		t.Errorf("content in the queue: %d %v", n, err)
	}
}

// TestPoolContinue: a handler that returns Continue is called again with
// its payload, each time as attempt 1, until it returns nil; a shutdown
// after Continue keeps the new payload.
func TestPoolContinue(t *testing.T) {
	h := newHarness(t, 1)
	var rec recorder
	h.register(Kind{Name: "test", Handler: func(ctx context.Context, j Job) error {
		rec.add(j)
		var p struct{ C int }
		_ = json.Unmarshal(j.Payload, &p)
		switch {
		case j.Key == "stop":
			<-ctx.Done()
			return Continue(json.RawMessage(`{"C": 9}`))
		case p.C < 3:
			return Continue(json.RawMessage(fmt.Sprintf(`{"C": %d}`, p.C+1)))
		}
		return nil
	}})
	h.enqueue(newJob("test", "a", store.PriorityNew))
	h.start()
	jobs := rec.waitFor(t, 4, "four calls")
	for i, j := range jobs {
		if want := fmt.Sprintf(`{"C": %d}`, i); i > 0 && string(j.Payload) != want || j.Attempt != 1 {
			t.Errorf("call %d: payload %s attempt %d", i, j.Payload, j.Attempt)
		}
	}
	h.stateUntil("test", "a", store.JobDone)
	h.enqueue(newJob("test", "stop", store.PriorityNew))
	h.fire(h.poll, "poll")
	rec.waitFor(t, 5, "the blocking call")
	h.stop()
	if r := h.row("test", "stop"); r.State != store.JobQueued || r.Payload != `{"C": 9}` || r.Attempts != 0 {
		t.Errorf("continued at shutdown: %+v", r)
	}
}

// TestPoolWakesAtExpiredLease: the dispatcher waits for the lease of a
// job another process holds and retries it once it expired, without a
// poll.
func TestPoolWakesAtExpiredLease(t *testing.T) {
	h := newHarness(t, 1)
	var rec recorder
	h.register(Kind{Name: "test", Handler: func(_ context.Context, j Job) error { rec.add(j); return nil }})
	h.enqueue(newJob("test", "a", store.PriorityNew))
	c, err := h.st.ClaimJobs(context.Background(), []string{"test"}, []byte("dead process"), time.Minute, 1)
	if err != nil || len(c) != 1 {
		t.Fatal(c, err)
	}
	h.start()
	if w := h.nextWait(0); !w.ok || w.d <= 0 || w.d > time.Minute {
		t.Fatalf("waits %+v, want the lease's end", w)
	}
	h.expireLease(c[0].ID)
	n := h.waitCount()
	h.fireDue()
	if r := h.stateUntil("test", "a", store.JobQueued); r.LastError != CodeLeaseExpired {
		t.Fatalf("after the expiry: %+v", r)
	}
	// The pass that reaped it waits for its backoff.
	h.waitWhere(n, "a wait for the backoff", func(w waitInfo) bool { return w.ok && w.d >= 10*time.Second && w.d <= 30*time.Second })
	h.makeDue(c[0].ID)
	h.fireDue()
	if jobs := rec.waitFor(t, 1, "the retried job"); jobs[0].Attempt != 2 {
		t.Errorf("attempt %d", jobs[0].Attempt)
	}
}

// TestPoolWaitsOnlyForItsKinds: a lease of a kind the pool does not run
// does not set its wake-up.
func TestPoolWaitsOnlyForItsKinds(t *testing.T) {
	h := newHarness(t, 1)
	h.register(Kind{Name: "a", Handler: func(context.Context, Job) error { return nil }})
	later := newJob("a", "later", store.PriorityNew)
	later.RunAfter = time.Now().Add(time.Hour)
	h.enqueue(later, newJob("b", "x", store.PriorityNew))
	if _, err := h.st.ClaimJobs(context.Background(), []string{"b"}, []byte("other"), 10*time.Second, 1); err != nil {
		t.Fatal(err)
	}
	h.start()
	if w := h.nextWait(0); !w.ok || w.d < 50*time.Minute {
		t.Errorf("waits %+v, want about an hour (job a), not the lease of b", w)
	}
}

// TestPoolConcurrentClaims: 4 pools with 8 workers each run 1000 jobs,
// each exactly once.
func TestPoolConcurrentClaims(t *testing.T) {
	h := newHarness(t, 8)
	const jobs = 1000
	var mu sync.Mutex
	seen := map[int64]int{}
	var total atomic.Int32
	var all testwait.Signal
	h.register(Kind{Name: "test", Handler: func(_ context.Context, j Job) error {
		mu.Lock()
		seen[j.ID]++
		mu.Unlock()
		if total.Add(1) >= jobs {
			all.Notify()
		}
		return nil
	}})
	batch := make([]store.NewJob, jobs)
	for i := range batch {
		batch[i] = newJob("test", fmt.Sprint(i), i%3*50)
	}
	h.enqueue(batch...)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := range 4 {
		st := h.st
		if i > 0 {
			var err error
			if st, err = store.Open(context.Background(), h.url); err != nil {
				t.Fatal(err)
			}
			defer st.Close()
		}
		p := h.newPool(st, 8)
		p.poll = make(chan time.Time) // each pool its own: the shared one would block
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.Run(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	testwait.Until(t, &all, "every job", func() bool { return total.Load() >= jobs })
	cancel()
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != jobs {
		t.Errorf("%d jobs run, want %d", len(seen), jobs)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("job %d ran %d times", id, n)
		}
	}
	var done int
	if err := h.conn.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE state = 'done'`).Scan(&done); err != nil || done != jobs {
		t.Errorf("%d done: %v", done, err)
	}
}

const crashChildEnv = "MAIL_ARCHIVE_TEST_JOBS_CRASH_CHILD"

// crashChild runs a pool whose handler reports "started" on stdout and
// then blocks until the parent kills the process.
func crashChild(url string) {
	ctx := context.Background()
	st, err := store.Open(ctx, url)
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(2)
	}
	reg := &Registry{}
	_ = reg.Register(Kind{Name: "crash", MaxAttempts: 3, Handler: func(ctx context.Context, j Job) error {
		fmt.Printf("started %d\n", j.Attempt)
		<-ctx.Done()
		return nil
	}})
	_ = (&Pool{Store: st, Kinds: reg, Workers: 1, Logger: quietLogger()}).Run(ctx)
	os.Exit(3)
}

// TestPoolCrashLoop: a job whose process is killed (SIGKILL) during the
// handler keeps its attempt; after three such crashes it is failed with
// "lease expired" and never runs again.
func TestPoolCrashLoop(t *testing.T) {
	if url := os.Getenv(crashChildEnv); url != "" {
		crashChild(url)
	}
	h := newHarness(t, 1)
	h.enqueue(store.NewJob{Kind: "crash", Key: "pdf", Priority: store.PriorityNew, MaxAttempts: 3})
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	for crash := 1; crash <= 3; crash++ {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPoolCrashLoop$") //nolint:gosec // the test binary itself
		cmd.Env = append(os.Environ(), crashChildEnv+"="+h.url)
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(out).ReadString('\n')
		if err != nil || line != fmt.Sprintf("started %d\n", crash) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("child %d: %q %v", crash, line, err)
		}
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		_ = cmd.Wait()
		r := h.row("crash", "pdf")
		if r.State != store.JobRunning || r.Attempts != crash {
			t.Fatalf("after crash %d: %+v", crash, r)
		}
		h.expireLease(r.ID)
		reaped, err := h.st.ReapExpiredJobs(context.Background(), "crash", store.JobBackoff{First: 30 * time.Second, Cap: time.Hour}, nil, 10)
		if err != nil || len(reaped) != 1 {
			t.Fatalf("reap %d: %+v %v", crash, reaped, err)
		}
		if crash < 3 {
			h.makeDue(r.ID)
		}
	}
	if r := h.row("crash", "pdf"); r.State != store.JobFailed || r.LastError != CodeLeaseExpired {
		t.Fatalf("after three crashes: %+v", r)
	}
	// A healthy pool never runs it again.
	var rec recorder
	h.register(Kind{Name: "crash", Handler: func(_ context.Context, j Job) error { rec.add(j); return nil }})
	h.register(Kind{Name: "sentinel", Handler: func(_ context.Context, j Job) error { rec.add(j); return nil }})
	h.enqueue(newJob("sentinel", "s", store.PriorityBackfill))
	h.start()
	jobs := rec.waitFor(t, 1, "the sentinel")
	h.stop()
	if len(jobs) != 1 || jobs[0].Kind != "sentinel" {
		t.Errorf("ran %+v", jobs)
	}
}

func TestPoolRefusesBadWorkers(t *testing.T) {
	for _, n := range []int{0, -1, MaxWorkers + 1} {
		if err := (&Pool{Workers: n, Kinds: &Registry{}}).Run(context.Background()); err == nil {
			t.Errorf("Workers %d accepted", n)
		}
	}
}

// passes counts the dispatcher's passes; each reports its free slots
// after claiming.
type passes struct {
	n   atomic.Int32
	sig testwait.Signal
}

func (h *harness) countPasses() *passes {
	p := &passes{}
	h.pool.onPass = func(int) { p.n.Add(1); p.sig.Notify() }
	return p
}

// poll fires the poll and waits for the pass it caused.
func (p *passes) poll(h *harness) {
	h.t.Helper()
	n := p.n.Load()
	h.fire(h.poll, "poll")
	testwait.Until(h.t, &p.sig, "the pass after the poll", func() bool { return p.n.Load() > n })
}

// peakCounter tracks how many handlers run at once.
type peakCounter struct{ current, peak atomic.Int32 }

func (c *peakCounter) enter() {
	n := c.current.Add(1)
	for {
		p := c.peak.Load()
		if n <= p || c.peak.CompareAndSwap(p, n) {
			return
		}
	}
}

func (c *peakCounter) leave() { c.current.Add(-1) }

func (h *harness) leaseLeft(id int64) time.Duration {
	h.t.Helper()
	var s float64
	if err := h.conn.QueryRow(context.Background(), `SELECT EXTRACT(EPOCH FROM lease_until - now())::float8 FROM jobs WHERE id = $1`, id).Scan(&s); err != nil {
		h.t.Fatal(err)
	}
	return time.Duration(s * float64(time.Second))
}

// TestPoolDoesNotReapItsOwnJob: when the lease of a job this pool still
// runs has expired (heartbeats failed during a database outage), the pool
// does not reap and run it a second time; its heartbeat extends the
// lease again.
func TestPoolDoesNotReapItsOwnJob(t *testing.T) {
	h := newHarness(t, 2)
	ps := h.countPasses()
	var rec recorder
	var pc peakCounter
	release := make(chan struct{})
	h.register(Kind{Name: "test", RetryFirst: time.Millisecond, RetryCap: time.Millisecond, Handler: func(_ context.Context, j Job) error {
		pc.enter()
		defer pc.leave()
		rec.add(j)
		<-release // ignores its context, like a handler in a long call
		return nil
	}})
	h.enqueue(newJob("test", "a", store.PriorityNew))
	h.start()
	rec.waitFor(t, 1, "the job")
	id := h.row("test", "a").ID
	h.expireLease(id)
	ps.poll(h)
	h.makeDue(id) // makes a wrongly reaped job due at once; nothing while it runs
	ps.poll(h)
	if n := rec.count(); n != 1 {
		t.Fatalf("the job ran %d times", n)
	}
	if r := h.row("test", "a"); r.State != store.JobRunning || r.Attempts != 1 || r.LastError != "" {
		t.Fatalf("reaped by its own pool: %+v", r)
	}
	h.beat()
	if left := h.leaseLeft(id); left < 30*time.Second {
		t.Errorf("own lease not extended: %v left", left)
	}
	h.mu.Lock()
	lost := len(h.lost)
	h.mu.Unlock()
	if lost != 0 {
		t.Errorf("%d leases lost", lost)
	}
	close(release)
	h.stateUntil("test", "a", store.JobDone)
	if p := pc.peak.Load(); p != 1 {
		t.Errorf("peak %d", p)
	}
}

// TestPoolSameJobClaimedAgainWhileFinishing: a job continued (or enqueued
// again) is claimed again while the goroutine of its previous claim still
// finishes. That goroutine's end frees only its own slot: the new claim
// keeps its heartbeat and its slot, and the pool stays within Workers.
func TestPoolSameJobClaimedAgainWhileFinishing(t *testing.T) {
	h := newHarness(t, 2)
	ps := h.countPasses()
	var rec recorder
	var pc peakCounter
	release := make(chan struct{})
	h.register(Kind{Name: "test", Handler: func(ctx context.Context, j Job) error {
		rec.add(j)
		if j.Key == "a" && string(j.Payload) == "{}" {
			return Continue(json.RawMessage(`{"c": 1}`))
		}
		pc.enter()
		defer pc.leave()
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}})
	held, hold := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h.pool.afterComplete = func(int64) {
		once.Do(func() {
			close(held)
			<-hold
		})
	}
	var freed atomic.Int32
	var freedSig testwait.Signal
	h.pool.onFree = func(int64) { freed.Add(1); freedSig.Notify() }
	h.enqueue(newJob("test", "a", store.PriorityNew))
	h.start()
	<-held // the first claim is recorded (continued); its goroutine waits
	ps.poll(h)
	if jobs := rec.waitFor(t, 2, "the second claim"); jobs[1].ID != jobs[0].ID {
		t.Fatalf("claims %+v", jobs)
	}
	close(hold)
	testwait.Until(t, &freedSig, "the first claim's slot to be free", func() bool { return freed.Load() == 1 })
	// The new claim must still hold its slot: a pass claims one more job,
	// not two.
	h.enqueue(newJob("test", "b", store.PriorityNew), newJob("test", "c", store.PriorityNew))
	ps.poll(h)
	rec.waitFor(t, 3, "a second job")
	ps.poll(h)
	if n := rec.count(); n != 3 {
		t.Errorf("%d handler calls, want 3 (Workers=2)", n)
	}
	if p := pc.peak.Load(); p > 2 {
		t.Errorf("peak %d with Workers=2", p)
	}
	id := h.row("test", "a").ID
	h.exec(`UPDATE jobs SET lease_until = now() + interval '5 seconds' WHERE id = $1`, id)
	h.beat()
	if left := h.leaseLeft(id); left < 30*time.Second {
		t.Errorf("the new claim lost its heartbeat: %v left", left)
	}
	close(release)
	h.stateUntil("test", "a", store.JobDone)
}
