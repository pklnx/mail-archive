// Package archive copies messages from IMAP accounts into the archive.
package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/keepia/archive/internal/blobstore"
	"github.com/keepia/archive/internal/config"
	"github.com/keepia/archive/internal/crypto"
	"github.com/keepia/archive/internal/imapsync"
	"github.com/keepia/archive/internal/quota"
	"github.com/keepia/archive/internal/store"
)

// DefaultBatchSize is the number of messages committed per transaction.
const DefaultBatchSize = 100

// Syncer copies new messages from all enabled accounts.
type Syncer struct {
	Store     *store.Store
	Blobs     *blobstore.Store
	Sealer    *crypto.Sealer
	Logger    *slog.Logger
	BatchSize int
	// ReconcileInterval is how old a folder's last reconcile may get before
	// a sync compares it with the server again. Zero: only on request.
	ReconcileInterval time.Duration
	// MaxReconcileMessages: larger folders are not reconciled (default
	// DefaultMaxReconcileMessages).
	MaxReconcileMessages uint32
	// IMAP are the operator's IMAP limits, for syncs and login checks.
	IMAP IMAPSettings
	// MaxRunTime limits one account's run (DefaultMaxRunTime in the web
	// server); zero means no limit. A run at its limit keeps what it
	// committed, and the Runner queues it again if it made progress.
	MaxRunTime time.Duration
	// Dial is used to connect to IMAP servers. Defaults to imapsync.Dial.
	Dial func(context.Context, imapsync.Config) (*imapsync.Conn, error)
	// Storage is the storage gate (#397); nil stores without a limit. A
	// sync of an account whose group or the instance is full does not
	// start, and a running one stops before the next message.
	Storage *quota.Gate

	// limitRun replaces the run's time limit; a test seam.
	limitRun func(context.Context) (context.Context, context.CancelFunc)
	// afterListFlags, if set, is called with the length and capacity of a
	// reconcile's UID list; a test seam.
	afterListFlags func(n, capacity int)
}

// ErrSyncRunning is returned when another process already syncs the account.
var ErrSyncRunning = errors.New("a sync of this account is already running")

// ErrAccountRemoved is returned when syncing an account that was removed.
var ErrAccountRemoved = errors.New("account was removed")

// ErrNoServer is returned when syncing an import or smtp account.
var ErrNoServer = store.ErrNoServer

// AccountResult is the outcome of syncing one account.
type AccountResult struct {
	Account string
	OwnerID *int64
	Fetched int
	New     int // messages new to the account (store.SaveResult.Added)
	// Skipped counts messages deleted by retention that were not archived
	// again (see store.SaveBatch).
	Skipped int
	// Reconcile sums what comparing folders with the server changed.
	Reconcile store.ReconcileCounts
	// Resume reports a run that reached its time limit after it committed
	// messages: the rest follows in another run.
	Resume bool
	// Err is a *quota.FullError (errors.Is quota.ErrStorageFull) when the
	// storage gate paused the sync: at its start (no run is recorded) or
	// before a message (the run is partial; IsPaused).
	Err error
}

// IsPaused reports whether the storage gate paused the sync.
func (r AccountResult) IsPaused() bool { return errors.Is(r.Err, quota.ErrStorageFull) }

// SyncAll syncs every enabled account (or only the named ones, even if
// disabled), of all users or, with owner set, of one user. Removed,
// import and smtp accounts are skipped. A failing account does not stop the others.
func (s *Syncer) SyncAll(ctx context.Context, only []string, owner *int64, opts SyncOptions) ([]AccountResult, error) {
	accounts, err := s.Store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	var results []AccountResult
	for _, a := range accounts {
		if a.RemovedAt != nil || !a.Kind.HasServer() {
			continue
		}
		if owner != nil && (a.OwnerID == nil || *a.OwnerID != *owner) {
			continue
		}
		if len(only) > 0 && !containsFold(only, a.Name) {
			continue
		}
		if len(only) == 0 && !a.Enabled {
			continue
		}
		if err := ctx.Err(); err != nil {
			return results, err
		}
		results = append(results, s.SyncAccountWith(ctx, a, opts))
	}
	return results, nil
}

