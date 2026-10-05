// Package archive copies messages from IMAP accounts into the archive.
package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/mime"
	"github.com/pklnx/mail-archive/internal/store"
)

// DefaultBatchSize is the number of messages committed per transaction.
const DefaultBatchSize = 100

// PasswordContext returns the associated data used to encrypt an account's
// password, binding the ciphertext to the account name.
func PasswordContext(accountName string) []byte {
	return []byte("account-password:" + accountName)
}

// Syncer copies new messages from all enabled accounts.
type Syncer struct {
	Store     *store.Store
	Blobs     *blobstore.Store
	Sealer    *crypto.Sealer
	Logger    *slog.Logger
	BatchSize int
	// Dial is used to connect to IMAP servers. Defaults to imapsync.Dial.
	Dial func(context.Context, imapsync.Config) (*imapsync.Conn, error)
}

// AccountResult is the outcome of syncing one account.
type AccountResult struct {
	Account string
	Fetched int
	New     int
	Err     error
}

// SyncAll syncs every enabled account (or only the named ones). A failing
// account does not stop the others.
func (s *Syncer) SyncAll(ctx context.Context, only []string) ([]AccountResult, error) {
	accounts, err := s.Store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	var results []AccountResult
	for _, a := range accounts {
		if len(only) > 0 && !containsFold(only, a.Name) {
			continue
		}
		if len(only) == 0 && !a.Enabled {
			continue
		}
		if err := ctx.Err(); err != nil {
			return results, err
		}
		results = append(results, s.SyncAccount(ctx, a))
	}
	return results, nil
}

// SyncAccount syncs all selected folders of one account and records a run.
func (s *Syncer) SyncAccount(ctx context.Context, a *store.Account) AccountResult {
	log := s.logger().With("account", a.Name)
	res := AccountResult{Account: a.Name}

	run, err := s.Store.StartSyncRun(ctx, a.ID)
	if err != nil {
		res.Err = err
		return res
	}
	var folderErrs []error
	finish := func(status string) AccountResult {
		run.Status = status
		run.MessagesFetched, run.MessagesNew = res.Fetched, res.New
		if res.Err != nil {
			run.Error = res.Err.Error()
		}
		// Record the outcome even if ctx was cancelled.
		if err := s.Store.FinishSyncRun(context.WithoutCancel(ctx), run); err != nil {
			log.Error("record sync run", "err", err)
		}
		return res
	}

	password, err := s.Sealer.Open(a.PasswordEnc, PasswordContext(a.Name))
	if err != nil {
		res.Err = fmt.Errorf("decrypt password: %w", err)
		return finish("failed")
	}
	dial := s.Dial
	if dial == nil {
		dial = imapsync.Dial
	}
	conn, err := dial(ctx, imapsync.Config{
		Host: a.Host, Port: a.Port, TLSMode: string(a.TLSMode),
		Username: a.Username, Password: string(password),
	})
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
	for _, f := range folders {
		if !FolderSelected(f.Name, a.IncludedFolders, a.ExcludedFolders) {
			log.Debug("skip folder", "folder", f.Name)
			continue
		}
		fetched, added, err := s.syncFolder(ctx, conn, a, f.Name, log.With("folder", f.Name))
		res.Fetched += fetched
		res.New += added
		if err != nil {
			if ctx.Err() != nil {
				res.Err = ctx.Err()
				return finish("failed")
			}
			log.Error("folder sync failed", "err", err)
			folderErrs = append(folderErrs, fmt.Errorf("%s: %w", f.Name, err))
		}
	}
	if len(folderErrs) > 0 {
		res.Err = errors.Join(folderErrs...)
		return finish("partial")
	}
	log.Info("account synced", "fetched", res.Fetched, "new", res.New)
	return finish("ok")
}

type storedBody struct {
	blob    blobstore.Blob
	created bool
}

