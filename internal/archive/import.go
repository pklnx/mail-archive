package archive

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/textproto"
	"os"
	"path/filepath"
	"time"

	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/mailbox"
	"github.com/pklnx/mail-archive/internal/store"
)

// DefaultMaxMessageSize is the largest message an import stores.
const DefaultMaxMessageSize = 256 << 20

// ImportFolders lists the folders of an import source. With folder set, a
// source with one folder gets that name and a source with several gets it as
// a prefix ("folder/<name>").
func ImportFolders(format, path, folder string) ([]mailbox.SourceFolder, []mailbox.Skipped, error) {
	var (
		folders []mailbox.SourceFolder
		skipped []mailbox.Skipped
		err     error
	)
	switch format {
	case mailbox.KindMbox:
		folders, skipped, err = mailbox.ListMbox(path)
	case mailbox.KindMaildir:
		folders, skipped, err = mailbox.ListMaildir(path)
	case mailbox.KindEML:
		folders, skipped, err = mailbox.ListEML(path)
	default:
		return nil, nil, fmt.Errorf("unknown format %q (use mbox, maildir or eml)", format)
	}
	if err != nil {
		return nil, nil, err
	}
	if len(folders) == 0 {
		return nil, skipped, fmt.Errorf("no %s folders found in %s", format, path)
	}
	if folder != "" {
		if err := mailbox.CheckFolderName(folder); err != nil {
			return nil, nil, err
		}
	}
	for i := range folders {
		switch {
		case folder == "" && folders[i].Name == "":
			// Only ListEML leaves names empty: files directly in path.
			return nil, nil, fmt.Errorf(".eml files directly in %s need --folder NAME", path)
		case folder == "":
		case len(folders) == 1 || folders[i].Name == "":
			folders[i].Name = folder
		default:
			folders[i].Name = folder + "/" + folders[i].Name
		}
		if err := mailbox.CheckFolderName(folders[i].Name); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", folders[i].Path, err)
		}
	}
	return folders, skipped, nil
}

// Importer stores messages from mbox files and Maildirs in an import
// account, the same way a sync stores messages from IMAP.
type Importer struct {
	Store          *store.Store
	Blobs          *blobstore.Store
	Logger         *slog.Logger
	BatchSize      int
	MaxMessageSize int64 // default DefaultMaxMessageSize
	Now            func() time.Time
	// Progress is called after every committed batch.
	Progress func(ImportFolderResult)
}

// ImportFolderResult counts what an import did in one folder.
type ImportFolderResult struct {
	Folder  string
	Read    int // messages read from the source
	New     int // messages new to the archive
	Added   int // new locations in the folder
	Present int // already in the folder from an earlier import
	Skipped int // too large or unreadable
}

// ImportResult is the outcome of an import.
type ImportResult struct {
	Status  string // ok, partial or failed
	Folders []ImportFolderResult
}

// Total sums the folder results.
func (r *ImportResult) Total() ImportFolderResult {
	var t ImportFolderResult
	for _, f := range r.Folders {
		t.Read += f.Read
		t.New += f.New
		t.Added += f.Added
		t.Present += f.Present
		t.Skipped += f.Skipped
	}
	return t
}

// errTooLarge marks a message over MaxMessageSize.
var errTooLarge = errors.New("message too large")

// sourceError is a failure to read the source, which skips one message.
type sourceError struct{ err error }

func (e *sourceError) Error() string { return e.err.Error() }
func (e *sourceError) Unwrap() error { return e.err }

// sourceReader marks read errors of the source and enforces the size limit.
type sourceReader struct {
	r    io.Reader
	left int64
}