// SyncAccount syncs all selected folders of one account and records a run.
// It fails with ErrSyncRunning if the account is already being synced and
// with ErrAccountRemoved if it was removed or deleted, and with
// ErrNoServer for import accounts. Folders due by ReconcileInterval are
// compared with the server after their sync.
func (s *Syncer) SyncAccount(ctx context.Context, a *store.Account) AccountResult {
	return s.SyncAccountWith(ctx, a, SyncOptions{})
}

// SyncAccountWith is SyncAccount with options. Reconciling uses the same
// connection and lock as the sync, so it never overlaps another sync, a
// reconcile or the deletion of the account.
func (s *Syncer) SyncAccountWith(ctx context.Context, a *store.Account, opts SyncOptions) AccountResult {
	log := s.logger().With("account", a.Name)
	res := AccountResult{Account: a.Name, OwnerID: a.OwnerID}
	if a.RemovedAt != nil {
		res.Err = ErrAccountRemoved
		return res
	}
	if !a.Kind.HasServer() {
		res.Err = ErrNoServer
		return res
	}
	unlock, ok, err := s.Store.TryLockSyncForWrite(ctx, a.ID)
	if err != nil {
		res.Err = err
		return res
	}
	if !ok {
		res.Err = ErrSyncRunning
		return res
	}
	defer unlock()

	// Read the account again under the lock: it may have been changed,
	// removed or deleted since a was loaded. Deleting takes this lock too,
	// so it cannot happen while the sync runs.
	fresh, err := s.Store.GetAccount(ctx, a.ID)
	if errors.Is(err, store.ErrNotFound) || err == nil && fresh.RemovedAt != nil {
		res.Err = ErrAccountRemoved
		return res
	}
	if err != nil {
		res.Err = err
		return res
	}
	a = fresh
	// Paused while its group or the instance is full: no run, no change
	// of its health; the next run continues from the last UID.
	if err := s.Storage.Admit(a.GroupID); err != nil {
		res.Err = err
		return res
	}

	run, err := s.Store.StartSyncRun(ctx, a.ID)
	if err != nil {
		res.Err = err
		return res
	}
	// The run's own context ends at its time limit; parent's end is a
	// shutdown, which records no health.
	parent := ctx
	ctx, cancelRun := runContext(parent, s.MaxRunTime, s.limitRun)
	defer cancelRun()
	var (
		folderErrs []error
		// committed counts the batches this run committed; okFolders the
		// folders that synced cleanly.
		committed, okFolders int
		// stuck: a folder waits behind a message over the size cap.
		stuck bool
		// paused: the storage gate stopped the run (#397).
		paused bool
	)
	// Folders whose first reconcile runs now: what they find gone was
	// deleted before, not in this run, and never alerts.
	var baseline []int64
	finish := func(status string) AccountResult {
		// Also after a cancelled run: the next run's window would miss
		// what this one marked. Lost messages never outnumber the gone
		// locations, so a normal run skips the query.
		if res.Reconcile.Gone >= store.LossAlertMin {
			alert, err := s.Store.LostMessages(context.WithoutCancel(ctx), a.ID, run.ID, baseline)
			switch {
			case err != nil:
				log.Error("count lost messages", "err", err)
				if status != "failed" {
					status, res.Err = "partial", errors.Join(res.Err, fmt.Errorf("count lost messages: %w", err))
				}
			case store.LossAlertDue(alert.Lost, alert.PresentBefore):
				run.LossAlert = alert
				log.Warn("messages deleted on the server", "lost", alert.Lost, "before", alert.PresentBefore)
			}
		}
		run.Status = status
		run.MessagesFetched, run.MessagesNew = res.Fetched, res.New
		run.Reconcile = res.Reconcile
		// The stored text is shown in the web UI and sent in alerts: an
		// IMAP error's own text goes only to the log. A pause names no
		// limit: the account's users learn only that storage is full.
		switch {
		case paused && len(folderErrs) > 0:
			run.Error = DescribeError(errors.Join(folderErrs...)) + "\n" + quota.PausedText
		case paused:
			run.Error = quota.PausedText
		case res.Err != nil:
			run.Error = DescribeError(res.Err)
		}
		// A run cancelled by a shutdown says nothing about the account. A
		// folder stuck behind a large message is a failure: the account is
		// not fully archived, and the alert says so. A paused run leaves
		// the streak as it is.
		switch {
		case paused:
			run.Health = store.HealthUnchanged
		case stuck && status == "partial":
			run.Health = store.HealthFailure
		case status == "ok" || status == "partial":
			run.Health = store.HealthSuccess
		case parent.Err() == nil:
			run.Health = store.HealthFailure
		}
		// Record the outcome even if ctx was cancelled.
		if err := s.Store.FinishSyncRun(context.WithoutCancel(ctx), run); err != nil {
			log.Error("record sync run", "err", err)
		}
		return res
	}

	password, err := OpenPassword(s.Sealer, a)
	if err != nil {
		res.Err = err
		return finish("failed")
	}
	// stopped ends the run before its last folder. At the time limit it
	// is partial if it committed messages (and is resumed), else failed.
	// When the connection cannot be used again, it is partial if earlier
	// folders synced, else failed.
	stopped := func(cause error) AccountResult {
		if parent.Err() != nil {
			res.Err = parent.Err()
			return finish("failed")
		}
		progress := okFolders > 0 || committed > 0
		if atTimeLimit(parent, ctx) {
			cause = timeLimitError(s.MaxRunTime)
			progress = committed > 0
			res.Resume = progress
		}
		res.Err = errors.Join(append(folderErrs, cause)...)
		if progress {
			return finish("partial")
		}
		return finish("failed")
	}
	conn, err := s.dial(ctx, a, password)
	if err != nil {
		if atTimeLimit(parent, ctx) {
			return stopped(err)
		}
		res.Err = nameSetting(err, "")
		return finish("failed")
	}
	defer func() { _ = conn.Close() }()

	folders, err := conn.ListFolders()
	if err != nil {
		if atTimeLimit(parent, ctx) {
			return stopped(err)
		}
		res.Err = nameSetting(err, "")
		return finish("failed")
	}
	now := time.Now()
	var redial redialer
	for _, f := range folders {
		if !FolderSelected(f.Name, a.IncludedFolders, a.ExcludedFolders) {
			log.Debug("skip folder", "folder", f.Name)
			continue
		}
		if ctx.Err() != nil {
			return stopped(ctx.Err())
		}
		// One bad folder must not fail the ones after it: a connection
		// that broke (a cap, the idle deadline, a read error) is replaced.
		if conn.Broken() {
			nc, err := redial.again(conn, func() (*imapsync.Conn, error) { return s.dial(ctx, a, password) })
			if err != nil {
				log.Error("sync stopped: no new connection", "err", err)
				return stopped(nameSetting(err, ""))
			}
			conn = nc
			log.Info("connected again after a broken connection", "redials", redial.n)
		}
		flog := log.With("folder", f.Name)
		base := res
		progress := func(fetched, added int) {
			committed++
			run.MessagesFetched, run.MessagesNew = base.Fetched+fetched, base.New+added
			if err := s.Store.UpdateSyncRunProgress(ctx, run); err != nil {
				log.Debug("record sync progress", "err", err)
			}
		}
		folder, fetched, added, skipped, err := s.syncFolder(ctx, conn, a, f.Name, progress, flog)
		res.Fetched += fetched
		res.New += added
		res.Skipped += skipped
		if errors.Is(err, quota.ErrStorageFull) {
			// What came before is saved; the run ends here, before the
			// next folder, and the next one continues from the last UID.
			flog.Info("sync paused: storage limit reached", "fetched", res.Fetched, "new", res.New)
			paused = true
			res.Err = errors.Join(append(folderErrs, err)...)
			return finish("partial")
		}
		// Only a folder that synced cleanly is reconciled: its locations are
		// complete for the UIDVALIDITY that EXAMINE reported.
		if err == nil && s.reconcileDue(folder.LastReconciledAt, opts, now) {
			if folder.LastReconciledAt == nil {
				baseline = append(baseline, folder.ID)
			}
			var r store.FolderReconcile
			if r, err = s.reconcileFolder(ctx, conn, folder); err == nil {
				res.Reconcile.Add(r)
				warnOnLoss(flog, r)
			} else {
				err = fmt.Errorf("reconcile: %w", nameSetting(err, config.EnvReconcileMaxMessages))
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return stopped(fmt.Errorf("%s: %w", f.Name, err))
			}
			var st *StuckError
			if errors.As(err, &st) {
				stuck = true
			}
			flog.Error("folder sync failed", "err", err)
			folderErrs = append(folderErrs, fmt.Errorf("%s: %w", f.Name, nameSetting(err, "")))
		} else {
			okFolders++
		}
	}
	// After the loop, so folders created in this run get theirs too. Also for
	// folders the filters skip that have a row from before. Only the sidebar
	// needs it, so a failure does not make the run partial.
	if err := s.recordDelimiters(ctx, a.ID, folders); err != nil && ctx.Err() == nil {
		log.Warn("record folder delimiters", "err", err)
	}
	if err := s.reconcileVanished(ctx, a, folders, opts, now, &res.Reconcile, &baseline, log); err != nil {
		if ctx.Err() != nil {
			return stopped(err)
		}
		log.Error("reconcile vanished folders failed", "err", err)
		folderErrs = append(folderErrs, fmt.Errorf("vanished folders: %w", err))
	}
	if rc := res.Reconcile; rc.Gone+rc.Back+rc.FlagsChanged > 0 {
		log.Info("account reconciled", "folders", rc.Folders, "gone", rc.Gone, "back", rc.Back, "flags", rc.FlagsChanged)
	}
	if len(folderErrs) > 0 {
		res.Err = errors.Join(folderErrs...)
		return finish("partial")
	}
	log.Info("account synced", "fetched", res.Fetched, "new", res.New)
	return finish("ok")
}

