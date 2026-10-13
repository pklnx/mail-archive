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

// FillRunner runs fills in the background for the web server, one at a
// time and apart from the sync runner, so a fill of a large mailbox does not
// hold back the scheduled syncs. It also continues fills that a stopped
// process left running: those still have their password.
type FillRunner struct {
	Filler *Filler
	// CheckEvery is how often running fills are picked up (default one
	// minute).
	CheckEvery time.Duration

	mu     sync.Mutex
	queue  []int64         // fill IDs
	queued map[int64]int64 // fill ID -> account ID
	wake   chan struct{}
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

// Queued reports the accounts whose fill waits to start.
func (r *FillRunner) Queued() map[int64]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int64]bool, len(r.queued))
	for _, acc := range r.queued {
		out[acc] = true
	}
	return out
}

func (r *FillRunner) init() {
	if r.queued == nil {
		r.queued = map[int64]int64{}
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
	delete(r.queued, id)
	return id, true
}

// Run works through the queue until ctx is cancelled. At start and every
// CheckEvery it queues the running fills, so one that a stopped process
// left behind continues; a fill whose account lock another process holds
// (the command line) is skipped until then.
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
		for id, ok := r.next(); ok && ctx.Err() == nil; id, ok = r.next() {
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
	for _, f := range fills {
		r.Enqueue(f.ID, f.AccountID)
	}
	return nil
}

func (r *FillRunner) runOne(ctx context.Context, id int64) {
	log := r.Filler.logger().With("fill", id)
	fill, err := r.Filler.Run(ctx, id)
	switch {
	case errors.Is(err, ErrFillBusy), errors.Is(err, store.ErrFillNotRunning), errors.Is(err, store.ErrNotFound):
		log.Debug("fill skipped", "reason", err)
		return
	case ctx.Err() != nil:
		// Shutting down: the fill stays running and continues at the next start.
		log.Info("fill interrupted; it continues at the next start")
		return
	case err != nil:
		log.Error("fill failed", "err", err)
		return
	}
	name := ""
	if a, err := r.Filler.Store.GetAccount(ctx, fill.AccountID); err == nil {
		name = a.Name
	}
	if err := r.Filler.Store.Audit(context.WithoutCancel(ctx), FillDoneEntry(fill, name, "web server")); err != nil {
		log.Error("audit entry of the fill's end lost", "err", err)
	}
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
