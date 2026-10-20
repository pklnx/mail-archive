package archive

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/store"
)

// A fill copies an IMAP mailbox into an smtp account: once, the mail that
// was there before journaling started (#89), or on a schedule, what shows
// up in the mailbox later and never passed the journal (a recurring fill,
// #112). It writes into the journal folder (store.SMTPFolder), whatever the
// IMAP folder, so a message filed in two IMAP folders is stored once. The
// journal copy and the mailbox copy of one message differ in their
// Received: lines, so the archive cannot tell them apart by content; the
// fill decides by INTERNALDATE and leaves out messages the account already
// received over SMTP (same Message-ID). Its locations have no envelope,
// which tells them from journaled ones.
//
// A one-time fill imports what is older than its cut-off. A recurring
// fill imports a message once it is RecurringGrace old, so that a journal
// copy still in the mail server's queue counts; younger messages wait for
// the next run. It goes through each IMAP folder by UID, like a sync, and
// ignores moves and deletions in the mailbox: a moved message is the same
// message, already in the journal folder.

// FillWindow is how long after the cut-off messages are still imported
// (unless journaled), for clock skew and late deliveries.
const FillWindow = time.Hour

// RecurringGrace is how old a message must be before a recurring fill
// imports it: its journal copy may still be in the mail server's queue.
const RecurringGrace = 2 * time.Hour

// futureSlack: a recurring fill takes a message dated more than this in
// the future for old (a server with a wrong clock), so that it cannot hold
// back its folder for ever.
const futureSlack = 24 * time.Hour

// ErrFillBusy is returned when the account is being synced, filled or
// removed by another process.
var ErrFillBusy = errors.New("the account is being synced, filled or removed")

// ErrFillSkipped is returned for a run of a recurring fill whose account
// is disabled or removed: the fill stays as it is.
var ErrFillSkipped = errors.New("the account is disabled or removed")

// errFillCancelled stops a fill that was cancelled meanwhile.
var errFillCancelled = errors.New("the fill was cancelled")

// fillVerdict is what a fill does with one message.
type fillVerdict int