// recordDelimiters stores the hierarchy delimiter of each listed folder.
func (s *Syncer) recordDelimiters(ctx context.Context, accountID int64, folders []imapsync.Folder) error {
	delims := make(map[string]string, len(folders))
	for _, f := range folders {
		delims[f.Name] = f.Delim
	}
	_, err := s.Store.SetFolderDelimiters(ctx, accountID, delims)
	return err
}

func (s *Syncer) dial(ctx context.Context, a *store.Account, password string) (*imapsync.Conn, error) {
	return s.dialWith(ctx, s.IMAP.config(a.Host, a.Port, string(a.TLSMode), a.Username, password))
}

func (s *Syncer) dialWith(ctx context.Context, cfg imapsync.Config) (*imapsync.Conn, error) {
	dial := s.Dial
	if dial == nil {
		dial = imapsync.Dial
	}
	return dial(ctx, cfg)
}

// CheckLogin connects with the given settings and password and lists the
// folders, to verify an account before it is saved. It takes at most a
// minute and reads at most loginCheckMaxRead bytes.
func (s *Syncer) CheckLogin(ctx context.Context, a *store.Account, password string) ([]imapsync.Folder, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cfg := s.IMAP.config(a.Host, a.Port, string(a.TLSMode), a.Username, password)
	cfg.MaxRead = loginCheckMaxRead(s.IMAP.MaxFolders)
	conn, err := s.dialWith(ctx, cfg)
	if err != nil {
		return nil, nameSetting(err, "")
	}
	defer func() { _ = conn.Close() }()
	folders, err := conn.ListFolders()
	return folders, nameSetting(err, "")
}

