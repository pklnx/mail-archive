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

	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/store"
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
	// Dial is used to connect to IMAP servers. Defaults to imapsync.Dial.
	Dial func(context.Context, imapsync.Config) (*imapsync.Conn, error)
}

// ErrSyncRunning is returned when another process already syncs the account.
var ErrSyncRunning = errors.New("a sync of this account is already running")

// ErrAccountRemoved is returned when syncing an account that was removed.
var ErrAccountRemoved = errors.New("account was removed")

// ErrImportAccount is returned when syncing an import account.
var ErrImportAccount = store.ErrImportAccount

// AccountResult is the outcome of syncing one account.
type AccountResult struct {
	Account string
	OwnerID *int64
	Fetched int
	New     int
	// Reconcile sums what comparing folders with the server changed.
	Reconcile store.ReconcileCounts
	Err       error
}

// SyncAll syncs every enabled account (or only the named ones, even if
// disabled), of all users or, with owner set, of one user. Removed and
// import accounts are skipped. A failing account does not stop the others.
func (s *Syncer) SyncAll(ctx context.Context, only []string, owner *int64, opts SyncOptions) ([]AccountResult, error) {
	accounts, err := s.Store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	var results []AccountResult
	for _, a := range accounts {
		if a.RemovedAt != nil || a.Kind == store.KindImport {
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
// ErrImportAccount for import accounts. Folders due by ReconcileInterval are
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
	if a.Kind == store.KindImport {
		res.Err = ErrImportAccount
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

	run, err := s.Store.StartSyncRun(ctx, a.ID)
	if err != nil {
		res.Err = err
		return res
	}
	var folderErrs []error
	finish := func(status string) AccountResult {
		run.Status = status
		run.MessagesFetched, run.MessagesNew = res.Fetched, res.New
		run.Reconcile = res.Reconcile
		if res.Err != nil {
			run.Error = res.Err.Error()
		}
		// A run cancelled by a shutdown says nothing about the account.
		switch {
		case status == "ok" || status == "partial":
			run.Health = store.HealthSuccess
		case ctx.Err() == nil:
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
	conn, err := s.dial(ctx, a, password)
	if err != nil {
		res.Err = err
		return finish("failed")
	}
	defer func() { _ = conn.Close() }()

	folders, err := conn.ListFolders()
	if err != nil {
		res.Err = err
		return finish("failed")
	}
	now := time.Now()
	for _, f := range folders {
		if !FolderSelected(f.Name, a.IncludedFolders, a.ExcludedFolders) {
			log.Debug("skip folder", "folder", f.Name)
			continue
		}
		flog := log.With("folder", f.Name)
		base := res
		progress := func(fetched, added int) {
			run.MessagesFetched, run.MessagesNew = base.Fetched+fetched, base.New+added
			if err := s.Store.UpdateSyncRunProgress(ctx, run); err != nil {
				log.Debug("record sync progress", "err", err)
			}
		}
		folder, fetched, added, err := s.syncFolder(ctx, conn, a, f.Name, progress, flog)
		res.Fetched += fetched
		res.New += added
		// Only a folder that synced cleanly is reconciled: its locations are
		// complete for the UIDVALIDITY that EXAMINE reported.
		if err == nil && s.reconcileDue(folder.LastReconciledAt, opts, now) {
			var r store.FolderReconcile
			if r, err = s.reconcileFolder(ctx, conn, folder); err == nil {
				res.Reconcile.Add(r)
				warnOnLoss(flog, r)
			} else {
				err = fmt.Errorf("reconcile: %w", err)
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				res.Err = ctx.Err()
				return finish("failed")
			}
			flog.Error("folder sync failed", "err", err)
			folderErrs = append(folderErrs, fmt.Errorf("%s: %w", f.Name, err))
		}
	}
	if err := s.reconcileVanished(ctx, a, folders, opts, now, &res.Reconcile, log); err != nil {
		if ctx.Err() != nil {
			res.Err = ctx.Err()
			return finish("failed")
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

func (s *Syncer) dial(ctx context.Context, a *store.Account, password string) (*imapsync.Conn, error) {
	dial := s.Dial
	if dial == nil {
		dial = imapsync.Dial
	}
	return dial(ctx, imapsync.Config{
		Host: a.Host, Port: a.Port, TLSMode: string(a.TLSMode),
		Username: a.Username, Password: password,
	})
}

// CheckLogin connects with the given settings and password and lists the
// folders, to verify an account before it is saved.
func (s *Syncer) CheckLogin(ctx context.Context, a *store.Account, password string) ([]imapsync.Folder, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	conn, err := s.dial(ctx, a, password)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	return conn.ListFolders()
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

type storedBody struct {
	blob    blobstore.Blob
	created bool
}

// syncFolder copies new messages of one folder and leaves it examined.
// progress is called after each committed batch with the folder's counts
// so far.
func (s *Syncer) syncFolder(ctx context.Context, conn *imapsync.Conn, a *store.Account, name string,
	progress func(fetched, added int), log *slog.Logger) (folder *store.Folder, fetched, added int, err error) {
	status, err := conn.Examine(name)
	if err != nil {
		return nil, 0, 0, err
	}
	folder, err = s.Store.GetOrCreateFolder(ctx, a.ID, name)
	if err != nil {
		return nil, 0, 0, err
	}
	if folder.UIDValidity != status.UIDValidity {
		if folder.UIDValidity != 0 {
			log.Warn("UIDVALIDITY changed, rescanning folder", "old", folder.UIDValidity, "new", status.UIDValidity)
		}
		if err := s.Store.ResetFolder(ctx, folder.ID, status.UIDValidity); err != nil {
			return nil, 0, 0, err
		}
		folder.UIDValidity, folder.LastUID = status.UIDValidity, 0
	}
	if status.Messages == 0 || (status.UIDNext != 0 && folder.LastUID+1 >= status.UIDNext) {
		return folder, 0, 0, s.Store.TouchFolder(ctx, folder.ID)
	}

	var w *folderWriter
	w = newFolderWriter(s.Store, folder.ID, s.BatchSize, func(int) { progress(fetched, w.added) })

	sink := func(r io.Reader) (storedBody, error) {
		blob, created, err := s.Blobs.Put(r)
		return storedBody{blob, created}, err
	}
	err = imapsync.FetchAfter(conn, folder.LastUID, sink, func(m imapsync.Message[storedBody]) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		fetched++
		meta, err := BuildMeta(ctx, s.Store, s.Blobs, m.Body.blob, m.Body.created)
		if err != nil {
			return err
		}
		return w.add(ctx, meta, store.Location{
			FolderID: folder.ID, UIDValidity: folder.UIDValidity, UID: m.UID,
			Flags: storedFlags(m.Flags), InternalDate: m.InternalDate,
		})
	})
	if err == nil {
		err = w.flush(ctx)
	}
	if err != nil {
		return folder, fetched, w.added, err
	}
	log.Debug("folder synced", "fetched", fetched, "new", w.added)
	return folder, fetched, w.added, nil
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