const (
	fillImport fillVerdict = iota
	fillAfterCutoff
	fillJournaled
	fillWaiting // younger than RecurringGrace: for the next run
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

// recurringVerdict applies the rule of a recurring fill: a message the
// account received over SMTP is left out, even a young one; any other is
// imported once it is RecurringGrace old (also without a Message-ID: a
// duplicate beats a gap) and waits until then. A date more than a day in
// the future counts as old.
func recurringVerdict(internalDate, now time.Time, journaled bool) fillVerdict {
	switch {
	case journaled:
		return fillJournaled
	case internalDate.After(now.Add(futureSlack)):
		return fillImport
	case now.Sub(internalDate) < RecurringGrace:
		return fillWaiting
	default:
		return fillImport
	}
}

// watermark moves a folder's LastUID during one pass, in UID order: over
// every decided message until the first one that is held (it waits, or its
// body did not come back), and no further in that pass. The messages from
// there on are decided again by the next run; imported ones are found by
// the pre-check without their bodies.
type watermark struct{ held bool }

func (w *watermark) advance(last *uint32, uid uint32, hold bool) {
	if hold {
		w.held = true
	}
	if !w.held {
		*last = max(*last, uid)
	}
}

// advanceBatch moves the watermark over a batch of headers in UID order:
// a message is held when it waits, or when its body was requested but did
// not come back (the server lost it meanwhile, or skipped it), so that the
// next run asks for it again.
func (w *watermark) advanceBatch(last *uint32, chunk []imapsync.Header, waiting, requested, fetched map[uint32]bool) {
	for _, h := range chunk {
		w.advance(last, h.UID, waiting[h.UID] || requested[h.UID] && !fetched[h.UID])
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
	// Now returns the current time (default time.Now), for tests: a
	// recurring fill measures the age of messages from it.
	Now func() time.Time
}

func (f *Filler) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Run takes the account's sync lock and runs or resumes a fill (RunLocked).
// An active recurring fill between runs starts its next run, unless its
// account is disabled or removed (ErrFillSkipped). It fails with
// ErrFillBusy while the lock is held elsewhere or another fill of the
// account runs.
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
	if fill.KeepRunning && fill.Status != store.FillRunning {
		a, err := f.Store.GetAccount(ctx, fill.AccountID)
		if errors.Is(err, store.ErrNotFound) || err == nil && (a.RemovedAt != nil || !a.Enabled) {
			return fill, ErrFillSkipped
		}
		if err != nil {
			return nil, err
		}
		switch err := f.Store.StartFillRun(ctx, fill.ID, f.now()); {
		case errors.Is(err, store.ErrFillRunning):
			return nil, ErrFillBusy
		case err != nil:
			return fill, err
		}
	}
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
	if fill.KeepRunning {
		// A recurring fill waits while its account is disabled: no run, no
		// failure, no change of its health. (A removed account's fills were
		// stopped with it.)
		a, err := f.Store.GetAccount(ctx, fill.AccountID)
		if errors.Is(err, store.ErrNotFound) || err == nil && (a.RemovedAt != nil || !a.Enabled) {
			return fill, ErrFillSkipped
		}
		if err != nil {
			return nil, err
		}
	}
	log := f.logger().With("fill", fill.ID, "account_id", fill.AccountID)
	now := f.now() // a recurring fill's messages are as old as at the start
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
		if got.Recurring() && got.Status != store.FillCancelled {
			log.Info("fill run ended", "status", got.Status)
		} else {
			log.Info("fill ended", "status", got.Status)
		}
		return got, nil
	}

	a, err := f.Store.GetAccount(ctx, fill.AccountID)
	if errors.Is(err, store.ErrNotFound) || err == nil && (a.RemovedAt != nil || a.Kind != store.KindSMTP) {
		return end(store.FillFailed, errors.New("the account was removed"))
	}
	if err != nil {
		return nil, err
	}
	if fill.Recurring() && fill.FolderPrefix != "" {
		// A fill from before #112 switched to recurring: its mail moves into
		// the journal folder as locations, before the first run's scan.
		n, err := f.Store.ConvertLegacyFill(ctx, fill)
		if err != nil {
			if ctx.Err() != nil {
				return fill, ctx.Err()
			}
			return end(store.FillFailed, fmt.Errorf("copy the fill's folders into %s: %w", store.SMTPFolder, err))
		}
		if n > 0 {
			log.Info("fill's folders copied into the journal folder", "locations", n)
		}
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
	// A recurring fill picks up new folders, but a trash or spam folder
	// only if the user confirmed it: it has a row from the start, or the
	// included folders name it.
	filled := map[string]bool{}
	for _, ff := range fill.Folders {
		filled[ff.SourceName] = true
	}
	var selected []string
	unconfirmed := []string{}
	for _, src := range folders {
		if !FolderSelected(src.Name, fill.Included, fill.Excluded) {
			continue
		}
		if role := src.SpecialUse(); fill.Recurring() && (role == imapsync.RoleJunk || role == imapsync.RoleTrash) &&
			!filled[src.Name] && !containsFold(fill.Included, src.Name) {
			log.Warn("new trash or spam folder skipped; not confirmed", "folder", src.Name)
			unconfirmed = append(unconfirmed, src.Name)
			continue
		}
		selected = append(selected, src.Name)
	}
	if fill.Recurring() {
		if err := f.Store.SetFillUnconfirmed(ctx, fill.ID, unconfirmed); err != nil {
			if ctx.Err() != nil {
				return fill, ctx.Err()
			}
			return end(store.FillFailed, err)
		}
	}
	var folderErrs []error
	for _, name := range selected {
		err := f.fillFolder(ctx, conn, fill, now, name, log.With("folder", name))
		switch {
		case errors.Is(err, errFillCancelled):
			return end(store.FillCancelled, nil)
		case ctx.Err() != nil:
			return fill, ctx.Err()
		case err != nil:
			log.Error("fill of a folder failed", "err", err)
			folderErrs = append(folderErrs, fmt.Errorf("%s: %w", name, err))
		}
	}
	if len(folderErrs) > 0 {
		return end(store.FillPartial, errors.Join(folderErrs...))
	}
	return end(store.FillOK, nil)
}

// fillFolder copies what the fill's rule imports from one IMAP folder,
// one transaction per batch of headers, with the progress.
func (f *Filler) fillFolder(ctx context.Context, conn *imapsync.Conn, fill *store.Fill, now time.Time, source string,
	log *slog.Logger) (err error) {
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
	// The watermark needs UID order, which servers do not promise.
	slices.SortFunc(headers, func(a, b imapsync.Header) int { return cmp.Compare(a.UID, b.UID) })
	ff.Error = ""
	ff.Counts.Waiting = 0 // a snapshot of this pass
	var wm watermark
	batch := f.BatchSize
	if batch <= 0 {
		batch = DefaultBatchSize
	}
	if len(headers) == 0 {
		ff.Done = true
		_, err := f.Store.SaveFillBatch(ctx, ff, nil, nil)
		return err
	}
	for start := 0; start < len(headers); start += batch {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := headers[start:min(start+batch, len(headers))]
		if err := f.fillBatch(ctx, conn, fill, ff, &wm, now, chunk, start+len(chunk) == len(headers), log); err != nil {
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
		"journaled", c.Journaled, "present", c.Present, "waiting", c.Waiting)
	return nil
}

// fillBatch decides about a batch of headers, fetches the messages to
// import and commits them with the progress in one transaction. The check
// for messages the folder already holds here only saves work: deliveries
// write into the journal folder meanwhile, so SaveFillBatch checks again
// under the folder's lock and numbers the new locations. A recurring fill
// first leaves out, without fetching them, messages it imported before
// (same Message-ID, INTERNALDATE and size): moved or seen again after a
// rescan.
func (f *Filler) fillBatch(ctx context.Context, conn *imapsync.Conn, fill *store.Fill, ff *store.FillFolder, wm *watermark,
	now time.Time, chunk []imapsync.Header, last bool, log *slog.Logger) error {
	ids := make([]string, len(chunk))
	for i, h := range chunk {
		ids[i] = ParseHeaders(bytes.NewReader(h.MessageID)).MessageID
	}
	journaled, err := f.Store.JournaledMessageIDs(ctx, fill.AccountID, ids)
	if err != nil {
		return err
	}
	recurring := fill.Recurring()
	var imported map[store.ImportedKey]bool
	if recurring {
		if imported, err = f.Store.FillImported(ctx, fill.ID, ids); err != nil {
			return err
		}
	}
	next := *ff // the progress if this batch commits
	var fetch []uint32
	waiting, requested, fetched := map[uint32]bool{}, map[uint32]bool{}, map[uint32]bool{}
	for i, h := range chunk {
		next.Counts.Scanned++
		isJournaled := ids[i] != "" && journaled[ids[i]]
		var v fillVerdict
		if recurring {
			if h.InternalDate.After(now.Add(futureSlack)) && !isJournaled {
				log.Warn("message dated in the future; taken for old", "uid", h.UID, "internal_date", h.InternalDate)
			}
			v = recurringVerdict(h.InternalDate, now, isJournaled)
		} else {
			v = verdict(h.InternalDate, *fill.Cutoff, isJournaled)
		}
		switch {
		case v == fillAfterCutoff:
			next.Counts.AfterCutoff++
		case v == fillJournaled:
			next.Counts.Journaled++
		case v == fillWaiting:
			next.Counts.Waiting++
		case recurring && ids[i] != "" && imported[store.NewImportedKey(ids[i], h.InternalDate, h.Size)]:
			next.Counts.Present++
		default:
			fetch = append(fetch, h.UID)
			requested[h.UID] = true
		}
		waiting[h.UID] = v == fillWaiting
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
		fetched[m.UID] = true
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
		meta, err := BuildMeta(ctx, f.Store, f.Blobs, m.Body.blob, m.Body.created)
		if err != nil {
			return err
		}
		metas = append(metas, meta)
		locs = append(locs, store.Location{FolderID: ff.FolderID, Flags: storedFlags(m.Flags), InternalDate: m.InternalDate})
		next.Counts.Imported++
		return nil
	})
	if err != nil {
		return err
	}
	wm.advanceBatch(&next.LastUID, chunk, waiting, requested, fetched)
	next.Done = last
	if _, err := f.Store.SaveFillBatch(ctx, &next, metas, locs); err != nil {
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
