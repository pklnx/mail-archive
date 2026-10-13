package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/store"
)

// A fill copies an IMAP mailbox once into an smtp account: the mail that was
// there before journaling started (#89). The journal copy and the mailbox
// copy of one message differ in their Received: lines, so the archive cannot
// tell them apart by content; the fill decides by INTERNALDATE and leaves
// out messages the account already received over SMTP (same Message-ID).

// FillWindow is how long after the cut-off messages are still imported
// (unless journaled), for clock skew and late deliveries.
const FillWindow = time.Hour

// ErrFillBusy is returned when the account is being synced, filled or
// removed by another process.
var ErrFillBusy = errors.New("the account is being synced, filled or removed")

// errFillCancelled stops a fill that was cancelled meanwhile.
var errFillCancelled = errors.New("the fill was cancelled")

// fillVerdict is what a fill does with one message.
type fillVerdict int

const (
	fillImport fillVerdict = iota
	fillAfterCutoff
	fillJournaled
)

// verdict applies the cut-off rule: a message from an hour after the
// cut-off on is left to journaling; an earlier one is imported unless the
// account received it over SMTP.
func verdict(internalDate, cutoff time.Time, journaled bool) fillVerdict {
	switch {
	case !internalDate.Before(cutoff.Add(FillWindow)):
		return fillAfterCutoff
	case journaled:
		return fillJournaled
	default:
		return fillImport
	}
}

// A fill's password is sealed with its own associated data, so it cannot be
// moved into an account row or back.
func fillPasswordContext(fillID int64) []byte {
	return []byte("smtp-fill-password:id:" + strconv.FormatInt(fillID, 10))
}

// SealFillPassword encrypts the IMAP password of a fill.
func SealFillPassword(sealer *crypto.Sealer, fillID int64, password string) ([]byte, error) {
	enc, err := sealer.Seal([]byte(password), fillPasswordContext(fillID))
	if err != nil {
		return nil, err
	}
	if check, err := sealer.Open(enc, fillPasswordContext(fillID)); err != nil || !bytes.Equal(check, []byte(password)) {
		return nil, errors.New("encrypting the password failed")
	}
	return enc, nil
}

// OpenFillPassword decrypts the IMAP password of a running fill.
func OpenFillPassword(sealer *crypto.Sealer, f *store.Fill) (string, error) {
	if len(f.PasswordEnc) == 0 {
		return "", errors.New("the fill has no stored password")
	}
	pw, err := sealer.Open(f.PasswordEnc, fillPasswordContext(f.ID))
	if err != nil {
		return "", fmt.Errorf("decrypt the fill's password: %w", err)
	}
	return string(pw), nil
}

// Filler runs fills.
type Filler struct {
	Store     *store.Store
	Blobs     *blobstore.Store
	Sealer    *crypto.Sealer
	Logger    *slog.Logger
	BatchSize int
	// Dial connects to the IMAP server; defaults to imapsync.Dial.
	Dial func(context.Context, imapsync.Config) (*imapsync.Conn, error)
	// Progress, if set, is called after each committed batch.
	Progress func(store.FillFolder)
}

// Run takes the account's sync lock and runs or resumes a fill (RunLocked).
// It fails with ErrFillBusy while the lock is held elsewhere.
func (f *Filler) Run(ctx context.Context, fillID int64) (*store.Fill, error) {
	fill, err := f.Store.GetFill(ctx, fillID)
	if err != nil {
		return nil, err
	}
	unlock, ok, err := f.Store.TryLockSyncForWrite(ctx, fill.AccountID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrFillBusy
	}
	defer unlock()
	return f.RunLocked(ctx, fillID)
}

