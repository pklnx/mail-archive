package archive

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

// Runner syncs accounts in the background for the web server: on request
// and on a schedule. It syncs one account at a time, so a large first sync
// does not open many IMAP connections at once.
type Runner struct {
	Syncer *Syncer
	// Interval is how often each enabled account is synced. An account is due
	// when its last run started at least Interval ago. Zero disables the
	// schedule; syncs then only run on request.
	Interval time.Duration
	// CheckEvery is how often the schedule is checked (default one minute).
	CheckEvery time.Duration

	mu     sync.Mutex
	queue  []int64
	queued map[int64]bool
	wake   chan struct{}
}

// Enqueue adds accounts to the sync queue. Accounts already waiting are not
// added twice. It returns immediately; Run does the work.
func (r *Runner) Enqueue(ids ...int64) {
	r.mu.Lock()
	r.init()
	for _, id := range ids {
		if !r.queued[id] {
			r.queued[id] = true
			r.queue = append(r.queue, id)
		}
	}
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Queued reports which accounts wait for their sync to start.
func (r *Runner) Queued() map[int64]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int64]bool, len(r.queued))
	for id := range r.queued {
		out[id] = true
	}
	return out
}

func (r *Runner) init() {
	if r.queued == nil {
		r.queued = map[int64]bool{}
		r.wake = make(chan struct{}, 1)
	}
}

func (r *Runner) next() (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.queue) == 0 {
		return 0, false
	}
	id := r.queue[0]
	r.queue = slices.Delete(r.queue, 0, 1)
	delete(r.queued, id)
	return id, true
}

// Run works through the queue and the schedule until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) {
	r.mu.Lock()
	r.init()
	wake := r.wake
	r.mu.Unlock()
	every := r.CheckEvery
	if every <= 0 {
		every = time.Minute
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	log := r.Syncer.logger()
	for {
		if r.Interval > 0 {
			if err := r.enqueueDue(ctx); err != nil && ctx.Err() == nil {
				log.Error("check sync schedule", "err", err)
			}
		}
		for id, ok := r.next(); ok && ctx.Err() == nil; id, ok = r.next() {
			r.syncOne(ctx, id)
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-tick.C:
		}
	}
}

// enqueueDue queues every enabled account whose last run is older than the
// interval, or that was never synced.
func (r *Runner) enqueueDue(ctx context.Context) error {
	st := r.Syncer.Store
	accounts, err := st.ListAccounts(ctx)
	if err != nil {
		return err
	}
	runs, err := st.LastRuns(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	var due []int64
	for _, a := range accounts {
		if !a.Enabled || a.RemovedAt != nil {
			continue
		}
		if run, ok := runs[a.ID]; ok && now.Sub(run.StartedAt) < r.Interval {
			continue
		}
		due = append(due, a.ID)
	}
	if len(due) > 0 {
		r.Enqueue(due...)
	}
	return nil
}

func (r *Runner) syncOne(ctx context.Context, id int64) {
	log := r.Syncer.logger()
	a, err := r.Syncer.Store.GetAccount(ctx, id)
	if err != nil {
		log.Error("load account for sync", "id", id, "err", err)
		return
	}
	res := r.Syncer.SyncAccount(ctx, a)
	switch {
	case errors.Is(res.Err, ErrSyncRunning), errors.Is(res.Err, ErrAccountRemoved):
		log.Debug("sync skipped", "account", a.Name, "reason", res.Err)
	case res.Err != nil:
		log.Error("sync failed", "account", a.Name, "err", res.Err)
	}
}