// ListFolders connects with the account's stored password and lists its
// folders.
func (s *Syncer) ListFolders(ctx context.Context, a *store.Account) ([]imapsync.Folder, error) {
	password, err := OpenPassword(s.Sealer, a)
	if err != nil {
		return nil, err
	}
	return s.CheckLogin(ctx, a, password)
}

// syncFolder copies new messages of one folder and leaves it examined.
// progress is called after each committed batch with the folder's counts
// so far.
func (s *Syncer) syncFolder(ctx context.Context, conn *imapsync.Conn, a *store.Account, name string,
	progress func(fetched, added int), log *slog.Logger) (folder *store.Folder, fetched, added, skipped int, err error) {
	status, err := conn.Examine(name)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	folder, err = s.Store.GetOrCreateFolder(ctx, a.ID, name)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	if folder.UIDValidity != status.UIDValidity {
		if folder.UIDValidity != 0 {
			log.Warn("UIDVALIDITY changed, rescanning folder", "old", folder.UIDValidity, "new", status.UIDValidity)
		}
		if err := s.Store.ResetFolder(ctx, folder.ID, status.UIDValidity); err != nil {
			return nil, 0, 0, 0, err
		}
		folder.UIDValidity, folder.LastUID = status.UIDValidity, 0
	}
	if status.Messages == 0 || (status.UIDNext != 0 && folder.LastUID+1 >= status.UIDNext) {
		return folder, 0, 0, 0, s.Store.TouchFolder(ctx, folder.ID)
	}

	var w *folderWriter
	w = newFolderWriter(s.Store, folder.ID, s.BatchSize, func(int) { progress(fetched, w.added) })
	w.pending = s.Storage.Pending(a.GroupID)
	defer w.pending.Settle() // files of messages that were not saved

	lookup := s.Store.NewGroupLookup(a.ID) // once per folder run
	w.blobs, w.lookup = s.Blobs, lookup
	sink := func(r io.Reader) (Stored, error) {
		// Before each file: a full group or instance writes no more.
		if err := w.admit(ctx, s.Storage, a.GroupID); err != nil {
			if errors.Is(err, quota.ErrStorageFull) {
				return Stored{}, imapsync.Stop(err)
			}
			return Stored{}, err
		}
		stored, err := PutFor(ctx, s.Blobs, r, lookup)
		if err == nil {
			w.pending.Add(stored.Blob.Size)
		}
		return stored, err
	}
	err = imapsync.FetchAfter(conn, folder.LastUID, sink, func(m imapsync.Message[Stored]) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		fetched++
		meta, err := BuildMeta(ctx, s.Store, s.Blobs, m.Body.Blob, m.Body.Created, m.Body.NewToGroup)
		if err != nil {
			return err
		}
		meta.LookupGroup = m.Body.Group
		return w.add(ctx, meta, store.Location{
			FolderID: folder.ID, UIDValidity: folder.UIDValidity, UID: m.UID,
			Flags: storedFlags(m.Flags), InternalDate: m.InternalDate,
		})
	})
	switch lim, ok := oversized(err); {
	case ok:
		err = s.holdBefore(ctx, w, folder.ID, lim, err, log)
	case errors.Is(err, quota.ErrStorageFull):
		// Paused by the storage gate: the queued messages are saved as at
		// the end of a batch, so the last UID covers every stored message.
		if ferr := w.flush(ctx); ferr != nil {
			err = ferr
		}
	case err == nil:
		err = w.flush(ctx)
	}
	if w.skipped > 0 {
		log.Info("deleted by retention, not archived again", "messages", w.skipped)
	}
	if err != nil {
		return folder, fetched, w.added, w.skipped, err
	}
	log.Debug("folder synced", "fetched", fetched, "new", w.added)
	return folder, fetched, w.added, w.skipped, nil
}

