package archive

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
)

// Runner syncs accounts in the background for the web server: on request
// and on a schedule. It syncs one account at a time, so a large first sync
// does not open many IMAP connections at once.
type Runner struct {
	Syncer *Syncer
	// Schedule says how often each enabled IMAP account is synced. An
	// account is due when its last run started at least its interval ago
	// (Schedule.Every). A zero Schedule.Sync disables the schedule; syncs
	// then only run on request.
	Schedule Schedule
	// Now returns the current time (default time.Now), for tests.
	Now func() time.Time
	// CheckEvery is how often the schedule is checked (default one minute).
	CheckEvery time.Duration
	// AfterSync, if set, is called after each sync, for example to deliver
	// notifications right away. The account's claim (see Pending) has
	// ended by then.
	AfterSync func()
	// BeforeSync, if set, is called for tests once Run has taken an
	// account from the queue (it is claimed) and before the account is
	// loaded. A hook that blocks must also return when ctx is done.
	BeforeSync func(ctx context.Context, id int64)

	mu     sync.Mutex
	queue  []int64
	queued map[int64]SyncOptions
	// claimed holds the accounts Run has taken from the queue and whose
	// sync has not returned yet: it waits for or holds the account's sync
	// lock, or waits for the blob lock (parked).
	claimed map[int64]bool
	wake    chan struct{}
}

// Enqueue adds accounts to the sync queue. Accounts already waiting are not
// added twice. It returns immediately; Run does the work.
func (r *Runner) Enqueue(ids ...int64) {
	r.EnqueueWith(SyncOptions{}, ids...)
}

// EnqueueWith is Enqueue with options. An account already waiting keeps
// its place; a request to reconcile upgrades its queued sync.
func (r *Runner) EnqueueWith(opts SyncOptions, ids ...int64) {
	r.mu.Lock()
	r.init()
	for _, id := range ids {
		cur, ok := r.queued[id]
		switch {
		case !ok:
			r.queued[id] = opts
			r.queue = append(r.queue, id)
		case opts.Reconcile && (!cur.Reconcile || opts.ReconcileMinAge < cur.ReconcileMinAge):
			r.queued[id] = opts
		}
	}
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Pending reports the accounts this runner works on: waiting in the queue,
// or taken from it and not done yet (claimed). An account taken from the
// queue is claimed in the same step, so it is in one of the two sets until
// its sync has returned; by then its sync lock is released and the run's
// row is finished.
func (r *Runner) Pending() map[int64]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int64]bool, len(r.queued)+len(r.claimed))
	for id := range r.queued {
		out[id] = true
	}
	for id := range r.claimed {
		out[id] = true
	}
	return out
}

func (r *Runner) init() {
	if r.queued == nil {
		r.queued = map[int64]SyncOptions{}
		r.claimed = map[int64]bool{}
		r.wake = make(chan struct{}, 1)
	}
}

// release ends the claim that next took on the account.
func (r *Runner) release(id int64) {
	r.mu.Lock()
	delete(r.claimed, id)
	r.mu.Unlock()
}

func (r *Runner) next() (int64, SyncOptions, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.queue) == 0 {
		return 0, SyncOptions{}, false
	}
	id := r.queue[0]
	r.queue = slices.Delete(r.queue, 0, 1)
	opts := r.queued[id]
	delete(r.queued, id)
	r.claimed[id] = true
	return id, opts, true
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
		if r.Schedule.Sync > 0 {
			if err := r.enqueueDue(ctx); err != nil && ctx.Err() == nil {
				log.Error("check sync schedule", "err", err)
			}
		}
		// ctx is checked before next, which claims the account, so no
		// account is claimed without syncOne, which ends every claim.
		for ctx.Err() == nil {
			id, opts, ok := r.next()
			if !ok {
				break
			}
			r.syncOne(ctx, id, opts)
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-tick.C:
		}
	}
}

// enqueueDue queues every account that is due (see dueAccounts).
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
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	if due := dueAccounts(accounts, runs, r.Schedule, now()); len(due) > 0 {
		r.Enqueue(due...)
	}
	return nil
}

// dueAccounts returns the enabled IMAP accounts whose last run of any kind
// started at least their interval ago, or that never ran.
func dueAccounts(accounts []*store.Account, runs map[int64]store.LastRun, s Schedule, now time.Time) []int64 {
	var due []int64
	for _, a := range accounts {
		var last *store.LastRun
		if run, ok := runs[a.ID]; ok {
			last = &run
		}
		if next, ok := s.Next(ScheduledAccount(a, last)); ok && !now.Before(next) {
			due = append(due, a.ID)
		}
	}
	return due
}

// syncOne syncs a claimed account; its claim has ended before AfterSync.
func (r *Runner) syncOne(ctx context.Context, id int64, opts SyncOptions) {
	a, res, ok := r.syncClaimed(ctx, id, opts)
	if !ok {
		return
	}
	if r.AfterSync != nil {
		r.AfterSync()
	}
	log := r.Syncer.logger()
	switch {
	case errors.Is(res.Err, ErrSyncRunning), errors.Is(res.Err, ErrAccountRemoved), errors.Is(res.Err, ErrNoServer):
		log.Debug("sync skipped", "account", a.Name, "reason", res.Err)
	case res.Err != nil:
		log.Error("sync failed", "account", a.Name, "err", res.Err)
	}
}

// syncClaimed syncs a claimed account. The claim ends when it returns, on
// every path: the account was removed or could not be loaded, ctx was
// cancelled, a panic. ok is false when no sync ran.
func (r *Runner) syncClaimed(ctx context.Context, id int64, opts SyncOptions) (_ *store.Account, _ AccountResult, ok bool) {
	defer r.release(id)
	if r.BeforeSync != nil {
		r.BeforeSync(ctx, id)
	}
	if ctx.Err() != nil {
		return nil, AccountResult{}, false
	}
	log := r.Syncer.logger()
	a, err := r.Syncer.Store.GetAccount(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		log.Debug("sync skipped: account deleted while queued", "id", id)
		return nil, AccountResult{}, false
	}
	if err != nil {
		log.Error("load account for sync", "id", id, "err", err)
		return nil, AccountResult{}, false
	}
	return a, r.Syncer.SyncAccountWith(ctx, a, opts), true
}
