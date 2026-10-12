package archive

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/store"
)

// ErrDeletionRunning is returned while another process executes a run.
var ErrDeletionRunning = errors.New("another process executes a deletion run")

// ErrBlobsBusy is returned when the blob lock stayed busy (a sync or import
// writing files) for the whole lock timeout.
var ErrBlobsBusy = errors.New("syncs or imports kept writing files; try again later")

// DefaultDeletionBatch is how many candidates one transaction deletes.
const DefaultDeletionBatch = 500

// DeletionOptions configures ExecuteDeletionRun.
type DeletionOptions struct {
	// Grace is how long the files outlive their rows.
	Grace time.Duration
	// LockTimeout is how long each batch waits for the blob lock (default
	// 10 minutes; a long first sync holds it shared).
	LockTimeout time.Duration
	BatchSize   int
	// Audit returns the audit entry of a batch ("retention.batch") or of
	// the end of the run ("retention.finish"); nil writes none.
	Audit    func(action string) *store.AuditEntry
	Progress func(deleted, kept int)
	Logger   *slog.Logger
}

// ExecuteDeletionRun executes a confirmed run, or resumes an interrupted
// one: in batches, each under the exclusive blob lock and in one
// transaction, it deletes the candidates that are still deletable and
// keeps the others (a rule removed, a hold added or a new location since
// the preview). A done run is returned as it is.
func ExecuteDeletionRun(ctx context.Context, st *store.Store, runID int64, opts DeletionOptions) (*store.DeletionRun, error) {
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultDeletionBatch
	}
	if opts.LockTimeout <= 0 {
		opts.LockTimeout = 10 * time.Minute
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	unlock, ok, err := st.TryLockDeletionRun(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrDeletionRunning
	}
	defer unlock()
	run, err := st.DeletionRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	switch run.State {
	case store.RunDone:
		return run, nil
	case store.RunConfirmed:
	default:
		return nil, errors.Join(store.ErrRunState, errors.New("run "+run.State+": only a confirmed run is executed"))
	}
	// Holds added since the preview: their matches must be known before
	// the first batch re-checks.
	if _, err := st.MatchLegalHolds(ctx); err != nil {
		return nil, err
	}
	deleted, kept := run.Deleted, run.Kept
	after := ""
	for {
		shas, err := st.PendingCandidates(ctx, runID, after, opts.BatchSize)
		if err != nil {
			return nil, err
		}
		if len(shas) == 0 {
			break
		}
		unlockBlobs, err := waitBlobLock(ctx, st, opts.LockTimeout)
		if err != nil {
			return nil, err
		}
		if unlockBlobs == nil {
			return nil, ErrBlobsBusy
		}
		bctx := ctx
		if opts.Audit != nil {
			bctx = store.WithAudit(ctx, opts.Audit("retention.batch"))
		}
		d, k, err := st.DeleteBatch(bctx, runID, shas, opts.Grace)
		unlockBlobs()
		if err != nil {
			return nil, err
		}
		deleted, kept = deleted+d, kept+k
		opts.Logger.Info("deletion batch", "run", runID, "deleted", d, "kept", k)
		if opts.Progress != nil {
			opts.Progress(deleted, kept)
		}
		after = shas[len(shas)-1]
	}
	fctx := ctx
	if opts.Audit != nil {
		fctx = store.WithAudit(ctx, opts.Audit("retention.finish"))
	}
	if err := st.FinishDeletionRun(fctx, runID); err != nil {
		return nil, err
	}
	return st.DeletionRun(ctx, runID)
}