// RunLocked runs or resumes a fill; the caller holds the account's sync
// lock for writing (TryLockSyncForWrite), so no sync, removal or other fill
// of the account runs meanwhile. Journaling goes on: receiving takes no
// sync lock. It returns the fill as it ended. When ctx is cancelled, the
// fill stays running with its password and the context's error is
// returned: the caller decides whether a later start resumes it.
func (f *Filler) RunLocked(ctx context.Context, fillID int64) (*store.Fill, error) {
	fill, err := f.Store.GetFill(ctx, fillID)
	if err != nil {
		return nil, err
	}
	if fill.Status != store.FillRunning {
		return fill, store.ErrFillNotRunning
	}
	log := f.logger().With("fill", fill.ID, "account_id", fill.AccountID)
	end := func(status string, cause error) (*store.Fill, error) {
		text := ""
		if cause != nil {
			text = cause.Error()
		}
		err := f.Store.FinishFill(context.WithoutCancel(ctx), fill.ID, status, text)
		if err != nil && !errors.Is(err, store.ErrFillNotRunning) {
			return nil, err
		}
		got, gerr := f.Store.GetFill(context.WithoutCancel(ctx), fill.ID)
		if gerr != nil {
			return nil, gerr
		}
		log.Info("fill ended", "status", got.Status)
		return got, nil
	}

	a, err := f.Store.GetAccount(ctx, fill.AccountID)
	if errors.Is(err, store.ErrNotFound) || err == nil && (a.RemovedAt != nil || a.Kind != store.KindSMTP) {
		return end(store.FillFailed, errors.New("the account was removed"))
	}
	if err != nil {
		return nil, err
	}
	password, err := OpenFillPassword(f.Sealer, fill)
	if err != nil {
		return end(store.FillFailed, err)
	}
	dial := f.Dial
	if dial == nil {
		dial = imapsync.Dial
	}
	conn, err := dial(ctx, imapsync.Config{
		Host: fill.Host, Port: fill.Port, TLSMode: string(fill.TLSMode), Username: fill.Username, Password: password,
	})
	if err != nil {
		if ctx.Err() != nil {
			return fill, ctx.Err()
		}
		return end(store.FillFailed, err)
	}
	defer func() { _ = conn.Close() }()
	folders, err := conn.ListFolders()
	if err != nil {
		if ctx.Err() != nil {
			return fill, ctx.Err()
		}
		return end(store.FillFailed, err)
	}
	var folderErrs []error
	for _, src := range folders {
		if !FolderSelected(src.Name, fill.Included, fill.Excluded) {
			continue
		}
		err := f.fillFolder(ctx, conn, fill, src.Name, log.With("folder", src.Name))
		switch {
		case errors.Is(err, errFillCancelled):
			return end(store.FillCancelled, nil)
		case ctx.Err() != nil:
			return fill, ctx.Err()
		case err != nil:
			log.Error("fill of a folder failed", "err", err)
			folderErrs = append(folderErrs, fmt.Errorf("%s: %w", src.Name, err))
		}
	}
	if len(folderErrs) > 0 {
		return end(store.FillPartial, errors.Join(folderErrs...))
	}
	return end(store.FillOK, nil)
}

// fillFolder copies what the cut-off rule imports from one IMAP folder,
// one transaction per batch of headers, with the progress.
func (f *Filler) fillFolder(ctx context.Context, conn *imapsync.Conn, fill *store.Fill, source string, log *slog.Logger) (err error) {
	ff, err := f.Store.FillFolder(ctx, fill, source)
	if err != nil {
		return err
	}
	if ff.Done {
		return nil
	}
	defer func() {
		// Keep the folder's error for the report; the progress so far stays.
		if err != nil && !errors.Is(err, errFillCancelled) && ctx.Err() == nil {
			ff.Error = err.Error()
			if uerr := f.Store.UpdateFillFolder(context.WithoutCancel(ctx), ff); uerr != nil {
				log.Error("record the folder's error", "err", uerr)
			}
		}
	}()
	status, err := conn.Examine(source)
	if err != nil {
		return err
	}
	if ff.SourceUIDValidity != status.UIDValidity {
		if ff.SourceUIDValidity != 0 {
			log.Warn("UIDVALIDITY changed, scanning the folder again", "old", ff.SourceUIDValidity, "new", status.UIDValidity)
		}
		ff.SourceUIDValidity, ff.LastUID = status.UIDValidity, 0
	}
	var headers []imapsync.Header
	if err := imapsync.ListHeaders(conn, ff.LastUID, func(h imapsync.Header) error {
		headers = append(headers, h)
		return ctx.Err()
	}); err != nil {
		return err
	}
	ff.Error = ""
	batch := f.BatchSize
	if batch <= 0 {
		batch = DefaultBatchSize
	}
	if len(headers) == 0 {
		ff.Done = true
		_, err := f.Store.SaveFillBatch(ctx, ff, ff.TargetLastUID, nil, nil)
		return err
	}
	for start := 0; start < len(headers); start += batch {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := headers[start:min(start+batch, len(headers))]
		if err := f.fillBatch(ctx, conn, fill, ff, chunk, start+len(chunk) == len(headers)); err != nil {
			return err
		}
		if f.Progress != nil {
			f.Progress(*ff)
		}
		st, err := f.Store.FillStatus(ctx, fill.ID)
		if err != nil {
			return err
		}
		if st == store.FillCancelled {
			return errFillCancelled
		}
	}
	c := ff.Counts
	log.Info("folder filled", "scanned", c.Scanned, "imported", c.Imported, "after_cutoff", c.AfterCutoff,
		"journaled", c.Journaled, "present", c.Present)
	return nil
}