// holdBefore stops a folder's sync before a message over the size cap:
// the folder's last UID becomes at most the message's UID - 1, also if an
// earlier batch went past it (a server answering out of UID order), and
// the queued messages are saved only if all of them come before it. With
// the UID unknown nothing moves. It returns a StuckError, or the error of
// saving.
func (s *Syncer) holdBefore(ctx context.Context, w *folderWriter, folderID int64, lim *imapsync.LimitError,
	cause error, log *slog.Logger) error {
	stuck := &StuckError{UID: lim.UID, Err: cause}
	if lim.UID == 0 {
		log.Warn("a message over the size cap came without its UID; the folder's last UID stays, queued messages are not saved",
			"queued", len(w.locs))
		return stuck
	}
	if w.below(lim.UID) {
		w.maxUID = min(w.maxUID, lim.UID-1)
		if err := w.flush(ctx); err != nil {
			return err
		}
	} else {
		log.Info("queued messages not saved: some come after the large message", "queued", len(w.locs))
	}
	if err := s.Store.CapFolderLastUID(ctx, folderID, lim.UID-1); err != nil {
		return err
	}
	log.Warn("folder stops before a message over the size cap", "uid", lim.UID)
	return stuck
}

func (s *Syncer) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// FolderSelected applies the include and exclude lists (case-insensitive,
// exact names). An empty include list selects all folders.
func FolderSelected(name string, included, excluded []string) bool {
	if len(included) > 0 && !containsFold(included, name) {
		return false
	}
	return !containsFold(excluded, name)
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// SuggestExclusions returns the junk and trash folders (by special-use
// attribute) that the current filters would still archive.
func SuggestExclusions(folders []imapsync.Folder, included, excluded []string) []string {
	var out []string
	for _, f := range folders {
		role := f.SpecialUse()
		if (role == imapsync.RoleJunk || role == imapsync.RoleTrash) && FolderSelected(f.Name, included, excluded) {
			out = append(out, f.Name)
		}
	}
	return out
}
