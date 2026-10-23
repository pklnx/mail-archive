package archive

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
)

// RecurringStartHold is how long recurring fills wait after the web server
// started before they run: after an outage, the mail server delivers the
// journal copies it queued meanwhile within this time (Postfix retries at
// least every 4000 seconds by default), and a recurring fill must not
// import the mailbox copies first.
const RecurringStartHold = 70 * time.Minute

// FillRunner runs fills in the background for the web server, one at a
// time and apart from the sync runner, so a fill of a large mailbox does not
// hold back the scheduled syncs. It also continues fills that a stopped
// process left running: those still have their password. Recurring fills
// run on the sync schedule, each every its interval.
type FillRunner struct {
	Filler *Filler
	// Schedule is the sync runner's: a recurring fill runs every
	// Schedule.Every(its interval), counted from the start of its last run.
	// A zero Schedule.Sync turns recurring runs off.
	Schedule Schedule
	// HoldUntil: no run of a recurring fill starts before this time (the
	// web server's start plus RecurringStartHold). Zero: no hold.
	HoldUntil time.Time
	// Now returns the current time (default time.Now), for tests.
	Now func() time.Time
	// CheckEvery is how often running and due fills are picked up (default
	// one minute).
	CheckEvery time.Duration
	// AfterRun, if set, is called after each run, for example to deliver
	// notifications right away. The fill's claim (see Pending) has ended
	// by then.
	AfterRun func()
	// BeforeRun, if set, is called for tests once Run has taken a fill
	// from the queue (it is claimed) and before it runs. A hook that
	// blocks must also return when ctx is done.
	BeforeRun func(ctx context.Context, fillID int64)

	mu     sync.Mutex
	queue  []int64         // fill IDs
	queued map[int64]int64 // fill ID -> account ID
	// claimed holds the fills Run has taken from the queue and whose run
	// has not returned yet, by fill ID.
	claimed map[int64]fillClaim
	wake    chan struct{}
}

// fillClaim is a fill Run took from the queue: its account, and when.
type fillClaim struct {
	account int64
	at      time.Time
}

// Enqueue queues a running fill of the account. A fill already waiting is
// not added twice.
func (r *FillRunner) Enqueue(fillID, accountID int64) {
	r.mu.Lock()
	r.init()
	if _, ok := r.queued[fillID]; !ok {
		r.queued[fillID] = accountID
		r.queue = append(r.queue, fillID)
	}
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Pending reports the accounts whose fill this runner works on: waiting
// in the queue, or taken from it and not done yet (claimed). A fill taken
// from the queue is claimed in the same step, so it is in one of the two
// sets until its run has returned. The value is when Run took the
// account's fill from the queue (Now), zero while it only waits in the
// queue.
func (r *FillRunner) Pending() map[int64]time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int64]time.Time, len(r.queued)+len(r.claimed))
	for _, acc := range r.queued {
		if _, ok := out[acc]; !ok {
			out[acc] = time.Time{}
		}
	}
	for _, c := range r.claimed {
		out[c.account] = c.at
	}
	return out
}

// PendingFills is Pending by fill ID.
func (r *FillRunner) PendingFills() map[int64]time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int64]time.Time, len(r.queued)+len(r.claimed))
	for id := range r.queued {
		out[id] = time.Time{}
	}
	for id, c := range r.claimed {
		out[id] = c.at
	}
	return out
}

// release ends the claim that next took on the fill.
func (r *FillRunner) release(id int64) {
	r.mu.Lock()
	delete(r.claimed, id)
	r.mu.Unlock()
}

func (r *FillRunner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// held reports whether recurring fills still wait for the hold after the
// start.
func (r *FillRunner) held() bool {
	return r.now().Before(r.HoldUntil)
}

func (r *FillRunner) init() {
	if r.queued == nil {
		r.queued = map[int64]int64{}
		r.claimed = map[int64]fillClaim{}
		r.wake = make(chan struct{}, 1)
	}
}

func (r *FillRunner) next() (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.queue) == 0 {
		return 0, false
	}
	id := r.queue[0]
	r.queue = slices.Delete(r.queue, 0, 1)
	r.claimed[id] = fillClaim{account: r.queued[id], at: r.now()}
	delete(r.queued, id)
	return id, true
}

// Run works through the queue until ctx is cancelled. At start and every
// CheckEvery it queues the running fills, so one that a stopped process
// left behind continues, and the recurring fills that are due; a fill whose
// account lock another process holds (the command line) is skipped until
// then.
func (r *FillRunner) Run(ctx context.Context) {
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
	log := r.Filler.logger()
	for {
		if err := r.enqueueRunning(ctx); err != nil && ctx.Err() == nil {
			log.Error("look for running fills", "err", err)
		}
		if err := r.enqueueDue(ctx); err != nil && ctx.Err() == nil {
			log.Error("check the schedule of recurring fills", "err", err)
		}
		// ctx is checked before next, which claims the fill, so no fill
		// is claimed without runOne, which ends every claim.
		for ctx.Err() == nil {
			id, ok := r.next()
			if !ok {
				break
			}
			r.runOne(ctx, id)
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-tick.C:
		}
	}
}

