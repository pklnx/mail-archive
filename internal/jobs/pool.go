package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
)

const (
	// LeaseDuration is how long a claim holds a job; the heartbeat extends
	// it every LeaseDuration/3. A job whose process died is retried once
	// it expired.
	LeaseDuration = 60 * time.Second
	// PollInterval is the fallback when notifications are lost or LISTEN
	// does not work (a pooler in transaction mode).
	PollInterval = 5 * time.Minute
	// CleanupInterval is how often a pool removes old done jobs.
	CleanupInterval = time.Hour
	// KeepDone is how long done jobs stay.
	KeepDone = 24 * time.Hour
	// StaleAfter is how long a job of new mail may have been due before
	// /healthz/jobs is degraded.
	StaleAfter = 30 * time.Minute
	// finishTimeout bounds recording a result after shutdown.
	finishTimeout = 5 * time.Second
	// reapLimit is the most expired leases a pass retries per kind.
	reapLimit = 100
	// minPass keeps the dispatcher from spinning on a due time it cannot
	// act on (a row another pool holds).
	minPass = time.Second
	// listenWarnAfter failed LISTEN attempts in a row log one warning.
	listenWarnAfter = 3
)

// listener is the LISTEN connection (store.JobListener).
type listener interface {
	Wait(ctx context.Context) (string, error)
	Close(ctx context.Context) error
}

// Pool runs the jobs of the registered kinds with at most Workers handlers
// at a time. Several pools, in one process or many, share the queue
// safely.
type Pool struct {
	Store   *store.Store
	Kinds   *Registry
	Workers int
	Logger  *slog.Logger
	// UntilIdle makes Run return once none of its jobs runs and no job of
	// its kinds is due, for a command that works off the queue in the
	// foreground (reindex). Jobs that other pools hold, or that wait for a
	// retry, are left to them.
	UntilIdle bool

	// Test seams; nil means the real thing.
	heartbeat <-chan time.Time
	poll      <-chan time.Time
	cleanup   <-chan time.Time
	// after returns a channel that fires when the next job is due.
	after  func(time.Duration) <-chan time.Time
	listen func(context.Context) (listener, error)
	// reconnectDelay is the wait before LISTEN attempt failures+1.
	reconnectDelay func(failures int) time.Duration
	// hooks
	onNotify    func(kind string)
	onListen    func()
	onWait      func(due time.Duration, ok bool)
	onPass      func(free int)
	onHeartbeat func()
	onLost      func(id int64)
	// afterComplete runs after a job's result is recorded, before its
	// slot is free.
	afterComplete func(id int64)
	// onFree runs once its slot is free.
	onFree func(id int64)
}

// Run claims and runs jobs until ctx ends. Then it stops claiming, cancels
// the handlers' contexts and waits for them: a job whose handler returned
// the cancellation is released (queued again without counting the
// attempt), other results are recorded as usual.
func (p *Pool) Run(ctx context.Context) error {
	if p.Workers < 1 || p.Workers > MaxWorkers {
		return fmt.Errorf("job workers must be 1 to %d, got %d", MaxWorkers, p.Workers)
	}
	r := &run{p: p, kinds: p.Kinds.Names(), running: map[*task]struct{}{}, wake: make(chan struct{}, 1)}

	listenCtx, stopListen := context.WithCancel(ctx)
	listenDone := make(chan struct{})
	go func() {
		defer close(listenDone)
		r.listen(listenCtx)
	}()
	// The heartbeat keeps the leases of handlers that are still ending
	// after ctx ended.
	hbCtx, stopHB := context.WithCancel(context.WithoutCancel(ctx))
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		r.heartbeat(hbCtx)
	}()

	r.dispatch(ctx)
	r.handlers.Wait()
	stopHB()
	<-hbDone
	stopListen()
	<-listenDone
	return nil
}

func (p *Pool) log() *slog.Logger {
	if p.Logger == nil {
		return slog.Default()
	}
	return p.Logger
}

type task struct {
	job    store.ClaimedJob
	cancel context.CancelFunc
	// lost: the heartbeat found the lease gone; completing: the result is
	// being recorded. Both under run.mu.
	lost, completing bool
}

type run struct {
	p     *Pool
	kinds []string
	mu    sync.Mutex
	// running holds every claimed job until its result is recorded; it is
	// keyed by the claim, not the job ID: the same job can be claimed
	// again (after Continue, or a re-enqueue of a done row) while the
	// goroutine of the previous claim still finishes.
	running  map[*task]struct{}
	handlers sync.WaitGroup
	// wake has a buffer of one and is sent to without blocking.
	wake chan struct{}
}

