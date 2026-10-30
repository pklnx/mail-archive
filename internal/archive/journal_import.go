package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/mailbox"
	"github.com/pklnx/mail-archive/internal/store"
)

// ImportJournal stores the messages of the folders in the journal folder of
// an smtp account, with a fill's rules (#152): next to what arrives over
// SMTP, numbered after it under the folder's row lock, without an envelope
// and naming the import imp. A message the folder holds already counts as
// Present; one whose Message-ID the account received over SMTP is left out
// and counts as Journaled. The source folders are merged; their names only
// label the counts.
//
// Copies of the same bytes count one by one, in one batch as in two (#306):
// each copy of a journaled message counts as Journaled, each copy of one
// retention deleted from the folder as Deleted, and of a new message the
// first is Added and the others Present. Retention decides per hash: two
// copies with different dates (mbox From_ lines) both count as Deleted if
// either is old enough, as in a fill batch.
//
// It holds the account's sync lock for writing, which keeps fills, syncs
// and removal out, while deliveries go on; a busy lock is ErrSyncRunning. A
// re-run adds only what is missing. Batches committed before an error stay.
// No sync run is recorded: smtp accounts have none.
//
// The Message-ID check runs before the batch takes the folder's lock, like
// a fill's: a delivery with the same Message-ID but other bytes that
// arrives in between gives two copies, never a gap.
func (im *Importer) ImportJournal(ctx context.Context, acc *store.Account, imp *store.SMTPImport,
	folders []mailbox.SourceFolder) (*ImportResult, error) {
	if acc.Kind != store.KindSMTP {
		return nil, errors.New("not an smtp account")
	}
	if imp == nil || imp.ID == 0 || imp.AccountID != acc.ID {
		return nil, errors.New("the import is not one of this account")
	}
	unlock, ok, err := im.Store.TryLockSyncForWrite(ctx, acc.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrSyncRunning
	}
	defer unlock()
	fresh, err := im.Store.GetAccount(ctx, acc.ID)
	if errors.Is(err, store.ErrNotFound) || err == nil && fresh.RemovedAt != nil {
		return nil, ErrAccountRemoved
	}
	if err != nil {
		return nil, err
	}
	if fresh.Kind != store.KindSMTP {
		return nil, errors.New("not an smtp account")
	}
	folderID, err := im.Store.JournalFolderID(ctx, fresh.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("the account has no folder %s", store.SMTPFolder)
	}
	if err != nil {
		return nil, err
	}

	res := &ImportResult{}
	started := im.now()
	log := im.logger().With("account", fresh.Name, "import", imp.ID)
	var runErr error
	for _, sf := range folders {
		res.Folders = append(res.Folders, ImportFolderResult{Folder: sf.Name})
		fr := &res.Folders[len(res.Folders)-1]
		j := &journalImport{im: im, accountID: fresh.ID, folderID: folderID, importID: imp.ID, fr: fr, log: log.With("folder", sf.Name)}
		if err := j.run(ctx, sf); err != nil {
			runErr = fmt.Errorf("%s: %w", sf.Name, err)
			break
		}
	}
	t := res.Total()
	switch {
	case runErr != nil:
		res.Status = "failed"
	case t.Skipped > 0:
		res.Status = "partial"
	default:
		res.Status = "ok"
	}
	log.Info("import finished", "status", res.Status, "read", t.Read, "new", t.New, "added", t.Added,
		"present", t.Present, "journaled", t.Journaled, "skipped", t.Skipped, "duration", im.now().Sub(started).Round(time.Millisecond))
	return res, runErr
}

// journalImport is ImportJournal in one source folder.
type journalImport struct {
	im                            *Importer
	accountID, folderID, importID int64
	fr                            *ImportFolderResult
	log                           *slog.Logger
	batch                         []journalPending
}