func (s *sourceReader) Read(p []byte) (int, error) {
	if s.left <= 0 {
		var one [1]byte
		if n, _ := s.r.Read(one[:]); n > 0 {
			return 0, errTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > s.left {
		p = p[:s.left]
	}
	n, err := s.r.Read(p)
	s.left -= int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, &sourceError{err}
	}
	return n, err
}

// Import stores the messages of the folders in an import account. It holds
// the account's sync lock, so a second import of the account fails with
// ErrSyncRunning, and records the run like a sync. A re-run adds only what
// is not in the folders yet. Batches committed before an error stay.
func (im *Importer) Import(ctx context.Context, acc *store.Account, folders []mailbox.SourceFolder) (*ImportResult, error) {
	if acc.Kind != store.KindImport {
		return nil, errors.New("not an import account")
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

	run, err := im.Store.StartSyncRun(ctx, acc.ID)
	if err != nil {
		return nil, err
	}
	res := &ImportResult{}
	started := im.now()
	log := im.logger().With("account", acc.Name)
	runErr := im.importAll(ctx, fresh, folders, run, res, started, log)
	t := res.Total()
	switch {
	case runErr != nil:
		res.Status = "failed"
		run.Error = runErr.Error()
	case t.Skipped > 0:
		res.Status = "partial"
		run.Error = fmt.Sprintf("%d message(s) skipped", t.Skipped)
	default:
		res.Status = "ok"
	}
	run.Status, run.MessagesFetched, run.MessagesNew = res.Status, t.Read, t.New
	if err := im.Store.FinishSyncRun(context.WithoutCancel(ctx), run); err != nil {
		log.Error("record import run", "err", err)
	}
	log.Info("import finished", "status", res.Status, "read", t.Read, "new", t.New, "added", t.Added,
		"present", t.Present, "skipped", t.Skipped, "duration", im.now().Sub(started).Round(time.Millisecond))
	return res, runErr
}

func (im *Importer) importAll(ctx context.Context, acc *store.Account, folders []mailbox.SourceFolder,
	run *store.SyncRun, res *ImportResult, started time.Time, log *slog.Logger) error {
	for _, sf := range folders {
		res.Folders = append(res.Folders, ImportFolderResult{Folder: sf.Name})
		fr := &res.Folders[len(res.Folders)-1]
		err := im.importFolder(ctx, acc, sf, fr, started, log.With("folder", sf.Name), func() {
			t := res.Total()
			run.MessagesFetched, run.MessagesNew = t.Read, t.New
			if err := im.Store.UpdateSyncRunProgress(ctx, run); err != nil {
				log.Debug("record import progress", "err", err)
			}
		})
		if err != nil {
			return fmt.Errorf("%s: %w", sf.Name, err)
		}
	}
	return nil
}

func (im *Importer) importFolder(ctx context.Context, acc *store.Account, sf mailbox.SourceFolder, fr *ImportFolderResult,
	started time.Time, log *slog.Logger, progress func()) error {
	folder, err := im.Store.GetOrCreateFolder(ctx, acc.ID, sf.Name)
	if err != nil {
		return err
	}
	if folder.UIDValidity == 0 {
		v := started.Unix()
		if v < 1 || v > math.MaxUint32 {
			return fmt.Errorf("start time %v does not fit a UIDVALIDITY", started)
		}
		folder.UIDValidity = uint32(v) //nolint:gosec // range checked above
		if err := im.Store.ResetFolder(ctx, folder.ID, folder.UIDValidity); err != nil {
			return err
		}
	}
	nextUID := int64(folder.LastUID) + 1
	inBatch := map[string]bool{}
	var w *folderWriter
	w = newFolderWriter(im.Store, folder.ID, im.BatchSize, func(int) {
		clear(inBatch)
		fr.New = w.added
		progress()
		if im.Progress != nil {
			im.Progress(*fr)
		}
		log.Info("import progress", "read", fr.Read, "added", fr.Added)
	})
	store1 := func(body io.Reader, m sourceMessage) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		fr.Read++
		where := m.logAttrs()
		src := &sourceReader{r: body, left: im.maxSize()}
		blob, created, err := im.Blobs.Put(mailbox.NewCRLFReader(src))
		var se *sourceError
		switch {
		case errors.Is(err, errTooLarge):
			fr.Skipped++
			log.Warn("message skipped: larger than the limit", append(where, "limit", im.maxSize())...)
			return nil
		case errors.As(err, &se):
			fr.Skipped++
			log.Warn("message skipped: cannot read it", append(where, "err", se.err)...)
			return nil
		case err != nil:
			return err
		}
		if inBatch[blob.SHA256] {
			fr.Present++
			return nil
		}
		present, err := im.Store.LocationInFolder(ctx, folder.ID, blob.SHA256)
		if err != nil {
			return err
		}
		if present {
			fr.Present++
			return nil
		}
		if nextUID > math.MaxUint32 {
			return errors.New("the folder has no UIDs left")
		}
		flags, date := m.flags, m.date
		if m.kind == mailbox.KindMbox {
			flags, err = im.statusFlags(blob)
			if err != nil {
				return err
			}
		}
		meta, err := BuildMeta(ctx, im.Store, im.Blobs, blob, created)
		if err != nil {
			return err
		}
		if m.kind == mailbox.KindEML {
			if date, err = im.headerDate(meta, blob, m.date); err != nil {
				return err
			}
		}
		inBatch[blob.SHA256] = true
		fr.Added++
		loc := store.Location{FolderID: folder.ID, UIDValidity: folder.UIDValidity, UID: uint32(nextUID), Flags: flags, InternalDate: date} //nolint:gosec // checked above
		nextUID++
		return w.add(ctx, meta, loc)
	}

	switch sf.Kind {
	case mailbox.KindMbox:
		err = im.readMbox(sf.Path, func(m *mailbox.ScannedMessage, index int) error {
			return store1(m.Body, sourceMessage{kind: mailbox.KindMbox, date: mailbox.ParseFromDate(m.From), index: index, offset: m.Offset})
		})
	case mailbox.KindMaildir:
		err = im.readMaildir(sf.Path, log, func(f mailbox.MaildirFile, index int) error {
			file, err := os.Open(f.Path) //nolint:gosec // a file of the import source
			if err != nil {
				fr.Read++
				fr.Skipped++
				log.Warn("message skipped: cannot open it", "index", index, "err", err)
				return nil
			}
			defer func() { _ = file.Close() }()
			return store1(file, sourceMessage{kind: mailbox.KindMaildir, flags: f.Flags, date: f.Date, index: index})
		})
	case mailbox.KindEML:
		err = im.readEML(sf.Path, func(f mailbox.EMLFile, index int) error {
			m := sourceMessage{kind: mailbox.KindEML, date: f.Date, index: index, file: filepath.Base(f.Path)}
			if f.Size == 0 {
				fr.Read++
				fr.Skipped++
				log.Warn("message skipped: empty file", m.logAttrs()...)
				return nil
			}
			file, err := os.Open(f.Path) //nolint:gosec // a file of the import source
			if err != nil {
				fr.Read++
				fr.Skipped++
				log.Warn("message skipped: cannot open it", append(m.logAttrs(), "err", err)...)
				return nil
			}
			defer func() { _ = file.Close() }()
			return store1(file, m)
		})
	default:
		err = fmt.Errorf("unknown source kind %q", sf.Kind)
	}
	if err == nil {
		err = w.flush(ctx)
	}
	fr.New = w.added
	return err
}

// sourceMessage is where a message comes from and what the source says
// about it.
type sourceMessage struct {
	kind   string
	flags  []string
	date   time.Time // the source's date; for EML the fallback
	index  int
	offset int64  // mbox only
	file   string // EML only: the file name within its folder
}

// logAttrs names the message in log lines, never its content.
func (m sourceMessage) logAttrs() []any {
	switch m.kind {
	case mailbox.KindMbox:
		return []any{"index", m.index, "offset", m.offset}
	case mailbox.KindEML:
		return []any{"file", m.file}
	}
	return []any{"index", m.index}
}

func (im *Importer) readEML(dir string, fn func(mailbox.EMLFile, int) error) error {
	files, err := mailbox.EMLFiles(dir)
	if err != nil {
		return err
	}
	for i, f := range files {
		if err := fn(f, i); err != nil {
			return err
		}
	}
	return nil
}

// headerDate is the internal date of an EML message: its Date header, else
// the file's time. BuildMeta parsed the headers of a message new to the
// archive; for one the archive already holds, they are read from the blob.
func (im *Importer) headerDate(meta store.MessageMeta, blob blobstore.Blob, fallback time.Time) (time.Time, error) {
	if meta.SentAt != nil {
		return *meta.SentAt, nil
	}
	f, err := im.Blobs.Open(blob.Path)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = f.Close() }()
	if h := ParseHeaders(io.LimitReader(f, maxHeaderBytes)); h.Date != nil {
		return *h.Date, nil
	}
	return fallback, nil
}