func (r *FillRunner) enqueueRunning(ctx context.Context) error {
	fills, err := r.Filler.Store.RunningFills(ctx)
	if err != nil {
		return err
	}
	held := r.held()
	for _, f := range fills {
		if held && f.Recurring() {
			continue
		}
		r.Enqueue(f.ID, f.AccountID)
	}
	return nil
}

// enqueueDue queues the recurring fills that are due (see dueFills).
func (r *FillRunner) enqueueDue(ctx context.Context) error {
	if r.Schedule.Sync <= 0 || r.held() {
		return nil
	}
	fills, err := r.Filler.Store.RecurringFills(ctx)
	if err != nil {
		return err
	}
	for _, f := range dueFills(fills, r.Schedule, r.now(), r.HoldUntil) {
		r.Enqueue(f.ID, f.AccountID)
	}
	return nil
}

// dueFills returns the recurring fills between runs whose next run (see
// Schedule.NextFill) has come, none before holdUntil.
func dueFills(fills []store.Fill, s Schedule, now, holdUntil time.Time) []store.Fill {
	if now.Before(holdUntil) {
		return nil
	}
	var due []store.Fill
	for _, f := range fills {
		if next, ok := s.NextFill(&f); ok && !now.Before(next) {
			due = append(due, f)
		}
	}
	return due
}

// runOne runs a claimed fill; its claim has ended before AfterRun.
func (r *FillRunner) runOne(ctx context.Context, id int64) {
	if r.runClaimed(ctx, id) && r.AfterRun != nil {
		r.AfterRun()
	}
}

// runClaimed runs a claimed fill. The claim ends when it returns, on every
// path: the hold, a skipped or interrupted fill, ctx was cancelled, a
// panic. It reports whether the fill was run.
func (r *FillRunner) runClaimed(ctx context.Context, id int64) bool {
	defer r.release(id)
	if r.BeforeRun != nil {
		r.BeforeRun(ctx, id)
	}
	if ctx.Err() != nil {
		return false
	}
	log := r.Filler.logger().With("fill", id)
	if r.held() {
		// Queued directly (a new or resumed recurring fill): it runs once
		// the hold is over, picked up as a running fill.
		if f, err := r.Filler.Store.GetFill(ctx, id); err == nil && f.Recurring() {
			log.Info("recurring fill waits for the hold after the start", "until", r.HoldUntil)
			return false
		}
	}
	fill, err := r.Filler.Run(ctx, id)
	switch {
	case errors.Is(err, ErrFillBusy), errors.Is(err, ErrFillSkipped), errors.Is(err, store.ErrFillNotRunning),
		errors.Is(err, store.ErrNotFound):
		log.Debug("fill skipped", "reason", err)
		return true
	case ctx.Err() != nil:
		// Shutting down: the fill stays running and continues at the next start.
		log.Info("fill interrupted; it continues at the next start")
		return true
	case err != nil:
		log.Error("fill failed", "err", err)
		return true
	case fill.Recurring():
		// Like scheduled syncs, runs of a recurring fill are not audited.
		return true
	}
	name := ""
	if a, err := r.Filler.Store.GetAccount(ctx, fill.AccountID); err == nil {
		name = a.Name
	}
	if err := r.Filler.Store.Audit(context.WithoutCancel(ctx), FillDoneEntry(fill, name, "web server")); err != nil {
		log.Error("audit entry of the fill's end lost", "err", err)
	}
	return true
}

// FillTotals sums the counts of a fill's folders.
func FillTotals(f *store.Fill) store.FillCounts {
	var t store.FillCounts
	for _, ff := range f.Folders {
		t.Add(ff.Counts)
	}
	return t
}

// AuditFillDone adds the end of a fill to an audit entry: its status and
// the counts.
func AuditFillDone(e *store.AuditEntry, f *store.Fill) {
	t := FillTotals(f)
	e.Detail("fill", f.ID)
	e.Detail("status", f.Status)
	e.Detail("scanned", t.Scanned)
	e.Detail("imported", t.Imported)
	e.Detail("new", t.New)
	e.Detail("after_cutoff", t.AfterCutoff)
	e.Detail("journaled", t.Journaled)
}

// FillDoneEntry is the data.fill_done entry of a fill that a process ran
// on its own, like the web server: there is no user, as for the deletion
// runs it executes.
func FillDoneEntry(f *store.Fill, account, by string) store.AuditEntry {
	e := &store.AuditEntry{Actor: store.ActorCLI, Action: "data.fill_done", Details: map[string]any{"by": by}}
	e.SetTarget(store.TargetAccount, strconv.FormatInt(f.AccountID, 10), account)
	AuditFillDone(e, f)
	return *e
}

// FillIntervalDetail is a fill's own interval for the audit log: "default"
// or the duration, null for a fill that was never recurring.
func FillIntervalDetail(f *store.Fill) any {
	if !f.Recurring() {
		return nil
	}
	return FormatOwnInterval(f.Interval)
}