func (r *run) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *run) dispatch(ctx context.Context) {
	pollC, cleanupC := r.p.poll, r.p.cleanup
	if pollC == nil {
		t := time.NewTicker(PollInterval)
		defer t.Stop()
		pollC = t.C
	}
	if cleanupC == nil {
		t := time.NewTicker(CleanupInterval)
		defer t.Stop()
		cleanupC = t.C
	}
	after := r.p.after
	if after == nil {
		after = time.After
	}
	r.cleanupDone(ctx)
	for ctx.Err() == nil {
		progress := r.reap(ctx)
		free := r.p.Workers - r.count()
		if free > 0 {
			claimed := r.claim(ctx, free)
			progress = progress || claimed > 0
			free -= claimed
		}
		if r.p.onPass != nil {
			r.p.onPass(free)
		}
		idle := r.p.UntilIdle && !progress && r.count() == 0
		var dueC <-chan time.Time
		if free > 0 || idle {
			wait, ok, err := r.p.Store.NextJobDue(ctx, r.kinds, r.ownLeases())
			switch {
			case err != nil:
				if ctx.Err() == nil {
					r.p.log().Warn("jobs: cannot read the next due time", "err", err)
				}
			case idle && (!ok || wait > 0):
				return
			case ok:
				if wait <= 0 && !progress {
					wait = minPass
				}
				dueC = after(max(wait, 0))
			}
			if r.p.onWait != nil {
				r.p.onWait(wait, ok && err == nil)
			}
		}
		select {
		case <-ctx.Done():
		case <-r.wake:
		case <-pollC:
		case <-dueC:
		case <-cleanupC:
			r.cleanupDone(ctx)
		}
	}
}

func (r *run) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.running)
}

func (r *run) ownLeases() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]byte, 0, len(r.running))
	for t := range r.running {
		out = append(out, t.job.Lease)
	}
	return out
}

func (r *run) cleanupDone(ctx context.Context) {
	n, err := r.p.Store.DeleteDoneJobs(ctx, KeepDone)
	if err != nil && ctx.Err() == nil {
		r.p.log().Warn("jobs: cannot remove done jobs", "err", err)
	} else if n > 0 {
		r.p.log().Debug("jobs: removed done jobs", "jobs", n)
	}
}

// reap retries the expired leases of the pool's kinds and reports whether
// it found any.
func (r *run) reap(ctx context.Context) bool {
	found := false
	for _, name := range r.kinds {
		k, _ := r.p.Kinds.Kind(name)
		reaped, err := r.p.Store.ReapExpiredJobs(ctx, name, k.backoff(), r.ownLeases(), reapLimit)
		if err != nil {
			if ctx.Err() == nil {
				r.p.log().Warn("jobs: cannot retry expired leases", "kind", name, "err", err)
			}
			return found
		}
		for _, j := range reaped {
			found = true
			level := slog.LevelWarn
			if j.State == store.JobFailed {
				level = slog.LevelError
			}
			r.p.log().Log(ctx, level, "job lease expired; its process ended or hung", "job", j.ID, "kind", j.Kind, "key", j.Key,
				"attempts", j.Attempts, "state", j.State)
		}
	}
	return found
}

func (r *run) claim(ctx context.Context, free int) int {
	lease := newLease()
	claimed, err := r.p.Store.ClaimJobs(ctx, r.kinds, lease, LeaseDuration, free)
	if err != nil {
		if ctx.Err() == nil {
			r.p.log().Warn("jobs: cannot claim", "err", err)
		}
		return 0
	}
	for _, j := range claimed {
		r.start(ctx, j)
	}
	return len(claimed)
}

func (r *run) start(ctx context.Context, j store.ClaimedJob) {
	k, _ := r.p.Kinds.Kind(j.Kind)
	hctx, cancel := context.WithCancel(ctx)
	t := &task{job: j, cancel: cancel}
	r.mu.Lock()
	r.running[t] = struct{}{}
	r.mu.Unlock()
	r.handlers.Add(1)
	go func() {
		defer r.handlers.Done()
		err := call(hctx, k.Handler, Job{
			ID: j.ID, Kind: j.Kind, Key: j.Key, MessageSHA256: j.MessageSHA256, Priority: j.Priority,
			Payload: j.Payload, Attempt: j.Attempt, MaxAttempts: j.MaxAttempts,
		})
		r.complete(ctx, t, k, err)
		cancel()
		if r.p.afterComplete != nil {
			r.p.afterComplete(j.ID)
		}
		r.mu.Lock()
		delete(r.running, t)
		r.mu.Unlock()
		if r.p.onFree != nil {
			r.p.onFree(j.ID)
		}
		r.signal()
	}()
}

// call runs h and turns a panic into an error.
func call(ctx context.Context, h Handler, j Job) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = &panicError{value: v, stack: debug.Stack()}
		}
	}()
	return h(ctx, j)
}