func (im *Importer) readMbox(path string, fn func(*mailbox.ScannedMessage, int) error) error {
	f, err := os.Open(path) //nolint:gosec // a file of the import source
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	s := mailbox.NewMboxScanner(f)
	for i := 0; ; i++ {
		m, err := s.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(m, i); err != nil {
			return err
		}
	}
}

func (im *Importer) readMaildir(dir string, log *slog.Logger, fn func(mailbox.MaildirFile, int) error) error {
	files, skipped, err := mailbox.MaildirFiles(dir)
	if err != nil {
		return err
	}
	for _, s := range skipped {
		log.Debug("skipped", "path", s.Path, "reason", s.Reason)
	}
	for i, f := range files {
		if err := fn(f, i); err != nil {
			return err
		}
	}
	return nil
}

// statusFlags reads the Status and X-Status headers of a stored message.
func (im *Importer) statusFlags(blob blobstore.Blob) ([]string, error) {
	f, err := im.Blobs.Open(blob.Path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	h, err := textproto.NewReader(bufio.NewReader(io.LimitReader(f, maxHeaderBytes))).ReadMIMEHeader()
	if err != nil && len(h) == 0 {
		return nil, nil // no readable header: no flags
	}
	return mailbox.StatusFlags(h.Get("Status"), h.Get("X-Status")), nil
}

func (im *Importer) maxSize() int64 {
	if im.MaxMessageSize > 0 {
		return im.MaxMessageSize
	}
	return DefaultMaxMessageSize
}

func (im *Importer) now() time.Time {
	if im.Now != nil {
		return im.Now()
	}
	return time.Now()
}

func (im *Importer) logger() *slog.Logger {
	if im.Logger != nil {
		return im.Logger
	}
	return slog.Default()
}