// waitBlobLock takes the blob lock exclusively, retrying every second for
// up to timeout. It returns a nil unlock when the lock stayed busy.
func waitBlobLock(ctx context.Context, st *store.Store, timeout time.Duration) (func(), error) {
	deadline := time.Now().Add(timeout)
	for {
		unlock, ok, err := st.TryLockBlobs(ctx)
		if err != nil {
			return nil, err
		}
		if ok {
			return unlock, nil
		}
		wait := min(time.Second, time.Until(deadline))
		if wait <= 0 {
			return nil, nil
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// SweepOptions configures SweepBlobs.
type SweepOptions struct {
	LockTimeout time.Duration // default 1 minute
	// Audit returns the "retention.sweep" entry, written when files were
	// removed; nil writes none.
	Audit  func() *store.AuditEntry
	Logger *slog.Logger
}

// sweepPage is how many due removals are read at a time.
const sweepPage = 1000

// SweepBlobs removes the files of deleted messages whose grace period has
// ended, under the exclusive blob lock. A file whose message was archived
// again stays. Without due removals it takes no lock, so syncs and SMTP
// deliveries never wait for nothing.
func SweepBlobs(ctx context.Context, st *store.Store, blobs *blobstore.Store, opts SweepOptions) (int, error) {
	if opts.LockTimeout <= 0 {
		opts.LockTimeout = time.Minute
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	due, err := st.BlobRemovalDue(ctx)
	if err != nil || !due {
		return 0, err
	}
	unlock, err := waitBlobLock(ctx, st, opts.LockTimeout)
	if err != nil {
		return 0, err
	}
	if unlock == nil {
		return 0, ErrBlobsBusy
	}
	defer unlock()
	removed := 0
	var afterDue time.Time
	afterSHA := ""
	for {
		rows, err := st.DueBlobRemovals(ctx, afterDue, afterSHA, sweepPage)
		if err != nil {
			return removed, err
		}
		if len(rows) == 0 {
			break
		}
		shas := make([]string, 0, len(rows))
		for _, r := range rows {
			if !r.Archived {
				if err := blobs.Remove(r.SHA256); err != nil {
					return removed, err
				}
				removed++
			}
			shas = append(shas, r.SHA256)
		}
		// A crash before this only removes again.
		if _, err := st.DoneBlobRemovals(ctx, shas); err != nil {
			return removed, err
		}
		last := rows[len(rows)-1]
		afterDue, afterSHA = last.DueAt, last.SHA256
	}
	if removed > 0 {
		opts.Logger.Info("files of deleted messages removed", "files", removed)
		if opts.Audit != nil {
			e := opts.Audit()
			e.Detail("files", removed)
			if err := st.Audit(ctx, *e); err != nil {
				return removed, err
			}
		}
	}
	return removed, nil
}

// Retention is the retention job of the web server: it resumes a
// confirmed deletion run (interrupted, or confirmed in the web UI) and
// sweeps the files whose grace period ended, at start, every CheckEvery,
// and when woken.
type Retention struct {
	Store      *store.Store
	Blobs      *blobstore.Store
	Grace      time.Duration
	CheckEvery time.Duration // default one hour
	Logger     *slog.Logger

	once sync.Once
	wake chan struct{}
}

func (r *Retention) init() {
	r.once.Do(func() { r.wake = make(chan struct{}, 1) })
}

// Wake makes Run check at once, for a run confirmed meanwhile.
func (r *Retention) Wake() {
	r.init()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run checks until ctx ends.
func (r *Retention) Run(ctx context.Context) {
	r.init()
	every := r.CheckEvery
	if every <= 0 {
		every = time.Hour
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		r.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.wake:
		}
	}
}

// RunOnce executes a confirmed run, if any, then sweeps. Errors are
// logged; the next check retries.
func (r *Retention) RunOnce(ctx context.Context) {
	log := r.Logger
	if log == nil {
		log = slog.Default()
	}
	if id, err := r.Store.OpenDeletionRun(ctx); err != nil {
		log.Error("retention check failed", "err", err)
	} else if id != 0 {
		run, err := r.Store.DeletionRun(ctx, id)
		if err == nil && run.State == store.RunConfirmed {
			audit := func(action string) *store.AuditEntry {
				return &store.AuditEntry{Actor: store.ActorCLI, Action: action, Details: map[string]any{"by": "web server"}}
			}
			run, err = ExecuteDeletionRun(ctx, r.Store, id, DeletionOptions{Grace: r.Grace, Audit: audit, Logger: log})
			switch {
			case errors.Is(err, ErrDeletionRunning):
				log.Debug("deletion run executed elsewhere", "run", id)
			case err != nil && ctx.Err() == nil:
				log.Error("deletion run failed; it resumes at the next check", "run", id, "err", err)
			case err == nil:
				log.Info("deletion run done", "run", id, "deleted", run.Deleted, "kept", run.Kept)
			}
		} else if err != nil {
			log.Error("retention check failed", "err", err)
		}
	}
	audit := func() *store.AuditEntry {
		return &store.AuditEntry{Actor: store.ActorCLI, Action: "retention.sweep", Details: map[string]any{"by": "web server"}}
	}
	if _, err := SweepBlobs(ctx, r.Store, r.Blobs, SweepOptions{Audit: audit, Logger: log}); err != nil && ctx.Err() == nil {
		if errors.Is(err, ErrBlobsBusy) {
			log.Info("file sweep postponed: syncs kept writing files")
		} else {
			log.Error("file sweep failed", "err", err)
		}
	}
}