func (s *Syncer) syncFolder(ctx context.Context, conn *imapsync.Conn, a *store.Account, name string, log *slog.Logger) (fetched, added int, err error) {
	status, err := conn.Examine(name)
	if err != nil {
		return 0, 0, err
	}
	folder, err := s.Store.GetOrCreateFolder(ctx, a.ID, name)
	if err != nil {
		return 0, 0, err
	}
	if folder.UIDValidity != status.UIDValidity {
		if folder.UIDValidity != 0 {
			log.Warn("UIDVALIDITY changed, rescanning folder", "old", folder.UIDValidity, "new", status.UIDValidity)
		}
		if err := s.Store.ResetFolder(ctx, folder.ID, status.UIDValidity); err != nil {
			return 0, 0, err
		}
		folder.UIDValidity, folder.LastUID = status.UIDValidity, 0
	}
	if status.Messages == 0 || (status.UIDNext != 0 && folder.LastUID+1 >= status.UIDNext) {
		return 0, 0, s.Store.TouchFolder(ctx, folder.ID)
	}

	batchSize := s.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	var (
		metas  []store.MessageMeta
		locs   []store.Location
		maxUID uint32
	)
	flush := func() error {
		if len(metas) == 0 {
			return nil
		}
		n, err := s.Store.SaveBatch(ctx, folder.ID, maxUID, metas, locs)
		if err != nil {
			return err
		}
		added += n
		metas, locs = metas[:0], locs[:0]
		return nil
	}

	sink := func(r io.Reader) (storedBody, error) {
		blob, created, err := s.Blobs.Put(r)
		return storedBody{blob, created}, err
	}
	err = imapsync.FetchAfter(conn, folder.LastUID, sink, func(m imapsync.Message[storedBody]) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		fetched++
		meta, err := s.metaFor(ctx, m.Body)
		if err != nil {
			return err
		}
		metas = append(metas, meta)
		locs = append(locs, store.Location{
			FolderID: folder.ID, UIDValidity: folder.UIDValidity, UID: m.UID,
			Flags: m.Flags, InternalDate: m.InternalDate,
		})
		maxUID = max(maxUID, m.UID)
		if len(metas) >= batchSize {
			return flush()
		}
		return nil
	})
	if err != nil {
		return fetched, added, err
	}
	if err := flush(); err != nil {
		return fetched, added, err
	}
	log.Debug("folder synced", "fetched", fetched, "new", added)
	return fetched, added, nil
}

// metaFor builds message metadata, parsing headers only for messages not yet
// known to the database.
func (s *Syncer) metaFor(ctx context.Context, b storedBody) (store.MessageMeta, error) {
	meta := store.MessageMeta{SHA256: b.blob.SHA256, Size: b.blob.Size, StoredPath: b.blob.Path}
	if !b.created {
		exists, err := s.Store.MessageExists(ctx, b.blob.SHA256)
		if err != nil {
			return meta, err
		}
		if exists {
			return meta, nil // metadata is already stored; ON CONFLICT ignores this row
		}
	}
	f, err := s.Blobs.Open(b.blob.Path)
	if err != nil {
		return meta, err
	}
	defer func() { _ = f.Close() }()
	h := ParseHeaders(f)
	meta.MessageID, meta.Subject, meta.From, meta.SentAt = h.MessageID, h.Subject, h.From, h.Date
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return meta, err
	}
	meta.BodyText = mime.IndexText(f)
	return meta, nil
}

// Reindex extracts body text for messages archived before full-text search
// existed. It processes batches until none are left and returns how many
// messages it indexed. It is safe to interrupt and rerun.
func Reindex(ctx context.Context, st *store.Store, blobs *blobstore.Store, log *slog.Logger) (int, error) {
	total := 0
	for {
		batch, err := st.ListUnindexed(ctx, 200)
		if err != nil {
			return total, err
		}
		if len(batch) == 0 {
			return total, nil
		}
		for _, m := range batch {
			if err := ctx.Err(); err != nil {
				return total, err
			}
			text, err := indexBlob(blobs, m.StoredPath)
			if err != nil {
				// Keep going: mark it as indexed with empty text so a missing
				// or unreadable file does not block the rest forever.
				log.Warn("cannot read message for indexing", "sha256", m.SHA256, "err", err)
			}
			if err := st.SetBodyText(ctx, m.SHA256, text); err != nil {
				return total, err
			}
			total++
		}
		log.Info("reindex progress", "indexed", total)
	}
}

func indexBlob(blobs *blobstore.Store, path string) (string, error) {
	f, err := blobs.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	return mime.IndexText(f), nil
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