// fillBatch decides about a batch of headers, fetches the messages to
// import and commits them with the progress in one transaction.
func (f *Filler) fillBatch(ctx context.Context, conn *imapsync.Conn, fill *store.Fill, ff *store.FillFolder,
	chunk []imapsync.Header, last bool) error {
	ids := make([]string, len(chunk))
	for i, h := range chunk {
		ids[i] = ParseHeaders(bytes.NewReader(h.MessageID)).MessageID
	}
	journaled, err := f.Store.JournaledMessageIDs(ctx, fill.AccountID, ids)
	if err != nil {
		return err
	}
	next := *ff // the progress if this batch commits
	var fetch []uint32
	for i, h := range chunk {
		next.Counts.Scanned++
		switch verdict(h.InternalDate, fill.Cutoff, ids[i] != "" && journaled[ids[i]]) {
		case fillAfterCutoff:
			next.Counts.AfterCutoff++
		case fillJournaled:
			next.Counts.Journaled++
		default:
			fetch = append(fetch, h.UID)
		}
		next.LastUID = max(next.LastUID, h.UID)
	}
	var (
		metas []store.MessageMeta
		locs  []store.Location
		seen  = map[string]bool{}
	)
	sink := func(r io.Reader) (storedBody, error) {
		blob, created, err := f.Blobs.Put(r)
		return storedBody{blob, created}, err
	}
	err = imapsync.FetchUIDs(conn, fetch, sink, func(m imapsync.Message[storedBody]) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		sha := m.Body.blob.SHA256
		if seen[sha] {
			next.Counts.Present++
			return nil
		}
		seen[sha] = true
		present, err := f.Store.LocationInFolder(ctx, ff.FolderID, sha)
		if err != nil {
			return err
		}
		if present {
			next.Counts.Present++
			return nil
		}
		uid := int64(ff.TargetLastUID) + int64(len(locs)) + 1
		if uid > math.MaxUint32 {
			return errors.New("the folder has no UIDs left")
		}
		meta, err := BuildMeta(ctx, f.Store, f.Blobs, m.Body.blob, m.Body.created)
		if err != nil {
			return err
		}
		metas = append(metas, meta)
		locs = append(locs, store.Location{
			FolderID: ff.FolderID, UIDValidity: ff.TargetUIDValidity, UID: uint32(uid), //nolint:gosec // range checked above
			Flags: storedFlags(m.Flags), InternalDate: m.InternalDate,
		})
		next.Counts.Imported++
		return nil
	})
	if err != nil {
		return err
	}
	next.Done = last
	target := ff.TargetLastUID + uint32(len(locs)) //nolint:gosec // checked against MaxUint32 above
	if _, err := f.Store.SaveFillBatch(ctx, &next, target, metas, locs); err != nil {
		return err
	}
	*ff = next
	return nil
}

func (f *Filler) logger() *slog.Logger {
	if f.Logger != nil {
		return f.Logger
	}
	return slog.Default()
}