// journalPending is a message waiting for the Message-ID check.
type journalPending struct {
	blob      blobstore.Blob
	created   bool
	messageID string
	loc       store.Location
}

func (j *journalImport) run(ctx context.Context, sf mailbox.SourceFolder) error {
	err := j.im.readSource(sf, j.fr, j.log, func(body io.Reader, m sourceMessage) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return j.add(ctx, body, m)
	})
	if err != nil {
		return err
	}
	return j.flush(ctx)
}

func (j *journalImport) add(ctx context.Context, body io.Reader, m sourceMessage) error {
	blob, created, ok, err := j.im.put(body, m, j.fr, j.log)
	if !ok || err != nil {
		return err
	}
	present, err := j.im.Store.LocationInFolder(ctx, j.folderID, blob.SHA256)
	if err != nil {
		return err
	}
	if present {
		j.fr.Present++
		return nil
	}
	// Always from the message itself: BuildMeta leaves the Message-ID
	// empty for a message the archive already holds.
	h, err := j.im.blobHeaders(blob)
	if err != nil {
		return err
	}
	flags, date := m.flags, m.date
	switch m.kind {
	case mailbox.KindMbox:
		if flags, err = j.im.statusFlags(blob); err != nil {
			return err
		}
	case mailbox.KindEML:
		if h.Date != nil {
			date = *h.Date
		}
	}
	j.batch = append(j.batch, journalPending{
		blob: blob, created: created, messageID: h.MessageID,
		loc: store.Location{FolderID: j.folderID, Flags: flags, InternalDate: date},
	})
	if len(j.batch) >= j.im.batchSize() {
		return j.flush(ctx)
	}
	return nil
}

// flush leaves out the journaled messages of the batch and stores the rest.
func (j *journalImport) flush(ctx context.Context) error {
	if len(j.batch) == 0 {
		return nil
	}
	ids := make([]string, len(j.batch))
	for i, p := range j.batch {
		ids[i] = p.messageID
	}
	journaled, err := j.im.Store.JournaledMessageIDs(ctx, j.accountID, ids)
	if err != nil {
		return err
	}
	var (
		metas []store.MessageMeta
		locs  []store.Location
	)
	for _, p := range j.batch {
		if p.messageID != "" && journaled[p.messageID] {
			j.fr.Journaled++
			if p.created {
				// A file only this import wrote: the sweep removes it
				// unless a message row refers to it by then.
				if err := j.im.Store.ScheduleBlobRemoval(ctx, p.blob.SHA256, time.Now()); err != nil {
					return err
				}
			}
			continue
		}
		meta, err := BuildMeta(ctx, j.im.Store, j.im.Blobs, p.blob, p.created)
		if err != nil {
			return err
		}
		metas = append(metas, meta)
		locs = append(locs, p.loc)
	}
	if len(metas) > 0 {
		res, present, err := j.im.Store.SaveJournalImportBatch(ctx, j.folderID, j.importID, metas, locs)
		if err != nil {
			return err
		}
		j.fr.New += res.New
		j.fr.Deleted += res.Skipped
		j.fr.Present += present
		j.fr.Added += len(metas) - res.Skipped - present
	}
	j.batch = j.batch[:0]
	if j.im.Progress != nil {
		j.im.Progress(*j.fr)
	}
	j.log.Info("import progress", "read", j.fr.Read, "added", j.fr.Added, "journaled", j.fr.Journaled)
	return nil
}

// blobHeaders parses the headers of a stored message.
func (im *Importer) blobHeaders(blob blobstore.Blob) (Headers, error) {
	f, err := im.Blobs.Open(blob.Path)
	if err != nil {
		return Headers{}, err
	}
	defer func() { _ = f.Close() }()
	return ParseHeaders(io.LimitReader(f, maxHeaderBytes)), nil
}

func (im *Importer) batchSize() int {
	if im.BatchSize > 0 {
		return im.BatchSize
	}
	return DefaultBatchSize
}