func (r *run) complete(ctx context.Context, t *task, k Kind, err error) {
	r.mu.Lock()
	lost := t.lost
	t.completing = true
	r.mu.Unlock()
	j := t.job
	log := r.p.log().With("job", j.ID, "kind", j.Kind, "key", j.Key, "attempt", j.Attempt)
	if lost {
		log.Warn("job lost its lease; its result is dropped")
		return
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	var cont *continueError
	var dbErr error
	switch {
	case err == nil:
		dbErr = r.p.Store.FinishJob(bg, j.ID, j.Lease)
	case errors.As(err, &cont):
		dbErr = r.p.Store.ContinueJob(bg, j.ID, j.Lease, cont.payload)
	case ctx.Err() != nil && errors.Is(err, context.Canceled):
		dbErr = r.p.Store.ReleaseJob(bg, j.ID, j.Lease)
		if dbErr == nil {
			log.Info("job released at shutdown")
		}
	default:
		var perm *permanentError
		code := ErrorCode(err)
		var state string
		var next time.Time
		state, next, dbErr = r.p.Store.RetryJob(bg, j.ID, j.Lease, code, errors.As(err, &perm), k.backoff())
		if dbErr == nil {
			if state == store.JobFailed {
				log.Error("job failed", "code", code, "err", err)
			} else {
				log.Warn("job will be retried", "code", code, "err", err, "at", next)
			}
		}
	}
	switch {
	case errors.Is(dbErr, store.ErrNotFound):
		log.Warn("job lost its lease; its result is dropped")
	case dbErr != nil:
		log.Warn("cannot record the job's result; its lease expires and the attempt counts", "err", dbErr)
	}
}

func (r *run) heartbeat(ctx context.Context) {
	tick := r.p.heartbeat
	if tick == nil {
		t := time.NewTicker(LeaseDuration / 3)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
		}
		r.mu.Lock()
		var tasks []*task
		ids := make([]int64, 0, len(r.running))
		leases := make([][]byte, 0, len(r.running))
		for t := range r.running {
			if !t.completing && !t.lost {
				tasks = append(tasks, t)
				ids, leases = append(ids, t.job.ID), append(leases, t.job.Lease)
			}
		}
		r.mu.Unlock()
		held, err := r.p.Store.ExtendJobLeases(ctx, ids, leases, LeaseDuration)
		if err != nil {
			if ctx.Err() == nil {
				r.p.log().Warn("jobs: cannot extend leases", "err", err)
			}
		} else {
			r.mu.Lock()
			for _, t := range tasks {
				id := t.job.ID
				if bytes.Equal(held[id], t.job.Lease) || t.completing || t.lost {
					continue
				}
				t.lost = true
				t.cancel()
				r.p.log().Warn("job lost its lease; its handler is stopped", "job", id, "kind", t.job.Kind, "key", t.job.Key)
				if r.p.onLost != nil {
					r.p.onLost(id)
				}
			}
			r.mu.Unlock()
		}
		if r.p.onHeartbeat != nil {
			r.p.onHeartbeat()
		}
	}
}

func (r *run) listen(ctx context.Context) {
	listen := r.p.listen
	if listen == nil {
		listen = func(ctx context.Context) (listener, error) { return r.p.Store.ListenJobs(ctx) }
	}
	delay := r.p.reconnectDelay
	if delay == nil {
		delay = reconnectDelay
	}
	failures := 0
	warned := false
	for ctx.Err() == nil {
		l, err := listen(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			if failures == listenWarnAfter {
				warned = true
				r.p.log().Warn("jobs: cannot listen for new jobs; polling instead", "every", PollInterval, "err", err)
			}
			if !wait(ctx, delay(failures)) {
				return
			}
			continue
		}
		if warned {
			r.p.log().Info("jobs: listening for new jobs again")
		}
		warned = false
		// A pass right away: notifications sent while not listening are
		// lost.
		r.signal()
		if r.p.onListen != nil {
			r.p.onListen()
		}
		for {
			kind, err := l.Wait(ctx)
			if err != nil {
				break
			}
			if r.p.onNotify != nil {
				r.p.onNotify(kind)
			}
			if _, ok := r.p.Kinds.Kind(kind); ok {
				r.signal()
			}
		}
		_ = l.Close(context.WithoutCancel(ctx))
		if ctx.Err() != nil {
			return
		}
		r.p.log().Debug("jobs: listener lost; reconnecting")
		failures = 1
		if !wait(ctx, delay(failures)) {
			return
		}
	}
}

// reconnectDelay doubles from 1 s to 30 s.
func reconnectDelay(failures int) time.Duration {
	d := time.Second
	for i := 1; i < failures && d < 30*time.Second; i++ {
		d *= 2
	}
	return min(d, 30*time.Second)
}

func wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
