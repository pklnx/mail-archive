package archive

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
)

// ErrPurgeRunning is returned while another process executes a purge.
var ErrPurgeRunning = errors.New("another process executes an account purge")

// PurgeOptions configures ExecutePurge.
type PurgeOptions struct {
	// Grace is how long the files of deleted messages outlive their rows.
	Grace time.Duration
	// LockTimeout is how long each batch waits for the blob lock (default
	// 10 minutes).
	LockTimeout time.Duration
	BatchSize   int // default DefaultDeletionBatch
	// Audit returns the audit entry of a batch ("account.purge_batch"), of
	// the end ("account.purge_finish") or of a cancellation
	// ("account.purge_cancel"); nil writes none.
	Audit func(action string) *store.AuditEntry
	// Progress is called after each batch with the purge's totals so far.
	Progress func(p *store.AccountPurge)
	Logger   *slog.Logger
}

// ExecutePurge executes a confirmed purge, or resumes an interrupted one:
// it holds the purge lock (ErrPurgeRunning) and the account's sync lock
// (ErrSyncRunning) throughout, matches the filter holds, and purges the
// account in batches, each under the exclusive blob lock and in one
// transaction (store.PurgeBatch), then deletes the account
// (store.FinishPurge). The locks and the batches share one connection
// (store.PurgeSession), so writers waiting for the blob lock can never
// take the connection a batch needs. A rule or a hold found on the way, or more
// messages to delete than confirmed, cancel the purge: what was deleted
// stays deleted, the account stays removed with the rest of its mail, and
// the error says why. A done purge is returned as it is.
func ExecutePurge(ctx context.Context, st *store.Store, purgeID int64, opts PurgeOptions) (*store.AccountPurge, error) {
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultDeletionBatch
	}
	if opts.LockTimeout <= 0 {
		opts.LockTimeout = 10 * time.Minute
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	// All locks and the batches on one connection (store.PurgeSession):
	// while the blob lock is held, waiting writers may hold every other
	// connection of the pool.
	sess, ok, err := st.StartPurgeSession(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrPurgeRunning
	}
	defer sess.Close(ctx)
	p, err := st.AccountPurge(ctx, purgeID)
	if err != nil {
		return nil, err
	}
	switch p.State {
	case store.PurgeDone:
		return p, nil
	case store.PurgeConfirmed:
	default:
		return nil, fmt.Errorf("purge %d is %s: %w", purgeID, p.State, store.ErrPurgeState)
	}
	if ok, err := sess.TryLockSync(ctx, p.AccountID); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrSyncRunning
	}
	// Holds added since the confirmation: their matches must be known
	// before the first batch checks.
	if _, err := st.MatchLegalHolds(ctx); err != nil {
		return nil, err
	}
	if err := st.CheckPurgeCount(ctx, purgeID); err != nil {
		return nil, cancelPurge(ctx, st, purgeID, opts, err)
	}
	for {
		ok, err := waitLock(ctx, opts.LockTimeout, func() (bool, error) { return sess.TryLockBlobs(ctx) })
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrBlobsBusy
		}
		bctx := ctx
		if opts.Audit != nil {
			bctx = store.WithAudit(ctx, opts.Audit("account.purge_batch"))
		}
		res, err := sess.Batch(bctx, purgeID, opts.BatchSize, opts.Grace)
		if uerr := sess.UnlockBlobs(ctx); uerr != nil && err == nil {
			return nil, uerr
		}
		if err != nil {
			return nil, cancelPurge(ctx, st, purgeID, opts, err)
		}
		if res.Locations > 0 {
			opts.Logger.Info("purge batch", "purge", purgeID, "locations", res.Locations, "deleted", res.Deleted, "kept", res.Kept)
			if opts.Progress != nil {
				cur, err := st.AccountPurge(ctx, purgeID)
				if err != nil {
					return nil, err
				}
				opts.Progress(cur)
			}
		}
		if res.Done {
			break
		}
	}
	fctx := ctx
	if opts.Audit != nil {
		fctx = store.WithAudit(ctx, opts.Audit("account.purge_finish"))
	}
	done, err := sess.Finish(fctx, purgeID)
	if err != nil {
		return nil, cancelPurge(ctx, st, purgeID, opts, err)
	}
	return done, nil
}

// cancelPurge cancels the purge if err is a reason to (a blocker, or more
// messages to delete than confirmed) and returns a PurgeCancelledError
// wrapping err. Other errors (a lock, the database, a changed account)
// are returned as they are and leave the purge confirmed, to be resumed
// or cancelled by the admin.
func cancelPurge(ctx context.Context, st *store.Store, purgeID int64, opts PurgeOptions, err error) error {
	var blocked *store.PurgeBlockedError
	var changed *store.PurgeChangedError
	if !errors.As(err, &blocked) && !errors.As(err, &changed) {
		return err
	}
	cctx := context.WithoutCancel(ctx)
	if opts.Audit != nil {
		cctx = store.WithAudit(cctx, opts.Audit("account.purge_cancel"))
	}
	if cerr := st.CancelPurge(cctx, purgeID, err.Error()); cerr != nil {
		return errors.Join(err, fmt.Errorf("cancel purge %d: %w", purgeID, cerr))
	}
	opts.Logger.Warn("purge cancelled", "purge", purgeID, "reason", err.Error())
	return &PurgeCancelledError{PurgeID: purgeID, Err: err}
}

// PurgeCancelledError is returned when ExecutePurge cancelled a purge;
// Err is why.
type PurgeCancelledError struct {
	PurgeID int64
	Err     error
}

func (e *PurgeCancelledError) Error() string {
	return fmt.Sprintf("purge %d cancelled: %v", e.PurgeID, e.Err)
}

func (e *PurgeCancelledError) Unwrap() error { return e.Err }
